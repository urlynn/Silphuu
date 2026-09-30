package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestRouteGateways(t *testing.T) {
	// This test POSTs to /admin/commit/config — an endpoint that REALLY writes
	// config/site.json, so it has to run against a site directory of its own.
	//
	// Use REAL isolation instead of "snapshot + restore": a snapshot only
	// repairs after the fact — the real file would still be briefly rewritten
	// during the test (a dev-server request at just the wrong moment would
	// render the damage), and a defer would not run if the process got
	// SIGKILLed. t.Chdir moves the whole test working directory into a temp
	// dir, so every relative path in the code (config/site.json,
	// staging/friend/, …) lands in the temp dir and real data is never
	// touched; t.Cleanup switches back automatically.
	// (Go 1.24+; this package has no t.Parallel(), so process-wide chdir is safe.)
	t.Chdir(t.TempDir())
	for _, dir := range []string{RootConfig, RootState} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// The config save commits its delta to the event log, so the store has to be up: with no
	// log the handler answers an error instead of a change no other node would learn about.
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	t.Cleanup(closeEventStore)

	mux := http.NewServeMux()

	// Use the production route table rather than a copy of it: a hand-written copy can
	// list a route the server never mounts, and then the test passes while the endpoint
	// 405s in production. Anything the frontend is handed must be mounted here.
	registerAPIRoutes(mux)

	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Disable redirect auto-following to inspect exact statuses like 303 / 401
	client := ts.Client()
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	// 1. POST /admin/commit/config (admin save)
	reqCommit, _ := http.NewRequest("POST", ts.URL+"/admin/commit/config", strings.NewReader("signature=test_signature"))
	reqCommit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqCommit.Header.Set("X-Requested-With", "fetch")
	reqCommit.Header.Set("X-Is-Admin", "1")
	resCommit, err := client.Do(reqCommit)
	if err != nil {
		t.Fatalf("POST /admin/commit/config failed: %v", err)
	}
	if resCommit.StatusCode != http.StatusOK {
		t.Fatalf("POST /admin/commit/config expected 200, got %d", resCommit.StatusCode)
	}
	resCommit.Body.Close()

	// 2. POST /api/view (read tracking)
	resView, err := client.Post(ts.URL+"/api/view?p=/post/1", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/view failed: %v", err)
	}
	if resView.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/view status: %d", resView.StatusCode)
	}
	var viewResp map[string]interface{}
	json.NewDecoder(resView.Body).Decode(&viewResp)
	resView.Body.Close()
	if !viewResp["ok"].(bool) {
		t.Fatalf("view response ok is not true")
	}

	// 3. GET /api/stats (visitor stats)
	resStats, err := client.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatalf("GET /api/stats failed: %v", err)
	}
	if resStats.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/stats status: %d", resStats.StatusCode)
	}
	resStats.Body.Close()

	// 4. GET /api/comment/list (comment partial render)
	resList, err := client.Get(ts.URL + "/api/comment/list?post_id=0")
	if err != nil {
		t.Fatalf("GET /api/comment/list failed: %v", err)
	}
	if resList.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/comment/list status: %d", resList.StatusCode)
	}
	resList.Body.Close()

	// 5. DELETE /api/comment/{id} (authorized delete)
	// 5.1 Authorized delete with the author token
	initCommentStore()
	cid := NewULID()
	{
		seed := &Comment{ID: cid, Nick: "测试", Content: "seed", Date: "2026-09-06 00:00", PostID: 0}
		cmMu.Lock()
		cm.byID[cid] = seed
		cm.byPost[0] = append(cm.byPost[0], seed)
		cmMu.Unlock()
	}
	token := genCommentOwnerToken(cid)
	form := url.Values{"token": {token}, "id": {cid}}
	resDelAuth, err := client.PostForm(ts.URL+"/api/comment/delete", form)
	if err != nil {
		t.Fatalf("POST /api/comment/delete with token failed: %v", err)
	}
	if resDelAuth.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/comment/delete with valid token expected 200, got %d", resDelAuth.StatusCode)
	}
	resDelAuth.Body.Close()
}

// The commit gateway dispatches on the category, the action and the body encoding together,
// and reading only two of the three has gone wrong twice: an upload action without a
// multipart body saved the article instead of being refused, and a multipart body under a
// media category was read as an upload whatever the path said.
func TestCommitGatewayDispatchesOnTheActionNotOnlyTheBody(t *testing.T) {
	// Same isolation as TestRouteGateways: relative config/state paths land in the temp dir.
	t.Chdir(t.TempDir())
	for _, dir := range []string{RootConfig, RootState} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// The reorder handler commits its delta to the event log, so the store has to be up: with
	// no log the handler answers an error instead of a change no other node would learn about.
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	t.Cleanup(closeEventStore)

	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// 1. An upload action with a urlencoded body is refused, not treated as the article save.
	res, err := http.Post(ts.URL+"/admin/commit/post/upload", "application/x-www-form-urlencoded", strings.NewReader("title=hi"))
	if err != nil {
		t.Fatalf("POST /admin/commit/post/upload: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("POST /admin/commit/post/upload with a urlencoded body = %d, want 400 (body: %s)", res.StatusCode, body)
	}

	// 2. A multipart body under a media category is not an upload when the path names another
	//    action: the reorder handler has to be reached rather than the media pipeline.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("ids", ""); err != nil {
		t.Fatalf("write multipart field: %v", err)
	}
	mw.Close()

	req, err := http.NewRequest("POST", ts.URL+"/admin/commit/photo/reorder", &buf)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /admin/commit/photo/reorder: %v", err)
	}
	body2, _ := io.ReadAll(res2.Body)
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("POST /admin/commit/photo/reorder with a multipart body = %d, want 200 from the reorder handler (body: %s)", res2.StatusCode, body2)
	}

	// 3. The reorder handler reads its fields with r.FormValue, so the body has to be
	//    urlencoded for them to arrive at all: an empty ids field returns before it answers,
	//    which is what a multipart body would look like.
	res3, err := http.PostForm(ts.URL+"/admin/commit/photo/reorder", url.Values{"ids": {"no-such-photo"}})
	if err != nil {
		t.Fatalf("POST /admin/commit/photo/reorder (urlencoded): %v", err)
	}
	body3, _ := io.ReadAll(res3.Body)
	res3.Body.Close()
	if strings.TrimSpace(string(body3)) != `{"ok":true}` {
		t.Errorf("POST /admin/commit/photo/reorder with a urlencoded body answered %q, want {\"ok\":true} — the handler did not see its field", body3)
	}
}

// The home handler is registered on "GET /", which Go's ServeMux treats as the catch-all.
// Without the path check the site has no 404s at all: a typo, a deleted page and a retired
// endpoint would all come back as the home page with a 200.
func TestHomeHandlerAnswersOnlyTheRootPath(t *testing.T) {
	// The guard runs before any rendering, so no posts or templates are needed here.
	for _, path := range []string{
		"/posts.json",
		PrefixAPI + "/content/posts.json",
		"/王小明.json",
		"/archive/nope",
	} {
		rec := httptest.NewRecorder()
		handleHome(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404 (the home page is not a fallback)", path, rec.Code)
		}
	}
}

// TestEveryPageRendersOK renders every page the site mounts and fails on anything that is not
// a 200. A template mistake — a .UI reference outside its scope, a renamed field — reaches the
// reader as a 500 whose body is the template error, and nothing else in the suite executes a
// page, so it could ship unnoticed.
//
// Routes come from registerPageRoutes and the path list from warmPagePaths: the same two
// sources the server uses. A test that registered its own table would not notice a page the
// server never mounted, and a hand-written path list would drift from the warm list.
func TestEveryPageRendersOK(t *testing.T) {
	useExampleSite(t)
	initTemplates()
	backfillPosts()

	// The admin page's font studio renders one block per preset, and the demo site ships no
	// fonts.json. With an empty list the ranged body never executes, so a broken reference
	// inside it stays invisible — seed one preset to make the guard cover that block.
	cfg := loadAppConfig()
	cfg.FontPresets = []FontPreset{{
		ID:    "preset_probe",
		Name:  "probe",
		Fonts: map[string]string{"body": "'ExampleSans'|400"},
	}}
	cfg.ActiveFontPreset = "preset_probe"
	if err := os.MkdirAll(RootState, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", RootState, err)
	}
	if err := saveFontState(cfg); err != nil {
		t.Fatalf("seed a font preset: %v", err)
	}

	oldMux := globalMux
	defer func() { globalMux = oldMux }()
	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	registerPageRoutes(mux)
	globalMux = mux

	// /search serves a pre-rendered shell; without it the page answers 500 for a reason that
	// has nothing to do with templates.
	warmSearchShell()

	paths := append([]string{PageHome, PageSearch, PageFeed, PageSitemap, PageRobots}, warmPagePaths()...)
	// The editor forms are pages too and neither is in the warm list: they render from
	// request-scoped data (today's date, the post being edited).
	paths = append(paths, RouteAdminNewPost)
	if posts := loadPostsFromDB(); len(posts) > 0 {
		paths = append(paths, URLPostEdit(posts[0].ID))
	}

	if len(paths) < 10 {
		t.Fatalf("只收集到 %d 个页面，这个守卫会空跑", len(paths))
	}

	html := 0
	for _, path := range paths {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, warmSyntheticRequest(path))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200: %s", path, rec.Code, firstLine(rec.Body.String()))
			continue
		}
		if strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
			html++
		}
	}
	if html < 5 {
		t.Errorf("只有 %d 个页面真的渲染了 HTML，这个守卫会空跑", html)
	}
}

// firstLine keeps a 500's body — which is the template error — short enough to read in the
// failure output.
func firstLine(s string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(line) > 200 {
		line = line[:200] + "…"
	}
	return line
}
