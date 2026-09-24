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
)

// nopWriter discards warmed response headers/body (output is buffered into dist by cacheWriter).
type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

// warmSyntheticRequest builds a synthetic GET request for warming.
// A non-localhost RemoteAddr -> isAdmin()=false, keeping admin content out of the public cache.
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
func warmPages() {
	if os.Getenv("DEV_MODE") == "1" {
		return
	}
	// Part of every list invalidation: invalidateAllCache drops the whole render cache, so the
	// shell has to be regenerated along with everything else.
	warmSearchShell()
	warmHome()
	warmPage("/archive", cachePath("/archive"), nil)
	// The list page renders the whole post set, so it belongs to every list invalidation.
	warmPage(PagePosts, cachePath(PagePosts), nil)
	for _, p := range []string{PageAbout, PageFriends, PageSponsor, PageGuestbook, PageFunError} {
		warmPage(p, cachePath(p), nil)
	}
	for _, cat := range getCategories() {
		// Both URLs, not just the alias the site links: handleCategory answers the raw name
		// too, so the tree needs a file for it as well.
		for _, p := range topicURLs(cat) {
			warmPage(p, cachePath(p), nil)
		}
	}
	warmPosts()
}

// warmPosts renders every post into the cache.
//
// Nothing else does this: post pages are otherwise warmed only when that post is
// published or edited, so a fresh deployment would publish a tree with no articles in
// it at all.
func warmPosts() {
	for _, p := range loadPostsFromDB() {
		path := URLPost(p.ID)
		warmPage(path, cachePath(path), nil)
	}
}

// warmPostPageAsync asynchronously pre-warms a post page (single-page comment/body changes).
func warmPostPageAsync(postID int) {
	go func() {
		if postID <= 0 {
			return
		}
		p := "/post/" + strconv.Itoa(postID)
		warmPage(p, cachePath(p), nil)
	}()
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
func warmPagesAsync() {
	if !warmListMu.TryLock() {
		return // a warm goroutine is already running; coalesce and avoid concurrent conflicts
	}
	go func() {
		defer warmListMu.Unlock()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("warm: list panic: %v", r)
			}
		}()
		warmPages()
	}()
}
