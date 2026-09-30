package main

import (
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// uiPathOptional lists .UI paths a template reads through {{with}}/{{else}} on purpose: the
// literal in the else branch is the fallback for a deployment whose copy library omits the
// key, so an absent key is the designed path rather than a typo.
var uiPathOptional = map[string]bool{
	"status.logs.th_via": true,
}

// TestEveryUIPathResolves resolves every {{.UI.a.b}} reference in the templates against the
// merged copy library. A missing key is not an error at render time — missingkey=zero renders
// an empty string — so a typo reaches the reader as blank copy, and only a check like this one
// can see it. TestEveryPageRendersOK catches the other half: a reference that cannot be
// evaluated at all (a struct field, a scope the range replaced).
func TestEveryUIPathResolves(t *testing.T) {
	useExampleSite(t)
	loadUIStrings()

	ui := currentUIStrings()
	if len(ui) == 0 {
		t.Fatal("文案库是空的，这个守卫会空跑")
	}

	names, err := templateNames()
	if err != nil {
		t.Fatalf("templateNames: %v", err)
	}
	ref := regexp.MustCompile(`\.UI\.([A-Za-z0-9_.]+)`)
	missing := map[string]string{}
	seen := 0
	for _, name := range names {
		data, err := os.ReadFile(templatePath(name))
		if err != nil {
			t.Errorf("读不到 %s: %v", name, err)
			continue
		}
		for _, m := range ref.FindAllStringSubmatch(string(data), -1) {
			path := strings.TrimRight(m[1], ".")
			seen++
			if uiPathOptional[path] {
				continue
			}
			if !uiResolves(ui, path) {
				missing[path] = name
			}
		}
	}
	if seen < 100 {
		t.Fatalf("只从模板里找到 %d 个 .UI 引用，这个守卫会空跑", seen)
	}
	paths := make([]string, 0, len(missing))
	for p := range missing {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		t.Errorf("%s 引用了 .UI.%s，但文案库里没有这个键（渲染成空串，不报错）", missing[p], p)
	}
}

// uiResolves walks a dotted path through the flattened copy library. A path may end at a leaf
// string or at a subtree the template ranges over (the random-error cards are a map).
func uiResolves(ui map[string]interface{}, path string) bool {
	var node interface{} = ui
	for _, part := range strings.Split(path, ".") {
		m, ok := node.(map[string]interface{})
		if !ok {
			return false
		}
		if node, ok = m[part]; !ok {
			return false
		}
	}
	return node != nil
}

// TestThemePickerOrderIsAuthored checks the picker's own order and names against the copy
// library and the served stylesheets. Order is a per-entry _order value rather than the themes
// object's key order: a key order is not addressable, so nothing could verify it, and any tool
// that rewrites the file would reorder the picker with no error anywhere. The picker is
// rendered from exactly this data, so a slug no stylesheet defines would paint the page
// colourless on click, and an entry the reader drops would vanish from the picker silently.
func TestThemePickerOrderIsAuthored(t *testing.T) {
	useExampleSite(t)
	data, err := os.ReadFile(overlayRelPath("config/ui_strings.json"))
	if err != nil {
		t.Fatalf("读不到文案库: %v", err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("解析文案库: %v", err)
	}
	theme, _ := raw["theme"].(map[string]interface{})
	picker, _ := theme["picker"].(map[string]interface{})
	if picker == nil {
		t.Fatal("文案库里没有 theme.picker，这个守卫会空跑")
	}
	entries, _ := picker["themes"].(map[string]interface{})
	if len(entries) == 0 {
		t.Fatal("theme.picker.themes 是空的，这个守卫会空跑")
	}
	defined := map[string]bool{}
	for _, s := range scanThemes() {
		defined[s] = true
	}
	if len(defined) == 0 {
		t.Fatal("扫不到任何主题样式表，这个守卫会空跑")
	}

	pos := map[string]float64{}
	taken := map[float64]string{}
	for slug, node := range entries {
		entry, _ := node.(map[string]interface{})
		p, ok := entry["_order"].(float64)
		if !ok {
			t.Errorf("%s 没有 _order，浮窗里排不到位置", slug)
			continue
		}
		pos[slug] = p
		if other, dup := taken[p]; dup {
			t.Errorf("_order %v 被 %s 和 %s 同时占用", p, other, slug)
		}
		taken[p] = slug
		if !defined[slug] {
			t.Errorf("%s 没有任何样式表定义它（选中它会让整页无色）", slug)
		}
	}

	list, _ := themePickerFromRaw(raw)
	if len(list) != len(entries) {
		t.Errorf("themePickerFromRaw 返回 %d 项，文案库有 %d 项（有条目被静默丢掉）", len(list), len(entries))
	}
	prev, first := 0.0, true
	for _, e := range list {
		if !first && pos[e[0]] < prev {
			t.Errorf("themePickerFromRaw 的顺序与 _order 不符：%s 排在 _order 更大的条目之后", e[0])
		}
		prev, first = pos[e[0]], false
	}
}
