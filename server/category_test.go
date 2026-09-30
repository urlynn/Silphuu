package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// categoryOptionRe extracts the option values from the editor's category select, tolerating
// the minifier's unquoted attribute form (value=essay).
var categoryOptionRe = regexp.MustCompile(`(?s)<select name=?"?category"?>(.*?)</select>`)
var optionValueRe = regexp.MustCompile(`value="?([^"\s>]+)"?`)

// editorCategoryOptionValues renders the new-post editor and returns the option values of
// its category select, in document order.
func editorCategoryOptionValues(t *testing.T) []string {
	t.Helper()
	req := httptest.NewRequest("GET", RouteAdminNewPost, nil)
	req.Header.Set("X-Is-Admin", "1")
	w := httptest.NewRecorder()
	handleAdminNewPost(w, req)

	if w.Code != 200 {
		t.Fatalf("editor status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	m := categoryOptionRe.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("the editor has no select[name=category]:\n%s", body)
	}
	var out []string
	for _, v := range optionValueRe.FindAllStringSubmatch(m[1], -1) {
		out = append(out, v[1])
	}
	return out
}

// TestEditorCategoryOptionsMatchRegistry: the dropdown is the registry. Its options are
// exactly the declared keys — no literal list in the template — and a category holding no
// post yet is still selectable, so a new category needs no posts/ directory first.
func TestEditorCategoryOptionsMatchRegistry(t *testing.T) {
	useExampleSite(t)
	initTemplates()
	loadUIStrings()

	declared := loadCategoryRegistry()
	if len(declared) == 0 {
		t.Fatal("the example site declares no category; this guard would pass vacuously")
	}
	seenName := map[string]bool{}
	for _, c := range declared {
		if seenName[c.Name] {
			t.Errorf("display name %q is declared twice; it keys CategoryAliases and must be unique", c.Name)
		}
		seenName[c.Name] = true
	}

	got := editorCategoryOptionValues(t)
	if len(got) == 0 {
		t.Fatal("the editor renders no category option at all")
	}
	want := make([]string, 0, len(declared))
	for _, c := range declared {
		want = append(want, c.Key)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("editor options = %v, want the registry keys in order %v", got, want)
	}

	// A declared category with no directory must still be offered.
	dirKeys := map[string]bool{}
	for _, d := range postDirs() {
		dirKeys[d.Key] = true
	}
	offered := map[string]bool{}
	for _, k := range got {
		offered[k] = true
	}
	for _, c := range declared {
		if !dirKeys[c.Key] && !offered[c.Key] {
			t.Errorf("declared category %q has no posts/ directory and is missing from the dropdown", c.Key)
		}
	}
}

// TestEveryPostDirIsDeclared: a posts/ subdirectory the registry does not declare has no
// display name and no stable URL, so it renders under its raw directory name. That is the
// drift the registry exists to prevent, and the example site must not carry any.
func TestEveryPostDirIsDeclared(t *testing.T) {
	useExampleSite(t)

	dirs := postDirs()
	if len(dirs) == 0 {
		t.Fatal("no posts/ subdirectory found; this guard would pass without looking at anything")
	}
	if len(loadCategoryRegistry()) == 0 {
		t.Fatal("the example site declares no category; this guard would pass without looking at anything")
	}
	for _, d := range dirs {
		name := filepath.Base(d.Path)
		if _, ok := categoryByKey(name); !ok {
			t.Errorf("posts/%s is not a registry key; declare it in config/topic.json and name the directory after the key", name)
		}
	}
}

// TestNavLabelComesFromTheSiteDataFile: the server-rendered nav signature and the one the
// navbar script swaps in read the same file. A hardcoded Go table was the odd one out — it
// went stale the moment the site renamed itself, so every page painted the wrong line
// before the script replaced it.
func TestNavLabelComesFromTheSiteDataFile(t *testing.T) {
	useExampleSite(t)

	sigs := loadNavSignatures()
	if len(sigs) == 0 {
		t.Fatal("the example site declares no nav signature; this guard would pass without looking at anything")
	}
	for _, c := range loadCategoryRegistry() {
		path := "/topic/" + c.Key
		if sigs[path] == "" {
			t.Errorf("%s has no nav signature in config/nav_signatures.json", path)
		}
	}
	if got, want := navLabelForPath("/topic/essay"), sigs["/topic/essay"]; got != want {
		t.Errorf("navLabelForPath(/topic/essay) = %q, want the data file's %q", got, want)
	}
	// A path with no entry mirrors the script's own fallback.
	if got, want := navLabelForPath("/post/1"), sigs["/"]; got != want {
		t.Errorf("navLabelForPath(/post/1) = %q, want the fallback %q", got, want)
	}
}

// TestEngineNavSignaturesKnowNoCategory: the engine's own copy must not name a category.
// A category is the site's to declare, and an engine entry for one would go stale the
// moment the site renames or reorders it.
func TestEngineNavSignaturesKnowNoCategory(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("config", "nav_signatures.json"))
	if err != nil {
		t.Fatalf("read the engine's config/nav_signatures.json: %v", err)
	}
	var entries []navSignatureEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parse the engine's config/nav_signatures.json: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the engine declares no nav signature; this guard would pass without looking at anything")
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "/topic/") {
			t.Errorf("the engine's nav signatures carry a category entry %q; categories belong to the site", e.Path)
		}
	}
}

// postCatPillRe captures a category pill's href and label. The pill has one implementation
// (components.html, used by both post_card and post_article_body), so a page that renders
// it differently is reading a different Post field.
var postCatPillRe = regexp.MustCompile(`<a href=["']?([^"'\s>]+)["']? class=["']?post-cat["']?>\s*<span class=["']?pill-label["']?>([^<]*)</span>`)

// navActiveRe captures the label of the highlighted navbar category. The minifier emits the
// class attribute with an unquoted value and a stray closing quote, hence the tolerance.
var navActiveRe = regexp.MustCompile(`nav-filter-cat["']?\s+active["']?\s*>([^<]*)`)

// catGroupOpenRe captures the display name of the sidebar group rendered open.
var catGroupOpenRe = regexp.MustCompile(`(?s)cat-group["']?\s+open["']?\s*>.*?cat-name["']?\s*>([^<]*)<`)

// TestPostCategoryIsTheDisplayName: Post.Category is both the text the templates print and
// the key they look up CategoryAliases by, so every producer must fill it with the
// registry's display name — never the posts/ directory name.
//
// Two producers disagreed once: the list page printed "随笔" while the post page printed
// "essay", because one filled the field from the registry and the other from the directory.
func TestPostCategoryIsTheDisplayName(t *testing.T) {
	useExampleSite(t)
	backfillPostIDs()
	rebuildPostIndex()

	reg := loadCategoryRegistry()
	if len(reg) == 0 {
		t.Fatal("the example site declares no category; this guard would pass vacuously")
	}
	distinct := false
	for _, c := range reg {
		if c.Key != c.Name {
			distinct = true
		}
	}
	if !distinct {
		t.Fatal("every registry entry has key == display name, so a key leaking through would be invisible")
	}

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("the example site has no post; this guard would pass without looking at anything")
	}

	aliases := getCategoryAliases()
	for _, p := range posts {
		c, ok := categoryByName(p.Category)
		if !ok {
			t.Errorf("post %d has Category %q, which is no registry display name", p.ID, p.Category)
			continue
		}
		if p.Category == c.Key {
			t.Errorf("post %d renders the registry key %q where the display name %q belongs", p.ID, c.Key, c.Name)
		}
		// Exactly what the pill's href is built from: URLTopic(index .Cats .P.Category).
		if aliases[p.Category] == "" {
			t.Errorf("post %d: CategoryAliases has no entry for %q, so its pill links to a bare /topic/", p.ID, p.Category)
		}
	}

	// loadPostWithContent is the post page's producer — the one that read the directory name.
	for _, p := range posts {
		full, ok := loadPostWithContent(p.ID)
		if !ok {
			t.Fatalf("loadPostWithContent(%d) found no file, but the index lists it", p.ID)
		}
		if full.Category != p.Category {
			t.Errorf("loadPostWithContent(%d).Category = %q, want the index's %q", p.ID, full.Category, p.Category)
		}
	}
}

// TestPostPageCategoryPillMatchesTheRegistry: the producer guard above only checks the
// Post values. This one checks what a reader actually gets — the pill's text and href, the
// navbar highlight and the sidebar's open group, all of which go through the same
// CurrentCat and CategoryAliases lookup and all of which went wrong together.
func TestPostPageCategoryPillMatchesTheRegistry(t *testing.T) {
	useExampleSite(t)
	initTemplates()
	loadUIStrings()
	backfillPostIDs()
	rebuildPostIndex()

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("the example site has no post; this guard would pass without looking at anything")
	}
	p := posts[0]
	c, ok := categoryByName(p.Category)
	if !ok {
		t.Fatalf("post %d has Category %q, which is no registry display name", p.ID, p.Category)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /post/{id}", handlePostByID)
	req := httptest.NewRequest("GET", fmt.Sprintf("/post/%d", p.ID), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /post/%d = %d, want 200", p.ID, w.Code)
	}
	body := w.Body.String()

	pills := postCatPillRe.FindAllStringSubmatch(body, -1)
	if len(pills) == 0 {
		t.Fatalf("the post page renders no category pill; this guard would pass without looking at anything")
	}
	for _, pill := range pills {
		if pill[2] != c.Name {
			t.Errorf("pill label = %q, want the display name %q", pill[2], c.Name)
		}
		if want := "/topic/" + c.Key; pill[1] != want {
			t.Errorf("pill href = %q, want %q", pill[1], want)
		}
	}

	// CurrentCat is the post's category compared against getCategories()' display names.
	m := navActiveRe.FindStringSubmatch(body)
	if m == nil {
		t.Errorf("no navbar category is highlighted on /post/%d", p.ID)
	} else if m[1] != c.Name {
		t.Errorf("the highlighted navbar category is %q, want %q", m[1], c.Name)
	}

	open := catGroupOpenRe.FindStringSubmatch(body)
	if open == nil {
		t.Errorf("no sidebar category group is expanded on /post/%d", p.ID)
	} else if open[1] != c.Name {
		t.Errorf("the expanded sidebar group is %q, want %q", open[1], c.Name)
	}
}
