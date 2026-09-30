package main

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// funErrorCSSPath is read relative to this package's directory, the way the other
// source-scanning tests reach the engine tree.
const funErrorCSSPath = "src/css/components/fun-error.css"

var (
	cssCommentRe  = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssKeyframeRe = regexp.MustCompile(`@keyframes\s+([\w-]+)\s*\{`)
	cssRuleRe     = regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	cssAnimNameRe = regexp.MustCompile(`animation:\s*([^;]+)`)
	cssDeclRe     = regexp.MustCompile(`(?:^|[;{\s])([a-z-]+)\s*:`)
)

// TestFunErrorHostAnimationStaysOffGlass holds the rule that makes the card swap survive
// both engines: the host of the frosted card animates transform alone.
//
// An animated opacity keeps WebKit from mounting the host's backdrop layer for the whole
// track, and any opacity below 1 makes the host a backdrop root, which blanks the blur of
// the Chromium glass triplet nested inside it. An animated filter does the same, and in
// WebKit it also degrades the host's rounded clip. The fade therefore belongs on .card, a
// child that paints content rather than glass; the exit blurs instead of fading.
//
// The scan reads the selectors rather than a hand-kept list, so a renamed keyframe or a
// reworded selector fails here instead of silently dropping out of the check.
func TestFunErrorHostAnimationStaysOffGlass(t *testing.T) {
	raw, err := os.ReadFile(funErrorCSSPath)
	if err != nil {
		t.Fatalf("read %s: %v", funErrorCSSPath, err)
	}
	// Comments are stripped first: a property named in prose is not a declaration.
	css := cssCommentRe.ReplaceAllString(string(raw), "")

	keyframes := map[string]string{}
	for _, loc := range cssKeyframeRe.FindAllStringSubmatchIndex(css, -1) {
		name := css[loc[2]:loc[3]]
		body, ok := balancedBlock(css, loc[1]-1)
		if !ok {
			t.Fatalf("@keyframes %s: unbalanced braces", name)
		}
		keyframes[name] = body
	}
	if len(keyframes) == 0 {
		t.Fatal("no @keyframes found; the check would pass vacuously")
	}

	// Only rules that land on the host itself count. A rule ending in a descendant
	// ("> .card", "::before") animates something that is not the glass.
	hostRuleRe := regexp.MustCompile(`\.fun-error__card-inner\.is-(incoming|outgoing)\s*$`)

	banned := map[string][]string{
		"incoming": {"opacity", "filter"},
		"outgoing": {"opacity"},
	}

	checked := map[string]int{}
	for _, rule := range cssRuleRe.FindAllStringSubmatch(css, -1) {
		selector := strings.Join(strings.Fields(rule[1]), " ")
		state := hostRuleRe.FindStringSubmatch(selector)
		if state == nil {
			continue
		}
		anim := cssAnimNameRe.FindStringSubmatch(rule[2])
		if anim == nil {
			continue
		}
		name := strings.Fields(anim[1])[0]
		body, ok := keyframes[name]
		if !ok {
			t.Errorf("%s animates %s, which has no @keyframes block", selector, name)
			continue
		}
		checked[state[1]]++

		declared := map[string]bool{}
		for _, d := range cssDeclRe.FindAllStringSubmatch(body, -1) {
			declared[d[1]] = true
		}
		for _, prop := range banned[state[1]] {
			if declared[prop] {
				t.Errorf("%s animates %s, which declares %s on the glass host", selector, name, prop)
			}
		}
	}

	// Both states have to be seen. One alone would leave the other free to drop out of the
	// scan unnoticed — a renamed selector is exactly how this rule would stop being enforced.
	for state := range banned {
		if checked[state] == 0 {
			t.Errorf("no .is-%s rule animating the glass host in %s; that half of the check would pass vacuously", state, funErrorCSSPath)
		}
	}
}

// balancedBlock returns the body of the block that opens at open, where open indexes the
// opening brace, or false when the braces never balance.
func balancedBlock(s string, open int) (string, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[open+1 : i], true
			}
		}
	}
	return "", false
}
