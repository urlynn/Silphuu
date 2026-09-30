package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Sticker is a single sticker.
type Sticker struct {
	AVIFURL     string // display canonical: /static/sticker/{group}/avif/{name}.avif
	JXLURL      string // /static/sticker/{group}/jxl/{name}.jxl (fallback, injected on demand by the probe)
	ThumbURL    string // 96x96 static thumbnail path
	AnimURL     string // 96x96 animated thumbnail path (GIF only)
	Name        string // file name without extension
	DisplayName string // readable name (underscores replaced with spaces)
}

// StickerSet is one sticker pack.
type StickerSet struct {
	Name        string    // directory name (group name), used for data-group matching
	DisplayName string    // display name (config "name"; falls back to Name)
	Stickers    []Sticker // all images in this group
}

// stickerConfig is the sticker group config file structure.
type stickerConfig struct {
	Groups []stickerGroupConfig `json:"groups"`
}

type stickerGroupConfig struct {
	Dir   string `json:"dir"`
	Name  string `json:"name"`
	Cover string `json:"cover,omitempty"`
}

// stickerCache is the global in-memory cache, scanned and loaded at startup.
var stickerCache []StickerSet

// shortcodeMap shortcode -> StickerRef
var shortcodeMap map[string]StickerRef

// StickerRef is a shortcode reference.
type StickerRef struct {
	URL string
}

// stickerSourceExt lists the extensions a sticker source may carry at upload. An animated
// PNG is not among them: its extension is the still one, so nothing here can tell an APNG
// from a PNG before it is decoded.
//
// Whatever arrives, gif/ is what the pipeline reads, so a source that is not already a GIF
// is converted on the way in.
var stickerSourceExt = map[string]bool{
	".gif": true, ".jxl": true, ".avif": true,
}

// loadStickerConfig reads config/sticker.json and returns a dir -> group config map.
func loadStickerConfig() (map[string]stickerGroupConfig, []string) {
	configMap := make(map[string]stickerGroupConfig)
	var orderedDirs []string

	data, err := os.ReadFile(FileStickerData)
	if err != nil {
		log.Printf("sticker: no config file (config/sticker.json), using filesystem order: %v", err)
		return configMap, nil
	}

	var cfg stickerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("sticker: config parse error: %v", err)
		return configMap, nil
	}

	for _, g := range cfg.Groups {
		configMap[g.Dir] = g
		orderedDirs = append(orderedDirs, g.Dir)
	}
	return configMap, orderedDirs
}

// stickerPayloadURL returns the URL of one stored sticker file.
//
// Payload URLs are deliberately not hash-addressed: the shortcode pass writes them into
// article HTML, so a changing URL would invalidate every page that mentions the sticker.
// A rebuild keeps the URL and purges it instead — see refreshStickers.
func stickerPayloadURL(group, format, file string) string {
	return PrefixStatic + "/sticker/" + group + "/" + format + "/" + file
}

// stickerIndexPath is where the published sticker index is written.
func stickerIndexPath() string {
	return filepath.Join(RootAssets, "stickers.json")
}

// stickerIndexURL returns the index's URL, hash-addressed so a rebuilt index reaches
// clients without a purge.
func stickerIndexURL() string {
	url := PrefixAssets + "/stickers.json"
	if h, err := computeFileHash(stickerIndexPath()); err == nil {
		return url + "?v=" + h
	}
	return url
}

// eachStickerSource walks the deployment's sticker groups and calls fn for every GIF
// source. A group without a gif/ subfolder is not published, so it is skipped.
func eachStickerSource(fn func(group, base, gifPath string)) {
	dir := DirSticker
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sticker: read dir %s error: %v", dir, err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		group := entry.Name()
		// _pending/ holds unreviewed uploads, so keep them out of the picker.
		if group == "_pending" || strings.HasPrefix(group, ".") {
			continue
		}
		gifDir := filepath.Join(dir, group, "gif")
		files, err := os.ReadDir(gifDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || strings.ToLower(filepath.Ext(f.Name())) != ".gif" {
				continue
			}
			base := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
			fn(group, base, filepath.Join(gifDir, f.Name()))
		}
	}
}

// publishStickerIndex writes the sticker index to disk, empty included: it is the
// published truth for the picker, so the URL has to resolve on every site.
func publishStickerIndex(sets []StickerSet) {
	if sets == nil {
		sets = []StickerSet{}
	}
	data, err := json.Marshal(sets)
	if err != nil {
		log.Printf("sticker: marshal index error: %v", err)
		return
	}
	if err := writeServedFile(stickerIndexPath(), data, 0644); err != nil {
		log.Printf("sticker: write index error: %v", err)
	}
}

// loadStickers scans the deployment's sticker directory and loads only metadata into the
// cache (no thumbnails). The directory is deployment content — the engine ships none — so
// its absence is the normal case, not an error.
//
// A node without the tree is not the authority for the index, and it must not publish an
// empty one over the copy that was synced to it. It still has to READ that copy: the
// shortcode map is built from this cache, so an empty one would render every `:group name:`
// in every article as plain text.
func loadStickers() {
	dir := DirSticker
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sticker: read dir %s error: %v", dir, err)
		}
		// Two cases reach here, and only the directory tells them apart. An existing but
		// empty tree means the site ships no stickers at all — publishing an empty index is
		// then correct, and ReadDir returns no error for it, so it never arrives here. A
		// missing tree means this node is not where the bytes live.
		loadPublishedStickerIndex()
		return
	}

	// Read config
	configMap, orderedDirs := loadStickerConfig()

	// Collect sticker data for all folders first
	type groupData struct {
		displayName string
		stickers    []Sticker
	}
	allGroups := make(map[string]groupData)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		groupName := entry.Name()
		groupDir := filepath.Join(dir, groupName)
		gifDir := filepath.Join(groupDir, "gif")

		gifEntries, err := os.ReadDir(gifDir)
		if err != nil {
			// Strictly read only the gif/ subfolder (no root-level fallback); a group
			// without a gif/ source is not published
			continue
		}

		var stickers []Sticker
		for _, f := range gifEntries {
			if f.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(f.Name()))
			if ext != ".gif" {
				continue // gif is the only source; dual formats are generated by generateStickerDualFormat
			}
			baseName := strings.TrimSuffix(f.Name(), ext)

			// Publish only when both formats exist; skip otherwise (no gif fallback),
			// prompting the admin to hit refresh to regenerate
			avifPath := filepath.Join(groupDir, "avif", baseName+".avif")
			jxlPath := filepath.Join(groupDir, "jxl", baseName+".jxl")
			if !fileExists(avifPath) {
				log.Printf("sticker: %s/%s 缺 avif（点「刷新表情包缓存」补生成），跳过发布", groupName, baseName)
				continue
			}
			if !fileExists(jxlPath) {
				log.Printf("sticker: %s/%s 缺 jxl（点「刷新表情包缓存」补生成），跳过发布", groupName, baseName)
				continue
			}

			stickers = append(stickers, Sticker{
				AVIFURL:     stickerPayloadURL(groupName, "avif", baseName+".avif"),
				JXLURL:      stickerPayloadURL(groupName, "jxl", baseName+".jxl"),
				ThumbURL:    URLThumbSticker(groupName, baseName, "preview"),
				AnimURL:     animThumbURL(groupName, baseName, ext),
				Name:        baseName,
				DisplayName: strings.ReplaceAll(baseName, "_", " "),
			})
		}

		if len(stickers) == 0 {
			continue
		}

		sort.Slice(stickers, func(i, j int) bool {
			return stickers[i].Name < stickers[j].Name
		})

		// Apply the cover config: the file named by cover is moved to the first position
		if cfg, ok := configMap[groupName]; ok && cfg.Cover != "" {
			cover := strings.ToLower(cfg.Cover)
			for i, st := range stickers {
				if strings.ToLower(st.Name) == cover {
					if i > 0 {
						coverSticker := stickers[i]
						stickers = append(stickers[:i], stickers[i+1:]...)
						stickers = append([]Sticker{coverSticker}, stickers...)
					}
					break
				}
			}
		}

		displayName := groupName
		if cfg, ok := configMap[groupName]; ok && cfg.Name != "" {
			displayName = cfg.Name
		}

		allGroups[groupName] = groupData{
			displayName: displayName,
			stickers:    stickers,
		}
	}

	// Output in config order
	var sets []StickerSet
	seen := make(map[string]bool)

	for _, dirName := range orderedDirs {
		if gd, ok := allGroups[dirName]; ok {
			sets = append(sets, StickerSet{
				Name:        dirName,
				DisplayName: gd.displayName,
				Stickers:    gd.stickers,
			})
			seen[dirName] = true
		}
	}

	// Folders not present in the config are appended alphabetically
	var remaining []string
	for name := range allGroups {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		gd := allGroups[name]
		sets = append(sets, StickerSet{
			Name:        name,
			DisplayName: gd.displayName,
			Stickers:    gd.stickers,
		})
	}

	stickerCache = sets
	publishStickerIndex(sets)
	log.Printf("sticker: loaded %d sets, %d total stickers", len(sets), func() int {
		n := 0
		for _, s := range sets {
			n += len(s.Stickers)
		}
		return n
	}())

	// Rebuild the shortcode map
	buildShortcodeMap()
}

// loadPublishedStickerIndex reads the index the node holding the sticker tree published.
//
// A tree is scanned by whoever holds it; every other node reads the result. The index is
// what the picker lists and what the shortcode map is built from, so a node that only
// renders still needs it in full — "no stickers" and "not my tree" are different answers,
// and only the second one is true here.
//
// Nothing is published from this path: writing here would put this node's emptiness over
// the copy it just read.
func loadPublishedStickerIndex() {
	path := stickerIndexPath()
	data, err := os.ReadFile(path)
	if err != nil {
		stickerCache = nil
		log.Printf("sticker: no sticker tree at %s and no published index at %s; the picker stays empty", DirSticker, path)
		return
	}
	var sets []StickerSet
	if err := json.Unmarshal(data, &sets); err != nil {
		stickerCache = nil
		log.Printf("sticker: published index %s is unreadable: %v", path, err)
		return
	}
	stickerCache = sets
	total := 0
	for _, s := range sets {
		total += len(s.Stickers)
	}
	log.Printf("sticker: read the published index at %s (%d sets, %d stickers); this node holds no sticker tree", path, len(sets), total)
	buildShortcodeMap()
}

// buildShortcodeMap builds the shortcode -> URL map from stickerCache.
func buildShortcodeMap() {
	m := make(map[string]StickerRef)
	for _, set := range stickerCache {
		prefix := set.DisplayName
		for _, st := range set.Stickers {
			sc := prefix + "_" + st.Name
			m[sc] = StickerRef{URL: st.AVIFURL}
		}
	}
	shortcodeMap = m
}

// animThumbURL returns the animated thumbnail path; only GIF has one.
func animThumbURL(groupName, baseName, ext string) string {
	if ext == ".gif" {
		return URLThumbSticker(groupName, baseName, "dynamic")
	}
	return ""
}

// needsRegen reports whether the source is newer than the thumbnail (or the thumbnail
// is missing), i.e. whether regeneration is needed.
func needsRegen(srcPath, thumbPath string) bool {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false // source missing, skip
	}
	thumbInfo, err := os.Stat(thumbPath)
	if err != nil {
		return true // thumbnail missing, needs generating
	}
	return srcInfo.ModTime().After(thumbInfo.ModTime())
}

// generateStickerThumbnails generates missing/stale thumbnails (128x128 preview +
// 96x96 dynamic).
//
// It walks the filesystem rather than stickerCache: loadStickers hashes each thumbnail
// URL, so the files have to exist before the cache is built.
func generateStickerThumbnails() {
	if magickCmd == "" {
		log.Printf("sticker: no imagemagick, skip thumbnails")
		return
	}
	eachStickerSource(func(groupName, baseName, srcPath string) {
		previewDir := DirThumbStickerPreview(groupName)
		dynamicDir := DirThumbStickerDynamic(groupName)
		os.MkdirAll(previewDir, 0755)
		os.MkdirAll(dynamicDir, 0755)

		// 1. Static 128x128 preview (~1.5KB, preview)
		previewPath := filepath.Join(previewDir, baseName+".avif")
		if needsRegen(srcPath, previewPath) {
			tmpPNG, _ := os.CreateTemp("", "thumb-*.png")
			pngPath := tmpPNG.Name()
			tmpPNG.Close()
			defer os.Remove(pngPath)

			args := []string{srcPath + "[0]", "-resize", "128x128", pngPath}
			if strings.HasSuffix(magickCmd, "magick") {
				args = append([]string{"convert"}, args...)
			}
			if out, err := exec.Command(magickCmd, args...).CombinedOutput(); err != nil {
				log.Printf("sticker: magick error for %s: %s", baseName, strings.TrimSpace(string(out)))
			} else if avifencBin != "" {
				exec.Command(avifencBin, "-c", "aom", "-s", "6", "-j", "all", "-q", "42", "-a", "aq-mode=1", pngPath, previewPath).Run()
			}
		}

		// 2. Dynamic 96x96 micro-thumbnail: per-frame durations come from the source's
		// original frame rate (%T centiseconds)
		dynamicPath := filepath.Join(dynamicDir, baseName+".avif")
		if needsRegen(srcPath, dynamicPath) {
			tmpDir, _ := os.MkdirTemp("", "thumb-anim-*")
			framePattern := filepath.Join(tmpDir, "frame_%04d.png")
			ffmpeg := toolPath("ffmpeg")
			if out, err := exec.Command(ffmpeg, "-i", srcPath, "-vsync", "0", "-vf", "scale=96:96:flags=area", framePattern).CombinedOutput(); err != nil {
				log.Printf("sticker: ffmpeg error for %s: %s", baseName, strings.TrimSpace(string(out)))
			} else if avifencBin != "" {
				frameFiles, _ := filepath.Glob(filepath.Join(tmpDir, "frame_*.png"))
				sort.Strings(frameFiles)
				if len(frameFiles) > 0 {
					delaysCS := getFrameDelaysCS(srcPath, len(frameFiles))
					encArgs := []string{"-c", "aom", "-s", "6", "-j", "all", "-q", "50", "-a", "aq-mode=1", "--timescale", "100"}
					for i, f := range frameFiles {
						d := 3 // default 30ms
						if i < len(delaysCS) {
							d = delaysCS[i]
						}
						if d < 1 {
							d = 1
						}
						encArgs = append(encArgs, "--duration", strconv.Itoa(d), f)
					}
					encArgs = append(encArgs, dynamicPath)
					exec.Command(avifencBin, encArgs...).Run()
				}
			}
			os.RemoveAll(tmpDir)
		}
	})
	log.Println("sticker: thumbnails ready")
}

// refreshStickers regenerates every derived sticker file, republishes the index and
// invalidates whatever kept its URL while changing.
//
// Order matters: thumbnails have to exist before loadStickers, which hashes their URLs.
//
// Step 5 is the full invalidation, not resetRenderCache: every page embeds the index URL,
// so the whole site is stale — and dropping the served pages without purging their URLs
// leaves the edge holding a one-year copy, while dropping them without a warm leaves the
// served tree with no page files at all (a web server resolves $uri against that tree, so
// an absent page is a 404, not a render-on-demand). invalidateAllCache is the shape that
// carries all three: read the URLs off the pages, drop them, purge them, warm them back.
func refreshStickers() {
	// The regenerated payloads, the rescan and the full invalidation are one change: the pages
	// embed the sticker URLs, so a fingerprint taken between them describes neither generation.
	beginServedWrite()
	defer endServedWrite()
	rewritten := generateStickerDualFormat() // 1. regenerate stale jxl+avif from the gif/ sources
	generateStickerThumbnails()              // 2. regenerate stale thumbnails (hash-addressed, no purge)
	loadStickers()                           // 3. rescan and republish the index (hash-addressed, no purge)
	invalidatePaths(rewritten)               // 4. invalidate the payload URLs that changed under a fixed URL
	invalidateAllCache()                     // 5. every page embeds sticker URLs, so all of them are stale

	// 6. ship the regenerated face. Off the request path, because this is a network round trip
	// and the write lease above must not be held across it.
	if faceProducer() && blobSyncEnabled() {
		go func() {
			if _, err := reconcileFace(); err != nil {
				log.Printf("[blob sync] 贴纸对账失败: %v", err)
			}
		}()
	}
}

// fileExists reports whether the file exists and is not a directory.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// getFrameDurationsMS reads the GIF's raw per-frame delays (seconds) via ffprobe and
// converts to milliseconds; raw per-frame values, no averaging/hardcoding.
func getFrameDurationsMS(input, ffprobeBin string) []int {
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=duration_time", "-of", "csv=p=0", input).CombinedOutput()
	if err != nil {
		return nil
	}
	var ds []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v, e := strconv.ParseFloat(line, 64)
		if e != nil {
			continue
		}
		ms := int(v*1000 + 0.5)
		if ms < 1 {
			ms = 1
		}
		ds = append(ds, ms)
	}
	return ds
}

// generateStickerDualFormat reads source gifs from each group's gif/ subfolder and
// regenerates the avif+jxl of any whose source is newer (no root-level fallback). It
// returns the payload URLs it rewrote — the purge set for refreshStickers.
//
// Parameters copied verbatim from the final sticker pipeline: original resolution, no
// scaling; JXL = magick -coalesce -> cjxl -d 0 -m 1 -e 10 (OOM degrades to -e 9);
// AVIF = ffmpeg -fps_mode passthrough + alpha-safe hqdn3d -> avifenc q70 444 10bit k1
// timescale1000 with per-frame --duration.
func generateStickerDualFormat() []string {
	ff := ffmpegBin
	fp := toolPath("ffprobe")
	av := avifencBin
	cj := cjxlBin
	magick := magickCmd
	if ff == "" || fp == "" || av == "" || cj == "" || magick == "" {
		log.Printf("sticker: 缺少媒体工具，跳过双格式生成")
		return nil
	}
	vf := "format=rgba,split=2[m][a];[m]hqdn3d=2:1:6:6,format=rgba[dn];[a]alphaextract,format=gray[am];[dn][am]alphamerge"
	var rewritten []string
	total := 0

	eachStickerSource(func(groupName, base, gifPath string) {
		groupDir := filepath.Join(DirSticker, groupName)
		avifOut := filepath.Join(groupDir, "avif", base+".avif")
		jxlOut := filepath.Join(groupDir, "jxl", base+".jxl")
		os.MkdirAll(filepath.Dir(avifOut), 0755)
		os.MkdirAll(filepath.Dir(jxlOut), 0755)

		// AVIF (regenerate when the source is newer).
		if needsRegen(gifPath, avifOut) {
			tmp, _ := os.MkdirTemp("", "stk-anim-*")
			framePattern := filepath.Join(tmp, "frame_%04d.png")
			if out, err := exec.Command(ff, "-y", "-i", gifPath, "-fps_mode", "passthrough", "-vf", vf, framePattern).CombinedOutput(); err != nil {
				log.Printf("sticker: ffmpeg fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
			} else {
				durs := getFrameDurationsMS(gifPath, fp)
				frameFiles, _ := filepath.Glob(filepath.Join(tmp, "frame_*.png"))
				sort.Strings(frameFiles)
				nfrm := len(frameFiles)
				encArgs := []string{"-y", "444", "-q", "70", "-s", "4", "--depth", "10", "-k", "1", "--timescale", "1000"}
				if nfrm > 0 && nfrm == len(durs) {
					for i, fr := range frameFiles {
						d := durs[i]
						if d < 1 {
							d = 1
						}
						encArgs = append(encArgs, "--duration", strconv.Itoa(d), fr)
					}
				} else {
					avg := 33
					if len(durs) > 0 {
						sum := 0
						for _, d := range durs {
							sum += d
						}
						avg = sum / len(durs)
					}
					if avg < 1 {
						avg = 33
					}
					for _, fr := range frameFiles {
						encArgs = append(encArgs, "--duration", strconv.Itoa(avg), fr)
					}
					if nfrm != len(durs) {
						log.Printf("sticker: WARN %s/%s 帧数/延时不匹配 (%d vs %d) -> uniform %d ms", groupName, base, nfrm, len(durs), avg)
					}
				}
				encArgs = append(encArgs, avifOut)
				if out, err := exec.Command(av, encArgs...).CombinedOutput(); err != nil {
					log.Printf("sticker: avifenc fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
				} else {
					total++
					rewritten = append(rewritten, stickerPayloadURL(groupName, "avif", base+".avif"))
				}
			}
			os.RemoveAll(tmp)
		}

		// JXL (regenerate when the source is newer; forced coalesce, OOM degrades to -e 9).
		if needsRegen(gifPath, jxlOut) {
			tmp, _ := os.MkdirTemp("", "stk-jxl-*")
			coalesced := filepath.Join(tmp, "coalesced.gif")
			if out, err := exec.Command(magick, gifPath, "-coalesce", coalesced).CombinedOutput(); err != nil {
				log.Printf("sticker: coalesce fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
			} else if _, err := exec.Command(cj, "-d", "0", "-m", "1", "-e", "10", coalesced, jxlOut).CombinedOutput(); err != nil {
				if out2, err2 := exec.Command(cj, "-d", "0", "-m", "1", "-e", "9", coalesced, jxlOut).CombinedOutput(); err2 != nil {
					log.Printf("sticker: cjxl fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out2)))
				} else {
					total++
					rewritten = append(rewritten, stickerPayloadURL(groupName, "jxl", base+".jxl"))
				}
			} else {
				total++
				rewritten = append(rewritten, stickerPayloadURL(groupName, "jxl", base+".jxl"))
			}
			os.RemoveAll(tmp)
		}
	})
	log.Printf("sticker: generateStickerDualFormat done, %d 生成", total)
	return rewritten
}

// handleRefreshStickers POST /admin/refresh-stickers — manually refresh the sticker cache.
func handleRefreshStickers(w http.ResponseWriter, r *http.Request) {
	refreshStickers()
	adminRespond(w, r, uiText("admin.sticker.refreshed"))
}

// stickerNameMaxRunes caps how long a group name and a sticker name may be. Both land in
// a path and in a shortcode, so an unbounded name is an unbounded path.
const stickerNameMaxRunes = 64

// stickerUploadEntry is one image an upload carried: a file the browser posted as-is, or
// a member taken out of a zip. Its path points into the scratch directory, which the
// handler removes before it returns.
type stickerUploadEntry struct {
	path string
	ext  string
	name string
}

// stickerGroupName validates the name the form posted. It becomes the group directory
// verbatim, so it has to stay one path segment, and it has to stay usable as a shortcode:
// :[group]_[sticker]: never resolves with a bracket or a colon in either half.
func stickerGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("empty name")
	}
	if len([]rune(name)) > stickerNameMaxRunes {
		return "", errors.New("name too long")
	}
	if name != filepath.Base(filepath.Clean(name)) || strings.ContainsAny(name, `/\`) {
		return "", errors.New("name is not one path segment")
	}
	if strings.ContainsAny(name, ":[]") {
		return "", errors.New("name carries a shortcode delimiter")
	}
	if strings.HasPrefix(name, ".") || name == "_pending" {
		return "", errors.New("name is reserved")
	}
	return name, nil
}

// sanitizeStickerName drops what a sticker name cannot carry, the same delimiters the
// group name is checked against: the shortcode is built from both halves.
func sanitizeStickerName(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r == '/' || r == '\\' || r == ':' || r == '[' || r == ']':
			b.WriteRune('-')
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// collectStickerUploads flattens an upload into one entry per image and reports how many
// inputs it dropped. A zip is one pack: its members are taken out and their directories
// dropped, so the archive's own layout never decides where a file lands.
func collectStickerUploads(files []*multipart.FileHeader, scratch string) ([]stickerUploadEntry, int) {
	var out []stickerUploadEntry
	dropped := 0

	for _, fh := range files {
		if fh.Size > 256<<20 {
			dropped++
			continue
		}
		src, err := fh.Open()
		if err != nil {
			dropped++
			continue
		}
		ext := strings.ToLower(filepath.Ext(fh.Filename))
		switch {
		case ext == ".zip":
			dropped += addZipStickers(src, fh.Size, scratch, &out)
		case stickerSourceExt[ext]:
			base := strings.TrimSuffix(filepath.Base(fh.Filename), filepath.Ext(fh.Filename))
			dropped += addStickerFile(src, scratch, sanitizeStickerName(base), ext, &out)
		default:
			dropped++
		}
		src.Close()
	}
	return out, dropped
}

// addStickerFile copies one image into the scratch directory and records it.
func addStickerFile(src io.Reader, scratch, name, ext string, out *[]stickerUploadEntry) int {
	if name == "" {
		return 1
	}
	path := uniqueScratchPath(scratch, name, ext)
	dst, err := os.Create(path)
	if err != nil {
		return 1
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return 1
	}
	if err := dst.Close(); err != nil {
		return 1
	}
	*out = append(*out, stickerUploadEntry{path: path, ext: ext, name: name})
	return 0
}

// uniqueScratchPath keeps two uploads of the same name apart, so neither overwrites the
// other before the handler has read them.
func uniqueScratchPath(scratch, name, ext string) string {
	path := filepath.Join(scratch, name+ext)
	for i := 2; ; i++ {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
		path = filepath.Join(scratch, fmt.Sprintf("%s_%d%s", name, i, ext))
	}
}

// addZipStickers takes the images out of one uploaded zip. Directories are dropped: a
// pack is one group, whatever the archive looked like inside.
func addZipStickers(src multipart.File, size int64, scratch string, out *[]stickerUploadEntry) int {
	zr, err := zip.NewReader(src, size)
	if err != nil {
		return 1
	}

	dropped := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		base := filepath.Base(f.Name)
		if base == "." || strings.HasPrefix(base, ".") {
			continue
		}
		// A name that is neither flagged UTF-8 nor valid UTF-8 cannot be read as text, so
		// it is dropped rather than published under a shortcode nobody can type.
		if f.NonUTF8 && !utf8.ValidString(f.Name) {
			log.Printf("sticker: zip member %q is not UTF-8, skipped", f.Name)
			dropped++
			continue
		}
		ext := strings.ToLower(filepath.Ext(base))
		if !stickerSourceExt[ext] {
			dropped++
			continue
		}
		rc, err := f.Open()
		if err != nil {
			dropped++
			continue
		}
		name := sanitizeStickerName(strings.TrimSuffix(base, filepath.Ext(base)))
		dropped += addStickerFile(rc, scratch, name, ext, out)
		rc.Close()
	}
	return dropped
}

// publishStickerUpload moves one pack into the group's gif/ folder, the only source
// folder the pipeline reads, and reports how many landed. A source that is not already a
// GIF becomes a single frame one on the way in: dual formats, thumbnails and the index are
// all written around an animated source.
func publishStickerUpload(group string, entries []stickerUploadEntry) int {
	gifDir := filepath.Join(DirSticker, group, "gif")
	if err := os.MkdirAll(gifDir, 0755); err != nil {
		log.Printf("sticker: mkdir %s failed: %v", gifDir, err)
		return 0
	}

	published := 0
	for _, e := range entries {
		target := filepath.Join(gifDir, e.name+".gif")
		var err error
		if e.ext == ".gif" {
			err = copyStickerFile(e.path, target)
		} else {
			err = convertToGIF(e.path, target)
		}
		if err != nil {
			log.Printf("sticker: %s/%s failed: %v", group, e.name, err)
			continue
		}
		published++
	}
	return published
}

// convertToGIF writes src as a single-frame GIF at dst.
func convertToGIF(src, dst string) error {
	if magickCmd == "" {
		return errNoImageMagick
	}
	args := []string{src, dst}
	if strings.HasSuffix(magickCmd, "magick") {
		args = append([]string{"convert"}, args...)
	}
	if out, err := exec.Command(magickCmd, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func copyStickerFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// stageStickerUpload keeps a visitor's pack outside the published tree until a moderator
// approves it, and returns the staged directory's name. Nothing is converted here: staging
// holds what was uploaded.
func stageStickerUpload(group string, entries []stickerUploadEntry, email string) (string, error) {
	staged := fmt.Sprintf("%s_%s", time.Now().Format("20060102_150405"), group)
	gifDir := filepath.Join(DirStagingSticker, staged, "gif")
	if err := os.MkdirAll(gifDir, 0755); err != nil {
		return staged, err
	}
	for _, e := range entries {
		if err := copyStickerFile(e.path, filepath.Join(gifDir, e.name+e.ext)); err != nil {
			return staged, err
		}
	}
	meta, _ := json.Marshal(map[string]string{
		"name":  group,
		"email": email,
		"time":  time.Now().Format(time.RFC3339),
	})
	return staged, os.WriteFile(filepath.Join(DirStagingSticker, staged, "meta.json"), meta, 0644)
}

// writeStickerUploadResult reports what became of an upload. Published is the fact the
// panel's copy hangs on: an admin upload is on the site already, a visitor's is waiting.
func writeStickerUploadResult(w http.ResponseWriter, published bool, count, dropped int) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":        true,
		"published": published,
		"count":     count,
		"dropped":   dropped,
	})
}

// handleUploadSticker POST /api/sticker/upload — take a sticker pack from the picker.
//
// The two callers get two paths, the way a friend-link application does: an admin upload
// is published into static/sticker/<name>/gif/ and the sticker cache is rebuilt, a visitor
// upload is staged for review and announced by email.
func handleUploadSticker(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, uiText("upload.failed")+": "+err.Error(), http.StatusBadRequest)
		return
	}

	group, err := stickerGroupName(r.FormValue("name"))
	if err != nil {
		http.Error(w, uiText("comment.sticker.err.name"), http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, uiText("comment.sticker.err.no_file"), http.StatusBadRequest)
		return
	}

	scratch, err := os.MkdirTemp("", "sticker-upload-*")
	if err != nil {
		http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(scratch)

	entries, dropped := collectStickerUploads(files, scratch)
	if len(entries) == 0 {
		http.Error(w, uiText("comment.sticker.err.no_image"), http.StatusBadRequest)
		return
	}

	// The address a visitor leaves is recorded with the submission and shown to the owner
	// in the notice. Nothing is ever sent to it: there is no mail path back to the sender.
	email := strings.TrimSpace(r.FormValue("email"))

	if !isAdmin(r) {
		staged, err := stageStickerUpload(group, entries, email)
		if err != nil {
			log.Printf("sticker: stage %s failed: %v", group, err)
			http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
			return
		}
		go sendStickerApplyNotify(stickerEmailData{
			Group:   group,
			Email:   email,
			Count:   len(entries),
			Staged:  staged,
			PageURL: siteBaseURL() + PageDashboard,
		})
		writeStickerUploadResult(w, false, len(entries), dropped)
		return
	}

	published := publishStickerUpload(group, entries)
	if published == 0 {
		// Nothing landed, most likely because the converter is missing: reporting success
		// here would leave the pack absent under an "on the site already" confirmation.
		http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
		return
	}
	refreshStickers()
	writeStickerUploadResult(w, true, published, dropped)
}
