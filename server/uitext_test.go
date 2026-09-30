package main

import (
	"html/template"
	"strings"
	"testing"
)

// The strike marker renders a <del>: the renderer draws the line at the text's own width,
// so no font has to carry a combining glyph.
func TestUICopyStrikeRendersDel(t *testing.T) {
	got := uiCopyRender("甲~~乙丙~~丁")
	h, ok := got.(template.HTML)
	if !ok {
		t.Fatalf("含 ~~ 的文案应返回 template.HTML，实际 %T", got)
	}
	if !strings.Contains(string(h), "<del>乙丙</del>") {
		t.Errorf("uiCopyRender = %q, want a <del> run", string(h))
	}
	if strings.Contains(string(h), "~~") || strings.ContainsRune(string(h), '\u0336') {
		t.Errorf("标记或组合符残留: %q", string(h))
	}
}

// A marker without its closing pair must survive untouched, or ordinary prose that
// happens to contain ~~ or || would be silently rewritten.
func TestUICopyUnpairedMarkerStaysLiteral(t *testing.T) {
	for _, s := range []string{"2 ~~ 3", "a || b", "~~开头没闭合", "闭合没有~~", "单个 ~ 和 |"} {
		if got := uiCopyPlain(s); got != s {
			t.Errorf("uiCopyPlain(%q) = %q, want it unchanged", s, got)
		}
	}
}

func TestUICopyEmptyMarkerStaysLiteral(t *testing.T) {
	const s = "||||"
	if got := uiCopyPlain(s); got != s {
		t.Errorf("uiCopyPlain(%q) = %q, want it unchanged", s, got)
	}
}

// The heimu projection is markup, and the word it reveals must be escaped: the span is
// handed to the template as trusted HTML.
func TestUICopyHeimuRendersSpanAndEscapes(t *testing.T) {
	got := uiCopyRender("前缀||甲<段&||后缀")
	h, ok := got.(template.HTML)
	if !ok {
		t.Fatalf("含 || 的文案应返回 template.HTML，实际 %T", got)
	}
	want := `前缀<span class="heimu"><span class="heimu__word">甲&lt;段&amp;</span></span>后缀`
	if string(h) != want {
		t.Errorf("uiCopyRender = %q, want %q", string(h), want)
	}
}

// A leaf with no marker must stay a plain string. Turning it into template.HTML would make
// html/template strip markup in attribute slots and stop escaping &, which mangles copy
// such as the svg placeholder example.
func TestUICopyWithoutMarkersStaysPlainString(t *testing.T) {
	const s = "普通文案 & <示例>"
	got := uiCopyRender(s)
	if _, isHTML := got.(template.HTML); isHTML {
		t.Fatal("不含标记的文案必须是纯字符串，否则属性槽位会被剥标签")
	}
	if out, ok := got.(string); !ok || out != s {
		t.Fatalf("uiCopyRender = %#v, want the original string", got)
	}
}

// The subsetter collects from the plain projection, so it must see the characters the page
// really renders: the text inside the markers, with no markup and no combining glyph.
func TestUICopyPlainKeepsTextDropsMarkup(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"甲~~乙丙~~丁", "甲乙丙丁"},
		{"前||中段||后", "前中段后"},
		{"甲~~乙~~||丙||丁", "甲乙丙丁"},
	} {
		if got := uiCopyPlain(tc.in); got != tc.want {
			t.Errorf("uiCopyPlain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestUICopyMixedMarkers(t *testing.T) {
	got := uiCopyRender("看||甲段||的~~乙段~~")
	h, ok := got.(template.HTML)
	if !ok {
		t.Fatalf("含标记的文案应返回 template.HTML，实际 %T", got)
	}
	if !strings.Contains(string(h), `<span class="heimu"><span class="heimu__word">甲段</span></span>`) {
		t.Errorf("黑幕 span 缺失: %q", string(h))
	}
	if !strings.Contains(string(h), "<del>乙段</del>") {
		t.Errorf("删除线缺失: %q", string(h))
	}
	if strings.Contains(string(h), "~~") || strings.Contains(string(h), "||") {
		t.Errorf("标记残留: %q", string(h))
	}
}

// The subsetter collects through walkUILeaves, so it has to see resolved copy. Leaving the
// markers in would ask the subset for glyphs the page never draws.
func TestWalkUILeavesResolvesMarkers(t *testing.T) {
	tree := map[string]any{"text": "甲~~乙丙~~丁", "role": "body"}
	var got []string
	walkUILeaves(tree, func(text, role string) { got = append(got, text) })
	if len(got) != 1 {
		t.Fatalf("叶子数 = %d, want 1", len(got))
	}
	if want := "甲乙丙丁"; got[0] != want {
		t.Errorf("walkUILeaves 传出 %q, want %q", got[0], want)
	}
}
