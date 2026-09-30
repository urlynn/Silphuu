package main

import (
	"bytes"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// withIsolatedRoots points the engine and deployment roots at separate temporary
// directories, so a test never reads the real trees. engineRoot is not settable through
// InitRoots, so it is assigned directly and restored on cleanup.
func withIsolatedRoots(t *testing.T) (site, engine string) {
	t.Helper()
	site, engine = t.TempDir(), t.TempDir()
	oldEngine := engineRoot
	engineRoot = engine
	InitRoots(site, site)
	t.Cleanup(func() {
		engineRoot = oldEngine
		InitRoots("")
	})
	return site, engine
}

func recordAvatar(t *testing.T, urls ...string) {
	t.Helper()
	if err := saveHeroConfig(HeroConfig{Avatar: urls}); err != nil {
		t.Fatalf("save hero record: %v", err)
	}
}

// The record is the whole input: the last file it names is what every browser gets, and the
// earlier ones are offered by type. Nothing here consults a disk, which is what lets a node
// that does not hold the site's media render the same page as one that does.
func TestAvatarPictureSplitsTheRecordByPreference(t *testing.T) {
	withIsolatedRoots(t)
	recordAvatar(t, "/static/image/avatar.jxl", "/static/image/avatar.avif")

	pic := avatarURL()
	if pic == nil {
		t.Fatal("avatarURL = nil, want the recorded avatar")
	}
	if pic.Src != "/static/image/avatar.avif" {
		t.Errorf("Src = %q, want the last recorded file", pic.Src)
	}
	want := []avatarSource{{URL: "/static/image/avatar.jxl", Type: "image/jxl"}}
	if !slices.Equal(pic.Sources, want) {
		t.Errorf("Sources = %v, want %v", pic.Sources, want)
	}
}

// A record with one file has nothing to negotiate: the browser gets that file, and no
// <source> is emitted at all.
func TestAvatarRecordWithOneFileOffersNoSource(t *testing.T) {
	withIsolatedRoots(t)
	recordAvatar(t, "/static/image/avatar.svg")

	pic := avatarURL()
	if pic == nil || pic.Src != "/static/image/avatar.svg" {
		t.Fatalf("avatarURL = %v, want the recorded file as Src", pic)
	}
	if len(pic.Sources) != 0 {
		t.Errorf("Sources = %v, want none", pic.Sources)
	}
}

// No record means no avatar, and the templates guard the whole element with {{with}} — so
// the answer has to be nil rather than an empty picture, which would render a broken <img>.
func TestAvatarWithoutARecordIsNil(t *testing.T) {
	withIsolatedRoots(t)
	if pic := avatarURL(); pic != nil {
		t.Fatalf("avatarURL = %+v, want nil", pic)
	}
}

// An extension the engine cannot name a MIME type for cannot be offered as a <source>: the
// browser would have no type to negotiate on. It can still be the file every browser gets,
// which is why the record's last entry is taken as Src unconditionally.
func TestAvatarSourceWithoutAKnownMIMETypeIsNotOffered(t *testing.T) {
	withIsolatedRoots(t)
	recordAvatar(t, "/static/image/avatar.png", "/static/image/avatar.avif")

	pic := avatarURL()
	if pic == nil {
		t.Fatal("avatarURL = nil")
	}
	if len(pic.Sources) != 0 {
		t.Errorf("Sources = %v, want none: .png carries no MIME type this engine names", pic.Sources)
	}
	if pic.Src != "/static/image/avatar.avif" {
		t.Errorf("Src = %q, want the last recorded file", pic.Src)
	}
}

// The art for an error page follows from the code, so there is nothing to discover and
// nothing to walk: a site that ships no art for a code renders a broken image.
func TestErrorImageURLFollowsTheCode(t *testing.T) {
	withIsolatedRoots(t)
	if got := errorImageURL("404"); got != StaticPrefix+"/image/error/404.avif" {
		t.Fatalf("errorImageURL = %q, want the URL for the code", got)
	}
}

// heroAvatarRecord turns what the pipeline wrote into the record. A transcode writes both
// formats; a passthrough writes the one it was handed.
func TestHeroAvatarRecordNamesTheFilesThePipelineWrote(t *testing.T) {
	withIsolatedRoots(t)

	both := []string{StaticPrefix + "/image/avatar.jxl", StaticPrefix + "/image/avatar.avif"}
	if got := heroAvatarRecord("jxl+avif"); !slices.Equal(got, both) {
		t.Errorf("heroAvatarRecord(jxl+avif) = %v, want %v", got, both)
	}
	for _, one := range []string{"svg", "jxl", "avif"} {
		want := []string{StaticPrefix + "/image/avatar." + one}
		if got := heroAvatarRecord(one); !slices.Equal(got, want) {
			t.Errorf("heroAvatarRecord(%s) = %v, want %v", one, got, want)
		}
	}
}

// An svg is stored exactly as it arrived — rasterizing it would lose vectorness — and the
// record follows the file. The endpoint is the only place that knows what was written.
func TestHeroAvatarUploadStoresAnSVGVerbatim(t *testing.T) {
	withIsolatedRoots(t)
	// The endpoint commits the avatar record to the event log, so the store has to be up: with
	// no log it answers an error instead of a record no other node would learn about.
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	t.Cleanup(closeEventStore)

	const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"></svg>`
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "me.svg")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(svg)); err != nil {
		t.Fatalf("write part: %v", err)
	}
	mw.Close()

	req := httptest.NewRequest("POST", "/admin/commit/hero/avatar", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleAdminHeroAvatar(rec, req)
	if rec.Code != 200 {
		t.Fatalf("upload returned %d: %s", rec.Code, rec.Body.String())
	}

	stored := filepath.Join(RootStatic, "image", "avatar.svg")
	data, err := os.ReadFile(stored)
	if err != nil {
		t.Fatalf("read the stored avatar: %v", err)
	}
	if string(data) != svg {
		t.Errorf("stored bytes = %q, want the upload verbatim", data)
	}
	if got := loadHeroConfig().Avatar; !slices.Equal(got, []string{StaticPrefix + "/image/avatar.svg"}) {
		t.Errorf("record = %v, want the file that was written", got)
	}
}

// The URL is fixed, so a replaced image reaches the edge by purging that address rather than
// by changing it. staticImageURLs is what carries the avatar into the purge set; the walk
// alone would miss it on a node that holds no media, which is why the record is unioned in.
// The record alone must NOT put a URL into the blanket purge set.
//
// A URL with no bytes on this node cannot be compared, and the fingerprint sends what it cannot
// compare on EVERY flush. One such URL is affordable; a family of them is a quota bomb. The
// site's stickers are 1073 files whose URLs are FIXED — a post or a comment names them, so a
// change has to be purged rather than hash-addressed — and a blanket invalidation carrying all
// of them from a node that does not hold the tree would send 1073 URLs per flush, every one a
// wrong-origin fetch.
//
// The record still decides what the PAGES render (avatarURL), which is what it is for. What
// reaches the edge is decided by bytes this node holds: an upload writes its files here
// (ProcessMediaPipeline → DirImage), so the walk covers the avatar on the node that took it, and
// a change made elsewhere is purged by that change. See staticImageURLs.
func TestRecordedAvatarWithoutTheBytesStaysOutOfThePurgeSet(t *testing.T) {
	withIsolatedRoots(t)
	recordAvatar(t, StaticPrefix+"/image/avatar.avif")

	if urls := staticImageURLs(); slices.Contains(urls, StaticPrefix+"/image/avatar.avif") {
		t.Fatalf("a record naming a file this node does not have put %s into the purge set %v: "+
			"nothing here can compare it, so every flush would send it",
			StaticPrefix+"/image/avatar.avif", urls)
	}
}

// The record drives the markup, so the same page comes out of a node that holds the site's
// media and one that does not. That is the property the whole record exists for: the bytes
// live on one node, the pages are built on another.
func TestHomeAvatarMarkupComesFromTheRecord(t *testing.T) {
	useExampleSite(t)
	backfillPosts()

	render := func() string {
		// Render fresh: the test is about the markup a render produces, so the cached page
		// must not answer for it. Dropping that one file is what forces the render — an
		// invalidation no longer wipes the tree, so it would leave the file in place.
		if err := os.Remove(filepath.Join(RootRender, "index.html")); err != nil && !os.IsNotExist(err) {
			t.Fatalf("drop the home cache: %v", err)
		}
		rec := httptest.NewRecorder()
		handleHome(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 200 {
			t.Fatalf("handleHome returned %d", rec.Code)
		}
		return rec.Body.String()
	}

	holding := render()
	if !strings.Contains(holding, StaticPrefix+"/image/avatar.avif") {
		t.Fatalf("the home page carries no avatar image: %s", holding[:min(400, len(holding))])
	}
	if strings.Contains(holding, "data-fb") {
		t.Errorf("the avatar markup still carries a fallback chain: %s", holding[:min(400, len(holding))])
	}

	// Now take the site's media away, as on a node that only renders pages.
	if err := os.RemoveAll(filepath.Join(RootStatic, "image")); err != nil {
		t.Fatalf("remove static/image: %v", err)
	}
	without := render()
	if at := strings.Index(without, StaticPrefix+"/image/avatar.avif"); at < 0 {
		t.Errorf("the avatar left the markup with the bytes: %s", without[:min(400, len(without))])
	}
}

// Every recorded format ahead of the last one is offered as a <source type>, which a browser
// resolves by whether it can decode the type — no request is made for one it cannot use. The
// last entry is the <img src>, the file every browser gets.
func TestHomeAvatarMarkupOffersEachRecordedFormat(t *testing.T) {
	useExampleSite(t)
	backfillPosts()
	recordAvatar(t, StaticPrefix+"/image/avatar.jxl", StaticPrefix+"/image/avatar.avif")
	if err := os.Remove(filepath.Join(RootRender, "index.html")); err != nil && !os.IsNotExist(err) {
		t.Fatalf("drop the home cache: %v", err)
	}

	rec := httptest.NewRecorder()
	handleHome(rec, httptest.NewRequest("GET", "/", nil))
	body := rec.Body.String()

	// The minifier drops the quotes around an attribute value, so the URL and the type are
	// located rather than an exact tag.
	for _, want := range []string{StaticPrefix + "/image/avatar.jxl", "image/jxl", StaticPrefix + "/image/avatar.avif"} {
		if !strings.Contains(body, want) {
			t.Errorf("the avatar markup carries no %q: %s", want, body[:min(600, len(body))])
		}
	}
	if strings.Index(body, StaticPrefix+"/image/avatar.jxl") > strings.Index(body, StaticPrefix+"/image/avatar.avif") {
		t.Errorf("the sources come after the fallback: %s", body[:min(600, len(body))])
	}
}
