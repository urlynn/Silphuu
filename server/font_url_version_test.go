package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFontFaceURLsCarryTheSubsetToken pins the link between a subset's bytes and the URL
// that names it. The served tree answers /assets/ as immutable, so a subset regenerated
// under a fixed name is never fetched again; the token is the only thing that can move.
func TestFontFaceURLsCarryTheSubsetToken(t *testing.T) {
	withIsolatedRoots(t)
	fontPath := filepath.Join(RootAssets, "font", "LemiMuhe.woff2")
	if err := os.MkdirAll(filepath.Dir(fontPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontPath, []byte("first generation"), 0o644); err != nil {
		t.Fatal(err)
	}

	const css = "@font-face{font-family:'LemiMuhe';src:url('/assets/font/LemiMuhe.woff2') format('woff2')}"
	first := rewriteFontFaceURLs(css)
	firstHash, err := computeFileHash(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "url('/assets/font/LemiMuhe.woff2?v="+firstHash+"')") {
		t.Fatalf("the font URL carries no token for its own bytes: %s", first)
	}

	// A subset is regenerated in place: the bytes change, the path does not.
	if err := os.WriteFile(fontPath, []byte("second generation"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := rewriteFontFaceURLs(css)
	if second == first {
		t.Fatal("a regenerated subset kept the previous token; its bytes are unreachable")
	}
	if strings.Contains(second, firstHash) {
		t.Fatalf("the URL still names the previous generation: %s", second)
	}
}

// TestFontFaceURLOutsideTheAssetsPrefixIsLeftAlone: a URL this tree does not serve must not
// be given a token, because the token would promise bytes the tree never had.
func TestFontFaceURLOutsideTheAssetsPrefixIsLeftAlone(t *testing.T) {
	withIsolatedRoots(t)
	const css = "@font-face{src:url('https://example.invalid/x.woff2')}"
	if got := rewriteFontFaceURLs(css); got != css {
		t.Fatalf("a foreign URL was rewritten: %s", got)
	}
}

// TestFontURLMapCarriesTheSameTokenAsTheStylesheet keeps the 103 preload list on the bytes
// the stylesheet asks for. A preload naming the previous generation warms nothing, and the
// mismatch is silent: the browser fetches whatever the CSS says either way.
func TestFontURLMapCarriesTheSameTokenAsTheStylesheet(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	const css = "@font-face{font-family:'LemiMuhe';src:url('/assets/font/LemiMuhe.woff2') format('woff2')}"

	srcDir := filepath.Join(site, "src", "css")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "fonts.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
	fontPath := filepath.Join(RootAssets, "font", "LemiMuhe.woff2")
	if err := os.MkdirAll(filepath.Dir(fontPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontPath, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	refreshFontURLMap()
	m := fontURLMap.Load()
	if m == nil {
		t.Fatal("the font URL map was never published")
	}
	got := (*m)["LemiMuhe"]
	if got == "" {
		t.Fatal("the map dropped a family the stylesheet declares")
	}
	if want := rewriteFontFaceURLs(css); !strings.Contains(want, got) {
		t.Fatalf("103 would preload %q, the stylesheet asks for %q", got, want)
	}
}

// TestBundledStylesheetCarriesTheFontToken covers the wiring, not the rewrite: the bundle a
// browser downloads is what has to carry the token, and it is assembled by concatFiles.
func TestBundledStylesheetCarriesTheFontToken(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	const css = "@font-face{font-family:'LemiMuhe';src:url('/assets/font/LemiMuhe.woff2') format('woff2')}"

	srcDir := filepath.Join(site, "src", "css")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "fonts.css"), []byte(css), 0o644); err != nil {
		t.Fatal(err)
	}
	fontPath := filepath.Join(RootAssets, "font", "LemiMuhe.woff2")
	if err := os.MkdirAll(filepath.Dir(fontPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fontPath, []byte("bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(RootAssets, "css", "core.bundle.css")
	if err := concatFiles([]string{"src/css/fonts.css"}, target); err != nil {
		t.Fatalf("concatFiles: %v", err)
	}
	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	hashStr, err := computeFileHash(fontPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "?v="+hashStr) {
		t.Fatalf("the bundle shipped the font URL without its token: %s", out)
	}
}

// TestEveryFontURLShapeGetsAToken runs the rewrite over a stylesheet that declares one of every
// URL shape a site's fonts.css may use, through the same concatFiles the served bundle is built
// with. The guards above use a single URL this test wrote, so they cannot see the failure that
// matters here: a rewrite that matches only the shape this file happens to use passes all of
// them and leaves every other shape bare in the served bundle.
//
// The shapes are listed rather than read from a stylesheet on disk. A guard that reads a path
// only one checkout has is a guard that skips, and a skipped guard reads as green; the shapes
// are what the rewrite has to cover, and they are the same for every site.
func TestEveryFontURLShapeGetsAToken(t *testing.T) {
	// One declaration per shape. The engine's own fonts.css declares no @font-face at all —
	// typefaces are content, supplied by each site — so the sample is the engine's contract
	// with those sites, not an example it ships.
	const sheet = `
@font-face { font-family:'Plain';      src:url('/assets/font/plain.woff2') format('woff2'); }
@font-face { font-family:'Double';     src:url("/assets/font/double.woff2") format('woff2'); }
@font-face { font-family:'Unquoted';   src:url(/assets/font/unquoted.woff2) format('woff2'); }
@font-face { font-family:'Spaced';     src: url( /assets/font/spaced.woff2 ) format('woff2'); }
@font-face { font-family:'Variations'; src:url('/assets/font/variations.woff2') format('woff2-variations'), url('/assets/font/variations.woff2') format('woff2'); font-weight:200 700; }
@font-face { font-family:'Scoped';     src:url('/assets/font/home/scoped-home.woff2') format('woff2'); }
@font-face { font-family:'Foreign';    src:url('https://cdn.example/font/foreign.woff2') format('woff2'); }
@font-face { font-family:'Tokened';    src:url('/assets/font/tokened.woff2?v=deadbeef') format('woff2'); }
`

	// Occurrences rather than files: the variations face names the same file twice, once per
	// format, and both have to be rewritten.
	owned := []string{
		"plain.woff2",
		"double.woff2",
		"unquoted.woff2",
		"spaced.woff2",
		"variations.woff2", // the first source, for woff2-variations
		"variations.woff2", // the second, for plain woff2
		"home/scoped-home.woff2",
	}

	site, _ := withIsolatedRoots(t)
	if err := os.MkdirAll(filepath.Join(site, "src", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(site, "src", "css", "fonts.css"), []byte(sheet), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, rel := range append(owned, "tokened.woff2") {
		p := filepath.Join(RootAssets, "font", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("stub "+rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	target := filepath.Join(RootAssets, "css", "core.bundle.css")
	if err := concatFiles([]string{"src/css/fonts.css"}, target); err != nil {
		t.Fatalf("concatFiles: %v", err)
	}
	out, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	body := string(out)

	// Precondition: the bundle really carries the declarations, so a green result below cannot
	// come from the sample having been dropped on the way in.
	if n := strings.Count(body, "@font-face"); n != strings.Count(sheet, "@font-face") {
		t.Fatalf("the bundle holds %d of %d @font-face rules:\n%s", n, strings.Count(sheet, "@font-face"), body)
	}

	// The minifier drops the quotes the source wrote, so match either shape.
	bare := regexp.MustCompile(`url\(\s*['"]?(/assets/font/[^'")?#]+\.woff2)['"]?\s*\)`)
	if m := bare.FindAllStringSubmatch(body, -1); len(m) > 0 {
		t.Errorf("%d font URLs shipped without a token: %v", len(m), m)
	}

	tokened := regexp.MustCompile(`url\(\s*['"]?(/assets/font/[^'")?]+\.woff2\?v=[0-9a-f]+)['"]?\s*\)`)
	got := tokened.FindAllStringSubmatch(body, -1)
	if len(got) != len(owned)+1 { // the occurrences above, plus the one that arrived with a token
		t.Errorf("the bundle holds %d tokened font URLs, want %d: %v", len(got), len(owned)+1, got)
	}

	// Each of the six carries the token of its own bytes, not just any token.
	for _, rel := range owned {
		hash, err := computeFileHash(filepath.Join(RootAssets, "font", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("hash %s: %v", rel, err)
		}
		if !strings.Contains(body, "/assets/font/"+rel+"?v="+hash) {
			t.Errorf("%s did not get the token of its own bytes (%s)", rel, hash)
		}
	}

	// The two that must be left alone: a URL outside the prefix belongs to another tree, and a
	// URL that already carries a token must not collect a second one.
	if !strings.Contains(body, "https://cdn.example/font/foreign.woff2") {
		t.Error("a foreign URL was rewritten; its token would promise bytes this tree never had")
	}
	if strings.Contains(body, "tokened.woff2?v=deadbeef?v=") {
		t.Error("a URL that already carried a token collected a second one")
	}
}
