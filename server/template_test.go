package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestTemplateTags(t *testing.T) {
	src := `<span class="test"><b>999</b><i>阅读</i></span>`
	tmpl := template.Must(template.New("test").Parse(src))
	var buf strings.Builder
	tmpl.Execute(&buf, nil)
	result := buf.String()
	t.Logf("Input:  %s", src)
	t.Logf("Output: %s", result)
	if !strings.Contains(result, "<b>") {
		t.Error("<b> tag was stripped by html/template!")
	}
}

func TestTemplateTagsWithSVG(t *testing.T) {
	svg := template.HTML(`<svg viewBox="0 0 24 24"><path d="M1 12s4-8 11-8"/></svg>`)
	src := `<span class="test">{{.SVG}}<b>999</b><i>阅读</i></span>`
	tmpl := template.Must(template.New("test").Parse(src))
	var buf strings.Builder
	tmpl.Execute(&buf, map[string]interface{}{"SVG": svg})
	result := buf.String()
	t.Logf("Output: %s", result)
	if !strings.Contains(result, "<b>") {
		t.Error("<b> tag was stripped by html/template when preceded by template.HTML!")
	}
}

func TestBuildAssets(t *testing.T) {
	useExampleSite(t)
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles failed: %v", err)
	}
}

func TestParseFrontmatterSummary(t *testing.T) {
	raw := "---\nid: 99\ntitle: 测试文章\nsummary: 这是作者手动写的摘要，不应被截断\n---\n# 正文标题\n正文内容。"
	title, _, _, body, _, id, summary := parseFrontmatter(raw)
	if id != 99 || title != "测试文章" {
		t.Errorf("parse error: id=%d, title=%s", id, title)
	}
	if summary != "这是作者手动写的摘要，不应被截断" {
		t.Errorf("summary mismatch: got %q", summary)
	}
	if !strings.Contains(body, "正文内容") {
		t.Errorf("body missing: got %q", body)
	}
}

func TestAdminPageRender(t *testing.T) {
	req := httptest.NewRequest("GET", "/admin", nil)
	req.Header.Set("X-Is-Admin", "1")
	rec := httptest.NewRecorder()
	handleAdmin(rec, req)

	res := rec.Result()
	if res.StatusCode != 200 {
		t.Fatalf("handleAdmin returned status %d", res.StatusCode)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "profile-card page-card admin-dashboard") {
		t.Error("admin page missing profile-card page-card admin-dashboard")
	}
	if !strings.Contains(body, "系统运维与缓存重建") {
		t.Error("admin page missing maintenance card title")
	}
	if !strings.Contains(body, "一键全量重建") {
		t.Error("admin page missing maintenance rebuild all button")
	}
}

func TestCommitRebuildEndpoints(t *testing.T) {
	// The endpoints rebuild for real. Without a deployment of its own, homeRoot stays the
	// working directory and the bundles, themes.json and render cache land in the checkout.
	useExampleSite(t)
	targets := []string{"posts", "sticker", "bundle", "sync"}
	for _, target := range targets {
		req := httptest.NewRequest("POST", "/commit/rebuild?target="+target, nil)
		req.Header.Set("X-Is-Admin", "1")
		rec := httptest.NewRecorder()
		handleCommitRebuild(rec, req)
		res := rec.Result()
		if res.StatusCode != 200 && res.StatusCode != 303 {
			t.Errorf("handleCommitRebuild target=%s returned %d", target, res.StatusCode)
		}
	}
}

func TestAdminPostTogglePin(t *testing.T) {
	useExampleSite(t)

	backfillPosts()
	allPosts := loadPostsFromDB()
	if len(allPosts) < 2 {
		t.Skip("less than 2 posts, skipping pin toggle test")
	}

	// Normalize the ambient pinned_post_id to 0: this test flips the value via the
	// handler and asserts absolute states, so it must not depend on whatever the
	// previous run left behind in config/site.json.
	cfgReset := loadAppConfig()
	cfgReset.PinnedPostID = 0
	if data, err := json.MarshalIndent(cfgReset, "", "  "); err == nil {
		if err := os.WriteFile(FileConfig, data, 0644); err != nil {
			t.Fatalf("failed to reset pinned_post_id: %v", err)
		}
	}

	targetPostID := allPosts[1].ID

	// 1. Unauthenticated request must be redirected (303)
	unauthReq := httptest.NewRequest("POST", "/commit/post/pin", strings.NewReader("post_id="+strconv.Itoa(targetPostID)))
	unauthReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unauthRec := httptest.NewRecorder()
	handleAdminPostTogglePin(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusSeeOther {
		t.Errorf("unauthenticated pin request returned code %d, want %d", unauthRec.Code, http.StatusSeeOther)
	}

	// 2. Admin request: pin the post
	form := url.Values{}
	form.Set("post_id", strconv.Itoa(targetPostID))
	authReq := httptest.NewRequest("POST", "/commit/post/pin", strings.NewReader(form.Encode()))
	authReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authReq.Header.Set("X-Is-Admin", "1")
	authRec := httptest.NewRecorder()
	handleAdminPostTogglePin(authRec, authReq)

	cfg := loadAppConfig()
	if cfg.PinnedPostID != targetPostID {
		t.Errorf("expected PinnedPostID=%d, got %d", targetPostID, cfg.PinnedPostID)
	}

	// 3. Admin request: submit the same post again, toggling the pin off (reset to 0)
	authReq2 := httptest.NewRequest("POST", "/commit/post/pin", strings.NewReader(form.Encode()))
	authReq2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	authReq2.Header.Set("X-Is-Admin", "1")
	authRec2 := httptest.NewRecorder()
	handleAdminPostTogglePin(authRec2, authReq2)

	cfg2 := loadAppConfig()
	if cfg2.PinnedPostID != 0 {
		t.Errorf("expected PinnedPostID=0 after toggle, got %d", cfg2.PinnedPostID)
	}
}

func TestHomeScheme4A12Rendering(t *testing.T) {
	useExampleSite(t)

	backfillPosts()
	allPosts := loadPostsFromDB()
	if len(allPosts) < 2 {
		t.Skip("less than 2 posts, skipping home rendering test")
	}

	// Scenario A: a pinned post exists (pin allPosts[1].ID; allPosts[0] is the latest)
	pinnedID := allPosts[1].ID
	cfg := loadAppConfig()
	cfg.PinnedPostID = pinnedID
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal config error: %v", err)
	}
	if err := os.WriteFile(FileConfig, data, 0644); err != nil {
		t.Fatalf("write config error: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handleHome(rec, req)

	if rec.Code != 200 {
		t.Fatalf("handleHome returned status %d", rec.Code)
	}
	body := rec.Body.String()

	// Verify the key elements of the pinned card
	expectedPinned := []string{
		"has-ribbon has-badge",
		"card-ribbon-clip",
		"corner-ribbon",
		"PINNED",
		"badge-top-left pinned",
		"置顶",
		"home-post-card__footer",
	}
	for _, exp := range expectedPinned {
		if !strings.Contains(body, exp) {
			t.Errorf("home page with pinned post missing element: %s", exp)
		}
	}

	// Verify the key elements of the latest-post card
	expectedLatest := []string{
		"badge-top-left badge-new",
		"最新",
	}
	for _, exp := range expectedLatest {
		if !strings.Contains(body, exp) {
			t.Errorf("home page missing latest post element: %s", exp)
		}
	}

	// Verify visual cleanliness: the legacy swallowtail classes must never reappear
	if strings.Contains(body, "corner-swallowtail") || strings.Contains(body, "swallowtail") {
		t.Errorf("home page should not contain swallowtail elements")
	}

	// Scenario B: no pinned post (PinnedPostID = 0)
	cfg.PinnedPostID = 0
	data0, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(FileConfig, data0, 0644)

	rec0 := httptest.NewRecorder()
	handleHome(rec0, req)
	if rec0.Code != 200 {
		t.Fatalf("handleHome with 0 pinned returned status %d", rec0.Code)
	}
	body0 := rec0.Body.String()

	// Must not contain the pinned ribbon or badge
	if strings.Contains(body0, "card-ribbon-clip") || strings.Contains(body0, "corner-ribbon") || strings.Contains(body0, "badge-top-left pinned") {
		t.Errorf("home page without pinned post should not contain pinned ribbon/badge")
	}
	// Must still contain the latest-post badge
	if !strings.Contains(body0, "badge-top-left badge-new") {
		t.Errorf("home page without pinned post should still display latest post badge")
	}
}

func TestAdminPostDeleteUnpins(t *testing.T) {
	useExampleSite(t)

	testPinID := 999999
	cfg := loadAppConfig()
	cfg.PinnedPostID = testPinID
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(FileConfig, data, 0644)

	form := url.Values{}
	form.Set("post_id", strconv.Itoa(testPinID))
	req := httptest.NewRequest("POST", "/commit/post/delete", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")
	rec := httptest.NewRecorder()
	handleAdminPostDelete(rec, req)

	cfgAfter := loadAppConfig()
	if cfgAfter.PinnedPostID != 0 {
		t.Errorf("deleting pinned post should reset PinnedPostID to 0, got %d", cfgAfter.PinnedPostID)
	}
}

func TestWarmHomeStaticGeneration(t *testing.T) {
	useExampleSite(t)

	backfillPosts()
	allPosts := loadPostsFromDB()
	if len(allPosts) < 2 {
		t.Skip("less than 2 posts, skipping warm test")
	}

	oldMux := globalMux
	defer func() { globalMux = oldMux }()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleHome)
	globalMux = mux

	// Pin allPosts[1].ID and run the pre-render warm
	cfg := loadAppConfig()
	cfg.PinnedPostID = allPosts[1].ID
	data, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(FileConfig, data, 0644)

	// The cache may hold a shard directory from before the home page became a single
	// file. Drop it so the assertion below measures this run, not leftover state.
	_ = os.RemoveAll(filepath.Join(RootRender, "home"))

	warmHome()

	// One file, at the cache root: that is what lets a static web server serve "/".
	homePath := cachePath("/")
	if homePath != filepath.Join(RootRender, "index.html") {
		t.Fatalf("home cache path = %s, want the cache root so a static server can serve /", homePath)
	}

	body, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatalf("failed to read pre-rendered home: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		"card-ribbon-clip", "corner-ribbon",
		"badge-top-left pinned", "badge-top-left badge-new",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("pre-rendered home missing %q", want)
		}
	}

	// No sharded variants. One page means one file — see pickHomeBackground.
	if _, err := os.Stat(filepath.Join(RootRender, "home")); !os.IsNotExist(err) {
		t.Error("public/home/ exists; the home page must not be sharded into device or background variants")
	}
}

// TestPickHomeBackgroundsIsDeterministic: the home page is published as static HTML, so
// its backgrounds cannot vary between requests. A rotating or UA-sharded pick would need
// one HTML file per variant, which a static server cannot choose between.
func TestPickHomeBackgroundsIsDeterministic(t *testing.T) {
	firstD, firstM := pickHomeBackgrounds()
	for i := 0; i < 5; i++ {
		d, m := pickHomeBackgrounds()
		if d != firstD || m != firstM {
			t.Fatalf("pick %d = (%+v, %+v), want (%+v, %+v) — the home page must render to one static file", i, d, m, firstD, firstM)
		}
	}
}

// TestPickHomeBackgroundsSplitsByDevice: both backgrounds come from the same render, and
// the choice between them is left to a CSS media query. This is how one static file can
// still show a different picture on a phone.
func TestPickHomeBackgroundsSplitsByDevice(t *testing.T) {
	withIsolatedRoots(t)
	writeBgConfig(t, []BgItem{
		{Src: "/assets/image/background/desktop-01.avif", Device: "desktop", Tone: "light"},
		{Src: "/assets/image/background/mobile-01.avif", Device: "mobile", Tone: "dark"},
	})

	desktop, mobile := pickHomeBackgrounds()
	if desktop.Src != "/assets/image/background/desktop-01.avif" {
		t.Errorf("desktop = %q", desktop.Src)
	}
	if mobile.Src != "/assets/image/background/mobile-01.avif" {
		t.Errorf("mobile = %q", mobile.Src)
	}
	if desktop.Tone != "light" || mobile.Tone != "dark" {
		t.Errorf("tone not taken from each item: desktop %q, mobile %q", desktop.Tone, mobile.Tone)
	}
}

// TestPickHomeBackgroundsMobileFallsBackToDesktop: a site that has not supplied a mobile
// background yet must still render — it just shows the same picture everywhere.
func TestPickHomeBackgroundsMobileFallsBackToDesktop(t *testing.T) {
	withIsolatedRoots(t)
	writeBgConfig(t, []BgItem{
		{Src: "/assets/image/background/desktop-01.avif", Device: "desktop", Tone: "light"},
	})

	desktop, mobile := pickHomeBackgrounds()
	if mobile != desktop {
		t.Errorf("with no mobile background the desktop one must be used for both, got %+v vs %+v", mobile, desktop)
	}
}

func writeBgConfig(t *testing.T, items []BgItem) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"backgrounds": items})
	if err != nil {
		t.Fatal(err)
	}
	path := FileBackgroundData
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// renderHomeForBgTest renders "/" with an explicit background config (nil = none) in the
// package's own state dir, restoring whatever was there. The mux is repointed at
// handleHome the same way the warm tests do.
func renderHomeForBgTest(t *testing.T, items []BgItem) string {
	t.Helper()
	path := FileBackgroundData
	saved, hadSaved, err := readIfExists(path)
	if err != nil {
		t.Fatal(err)
	}
	if items == nil {
		_ = os.Remove(path)
	} else {
		writeBgConfig(t, items)
	}
	t.Cleanup(func() {
		_ = os.Remove(path)
		if hadSaved {
			_ = os.WriteFile(path, saved, 0o644)
		}
	})

	oldMux := globalMux
	defer func() { globalMux = oldMux }()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleHome)
	globalMux = mux

	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handleHome(rec, req)
	if rec.Code != 200 {
		t.Fatalf("handleHome returned status %d", rec.Code)
	}
	return rec.Body.String()
}

func readIfExists(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	return data, err == nil, err
}

// TestHomeFallsBackToEngineDefaultBg: with no background configured anywhere the hero
// still carries a wallpaper — the engine's own default — because a hero without one
// borrows the fixed global tangram layer, which then stays in the viewport and overlaps
// the second screen's own layer. See engineDefaultBgItem.
func TestHomeFallsBackToEngineDefaultBg(t *testing.T) {
	useExampleSite(t)
	body := renderHomeForBgTest(t, nil)

	if !strings.Contains(body, "hero--tone-dark") {
		t.Errorf("the engine default wallpaper is dark, so the hero must carry the dark tone")
	}
	if !strings.Contains(body, "<picture") {
		t.Errorf("unconfigured home must render a background <picture>")
	}
	if !strings.Contains(body, "/assets/image/background/default.avif") {
		t.Errorf("unconfigured home must point at the engine default wallpaper")
	}
	if !strings.Contains(body, "--ox:") {
		t.Errorf("unconfigured home must emit the default offsets")
	}
	// The rendered path only proves the template asked for it; picSrc resolves a sibling
	// only when the file is there, and an absent one renders as an empty src.
	if _, err := os.Stat(filepath.Join(RootAssets, "image", "background", "default.avif")); err != nil {
		t.Errorf("the default wallpaper is missing from the site: %v", err)
	}
}

// TestHomeRendersConfiguredBgMarkers: a configured background keeps the per-image
// machinery — tone class, <picture>, and the inline offsets.
func TestHomeRendersConfiguredBgMarkers(t *testing.T) {
	body := renderHomeForBgTest(t, []BgItem{
		{Src: "/assets/image/avatar.avif", Device: "desktop", Tone: "light", OffsetX: 0.5, OffsetY: 0.5, AvatarPos: "left"},
	})

	if !strings.Contains(body, "hero--tone-light") {
		t.Errorf("background home missing the tone marker")
	}
	if !strings.Contains(body, "<picture") {
		t.Errorf("background home missing the background <picture>")
	}
	if !strings.Contains(body, "--ox:") {
		t.Errorf("background home missing inline offsets")
	}
}

// TestHomeHeroSceneAndFooter: the hero's four cards sample .hero__bg through Scene A, so
// they carry no .g-content (the clone lands there, and Scene A never clones). The page
// also renders exactly one footer: home's own footer rides the second screen, so the
// layout-level one is blanked. See docs/GOTCHAS.md §home-layout-footer.
func TestHomeHeroSceneAndFooter(t *testing.T) {
	body := renderHomeForBgTest(t, nil)

	if got := strings.Count(body, "<footer"); got != 1 {
		t.Errorf("home renders %d footers, want exactly 1", got)
	}

	start := strings.Index(body, `<section class="hero`)
	if start < 0 {
		t.Fatalf("no hero section in the render")
	}
	end := strings.Index(body[start:], "</section>")
	if end < 0 {
		t.Fatalf("hero section is never closed")
	}
	hero := body[start : start+end]

	// The rendered page may be minified, which drops optional attribute quotes:
	// data-backdrop-scene="a" becomes data-backdrop-scene=a.
	countScene := func(html, want string) int {
		return strings.Count(html, `data-backdrop-scene="`+want+`"`) +
			strings.Count(html, `data-backdrop-scene=`+want)
	}
	if got := countScene(hero, "a"); got != 4 {
		t.Errorf("hero: %d Scene A cards, want 4 (hero__text plus three tags)", got)
	}
	if countScene(hero, "b") != 0 {
		t.Errorf("hero must stay Scene A — its .hero__bg is the sampling source")
	}
	if strings.Contains(hero, "g-content") {
		t.Errorf("hero must carry no .g-content slots — those belong to Scene B")
	}
}

func TestHomeHoverStylesAndFontAssignment(t *testing.T) {
	// Build the bundle into a deployment of its own to ensure a clean read.
	useExampleSite(t)
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles failed: %v", err)
	}
	bundleBytes, err := os.ReadFile(filepath.Join(RootAssets, "css", "home.bundle.css"))
	if err != nil {
		t.Fatalf("failed to read home.bundle.css: %v", err)
	}
	css := string(bundleBytes)
	// The bundle may be minified (concat minifies in-process; failures keep it raw), so
	// every needle below must survive whitespace removal.
	flat := strings.ReplaceAll(css, " ", "")

	// 1. Hover coloring is strictly limited to the title; footer categories and
	//    tags must never recolor passively on hover
	if strings.Contains(css, ".home-post-card:hover .footer-item.cat") {
		t.Errorf("home.bundle.css should NOT color category on card hover")
	}
	if strings.Contains(css, ".home-post-card:hover .footer-tag") {
		t.Errorf("home.bundle.css should NOT color tags on card hover")
	}
	if !strings.Contains(css, ".home-post-card:hover .home-post-card__title") {
		t.Errorf("home.bundle.css missing card hover title color transition")
	}

	// 2. The PINNED ribbon uses display-mono
	if !strings.Contains(flat, "font-family:var(--font-display-mono") {
		t.Errorf("corner ribbon missing font-display-mono assignment")
	}

	// 3. Dates and numbers use font-data
	if !strings.Contains(css, ".home-post-card__footer .footer-item.date {\n    font-family: var(--font-data)") &&
		!strings.Contains(css, ".footer-item.date") {
		t.Errorf("footer date missing font-data assignment")
	}
	if !strings.Contains(css, ".words-num {\n    font-family: var(--font-data)") &&
		!strings.Contains(css, ".words-num") {
		t.Errorf("footer words number missing font-data assignment")
	}

	// 4. Remaining elements (categories, word-count units, badges, tags) share
	//    the body/title font (font-body)
	if !strings.Contains(flat, "font-family:var(--font-body)") {
		t.Errorf("badge-top-left missing font-body assignment")
	}
}

func TestHomeTemplateNoHardcodedSVGAndHeadings(t *testing.T) {
	// The home page's second-section CONTENT lives in the home_section_inner
	// partial of components.html, shared by home.html and the admin font
	// preview. Checks are therefore split across two files:
	//   · templates/home.html        — second-section shell (<section class="home-section">
	//                                  + tangram background layer + layout/alignment attributes)
	//   · templates/components.html  — second-section content (dual headings + stat cards + post cards)
	homeBytes, err := os.ReadFile(filepath.Join(RootTemplates, "home.html"))
	if err != nil {
		t.Fatalf("failed to read templates/home.html: %v", err)
	}
	compBytes, err := os.ReadFile(filepath.Join(RootTemplates, "components.html"))
	if err != nil {
		t.Fatalf("failed to read templates/components.html: %v", err)
	}
	html := string(homeBytes)
	comp := string(compBytes)

	// 1. Hardcoded <svg> is banned in both files; {{svg "..."}} is mandatory
	for name, src := range map[string]string{"home.html": html, "components.html": comp} {
		if strings.Contains(src, "<svg") {
			t.Errorf("templates/%s contains hardcoded <svg> tag! Must use {{svg \"...\"}} helper!", name)
		}
	}

	// 2. Icon helper references (now inside the home_section_inner partial)
	requiredIcons := []string{`{{svg "pin"}}`, `{{svg "sparkle"}}`, `{{svg "book"}}`, `{{svg "calendar"}}`, `{{svg "file"}}`}
	for _, ic := range requiredIcons {
		if !strings.Contains(comp, ic) {
			t.Errorf("templates/components.html (home_section_inner) missing svg helper call: %s", ic)
		}
	}

	// 3. Dual headings: site STATS and latest-post NEW (now inside the partial)
	if !strings.Contains(comp, `class="home-section__heading"`) {
		t.Errorf("templates/components.html (home_section_inner) missing home-section__heading")
	}
	if !strings.Contains(comp, `class="home-section__heading home-section__heading--posts"`) {
		t.Errorf("templates/components.html (home_section_inner) missing home-section__heading--posts")
	}
	if !strings.Contains(comp, `.UI.home.section.posts_kicker`) {
		t.Errorf("templates/components.html (home_section_inner) missing posts_kicker for NEW")
	}
	// 4. Second-section shell layout/alignment config (still in home.html)
	if !strings.Contains(html, `data-heading-layout="1"`) {
		t.Errorf("templates/home.html missing data-heading-layout=\"1\"")
	}
	if !strings.Contains(html, `data-heading-align="right"`) {
		t.Errorf("templates/home.html missing data-heading-align=\"right\"")
	}
	// 5. Guard: home.html must REFERENCE the partial, never re-inline the
	//    second-section content (inlining would immediately desync the admin
	//    font preview from production — the exact problem this refactor removed)
	if !strings.Contains(html, `{{template "home_section_inner"`) {
		t.Errorf("templates/home.html 必须引用 home_section_inner partial, 不要内联第二屏内容")
	}
}

func TestHomeSectionRender(t *testing.T) {
	_ = reloadTemplatesSafe()
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handleHome(rec, req)

	res := rec.Result()
	if res.StatusCode != 200 {
		t.Fatalf("handleHome returned status %d", res.StatusCode)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data-heading-layout=1") && !strings.Contains(body, `data-heading-layout="1"`) {
		t.Errorf("rendered home page missing data-heading-layout=1")
	}
	if !strings.Contains(body, "data-heading-align=right") && !strings.Contains(body, `data-heading-align="right"`) {
		t.Errorf("rendered home page missing data-heading-align=right")
	}
	if !strings.Contains(body, "home-section__posts") {
		t.Errorf("rendered home page missing home-section__posts")
	}
	if !strings.Contains(body, "POSTS") {
		t.Errorf("rendered home page missing POSTS kicker")
	}
}
