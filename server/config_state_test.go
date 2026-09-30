package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestTheSponsorRedirectReRendersThePage: the sponsor page is a file the web server hands out,
// and the event the write commits only queues it for the edge — the render behind it is the
// watcher's, after its debounce. So the handler has to render the page before it answers, or
// the administrator is sent back to it as it was before the write.
func TestTheSponsorRedirectReRendersThePage(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	wireTestMux(t)
	t.Setenv("DEV_MODE", "")

	cached := cachePath(PageSponsor)
	stale := "<html><body>rendered before the write</body></html>"
	writeTestFile(t, cached, stale)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/admin/commit/sponsor/add",
		strings.NewReader("nick=probe&amount=1.00&date=2026-09-28"))
	handleAdminSponsorAdd(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("redirect = %d, want 303", rec.Code)
	}
	body, err := os.ReadFile(cached)
	if err != nil {
		t.Fatalf("the sponsor page has no file after the redirect: %v", err)
	}
	// Anti-vacuity: a render that never ran leaves the sentinel in place, and the check below
	// would then pass for a reason that has nothing to do with rendering.
	if string(body) == stale {
		t.Fatal("the redirect answered with the page from before the write")
	}
	if !bytes.HasSuffix(bytes.TrimSpace(body), []byte("</html>")) {
		t.Errorf("the page was not replaced by a rendering: %.120s", body)
	}
}

// friendName returns a link's display name, or "" when the list does not hold the URL.
func friendName(list []FriendLink, url string) string {
	if at := friendIndex(list, url); at >= 0 {
		return list[at].Name
	}
	return ""
}

// TestApplyFriendEventLandsAndIsIdempotent: an admin edit reaches config/friend.json, which is
// what lets a node that never saw the edit render the same page.
func TestApplyFriendEventLandsAndIsIdempotent(t *testing.T) {
	useExampleSite(t)

	link := `{"name":"Probe","url":"https://probe.example.com","desc":"d","icon":"link","tag":"Blog"}`
	applyConfigEvent(testEvent(EvFriendAdded, link))
	applyConfigEvent(testEvent(EvFriendAdded, link))

	var found int
	for _, f := range loadFriends() {
		if f.URL == "https://probe.example.com" {
			found++
		}
	}
	if found != 1 {
		t.Errorf("the link appears %d time(s) after two deliveries, want 1", found)
	}
}

// TestApplyFriendEventEditAndRemoveLocateByURL: a delta names the link by the URL it carried,
// never by list position, because two nodes can hold the same links in a different order.
func TestApplyFriendEventEditAndRemoveLocateByURL(t *testing.T) {
	useExampleSite(t)

	applyConfigEvent(testEvent(EvFriendAdded, `{"name":"Old","url":"https://key.example.com","tag":"Blog"}`))
	applyConfigEvent(testEvent(EvFriendEdited,
		`{"url":"https://key.example.com","link":{"name":"New","url":"https://key.example.com","tag":"Blog"}}`))

	if got := friendName(loadFriends(), "https://key.example.com"); got != "New" {
		t.Errorf("name = %q after the edit, want %q", got, "New")
	}

	applyConfigEvent(testEvent(EvFriendRemoved, `{"url":"https://key.example.com"}`))
	if got := friendName(loadFriends(), "https://key.example.com"); got != "" {
		t.Errorf("name = %q after the tombstone, want the link gone", got)
	}
}

// TestApplySponsorEventLocatesByNick: a sponsor is keyed by its nickname.
func TestApplySponsorEventLocatesByNick(t *testing.T) {
	useExampleSite(t)

	applyConfigEvent(testEvent(EvSponsorAdded, `{"nick":"probe","amount":"1"}`))
	applyConfigEvent(testEvent(EvSponsorEdited, `{"nick":"probe","sponsor":{"nick":"probe","amount":"2"}}`))

	var amount string
	for _, s := range loadSponsors() {
		if s.Nick == "probe" {
			amount = s.Amount
		}
	}
	if amount != "2" {
		t.Errorf("amount = %q after the edit, want %q", amount, "2")
	}

	applyConfigEvent(testEvent(EvSponsorRemoved, `{"nick":"probe"}`))
	for _, s := range loadSponsors() {
		if s.Nick == "probe" {
			t.Errorf("the sponsor survived its tombstone: %+v", s)
		}
	}
}

// TestApplyBgEventModeIsAbsoluteNotAToggle: the payload carries the mode to hold. A toggle
// cannot be replayed, because its result depends on the value already there.
func TestApplyBgEventModeIsAbsoluteNotAToggle(t *testing.T) {
	useExampleSite(t)

	evt := testEvent(EvBgMode, `{"mode":"sequential"}`)
	applyConfigEvent(evt)
	if got := loadBgConfig().Mode; got != "sequential" {
		t.Fatalf("mode = %q, want the payload's", got)
	}
	applyConfigEvent(evt)
	if got := loadBgConfig().Mode; got != "sequential" {
		t.Errorf("mode = %q after a repeat, want it unchanged", got)
	}
}

// TestApplyBgEventOffsetLocatesBySrc: a background is keyed by the file it was made from.
func TestApplyBgEventOffsetLocatesBySrc(t *testing.T) {
	useExampleSite(t)

	const src = "/static/image/background/probe.avif"
	applyConfigEvent(testEvent(EvBgAdded, `{"src":"`+src+`","device":"desktop","tone":"dark"}`))
	applyConfigEvent(testEvent(EvBgOffset, `{"src":"`+src+`","x":0.25,"y":0.75}`))

	cfg := loadBgConfig()
	at := bgIndex(cfg.Backgrounds, src)
	if at < 0 {
		t.Fatalf("the background did not land: %+v", cfg.Backgrounds)
	}
	if cfg.Backgrounds[at].OffsetX != 0.25 || cfg.Backgrounds[at].OffsetY != 0.75 {
		t.Errorf("offset = %v, %v; want the payload's", cfg.Backgrounds[at].OffsetX, cfg.Backgrounds[at].OffsetY)
	}
}

// TestApplyConfigEventIgnoresOtherDomains: the projections share the live path, so each has to
// leave events it does not own alone.
func TestApplyConfigEventIgnoresOtherDomains(t *testing.T) {
	useExampleSite(t)

	before := len(loadFriends())
	applyConfigEvent(testEvent(EvPhotoAdded, `{"id":"dddd000000000009"}`))
	applyConfigEvent(testEvent("something_else", `{}`))
	if after := len(loadFriends()); after != before {
		t.Errorf("friend count moved from %d to %d on an event of another domain", before, after)
	}
}

// TestApplyHeroEventLandsTagsAndAvatar: the hero's record reaches config/hero.json. The two
// fields travel on separate events, so a repeated tag delivery must leave the avatar alone.
func TestApplyHeroEventLandsTagsAndAvatar(t *testing.T) {
	useExampleSite(t)

	tags := `{"tags":["probe-one","probe-two"]}`
	applyConfigEvent(testEvent(EvHeroTags, tags))
	applyConfigEvent(testEvent(EvHeroAvatar, `{"avatar":["/static/image/avatar.jxl"]}`))
	applyConfigEvent(testEvent(EvHeroTags, tags))

	cfg := loadHeroConfig()
	if !slices.Equal(cfg.Tags, []string{"probe-one", "probe-two"}) {
		t.Errorf("Tags = %v, want the payload's row", cfg.Tags)
	}
	if !slices.Equal(cfg.Avatar, []string{"/static/image/avatar.jxl"}) {
		t.Errorf("Avatar = %v, want the payload's record: the second tag delivery cleared it", cfg.Avatar)
	}
}

// TestApplySiteEventMovesOnlyTheFieldsItCarries: the payload is a delta, so the keys the panel
// does not own survive on the node receiving the edit — base_url names that node, and
// comment_salt is the comment-token salt.
func TestApplySiteEventMovesOnlyTheFieldsItCarries(t *testing.T) {
	useExampleSite(t)

	seed := loadAppConfig()
	seed.CommentSalt = "probe-salt"
	seed.BaseURL = "https://probe.example"
	if err := saveAppConfig(seed); err != nil {
		t.Fatalf("seed site config: %v", err)
	}

	applyConfigEvent(testEvent(EvSiteEdited, `{"site_name":"Probe Site","motto":"probe motto"}`))

	cfg := loadAppConfig()
	if cfg.SiteName != "Probe Site" || cfg.Motto != "probe motto" {
		t.Errorf("SiteName = %q, Motto = %q; want the payload's", cfg.SiteName, cfg.Motto)
	}
	if cfg.CommentSalt != "probe-salt" {
		t.Errorf("comment_salt = %q, want it untouched: the delta does not carry it", cfg.CommentSalt)
	}
	if cfg.BaseURL != "https://probe.example" {
		t.Errorf("base_url = %q, want it untouched: it names the node, not the site", cfg.BaseURL)
	}
}

// TestAdminHeroSaveTravelsThroughTheLog: the panel's save has to reach the log, not just the
// file. A node that never saw the edit renders the hero from the record, so an edit that
// stopped at the file would leave that node drawing the old row forever.
func TestAdminHeroSaveTravelsThroughTheLog(t *testing.T) {
	useExampleSite(t)

	post := httptest.NewRequest("POST", "/admin/commit/hero", strings.NewReader("tags=probe-one%2Cprobe-two"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAdminHeroSave(httptest.NewRecorder(), post)

	if got := loadHeroConfig().Tags; !slices.Equal(got, []string{"probe-one", "probe-two"}) {
		t.Fatalf("Tags = %v after the panel save", got)
	}
	if n := countEvents(EvHeroTags); n != 1 {
		t.Errorf("%s appears %d time(s) in the log, want 1", EvHeroTags, n)
	}
}

// TestAdminConfigSaveTravelsThroughTheLog: the same rule for the site form.
func TestAdminConfigSaveTravelsThroughTheLog(t *testing.T) {
	useExampleSite(t)

	post := httptest.NewRequest("POST", "/admin/commit/config", strings.NewReader("site_name=Probe+Site"))
	post.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAdminConfigSave(httptest.NewRecorder(), post)

	if got := loadAppConfig().SiteName; got != "Probe Site" {
		t.Fatalf("SiteName = %q after the panel save", got)
	}
	if n := countEvents(EvSiteEdited); n != 1 {
		t.Errorf("%s appears %d time(s) in the log, want 1", EvSiteEdited, n)
	}
}

// countEvents counts the committed events of one type.
func countEvents(typ string) int {
	var n int
	_ = scanEvents(func(evt *Event) bool {
		if evt.Type == typ {
			n++
		}
		return true
	})
	return n
}

// TestConfigRecordsHaveOneWriter: these three files are written by the projection in
// config_state.go and by nothing else. A handler that writes one directly bypasses the log, so
// a node that never saw the edit would never learn of it.
func TestConfigRecordsHaveOneWriter(t *testing.T) {
	allowed := map[string]bool{"config_state.go": true, "main.go": true}
	writers := []string{"saveFriends(", "saveSponsors(", "saveBgConfig("}

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
		var found bool
		for _, w := range writers {
			if bytes.Contains(body, []byte(w)) {
				found = true
			}
		}
		if !found {
			continue
		}
		hits++
		if !allowed[f] {
			t.Errorf("%s writes the site's config directly; the projection in config_state.go owns it", f)
		}
	}

	// A guard that scanned nothing, or found no writer at all, proves nothing.
	if scanned < 20 {
		t.Fatalf("scanned only %d source file(s)", scanned)
	}
	if hits != len(allowed) {
		t.Errorf("found a config writer in %d file(s), want %d", hits, len(allowed))
	}
}
