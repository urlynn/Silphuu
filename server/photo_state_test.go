package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhotoAnnounceRecordsBeforeTheBytes: announcing puts the record in the album without
// touching a single file, which is what lets a node that will never hold the bytes render the
// card and publish the media URLs.
//
// The request is the one the wall sends: the id in the query. Reading it with ParseForm +
// FormValue would answer every announcement 400 — ParseForm never fills a multipart body — and
// the card this exists to publish early would never appear.
func TestPhotoAnnounceRecordsBeforeTheBytes(t *testing.T) {
	useExampleSite(t)

	req := httptest.NewRequest("POST", "/admin/commit/photo/announce?id=aaaa111122223333", nil)
	rec := httptest.NewRecorder()
	handlePhotoAnnounce(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("announce = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if photoIndex(loadPhotos(), "aaaa111122223333") < 0 {
		t.Errorf("the announced record is not in the album: %+v", loadPhotos())
	}
}

// TestPhotoAnnounceRequiresAnID: the caller owns the id, because the upload that follows has
// to land on the URL the announcement published.
func TestPhotoAnnounceRequiresAnID(t *testing.T) {
	useExampleSite(t)

	rec := httptest.NewRecorder()
	handlePhotoAnnounce(rec, httptest.NewRequest("POST", "/admin/commit/photo/announce", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("announce without an id = %d, want 400", rec.Code)
	}
}

// TestPhotoAnnounceDoesNotReadTheBody: the id channel is the query, and a body field is not read.
//
// This is the half of the contract the server owns. Reading the body instead is what broke the
// announcement: the wall sent a multipart field, ParseForm does not fill one, and every caller
// got a 400. A handler that accepts both shapes hides the next mismatch rather than reporting it,
// so the body is refused here on purpose.
func TestPhotoAnnounceDoesNotReadTheBody(t *testing.T) {
	useExampleSite(t)

	req := httptest.NewRequest("POST", "/admin/commit/photo/announce",
		strings.NewReader("id=aaaa111122223333"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	handlePhotoAnnounce(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("announce with the id only in the body = %d, want 400", rec.Code)
	}
	if photoIndex(loadPhotos(), "aaaa111122223333") >= 0 {
		t.Errorf("an id that only reached the body was recorded: %+v", loadPhotos())
	}
}

// TestBlobReadyKeepsTheRenderedPageInPlace: the card was rendered while the bytes were still
// being written, so the page is stale once they are there and the event says so. What it must
// not do is take the page away.
//
// A page with no file is a 404, and nginx answers these URLs out of the served tree with no
// handler behind them, so nothing recovers it — while the render that would put it back is the
// watcher's, about two seconds behind. The administrator is sent to this page by the very action
// that invalidated it, so the window is the ordinary case.
func TestBlobReadyKeepsTheRenderedPageInPlace(t *testing.T) {
	useExampleSite(t)

	cached := cachePath(PageAbout)
	writeTestFile(t, cached, "<html><body>rendered before the bytes</body></html>")

	applyBlobReadyEvent(testEvent(EvBlobReady, `{"page":"`+PageAbout+`"}`))

	if _, err := os.Stat(cached); err != nil {
		t.Errorf("the album page has no file after the ready event: %v", err)
	}
}

// TestTheAlbumRedirectReRendersThePage: an invalidation only queues the page for the edge, so the
// redirect that follows a write has to render it. Without that the administrator lands on the
// rendering from before their own write — and, while invalidateCache still removed the file, on a
// 404 for the width of the watcher's warm.
func TestTheAlbumRedirectReRendersThePage(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	wireTestMux(t)
	t.Setenv("DEV_MODE", "")

	cached := cachePath(PageAbout)
	stale := []byte("<html><body>rendered before the write</body></html>")
	writeTestFile(t, cached, string(stale))

	rec := httptest.NewRecorder()
	redirectToAbout(rec, httptest.NewRequest("POST", "/admin/commit/photo/delete", nil))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("redirect = %d, want 303", rec.Code)
	}
	body, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("the album page has no file after the redirect: %v", err)
	}
	// Anti-vacuity: a render that never ran leaves the sentinel in place, and the check below
	// would then pass for a reason that has nothing to do with rendering.
	if bytes.Equal(body, stale) {
		t.Fatal("the redirect answered with the page from before the write")
	}
	if !bytes.HasSuffix(bytes.TrimSpace(body), []byte("</html>")) {
		t.Errorf("the page was not replaced by a rendering: %.120s", body)
	}
}

// TestBlobReadyIgnoresAPayloadWithoutAPage: the event is generic, so a malformed one has to be
// inert rather than invalidate something arbitrary.
func TestBlobReadyIgnoresAPayloadWithoutAPage(t *testing.T) {
	useExampleSite(t)

	for _, payload := range []string{`{}`, `{"page":""}`, `not json`} {
		applyBlobReadyEvent(testEvent(EvBlobReady, payload))
	}
}

// testEvent builds an event envelope around a payload the projections understand.
func testEvent(typ, payload string) *Event {
	return &Event{Type: typ, Payload: []byte(payload)}
}

// TestApplyPhotoEventLandsTheRecord: an album delta reaches config/photo.json, which is what
// lets a node that never took the upload render the photo.
func TestApplyPhotoEventLandsTheRecord(t *testing.T) {
	useExampleSite(t)

	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"dddd000000000001","color":"#123456"}`))

	list := loadPhotos()
	if len(list) != 1 || list[0].ID != "dddd000000000001" {
		t.Fatalf("after %s, records = %+v, want the one record", EvPhotoAdded, list)
	}
	if list[0].Color != "#123456" {
		t.Errorf("color = %q, want the payload's", list[0].Color)
	}
}

// TestApplyPhotoEventIsIdempotent: the same event can arrive twice (a retry, or the local
// append plus the peer's copy), and a repeat must change nothing.
func TestApplyPhotoEventIsIdempotent(t *testing.T) {
	useExampleSite(t)

	evt := testEvent(EvPhotoAdded, `{"id":"dddd000000000002"}`)
	applyPhotoEvent(evt)
	applyPhotoEvent(evt)

	if got := loadPhotos(); len(got) != 1 {
		t.Errorf("records = %+v, want exactly one after two deliveries", got)
	}
}

// TestApplyPhotoEventTombstoneRemoves: removal is a tombstone, so it drops the record it
// names and leaves the others alone.
func TestApplyPhotoEventTombstoneRemoves(t *testing.T) {
	useExampleSite(t)

	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"dddd000000000003"}`))
	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"dddd000000000004"}`))
	applyPhotoEvent(testEvent(EvPhotoRemoved, `{"id":"dddd000000000003"}`))

	got := loadPhotos()
	if len(got) != 1 || got[0].ID != "dddd000000000004" {
		t.Errorf("records = %+v, want only dddd000000000004", got)
	}
}

// TestApplyPhotoEventOrderIsTheWholeList: a reorder payload is the whole order, not a delta, so
// a record it does not name is dropped. Only a caller holding the full list may build one.
func TestApplyPhotoEventOrderIsTheWholeList(t *testing.T) {
	useExampleSite(t)

	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"eeee000000000001"}`))
	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"eeee000000000002"}`))
	applyPhotoEvent(testEvent(EvPhotoAdded, `{"id":"eeee000000000003"}`))

	// Name only the last two, reversed. The first is not part of the new order.
	applyPhotoEvent(testEvent(EvPhotoOrdered, `{"ids":["eeee000000000003","eeee000000000002"]}`))

	var order []string
	for _, p := range loadPhotos() {
		order = append(order, p.ID)
	}
	want := "eeee000000000003,eeee000000000002"
	if got := strings.Join(order, ","); got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
}

// TestApplyPhotoEventIgnoresOtherDomains: the projection shares the live path with every
// other domain, so it has to leave events it does not own alone.
func TestApplyPhotoEventIgnoresOtherDomains(t *testing.T) {
	useExampleSite(t)

	applyPhotoEvent(testEvent(EvCommentAdded, `{"id":"ffff000000000001"}`))
	if got := loadPhotos(); len(got) != 0 {
		t.Errorf("a comment event landed an album record: %+v", got)
	}
}

// TestCommitEventLandsThroughTheLivePath: the wiring, not just the projection. A committed
// event has to reach config/photo.json, or the album would only ever appear on the node that
// took the upload.
func TestCommitEventLandsThroughTheLivePath(t *testing.T) {
	useExampleSite(t)
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	t.Cleanup(closeEventStore)

	if err := CommitEvent(EvPhotoAdded, Photo{ID: "9999000000000001"}); err != nil {
		t.Fatalf("CommitEvent: %v", err)
	}
	got := loadPhotos()
	if len(got) != 1 || got[0].ID != "9999000000000001" {
		t.Errorf("records = %+v, want the committed record", got)
	}
}

// TestPhotoRecordsHaveOneWriter: config/photo.json may only be written by the event
// projection and by the explicit cleanup. A handler that writes it directly bypasses the log,
// so a node that never saw the upload would never learn of the record.
func TestPhotoRecordsHaveOneWriter(t *testing.T) {
	allowed := map[string]bool{"photo_state.go": true, "main.go": true}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	scanned, hits := 0, 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		scanned++
		if !bytes.Contains(body, []byte("savePhotos(")) {
			continue
		}
		hits++
		if !allowed[f] {
			t.Errorf("%s writes the album index directly; it is written by the projection in photo_state.go", f)
		}
	}

	// A guard that scanned nothing, or found no call at all, proves nothing.
	if scanned < 20 {
		t.Fatalf("scanned only %d source file(s)", scanned)
	}
	if hits != len(allowed) {
		t.Errorf("found savePhotos in %d file(s), want %d", hits, len(allowed))
	}
}
