package main

import (
	"html"
	"html/template"
	"strings"
)

// Effect markers for UI copy, resolved once at load so a config author writes ASCII:
// ~~text~~ renders <del> and ||text|| becomes the heimu reveal. An unpaired marker stays
// literal, so prose like "2 ~~ 3" is left alone.
const (
	uiMarkStrike = "~~"
	uiMarkHeimu  = "||"
)

// uiCopySeg is one run of copy plus the effect its markers asked for.
type uiCopySeg struct {
	text   string
	strike bool
	heimu  bool
}

// nextUIMarker reports the earliest marker in s and which one it is, or -1 for none.
func nextUIMarker(s string) (int, string) {
	a := strings.Index(s, uiMarkStrike)
	b := strings.Index(s, uiMarkHeimu)
	switch {
	case a < 0:
		return b, uiMarkHeimu
	case b < 0:
		return a, uiMarkStrike
	case a < b:
		return a, uiMarkStrike
	default:
		return b, uiMarkHeimu
	}
}

// parseUICopy splits copy into segments with paired markers resolved. Markers do not
// nest: an inner marker is ordinary text. An unpaired marker is emitted literally.
func parseUICopy(s string) []uiCopySeg {
	var segs []uiCopySeg
	for s != "" {
		i, mark := nextUIMarker(s)
		if i < 0 {
			break
		}
		j := strings.Index(s[i+2:], mark)
		if j <= 0 {
			segs = append(segs, uiCopySeg{text: s[:i+2]})
			s = s[i+2:]
			continue
		}
		if i > 0 {
			segs = append(segs, uiCopySeg{text: s[:i]})
		}
		segs = append(segs, uiCopySeg{
			text:   s[i+2 : i+2+j],
			strike: mark == uiMarkStrike,
			heimu:  mark == uiMarkHeimu,
		})
		s = s[i+2+j+2:]
	}
	if s != "" {
		segs = append(segs, uiCopySeg{text: s})
	}
	return segs
}

// uiCopyPlain resolves markers to the characters the page really renders: a struck run
// keeps its text (the line is drawn by the renderer, not a glyph) and a heimu run keeps
// only its word. The font subsetter collects from this.
func uiCopyPlain(s string) string {
	if !strings.Contains(s, uiMarkStrike) && !strings.Contains(s, uiMarkHeimu) {
		return s
	}
	var b strings.Builder
	for _, seg := range parseUICopy(s) {
		b.WriteString(seg.text)
	}
	return b.String()
}

// uiCopyRender returns the value a template prints for one copy string. Both effects need
// markup, so a leaf carrying either marker becomes template.HTML while every other leaf
// stays a plain string — see docs/GOTCHAS.md §ui-copy-effect-markers.
func uiCopyRender(s string) any {
	if !strings.Contains(s, uiMarkStrike) && !strings.Contains(s, uiMarkHeimu) {
		return s
	}
	var b strings.Builder
	for _, seg := range parseUICopy(s) {
		switch {
		case seg.heimu:
			b.WriteString(`<span class="heimu"><span class="heimu__word">`)
			b.WriteString(html.EscapeString(seg.text))
			b.WriteString(`</span></span>`)
		case seg.strike:
			b.WriteString(`<del>`)
			b.WriteString(html.EscapeString(seg.text))
			b.WriteString(`</del>`)
		default:
			b.WriteString(html.EscapeString(seg.text))
		}
	}
	return template.HTML(b.String())
}
