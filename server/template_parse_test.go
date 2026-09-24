package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestTemplatesParse confirms that all templates parse under the Go template
// engine.
//
// Template syntax errors only surface at server startup, and admin.html's
// font preview references a set of `$.Preview*` fields (the fixture layer
// from config/preview_fixtures.json) — a mistyped field name, a malformed
// {{template}} argument, or JS inside <script> that defeats html/template's
// context analysis all blow up at startup. This test moves that failure to
// `go test` time.
func TestTemplatesParse(t *testing.T) {
	tmpl, err := loadTemplatesSafe()
	if err != nil {
		t.Fatalf("模板解析失败: %v", err)
	}
	if tmpl == nil {
		t.Fatal("loadTemplatesSafe 返回 nil 模板")
	}
}

// TestAllTemplateRefsResolvable enforces the rule that every new shared
// partial must be registered in sharedTemplateFiles.
//
// templates/*.html outside main.go's sharedTemplateFiles are treated as page
// templates; their {{define}} blocks do not join the shared set, so any
// {{template "xxx"}} reference anywhere on the site fails at execution time
// with `no such template "xxx"` — and the parse stage does not report it.
// This test moves that failure from a production 500 to `go test`.
func TestAllTemplateRefsResolvable(t *testing.T) {
	base, err := loadTemplatesSafe()
	if err != nil {
		t.Fatalf("模板解析失败: %v", err)
	}

	defined := map[string]bool{}
	for _, t2 := range base.Templates() {
		defined[t2.Name()] = true
	}

	refRe := regexp.MustCompile(`\{\{\s*template\s+"([^"]+)"`)
	files, err := filepath.Glob(filepath.Join(RootTemplates, "*.html"))
	if err != nil {
		t.Fatalf("扫描 templates/ 失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("templates/ 下没有找到任何 .html")
	}

	checked := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Errorf("读取 %s 失败: %v", f, err)
			continue
		}
		for _, m := range refRe.FindAllStringSubmatch(string(src), -1) {
			name := m[1]
			checked++
			if !defined[name] {
				t.Errorf("%s 引用了 %q，但共享模板集里没有它 —— "+
					"请把它所在的文件登记进 main.go 的 sharedTemplateFiles",
					f, name)
			}
		}
	}
	t.Logf("检查了 %d 处 {{template}} 引用，全部可解析", checked)
}

// TestAdminSponsorPreviewUsesOwnCopy guards against the regression where
// multiple sponsor cards render with the same copy.
//
// The admin font workshop's sponsor preview renders several cards by looping
// the sponsor_card partial — the four display parameters (Nick/Amount/Msg/
// Date) must each come from $s (a config/preview_fixtures.json fixture entry).
// This once broke: Nick/Amount/Msg were bound to $.UI.admin.preview.sponsor_*
// (one shared sample set), so every rendered card came out identical.
func TestAdminSponsorPreviewUsesOwnCopy(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(RootTemplates, "admin.html"))
	if err != nil {
		t.Fatalf("读取 admin.html 失败: %v", err)
	}
	bad := regexp.MustCompile(`"(Nick|Amount|Msg)"\s*\$\.UI\.admin\.preview\.sponsor_`)
	if m := bad.FindString(string(src)); m != "" {
		t.Errorf("admin.html 的赞助预览又用固定样例覆写展示文案了：%q\n"+
			"四个展示参数应各自取自 $s，否则多张卡片会长得一样", m)
	}
}
