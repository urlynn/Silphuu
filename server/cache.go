package main

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// cachePath maps a URL path to the file its rendered HTML is cached in, or "" for
// requests that are never served from disk.
//
// The home page is a single file at the cache root rather than a directory of its own:
// a static web server maps "/" to <root>/index.html, and the home page has to be
// publishable as one page (see pickHomeBackground).
func cachePath(path string) string {
	if path == "" {
		return ""
	}
	if path == "/" {
		return filepath.Join(RootRender, "index.html")
	}
	path = strings.Trim(path, "/")
	return filepath.Join(RootRender, path, "index.html")
}

// cacheMiddleware wraps the entire mux with caching.
// Only GET requests outside /admin are cached. That prefix is the live one: it holds
// actions and data endpoints and never a page, so excluding it wholesale is exact.
// Identity is a separate exclusion further down.
// Set DEV_MODE=1 env var to disable all caching.
func cacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Only cache GET requests for public pages
		if r.Method != "GET" {
			next.ServeHTTP(w, r)
			return
		}

		// Admin requests never read/write the static cache: admin actions stay live and
		// the visitor cache can't be polluted. Whatever reaches the cache is therefore the
		// anonymous rendering.
		if isAdmin(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Dev mode: disable all caching
		if os.Getenv("DEV_MODE") == "1" {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			next.ServeHTTP(w, r)
			return
		}

		// ?nocache query param: bypass cache
		if r.URL.Query().Get("nocache") != "" {
			next.ServeHTTP(w, r)
			return
		}

		// htmx fragment requests: skip the full-page cache
		if r.Header.Get("HX-Request") == "true" {
			next.ServeHTTP(w, r)
			return
		}

		// Don't cache: static/assets files (by extension), the whole /admin prefix, debug,
		// login, search, comments, feeds, api, wp- probing
		if strings.HasSuffix(path, ".css") ||
			strings.HasSuffix(path, ".js") ||
			strings.HasSuffix(path, ".svg") ||
			strings.HasSuffix(path, ".ico") ||
			strings.HasSuffix(path, ".png") ||
			strings.HasSuffix(path, ".jpg") ||
			strings.HasSuffix(path, ".jpeg") ||
			strings.HasSuffix(path, ".avif") ||
			strings.HasSuffix(path, ".jxl") ||
			strings.HasSuffix(path, ".webp") ||
			strings.HasSuffix(path, ".json") ||
			strings.HasSuffix(path, ".xml") ||
			strings.HasSuffix(path, ".txt") ||
			strings.HasPrefix(path, PrefixStatic+"/") ||
			strings.HasPrefix(path, PrefixAssets+"/") ||
			strings.HasPrefix(path, "/debug/") ||
			strings.HasPrefix(path, "/admin") ||
			strings.HasPrefix(path, "/search") ||
			strings.HasPrefix(path, "/feed") ||
			strings.HasPrefix(path, "/sitemap") ||
			strings.HasPrefix(path, "/robots") ||
			strings.HasPrefix(path, "/api") ||
			strings.HasPrefix(path, "/wp-") {
			next.ServeHTTP(w, r)
			return
		}

		cp := cachePath(path)
		if cp != "" {
			if _, err := os.Stat(cp); err == nil {
				http.ServeFile(w, r, cp)
				return
			}
		}

		lw := &cacheWriter{ResponseWriter: w, buf: &bytes.Buffer{}, cp: cp}
		next.ServeHTTP(lw, r)
		lw.flush()
	})
}

type cacheWriter struct {
	http.ResponseWriter
	buf        *bytes.Buffer
	cp         string
	statusCode int
	hasError   bool
}

func (w *cacheWriter) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *cacheWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	n, err := w.ResponseWriter.Write(b)
	if err != nil {
		w.hasError = true
	}
	return n, err
}

func (w *cacheWriter) flush() {
	if w.cp == "" || w.buf.Len() == 0 || w.hasError {
		if w.hasError {
			log.Printf("cache: skip flush due to write error cp=%q len=%d", w.cp, w.buf.Len())
		}
		return
	}
	if w.statusCode != 0 && w.statusCode != http.StatusOK {
		log.Printf("cache: skip flush non-200 status %d for cp=%q", w.statusCode, w.cp)
		return
	}
	// The HTML page cache must end with </html>: prevents half-rendered HTML caused by a
	// mid-stream client disconnect from being baked into the render cache
	bs := bytes.TrimSpace(w.buf.Bytes())
	if strings.HasSuffix(w.cp, ".html") && !bytes.HasSuffix(bytes.ToLower(bs), []byte("</html>")) {
		log.Printf("cache: skip flush incomplete HTML for cp=%q (len=%d, missing </html>)", w.cp, w.buf.Len())
		return
	}

	// The page and its .br are each published by a rename, so a concurrent reader — a request
	// served from disk, the warm, the invalidation worker — sees a whole file, and nginx never
	// finds a .br that exists while its content is still being written.
	if err := writeServedFile(w.cp, w.buf.Bytes(), 0o644); err != nil {
		// A concurrent directory cleanup can remove the directory between the mkdir and the
		// rename; one retry recreates it.
		if retryErr := writeServedFile(w.cp, w.buf.Bytes(), 0o644); retryErr != nil {
			log.Printf("cache: write error: %v", retryErr)
			return
		}
	}
	log.Printf("cache: wrote %s (%d bytes)", w.cp, w.buf.Len())

	// The page on disk just changed, so the edge's copy may be stale. Recording it here rather
	// than at each caller means every writer is covered — the warm, an on-demand render, and
	// anything added later — and the worker can compare the bytes before it tells anyone.
	if u := urlForCachePath(w.cp); u != "" {
		invalidateURLs([]string{cdnURL(u)})
	}
}

// minifyAssetBytes minifies an in-memory HTML byte stream in-process via
// tdewolff/minify — the only compression pipeline (supports full pages and HTMX
// fragments). On error the original bytes are returned and the failure is logged;
// a page that keeps failing logs on every render.
func minifyAssetBytes(src []byte) []byte {
	if os.Getenv("HTML_MINIFY") == "0" || len(src) == 0 {
		return src
	}
	out, err := minifyHTMLString(string(src))
	if err != nil {
		log.Printf("html minify warn: %v", err)
		return src
	}
	return []byte(out)
}

// invalidateCache queues the named pages for the edge.
//
// It does not remove the page file, and it does not render. A page whose URL still has a route is
// overwritten by the next render, and that write is a rename, so a reader sees the previous page
// or the new one — never a missing file. Removing it instead publishes a 404 for as long as that
// render takes, and nginx answers these URLs out of the served tree with no handler behind them,
// so nothing recovers it. The window is the ordinary case rather than a rare interleaving: the
// callers that move a photo or a friend link answer with a redirect to the page they changed, and
// the render that follows them is the watcher's, about two seconds behind.
//
// The render is the caller's to schedule, not this function's. It runs under the event log's
// lock, and holding that across a full page render would couple every render to the sync loop.
// A caller that is about to answer with the page renders it itself: see warmPageNow.
func invalidateCache(paths ...string) {
	var urls []string
	for _, p := range paths {
		if strings.HasPrefix(p, "/") {
			urls = append(urls, p)
		}
	}
	invalidatePaths(urls)
}

// invalidatePostCache invalidates a single post: its detail page plus the pages that list
// it — its category page and the aggregates.
//
// Used for changes that affect one post: publishing, editing, deleting.
func invalidatePostCache(postID int) {
	if postID > 0 {
		if p, ok := loadPostWithContent(postID); ok {
			// The post is still there, so its URL is still there and the page is overwritten
			// in place. Deleting it first would publish a 404 for the width of one render and
			// buys nothing: cacheWriter.flush writes a temp file and renames it over the old
			// one, so a reader sees the previous page or the new one, never neither.
			invalidatePaths(postDependentURLs(p))
		} else {
			// The post is gone from disk (the deletion path), so its URL is gone too and the
			// file goes with it. Its category cannot be read back, so fall back to every
			// listing: anything narrower would leave the pages it used to appear on stale at
			// the edge for a year.
			os.RemoveAll(filepath.Join(RootRender, "post", strconv.Itoa(postID)))
			invalidatePaths(append([]string{"/post/" + strconv.Itoa(postID)}, listPageURLs()...))
		}
	}
	// Re-subset fonts asynchronously after content changes (new characters must join the subset)
	runFontSubset()
	// A post directory appearing or disappearing is rendered into the navbar, which is on every
	// page, so the escalation to a full invalidation belongs here rather than at each caller:
	// deleting a post is what can change the set.
	if categorySetChanged() {
		log.Printf("cache: category set changed; escalating the post invalidation to a full one")
		invalidateAllCache()
		return
	}
	// The pages the purge above names are re-rendered in the same breath, and the post's own
	// page with them. The edge's refetch reads this tree, so a listing left stale here is
	// cached again and the purge buys nothing: the change never reaches a visitor. The lease
	// is held from before the warm is scheduled, so the worker cannot fingerprint the tree
	// while its pages are being replaced.
	warmListPages()
}

// prunePostPages removes the post pages whose post no longer exists, and only those, and queues
// each removed page's URL for purging.
//
// A page whose post is still there is left alone: the warm that follows overwrites it, and
// its write is a rename, so a reader sees the old page or the new one. Removing the whole
// directory instead would take every post page down for the width of a full re-render, which
// is what a reader experiences as the site being gone.
//
// The purge is part of the removal rather than the caller's job. Once the file is gone, the
// edge holds the only copy of that page and no file is left for a fingerprint to compare, so a
// caller that dropped the URL would leave a deleted post answering for as long as the edge's
// policy says.
func prunePostPages() int {
	live := make(map[string]bool)
	for _, p := range loadPostsFromDB() {
		live[strconv.Itoa(p.ID)] = true
	}
	dir := filepath.Join(RootRender, "post")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0 // nothing rendered yet
	}
	removed := 0
	var urls []string
	for _, e := range entries {
		if live[e.Name()] {
			continue
		}
		target := filepath.Join(dir, e.Name())
		u := urlForCachePath(filepath.Join(target, "index.html"))
		if err := os.RemoveAll(target); err != nil {
			log.Printf("cache: prune %s: %v", e.Name(), err)
			continue
		}
		removed++
		if u != "" {
			urls = append(urls, u)
		}
	}
	invalidatePaths(urls)
	return removed
}

// invalidateCommentCache invalidates caches after a comment change. A post's page is decoupled
// from its comments and the comment fragment answers no-cache, so the edge revalidates it on
// every read and purging it would only spend quota. The guestboard (postID == 0) is the one
// page that renders its list into the HTML, so it is the only thing with a cache to invalidate.
func invalidateCommentCache(postID int) {
	if postID > 0 {
		return
	}
	invalidateCache("/guestbook")
	warmCommentPageAsync(0)
}

// purgeFriendPending drops the CDN's copy of the friends page's review-queue fragment. The
// fragment carries a one-year s-maxage and has no cache file of its own (it lives under
// /admin), so the purge is the only thing standing between a change and a stale queue.
func purgeFriendPending() {
	invalidateURLs([]string{cdnURL(RouteAdminContentFriendPending)})
}

// invalidateListCache invalidates the pages whose content depends on the post list: the
// home variants, the archive, the list page and every category page.
func invalidateListCache() {
	// The category list is rendered into the navbar, which is on every page. A directory
	// appearing or disappearing therefore changes every page, not just the list pages — so
	// escalate to a full invalidation rather than under-purging the rest of the site.
	if categorySetChanged() {
		log.Printf("cache: category set changed; escalating the list invalidation to a full one")
		invalidateAllCache()
		return
	}
	// The warm comes before the queue so the lease is already held when the URLs land in it.
	// The worker takes no fingerprint while it is, and one taken of the tree this is about to
	// replace would record a generation the edge never sees.
	warmListPages()
	invalidatePaths(listPageURLs())
}

// warmListPages drops the pages that no longer have a route and re-renders every page the cache
// can hold, asynchronously, so the first visitor gets a plain ServeFile instead of a render.
//
// The lease is taken here, before any of that is scheduled, and handed to the warm loop, which
// releases it when its last pass ends. A caller that queues URLs right after this call has
// therefore already put the tree out of the worker's reach.
//
// Every invalidation that names list pages goes through here rather than only re-rendering the
// page it was about: a purge reaches the edge as fresh bytes only if the origin re-renders the
// page, and the edge's refetch reads this tree.
func warmListPages() {
	beginServedWrite()
	pruneServedPages()
	warmPagesAsyncLeased()
}

// Purge target derivation: the CDN holds a one-year copy of every HTML page
// (s-maxage=31536000 on location /), so a changed page must be purged by URL — and
// precisely: never a URL that did not change, never a URL that did. The affected set is
// derived from the change itself, not a hand-written list.
// Three layers, one function each: aggregateURLs (pages depending on the post set as a
// whole), listPageURLs (aggregates + every category page), postDependentURLs(p)
// (one post's page, the listings it appears on, the aggregates). Full invalidation drops
// the entire render cache; its purge set is read back out of that cache (cachedPageURLs)
// plus purgeAlwaysPaths.

// purgeAlwaysPaths names the URLs purged on top of the render cache: the ones that carry their
// own long-lived Cache-Control, so the edge caches them even when the served tree has no file for
// them. `/` because its cache file can be absent on a fresh tree, the other three because they
// are never files, and the favicons because nothing regenerates them — a deployment that replaces
// its logo reaches the edge only through a full invalidation.
//
// Only the ones THIS NODE HOLDS are returned, which is the rule everywhere the blanket
// invalidation set is built (see staticImageURLs). A URL with no file here has nothing to
// compare, and the fingerprint sends what it cannot compare on EVERY flush. A named list is exactly
// the shape that grows, so the rule has to hold for it too: the moment someone adds an entry
// whose file this node does not have, that entry would be sent on every flush forever.
//
// Nothing is lost by leaving an unheld entry out. A URL whose bytes are absent would only be
// fetched as a 404 — and the moment the file appears, the digest becomes comparable and the URL
// is purged like any other.
func purgeAlwaysPaths() []string {
	named := []string{
		"/",
		"/feed.xml",
		"/sitemap.xml",
		"/robots.txt",
		RouteFaviconSvg,
		RouteFaviconIco,
	}
	held := make([]string, 0, len(named))
	for _, p := range named {
		if _, ok := servedNameForURL(cdnURL(p)); ok {
			held = append(held, p)
		}
	}
	return held
}

// aggregateURLs returns the pages whose content depends on the post set as a whole: the
// home page (latest-post cards, counts), the archive, the list page and the feeds.
//
// This is the one list that still has to be maintained by hand. It is short and stable —
// a new page type that lists posts has to be added here.
func aggregateURLs() []string {
	return []string{
		"/",
		"/archive",
		"/posts",
		"/feed.xml",
		"/sitemap.xml",
	}
}

// listPageURLs returns every URL whose content depends on the post list as a whole: the
// aggregates plus every category page.
//
// Every category is included, not only the ones this change touched. The list-change path
// cannot know which those are — a deleted post's category may already be gone from disk by
// the time the invalidation runs — so anything narrower would leave the pages it used to
// appear on stale at the edge for a year. Narrowing this needs the pre-change state captured
// by the caller.
func listPageURLs() []string {
	urls := aggregateURLs()
	for _, cat := range getCategories() {
		urls = append(urls, topicURLs(cat)...)
	}
	return dedupePaths(urls...)
}

// topicURLs returns the one URL a category page is reachable at: /topic/<registry key>.
// A display name is not a URL form and gets no URL of its own.
func topicURLs(category string) []string {
	cat := strings.TrimSpace(category)
	if cat == "" {
		return nil
	}
	return []string{"/topic/" + categoryToAlias(cat)}
}

// postDependentURLs returns every URL whose content depends on one post: its own page, the
// category page it appears on, and the aggregates.
//
// The category path comes from the post's own field, so this cannot drift out of sync with
// the content the way a hand-written list does.
func postDependentURLs(p Post) []string {
	urls := append([]string{"/post/" + strconv.Itoa(p.ID)}, aggregateURLs()...)
	urls = append(urls, topicURLs(p.Category)...)
	return dedupePaths(urls...)
}

// categorySet is the set of post directories the rendered pages were built with.
//
// The category list is rendered into the navbar (templates/navbar.html), which is on every
// page — so a directory appearing or disappearing changes every page, not just the list
// pages. Detecting that needs the previous set, which is what this holds.
var (
	categorySetMu sync.Mutex
	categorySet   []string
)

// initCategorySet records the current category set as the baseline. Called once at startup,
// after any startup invalidation, so the first runtime post change is compared against a
// set that actually matches the rendered pages.
func initCategorySet() {
	cats := getCategories()
	categorySetMu.Lock()
	categorySet = cats
	categorySetMu.Unlock()
	log.Printf("cache: category baseline = %v", cats)
}

// categorySetChanged reports whether the set of post directories differs from the baseline,
// and records the new set.
func categorySetChanged() bool {
	cats := getCategories()
	categorySetMu.Lock()
	defer categorySetMu.Unlock()
	if slices.Equal(cats, categorySet) {
		return false
	}
	categorySet = cats
	return true
}

// cachedPageURLsUnder returns the public URLs of the pages cached under dir. prefix is dir's
// path relative to RootRender, slash-separated ("" for the whole cache); the walk only sees
// paths below dir, so the prefix is needed to rebuild the URL.
func cachedPageURLsUnder(dir, prefix string) []string {
	var urls []string
	seen := make(map[string]bool)
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}

	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			// assets/ holds build output and static/ the site's own media, reached through
			// a link. Neither holds pages.
			if filepath.Dir(path) == dir && servedKept[filepath.Base(path)] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) != "index.html" {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(strings.TrimSuffix(rel, "/index.html"))
		if rel == "index.html" {
			rel = "" // the directory itself is the page
		}
		full := rel
		if prefix != "" {
			if rel == "" {
				full = filepath.ToSlash(prefix)
			} else {
				full = filepath.ToSlash(prefix) + "/" + rel
			}
		}
		if full == "" {
			add("/")
			return nil
		}
		add("/" + full)
		return nil
	})
	return urls
}

// cachedPageURLs returns the public URLs of every page held in the render cache.
//
// Must be called BEFORE the cache is deleted: afterwards there is nothing left to derive the
// purge list from, and the CDN keeps serving the old pages for a year.
func cachedPageURLs() []string {
	return cachedPageURLsUnder(RootRender, "")
}

// removeCachedPages drops the given cache-relative directories ("" means the whole cache) and
// returns the URLs of the pages they held.
//
// Read first, delete second. Deriving the purge set from what was actually removed is what
// keeps the local deletion and the CDN purge from drifting apart — the hand-written list this
// replaced deleted public/topic but purged no /topic/* URL.
func removeCachedPages(rels ...string) []string {
	var urls []string
	for _, rel := range rels {
		dir := RootRender
		if rel != "" {
			dir = filepath.Join(RootRender, filepath.FromSlash(rel))
		}
		urls = append(urls, cachedPageURLsUnder(dir, rel)...)
		os.RemoveAll(dir)
	}
	return urls
}

// invalidatePaths lives in invalidate.go, with the queue it feeds.

// staticImageURLs returns the URLs of the site's own images under static/image/, but only the
// ones THIS NODE HOLDS. They answer at a fixed URL, so a replaced file reaches the edge only
// when a full invalidation purges it; the set is read off the directory rather than listed here,
// so a new image needs no entry and a removed one leaves no dangling purge.
//
// It walks the tree, and that is the point: a URL this node does not hold has no bytes to
// compare, and the fingerprint sends what it cannot compare on EVERY flush. One such URL is
// affordable; a family of them is a quota bomb.
//
// The sticker family is the one that matters. It is 1073 files on the node that holds the media,
// and their URLs are FIXED — a post or a comment names them, so the URL cannot be hash-addressed
// and a change has to be purged. A blanket invalidation carrying all of them on a node that does
// not hold the tree would send 1073 URLs per flush, each one a wrong-origin fetch and a unit of
// the outlet's quota.
//
// So the rule is: the blanket invalidation set carries only URLs whose bytes are here. A change
// to a URL this node does not hold is purged by the change itself — which is what the sticker
// refresh already does (sticker.go: refreshStickers → invalidatePaths(rewritten)).
//
// The hero record is not appended: on the node that does not hold the media tree it has nothing
// to compare against, so /static/image/avatar.avif would go to the edge on every flush. An
// upload writes its files HERE (ProcessMediaPipeline → DirImage), so the walk already covers the
// avatar on the node that took the upload — appending the record would only add the case where
// the record names a file this node does not have, which is exactly the URL that must not be
// sent from here.
func staticImageURLs() []string {
	base := filepath.Join(RootStatic, "image")
	var urls []string
	if err := filepath.Walk(base, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasPrefix(info.Name(), ".") {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		urls = append(urls, StaticPrefix+"/image/"+filepath.ToSlash(rel))
		return nil
	}); err != nil {
		log.Printf("purge: walk %s: %v", base, err)
	}
	return urls
}

// fullInvalidationPaths returns every URL a full invalidation has to purge: each page the
// render cache holds, plus the handler-served pages that are never written to public/.
//
// Exists as its own function so the purge set can be asserted in a test. What matters is
// that the purged URLs and the deleted files come from the same source — a page dropped
// locally but left unpurged stays stale at the edge for a year.
//
// Must be called BEFORE the cache is deleted.
func fullInvalidationPaths() []string {
	paths := cachedPageURLs()
	paths = append(paths, purgeAlwaysPaths()...)
	paths = append(paths, staticImageURLs()...)
	return dedupePaths(paths...)
}

// invalidateAllCache invalidates the entire site: every page is purged from the CDN and
// re-rendered ahead of traffic.
//
// It does not drop the pages first. A page whose URL still has a route is overwritten by the
// warm below, and that write is a rename, so a reader sees the previous page or the new one —
// never a missing file. Wiping the tree instead took the whole site down for the width of a
// full re-render: every page the warm had not reached yet answered 404, and a second
// invalidation landing mid-warm left the pages it had already passed with nothing to render
// them back.
func invalidateAllCache() {
	traceInvalidationCaller()
	paths := fullInvalidationPaths()
	// The lease is taken before anything is queued or rewritten, and handed on to the warm:
	// from here until the warm's last pass ends, the worker takes no fingerprint. Queuing
	// first would leave it free to fingerprint the tree that is about to be replaced.
	beginServedWrite()
	resetRenderCache()
	invalidatePaths(paths)
	// Pre-warm: re-render every page asynchronously. Skipped during startup, where the mux is
	// not wired up yet and the startup warm does the same work.
	if globalMux == nil {
		endServedWrite()
		return
	}
	warmPagesAsyncLeased()
}

// resetRenderCache brings the served tree back in line with the routes: pages whose URL no
// longer exists are dropped, and the files that no page render produces — the static error
// pages, the service worker the generator writes verbatim, and the three crawler files —
// are rewritten.
//
// It does not wipe the tree. See invalidateAllCache.
func resetRenderCache() {
	// The files this rewrites are a set, not a sequence: the worker has to see the tree before
	// the reset or after it, never halfway through.
	beginServedWrite()
	defer endServedWrite()
	pruneServedPages()
	if err := generateStaticErrorPages(); err != nil {
		log.Printf("warn: generateStaticErrorPages: %v", err)
	}
	if err := materializeServiceWorker(); err != nil {
		log.Printf("warn: materializeServiceWorker: %v", err)
	}
	generateServedRootFiles("resetRenderCache")
}

// servedKept names the top-level entries under the served root that hold no pages: build
// output and visitor uploads. A cache wipe leaves them alone, and nothing in the wipe would
// recreate them.
var servedKept = map[string]bool{
	"assets": true,
	"static": true,
}

// routedPagePaths returns, as absolute paths, every page file the engine has a route for. A
// file under the served root that is not one of these is the only kind a reset may remove.
func routedPagePaths() map[string]bool {
	live := map[string]bool{}
	for _, p := range append([]string{"/", PageSearch}, warmPagePaths()...) {
		if cp := cachePath(p); cp != "" {
			live[cp] = true
		}
	}
	return live
}

// pruneServedPages removes the rendered pages under the served root that have no route any
// more, and nothing else. The served root also holds the built assets and the uploads, so it
// cannot be cleared wholesale the way a cache-only directory could.
//
// It replaces a wholesale wipe. A page that still has a route is left where it is: the warm
// that follows the invalidation overwrites it, and that write is a rename, so a reader sees
// the previous page or the new one — never a missing file.
func pruneServedPages() int {
	live := routedPagePaths()
	removed := 0
	_ = filepath.Walk(RootRender, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		name := info.Name()
		if info.IsDir() {
			if path != RootRender && (servedKept[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if live[path] || !strings.HasSuffix(name, ".html") || strings.HasPrefix(name, ".") {
			return nil
		}
		if rmErr := os.Remove(path); rmErr != nil {
			log.Printf("cache: prune %s: %v", path, rmErr)
			return nil
		}
		// The pre-compressed companion goes with the page it belongs to: left behind, it
		// describes a URL that no longer has any content.
		_ = os.Remove(path + ".br")
		removed++
		return nil
	})
	pruneEmptyPageDirs(RootRender)
	return removed
}

// pruneEmptyPageDirs removes the directories a prune left empty, so a page that is gone does
// not leave its directory behind. assets/ and static/ are kept whatever they hold.
func pruneEmptyPageDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if servedKept[name] || strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(dir, name)
		pruneEmptyPageDirs(child)
		if rest, err := os.ReadDir(child); err == nil && len(rest) == 0 {
			_ = os.Remove(child)
		}
	}
}

// generateServedRootFiles writes the three files that describe the site to crawlers. They are
// computed from the post set and the site's own base URL, and a served tree has no handler
// behind it, so each one has to exist as a file.
//
// by names the caller in the trace below. Two restarts of debugging: /feed.xml and
// /sitemap.xml were sent to the edge twice per restart, and their hashes moved and moved back
// within one second. The writer's own account of what it wrote, attributed to a caller, is the
// only thing that separates "the content really changed twice" from "a reader saw half a
// file" — from the flush's side the two are identical.
func generateServedRootFiles(by string) {
	// feed.xml and sitemap.xml are computed from the post index, and an unbuilt index reads
	// exactly like a site with no posts. Writing them before it exists publishes a feed with
	// no items — which is what the edge then holds, for as long as its cache policy says.
	// Refusing here makes a startup that reorders this a loud failure instead of a quiet one.
	if !postIndexIsReady() {
		log.Printf("rootfiles: skipped (%s): the post index is not built yet, so feed.xml would carry no posts", by)
		return
	}
	if err := os.MkdirAll(RootRender, 0o755); err != nil {
		log.Printf("warn: cannot create %s: %v", RootRender, err)
		return
	}
	base := siteBaseURL()
	for _, f := range []struct{ name, body string }{
		{"robots.txt", robotsTxt(base)},
		{"feed.xml", feedXML(base)},
		{"sitemap.xml", sitemapXML(base)},
	} {
		if err := writeServedFile(filepath.Join(RootRender, f.name), []byte(f.body), 0o644); err != nil {
			log.Printf("warn: cannot write %s: %v", f.name, err)
			continue
		}
		if traceEnabled() {
			if sum, err := fileSHA256(filepath.Join(RootRender, f.name)); err == nil {
				log.Printf("trace: rootfiles written by %s: %s sha=%s", by, f.name, sum[:8])
			}
		}
	}
}

// envTraceInvalidation turns on the invalidation trace. It is a debugging aid, not a
// behaviour: with it unset, nothing below logs and nothing else changes.
const envTraceInvalidation = "SILPHUU_TRACE_INVALIDATION"

func traceEnabled() bool { return os.Getenv(envTraceInvalidation) != "" }

// traceInvalidationCaller names the line that asked for a full invalidation. There are eight
// callers, and "which one ran twice during startup" is not answerable from the flush's side.
// Caller(2), not Caller(1): this function's own caller is invalidateAllCache itself.
func traceInvalidationCaller() {
	if !traceEnabled() {
		return
	}
	if _, file, line, ok := runtime.Caller(2); ok {
		log.Printf("trace: invalidateAllCache from %s:%d", filepath.Base(file), line)
	}
}
