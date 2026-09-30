package main

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

// Cache pre-warming: after a change the server asynchronously pre-renders into public/
// so the first visitor gets a plain ServeFile instead of waiting for a render.
// Warms go through the same render pipeline as real requests (globalMux page handlers +
// cacheWriter) — no logic duplication.
// Under DEV_MODE caching is disabled anyway, so all warm calls are skipped.

// globalMux is assigned in main(); warming reuses the registered page handlers.
var (
	globalMux  *http.ServeMux
	warmListMu sync.Mutex
	// warmAgain is set when an invalidation arrives while a warm is running. The running warm
	// reads it when it finishes and goes round once more rather than letting the request be
	// dropped.
	warmAgain atomic.Bool
)

// nopWriter discards warmed response headers/body (output is buffered into dist by cacheWriter).
type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

// warmSyntheticRequest builds a synthetic GET request for warming.
// It carries no X-Is-Admin header, so isAdmin()=false and the warmed page is the anonymous
// rendering. An admin view must never be written into the served tree.
func warmSyntheticRequest(path string) *http.Request {
	req := &http.Request{
		Method:     "GET",
		URL:        &url.URL{Path: path},
		Host:       "blog.local",
		RemoteAddr: "warm:0",
		Header:     http.Header{},
		RequestURI: path,
	}
	req.Header.Set("User-Agent", "blog-warm/1.0") // resolves to the desktop device class
	return req
}

// warmPage renders a single page and writes the render cache. Skips when cp is empty,
// under DEV_MODE, or when the mux is not ready.
func warmPage(path, cp string, req *http.Request) {
	if os.Getenv("DEV_MODE") == "1" || cp == "" || globalMux == nil {
		return
	}
	// Every render funnels through here, so this is where the invalidation worker learns that
	// the bytes on disk are mid-change and it should wait (invalidate.go).
	inflightRenders.Add(1)
	defer func() {
		inflightRenders.Add(-1)
		wakeInvalidationWorker()
	}()
	if req == nil {
		req = warmSyntheticRequest(path)
	}
	lw := &cacheWriter{ResponseWriter: nopWriter{}, buf: &bytes.Buffer{}, cp: cp}
	// Route through the mux itself, not the matched handler: ServeHTTP is what injects
	// the path values ({id}, {category}, …) into the request, and handlers read them back
	// with r.PathValue. Calling the matched handler directly leaves those empty, so every
	// parameterised page renders a 404 that flush then refuses to write — the warm is a
	// no-op that still logs success.
	globalMux.ServeHTTP(lw, req)
	lw.flush()
	log.Printf("warm: %s", path)
}

// warmHome renders the home page into the cache.
//
// One file, not one per device and background: the page is deterministic (see
// pickHomeBackground), which is what lets a static web server publish "/" at all.
func warmHome() {
	desktop, mobile := pickHomeBackgrounds()
	req := warmSyntheticRequest("/")
	req = req.WithContext(context.WithValue(req.Context(), heroBgCtxKey, heroBgs{Desktop: desktop, Mobile: mobile}))
	warmPage("/", cachePath("/"), req)
}

// warmSearchShell renders the /search shell into the render cache.
//
// The page is a static shell — the query never reaches the server — so it is rendered once and
// then served as bytes: locally by handleSearchPage, and in production by nginx straight out of
// the static output. Every ?q= variant gets the same file, which is what makes a one-year CDN
// copy safe.
func warmSearchShell() {
	if os.Getenv("DEV_MODE") == "1" {
		return
	}
	cp := cachePath(PageSearch)
	if cp == "" {
		return
	}
	req := warmSyntheticRequest(PageSearch)
	lw := &cacheWriter{ResponseWriter: nopWriter{}, buf: &bytes.Buffer{}, cp: cp}
	execute(lw, req, "search.html", searchShellPageData(req))
	lw.flush()
	log.Printf("warm: %s", PageSearch)
}

// warmPages pre-warms every page the render cache can hold: the /search shell, the home
// page, the archive, the list page, the standalone pages, the category pages, and every post.
//
// The rule is coverage, not popularity: every URL a handler answers has to have a file,
// because a published tree has no handler behind it to render on request.
//
// Posts are in here because a full invalidation drops the whole render cache: without
// this, every post page would vanish on a config change and only reappear after being
// re-published. For a static publish that matters more than for a running server — a
// post missing from the cache is a 404 in the published tree, where no handler exists
// to re-render it on request.
// warmPagePaths is the page set a full warm covers: the fixed pages, every topic URL and
// every post. warmPages walks it, and TestEveryPageRendersOK renders the same list — a page
// added here is therefore both pre-rendered and smoke-tested.
//
// Posts belong here because nothing else warms them: a post page is otherwise pre-rendered
// only when that post is published or edited, so a fresh deployment would publish a tree
// with no articles in it at all.
func warmPagePaths() []string {
	paths := []string{
		PageArchive,
		// The list page renders the whole post set, so it belongs to every list invalidation.
		PagePosts,
		PageAbout, PageFriends, PageSponsor, PageGuestbook, PageFunError, PageStatus, PageDashboard,
	}
	for _, cat := range getCategories() {
		// Both URLs, not just the alias the site links: handleCategory answers the raw name
		// too, so the tree needs a file for it as well.
		paths = append(paths, topicURLs(cat)...)
	}
	for _, p := range loadPostsFromDB() {
		paths = append(paths, URLPost(p.ID))
	}
	return paths
}

func warmPages() {
	if os.Getenv("DEV_MODE") == "1" {
		return
	}
	// The lease spans the whole pass, not each page: a tree with half its pages rewritten is a
	// tree the worker must not fingerprint, and per-page writes alone would leave it free
	// between them.
	beginServedWrite()
	defer endServedWrite()
	// Part of every list invalidation: invalidateAllCache drops the whole render cache, so the
	// shell has to be regenerated along with everything else.
	warmSearchShell()
	warmHome()
	for _, p := range warmPagePaths() {
		warmPage(p, cachePath(p), nil)
	}
}

// warmPageNow renders one page and waits for it.
//
// A caller that is about to answer with that page uses this rather than the asynchronous form:
// the warm an invalidation kicks off runs behind the response, so the administrator who just
// saved would be shown the previous version of the page they saved.
func warmPageNow(path string) {
	warmPage(path, cachePath(path), nil)
}

// warmPostPage renders one post's page and waits for it. See warmPageNow.
func warmPostPage(postID int) {
	if postID <= 0 {
		return
	}
	warmPageNow("/post/" + strconv.Itoa(postID))
}

// warmCommentPageAsync asynchronously pre-warms after a comment change (post page or guestboard).
func warmCommentPageAsync(postID int) {
	go func() {
		if postID > 0 {
			p := "/post/" + strconv.Itoa(postID)
			warmPage(p, cachePath(p), nil)
		} else {
			warmPage("/guestbook", cachePath("/guestbook"), nil)
		}
	}()
}

// warmPagesAsync asynchronously pre-warms every page (post add/delete/title/tag changes,
// full invalidations).
//
// A request that arrives while a warm is already running is not dropped: it marks the running
// warm to go round again. Dropping it is what turns a wipe into a permanent 404 — by the time
// the request arrives the pages it invalidated are already deleted, and the warm that is
// running has passed them, so nothing renders them back.
//
// The lease is taken here, synchronously. A caller that queues URLs and then asks for a warm
// would otherwise leave the worker free to fingerprint the tree between the two.
func warmPagesAsync() {
	beginServedWrite()
	warmPagesAsyncLeased()
}

// warmPagesAsyncLeased starts the loop on a lease the caller already holds, and releases that
// lease when the loop's last pass ends.
//
// The lease spans the whole loop rather than one pass. A pass boundary is a complete tree, so
// releasing there would be safe — but the loop takes it straight back, two instructions later,
// so the worker would only ever catch the gap by luck, and every purge it did catch would be
// repeated after the next pass. Holding it to the end sends once, when the tree has stopped
// moving, which is also the state the edge's next fetch will read.
func warmPagesAsyncLeased() {
	if !warmListMu.TryLock() {
		// A loop is already running, and its lease already covers this request.
		warmAgain.Store(true)
		endServedWrite()
		return
	}
	go func() {
		defer warmListMu.Unlock()
		defer endServedWrite()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("warm: list panic: %v", r)
			}
		}()
		for {
			// Cleared before the warm, so anything that invalidates while it runs sets it
			// again and gets one more pass. The loop is bounded by the invalidation rate,
			// not by the number of callers.
			warmAgain.Store(false)
			warmPages()
			if !warmAgain.Load() {
				return
			}
		}
	}()
}
