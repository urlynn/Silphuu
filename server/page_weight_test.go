package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// bundleRefRe matches a bundle URL inside a real href/src attribute, quoted or not.
//
// The asset table is inlined into every page as a JS object literal, so a bare substring
// match would find every bundle name on every route and the assertions below would be
// vacuous.
var bundleRefRe = regexp.MustCompile(`(?:href|src)=["']?/assets/(?:css|js)/([A-Za-z0-9._-]+\.bundle\.(?:css|js))`)

// referencedBundles returns the bundle filenames the page actually links.
func referencedBundles(body string) map[string]bool {
	out := map[string]bool{}
	for _, m := range bundleRefRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = true
	}
	return out
}

// TestPageWeightBudget pins the per-route asset budget: each route links exactly the bundles
// its own template declares. The expected sets are the union of head.html and the route's
// head_extra.
func TestPageWeightBudget(t *testing.T) {
	useExampleSite(t)
	// 1. Build the bundles into the site's assets/ and load the asset manifest
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles failed: %v", err)
	}
	loadStickers()
	if err := initAssetManifest(); err != nil {
		t.Fatalf("initAssetManifest failed: %v", err)
	}
	initCommentStore()
	backfillPostIDs()
	backfillPosts()
	rebuildInkAssets()

	// 2. Check the generated 6 CSS + 5 JS bundle assets
	expectedBundles := []string{
		"css/core.bundle.css",
		"css/home.bundle.css",
		"css/browse.bundle.css",
		"css/article.bundle.css",
		"css/comment.bundle.css",
		"css/admin.bundle.css",
		"js/core.bundle.js",
		"js/home.bundle.js",
		"js/browse.bundle.js",
		"js/article.bundle.js",
		"js/comment.bundle.js",
	}

	for _, b := range expectedBundles {
		url := asset(b)
		if !strings.Contains(url, "?v=") {
			t.Errorf("Bundle %s manifest URL missing hash: %s", b, url)
		}
	}

	// 3. Check each route's referenced bundle set against a real HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleHome)
	mux.HandleFunc("GET /about", handleAuthor)
	mux.HandleFunc("GET /archive", handleArchive)
	mux.HandleFunc("GET /friends", handleFriends)
	mux.HandleFunc("GET /sponsor", handleSponsor)
	mux.HandleFunc("GET /guestbook", handleMessage)
	mux.HandleFunc("GET /post/{id}", handlePostByID)

	ts := httptest.NewServer(earlyHintsMiddleware(cacheMiddleware(mux)))
	defer ts.Close()

	// The demo dataset ships a small set of posts; derive a real post ID instead
	// of hardcoding one so the test works with any content set.
	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("no posts in index; the demo posts/ content is required for this test")
	}
	postPath := fmt.Sprintf("/post/%d", posts[len(posts)-1].ID)

	testCases := []struct {
		path        string
		expectedRel []string
	}{
		{"/", []string{"core.bundle.css", "home.bundle.css", "core.bundle.js", "home.bundle.js"}},
		{"/about", []string{"core.bundle.css", "browse.bundle.css", "core.bundle.js", "browse.bundle.js"}},
		{"/archive", []string{"core.bundle.css", "browse.bundle.css", "core.bundle.js", "browse.bundle.js"}},
		{"/friends", []string{"core.bundle.css", "browse.bundle.css", "core.bundle.js", "browse.bundle.js"}},
		{"/sponsor", []string{"core.bundle.css", "browse.bundle.css", "core.bundle.js", "browse.bundle.js"}},
		{"/guestbook", []string{"core.bundle.css", "comment.bundle.css", "core.bundle.js", "comment.bundle.js"}},
		{postPath, []string{"core.bundle.css", "article.bundle.css", "comment.bundle.css", "core.bundle.js", "article.bundle.js", "comment.bundle.js"}},
	}

	for _, tc := range testCases {
		res, err := http.Get(ts.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s failed: %v", tc.path, err)
		}
		bodyBytes, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatalf("Read %s body failed: %v", tc.path, err)
		}

		if res.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned status %d: %s", tc.path, res.StatusCode, string(bodyBytes))
			continue
		}

		body := string(bodyBytes)
		referenced := referencedBundles(body)

		// An empty set would make the comparison below pass without reading anything.
		if len(referenced) == 0 {
			t.Errorf("Route %s linked no bundle at all", tc.path)
			continue
		}
		got := slices.Sorted(maps.Keys(referenced))
		want := slices.Clone(tc.expectedRel)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("Route %s links bundles %v, want %v", tc.path, got, want)
		}
	}

	// 4. Test HTMX requests
	{
		req, _ := http.NewRequest("GET", ts.URL+"/about", nil)
		req.Header.Set("HX-Request", "true")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("HTMX GET /about failed: %v", err)
		}
		defer res.Body.Close()
		bodyBytes, _ := io.ReadAll(res.Body)
		body := string(bodyBytes)
		// hx-select="#content-wrapper" picks the container out of a full document, so an
		// HTMX response that is not a full page would swap the container away.
		if !strings.Contains(strings.ToLower(body), "<!doctype html>") {
			t.Errorf("HTMX response is not a full page; hx-select would find no #content-wrapper")
		}
		if !strings.Contains(body, "id=content-wrapper") && !strings.Contains(body, `id="content-wrapper"`) {
			t.Errorf("HTMX response missing content-wrapper")
		}
		t.Logf("HTMX /about response size: %d bytes", len(body))
	}

	// 5. The published search index is a file under /assets and carries no live counter
	{
		data, err := os.ReadFile(postsIndexPath())
		if err != nil {
			t.Fatalf("read published posts index: %v", err)
		}
		var items []PostJSONItem
		if err := json.Unmarshal(data, &items); err != nil {
			t.Fatalf("Unmarshal published posts index: %v", err)
		}
		if len(items) == 0 {
			t.Logf("Notice: published posts index has 0 items (test DB might be empty)")
		} else {
			t.Logf("published posts index has %d posts. First item ID: %d", len(items), items[0].ID)
		}
		if strings.Contains(string(data), `"views"`) {
			t.Error("published posts index must not carry the live view counter")
		}
	}

	// Drain the async warm/invalidation chain this test triggered (backfillPosts ->
	// invalidateListCache -> warmPagesAsync). Without this barrier the goroutine keeps
	// running into the NEXT test and reads the swapped RootRender global there —
	// invalidateAllCache then deletes the next test's render tree (search shell 500).
	warmListMu.Lock()
	warmListMu.Unlock()
}
