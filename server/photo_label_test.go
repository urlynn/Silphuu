package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// photoCopyPool returns the strings under author.photo.<name> in the copy library, read the way
// loadUIStrings reads it (the deployment's copy when it has one, the shipped default otherwise).
func photoCopyPool(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(overlayRelPath("config/ui_strings.json"))
	if err != nil {
		t.Fatalf("read copy library: %v", err)
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatalf("parse copy library: %v", err)
	}
	author, ok := tree["author"].(map[string]any)
	if !ok {
		t.Fatalf("copy library has no author block")
	}
	photo, ok := author["photo"].(map[string]any)
	if !ok {
		t.Fatalf("copy library has no author.photo block")
	}
	items, ok := photo[name].([]any)
	if !ok {
		t.Fatalf("author.photo.%s is not an array", name)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("author.photo.%s holds a non-object entry", name)
		}
		text, ok := entry["text"].(string)
		if !ok || text == "" {
			t.Fatalf("author.photo.%s holds an entry with no text", name)
		}
		out = append(out, text)
	}
	return out
}

// TestPhotoLabelPoolPairsUp: the caption pool is two arrays whose indices pair up, so a length
// mismatch would drop a caption's kaomoji or serve a kaomoji with no caption. Nothing else
// couples them.
func TestPhotoLabelPoolPairsUp(t *testing.T) {
	useExampleSite(t)

	labels := photoCopyPool(t, "labels")
	kaomoji := photoCopyPool(t, "kaomoji")
	if len(labels) == 0 || len(kaomoji) == 0 {
		t.Fatal("文案池是空的——这个守卫会空跑")
	}
	if len(labels) != len(kaomoji) {
		t.Fatalf("author.photo.labels has %d entries, kaomoji has %d: they pair by index",
			len(labels), len(kaomoji))
	}
}

// TestPhotoKaomojiStayOutOfTheFontSubsets: the caption's two halves are drawn by different
// fonts — the text by --font-body, the kaomoji by the native --font-symbol — and the font
// collector builds its charsets from the same role the copy library records. A kaomoji that
// reaches a subset stops being drawn by the system symbol font and is redrawn in the web font,
// which changes the card's look with no error anywhere. role "native" is what keeps it out.
func TestPhotoKaomojiStayOutOfTheFontSubsets(t *testing.T) {
	useExampleSite(t)

	kaomoji := photoCopyPool(t, "kaomoji")
	if len(kaomoji) == 0 {
		t.Fatal("颜文字池是空的——这个守卫会空跑")
	}
	inPool := make(map[string]bool, len(kaomoji))
	for _, k := range kaomoji {
		inPool[k] = true
	}

	data, err := os.ReadFile(overlayRelPath("config/ui_strings.json"))
	if err != nil {
		t.Fatalf("read copy library: %v", err)
	}
	var tree map[string]any
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatalf("parse copy library: %v", err)
	}

	leaves := 0
	walkUILeaves(tree, func(text, role string) {
		leaves++
		if inPool[text] {
			t.Errorf("颜文字 %q 被收进 role %q 的字符集；它由 --font-symbol 画，必须保持 role:native", text, role)
		}
	})
	if leaves == 0 {
		t.Fatal("walkUILeaves 一个叶子都没走到——这个守卫会空跑")
	}

	// The other half must stay in: the caption text is drawn by the web font, so dropping its
	// role would let the browser silently substitute a fallback family.
	collected := make(map[string]bool)
	walkUILeaves(tree, func(text, role string) { collected[text] = true })
	for _, label := range photoCopyPool(t, "labels") {
		if !collected[uiCopyPlain(label)] {
			t.Errorf("文案 %q 没进任何字符集；它由 --font-body 画，缺了会静默换回退族", label)
		}
	}
}

// TestPhotoLabelPairsRenderFromThePool: the template function is what the wall carries, so it
// has to produce one [text, kaomoji] pair per pool entry — an empty pair would leave a card
// blank, and a missing kaomoji would ship half a caption.
func TestPhotoLabelPairsRenderFromThePool(t *testing.T) {
	useExampleSite(t)
	loadUIStrings()

	pairs := photoLabelPairs()
	labels := photoCopyPool(t, "labels")
	if len(pairs) != len(labels) {
		t.Fatalf("photoLabelPairs returned %d pairs, the pool has %d captions", len(pairs), len(labels))
	}
	for i, p := range pairs {
		if p[0] == "" || p[1] == "" {
			t.Errorf("pair %d is %q; both halves must be present", i, p)
		}
	}
}

// cssRule returns the declaration block of selector in path. A missing rule or an unterminated
// block fails rather than returning an empty string, so the guard reading it cannot pass on
// nothing.
func cssRule(t *testing.T, path, selector string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(data)
	open := selector + " {"
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatalf("%s has no %q rule — the guard would pass vacuously", path, selector)
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "}")
	if j < 0 {
		t.Fatalf("%s: the %q rule is unterminated", path, selector)
	}
	return rest[:j]
}

// TestPhotoCaptionHalvesHaveTheirOwnNodes: the caption's text and its kaomoji are drawn by two
// different fonts, so they must not share a node. Concatenated, the whole string goes to the
// caption's web font, which claims the kaomoji characters it happens to own and leaves the rest
// to the symbol stack — one kaomoji coming out of several families.
func TestPhotoCaptionHalvesHaveTheirOwnNodes(t *testing.T) {
	path := filepath.Join(RootTemplates, "components.html")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	body := string(data)
	const label = `<p class="photo-card-label">`
	i := strings.Index(body, label)
	if i < 0 {
		t.Fatalf("%s has no photo caption node — the guard would pass vacuously", path)
	}
	end := strings.Index(body[i:], "</p>")
	if end < 0 {
		t.Fatalf("%s: the photo caption node is unterminated", path)
	}
	caption := body[i : i+end]
	for _, want := range []string{`class="photo-card-label-text"`, `class="sym"`} {
		if !strings.Contains(caption, want) {
			t.Errorf("the photo caption has no %s node; the two halves would share the caption's web font", want)
		}
	}

	// The picker fills those two nodes separately. Writing the concatenation back into the
	// caption node is exactly what the split exists to prevent.
	jsPath := filepath.Join(RootSrc, "js/parts/20-photo-label.js")
	js, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsPath, err)
	}
	for _, want := range []string{`querySelector('.photo-card-label-text')`, `querySelector('.sym')`} {
		if !strings.Contains(string(js), want) {
			t.Errorf("%s never fills %s; a half of the caption would land in the caption's own node", jsPath, want)
		}
	}
}

// TestSymbolNodeKeepsTheNativeFamily: .sym is where the kaomoji is drawn, so its family must be
// the symbol stack alone. Routed through the web font, the kaomoji is claimed character by
// character and comes out of more than one family.
func TestSymbolNodeKeepsTheNativeFamily(t *testing.T) {
	rule := cssRule(t, filepath.Join(RootSrc, "css/components/kaomoji.css"), ".sym")
	if !strings.Contains(rule, "var(--font-symbol)") {
		t.Errorf(".sym does not draw from var(--font-symbol): %s", strings.TrimSpace(rule))
	}
	if strings.Contains(rule, "--font-body") {
		t.Errorf(".sym routes the kaomoji through the web font: %s", strings.TrimSpace(rule))
	}
}

// TestPhotoCaptionCarriesTheBodyInkCompensation: a declared size in this stylesheet is ink
// height, so the caption has to carry the body role's measured ink-scale and shift. A bare px
// size leaves the caption at the web font's raw ink — smaller than declared — and frozen when
// the preset's metrics change, which drifts the caption against every other text on the page.
//
// The two halves must each carry their own role's shift and nothing else. On the flex parent the
// body shift moves the kaomoji too, which already carries --font-symbol-shift, so the kaomoji
// ends up over-shifted by the body value and no longer sits on the text's ink line.
func TestPhotoCaptionCarriesTheBodyInkCompensation(t *testing.T) {
	path := filepath.Join(RootSrc, "css/components/gallery.css")
	rule := cssRule(t, path, ".photo-wall .photo-card-label")
	if !strings.Contains(rule, "var(--font-body-ink-scale") {
		t.Errorf(".photo-card-label declares a size without the body ink-scale: %s", strings.TrimSpace(rule))
	}
	if strings.Contains(rule, "var(--font-body-shift") {
		t.Errorf(".photo-card-label carries the body shift on the flex parent, which moves the "+
			"kaomoji as well as the text: %s", strings.TrimSpace(rule))
	}
	text := cssRule(t, path, ".photo-wall .photo-card-label-text")
	if !strings.Contains(text, "var(--font-body-shift") {
		t.Errorf(".photo-card-label-text has no body ink shift: %s", strings.TrimSpace(text))
	}
}

// TestPhotoKaomojiSizeIgnoresTheWebFontScale: .sym's own rule must size the kaomoji off the
// container's unrounded base. Sized off the inherited em instead, the kaomoji is multiplied by
// the web-font role's ink-scale on top of its own, which scales the kaomoji and the caption text
// by the same ratio — the kaomoji/text proportion is then exactly what it was before the
// compensation, and the caption's ink line is no closer for it.
func TestPhotoKaomojiSizeIgnoresTheWebFontScale(t *testing.T) {
	rule := cssRule(t, filepath.Join(RootSrc, "css/components/kaomoji.css"), ".sym")
	if !strings.Contains(rule, "var(--caption-base") {
		t.Errorf(".sym sizes the kaomoji off the inherited em, so the web-font ink-scale reaches "+
			"it: %s", strings.TrimSpace(rule))
	}

	// The base is what .sym reads, so the caption has to declare it.
	caption := cssRule(t, filepath.Join(RootSrc, "css/components/gallery.css"), ".photo-wall .photo-card-label")
	if !strings.Contains(caption, "--caption-base:") {
		t.Errorf(".photo-card-label declares no --caption-base, so .sym would fall back to the "+
			"scaled em: %s", strings.TrimSpace(caption))
	}
}
