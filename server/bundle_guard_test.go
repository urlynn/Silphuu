package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// bundleNameRe matches the names concatAllBundles writes. Anything else under assets/js or
// assets/css is a file the build does not own: a page fetches a 404, or an admin-only script
// rides along in the visitor payload.
var bundleNameRe = regexp.MustCompile(`^[a-z0-9-]+\.bundle\.(js|css)$`)

// assetRefRe matches a literal asset "js/…" / asset "css/…" reference in a template.
var assetRefRe = regexp.MustCompile(`asset\s+"((?:js|css)/[^"]+)"`)

// TestServedJSAndCSSAreBundles: every script and stylesheet the engine serves is a bundle.
// The service worker is no exception to carve out — it is written to the render tree, never to
// assets/. Checked against the tree a build just produced, so a new write step fails here.
func TestServedJSAndCSSAreBundles(t *testing.T) {
	// The bundles are pre-compressed as they are written, so the compressor has to be here for
	// the assertions below to mean anything. Failing with the reason beats failing with a
	// missing file and no explanation.
	if _, err := exec.LookPath(brotliBinary()); err != nil {
		t.Fatalf("the served tree is pre-compressed with %q and it is not installed: %v", brotliBinary(), err)
	}
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles: %v", err)
	}
	for _, dir := range []string{"js", "css"} {
		entries, err := os.ReadDir(filepath.Join(RootAssets, dir))
		if err != nil {
			t.Fatalf("read assets/%s: %v", dir, err)
		}
		if len(entries) == 0 {
			t.Fatalf("assets/%s holds nothing — the guard would pass vacuously", dir)
		}
		checked := 0
		for _, e := range entries {
			// The engine's own derived names: a .br is the pre-compressed copy of the bundle
			// beside it, and a dot-prefixed name is a write in progress. Neither is a file the
			// build owns or a page can address.
			if isDerivedServedName(e.Name()) {
				continue
			}
			if !bundleNameRe.MatchString(e.Name()) {
				t.Errorf("assets/%s/%s is not a bundle — it belongs in a bundle input list", dir, e.Name())
			}
			checked++
			if _, err := os.Stat(filepath.Join(RootAssets, dir, e.Name()+brotliSuffix)); err != nil {
				t.Errorf("assets/%s/%s has no %s: nginx would serve it uncompressed and nothing else would say so",
					dir, e.Name(), brotliSuffix)
			}
		}
		if checked == 0 {
			t.Fatalf("assets/%s held only derived files — the guard checked nothing", dir)
		}
	}
}

// TestTemplatesReferenceBundlesOnly: every js/css reference in the engine's templates names a
// bundle, so no page can point at a path the build does not write. The deployment's stylesheet
// override is not an exception any more: head.html inlines src/css/site.css into the page
// instead of linking it, so nothing outside a bundle is referenced.
func TestTemplatesReferenceBundlesOnly(t *testing.T) {
	found := 0
	err := filepath.Walk(RootTemplates, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range assetRefRe.FindAllStringSubmatch(string(data), -1) {
			ref := m[1]
			found++
			if !bundleNameRe.MatchString(filepath.Base(ref)) {
				t.Errorf("%s references %q, which is not a bundle", path, ref)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if found == 0 {
		t.Fatal("no js/css asset references found — the guard would pass vacuously")
	}
}
