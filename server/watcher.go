package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// startWatcher watches the source trees (src/js, src/css), posts/, config/, state/ and
// templates/ for changes.
//   - src/ change -> concatAllBundles() rebuilds assets/, then inlined caches are cleared
//     and ISR invalidated (inlined content changed)
//   - posts/ change -> invalidate list caches + clear post caches + re-subset fonts
//   - config/ or state/ change (ui_strings.json, site.json etc.) -> invalidate caches +
//     re-subset fonts (new UI-string characters must join the subset)
func startWatcher() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("warn: fsnotify: %v (file watcher disabled)", err)
		return
	}

	// Recursively add watch directories
	watchRoots := []string{RootPosts, RootConfig, RootState, RootTemplates, RootPublic}
	for _, src := range assetSourceTrees() {
		watchRoots = append(watchRoots, filepath.Join(src, "js"), filepath.Join(src, "css"))
	}
	for _, root := range watchRoots {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || !info.IsDir() {
				return nil
			}
			if e := watcher.Add(path); e != nil {
				log.Printf("warn: fsnotify watch %s: %v", path, e)
			}
			return nil
		})
	}
	// The private root itself, not recursively: the links only need to notice a link source
	// appearing or disappearing, and static/ is one directory link, so nothing inside it
	// ever needs reconciling.
	if e := watcher.Add(homeRoot); e != nil {
		log.Printf("warn: fsnotify watch %s: %v", homeRoot, e)
	}
	watchRoots = append(watchRoots, homeRoot)

	go func() {
		var mu sync.Mutex
		var timer *time.Timer
		var assetChanged, srcChanged, postsChanged, dataChanged, linksChanged bool

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
					continue
				}
				p := filepath.ToSlash(event.Name)
				if strings.HasPrefix(p, filepath.ToSlash(RootPosts)+"/") {
					postsChanged = true
				} else if strings.HasPrefix(p, filepath.ToSlash(RootConfig)+"/") ||
					strings.HasPrefix(p, filepath.ToSlash(RootState)+"/") {
					// site.json / ui_strings.json etc.: new UI-string characters must join the text subset.
					// Only watch JSON config, excluding dynamic event/view/sync state files and .DS_Store.
					base := filepath.Base(p)
					if base == "snapshot.json" || base == "views.json" ||
						base == "sync-state.json" || base == "events.log" ||
						strings.HasPrefix(base, "events.jsonl") {
						continue // high-frequency dynamic files: never trigger font rebuild / cache invalidation
					}
					if filepath.Ext(p) == ".json" {
						dataChanged = true
					}
				} else if p == filepath.ToSlash(RootStatic) ||
					p == filepath.ToSlash(RootPublic) ||
					strings.HasPrefix(p, filepath.ToSlash(RootPublic)+"/") {
					// A link source gained or lost an entry, so the served tree has to follow.
					linksChanged = true
				} else {
					// Sources are materialised into assets/, so any edit under a src/ tree
					// has to re-run the copy — otherwise the served bytes stay stale.
					for _, src := range assetSourceTrees() {
						if strings.HasPrefix(p, filepath.ToSlash(src)+"/") {
							srcChanged = true
							break
						}
					}
					ext := filepath.Ext(p)
					if ext == ".js" || ext == ".css" || ext == ".html" {
						assetChanged = true
					}
				}
				// Debounce 1000ms: let editor multi-file writes and formatters settle
				mu.Lock()
				if timer != nil {
					timer.Stop()
				}
				timer = time.AfterFunc(1000*time.Millisecond, func() {
					mu.Lock()
					defer mu.Unlock()
					if linksChanged {
						runEnsureLinks("link source changed")
						linksChanged = false
					}
					if srcChanged {
						// Rebuild the bundles first; the subsequent rebuildAssets refreshes
						// its inlined content
						if err := concatAllBundles(); err != nil {
							log.Printf("[watcher warn] concatAllBundles: %v", err)
						}
						// Icons under src/ are cached once parsed, too.
						resetSiteIconCache()
						srcChanged = false
					}
					if assetChanged {
						log.Printf("watcher: JS/CSS/HTML changed, reloading in-memory templates + invalidating all ISR caches")
						_ = generateThemesJSON()
						if err := reloadTemplatesSafe(); err != nil {
							log.Printf("[watcher warn] 模板处于中间编辑态 (保持旧版本正常运行): %v", err)
						}
						rebuildAssets()
						invalidateAllCache()
						assetChanged = false
					}
					if postsChanged {
						log.Printf("watcher: posts/ changed, invalidating caches + re-subsetting fonts")
						// Drop only the pages whose post is gone, before the invalidation warms
						// the rest back. An edited post keeps its URL, so its page is
						// overwritten by that warm rather than deleted first — and the warm is
						// asynchronous, so a removal after it would take pages it had just
						// written. Removing the whole directory takes every post page down for
						// the width of a full re-render.
						// Drops the pages whose post is gone and queues each one's URL: the
						// list invalidation below covers the pages that listed the post, not
						// the post's own page.
						if n := prunePostPages(); n > 0 {
							log.Printf("watcher: pruned %d post page(s) with no post", n)
						}
						invalidateListCache()
						runFontSubset()
						postsChanged = false
					}
					if dataChanged {
						log.Printf("watcher: config/ or state/ changed, reloading UI strings + ink vars + re-subsetting fonts")
						loadUIStrings()
						// state/fonts.json (the font workshop's output) is watched too.
						// The ink role variables are derived from it and cached in inkAssetsVal —
						// without this the watcher reloads everything else but keeps serving the
						// OLD shifts, so a hand-edited font state looks like "the edit did nothing"
						// (and is only fixed by a restart). Same class of staleness as the admin
						// subset right below, which is why that one already rebuilds here.
						rebuildInkAssets()
						runFontSubset()
						runHomeSubsetAndWBN()
						// Preview fixtures (config/preview_fixtures.json) also live under config/ —
						// without rebuilding the admin subset, fixture changes would need a restart.
						runAdminFontSubset()
						invalidateAllCache()
						dataChanged = false
					}
				})
				mu.Unlock()

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("fsnotify error: %v", err)
			}
		}
	}()

	log.Printf("fsnotify: watching %s", strings.Join(watchRoots, ", "))
}
