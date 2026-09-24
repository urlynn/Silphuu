package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Injection surface ↔ frontend key consistency:
// buildSiteURLs() is the source of truth injected into window.SITE_URLS and
// the service worker. Every B.* key referenced by the frontend must resolve
// to a non-nil value on that surface, otherwise it silently becomes
// undefined (silent degradation is forbidden).
func TestSiteURLsKeysResolvable(t *testing.T) {
	urls := buildSiteURLs()

	base := map[string][]string{
		"postsJSON":     nil,
		"stickersJSON":  nil,
		"view":          nil,
		"stats":         nil,
		"stickerUpload": nil,
		"friendApply":   nil,
		"assets":        nil,
		"static":        nil,
		"comment":       {"add", "like", "del", "delOwner", "list", "uploadImg"},
		"commit":        {"photo", "photoReorder", "config", "background", "setOffset", "setAvatar", "refreshStks", "post", "sponsor", "font", "fontActivate", "fontSave", "uploadImg"},
		"admin":         {"dashboard", "newPost", "edit", "status", "sentinel", "root"},
		"pages": {
			"home", "about", "guestbook", "funError", "archive", "sponsor", "friends",
			"search", "post", "posts", "topic",
		},
	}

	for group, keys := range base {
		val, ok := urls[group]
		if !ok {
			t.Fatalf("buildSiteURLs missing group %q", group)
		}
		if keys == nil {
			if isNilURL(val) {
				t.Errorf("group %q resolved to nil", group)
			}
			continue
		}
		child, ok := val.(map[string]interface{})
		if !ok {
			t.Fatalf("group %q is not a map", group)
		}
		for _, k := range keys {
			if cv, exists := child[k]; !exists || isNilURL(cv) {
				t.Errorf("buildSiteURLs missing/nil key %s.%s", group, k)
			}
		}
	}
}

func isNilURL(v interface{}) bool {
	switch x := v.(type) {
	case string:
		return x == ""
	case map[string]interface{}:
		return len(x) == 0
	}
	return v == nil
}

// Resource table consistency:
// window.__PAGE_RESOURCES__ carries only asset cache entries: every entry
// must hit under /assets and must not encode route semantics.
// The key set is pinned so a bundle that no page can load cannot be re-added.
func TestPageResourcesNonEmpty(t *testing.T) {
	res := buildPageResources()
	want := []string{"coreBundleCss", "coreBundleJs", "homeBundleCss", "homeBundleJs",
		"browseBundleCss", "browseBundleJs", "articleBundleCss", "articleBundleJs",
		"commentBundleCss", "commentBundleJs", "adminBundleCss"}
	for _, k := range want {
		if v, ok := res[k]; !ok || v == "" || !strings.HasPrefix(v, "/assets/") {
			t.Errorf("buildPageResources missing/invalid %q: %v", k, v)
		}
	}
	if len(res) != len(want) {
		t.Errorf("buildPageResources has %d entries, want %d: %v", len(res), len(want), res)
	}
}

// Residue probe: hardcoded route literals in the frontend.
// Bare string literals of backend interaction/dynamic routes
// (/api /admin /content /interact /post) must be zero.
// Ignored: bundle/third-party artifacts, template {{ }} logic, CSS url(),
// htmx wildcard matching, static asset references.
func TestNoHardcodedRouteLiterals(t *testing.T) {
	// Match quoted /xx/ path literals (single/double quotes; backticks handled separately)
	pathRe := regexp.MustCompile(`(['"])/(?:api|admin|content|interact|post|topic|tag|archive|about|guestbook|friends|sponsor|search)[^'"]*['"]`)
	backtickRe := regexp.MustCompile("`/(?:api|admin|content|interact|post|topic|tag|archive|about|guestbook|friends|sponsor|search)[^`]*`")
	excludedJS := map[string]bool{
		"fzstd.min.js": true, "htmx.min.js": true,
		"clicklove.js": true, "star.js": true,
		"kaomoji.js": true, "theme.js": true, "font-metrics-measure.js": true,
	}

	skip := func(path string) bool {
		if strings.HasSuffix(path, ".bundle.js") {
			return true
		}
		return excludedJS[filepath.Base(path)]
	}

	errs := []string{}
	for _, dir := range []string{filepath.Join(RootSrc, "js", "parts"), filepath.Join(RootAssets, "js"), RootTemplates} {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || skip(path) {
				return nil
			}
			data, _ := os.ReadFile(path)
			for i, line := range strings.Split(string(data), "\n") {
				ls := strings.TrimSpace(line)
				if ls == "" || strings.HasPrefix(ls, "//") || strings.HasPrefix(ls, "/*") ||
					strings.HasPrefix(ls, "*") || strings.HasPrefix(ls, "<!--") {
					continue
				}
				if strings.Contains(ls, "url(") || strings.Contains(ls, "href_matches") {
					continue
				}
				probe := ls
				if strings.HasSuffix(path, ".html") {
					probe = stripTemplate(ls)
					if probe == "" {
						continue
					}
				}
				if m := pathRe.FindString(probe); m != "" {
					errs = append(errs, path+":"+itoa(i+1)+"  "+ls+"   ->  "+m)
				} else if m := backtickRe.FindString(probe); m != "" {
					errs = append(errs, path+":"+itoa(i+1)+"  "+ls+"   ->  "+m)
				}
			}
			return nil
		})
	}
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("残留硬编码路由:\n  %s", e)
		}
		t.Fatalf("检测到 %d 处前端硬编码路由字面量，请改用 SITE_URLS / 模板函数", len(errs))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// stripTemplate removes {{ ... }} template logic blocks and returns the
// remaining bare literals; returns "" when nothing is left.
func stripTemplate(s string) string {
	var out strings.Builder
	in := false
	runes := []rune(s)
	for j := 0; j < len(runes); j++ {
		if !in && j+1 < len(runes) && runes[j] == '{' && runes[j+1] == '{' {
			in = true
			j++
			continue
		}
		if in && j+1 < len(runes) && runes[j] == '}' && runes[j+1] == '}' {
			in = false
			j++
			continue
		}
		if !in {
			out.WriteRune(runes[j])
		}
	}
	return strings.TrimSpace(out.String())
}

// Regression guard: {{ URL... }} template functions are banned inside <script>.
// Go html/template escapes slashes in JS context (-> \/); after html-minify
// compresses the output into backticks, browsers resolve mangled URLs like
// admin//dashboard. HTML attribute context is unaffected; only <script> is
// off limits.
// Rule: URLs inside scripts must read window.SITE_URLS (injected via
// template.JS, no escaping).
func TestNoURLTemplateFuncInsideScript(t *testing.T) {
	scriptRe := regexp.MustCompile(`\{\{\s*URL[A-Za-z]+`)
	errs := []string{}
	files, _ := filepath.Glob(filepath.Join(RootTemplates, "*.html"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		inScript := false
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(line, "<script") && !strings.Contains(line, "</script>") {
				inScript = true
			}
			if inScript && scriptRe.MatchString(line) {
				errs = append(errs, f+":"+itoa(i+1)+"  "+strings.TrimSpace(line))
			}
			if strings.Contains(line, "</script>") {
				inScript = false
			}
		}
	}
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("<script> 内禁用 URL 模板函数(Go JS 上下文转义斜杠, minify 后产生错误 URL):\n  %s", e)
		}
		t.Fatalf("检测到 %d 处 script 内 URL 模板函数，改用 window.SITE_URLS", len(errs))
	}
}
