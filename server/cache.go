package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
// Only GET requests for non-admin paths are cached.
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
		// the visitor cache can't be polluted
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

		// Don't cache: static/assets files (by extension), debug, admin, login, search, comments, feeds, api, wp- probing
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

	dir := filepath.Dir(w.cp)
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("cache: mkdir error: %v", err)
		return
	}
	// Atomic write: write to a temp file then rename, so concurrent readers (real-request
	// ServeFile + warm pre-render) never see a partial file
	tmp := w.cp + ".tmp"
	if err := os.WriteFile(tmp, w.buf.Bytes(), 0644); err != nil {
		log.Printf("cache: write error: %v", err)
		return
	}
	if err := os.Rename(tmp, w.cp); err != nil {
		// On concurrent directory cleanup, re-ensure the parent dir and retry once
		_ = os.MkdirAll(dir, 0755)
		if retryErr := os.Rename(tmp, w.cp); retryErr != nil {
			log.Printf("cache: rename error: %v", retryErr)
			os.Remove(tmp)
			return
		}
	}
	log.Printf("cache: wrote %s (%d bytes)", w.cp, w.buf.Len())
}

// minifyAssetBytes minifies an in-memory HTML byte stream in-process via
// tdewolff/minify — the only compression pipeline (supports full pages and HTMX
// fragments). On error the original bytes are returned and the failure is logged;
// a page that keeps failing logs on every render, see docs/GOTCHAS.md §minify-warn-noise.
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

func invalidateCache(paths ...string) {
	var urls []string
	for _, p := range paths {
		if cp := cachePath(p); cp != "" {
			os.Remove(cp)
		}
		if strings.HasPrefix(p, "/") {
			urls = append(urls, p)
		}
	}
	purgeCDN(urls)
}

// invalidatePostCache invalidates a single post: its detail page plus the pages that list
// it — its category page and the aggregates.
//
// Used for changes that affect one post: publishing, editing, deleting.
func invalidatePostCache(postID int) {
	if postID > 0 {
		os.RemoveAll(filepath.Join(RootRender, "post", strconv.Itoa(postID)))
		if p, ok := loadPostWithContent(postID); ok {
			purgeCDN(postDependentURLs(p))
		} else {
			// The post is already gone from disk (the deletion path), so its category
			// cannot be read back. Fall back to every listing: anything narrower would
			// leave the pages it used to appear on stale at the edge for a year.
			purgeCDN(append([]string{"/post/" + strconv.Itoa(postID)}, listPageURLs()...))
		}
	}
	// Re-subset fonts asynchronously after content changes (new characters must join the subset)
	runFontSubset()
	// Pre-warm: re-render this post page into the cache asynchronously so the first
	// visitor gets a plain ServeFile (warmCache.go)
	warmPostPageAsync(postID)
}

// invalidateCommentCache invalidates caches after a comment change:
// post pages are decoupled from comments — a comment change neither invalidates the
// post body HTML cache nor triggers global font subsetting; only for the guestboard
// (postID == 0) does it invalidate the /guestbook page cache, rewarm it asynchronously,
// and actively purge the CDN comment shards.
func invalidateCommentCache(postID int) {
	if postID > 0 {
		PurgeESACache(
			cdnURL(fmt.Sprintf("/api/comment/list?post_id=%d&sort=newest", postID)),
			cdnURL(fmt.Sprintf("/api/comment/list?post_id=%d&sort=oldest", postID)),
			cdnURL(fmt.Sprintf("/api/comment/list?post_id=%d&sort=hottest", postID)),
		)
		return // post page HTML keeps its 100% stable strong cache
	}
	invalidateCache("/guestbook")
	warmCommentPageAsync(0)
	PurgeESACache(cdnURL("/guestbook"), cdnURL("/api/comment/list?post_id=0&sort=newest"))
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
	removeCachedPages("home", "archive", "posts", "topic")
	purgeCDN(listPageURLs())
	// Pre-warm: re-render all list pages asynchronously (home variants + archive + categories)
	warmPagesAsync()
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

// purgeAlwaysPaths are named on top of the render cache. All four carry their own long-lived
// Cache-Control, so the CDN caches them even when public/ has no file for them — / because
// its cache file can be absent on a fresh tree, the other three because they are never files.
var purgeAlwaysPaths = []string{
	"/",
	"/feed.xml",
	"/sitemap.xml",
	"/robots.txt",
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
// by the caller; see PLAN.md.
func listPageURLs() []string {
	urls := aggregateURLs()
	for _, cat := range getCategories() {
		urls = append(urls, topicURLs(cat)...)
	}
	return dedupePaths(urls...)
}

// topicURLs returns every URL that reaches one category page.
//
// There are two. The templates link the *alias* (post_card and navbar build /topic/… from
// CategoryAliases), while handleCategory runs the path through aliasToCategory, which falls
// back to the raw name when nothing matches — so `/topic/essay` and `/topic/随笔` are two
// URLs for the same page, cached independently. Purging only the raw name left the one that
// is actually linked stale at the edge for a year. Both URLs must be purged/warmed —
// see docs/GOTCHAS.md §topic-dual-url.
func topicURLs(category string) []string {
	cat := strings.TrimSpace(category)
	if cat == "" {
		return nil
	}
	urls := []string{"/topic/" + cat}
	if alias := categoryToAlias(cat); alias != cat {
		urls = append(urls, "/topic/"+alias)
	}
	return urls
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
		if err != nil || info == nil || info.IsDir() {
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

// cdnPurgeBatchSize caps how many URLs go into one purge call. The ESA file-purge API
// accepts at most 1000 URLs per request; exceeding it fails the whole call.
const cdnPurgeBatchSize = 1000

// purgeCDN purges the given root-relative paths from the CDN, in batches.
func purgeCDN(paths []string) {
	var urls []string
	seen := make(map[string]bool)
	for _, p := range paths {
		if u := cdnURL(p); !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for len(urls) > 0 {
		n := min(len(urls), cdnPurgeBatchSize)
		PurgeESACache(urls[:n]...)
		urls = urls[n:]
	}
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
	return dedupePaths(append(cachedPageURLs(), purgeAlwaysPaths...)...)
}

// invalidateAllCache invalidates the entire site: the render cache is dropped, every page
// it held is purged from the CDN, and the list pages are re-rendered ahead of traffic.
func invalidateAllCache() {
	paths := fullInvalidationPaths()
	resetRenderCache()
	purgeCDN(paths)
	// Pre-warm: re-render home + list pages asynchronously; post detail pages rebuild
	// lazily. Skipped during startup, where the mux is not wired up yet.
	if globalMux != nil {
		warmPagesAsync()
	}
}

// resetRenderCache drops the render cache and puts back the two files that live there and
// that nothing recreates before the next restart: the static error pages, and the service
// worker a projection publishes verbatim — see docs/GOTCHAS.md §render-wipe-drops-error-pages.
func resetRenderCache() {
	if err := os.RemoveAll(RootRender); err != nil {
		log.Printf("cache: cannot clear %s: %v", RootRender, err)
	}
	if err := generateStaticErrorPages(); err != nil {
		log.Printf("warn: generateStaticErrorPages: %v", err)
	}
	if err := materializeServiceWorker(); err != nil {
		log.Printf("warn: materializeServiceWorker: %v", err)
	}
}
