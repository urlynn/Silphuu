package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"regexp"
	"strings"
	"testing"
)

var headAssetRefRe = regexp.MustCompile(`(?:href|src)="(/assets/[^"]+)"`)

func stripQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

// captureEarlyHints returns every Link header value carried by the 103 response.
// net/http does not expose 1xx as a normal response, so it has to come off the trace.
func captureEarlyHints(t *testing.T, url string) []string {
	t.Helper()
	var links []string
	trace := &httptrace.ClientTrace{
		Got1xxResponse: func(code int, header textproto.MIMEHeader) error {
			if code == http.StatusEarlyHints {
				links = append(links, header.Values("Link")...)
			}
			return nil
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return links
}

// pageHeadAssets returns the CSS/JS URLs the rendered page links in its head.
func pageHeadAssets(t *testing.T, url string) map[string]bool {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Say so when the page did not render at all: every assertion below then passes vacuously,
	// and "links no CSS/JS" would send the reader looking in the wrong place.
	if res.StatusCode != http.StatusOK {
		t.Errorf("GET %s: HTTP %d — the page did not render", url, res.StatusCode)
		return map[string]bool{}
	}
	out := map[string]bool{}
	for _, m := range headAssetRefRe.FindAllStringSubmatch(string(body), -1) {
		u := stripQuery(m[1])
		if strings.HasSuffix(u, ".css") || strings.HasSuffix(u, ".js") {
			out[u] = true
		}
	}
	return out
}

// preloadedAssets maps URL -> the part of the Link value after ">", so the caller can
// tell a stylesheet preload from a font one.
func preloadedAssets(links []string) map[string]string {
	out := map[string]string{}
	for _, l := range links {
		if !strings.HasPrefix(l, "<") {
			continue
		}
		end := strings.IndexByte(l, '>')
		if end < 0 {
			continue
		}
		out[stripQuery(l[1:end])] = l[end+1:]
	}
	return out
}

// TestEarlyHintsCoversEveryPageAsset keeps the 103 preload list in step with what the
// pages actually link. Both directions matter: a missing entry costs a round trip, an
// extra one costs a download the page never uses.
func TestEarlyHintsCoversEveryPageAsset(t *testing.T) {
	useExampleSite(t)
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles failed: %v", err)
	}
	if err := initAssetManifest(); err != nil {
		t.Fatalf("initAssetManifest failed: %v", err)
	}
	backfillPostIDs()
	backfillPosts()

	// /search is the one page whose handler never renders on demand: outside DEV_MODE it serves
	// the pre-rendered shell, so without this the fixture gets a 500 and the loop below passes
	// vacuously for it. Produce the shell the way startup does.
	warmSearchShell()

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PageHome, handleHome)
	mux.HandleFunc("GET "+PageAbout, handleAuthor)
	mux.HandleFunc("GET "+PageGuestbook, handleMessage)
	mux.HandleFunc("GET "+PageFunError, handleFunError)
	mux.HandleFunc("GET "+PageArchive, handleArchive)
	mux.HandleFunc("GET "+PageSponsor, handleSponsor)
	mux.HandleFunc("GET "+PageFriends, handleFriends)
	mux.HandleFunc("GET "+PagePosts, handlePosts)
	mux.HandleFunc("GET "+PageSearch, handleSearchPage)
	mux.HandleFunc("GET "+PagePost, handlePostByID)

	ts := httptest.NewServer(earlyHintsMiddleware(cacheMiddleware(mux)))
	defer ts.Close()

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("no posts in index; demo posts/ content is required for this test")
	}
	postPath := fmt.Sprintf("/post/%d", posts[len(posts)-1].ID)

	paths := []string{
		PageHome, PageAbout, PageGuestbook, PageFunError,
		PageArchive, PageSponsor, PageFriends, PagePosts, PageSearch, postPath,
	}

	checked := 0
	for _, p := range paths {
		links := captureEarlyHints(t, ts.URL+p)
		if len(links) == 0 {
			t.Errorf("%s: 103 carried no Link headers", p)
			continue
		}
		preloaded := preloadedAssets(links)
		refs := pageHeadAssets(t, ts.URL+p)
		if len(refs) == 0 {
			t.Errorf("%s: page links no CSS/JS at all — this check would pass vacuously", p)
			continue
		}

		for u := range refs {
			if _, ok := preloaded[u]; !ok {
				t.Errorf("%s: page loads %s but 103 does not preload it", p, u)
			}
		}
		for u, rest := range preloaded {
			if !strings.Contains(rest, "as=style") && !strings.Contains(rest, "as=script") {
				continue // fonts and images are not part of the head's link set
			}
			if !refs[u] {
				t.Errorf("%s: 103 preloads %s but the page never requests it", p, u)
			}
		}
		checked++
	}

	if checked == 0 {
		t.Fatal("no page was checked — this guard would pass vacuously")
	}
	t.Logf("checked %d pages", checked)
}
