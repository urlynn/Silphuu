package main

// refs.go — file reference counting (in-memory, orphan image cleanup).
//
// A fully in-memory map rebuilt at startup by scanning comment/post content.
// registerFileRef (pre-registered at upload with count 0) / incFileRef /
// decFileRef / delOrphanedFiles keep their original semantics.

import (
	"os"
	"strings"
	"sync"
)

var (
	refMu sync.RWMutex
	refs  = map[string]int{} // site-absolute path (/static/...) -> refcount
)

// rebuildRefs rebuilds refcounts from the in-memory comment projection + post bodies
// (the baseline for orphan detection).
func rebuildRefs() {
	refMu.Lock()
	refs = map[string]int{}
	refMu.Unlock()

	// Comment bodies
	var all []Comment
	for _, pid := range commentPostIDs() {
		all = append(all, sortCommentsSQL(pid, "oldest")...)
	}
	for _, c := range all {
		incFileRefs(commentImgPaths(c.Content, c.Avatar))
	}
	// Post bodies
	for _, p := range idxSnapshot(true) {
		incFileRefs(extractCommentImgPaths(p.Body))
	}
}

// commentPostIDs returns every post_id that has comments (guestboard included as 0).
func commentPostIDs() []int {
	cmMu.RLock()
	defer cmMu.RUnlock()
	ids := make([]int, 0, len(cm.byPost))
	for pid := range cm.byPost {
		ids = append(ids, pid)
	}
	return ids
}

func registerFileRef(path string) {
	if path == "" {
		return
	}
	refMu.Lock()
	if _, ok := refs[path]; !ok {
		refs[path] = 0
	}
	refMu.Unlock()
}

func incFileRef(path string) {
	if path == "" {
		return
	}
	refMu.Lock()
	refs[path]++
	refMu.Unlock()
}

func decFileRef(path string) {
	if path == "" {
		return
	}
	refMu.Lock()
	if refs[path] > 0 {
		refs[path]--
	}
	refMu.Unlock()
}

func incFileRefs(paths []string) {
	for _, p := range paths {
		incFileRef(p)
	}
}

func decFileRefs(paths []string) {
	for _, p := range paths {
		decFileRef(p)
	}
}

// diskPath maps a site path to a local file path (strips the leading /).
func diskPath(sitePath string) string {
	return strings.TrimPrefix(sitePath, "/")
}

// delOrphanedFiles deletes files whose refcount <= 0 and that actually exist on disk.
func delOrphanedFiles() {
	refMu.Lock()
	var orphans []string
	for path, count := range refs {
		if count <= 0 {
			orphans = append(orphans, path)
		}
	}
	for _, path := range orphans {
		delete(refs, path)
	}
	refMu.Unlock()
	for _, path := range orphans {
		fp := diskPath(path)
		if fp != "" {
			if _, err := os.Stat(fp); err == nil {
				os.Remove(fp)
			}
		}
	}
}
