package main

// css_guard_test.go — CI defense line against deprecated CSS patterns.
//
// Five assertions: A1 no rgba(var(--*-rgb), a) double-variable antipattern; A2 no
// var(--shadow-{sm,md,lg,card,overlay}) generic shadow scale; A3 no dangling
// var() refs (no fallback, no definition anywhere, not written dynamically); A4 balanced
// braces in every CSS source file; A5 no zero-consumer dead definitions.
//
// Exemption mechanism: reasoned exemptions (allowlists) record known-and-intentional
// debt. Every entry must state its reason. New violations are never auto-exempted: the
// test fails.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Scan scope.

// Skip a file when its path contains any of these fragments
// (build output / standalone subprojects / deploy snapshots)
var guardSkipPathFragments = []string{
	"/.git/",
	"/node_modules/",
	"/render/",
	"/public/",
	"/deploy/",
	"/Example/", // standalone example project, not built with this site
	"/.workbuddy-ai/",
	"/docs/",
	"/scripts/",
}

// Skip files whose name matches (build output / debug pages / test fixtures)
func guardSkipFile(name string) bool {
	if strings.HasSuffix(name, ".bundle.css") || strings.HasSuffix(name, ".bundle.js") {
		return true
	}
	if strings.HasPrefix(name, "debug_") || strings.HasPrefix(name, "debug-") {
		return true
	}
	if strings.Contains(name, "debug") || strings.Contains(name, "showcase") {
		return true
	}
	if strings.Contains(name, "test-backdrop") {
		return true
	}
	return false
}

// guardWalk collects files with the given extensions under roots (relative
// paths), filtered by the skip rules above
func guardWalk(t *testing.T, roots []string, exts ...string) []string {
	t.Helper()
	want := map[string]bool{}
	for _, e := range exts {
		want[e] = true
	}
	var out []string
	for _, root := range roots {
		// A missing root means the guard would scan nothing and pass. That is the one
		// failure mode a guard cannot have, so it is fatal here.
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("guard root %s is missing: %v", root, err)
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // tolerate unreadable entries
			}
			slash := filepath.ToSlash(path)
			for _, frag := range guardSkipPathFragments {
				if strings.Contains(slash, frag) {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
			if info.IsDir() {
				return nil
			}
			if !want[filepath.Ext(path)] {
				return nil
			}
			if guardSkipFile(filepath.Base(path)) {
				return nil
			}
			out = append(out, slash)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	sort.Strings(out)
	return out
}

// guardStripComments removes block comments but preserves the exact newline
// count, so reported line numbers still match the original file.
// Note: do not reuse the production stripCSSComments — it also strips
// newlines inside comments and collapses whitespace, shifting every reported
// line number (measured 57 lines off on admin.css), which kills the
// diagnostics value.
func guardStripComments(src string) string {
	var sb strings.Builder
	sb.Grow(len(src))
	n := len(src)
	inString := byte(0)
	for i := 0; i < n; i++ {
		ch := src[i]
		if inString != 0 {
			sb.WriteByte(ch)
			if ch == '\\' && i+1 < n {
				i++
				sb.WriteByte(src[i])
			} else if ch == inString {
				inString = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inString = ch
			sb.WriteByte(ch)
			continue
		}
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			sb.WriteString("  ")
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					sb.WriteByte('\n')
				}
				i++
			}
			i++
			sb.WriteString("  ")
			continue
		}
		// Line comments (needed when scanning JS / HTML sources)
		if ch == '/' && i+1 < n && src[i+1] == '/' {
			for i < n && src[i] != '\n' {
				i++
			}
			if i < n {
				sb.WriteByte('\n')
			}
			continue
		}
		sb.WriteByte(ch)
	}
	return sb.String()
}

// Regexes.

// Variable definitions. RE2 has no lookbehind, so a preceding-character class
// is used to exclude false hits inside selectors like
// `.admin-btn--primary:hover {` (the char before `--primary` is `n`).
var reCSSVarDef = regexp.MustCompile(`(?:^|[^-\w])(--[a-zA-Z][\w-]*)\s*:`)

// Variable consumption; capture group 2 distinguishes fallback presence:
//
//	var(--x)      -> ")"  no fallback (real dangling ref)
//	var(--x, fb)  -> ","  legal override slot
var reCSSVarUse = regexp.MustCompile(`var\(\s*(--[a-zA-Z][\w-]*)\s*([,)])`)

// Double-variable antipattern
var reRGBAVar = regexp.MustCompile(`rgba\(\s*var\(\s*--[a-zA-Z][\w-]*-rgb`)

// The generic shadow scale
var reLegacyShadow = regexp.MustCompile(`var\(\s*--shadow-(?:sm|md|lg|card|overlay)\s*[),]`)

// Go-side dynamic writes: fmt.Sprintf("--prefix-%s…")
var reGoSprintf = regexp.MustCompile(`fmt\.Sprintf\(\s*"(--[a-zA-Z][\w-]*%[sdv])`)

// Go-side dynamic writes: literal variable names
var reGoLiteral = regexp.MustCompile(`"(--[a-zA-Z][\w-]*)"`)

// JS-side dynamic reads/writes
var reJSSetProp = regexp.MustCompile(`(?:setProperty|getPropertyValue)\(\s*['"\x60](--[\w-]+)`)

// Custom-property declarations written inside an inline style *string*, e.g.
//
//	'--c:' + color + ';--dur:' + dur + 's'
//
// 15-sponsor-fx.js builds whole style attributes by concatenation instead of
// calling setProperty, so reJSSetProp alone cannot see these writes and the A3
// assertion reported them as dangling. A declaration always has a colon
// immediately after the name, whereas a var() *use* never does — so matching
// `--name:` cannot be confused with consumption.
var reInlineStyleDecl = regexp.MustCompile(`(--[a-zA-Z][\w-]*)\s*:`)

// Collection.

// guardDefined returns varName -> definition sites (relpath:line)
func guardDefined(t *testing.T, cssFiles []string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, f := range cssFiles {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		body := guardStripComments(string(raw))
		for i, line := range strings.Split(body, "\n") {
			for _, m := range reCSSVarDef.FindAllStringSubmatch(line, -1) {
				out[m[1]] = append(out[m[1]], fmt.Sprintf("%s:%d", f, i+1))
			}
		}
	}
	return out
}

// guardConsumed returns (no-fallback uses, with-fallback uses); both values
// map varName -> location list
func guardConsumed(t *testing.T, files []string) (noFallback, withFallback map[string][]string) {
	t.Helper()
	noFallback = map[string][]string{}
	withFallback = map[string][]string{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		body := guardStripComments(string(raw))
		for i, line := range strings.Split(body, "\n") {
			for _, m := range reCSSVarUse.FindAllStringSubmatch(line, -1) {
				loc := fmt.Sprintf("%s:%d", f, i+1)
				if m[2] == "," {
					withFallback[m[1]] = append(withFallback[m[1]], loc)
				} else {
					noFallback[m[1]] = append(noFallback[m[1]], loc)
				}
			}
		}
	}
	return
}

// guardDynamic returns the set of variable names written at runtime by Go/JS.
// Such variables may legitimately lack a static definition or a static
// consumption in CSS, so both assertions must exclude them to avoid false
// positives.
func guardDynamic(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	mark := func(name string) {
		if name != "" {
			out[name] = true
		}
	}
	// Go: literals + fmt.Sprintf prefixes
	for _, f := range guardWalk(t, []string{"."}, ".go") {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		s := string(raw)
		for _, m := range reGoLiteral.FindAllStringSubmatch(s, -1) {
			mark(m[1])
		}
		// "--font-%s-home" -> prefix "--font-" (drop everything from the verb on)
		for _, m := range reGoSprintf.FindAllStringSubmatch(s, -1) {
			prefix := m[1]
			if i := strings.Index(prefix, "%"); i >= 0 {
				prefix = prefix[:i]
			}
			prefix = strings.TrimRight(prefix, "-")
			if prefix != "" {
				mark(prefix + "-*")
			}
		}
	}
	// JS / HTML: setProperty / getPropertyValue, plus custom properties declared
	// inside inline style strings (style="--c:…", '--dur:' + x)
	for _, f := range guardWalk(t, []string{"src", "templates"}, ".js", ".mjs", ".html") {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		body := string(raw)
		for _, m := range reJSSetProp.FindAllStringSubmatch(body, -1) {
			mark(m[1])
		}
		for _, m := range reInlineStyleDecl.FindAllStringSubmatch(body, -1) {
			mark(m[1])
		}
	}
	// Register wildcard prefixes as sentinel entries
	mark("--font-*") // the Go fmt.Sprintf("--font-%s…") family
	return out
}

func guardIsDynamic(dynamic map[string]bool, name string) bool {
	if dynamic[name] {
		return true
	}
	for k := range dynamic {
		if strings.HasSuffix(k, "-*") && strings.HasPrefix(name, strings.TrimSuffix(k, "*")) {
			return true
		}
	}
	return false
}

// Exemption lists (every entry must state its reason).

// A1 exemptions: variables still using rgba(var(--*-rgb), a) -> reason
var guardA1Allowed = map[string]string{
	"--contrast-base-rgb": "editor.css + status.css (admin side); pending the admin " +
		"refactor, then migrate to color-mix().",
}

// A2 exemptions: variables still using the generic shadow scale -> reason
var guardA2Allowed = map[string]string{
	"--shadow-card": "cmt.css + themes/page-article.css (front-end article card and " +
		"comments); moving to the semantic shadow scale changes visuals and needs " +
		"per-site visual QA.",
	"--shadow-sm": "hover-layers.css (.admin-maint-card) + admin.css; admin side, " +
		"pending the admin refactor.",
}

// A3 exemptions: known dangling refs (consumed but undefined, no fallback) -> reason.
//
// Currently empty — all past entries have been fixed. New entries must state
// a reason.
// A3 exemptions: referenced with no fallback, statically undefined, and not
// recognised as dynamically written. Keep this empty if at all possible — a
// missing entry usually means guardDynamic has a blind spot worth fixing (the
// inline-style-string case in 15-sponsor-fx.js was solved that way, not here).
var guardA3Allowed = map[string]string{}

// A5 exemptions: defined but statically unconsumed, and not dynamically written -> reason
var guardA5Allowed = map[string]string{
	// Currently empty. New entries must state a reason; prefer deleting the
	// definition outright over exempting it.
}

// Assertions.

// A1: no rgba(var(--*-rgb), a)
func TestCSSGuardNoRGBATriple(t *testing.T) {
	files := guardWalk(t, []string{"src/css"}, ".css")
	var bad []string
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		body := guardStripComments(string(raw))
		for i, line := range strings.Split(body, "\n") {
			if !reRGBAVar.MatchString(line) {
				continue
			}
			name := ""
			if m := regexp.MustCompile(`var\(\s*(--[a-zA-Z][\w-]*-rgb)`).FindStringSubmatch(line); m != nil {
				name = m[1]
			}
			if _, ok := guardA1Allowed[name]; ok {
				continue
			}
			bad = append(bad, fmt.Sprintf("%s:%d  %s", f, i+1, strings.TrimSpace(line)))
		}
	}
	if len(bad) > 0 {
		t.Errorf("found %d uses of the doubled-variable anti-pattern rgba(var(--*-rgb), a):\n  %s\n\n"+
			"correct form: color-mix(in srgb, var(--x) calc(a * 100%%), transparent)\n"+
			"(keeping two tokens for one colour makes drift between them inevitable)",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// A2: no generic shadow scale
func TestCSSGuardNoLegacyShadowScale(t *testing.T) {
	files := guardWalk(t, []string{"src/css", "templates"}, ".css", ".html")
	var bad []string
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		body := guardStripComments(string(raw))
		for i, line := range strings.Split(body, "\n") {
			for _, m := range reLegacyShadow.FindAllStringSubmatch(line, -1) {
				// m[0] looks like "var(--shadow-sm)"; extract the var name for the allowlist lookup
				name := regexp.MustCompile(`--shadow-[a-z]+`).FindString(m[0])
				if _, ok := guardA2Allowed[name]; ok {
					continue
				}
				bad = append(bad, fmt.Sprintf("%s:%d  %s", f, i+1, strings.TrimSpace(line)))
			}
		}
	}
	if len(bad) > 0 {
		t.Errorf("found %d uses of the generic shadow scale:\n  %s\n\n"+
			"shadows must come from the L0-L3 semantic tokens\n"+
			"(--card-shadow-hover / --widget-shadow-hover etc.)\n"+
			"why: the generic scale reads as neon on dark themes and nearly vanishes on light ones",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// A3: no dangling refs
func TestCSSGuardNoDanglingRefs(t *testing.T) {
	cssFiles := guardWalk(t, []string{"src/css"}, ".css")
	defined := guardDefined(t, cssFiles)
	consumers := guardWalk(t, []string{"src", "templates"}, ".css", ".js", ".mjs", ".html")
	noFallback, _ := guardConsumed(t, consumers)
	dynamic := guardDynamic(t)

	var bad []string
	for name, locs := range noFallback {
		if _, ok := defined[name]; ok {
			continue
		}
		if guardIsDynamic(dynamic, name) {
			continue
		}
		if _, ok := guardA3Allowed[name]; ok {
			continue
		}
		sort.Strings(locs)
		bad = append(bad, fmt.Sprintf("%s  <- %s", name, strings.Join(locs, ", ")))
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("发现 %d 个悬空引用（var() 无 fallback 且全仓无定义、非动态写入）：\n  %s\n\n"+
			"这类失效**不报错、不崩** —— 浏览器只是丢弃整条声明、属性回退到默认值，\n"+
			"肉眼几乎看不出（例：--danger-red-rgb 被删后评论区悬停红色静默消失）。\n"+
			"修法二选一：① 补定义  ② 改写成现有 token。\n"+
			"! 注意：var(--x, fallback) 是合法覆写槽位，不在此断言范围内。",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// A4: balanced braces in every CSS source file
func TestCSSGuardBraceBalance(t *testing.T) {
	files := guardWalk(t, []string{"src/css"}, ".css")
	var bad []string
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		body := guardStripComments(string(raw))
		open := strings.Count(body, "{")
		close := strings.Count(body, "}")
		if open != close {
			bad = append(bad, fmt.Sprintf("%s  { × %d / } × %d", f, open, close))
		}
	}
	if len(bad) > 0 {
		t.Errorf("发现 %d 个 CSS 文件大括号不平衡：\n  %s\n\n"+
			"CSS 漏 } 会让**后面所有规则全部不生效**（表现为布局崩坏）。",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// A5: no zero-consumer dead definitions
func TestCSSGuardNoDeadDefinitions(t *testing.T) {
	cssFiles := guardWalk(t, []string{"src/css"}, ".css")
	defined := guardDefined(t, cssFiles)
	consumers := guardWalk(t, []string{"src", "templates"}, ".css", ".js", ".mjs", ".html")
	noFallback, withFallback := guardConsumed(t, consumers)
	dynamic := guardDynamic(t)

	var bad []string
	for name, locs := range defined {
		if len(noFallback[name]) > 0 || len(withFallback[name]) > 0 {
			continue
		}
		if guardIsDynamic(dynamic, name) {
			continue
		}
		if _, ok := guardA5Allowed[name]; ok {
			continue
		}
		bad = append(bad, fmt.Sprintf("%s  <- 定义于 %s", name, strings.Join(locs, ", ")))
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("发现 %d 个零消费死定义：\n  %s\n\n"+
			"死定义的危害：改它**看不到任何变化**，白费功夫；\n"+
			"12 个主题里各定义一遍的死变量更会让「每加一个主题就复制一份死代码」。\n"+
			"处置：直接删除（git 有历史）。确需保留的，请在 guardA5Allowed 写明原因。",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// Scope guard for ink-compensation rules.
//
// Compensation is per font ROLE, and the role is determined by the CONTAINER, so a bare
// class selector leaks its compensation var to every element using that class name: e.g.
// .footer-sep declared bare in footer.css while home-page cards reuse the class pushed the
// card dots off the text/icons on the same line. Rule: any rule whose translate/transform
// references var(--font-*-shift) or var(--icon-*) must carry an ancestor scope (or be
// wrapped in :is()); bare class selectors are never acceptable.

// guardScopedAllowed holds the rules the criterion below cannot clear, keyed by bare class
// selector. It is expected to be empty: the criterion already covers every name that one
// component owns, so an entry here means a name genuinely shared by two components, and the
// honest fix is an ancestor scope. Any entry that matches no rule fails the guard.
var guardScopedAllowed = map[string]string{
	".sym": "script applies it to symbol runs at any depth (kaomoji.js, 12-lifecycle.js), so " +
		"no ancestor names the role. Here the class name IS the role, and the shift it reads " +
		"is the symbol font's own measurement, so the rule cannot land on a wrong element.",
}

var guardBareClassRe = regexp.MustCompile(`^\.[A-Za-z0-9_-]+$`)

// TestCSSGuardHomeHidesGlobalBgLayers: bg_shapes.html renders .bg-base/.bg-shapes inside
// <body>, so the hide rule must read body.home > .bg-* and be keyed on the page — an
// html > .bg-* selector never matches, and a :has(.hero) one also fires on the admin font
// preview, hiding the layers for that whole page. Neither may depend on scroll position:
// the clone source is cached per recalculation, so a layer that comes and goes with
// scrolling leaves the cards with an empty clone — see docs/GOTCHAS.md §backdrop-shape-layers.
func TestCSSGuardHomeHidesGlobalBgLayers(t *testing.T) {
	raw, err := os.ReadFile("src/css/components/home-section.css")
	if err != nil {
		t.Fatalf("read home-section.css: %v", err)
	}
	src := guardStripComments(string(raw))
	for _, layer := range []string{".bg-base", ".bg-shapes"} {
		want := "body.home > " + layer
		if !strings.Contains(src, want) {
			t.Errorf("home hide rule missing %q — the global fixed layer would corrupt hero backdrop sampling", want)
		}
	}
	if regexp.MustCompile(`html:has\(\.hero[^)]*\) > \.bg-`).MatchString(src) {
		t.Errorf("hide rule reverted to html > .bg-* — that selector never matches (layers are body children)")
	}
	if regexp.MustCompile(`body:has\(\.hero\) > \.bg-`).MatchString(src) {
		t.Errorf("hide rule is keyed on hero presence — the admin font preview renders a hero, which hides the layers for the whole admin page")
	}
	if regexp.MustCompile(`body:has\(\.home-section\.is-visible\) > \.bg-`).MatchString(src) {
		t.Errorf("hide rule is scroll-dependent again — hiding the global layer while the second screen is visible empties every Scene B clone")
	}
}

// guardCompensates reports whether a declaration block moves an element by a font or icon
// shift var. Only the value of translate/transform counts: `translate: none` states the
// opposite, and an offset with no var in it is not compensation at all.
func guardCompensates(body string) bool {
	decls := guardCSSDecls(body)
	for _, prop := range []string{"translate", "transform"} {
		v := decls[prop]
		if strings.Contains(v, "var(--font-") || strings.Contains(v, "var(--icon-") {
			return true
		}
	}
	return false
}

// The three ways markup and script apply a class name. A class reaches an element through
// one of these, so these are what a leak surface is made of.
var (
	reClassAttr    = regexp.MustCompile(`class\s*=\s*["']([^"']*)["']`)
	reClassList    = regexp.MustCompile(`classList\.(?:add|remove|toggle|contains)\(\s*["']([^"']*)["']`)
	reClassQuery   = regexp.MustCompile(`(?:querySelector(?:All)?|closest|matches)\(\s*["']([^"']*)["']`)
	reClassWord    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]*`)
	reCSSSelectorC = regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9_-]*)`)
)

// guardAppliedClasses returns the class names one markup or script file applies, read out of
// the attribute value / selector string each call site carries.
func guardAppliedClasses(src string) []string {
	var out []string
	for _, re := range []*regexp.Regexp{reClassAttr, reClassList, reClassQuery} {
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			out = append(out, reClassWord.FindAllString(m[1], -1)...)
		}
	}
	return out
}

// guardClassSites returns, per class name, the markup/script files that apply it and the
// stylesheets that select it. A name with one site of each is a single component's private
// name: nothing else can pick up the compensation the rule hands out.
func guardClassSites(t *testing.T) (applied, styled map[string]map[string]bool) {
	t.Helper()
	applied, styled = map[string]map[string]bool{}, map[string]map[string]bool{}
	record := func(dst map[string]map[string]bool, cls, file string) {
		if dst[cls] == nil {
			dst[cls] = map[string]bool{}
		}
		dst[cls][file] = true
	}
	for _, f := range guardWalk(t, []string{"templates", "src"}, ".html", ".js", ".mjs") {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, cls := range guardAppliedClasses(guardStripComments(string(raw))) {
			record(applied, cls, f)
		}
	}
	for _, f := range guardWalk(t, []string{"src/css"}, ".css") {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range reCSSSelectorC.FindAllStringSubmatch(guardStripComments(string(raw)), -1) {
			record(styled, m[1], f)
		}
	}
	return
}

func TestCSSGuardCompensationNeedsScope(t *testing.T) {
	files := guardWalk(t, []string{"src/css"}, ".css")
	applied, styled := guardClassSites(t)
	hit := map[string]bool{}
	var bad []string
	for _, f := range files {
		if guardSkipFile(filepath.Base(f)) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		src := guardStripComments(string(raw))
		for _, m := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(src, -1) {
			sel := strings.TrimSpace(m[1])
			body := m[2]
			if !guardCompensates(body) {
				continue
			}
			if strings.HasPrefix(sel, "@") || sel == "" {
				continue
			}
			bare := true
			for _, part := range strings.Split(sel, ",") {
				if !guardBareClassRe.MatchString(strings.TrimSpace(part)) {
					bare = false
					break
				}
			}
			if !bare {
				continue
			}
			key := strings.TrimSpace(strings.Split(sel, ",")[0])
			hit[key] = true
			// One application site and one stylesheet means one component owns the name, so
			// the compensation has nothing else to land on. More than one of either is a
			// leak surface, and a leak surface needs an ancestor scope.
			single := true
			for _, part := range strings.Split(sel, ",") {
				cls := strings.TrimPrefix(strings.TrimSpace(part), ".")
				if len(applied[cls]) > 1 || len(styled[cls]) > 1 {
					single = false
					break
				}
			}
			if single {
				continue
			}
			if _, ok := guardScopedAllowed[key]; ok {
				continue
			}
			bad = append(bad, fmt.Sprintf("%s  <- %s", key, f))
		}
	}
	// An exemption nothing matches any more is dead weight: it hides a detection weakness
	// that no longer exists and makes the table look load-bearing when it is not. Fail on
	// it, so the list can only shrink.
	for key := range guardScopedAllowed {
		if !hit[key] {
			bad = append(bad, fmt.Sprintf("%s  <- 豁免已失效：没有任何规则命中它，删掉这条", key))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("发现 %d 条**裸类选择器**的墨迹补偿规则（无作用域，会泄漏到任何用了该类名的部件）：\n  %s\n\n"+
			"为什么危险：补偿值是按**字体角色**给的，而角色由**容器**决定。\n"+
			"裸类选择器表达不了\"我在哪个容器里\" -> 另一个部件只要复用了这个类名，\n"+
			"就会被套上一个不属于它的位移（.footer-sep 就是这么错的：卡片圆点被推走 0.22627px）。\n\n"+
			"修法：给选择器加祖先作用域，例如\n"+
			"    ✗ .footer-sep { transform: translateY(var(--font-footer-shift)); }\n"+
			"    ✓ .footer-support .footer-sep { ... }\n"+
			"确属安全的存量可登记进 guardScopedAllowed 并注明理由。",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// Collapsed-nav geometry guard.
//
// The navbar collapses its outer bubbles under two independent conditions: the viewport
// media query, and the .scrolled class the scroll handler toggles. Below 960px BOTH hold at
// once — the media query keeps the bubbles collapsed, but scrolling still adds and removes
// .scrolled — so any declaration the two disagree on becomes a transition. The right
// bubble's padding differed by 10px, which slid the search icon sideways on every
// scroll-up at narrow widths.
//
// Rule: every property declared in BOTH the scrolled rule and the media-query rule must
// carry the same value. A deliberate divergence needs an entry in the exemption map.

// guardCollapseAllowed holds deliberate divergences, keyed "selector|property" -> reason.
// Keep this empty if at all possible: the two states are meant to be indistinguishable.
var guardCollapseAllowed = map[string]string{}

type guardCSSRule struct {
	at    string // enclosing at-rule preludes, outermost first
	sel   string
	decls map[string]string
}

// guardCSSRules parses a stylesheet into flat rules, tracking at-rule nesting. The input
// must already be comment-stripped.
func guardCSSRules(src string, at []string) []guardCSSRule {
	var out []guardCSSRule
	for i := 0; i < len(src); {
		open := strings.IndexByte(src[i:], '{')
		if open < 0 {
			break
		}
		open += i
		prelude := strings.TrimSpace(src[i:open])
		depth, j := 1, open+1
		for ; j < len(src) && depth > 0; j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
			}
		}
		if j <= open+1 {
			break
		}
		body := src[open+1 : j-1]
		if strings.HasPrefix(prelude, "@") {
			out = append(out, guardCSSRules(body, append(append([]string{}, at...), prelude))...)
		} else {
			out = append(out, guardCSSRule{
				at:    strings.Join(at, " "),
				sel:   prelude,
				decls: guardCSSDecls(body),
			})
		}
		i = j
	}
	return out
}

// guardCSSDecls splits a declaration block into property -> normalised value.
func guardCSSDecls(body string) map[string]string {
	out := map[string]string{}
	for _, d := range strings.Split(body, ";") {
		k, v, ok := strings.Cut(d, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out[k] = strings.Join(strings.Fields(v), " ")
	}
	return out
}

func TestCSSGuardCollapsedNavGeometryMatchesScrolled(t *testing.T) {
	raw, err := os.ReadFile("src/css/components/nav.css")
	if err != nil {
		t.Fatalf("read nav.css: %v", err)
	}
	rules := guardCSSRules(guardStripComments(string(raw)), nil)

	// The guard must not run on nothing: a renamed selector has to fail, not pass.
	lookup := func(atFrag, sel string) *guardCSSRule {
		var hits []*guardCSSRule
		for i := range rules {
			if rules[i].sel != sel {
				continue
			}
			if atFrag == "" && rules[i].at != "" {
				continue
			}
			if atFrag != "" && !strings.Contains(rules[i].at, atFrag) {
				continue
			}
			hits = append(hits, &rules[i])
		}
		if len(hits) != 1 {
			t.Fatalf("expected exactly one %q rule (at %q) in nav.css, found %d", sel, atFrag, len(hits))
		}
		return hits[0]
	}

	pairs := []struct {
		name     string
		scrolled *guardCSSRule
		media    *guardCSSRule
	}{
		{"left", lookup("", "#navbar.scrolled .nav-bubble--left"), lookup("max-width: 960px", ".nav-bubble--left")},
		{"right", lookup("", "#navbar.scrolled .nav-bubble--right"), lookup("max-width: 960px", ".nav-bubble--right")},
	}

	var bad []string
	for _, p := range pairs {
		for prop, sv := range p.scrolled.decls {
			mv, ok := p.media.decls[prop]
			if !ok || mv == sv {
				continue
			}
			key := p.scrolled.sel + "|" + prop
			if _, exempt := guardCollapseAllowed[key]; exempt {
				continue
			}
			bad = append(bad, fmt.Sprintf("%s %s: scrolled=%q media=%q", p.name, prop, sv, mv))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("折叠态几何在 .scrolled 与 @media (max-width: 960px) 之间不一致（%d 处）：\n  %s\n\n"+
			"为什么这是 bug：≤960px 时媒体查询已经把气泡折成 38px 圆，但滚动处理器**仍然**在\n"+
			"切换 .scrolled -> 两边不一致的属性会在每次上下滑时走一遍 transition，\n"+
			"把图标横着推走（右栏 padding 差 10px = 搜索图标上滑时右移 10px）。\n\n"+
			"修法：把媒体查询里的值改成与 #navbar.scrolled 一致。确属有意的差异请登记 guardCollapseAllowed 并写明理由。",
			len(bad), strings.Join(bad, "\n  "))
	}
}

// Compensation-conflict guard: one class name must never receive different compensation
// variables from two different files. Class reuse across base + theme-override +
// page-override files is legitimate and usually just a redundant pair; only two DIFFERENT
// compensation variables landing on the same class name is a real conflict.

// guardUniqueAllowed holds legacy exemptions (currently empty; new entries need a reason).
var guardUniqueAllowed = map[string]string{}

func TestCSSGuardClassDefinedOnce(t *testing.T) {
	files := guardWalk(t, []string{"src/css"}, ".css")
	clsVars := map[string]map[string]string{} // class name -> var -> file
	for _, f := range files {
		if guardSkipFile(filepath.Base(f)) {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		src := guardStripComments(string(raw))
		for _, m := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(src, -1) {
			sel, body := m[1], m[2]
			vars := regexp.MustCompile(`var\((--font-[a-z-]+-shift[^,)]*)`).FindAllStringSubmatch(body, -1)
			if len(vars) == 0 {
				continue
			}
			for _, cls := range regexp.MustCompile(`\.([A-Za-z][A-Za-z0-9_-]*)`).FindAllStringSubmatch(sel, -1) {
				if clsVars[cls[1]] == nil {
					clsVars[cls[1]] = map[string]string{}
				}
				for _, v := range vars {
					clsVars[cls[1]][v[1]] = f
				}
			}
		}
	}
	var bad []string
	for cls, vm := range clsVars {
		if len(vm) < 2 {
			continue
		}
		// A conflict only counts ACROSS files. The same class name appearing in
		// several rules within one file is normal, e.g.
		//   `.home-post-card__footer .footer-item.date { --font-data-shift }` and
		//   `.home-post-card__footer .words-unit { --font-body-shift }`
		// — the two variables belong to different descendants inside it, and the
		// regex credits the selector's class names wholesale. Only two components
		// writing two files that land on the same class name is the real
		// .footer-sep-style conflict.
		fileSet := map[string]bool{}
		for _, f := range vm {
			fileSet[f] = true
		}
		if len(fileSet) < 2 {
			continue
		}
		if _, ok := guardUniqueAllowed[cls]; ok {
			continue
		}
		var list []string
		for v, f := range vm {
			list = append(list, v+" @ "+f)
		}
		sort.Strings(list)
		bad = append(bad, fmt.Sprintf(".%s\n      %s", cls, strings.Join(list, "\n      ")))
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("发现 %d 个类名被**两个不同的补偿变量**同时套用（真冲突）：\n  %s\n\n"+
			"这类冲突不报错：两个部件的位移会互相覆盖，谁赢取决于打包顺序；\n"+
			"更糟的是其中一方会被套上不属于它的角色位移（.footer-sep 就是这么错的）。\n"+
			"修法：两个部件改用**不同的类名**。",
			len(bad), strings.Join(bad, "\n  "))
	}
}
