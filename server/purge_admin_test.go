package main

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The manual purge is the deliberate override of the automatic path: the administrator names
// what the edge should drop, and it goes whether or not the bytes moved. What the three scopes
// mean is pinned here, because a wrong reading of "selected" that silently fell through to
// "all" would burn the whole day's quota on one click.

// purgeOutletRecorder points the outlet at a file and returns a reader for what it received.
func purgeOutletRecorder(t *testing.T) func() []string {
	t.Helper()
	record := filepath.Join(t.TempDir(), "outlet.txt")
	stubOutletRecord(t, record)
	return func() []string {
		data, err := os.ReadFile(record)
		if err != nil {
			return nil
		}
		return strings.Fields(string(data))
	}
}

// postPurge drives the gateway the way the admin form does. The path values are set by hand
// because httptest.NewRequest runs no mux — this is exactly what POST /admin/commit/{category}
// fills in for the real request.
func postPurge(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/commit/purge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")
	req.SetPathValue("category", "purge")
	rec := httptest.NewRecorder()
	handleCommitGateway(rec, req)
	return rec
}

// postPurgeFetch drives the gateway the way the admin page's fetch does, so the reply carries the
// message text instead of a redirect.
func postPurgeFetch(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/admin/commit/purge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")
	req.Header.Set("X-Requested-With", "fetch")
	req.SetPathValue("category", "purge")
	rec := httptest.NewRecorder()
	handleCommitGateway(rec, req)
	return rec
}

func TestAdminPurgeSelectedSendsOnlyTheCheckedURLs(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)

	postPurge(t, url.Values{
		"scope": {"selected"},
		"url":   {"/about", "/feed.xml", "/about"}, // a repeat must not cost twice
	})

	received := got()
	want := map[string]bool{"https://example.test/about": true, "https://example.test/feed.xml": true}
	if len(received) != len(want) {
		t.Fatalf("the outlet received %v, want exactly %v", received, want)
	}
	for _, u := range received {
		if !want[u] {
			t.Errorf("the outlet received %q, which was not checked", u)
		}
	}
}

// The custom box is the escape hatch for URLs the list cannot know about, and it has to survive
// a pasted list: blank lines and "#" notes are the shape a human writes.
func TestAdminPurgeCustomSkipsBlanksAndNotes(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)

	postPurge(t, url.Values{
		"scope": {"custom"},
		"urls":  {"/about\n\n# 这一行是注释\n/feed.xml\n  /sitemap.xml  \n"},
	})

	received := got()
	if len(received) != 3 {
		t.Fatalf("the outlet received %v, want the three named paths", received)
	}
	for _, u := range received {
		if !strings.HasPrefix(u, "https://example.test/") {
			t.Errorf("the outlet received %q, which is not a URL of this site", u)
		}
	}
}

// A pasted full URL is what the administrator copies out of a browser, so it has to be accepted
// and reduced to the path the rest of the invalidation path speaks.
func TestAdminPurgeCustomAcceptsAFullURLOfThisSite(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)

	postPurge(t, url.Values{
		"scope": {"custom"},
		"urls":  {"https://example.test/about\n"},
	})

	received := got()
	if len(received) != 1 || received[0] != "https://example.test/about" {
		t.Fatalf("the outlet received %v, want the one URL unchanged", received)
	}
}

// "all" is the whole invalidation set — the same one a full rebuild sends, not a subset of it.
func TestAdminPurgeAllSendsTheFullInvalidationSet(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)

	postPurge(t, url.Values{"scope": {"all"}})

	received := got()
	want := dedupeNonEmpty(cdnURLsOf(fullInvalidationPaths()))
	if len(received) != len(want) {
		t.Fatalf("the outlet received %d URL(s), want %d", len(received), len(want))
	}
	seen := map[string]bool{}
	for _, u := range received {
		seen[u] = true
	}
	for _, u := range want {
		if !seen[u] {
			t.Errorf("full invalidation set holds %q and the purge did not send it", u)
		}
	}
}

func cdnURLsOf(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if u := cdnURL(p); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// Selecting nothing is not a purge of everything: it is a click that should say so and cost
// nothing. This is the failure that would be worst to get wrong — an empty form silently
// sending the whole set is a day's quota.
func TestAdminPurgeWithNothingNamedSendsNothing(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)

	for _, form := range []url.Values{
		{"scope": {"selected"}},
		{"scope": {"custom"}, "urls": {"\n# 只有注释\n"}},
		{},
	} {
		postPurge(t, form)
	}
	if received := got(); len(received) != 0 {
		t.Errorf("an empty purge reached the outlet with %v", received)
	}
}

// A node with no outlet answers every purge with "skipped". It must not record the URLs either:
// recording a purge that never happened is what pins the edge to the copy it already holds.
func TestAdminPurgeWithoutAnOutletSendsNothingAndRecordsNothing(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	stubOutlet(t, &recordingOutlet{})
	ledger := purgeLedgerPath()
	_ = os.Remove(ledger)

	rec := postPurge(t, url.Values{"scope": {"selected"}, "url": {"/about"}})
	if rec.Code != 303 {
		t.Fatalf("purge without an outlet answered %d, want the dashboard redirect", rec.Code)
	}
	if _, err := os.Stat(ledger); err == nil {
		t.Error("the ledger was written for a purge that never left this node")
	}
}

// renderAdminPage renders the admin page the way the served tree gets it.
func renderAdminPage(t *testing.T) string {
	t.Helper()
	req := httptest.NewRequest("GET", PageDashboard, nil)
	req.Header.Set("X-Is-Admin", "1")
	rec := httptest.NewRecorder()
	handleAdmin(rec, req)
	if rec.Code != 200 {
		t.Fatalf("handleAdmin answered %d", rec.Code)
	}
	return rec.Body.String()
}

// The card's status row is PROCESS STATE, and the page carrying it is served from disk and is
// itself in the purge set. Rendered there, its bytes would move on every flush, so the page could
// never agree with the ledger the flush compares against — and it would be re-sent on every full
// invalidation. Two renders under different flush outcomes must be identical.
func TestAdminPageDoesNotCarryTheFlushState(t *testing.T) {
	useExampleSite(t)

	recordInvalidation("invalidate.skipped", 0, nil)
	first := renderAdminPage(t)
	recordInvalidation("invalidate.ok", 7, nil)
	second := renderAdminPage(t)

	if first != second {
		t.Error("the admin page's bytes depend on the last flush: it is served from disk and is " +
			"itself in the purge set, so it would be re-sent on every full invalidation, forever")
	}
	// The flush sentences are the concrete leak: a resolved one means the state got in.
	for _, leak := range []string{"字节未变", "已送出", "未配置边缘爆破", "缓存爆破已停用", "本次启动无失效", "进行中", "已暂停"} {
		if strings.Contains(first, leak) {
			t.Errorf("the admin page renders the flush state (%q); it has to be fetched instead", leak)
		}
	}

	// The card and the modal are still there: the URL list, the switch buttons and the actions
	// are static and belong in the page. Bare id names, not id="...": html/template drops the
	// quotes on a static attribute.
	for _, want := range []string{"purge-status", "purge-outlet", "purge-ledger", "purge-last", "purge-switch", "purge-card-last"} {
		if !strings.Contains(first, want) {
			t.Errorf("admin page is missing %s", want)
		}
	}
}

// The endpoint is where those values live now, and it must not be cached: a cached copy of
// process state is a copy of another moment, which is the mistake this replaced.
func TestAdminPurgeStatusEndpointServesTheLiveState(t *testing.T) {
	useExampleSite(t)
	stubOutlet(t, &recordingOutlet{ready: true})
	recordInvalidation("invalidate.ok", 5, nil)

	rec := httptest.NewRecorder()
	handleAdminPurgeStatus(rec, httptest.NewRequest("GET", RouteAdminPurgeStatus, nil))

	if rec.Code != 200 {
		t.Fatalf("handleAdminPurgeStatus answered %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control is %q; a cached copy of process state describes another moment", cc)
	}
	var got struct {
		OutletOK   bool   `json:"outlet_ok"`
		Outlet     string `json:"outlet"`
		Paused     bool   `json:"paused"`
		SwitchText string `json:"switch_text"`
		Ledger     int    `json:"ledger"`
		LastPrefix string `json:"last_prefix"`
		LastSuffix string `json:"last_suffix"`
		LastAtMS   int64  `json:"last_at_ms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("the endpoint did not answer JSON: %v (%s)", err, rec.Body.String())
	}
	if !got.OutletOK || got.Outlet == "" {
		t.Errorf("outlet = %q (ok=%v) although a ready outlet is compiled in", got.Outlet, got.OutletOK)
	}
	if got.Paused {
		t.Error("the endpoint reports the node paused although nothing paused it")
	}
	if got.SwitchText == "" {
		t.Error("switch_text is empty, so the card has nothing to say about the switch")
	}
	// The sentence arrives split around a placeholder, so the browser can draw the clock in the
	// reader's own timezone. The count has to survive the split, the epoch has to come with it,
	// and neither half may carry the placeholder or a mangled Sprintf verb.
	if !strings.Contains(got.LastPrefix, "5") {
		t.Errorf("last_prefix = %q, which does not carry the flush's count", got.LastPrefix)
	}
	if got.LastAtMS <= 0 {
		t.Errorf("last_at_ms = %d, want the flush's epoch so the browser can draw the clock", got.LastAtMS)
	}
	for _, bad := range []string{"\x00", "%!"} {
		if strings.Contains(got.LastPrefix, bad) || strings.Contains(got.LastSuffix, bad) {
			t.Errorf("the split sentence carries %q: prefix=%q suffix=%q", bad, got.LastPrefix, got.LastSuffix)
		}
	}
}

// A state sentence carries no clock. "No outlet configured" and "paused" say what the node is,
// not when something happened, so they are written without the clock placeholder; the browser
// glues the epoch it is handed onto whatever prefix and suffix it gets, so a non-zero at here
// would date the state to the last flush.
func TestAStateSentenceCarriesNoClock(t *testing.T) {
	useExampleSite(t)
	for _, tc := range []struct{ kind, key string }{
		{"invalidate.no_outlet", "admin.purge.last_no_outlet"},
		{"invalidate.paused", "admin.purge.last_paused"},
	} {
		recordInvalidation(tc.kind, 0, nil)
		prefix, suffix, at := purgeLastParts()
		if prefix == tc.key {
			t.Errorf("%s: %s is missing from the copy library", tc.kind, tc.key)
		}
		if want := uiText(tc.key); prefix != want {
			t.Errorf("%s: prefix = %q, want %q", tc.kind, prefix, want)
		}
		if suffix != "" || !at.IsZero() {
			t.Errorf("%s: suffix = %q, at = %v; a sentence without a clock placeholder must "+
				"come back without a clock", tc.kind, suffix, at)
		}
	}
}

// A paused node answers a manual purge with the brake, not with "no outlet". The two states have
// different causes and different fixes, and the wrong reply sends the administrator to look at
// credentials that are fine.
func TestAdminPurgeWhilePausedSendsNothingAndSaysSo(t *testing.T) {
	useExampleSite(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv("CDN_BASE_URL", "https://example.test")
	got := purgeOutletRecorder(t)
	if err := setPurgePaused(true); err != nil {
		t.Fatalf("setPurgePaused: %v", err)
	}
	t.Cleanup(func() { _ = setPurgePaused(false) })

	rec := postPurgeFetch(t, url.Values{"scope": {"selected"}, "url": {"/about"}})

	if received := got(); len(received) != 0 {
		t.Errorf("a paused node sent %v to the outlet", received)
	}
	body := rec.Body.String()
	if !strings.Contains(body, uiText("admin.purge.paused_block")) {
		t.Errorf("a paused purge answered %q, want the brake sentence", body)
	}
	if strings.Contains(body, uiText("admin.purge.no_outlet")) {
		t.Errorf("a paused purge answered with the no-outlet sentence, which contradicts the switch: %q", body)
	}
}

// The switch is a file, not a field: it has to outlive the process, which is the only reason it
// exists. A cached value would pass a same-process test and fail the case it was built for.
//
// Driven through the gateway, so the form's action value is pinned to the handler as well.
func TestPurgeSwitchIsPersistedToDisk(t *testing.T) {
	useExampleSite(t)
	if purgePaused() {
		t.Fatal("the node starts paused; the default has to be purging")
	}

	postPurge(t, url.Values{"action": {"disable"}})
	if !purgePaused() {
		t.Error("the pause did not reach the disk")
	}
	if _, err := os.Stat(purgePausedPath()); err != nil {
		t.Errorf("no marker at %s, so a restart would come back purging: %v", purgePausedPath(), err)
	}

	postPurge(t, url.Values{"action": {"enable"}})
	if purgePaused() {
		t.Error("the resume did not reach the disk")
	}
	if _, err := os.Stat(purgePausedPath()); !os.IsNotExist(err) {
		t.Errorf("the marker survived the resume: %v", err)
	}
}
