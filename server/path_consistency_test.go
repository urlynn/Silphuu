package main

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGenerateStaticErrorPages(t *testing.T) {
	// Writes render/error/*.html, so it needs a deployment to write into.
	useExampleSite(t)
	if err := generateStaticErrorPages(); err != nil {
		t.Fatalf("generateStaticErrorPages failed: %v", err)
	}
	errorCodes := []string{"403", "404", "418", "429", "502"}
	imgSrc := regexp.MustCompile(`<img src="([^"]+)"`)
	for _, code := range errorCodes {
		path := filepath.Join(RootError, code+".html")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("error page %s not generated: %v", path, err)
		}
		content := string(data)
		if !strings.Contains(content, `class="card"`) {
			t.Errorf("error page %s missing .card: %s", path, content)
		}

		// The URL follows from the code alone, so the render asks no disk anything: the node
		// that renders the page may not be the node holding the site's static/ tree.
		m := imgSrc.FindStringSubmatch(content)
		if m == nil {
			t.Fatalf("error page %s has no <img src=...>: %s", path, content)
		}
		urlPath := strings.SplitN(m[1], "?", 2)[0]
		if want := StaticPrefix + "/image/error/" + code + ".avif"; urlPath != want {
			t.Errorf("error page %s image %q, want %q", path, urlPath, want)
		}
		if strings.Contains(content, "data-fb") {
			t.Errorf("error page %s still walks a fallback chain: %s", path, content)
		}
	}
}

func TestFunErrorHandler(t *testing.T) {
	initTemplates()
	loadUIStrings()
	req := httptest.NewRequest("GET", PageFunError, nil)
	w := httptest.NewRecorder()

	handleFunError(w, req)
	resp := w.Result()
	if resp.StatusCode != 200 {
		t.Fatalf("handleFunError status = %d, want 200", resp.StatusCode)
	}

	body := w.Body.String()
	errorCodes := []string{"403", "404", "418", "429", "502"}
	for _, code := range errorCodes {
		if !strings.Contains(body, "data-code="+code) && !strings.Contains(body, `data-code="`+code+`"`) {
			t.Errorf("response body missing template for error code %s", code)
		}
	}
}

func TestSvgIconPath(t *testing.T) {
	if _, err := os.Stat("src/icon/refresh.svg"); err != nil {
		t.Fatalf("src/icon/refresh.svg does not exist: %v", err)
	}
	if _, err := os.Stat("src/icon/search.svg"); err != nil {
		t.Fatalf("src/icon/search.svg does not exist: %v", err)
	}

	tmpl := loadTemplates()
	var buf strings.Builder
	// Render template to test svg func
	err := tmpl.ExecuteTemplate(&buf, "theme_fab.html", PageData{})
	if err != nil {
		t.Logf("ExecuteTemplate theme_fab.html: %v", err)
	}
}

// TestLifecyclePathsExist: the stylesheets a page can request exist, and so do the bundles
// built from them. Source is checked under src/, generated output under the served tree.
func TestLifecyclePathsExist(t *testing.T) {
	useExampleSite(t)
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles: %v", err)
	}

	sources := []string{
		"css/components/status.css",
		"css/components/fun-error.css",
		"css/components/sponsor.css",
		"css/components/modal.css",
		"css/components/friends.css",
		"css/components/archive.css",
		"css/components/gallery.css",
		"css/admin.css",
		"css/editor.css",
	}
	for _, rel := range sources {
		if _, err := os.Stat(filepath.Join(RootSrc, rel)); err != nil {
			t.Errorf("lifecycle stylesheet missing from src/: %s (%v)", rel, err)
		}
	}

	bundles := []string{
		"css/home.bundle.css",
		"css/browse.bundle.css",
		"css/article.bundle.css",
		"css/comment.bundle.css",
		"css/admin.bundle.css",
	}
	for _, rel := range bundles {
		if _, err := os.Stat(filepath.Join(RootAssets, rel)); err != nil {
			t.Errorf("lifecycle bundle missing from the served tree: %s (%v)", rel, err)
		}
	}
}

// TestAssetExistsAnywhereSeesTheServedTree: asset() falls back to an unhashed URL when a
// name is missing from the manifest. A file added to the served tree after startup belongs
// there and a typo does not, and the two are only distinguishable by looking on disk.
func TestAssetExistsAnywhereSeesTheServedTree(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, "assets", "css", "local.css"), "b{}")

	prevEngine := engineRoot
	engineRoot = t.TempDir()
	InitRoots(home, home)
	t.Cleanup(func() {
		engineRoot = prevEngine
		InitRoots("")
	})

	for _, tc := range []struct {
		path string
		want bool
	}{
		{"css/local.css", true},
		{"css/typo.css", false},
	} {
		if got := assetExistsAnywhere(tc.path); got != tc.want {
			t.Errorf("assetExistsAnywhere(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestNoPackageLevelDerivedPathSnapshot: the Dir*/Root*/File* paths are assigned by
// initDerivedPaths() during package-variable initialisation, so a package-level variable
// reading one captures the zero value. Locals resolve at call time and are fine.
func TestNoPackageLevelDerivedPathSnapshot(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}

	// A package-level declaration is either `var x = DirFoo` on one line, or `x = DirFoo`
	// inside a top-level `var (` block. Indented lines outside such a block are locals,
	// which resolve at call time and are fine.
	decl := regexp.MustCompile(`^(var\s+)?\w+\s*=\s*(Dir|Root|File)\w*\s*$`)
	inBlock := false
	scanned := 0
	var offenders []string

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		for i, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimRight(raw, "\r")
			trimmed := strings.TrimSpace(line)
			if trimmed == "var (" {
				inBlock = true
				continue
			}
			if inBlock && trimmed == ")" {
				inBlock = false
				continue
			}
			if !inBlock && (line == "" || line[0] == ' ' || line[0] == '\t') {
				continue
			}
			if decl.MatchString(trimmed) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", e.Name(), i+1, trimmed))
			}
		}
	}

	if scanned == 0 {
		t.Fatal("scanned no Go files; the guard would pass without looking at anything")
	}
	// The detector has to still match the shape it exists to catch, or this guard is dead.
	if !decl.MatchString("var photoDir = DirPhoto") {
		t.Fatal("the detector no longer matches a package-level snapshot; the guard is dead")
	}
	for _, o := range offenders {
		t.Errorf("a package-level variable reads a derived path, which is empty at that point: %s", o)
	}
}

func TestReloadTemplatesSafe(t *testing.T) {
	// 1. Verify a normal reload succeeds
	if err := reloadTemplatesSafe(); err != nil {
		t.Fatalf("reloadTemplatesSafe failed on valid templates: %v", err)
	}

	// 2. Write a temporary malformed template (unclosed tag)
	brokenFile := filepath.Join(RootTemplates, "test_broken.html")
	brokenContent := `{{define "test_broken.html"}}{{if .InvalidTag}}hello`
	if err := os.WriteFile(brokenFile, []byte(brokenContent), 0644); err != nil {
		t.Fatalf("failed to create broken template: %v", err)
	}
	defer func() {
		os.Remove(brokenFile)
		_ = reloadTemplatesSafe()
	}()

	// 3. Verify reloadTemplatesSafe captures the syntax error without panicking
	err := reloadTemplatesSafe()
	if err == nil {
		t.Errorf("expected error on broken template, got nil")
	}

	// 4. Verify the existing valid templates still render
	tmplMu.RLock()
	homeTmpl, ok := pageTemplates["home.html"]
	tmplMu.RUnlock()
	if !ok || homeTmpl == nil {
		t.Errorf("pageTemplates corrupted after broken template reload attempt")
	}
}

// picSrcGo rewrites a URL to another format. It rewrites — it does not check: the variant
// question is a format question, and the node rendering the page may not hold the tree the
// file is in, so "is it there" is not a question it can answer. image-set's type() and
// <source type> hand the choice to the browser, which is the only party that knows what it
// can decode.
func TestPicSrcGoRewritesWithoutConsultingTheDisk(t *testing.T) {
	withIsolatedRoots(t) // deliberately empty: the answer must not depend on contents

	if got := picSrcGo("/static/image/background/desktop-01.avif", "jxl"); got != "/static/image/background/desktop-01.jxl" {
		t.Fatalf("picSrcGo = %q, want the jxl sibling", got)
	}
	// A variant with no file on this node is still rewritten: another node serves it, or
	// the browser does not ask for it because it cannot decode that format anyway.
	if got := picSrcGo("/static/image/background/desktop-01.avif", "webp"); got != "/static/image/background/desktop-01.webp" {
		t.Fatalf("picSrcGo = %q, want the webp sibling whether or not it exists here", got)
	}
	// The engine's own build output is rewritten the same way.
	if got := picSrcGo("/assets/image/background/desktop-01.avif", "jxl"); got != "/assets/image/background/desktop-01.jxl" {
		t.Fatalf("picSrcGo = %q, want the /assets/ sibling", got)
	}
	// A URL that is not ours to rewrite yields nothing.
	if got := picSrcGo("https://example.com/a.avif", "jxl"); got != "" {
		t.Fatalf("picSrcGo = %q, want empty for a remote source", got)
	}
}
