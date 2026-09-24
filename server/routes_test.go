package main

import (
	"encoding/json"
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
	reqCommit, _ := http.NewRequest("POST", ts.URL+"/admin/commit/config", strings.NewReader("motto=test_motto"))
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
