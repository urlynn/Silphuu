package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// renderHome renders the home page from the current configuration, bypassing the cached
// copy: these tests are about what a render produces, so the page written by an earlier one
// must not answer for it.
func renderHome(t *testing.T) string {
	t.Helper()
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

// The engine ships a default hero.json and a site replaces the whole file — there is no
// field-level merge, so a site that records only its tags keeps no avatar rather than
// inheriting the engine's.
func TestHeroConfigIsReplacedWhole(t *testing.T) {
	useExampleSite(t)

	writeTestFile(t, FileHeroData, `{"tags": ["only-a-tag"]}`)

	cfg := loadHeroConfig()
	if !slices.Equal(cfg.Tags, []string{"only-a-tag"}) {
		t.Errorf("Tags = %v, want the site's own list", cfg.Tags)
	}
	if len(cfg.Avatar) != 0 {
		t.Errorf("Avatar = %v, want none: a field the site's file omits is absent, not inherited", cfg.Avatar)
	}
}

// A file that is not valid JSON is an empty record, not a crash: the renderer must still
// produce a page, and the startup log is where the operator learns the file is broken.
func TestHeroConfigWithBrokenJSONReadsAsEmpty(t *testing.T) {
	useExampleSite(t)
	writeTestFile(t, FileHeroData, "{not json")

	cfg := loadHeroConfig()
	if len(cfg.Tags) != 0 || len(cfg.Avatar) != 0 {
		t.Fatalf("cfg = %+v, want the zero record", cfg)
	}
}

// The tag row is the record's, at any length and in the record's order. Seeded with tags the
// engine's own default does not carry, so the assertion cannot pass by rendering the default.
func TestHomeRendersEveryRecordedTagInOrder(t *testing.T) {
	useExampleSite(t)
	backfillPosts()

	tags := []string{"probe-one", "probe-two", "probe-three", "probe-four", "probe-five"}
	if err := saveHeroConfig(HeroConfig{Tags: tags}); err != nil {
		t.Fatalf("save hero record: %v", err)
	}
	body := renderHome(t)

	previous := -1
	for _, tag := range tags {
		at := strings.Index(body, tag)
		if at < 0 {
			t.Fatalf("tag %q is missing from the rendered hero: %s", tag, body[:min(600, len(body))])
		}
		if at < previous {
			t.Errorf("tag %q renders before the tag the record puts ahead of it", tag)
		}
		previous = at
	}
}

// An empty record renders an empty row: the engine's shipped default is where a deployment
// starts, not a floor every save is padded up to.
func TestHomeRendersNoTagRowWithoutATagRecord(t *testing.T) {
	useExampleSite(t)
	backfillPosts()

	if err := saveHeroConfig(HeroConfig{}); err != nil {
		t.Fatalf("save hero record: %v", err)
	}
	body := renderHome(t)

	// The minifier drops the quotes around an attribute value, so the class is located by
	// name rather than by its exact spelling.
	if !strings.Contains(body, "hero__tags") {
		t.Errorf("the tag row container is gone; the row is empty, not absent")
	}
	for _, tag := range []string{"Rust", "GNU/Linux"} {
		if strings.Contains(body, ">"+tag+"<") {
			t.Errorf("tag %q survived an empty record", tag)
		}
	}
}

// The blog name the hero draws is the configured site name, and it comes from config/site.json
// rather than from a literal in the template: a deployment that renames itself must not have
// to edit the engine.
func TestHomeRendersTheConfiguredSiteName(t *testing.T) {
	useExampleSite(t)
	backfillPosts()

	cfg := loadAppConfig()
	cfg.SiteName = "Renamed By Config"
	if err := saveAppConfig(cfg); err != nil {
		t.Fatalf("save site config: %v", err)
	}
	body := renderHome(t)

	if !strings.Contains(body, "Renamed By Config") {
		t.Fatalf("the hero does not carry the configured site name: %s", body[:min(600, len(body))])
	}
}
