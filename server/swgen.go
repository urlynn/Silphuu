package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Service Worker content generation number.
// CACHE_VERSION is rendered into render/sw.js (placeholder @GEN@ in src/sw.js).
// The generation number = newest .md mtime under posts/: it changes automatically after
// publishing/editing a post, so on the next navigation the client detects the sw.js byte
// change -> upgrades the SW -> purges old caches -> fresh content.
// Idempotent: recomputed from disk after restart, no reliance on in-memory state.
var (
	contentGenMu    sync.RWMutex
	contentGenVal   string
	contentGenDirty bool
)

// contentGen returns the current content generation number (cached; invalidated by
// invalidateContentGen after publish/delete).
func contentGen() string {
	contentGenMu.RLock()
	v, dirty := contentGenVal, contentGenDirty
	contentGenMu.RUnlock()
	if !dirty && v != "" {
		return v
	}
	contentGenMu.Lock()
	defer contentGenMu.Unlock()
	if !contentGenDirty && contentGenVal != "" {
		return contentGenVal
	}
	contentGenVal = computeContentGen()
	contentGenDirty = false
	return contentGenVal
}

// invalidateContentGen is called after a post is published/edited/deleted, forcing the
// next contentGen() call to recompute.
func invalidateContentGen() {
	contentGenMu.Lock()
	contentGenDirty = true
	contentGenMu.Unlock()
}

func computeContentGen() string {
	var latest time.Time
	dirs := []string{RootPosts, RootTemplates, RootConfig, RootAssets}
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if info.ModTime().After(latest) {
				latest = info.ModTime()
			}
			return nil
		})
	}
	if latest.IsZero() {
		// With no posts, still keep the generation number change-detectable (use boot time)
		return "boot-" + time.Now().UTC().Format("20060102-150405")
	}
	return latest.UTC().Format("20060102150405")
}

// materializeServiceWorker renders src/sw.js into render/sw.js with every placeholder
// resolved.
//
// /sw.js is a static file: a projection publishes render/sw.js verbatim and the web server
// hands it out, so nothing may be left for request time to substitute. It is rewritten
// whenever an input moves — the bundles behind PAGE_RESOURCES, the post index and prefetch
// list, and the content generation itself.
func materializeServiceWorker() error {
	tpl, err := os.ReadFile(srcReadPath("sw.js"))
	if err != nil {
		return fmt.Errorf("read sw.js: %w", err)
	}

	gen := contentGen()
	if os.Getenv("DEV_MODE") == "1" {
		gen = "dev"
	}

	urlsJSON, _ := json.Marshal(buildSiteURLs())
	resJSON, _ := json.Marshal(buildPageResources())
	prefetchJSON, _ := json.Marshal(prefetchPostIDs())
	noCachePrefixes, _ := json.Marshal([]string{
		PrefixAdmin,
		RouteAdminStatus,
		RouteStats,
		PrefixAPI,
		"/debug",
		PrefixAssets + "/image",
		PrefixStatic + "/image",
		PrefixStatic + "/uploads",
		PrefixStatic + "/sticker",
	})

	out := strings.ReplaceAll(string(tpl), "@GEN@", gen)
	out = strings.ReplaceAll(out, `"@SITE_URLS@"`, string(urlsJSON))
	out = strings.ReplaceAll(out, `"@PAGE_RESOURCES@"`, string(resJSON))
	out = strings.ReplaceAll(out, `"@PREFETCH_POSTS@"`, string(prefetchJSON))
	out = strings.ReplaceAll(out, `"@NO_CACHE_PREFIXES@"`, string(noCachePrefixes))

	if err := os.MkdirAll(RootRender, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(RootRender, "sw.js"), []byte(out), 0644)
}

// refreshServiceWorker marks the content generation stale and re-renders render/sw.js, so
// the next /sw.js response carries the new generation and the client drops its caches.
func refreshServiceWorker() {
	invalidateContentGen()
	if err := materializeServiceWorker(); err != nil {
		log.Printf("warn: materializeServiceWorker: %v", err)
	}
}
