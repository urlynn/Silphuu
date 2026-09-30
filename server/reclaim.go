package main

// reclaim.go — the reclamation registry: every class of bytes this node holds, and the record
// that decides whether a name under it is still referenced.
//
// A sweep walks each registered root and deletes what no record references. The record is the
// only judge: this file never asks whether a byte looks unused, and it never compares against
// the peer. Two nodes hold the same records because records travel over the event channel, so
// each node reclaiming its own copy from its own records is what makes a deletion converge.
//
// A class answers only when its record can be read. "Cannot answer" and "nothing is referenced"
// are different statements: an album that is genuinely empty makes its bytes garbage, while an
// album record that cannot be read says nothing at all about them.
//
// A root is a class of assets, never a source tree. A file under src/ is named by a template as
// a string, so the set of names in use is not something any record can enumerate — and a spare
// file there is a legitimate thing for a site to have, not an orphan. Nothing under src/ may be
// registered here; the guard in reclaim_test.go refuses it.

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// reclaimGrace keeps a freshly written file out of a sweep.
//
// An upload lands before the record that names it exists: the editor uploads the image, the
// person then submits the body that references it. A sweep judging by references alone would
// delete the upload in between. The window is read from mtime, which survives a restart, so it
// holds across the gap a restart would otherwise open.
const reclaimGrace = 24 * time.Hour

// resourceKind is one class of reclaimable bytes.
type resourceKind struct {
	// Name names the class in reports and on the admin page.
	Name string
	// Root is the byte root. Every file under it belongs to this class.
	Root func() string
	// Key maps a file's path relative to Root to the name a record uses. Two formats of one
	// image share a key, which is what lets a record name a base rather than a file.
	Key func(rel string) string
	// Live lists the keys a record still references. ok is false when this node cannot read the
	// record, and the class is then left alone.
	Live func() (live map[string]bool, ok bool)
	// Triggers names the event types whose arrival can change Live. A class with none is swept
	// only on an explicit request.
	Triggers []string
}

// stemKey drops the extension, so every format of one image shares a key.
func stemKey(rel string) string {
	return strings.TrimSuffix(rel, filepath.Ext(rel))
}

// stickerThumbKey maps a sticker thumbnail back to the sticker it belongs to: both
// "<group>/preview/<base>.avif" and "<group>/dynamic/<base>.avif" key to "<group>/<base>".
func stickerThumbKey(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) != 3 {
		return stemKey(rel)
	}
	return parts[0] + "/" + stemKey(parts[2])
}

// fontSubsetKey maps every variant of one family's subset to that family. The site, home,
// admin and comment subsets of one family are four files with four names, and all four are
// referenced by the same family entry.
func fontSubsetKey(rel string) string {
	base := stemKey(filepath.Base(rel))
	for _, suffix := range []string{"-home", "-admin", "-cmt"} {
		if trimmed := strings.TrimSuffix(base, suffix); trimmed != base {
			return trimmed
		}
	}
	return base
}

// resourceKinds is the registry. A new class of bytes is one entry here: its root, how a file
// under it is keyed, and the record that answers for it.
var resourceKinds = []resourceKind{
	{
		Name:     "comment-image",
		Root:     func() string { return DirImageComment },
		Key:      stemKey,
		Live:     liveBodyImages(PrefixStatic + "/image/comment"),
		Triggers: []string{EvCommentAdded, EvCommentDeleted},
	},
	{
		Name: "post-image",
		Root: func() string { return DirImagePost },
		Key:  stemKey,
		Live: liveBodyImages(PrefixStatic + "/image/post"),
		Triggers: []string{
			EvCommentAdded, EvCommentDeleted, EvPostSaved, EvPostRemoved,
		},
	},
	{
		Name:     "photo-source",
		Root:     func() string { return DirPhoto },
		Key:      stemKey,
		Live:     livePhotoIDs,
		Triggers: []string{EvPhotoAdded, EvPhotoRemoved},
	},
	{
		Name:     "photo-blur-thumb",
		Root:     func() string { return DirThumbPhotoBlur },
		Key:      stemKey,
		Live:     livePhotoIDs,
		Triggers: []string{EvPhotoAdded, EvPhotoRemoved},
	},
	{
		Name:     "background",
		Root:     func() string { return DirBackground },
		Key:      stemKey,
		Live:     liveBackgroundNames,
		Triggers: []string{EvBgAdded, EvBgRemoved},
	},
	{
		Name: "sticker-thumb",
		Root: func() string { return DirThumbStickerRoot },
		Key:  stickerThumbKey,
		Live: liveStickerNames,
	},
	{
		Name:     "font-subset",
		Root:     func() string { return DirFont },
		Key:      fontSubsetKey,
		Live:     liveFontFamilies,
		Triggers: []string{EvFontPresets},
	},
}

// liveBodyImages reads the image references out of every comment and post body, keeping the
// ones under one directory.
//
// The comment projection is rebuilt from the event log at startup, so a node whose log is not
// up cannot answer for these bytes at all; neither can one that cannot read the posts its
// bodies live in.
func liveBodyImages(dir string) func() (map[string]bool, bool) {
	return func() (map[string]bool, bool) {
		if localOrigin == "" || !isDir(RootPosts) {
			return nil, false
		}
		live := map[string]bool{}
		for _, pid := range commentPostIDs() {
			for _, c := range commentsForPost(pid, "oldest") {
				addImageBases(live, dir, c.Content+" "+c.Avatar)
			}
		}
		for _, p := range idxSnapshot(true) {
			addImageBases(live, dir, p.Body)
		}
		return live, true
	}
}

// addImageBases records the base names of the images one body references under dir.
func addImageBases(live map[string]bool, dir, body string) {
	for _, m := range imgRe.FindAllStringSubmatch(body, -1) {
		if m[1] == dir {
			live[m[2]] = true
		}
	}
}

// livePhotoIDs answers for every class derived from an album record.
//
// A node with no album tree holds none of those bytes and cannot answer for them. It says so
// rather than reporting an empty album, which would make every thumbnail garbage.
func livePhotoIDs() (map[string]bool, bool) {
	if !isDir(DirPhoto) {
		return nil, false
	}
	live := map[string]bool{}
	for _, p := range loadPhotos() {
		live[p.ID] = true
	}
	return live, true
}

// liveBackgroundNames answers from the background records, keyed by the file name each record
// points at.
func liveBackgroundNames() (map[string]bool, bool) {
	cfg, ok := loadStoredBgConfig()
	if !ok {
		return nil, false
	}
	live := map[string]bool{}
	for _, b := range cfg.Backgrounds {
		if base := stemKey(filepath.Base(b.Src)); base != "." && base != "" {
			live[base] = true
		}
	}
	return live, true
}

// liveStickerNames answers from the sticker tree itself: the tree is the record, and a group
// without a gif folder is not published, so its thumbnails are not referenced either.
func liveStickerNames() (map[string]bool, bool) {
	if !isDir(DirSticker) {
		return nil, false
	}
	live := map[string]bool{}
	eachStickerSource(func(group, base, _ string) {
		live[group+"/"+base] = true
	})
	return live, true
}

// liveFontFamilies answers from font_map.json, which is the set the admin panel offers.
//
// The boundary is that set and nothing narrower. A family the panel can select keeps every one
// of its subset files, because the panel previews them all: whether a source is on disk, and
// which family the current preset happens to use, are both beside the point. What is reclaimed
// is a family the map no longer names — the files left behind by a face that was removed.
func liveFontFamilies() (map[string]bool, bool) {
	fontMap := loadFontMap()
	if len(fontMap) == 0 {
		return nil, false
	}
	live := make(map[string]bool, len(fontMap))
	for fam := range fontMap {
		live[fam] = true
	}
	return live, true
}

// reclaimClassReport is what one sweep did to one class.
type reclaimClassReport struct {
	Name      string
	Root      string
	Reclaimed []string
	// Skipped says why the class was left alone, and is empty when it was swept.
	Skipped string
}

// reclaimReport is what one sweep did, class by class.
type reclaimReport struct {
	Classes []reclaimClassReport
}

// reclaimed counts the files one sweep removed.
func (r reclaimReport) reclaimed() int {
	n := 0
	for _, c := range r.Classes {
		n += len(c.Reclaimed)
	}
	return n
}

// reclaimOrphans sweeps the named classes, or every class when none are named, and deletes
// what no record references.
func reclaimOrphans(only ...string) reclaimReport {
	var rep reclaimReport
	for _, kind := range resourceKinds {
		if len(only) > 0 && !contains(only, kind.Name) {
			continue
		}
		rep.Classes = append(rep.Classes, reclaimClass(kind))
	}
	return rep
}

// reclaimClass walks one root and removes the files whose key no record lists.
//
// Dot entries are not content: they are skipped rather than judged, so a stray state directory
// or a desktop metadata file under a root is neither reclaimed nor taken for a record.
func reclaimClass(kind resourceKind) reclaimClassReport {
	rep := reclaimClassReport{Name: kind.Name, Root: kind.Root()}
	if !isDir(rep.Root) {
		rep.Skipped = "no such root on this node"
		return rep
	}
	live, ok := kind.Live()
	if !ok {
		rep.Skipped = "the record could not be read"
		return rep
	}
	cutoff := time.Now().Add(-reclaimGrace)

	err := filepath.Walk(rep.Root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if p != rep.Root && strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		rel, err := filepath.Rel(rep.Root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if live[kind.Key(rel)] || info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(p); err != nil {
			log.Printf("reclaim: %s/%s: %v", kind.Name, rel, err)
			return nil
		}
		rep.Reclaimed = append(rep.Reclaimed, rel)
		return nil
	})
	if err != nil {
		// A partial view is not a basis for deletion, so the walk stops and the class is
		// reported: what was removed before the error is already gone.
		rep.Skipped = err.Error()
	}
	return rep
}

// logReclaimReport writes one sweep to the log, including the classes it left alone. A class
// that answers "cannot read the record" every time is the one an operator has to see.
func logReclaimReport(rep reclaimReport) {
	for _, c := range rep.Classes {
		if c.Skipped != "" {
			log.Printf("reclaim: %s (%s): skipped: %s", c.Name, c.Root, c.Skipped)
			continue
		}
		log.Printf("reclaim: %s: %d file(s) reclaimed", c.Name, len(c.Reclaimed))
	}
}

// reclaimSummary renders a sweep for the administrator, one line per class.
func reclaimSummary(rep reclaimReport) string {
	lines := make([]string, 0, len(rep.Classes))
	for _, c := range rep.Classes {
		switch {
		case c.Skipped != "":
			lines = append(lines, c.Name+": skipped ("+c.Skipped+")")
		case len(c.Reclaimed) > 0:
			lines = append(lines, c.Name+": "+strings.Join(c.Reclaimed, " "))
		default:
			lines = append(lines, c.Name+": nothing")
		}
	}
	return strings.Join(lines, "; ")
}

// The rescan queue.
//
// A record change is the moment a class's live set can move, so an applied event queues the
// classes it can affect. The queue exists rather than a direct call because the sweep walks
// the filesystem and the event is applied while the log lock is held: the walk happens on the
// worker, and a burst of events coalesces into one pass.
var (
	reclaimQueueMu sync.Mutex
	reclaimQueued  = map[string]bool{}
	reclaimWake    = make(chan struct{}, 1)
)

// reclaimKinds queues a rescan of the named classes.
func reclaimKinds(names ...string) {
	added := false
	reclaimQueueMu.Lock()
	for _, n := range names {
		if !reclaimQueued[n] {
			reclaimQueued[n] = true
			added = true
		}
	}
	reclaimQueueMu.Unlock()
	if added {
		select {
		case reclaimWake <- struct{}{}:
		default: // a wake-up is already pending, and one is enough
		}
	}
}

// takeReclaimQueue empties the queue, sorted so two runs read the same way.
func takeReclaimQueue() []string {
	reclaimQueueMu.Lock()
	defer reclaimQueueMu.Unlock()
	if len(reclaimQueued) == 0 {
		return nil
	}
	out := make([]string, 0, len(reclaimQueued))
	for n := range reclaimQueued {
		out = append(out, n)
	}
	sort.Strings(out)
	reclaimQueued = map[string]bool{}
	return out
}

var startReclaimWorkerOnce sync.Once

// startReclaimWorker drains the queue. It is started once, where the event store is opened: a
// second call does nothing, so a test that wants the worker running can simply ask for it.
func startReclaimWorker() {
	startReclaimWorkerOnce.Do(func() {
		go func() {
			for range reclaimWake {
				for {
					batch := takeReclaimQueue()
					if len(batch) == 0 {
						break
					}
					logReclaimReport(reclaimOrphans(batch...))
				}
			}
		}()
	})
}

// reclaimOnEvent queues the classes whose record an applied event can change. The mapping lives
// in the registry, so a new class declares its own triggers and nothing here has to know it.
func reclaimOnEvent(typ string) {
	var names []string
	for _, kind := range resourceKinds {
		if contains(kind.Triggers, typ) {
			names = append(names, kind.Name)
		}
	}
	if len(names) > 0 {
		reclaimKinds(names...)
	}
}
