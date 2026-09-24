package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMinifyBundleEscapeSemantics: the minifier must DECODE escape sequences in
// string literals, never re-encode them. Re-encoding with a doubled backslash
// breaks every asset URL in the bundle.
func TestMinifyBundleEscapeSemantics(t *testing.T) {
	src := "var p = '\\/assets\\/image\\/background\\/desktop-01.avif';\n" +
		"var q = 'a`b${c}d\\\\e\\tf';\n" +
		"var r = '\\8\\9z';\n" +
		"var s = '中文\\/路径\\/x.avif';"
	out, err := minifyBundle(src, ".js")
	if err != nil {
		t.Fatalf("minifyBundle js: %v", err)
	}
	if !strings.Contains(out, "/assets/image/background/desktop-01.avif") {
		t.Errorf("decoded asset path missing from output:\n%s", out)
	}
	if strings.Contains(out, `\\/`) {
		t.Errorf("escape corruption signature (backslash-slash) in output:\n%s", out)
	}
	if !strings.Contains(out, "89z") {
		t.Errorf("legacy \\8\\9 identity escapes not collapsed to digits:\n%s", out)
	}
	if !strings.Contains(out, "中文/路径/x.avif") {
		t.Errorf("decoded unicode path missing from output:\n%s", out)
	}
}

// TestMinifyBundleCSSEscapeSemantics: same guarantee for the CSS channel. tdewolff
// keeps the single-backslash escape `\/` in CSS strings — that decodes to "/", which
// is correct; corruption would be the doubled `\\/` form.
func TestMinifyBundleCSSEscapeSemantics(t *testing.T) {
	src := "a::before { content: \"\\/assets\\/x.avif\"; }\n.b { color: red; }"
	out, err := minifyBundle(src, ".css")
	if err != nil {
		t.Fatalf("minifyBundle css: %v", err)
	}
	if strings.Contains(out, `\\/`) {
		t.Errorf("escape corruption signature in css output:\n%s", out)
	}
	if !strings.Contains(out, `\/assets\/x.avif`) {
		t.Errorf("escaped path missing from css output:\n%s", out)
	}
	if !strings.Contains(out, "color:red") {
		t.Errorf("css body not minified:\n%s", out)
	}
}

// TestConcatFilesMinifiesAndPreservesBanner: concatFiles must minify the body and
// head the artifact with the untouched licence banner.
func TestConcatFilesMinifiesAndPreservesBanner(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.js")
	body := "// a comment to strip\nvar p = '\\/assets\\/x.avif';\n"
	if err := os.WriteFile(src, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.bundle.js")
	if err := concatFiles([]string{src}, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.HasPrefix(s, assetLicenceBanner) {
		t.Errorf("licence banner must be the untouched prefix, got:\n%.80s", s)
	}
	if !strings.Contains(s, "/assets/x.avif") {
		t.Errorf("decoded asset path missing:\n%s", s)
	}
	if strings.Contains(s, "// a comment") {
		t.Errorf("comment survived stripping+minification:\n%s", s)
	}
}

// TestConcatFilesEmptyBundleContract: an empty manifest produces a truly 0-byte
// artifact (no banner, no newline).
func TestConcatFilesEmptyBundleContract(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "empty.bundle.js")
	if err := concatFiles(nil, out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 0 {
		t.Errorf("empty bundle must be 0 bytes, got %d bytes:\n%q", len(b), string(b))
	}
}
