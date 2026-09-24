package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeFontSubsetExtractionAndExecution(t *testing.T) {
	_, _, err := findSubsettingTools()
	if err != nil {
		t.Skipf("Google subsetting tools not found: %v, skipping test", err)
	}

	// The full font sources under Fonts/ are not shipped in this repository
	// (license + repo size); the pipeline can only be exercised where they
	// exist locally.
	src := filepath.Join(RootFontSources, "MaokenPearl.ttf")
	if _, err := os.Stat(src); err != nil {
		t.Skipf("font source %s not available, skipping test (Font sources are not part of this repo)", src)
	}

	// 1. Exercise the Google-native HarfBuzz + WOFF2 slicing/compression pipeline
	task := fontSubsetTask{
		Src:   src,
		Chars: "字体子集测试123ABC",
		Out:   filepath.Join(os.TempDir(), "test_go_subset.woff2"),
	}

	err = executeSubsetTasks("单元测试子集", []fontSubsetTask{task})
	if err != nil {
		t.Fatalf("executeSubsetTasks failed: %v", err)
	}

	data, err := os.ReadFile(task.Out)
	if err != nil {
		t.Fatalf("failed to read output woff2: %v", err)
	}
	if len(data) < 48 {
		t.Fatalf("output woff2 too small: %d bytes", len(data))
	}

	// WOFF2 magic must be 'wOF2' (0x77, 0x4F, 0x46, 0x32)
	woff2Magic := []byte{0x77, 0x4F, 0x46, 0x32}
	if !bytes.Equal(data[:4], woff2Magic) {
		t.Fatalf("invalid woff2 magic: %v (expected %v)", data[:4], woff2Magic)
	}

	t.Logf("PASS: generated valid WOFF2 font of %d bytes", len(data))
}

func TestNativeFontSubsetsLifecycle(t *testing.T) {
	// The pipeline writes the subsets, the post index and the sticker index into the
	// deployment, so it needs one of its own.
	useExampleSite(t)
	// Exercise the full Go-native subsetting pipeline (site body, home, admin, comments)
	runFontSubset()
	runHomeSubsetAndWBN()
	runAdminFontSubset()
	runCommentFontSubset("MaokenPearl|400")
	time.Sleep(3 * time.Second)
}

// TestPreviewFixturesCollectable guards the invariant that every preview
// fixture must be collectable into the admin font subset.
//
// config/preview_fixtures.json is the single content source for the admin font
// preview, and runAdminFontSubset collects the same file into the admin
// subset via walkUILeaves — same source, so "glyphs shown in the preview ⊆
// glyphs in the admin subset" holds automatically.
//
// The invariant is fragile, though: walkUILeaves expects every text as
// {"text":..., "role":...}; a missing role is silently dropped with no error
// at all. If a fixture drifts into a different shape, the preview quietly
// loses glyphs. This test is the sentinel for that silent failure.
//
// Fixture content is never copied into this file — scenarios are asserted by
// path and read back from the fixture, so re-wording the fixture cannot turn
// this test red.
func TestPreviewFixturesCollectable(t *testing.T) {
	raw, err := os.ReadFile(dataReadPath(FilePreviewFixtures))
	if err != nil {
		t.Fatalf("读取预览夹具失败: %v", err)
	}

	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("预览夹具 JSON 解析失败: %v", err)
	}

	// Mirror the collection logic of runAdminFontSubset
	var texts []string
	roles := map[string]int{}
	walkUILeaves(tree, func(text, role string) {
		texts = append(texts, text)
		roles[role]++
	})

	if len(texts) == 0 {
		t.Fatal("预览夹具没有任何文本叶子被收录 —— 检查是否漏写 role" +
			"（walkUILeaves 要求 {text, role} 成对，且 role != \"native\"）")
	}

	// Count the characters that actually enter the subset
	chars := map[rune]struct{}{}
	for _, s := range texts {
		for _, r := range s {
			if isWebOwnable(r) {
				chars[r] = struct{}{}
			}
		}
	}
	if len(chars) == 0 {
		t.Fatal("预览夹具没有产生任何可入子集的字符")
	}

	// Every {text, role} pair in the fixture must reach the subset. A leaf
	// whose role is missing or empty is dropped by walkUILeaves without a
	// word; counting both sides turns that silent drop into a failure.
	if declared := countDeclaredLeaves(tree); declared != len(texts) {
		t.Errorf("预览夹具声明了 %d 个可收录文本，实际收录 %d 个 —— 差额来自缺 role 的叶子"+
			"（walkUILeaves 要求 {text, role} 成对，且 role != \"native\"）",
			declared, len(texts))
	}

	// Key scenes must resolve and carry collectable text — otherwise that part
	// of the preview renders empty or loses glyphs. Only the paths live here;
	// the text itself comes from the fixture.
	for _, path := range []string{
		"identity.home_motto",
		"article.title",
		"article.content",
		"latest.title",
		"archive",
		"comments[0].nick",
		"comments[1].nick",
		"sponsors[0].nick",
		"sponsors[0].msg",
		"sponsors[1].nick",
		"sponsors[1].msg",
		"taxonomy.tags",
	} {
		node, ok := fixtureLookup(tree, path)
		if !ok {
			t.Errorf("预览夹具缺少场景 %s —— 该预览会整块空掉", path)
			continue
		}
		var found int
		walkUILeaves(node, func(text, role string) { found++ })
		if found == 0 {
			t.Errorf("预览夹具的场景 %s 没有可收录文本 —— 该预览会空掉或掉字形", path)
		}
	}

	t.Logf("预览夹具收录: %d 个文本叶子 / %d 个入集字符 / 角色分布 %v",
		len(texts), len(chars), roles)
}

// countDeclaredLeaves counts the fixture nodes that ought to reach the font
// subset: every non-empty text, except the ones the fixture explicitly opts
// out of by role "native". Nodes carrying text with no usable role are counted
// as well, so that walkUILeaves dropping them shows up as a mismatch.
func countDeclaredLeaves(node any) int {
	switch v := node.(type) {
	case map[string]any:
		if text, ok := v["text"].(string); ok && text != "" {
			if role, _ := v["role"].(string); role == "native" {
				return 0
			}
			return 1
		}
		n := 0
		for _, child := range v {
			n += countDeclaredLeaves(child)
		}
		return n
	case []any:
		n := 0
		for _, child := range v {
			n += countDeclaredLeaves(child)
		}
		return n
	}
	return 0
}

// fixtureLookup resolves a dotted path with optional [n] indices, such as
// "comments[0].nick", against the parsed fixture tree.
func fixtureLookup(node any, path string) (any, bool) {
	for _, seg := range strings.Split(path, ".") {
		key := seg
		if b := strings.IndexByte(seg, '['); b >= 0 {
			key = seg[:b]
		}
		if key != "" {
			m, ok := node.(map[string]any)
			if !ok {
				return nil, false
			}
			if node, ok = m[key]; !ok {
				return nil, false
			}
		}
		for rest := seg[len(key):]; rest != ""; {
			end := strings.IndexByte(rest, ']')
			if len(rest) < 2 || rest[0] != '[' || end < 0 {
				return nil, false
			}
			n, err := strconv.Atoi(rest[1:end])
			if err != nil {
				return nil, false
			}
			arr, ok := node.([]any)
			if !ok || n < 0 || n >= len(arr) {
				return nil, false
			}
			node = arr[n]
			rest = rest[end+1:]
		}
	}
	return node, true
}

// putDataFile writes a file under <site>/config/<name>.
func putDataFile(t *testing.T, site, name, content string) {
	t.Helper()
	p := filepath.Join(site, "config", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// The admin font dropdown is data, not code: a deployment adds a face by editing its own
// config/font_map.json. This locks the derivation — the order field, the -Medium weight
// convention, the name fallback, and the skip for an entry with no source file.
//
// It also pins the engine's own font_map.json shape: that file opens with a "_comment"
// string where an entry object is expected, and Go drops such a key while keeping the
// rest. If a future change made that abort the parse, the dropdown would go silently
// empty and only this test would notice.
func TestGetAvailableFontsDerivesFromFontMap(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	putDataFile(t, site, "font_map.json", `{
  "_comment": "a string where an entry is expected must not sink the whole file",
  "Zeta":        { "src": "zeta.ttf",  "weight": "400", "name": "Zeta",        "order": 1 },
  "Alpha":       { "src": "alpha.ttf", "weight": "700", "name": "Alpha",       "order": 2 },
  "Beta-Medium": { "src": "beta.ttf",  "weight": "500", "name": "Beta Medium", "order": 3 },
  "Gamma":       { "src": "",          "weight": "400", "name": "Gamma",       "order": 4 },
  "Nameless":    { "src": "n.ttf",     "weight": "400",                      "order": 5 },
  "Bevl":        { "src": "bevl.ttf",  "weight": "200 700", "bevl": "1 100",  "order": 6 }
}`)

	got := getAvailableFonts()

	// Zeta sorts first on order although it sorts last on name: order has to win, or the
	// curated grouping in the panel collapses to alphabetical.
	want := []string{
		"'Zeta'|Zeta|400|",
		"'Alpha'|Alpha|700|",
		"'Beta'|Beta Medium|500|",  // -Medium supplies a weight of Beta, it is not its own family
		"'Nameless'|Nameless|400|", // no name in the file falls back to the key
		"'Bevl'|Bevl|200 700|1 100",
	}
	if len(got) != len(want) {
		t.Fatalf("getAvailableFonts() returned %d options, want %d: %v", len(got), len(want), got)
	}
	for i, o := range got {
		if line := strings.Join([]string{o.Family, o.Name, o.Weight, o.Bevl}, "|"); line != want[i] {
			t.Errorf("option %d = %q, want %q", i, line, want[i])
		}
	}
	for _, o := range got {
		if strings.Contains(o.Family, "Gamma") {
			t.Errorf("an entry with no src was offered as selectable: %v", o)
		}
	}
}

// TestExtraCharsPresetFallsBackToTheEngineCopy covers a deployment that ships
// config/font_extra_chars.json but no config/presets/: the preset characters it names must
// still come from the engine's own copy, like every other data file does.
func TestExtraCharsPresetFallsBackToTheEngineCopy(t *testing.T) {
	preset, err := os.ReadFile(filepath.Join("config", "presets", "gb2312_level1.bin"))
	if err != nil {
		t.Fatalf("引擎自带 presets 读不到: %v", err)
	}

	InitRoots(t.TempDir())
	t.Cleanup(func() { InitRoots("") })

	cmt := loadAllExtraChars()["cmt"]
	if len(cmt) == 0 {
		t.Fatal("部署目录没有 presets/ 时，cmt 的预设字符全部丢失")
	}
	for i := 0; i+1 < len(preset); i += 2 {
		r := rune(binary.BigEndian.Uint16(preset[i:]))
		if _, ok := cmt[r]; !ok {
			t.Fatalf("preset 字符 %q (U+%04X) 没有回退到引擎副本", r, r)
		}
	}
}

// TestLoadPreviewFixtures verifies the fixture JSON -> real Go types path.
// What matters is that everything the templates call actually works:
// Date.Format, UpdatedAt.IsZero, UAShort(), RenderedContent() — if any of
// these breaks, the preview scenes fail to render.
func TestLoadPreviewFixtures(t *testing.T) {
	pv := loadPreviewFixtures()

	if pv.Nickname == "" || pv.HomeMotto == "" {
		t.Error("identity 为空 —— hero_section 预览会缺博客名/签名")
	}
	if pv.Post == nil || pv.Post.Title == "" {
		t.Fatal("article 夹具为空 —— 文章预览场景会整块空掉")
	}
	if pv.Post.Content == "" {
		t.Error("article.Content 为空 —— renderMarkdown 没产出内容")
	}
	if pv.Post.Date.IsZero() {
		t.Error("article.Date 未解析 —— 模板的 .P.Date.Format 会渲染出零值日期")
	}
	if pv.Post.UpdatedAt.IsZero() {
		t.Error("article.UpdatedAt 未解析 —— components.html 的「最近编辑」块会不渲染")
	}
	if len(pv.Post.Tags) == 0 {
		t.Error("article.Tags 为空")
	}
	if pv.Latest == nil || pv.Latest.Title == "" {
		t.Error("latest 夹具为空 —— 最新文章卡会空掉")
	}
	if len(pv.Posts) < 2 {
		t.Errorf("PreviewPosts 只有 %d 篇，首页/归档预览需要至少 2 篇", len(pv.Posts))
	}
	if len(pv.Archive) == 0 || len(pv.Archive[0].Months) == 0 {
		t.Error("archive 夹具为空 —— 归档预览场景会空掉")
	}
	if len(pv.Comments) < 2 {
		t.Errorf("comments 只有 %d 条，评论预览需要至少 2 条（含一条回复）", len(pv.Comments))
	}
	if len(pv.Sponsors) == 0 {
		t.Error("sponsors 夹具为空 —— 赞助预览场景会空掉")
	}
	if len(pv.Categories) == 0 || len(pv.AllTags) == 0 {
		t.Error("taxonomy 为空 —— 分类/标签导航预览会空掉")
	}
	if pv.Stats.TotalPV == 0 {
		t.Error("stats 为空 —— 首页统计卡会全 0")
	}

	// Methods the templates actually call must work
	if len(pv.Comments) > 0 {
		if got := pv.Comments[0].UAShort(); got == "" {
			t.Error("Comment.UAShort() 返回空 —— UA 串无法解析，评论预览会缺浏览器标签")
		}
		if got := pv.Comments[0].RenderedContent(); got == "" {
			t.Error("Comment.RenderedContent() 返回空")
		}
	}
	if len(pv.Sponsors) > 0 && pv.Sponsors[0].Nick == "" {
		t.Error("Sponsor.Nick 为空")
	}

	// Guard against the "mixing" regression: multiple sponsor cards must each
	// keep their own copy. This once broke — the admin preview overwrote every
	// card's Nick/Amount/Msg with the same ui_strings sample set, so all
	// rendered cards looked identical.
	if len(pv.Sponsors) >= 2 {
		a, b := pv.Sponsors[0], pv.Sponsors[1]
		if a.Nick == b.Nick {
			t.Errorf("两个赞助者昵称相同（%q）—— 是否又被同一组样例覆写了？", a.Nick)
		}
		if a.Msg == b.Msg {
			t.Errorf("两个赞助者留言相同（%q）—— 是否又被同一组样例覆写了？", a.Msg)
		}
	}
}

// probeChars appear nowhere else in the fixture — not in the engine's default ui_strings.json,
// not in the data JSON, not in the Go string literals the caption role collects (see
// collectGoStringChars, whose file list excludes tests). A character that only a post title
// carries therefore marks exactly the roles that title feeds.
const probeChars = "龘麤"

// TestPostTitleCharsReachEveryRoleThatRendersTitles: one title, three roles, three fonts.
//
// The site-wide collector once fed titles into heading only, while the templates also draw
// them in subheading (.post-title cards, .search-title, .home-post-card__title) and body
// (.ap-title archive rows, .cat-post-title sidebar rows). List and archive pages therefore
// showed the uncovered characters in a fallback family, which reads as "the title was not
// subset at all". The expected role set is written out here rather than derived, so changing
// where a title is rendered means updating this test — and that is the moment to ask whether
// the collector needs the new role too.
func TestPostTitleCharsReachEveryRoleThatRendersTitles(t *testing.T) {
	useExampleSite(t)

	writeTestFile(t, filepath.Join(RootPosts, "software", "title-role-probe.md"),
		"---\ntitle: "+probeChars+"\nsummary: probe\ncategory: software\ntags: [probe]\n"+
			"date: 2026-01-02\nid: 4242\n---\n\nA body that carries neither probe character.\n")

	roleChars := collectSiteRoleChars()

	// Precondition: the collector really read the fixture, so a failure below means the
	// title's roles are wrong rather than that nothing was scanned.
	if _, ok := roleChars["heading"]['渲']; !ok {
		t.Fatalf("collector did not read the fixture posts (heading lacks 渲 from " +
			"posts/software/markdown-demo.md), so this test would pass vacuously")
	}

	first, second := []rune(probeChars)[0], []rune(probeChars)[1]
	got := map[string]bool{}
	for role, chars := range roleChars {
		if _, ok := chars[first]; !ok {
			continue
		}
		if _, ok := chars[second]; !ok {
			continue
		}
		got[role] = true
	}

	want := map[string]bool{"heading": true, "subheading": true, "body": true}
	var missing, extra []string
	for role := range want {
		if !got[role] {
			missing = append(missing, role)
		}
	}
	for role := range got {
		if !want[role] {
			extra = append(extra, role)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("a post title is rendered in %v but these roles got none of its characters: %v\n"+
			"  heading    .article-title (article page h1)\n"+
			"  subheading .post-title, .search-title, .home-post-card__title\n"+
			"  body       .ap-title, .cat-post-title\n"+
			"a role left out falls back to a system family for the missing glyphs",
			want, missing)
	}
	if len(extra) > 0 {
		t.Errorf("these roles carry the title-only characters %q but no template renders a "+
			"title in them: %v — either the fixture leaked the characters into another source, "+
			"or a role is being fed characters nothing draws", probeChars, extra)
	}
}

// TestSiteSubsetSkipsPostsTheSiteNeverRenders: the collector must walk the same directories
// the post index does. Walking unused directories subsets bytes for glyphs no page draws.
func TestSiteSubsetSkipsPostsTheSiteNeverRenders(t *testing.T) {
	useExampleSite(t)

	writeTestFile(t, filepath.Join(RootPosts, ".draft", "hidden.md"),
		"---\ntitle: "+probeChars+"\nid: 9001\n---\n\nx\n")
	writeTestFile(t, filepath.Join(RootPosts, "software", "nested", "hidden.md"),
		"---\ntitle: "+probeChars+"\nid: 9002\n---\n\nx\n")

	roleChars := collectSiteRoleChars()

	// Precondition: the rendered fixture post is still collected, so a green result below
	// cannot come from the walk having found nothing at all.
	if _, ok := roleChars["heading"]['渲']; !ok {
		t.Fatalf("collector did not read the fixture posts at all")
	}

	for role, chars := range roleChars {
		for _, c := range probeChars {
			if _, ok := chars[c]; ok {
				t.Errorf("role %q collected %q, which only appears in posts/ the site never "+
					"renders (.draft/ and a nested subdirectory) — the collector is walking "+
					"directories the index does not", role, c)
			}
		}
	}
}

// TestHomeRolesAreASubsetOfSiteRoles: the home page is one page of the site, so every role
// its own subset collects must also exist in the site-wide set. A role added to the home set
// alone renders correctly on / and falls back to a system family on every other page — the
// hardest version of this bug to notice, because the home page is the one people check.
func TestHomeRolesAreASubsetOfSiteRoles(t *testing.T) {
	useExampleSite(t)

	site := collectSiteRoleChars()
	var orphan []string
	for role := range homeSubsetRoles {
		if _, ok := site[role]; !ok {
			orphan = append(orphan, role)
		}
	}
	sort.Strings(orphan)
	if len(orphan) > 0 {
		t.Errorf("the home subset collects roles the site-wide collector does not know: %v\n"+
			"the home page would render them and every other page would fall back", orphan)
	}
}

// The font role registry
//
// One role is named in five places, each with its own convention, and nothing checked that the
// five agree:
//
//	site.json font_presets[].fonts    snake_case  fun_pill              the input: family, weight, bevl
//	site.json font_presets[].metrics  kebab-case  fun-pill              the output: measured shift / ink
//	collectSiteRoleChars() role keys    snake_case  fun_pill              which characters the role draws
//	setFont("fun-pill", ...) in main.go kebab-case  --font-fun-pill       the CSS declaration
//	main.go's bases list                kebab-case  --font-fun-pill-shift metrics only
//	CSS / templates / JS                kebab-case  var(--font-fun-pill)  the consumers
//
// The only translation between the two conventions is the hand-written setFont list, and every
// way it can go wrong is silent: a role collects no characters, a family gets no subset, or a
// declaration never reaches the CSS. These tests hold the registries against each other. They
// read the engine's own sources (the test CWD is server/), so they need no site fixture.

var (
	// fontFamilyDecl captures the value of a font-family declaration in either form: the CSS /
	// inline-style one (`font-family: ...`) or the JS property one (`el.style.fontFamily = ...`).
	// Only font-family is scanned: --font-scale-body is a size and --font-*-shift an offset, and
	// neither is a role.
	fontFamilyDecl = regexp.MustCompile(`(?i)(?:font-family\s*:|fontFamily\s*[:=])([^;}\n]*)`)
	// fontVarRef captures the role name of a var(--font-<name>) reference.
	fontVarRef = regexp.MustCompile(`var\(--font-([a-z0-9-]+)`)
)

// fontRolesDeclaredOutsideSetFont reach the CSS through their own path in main.go rather than a
// setFont call: --font-cmt from the cmt block (the comment subset has its own pipeline) and
// --font-data from the family-stack build. Anything else turning up here is drift, not a
// special case — which is why these are ceilings to check against, never equalities.
var fontRolesDeclaredOutsideSetFont = map[string]bool{"cmt": true, "data": true}

// fontMetricNamesWithoutSetFont are names in main.go's bases list that no setFont call declares:
// cmt and data as above, plus data-num, the metric key of the data_override_num role whose
// family joins the data stack.
var fontMetricNamesWithoutSetFont = map[string]bool{"cmt": true, "data": true, "data-num": true}

// fontRolesCollectedWithoutSetFont are roles the collector feeds but setFont never declares:
// data draws counts, dates and units, yet is written by the family-stack path.
var fontRolesCollectedWithoutSetFont = map[string]bool{"data": true}

// fontRolesDeclaredWithoutCollector are setFont roles the collector cannot feed. Only symbol,
// and deliberately: kaomoji.css and nav.css both describe --font-symbol as the native *system*
// symbol font — it draws kaomoji and the cmd glyph — so no charset is collected for it. A site
// that points Fonts["symbol"] at a family gets no subset for that family and the declaration
// falls through to the next one in the stack: ineffective, not broken. Registering the role in
// the collector would be an improvement, and this ceiling allows it.
var fontRolesDeclaredWithoutCollector = map[string]bool{"symbol": true}

// parseSetFontPairs returns cssBase -> cfgKey for every
// setFont("css-base", active.Fonts["cfg_key"]) call in main.go.
func parseSetFontPairs(t *testing.T) map[string]string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatalf("parse main.go: %v", err)
	}
	pairs := map[string]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "setFont" || len(call.Args) != 2 {
			return true
		}
		cssBase, ok := stringLitValue(call.Args[0])
		if !ok {
			return true
		}
		idx, ok := call.Args[1].(*ast.IndexExpr)
		if !ok {
			return true
		}
		if cfgKey, ok := stringLitValue(idx.Index); ok {
			pairs[cssBase] = cfgKey
		}
		return true
	})
	return pairs
}

// stringLitValue unquotes a string literal expression.
func stringLitValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// parseStringSliceAssign returns the string literals of the composite literal assigned to name
// in path, for either `name := []string{...}` or `var name = []string{...}`.
func parseStringSliceAssign(t *testing.T, path, name string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		if id, ok := as.Lhs[0].(*ast.Ident); !ok || id.Name != name {
			return true
		}
		cl, ok := as.Rhs[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, el := range cl.Elts {
			if s, ok := stringLitValue(el); ok {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// parseSiteSubsetRoleKeys returns the role keys of the map that collectSiteRoleChars ranges over
// to seed its per-role character sets.
func parseSiteSubsetRoleKeys(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "font_subset.go", nil, 0)
	if err != nil {
		t.Fatalf("parse font_subset.go: %v", err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		rs, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		cl, ok := rs.X.(*ast.CompositeLit)
		if !ok {
			return true
		}
		mt, ok := cl.Type.(*ast.MapType)
		if !ok {
			return true
		}
		if id, ok := mt.Value.(*ast.Ident); !ok || id.Name != "bool" {
			return true
		}
		for _, el := range cl.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if s, ok := stringLitValue(kv.Key); ok {
				out = append(out, s)
			}
		}
		return true
	})
	return out
}

// fontRolesRenderedInTree returns every role named inside a font-family declaration under roots,
// mapped to one file that names it.
func fontRolesRenderedInTree(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".css", ".html", ".js":
			default:
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, decl := range fontFamilyDecl.FindAllStringSubmatch(string(body), -1) {
				for _, ref := range fontVarRef.FindAllStringSubmatch(decl[1], -1) {
					if _, seen := out[ref[1]]; !seen {
						out[ref[1]] = path
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	return out
}

// toSet builds a set from a string slice.
func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, s := range items {
		out[s] = true
	}
	return out
}

// keysOf returns the keys of a string-keyed map.
func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// outsideOf returns the sorted members of want that allowed does not contain.
func outsideOf(want, allowed map[string]bool) []string {
	var out []string
	for k := range want {
		if !allowed[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// TestSetFontTranslatesRoleNamesMechanically: the setFont list is the one place a config role
// key turns into a CSS variable name, and it is written out by hand.
func TestSetFontTranslatesRoleNamesMechanically(t *testing.T) {
	pairs := parseSetFontPairs(t)
	if len(pairs) < 15 {
		t.Fatalf("parsed only %d setFont calls from main.go, so this guard would pass vacuously", len(pairs))
	}
	byKey := map[string]string{}
	for cssBase, cfgKey := range pairs {
		if want := strings.ReplaceAll(cfgKey, "_", "-"); want != cssBase {
			t.Errorf("setFont(%q, active.Fonts[%q]): the CSS base must be the config key with _ turned into -, i.e. %q", cssBase, cfgKey, want)
		}
		if prev, dup := byKey[cfgKey]; dup {
			t.Errorf("config key %q is read by two setFont calls (--font-%s and --font-%s)", cfgKey, prev, cssBase)
		}
		byKey[cfgKey] = cssBase
	}
}

// TestFontRoleRegistriesCoverEachOther: the declaration list, the metrics list and the collector
// must all describe the same roles.
func TestFontRoleRegistriesCoverEachOther(t *testing.T) {
	declared := make(map[string]bool)
	for cssBase := range parseSetFontPairs(t) {
		declared[cssBase] = true
	}
	if len(declared) < 15 {
		t.Fatalf("parsed only %d roles from main.go", len(declared))
	}

	// 1. Every declared role needs a metrics entry, or its --font-<role>-shift and -ink-height
	//    are never written and the role silently loses its ink compensation.
	bases := toSet(parseStringSliceAssign(t, "main.go", "bases"))
	if len(bases) < 15 {
		t.Fatalf("parsed only %d entries from main.go's bases list", len(bases))
	}
	for _, role := range outsideOf(declared, bases) {
		t.Errorf("--font-%s is declared by setFont but absent from main.go's bases list, so no -shift/-ink-height is ever written for it", role)
	}
	for _, name := range outsideOf(bases, declared) {
		if !fontMetricNamesWithoutSetFont[name] {
			t.Errorf("main.go's bases list has %q, but setFont never declares --font-%s: a metrics name with no role", name, name)
		}
	}

	// 2. The collector must be able to feed every declared role. A role it cannot feed gives its
	//    family an empty charset, so no subset is produced for that family at all.
	var collected []string
	for _, key := range parseSiteSubsetRoleKeys(t) {
		collected = append(collected, strings.ReplaceAll(key, "_", "-"))
	}
	collectedSet := toSet(collected)
	if len(collectedSet) < 15 {
		t.Fatalf("parsed only %d roles from collectSiteRoleChars", len(collectedSet))
	}
	for _, role := range outsideOf(declared, collectedSet) {
		if !fontRolesDeclaredWithoutCollector[role] {
			t.Errorf("--font-%s is declared by setFont but collectSiteRoleChars cannot feed it: a family assigned this role gets an empty charset and no subset file", role)
		}
	}
	for _, role := range outsideOf(collectedSet, declared) {
		if !fontRolesCollectedWithoutSetFont[role] {
			t.Errorf("collectSiteRoleChars collects characters for %q, but no setFont call declares --font-%s: nothing renders it", role, role)
		}
	}
}

// TestEveryFontFamilyRoleIsDeclared: a role named in a font-family declaration must have a
// declaration of its own. Otherwise var(--font-<role>) resolves to the system stack in
// tokens.css and the site font silently never applies to that role.
func TestEveryFontFamilyRoleIsDeclared(t *testing.T) {
	allowed := map[string]bool{}
	for cssBase := range parseSetFontPairs(t) {
		allowed[cssBase] = true
	}
	if len(allowed) < 15 {
		t.Fatalf("parsed only %d roles from main.go", len(allowed))
	}
	for role := range fontRolesDeclaredOutsideSetFont {
		allowed[role] = true
	}

	rendered := fontRolesRenderedInTree(t, "src/css", "templates", "src/js")
	if len(rendered) < 15 {
		t.Fatalf("found only %d roles in font-family declarations under src/css, templates and src/js, so this guard would pass vacuously", len(rendered))
	}
	for _, role := range outsideOf(toSet(keysOf(rendered)), allowed) {
		t.Errorf("--font-%s is used in a font-family declaration in %s, but nothing declares it: it can only resolve to the system stack in tokens.css", role, rendered[role])
	}
}

// The cmt subset must carry the comment area's own copy, effect markers resolved, and
// must not drag in copy from modules it never renders.
func TestCollectCommentCopyChars(t *testing.T) {
	if len(commentBlocks) == 0 {
		t.Fatal("commentBlocks 为空，这个守卫会空跑")
	}
	// Both module keys are derived from the list under test: the positive one is a real
	// member, the negative one is synthetic. Naming a real module here would tie the
	// assertion to one site's module set instead of to the list itself.
	inside := commentBlocks[0]
	const outside = "not_a_comment_module"

	root := map[string]any{
		inside: map[string]any{
			"form": map[string]any{
				"ph":   map[string]any{"text": "甲~~乙丙~~丁", "role": "body"},
				"aria": map[string]any{"text": "庚辛", "role": "native"},
			},
		},
		outside: map[string]any{
			"notice": map[string]any{"text": "||戊己||", "role": "body"},
		},
	}

	chars := map[rune]struct{}{}
	collectCommentCopyChars(root, chars)

	for _, c := range "甲乙丙丁" {
		if _, ok := chars[c]; !ok {
			t.Errorf("评论模块的字 %q 没被收进 cmt 子集", c)
		}
	}
	if _, ok := chars['\u0336']; ok {
		t.Error("U+0336 被收进来了：文案划线改走 <del> 后不再需要这个字形")
	}
	if _, ok := chars['戊']; ok {
		t.Error("commentBlocks 之外的模块被收进来了：cmt 子集只该装评论区会画的字")
	}
	if _, ok := chars['庚']; ok {
		t.Error("role=native 的叶子被收进来了：那是系统字体的活")
	}
}

// commentBlocks is hand-written, so it can drift from the templates. Fail when a
// comment-area template references a ui_strings module the list does not carry.
func TestCommentSubsetCoversCommentTemplates(t *testing.T) {
	declared := map[string]bool{}
	for _, m := range commentBlocks {
		declared[m] = true
	}

	re := regexp.MustCompile(`\.UI\.([a-z_]+)`)
	seen := map[string]string{}
	for _, name := range commentTemplates {
		data, err := os.ReadFile(templatePath(name))
		if err != nil {
			t.Errorf("读不到 %s: %v", name, err)
			continue
		}
		for _, m := range re.FindAllStringSubmatch(string(data), -1) {
			if _, ok := seen[m[1]]; !ok {
				seen[m[1]] = name
			}
		}
	}
	if len(seen) < 2 {
		t.Fatalf("只从评论区模板里找到 %d 个 .UI.<模块>，这个守卫会空跑", len(seen))
	}
	for mod, file := range seen {
		if !declared[mod] {
			t.Errorf("%s 引用了 .UI.%s，但 commentBlocks 里没有它，cmt 子集不会收这个模块的文案", file, mod)
		}
	}
}
