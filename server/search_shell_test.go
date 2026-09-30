package main

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"text/template"
)

// withTempDist points the render cache at a temporary directory for the duration of a test, so
// these tests never touch the repository's public/.
func withTempDist(t *testing.T) {
	t.Helper()
	old := RootRender
	RootRender = t.TempDir()
	t.Cleanup(func() { RootRender = old })
}

// TestSearchShellIsQueryIndependent pins the /search contract.
//
// The page is a static shell: the query never reaches the server, so every ?q= variant renders
// the same bytes and a one-year CDN copy can never go stale. Two consequences are easy to break
// by accident:
//
//  1. It must be a full page. Site-wide navigation uses body hx-boost +
//     hx-select="#content-wrapper" + outerHTML swap; a response without #content-wrapper makes
//     hx-select match nothing and the swap deletes the container — search appeared stuck (the URL
//     changed to /search but the page went blank).
//  2. It must not depend on the query. Anything query-dependent makes the bytes differ per ?q=
//     and reintroduces exactly the staleness the shell exists to remove.
func TestSearchShellIsQueryIndependent(t *testing.T) {
	useExampleSite(t)
	withTempDist(t)
	rebuildPostIndex()
	initTemplates()
	loadUIStrings()
	warmSearchShell()

	render := func(rawQuery string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/search?"+rawQuery, nil)
		req.Header.Set("HX-Request", "true")
		w := httptest.NewRecorder()
		handleSearchPage(w, req)
		if w.Code != 200 {
			t.Fatalf("handleSearchPage(%s) status = %d, want 200 (body: %.200s)",
				rawQuery, w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	hit := render("q=Go")
	miss := render("q=%E5%AE%8C%E5%85%A8%E4%B8%8D%E5%AD%98%E5%9C%A8%E7%9A%84%E8%AF%8D")

	// 1) Full page, so the hx-boost swap keeps #content-wrapper.
	if !strings.Contains(hit, "content-wrapper") {
		t.Errorf("shell 缺少 #content-wrapper —— hx-select 会选空, outerHTML swap 将删除容器")
	}
	if !strings.Contains(hit, "<main") {
		t.Errorf("shell 缺少 <main>, 不是完整页面")
	}

	// 2) Every post's card is pre-rendered, via post_card — one implementation of the card.
	//
	//    Checked per post by title, not by counting .card-row-wrapper: the empty card carries
	//    that class too, so counting passes even with zero real cards — which is exactly the
	//    ordering bug this guards (the shell was once warmed before the post index was built,
	//    and shipped an empty list that still looked structurally right).
	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("测试前提不成立: 索引里没有文章")
	}
	for _, p := range posts {
		if !strings.Contains(hit, template.HTMLEscapeString(p.Title)) {
			t.Errorf("shell 缺文章 %q 的卡片 —— 外壳必须在文章索引就绪之后生成", p.Title)
		}
	}
	// The summary slot has to exist even though its text is blanked: it is where the browser
	// puts the excerpt cut from the post body.
	if !strings.Contains(hit, "post-summary search-snippet") {
		t.Errorf("shell 的卡片缺 .post-summary 落点 —— 浏览器没有地方填正文摘要")
	}
	// The empty card comes from post_empty_card, not from JS.
	if !strings.Contains(hit, "empty-card") {
		t.Errorf("shell 缺预渲染的空结果卡 (post_empty_card)")
	}

	// 3) Still not the legacy dropdown fragment.
	if strings.Contains(hit, "search-item") {
		t.Errorf("shell 含下拉片段标记 (search-item) —— 那是弹窗专用的")
	}

	// 4) The bytes must not depend on the query. This is the whole point of the shell.
	if hit != miss {
		t.Errorf("两个不同 ?q= 的响应字节不同 —— CDN 上必然陈旧")
	}

	// 5) The client renderer needs these hooks; without them the page renders nothing.
	//    Matched without quotes: the cached HTML is minified, which drops optional quotes
	//    (`id="searchPageResults"` becomes `id=searchPageResults`).
	for _, want := range []string{"searchPageResults", "searchPageCount", "searchPageEmpty"} {
		if !strings.Contains(hit, want) {
			t.Errorf("shell 缺少 %s —— 前端渲染会失效", want)
		}
	}

	// 6) The frontmatter summary must not survive into the shell: the excerpt a visitor should
	//    see comes from the body, and leaving the written summary in place would flash the wrong
	//    text before the browser replaces it.
	for _, p := range posts {
		if s := strings.TrimSpace(string(p.Summary)); s != "" && strings.Contains(hit, s) {
			t.Errorf("shell 里出现了站点手写的摘要 %q —— 会先闪出来再被替换", s)
		}
	}
}

// The shell is generated once at startup. If the render cache is dropped, warmPages has to
// regenerate it — otherwise /search 500s until the next restart.
func TestWarmListPagesRegeneratesSearchShell(t *testing.T) {
	withTempDist(t)
	rebuildPostIndex()
	initTemplates()
	loadUIStrings()

	if _, err := os.Stat(cachePath(PageSearch)); err == nil {
		t.Fatal("test setup is broken: the shell already exists in a fresh cache dir")
	}

	warmPages()

	if _, err := os.Stat(cachePath(PageSearch)); err != nil {
		t.Fatalf("warmPages 没有重建 /search 外壳: %v", err)
	}
}
