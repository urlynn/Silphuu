package main

// views.go — in-memory views/UV/PV layer.
//
// Design:
//   - Fully in-memory counting: reads never touch storage; a quiet-period timer (30s)
//     with a 60s cap flushes to state/views-<node>.json (atomic write) at most once per
//     window. Never writes when nothing changed.
//   - Cross-node merge: at the end of a dirty window a view_delta event is built and
//     pushed to the peer over the commit channel, merged with LWW.
//   - Post read counts (postViews): seeded once at startup from the stored count.
//   - Lossiness is allowed (up to the flush interval); comments are never affected.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// viewAgg holds one day's PV/UV.
type viewAgg struct {
	PV int64
	UV map[string]bool // deduplicated by ua_hash
}

type viewsState struct {
	mu        sync.RWMutex
	postViews map[int]int64       // per-post read count (persistent semantics)
	daily     map[string]*viewAgg // date -> that day's PV/UV
	totalPV   int64               // cumulative PV (in-memory total, includes seed)
	totalUV   int64               // cumulative UV (in-memory approximation, includes seed)
	dirty     bool
	lastFlush time.Time
	started   bool
}

var vst viewsState

// initViews initializes at startup (idempotent): in-memory state + restore from the views file.
func initViews() {
	vst.mu.Lock()
	vst.postViews = map[int]int64{}
	vst.daily = map[string]*viewAgg{}
	vst.started = true
	vst.lastFlush = time.Now()
	vst.mu.Unlock()

	loadViewsFile()
	go viewsFlusherLoop()
}

// postViewCount returns a post's read count.
func postViewCount(id int) int {
	vst.mu.RLock()
	defer vst.mu.RUnlock()
	if id <= 0 {
		return 0
	}
	return int(vst.postViews[id])
}

// recordView records one page view (beacon). A post page hit increments its read count.
func recordView(path string, uaHash string) int64 {
	if path == "" {
		return 0
	}
	var postID int
	if m := postIDRe.FindStringSubmatch(path); m != nil {
		_, _ = scanInt(m[1], &postID)
	}

	today := time.Now().Format("2006-01-02")
	vst.mu.Lock()
	if vst.daily[today] == nil {
		vst.daily[today] = &viewAgg{UV: map[string]bool{}}
	}
	d := vst.daily[today]
	d.PV++
	vst.totalPV++
	if uaHash != "" {
		if !d.UV[uaHash] {
			d.UV[uaHash] = true
			vst.totalUV++
		}
	}
	var latest int64
	if postID > 0 {
		vst.postViews[postID]++
		latest = vst.postViews[postID]
	}
	vst.dirty = true
	vst.mu.Unlock()
	return latest
}

func scanInt(s string, out *int) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return n, nil
}

// computeStats aggregates from memory (the visitor stats' only counting source).
func computeStats() VisitStats {
	vst.mu.RLock()
	defer vst.mu.RUnlock()
	today := time.Now().Format("2006-01-02")
	var s VisitStats
	if d, ok := vst.daily[today]; ok {
		s.TodayPV = int(d.PV)
		s.TodayUV = len(d.UV)
	}
	s.TotalPV = int(vst.totalPV)
	s.TotalUV = int(vst.totalUV)
	return s
}

// markViewsDirty sets the dirty flag externally (e.g. view_delta merges).
func markViewsDirty() {
	vst.mu.Lock()
	vst.dirty = true
	vst.mu.Unlock()
}

// flushViewsNow atomically persists the views aggregation file (tmp + fsync + rename).
func flushViewsNow() {
	vst.mu.Lock()
	if !vst.dirty {
		vst.mu.Unlock()
		return
	}
	snap := viewsPersistSnapshot()
	vst.dirty = false
	vst.lastFlush = time.Now()
	vst.mu.Unlock()
	writeViewsFile(snap)
}

type viewsPersist struct {
	PostViews map[int]int64  `json:"post_views"`
	Daily     map[string]any `json:"daily"` // date -> {"pv":n,"uv":[hashes]}
	TotalPV   int64          `json:"total_pv"`
	TotalUV   int64          `json:"total_uv"`
	Time      string         `json:"time"`
}

func viewsPersistSnapshot() viewsPersist {
	snap := viewsPersist{PostViews: map[int]int64{}, Daily: map[string]any{}}
	for k, v := range vst.postViews {
		snap.PostViews[k] = v
	}
	for date, d := range vst.daily {
		uvs := make([]string, 0, len(d.UV))
		for h := range d.UV {
			uvs = append(uvs, h)
		}
		snap.Daily[date] = map[string]any{"pv": d.PV, "uv": uvs}
	}
	snap.TotalPV = vst.totalPV
	snap.TotalUV = vst.totalUV
	snap.Time = time.Now().Format(time.RFC3339)
	return snap
}

func writeViewsFile(snap viewsPersist) {
	data, err := json.MarshalIndent(snap, "", " ")
	if err != nil {
		return
	}
	f := viewsFilePath()
	tmp := f + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, f)
}

func viewsFilePath() string {
	n := nodeName()
	return filepath.Join(RootState, "views-"+n+".json")
}

// loadViewsFile restores the last persisted views at startup (missing/corrupt files are fine).
func loadViewsFile() {
	data, err := os.ReadFile(viewsFilePath())
	if err != nil {
		return
	}
	var snap struct {
		PostViews map[int]int64  `json:"post_views"`
		Daily     map[string]any `json:"daily"`
		TotalPV   int64          `json:"total_pv"`
		TotalUV   int64          `json:"total_uv"`
	}
	if json.Unmarshal(data, &snap) != nil {
		return
	}
	vst.mu.Lock()
	defer vst.mu.Unlock()
	if vst.postViews == nil {
		vst.postViews = map[int]int64{}
	}
	for id, v := range snap.PostViews {
		if v > vst.postViews[id] {
			vst.postViews[id] = v // LWW: take the larger (includes the startup seed)
		}
	}
	if vst.totalPV < snap.TotalPV {
		vst.totalPV = snap.TotalPV
	}
	if vst.totalUV < snap.TotalUV {
		vst.totalUV = snap.TotalUV
	}
	// LWW-merge daily
	for date, raw := range snap.Daily {
		if vst.daily[date] == nil {
			vst.daily[date] = &viewAgg{UV: map[string]bool{}}
		}
		if m, ok := raw.(map[string]any); ok {
			if pv, ok := m["pv"].(float64); ok && int64(pv) > vst.daily[date].PV {
				vst.daily[date].PV = int64(pv)
			}
			if uvs, ok := m["uv"].([]any); ok {
				for _, h := range uvs {
					if hs, ok := h.(string); ok {
						vst.daily[date].UV[hs] = true
					}
				}
			}
		}
	}
	// If no cumulative PV exists yet (first boot with no history file), fall back to the
	// sum of post read counts
	if vst.totalPV == 0 {
		for _, v := range vst.postViews {
			vst.totalPV += v
		}
	}
}

// viewsFlusherLoop flushes on the dirty flag + quiet-window timer: writes only on real
// changes; after a change, flushes once the state has been quiet for >=30s.
func viewsFlusherLoop() {
	for {
		time.Sleep(10 * time.Second)
		vst.mu.RLock()
		dirty := vst.dirty
		since := time.Since(vst.lastFlush)
		vst.mu.RUnlock()
		if !dirty {
			continue
		}
		if since >= 30*time.Second {
			flushViewsNow()
			log.Printf("[views] 已落盘")
		}
	}
}
