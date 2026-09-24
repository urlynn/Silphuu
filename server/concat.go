package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RTT60 modular bundling: compiles all site static assets into tiered independent
// bundles and strips all comments to minimize CDN transfer size.

// JS bundle definitions.

var coreJSFiles = []string{
	"src/js/vendor/htmx.min.js",          // fetched at build time, see vendorDeps
	"src/js/vendor/hx-head.min.js",       // htmx hx-head extension: CSSOM await + automatic head merge
	"src/js/vendor/hx-preload.min.js",    // htmx preload extension: instant open on mousedown/touchstart
	"src/js/parts/03-nav-filter.js",      // nav category+tag dropdown panels
	"src/js/parts/04-mode-transition.js", // right-column "mode" transition animation + F5 entrance
	"src/js/parts/05-accordion.js",       // category accordion + body class sync + list pagination
	"src/js/parts/06-search.js",          // 0ms pure-frontend full-text in-memory instant search + highlight
	"src/js/parts/14-backdrop.js",        // Chromium backdrop-filter replication engine (must load before its consumers:
	//   12/13 call window.__glassKit / __backdropRescan at parse time)
	"src/js/parts/12-lifecycle.js",      // app loading / scroll progress / dynamic asset scheduler
	"src/js/parts/13-ink-transition.js", // ink-shatter engine: article line shatter / comment FLIP / TOC per-char
	"src/js/theme.js",                   // theme switching
	"src/js/stats.js",                   // view stats + 3D mechanical scroll-wheel effects
	"src/js/kaomoji.js",                 // kaomoji
	"src/js/navbar.js",                  // navbar bubble physics + signature shatter
}

var homeJSFiles = []string{}

var browseJSFiles = []string{
	"src/js/parts/01-core.js",        // card physics fly-in engine: setupCardAnimations / _browseRevealIO
	"src/js/parts/02-aslide.js",      // archive & list pagination ASLIDE engine + setupBrowseHeaderAnimation
	"src/js/timeline.js",             // archive SVG bezier curve dynamic drawing
	"src/js/parts/waterfall-anim.js", // photo-wall waterfall entrance effects + parallax flow engine
	"src/js/parts/15-sponsor-fx.js",  // sponsor card hover material effects
}

var articleJSFiles = []string{
	"src/js/parts/toc.js", // article TOC generation + scroll reading indicator
}

var commentJSFiles = []string{
	"src/js/parts/07-comment.js", // comment likes / delete / word count
	"src/js/parts/08-upload.js",  // comment image upload / paste
	"src/js/parts/09-sticker.js", // sticker picker
	"src/js/parts/10-reply.js",   // comment preview / reply mode
	"src/js/parts/11-media.js",   // image lazy loading / @nickname jump
	"src/js/parts/16-avatar.js",  // commenter avatar dialog
}

var editorJSFiles = []string{
	"src/js/vendor/marked.umd.js",
	"src/js/vendor/purify.min.js",
	"src/js/vendor/prism.min.js",
	"src/js/vendor/prism-go.min.js",
	"src/js/vendor/prism-bash.min.js",
	"src/js/vendor/prism-json.min.js",
	"src/js/vendor/prism-rust.min.js",
	"src/js/vendor/prism-python.min.js",
	"src/js/vendor/prism-zig.min.js",
	"src/js/vendor/prism-yaml.min.js",
	"src/js/vendor/prism-toml.min.js",
	"src/js/vendor/prism-nginx.min.js",
}

var adminJSFiles = []string{
	"src/js/vendor/fzstd.min.js",     // fetched at build time; status.js's dependency (must come first)
	"src/js/status.js",               // status centre client engine
	"src/js/font-metrics-measure.js", // font workshop measurement
}

// CSS bundle definitions.

var coreCSSFiles = []string{
	"src/css/tokens.css",
	"src/css/themes/oceans-dark.css",
	"src/css/themes/forest-light.css",
	"src/css/themes/frozen-dark.css",
	"src/css/themes/golden-light.css",
	"src/css/themes/cyanic-light.css",
	"src/css/themes/violet-light.css",
	"src/css/themes/stream-dark.css",
	"src/css/themes/coffee-dark.css",
	"src/css/themes/indigo-dark.css",
	"src/css/themes/summer-light.css",
	"src/css/themes/sakura-light.css",
	"src/css/themes/purple-dark.css",
	"src/css/themes/elements.css",
	"src/css/fonts.css",
	"src/css/components/nav.css",
	"src/css/components/glass.css",
	"src/css/components/hover-layers.css",
	"src/css/components/ink.css",
	"src/css/components/footer.css",
	"src/css/components/inline-pill.css",
	"src/css/components/background.css",
	"src/css/components/loading.css",
	"src/css/components/icons.css",
	"src/css/components/code.css", // code block Chroma token colors (site-wide)
	"src/css/components/kaomoji.css",
	"src/css/components/search.css",
	"src/css/components/page-header.css",
	"src/css/components/modal.css",
	"src/css/components/post-sidebar.css",
	"src/css/components/scroll-progress.css",
}

var homeCSSFiles = []string{
	"src/css/components/hero.css",
	"src/css/components/home-section.css",
}

var browseCSSFiles = []string{
	"src/css/components/post-list.css",
	"src/css/components/browse-header.css",
	"src/css/components/ink-row.css",
	"src/css/components/archive.css",
	"src/css/components/gallery.css",
	"src/css/components/friends.css",
	"src/css/components/sponsor.css",
	"src/css/components/sponsor-fx.css", // sponsor card hover material effects layer (sfx-*, pairs with 15-sponsor-fx.js)
	"src/css/components/fun-error.css",
}

var articleCSSFiles = []string{
	"src/css/themes/page-article.css",
	"src/css/content.css",
	"src/css/components/article-meta.css",
	"src/css/components/post-article.css",
}

var commentCSSFiles = []string{
	"src/css/components/cmt.css",
	"src/css/components/lazy-image.css",
}

var editorCSSFiles = []string{
	"src/css/editor.css",
}

var adminCSSFiles = []string{
	"src/css/admin.css",
	"src/css/components/status.css",
}

// assetLicenceBanner heads every built asset.
//
// `/*!` is the marker that makes minifiers keep a comment, which is why it also
// has to survive stripCSSComments / stripJSComments — see isPreservedComment.
//
// The notice travels inside the asset because the only other place the licence is
// stated is the page footer, and someone who pulls the CSS or JS straight out of
// devtools never sees the footer at all. This front end is BSL-licensed: without
// the notice a copier can plausibly assume it is free to reuse commercially.
//
// Bump the year here whenever the copyright line in LICENSE-FRONTEND changes.
const assetLicenceBanner = "/*! Silphuu · BSL 1.1 (non-commercial) · github.com/urlynn/Silphuu · © 2026 Urlynn */\n"

// Third-party copyright lines that have to survive into the bundle. MIT and Apache-2.0
// require the notice in every copy, and the upstream files lose theirs either to a CDN's
// minifier or to stripJSComments — see docs/GOTCHAS.md §vendor-notice-preservation.
const (
	fzstdNotice     = "/*! fzstd · Copyright (c) 2020 Arjun Barrett · MIT · github.com/101arrowz/fzstd */"
	markedNotice    = "/*! marked · Copyright (c) 2011-2025 Christopher Jeffrey and contributors · MIT · github.com/markedjs/marked */"
	dompurifyNotice = "/*! DOMPurify · Copyright (c) Cure53 and other contributors · Apache-2.0 or MPL-2.0 · github.com/cure53/DOMPurify */"
	prismNotice     = "/*! Prism · Copyright (c) 2012 Lea Verou · MIT · prismjs.com */"
)

// vendorNoticeFor returns the copyright line a vendored file carries, or "" when its
// licence requires none. htmx is deliberately absent: it is 0BSD.
func vendorNoticeFor(name string) string {
	switch {
	case name == "fzstd.min.js":
		return fzstdNotice
	case name == "marked.umd.js":
		return markedNotice
	case name == "purify.min.js":
		return dompurifyNotice
	case name == "prism.min.js" || strings.HasPrefix(name, "prism-"):
		return prismNotice
	}
	return ""
}

// bundleNotices collects the distinct notices for the files a bundle actually used, in
// first-seen order. The ten prism-* grammars collapse to a single line.
func bundleNotices(used []string) string {
	seen := make(map[string]bool, len(used))
	var lines []string
	for _, f := range used {
		n := vendorNoticeFor(filepath.Base(f))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		lines = append(lines, n)
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

// resolveBundleFile applies the override rule to one bundle input: when the site ships a
// same-named file under its own src/ directory, that copy is bundled instead. Inputs
// outside src/ are returned unchanged.
func resolveBundleFile(file string) string {
	rel := strings.TrimPrefix(file, "src/")
	if rel == file {
		return file
	}
	return srcReadPath(rel)
}

// concatFiles concatenates the file list into the target path, strips all comments
// and minifies the body (by extension) to minimize CDN payload.
func concatFiles(files []string, targetPath string) error {
	var sb strings.Builder

	// JS bundles only: terminate every input. A file ending in an expression would otherwise
	// call the next file's leading parenthesis — fzstd.min.js ends `})`, status.js opens
	// `(function`, which parses as one call and dies at run time.
	inputSeparator := ""
	if filepath.Ext(targetPath) == ".js" {
		inputSeparator = ";\n"
	}

	used := make([]string, 0, len(files))
	for _, file := range files {
		// Honour the override rule for bundled engine assets: a site may replace any of
		// them by shipping a same-named file in its own src/ directory. This is how a
		// deployment declares its typefaces (src/css/fonts.css).
		file = resolveBundleFile(file)
		data, err := os.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		if len(data) == 0 {
			continue
		}
		used = append(used, file)
		sb.Write(data)
		if data[len(data)-1] != '\n' {
			sb.WriteByte('\n')
		}
		sb.WriteString(inputSeparator)
	}

	content := sb.String()
	if strings.HasSuffix(targetPath, ".css") {
		content = stripCSSComments(content)
	} else if strings.HasSuffix(targetPath, ".js") {
		content = stripJSComments(content)
	}

	// Empty manifest -> the artifact must be truly 0 bytes.
	// routes.go / earlyhints.go / page_weight_test.go rely on the "empty bundle = 0 bytes"
	// contract; the comment stripper would otherwise append a newline to empty input,
	// so normalize any all-whitespace content to 0 bytes here.
	if strings.TrimSpace(content) == "" {
		content = ""
	}

	// Minify in-process (tdewolff/minify, the only compression pipeline). On error
	// keep the unminified content — the bundle stays correct, just larger, and the
	// failure is visible in the log.
	if content != "" {
		if minified, err := minifyBundle(content, filepath.Ext(targetPath)); err != nil {
			log.Printf("bundle: minify %s: %v (keeping unminified)", targetPath, err)
		} else {
			content = minified
		}
	}

	// Head every non-empty artifact with the licence banner. Added after stripping
	// and minification so neither the stripper nor the minifier can touch it, and
	// skipped for empty bundles so the "empty bundle = 0 bytes" contract above still
	// holds.
	// Own banner first, third-party notices from line two down. Both are attached after
	// stripping and minification so neither pass can drop them.
	if content != "" {
		content = assetLicenceBanner + bundleNotices(used) + content
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return err
	}

	if err := os.WriteFile(targetPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write %s: %w", targetPath, err)
	}
	return nil
}

// vendorDeps maps a vendored file name under src/js/vendor/ to the URL it is fetched
// from at build time. Nothing in that directory is committed.
//
// jsDelivr, not unpkg: it serves a minified file even for packages that ship none, and
// pre-minified input beats raw source through tdewolff, which does not mangle names.
// Entries where neither holds say so on the entry itself.
//
// htmx is pinned to the 4.x range: npm's "latest" dist-tag is still 2.0.11, so an
// unpinned URL silently downgrades htmx — see docs/GOTCHAS.md §vendor-htmx-range-pin.
var vendorDeps = map[string]string{
	"htmx.min.js":       "https://cdn.jsdelivr.net/npm/htmx.org@4/dist/htmx.min.js",
	"hx-head.min.js":    "https://cdn.jsdelivr.net/npm/htmx.org@4/dist/ext/hx-head.min.js",
	"hx-preload.min.js": "https://cdn.jsdelivr.net/npm/htmx.org@4/dist/ext/hx-preload.min.js",
	"fzstd.min.js":      "https://cdn.jsdelivr.net/npm/fzstd/umd/index.js",

	// marked@18 ships no .min.js, and the unversioned jsDelivr path serves a stale v15 —
	// see docs/GOTCHAS.md §vendor-marked-stale-path.
	"marked.umd.js":       "https://cdn.jsdelivr.net/npm/marked@18/lib/marked.umd.js",
	"purify.min.js":       "https://cdn.jsdelivr.net/npm/dompurify/dist/purify.min.js",
	"prism.min.js":        "https://cdn.jsdelivr.net/npm/prismjs/prism.min.js",
	"prism-go.min.js":     "https://cdn.jsdelivr.net/npm/prismjs/components/prism-go.min.js",
	"prism-bash.min.js":   "https://cdn.jsdelivr.net/npm/prismjs/components/prism-bash.min.js",
	"prism-json.min.js":   "https://cdn.jsdelivr.net/npm/prismjs/components/prism-json.min.js",
	"prism-rust.min.js":   "https://cdn.jsdelivr.net/npm/prismjs/components/prism-rust.min.js",
	"prism-python.min.js": "https://cdn.jsdelivr.net/npm/prismjs/components/prism-python.min.js",
	"prism-zig.min.js":    "https://cdn.jsdelivr.net/npm/prismjs/components/prism-zig.min.js",
	"prism-yaml.min.js":   "https://cdn.jsdelivr.net/npm/prismjs/components/prism-yaml.min.js",
	"prism-toml.min.js":   "https://cdn.jsdelivr.net/npm/prismjs/components/prism-toml.min.js",
	"prism-nginx.min.js":  "https://cdn.jsdelivr.net/npm/prismjs/components/prism-nginx.min.js",
}

// ensureVendorDependencies fills src/js/vendor/ from vendorDeps. A copy younger than
// 24 hours is left alone, so a build only reaches the network once a day.
func ensureVendorDependencies() error {
	vendorDir := filepath.Join(RootSrc, "js", "vendor")
	if err := os.MkdirAll(vendorDir, 0755); err != nil {
		return err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	var mu sync.Mutex
	var failed []string
	var wg sync.WaitGroup

	for name, url := range vendorDeps {
		wg.Add(1)
		go func(n, u string) {
			defer wg.Done()
			path := filepath.Join(vendorDir, n)
			if stat, err := os.Stat(path); err == nil && time.Since(stat.ModTime()) < 24*time.Hour {
				return
			}

			// A failed refresh keeps the cached copy, a file never fetched is fatal:
			// a core bundle without htmx breaks every page — see vendorDeps.
			fail := func(why string) {
				if !fileExists(path) {
					mu.Lock()
					failed = append(failed, n+": "+why)
					mu.Unlock()
				}
			}

			resp, err := client.Get(u)
			if err != nil {
				fail(err.Error())
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				fail(fmt.Sprintf("HTTP %d", resp.StatusCode))
				return
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				fail(err.Error())
				return
			}
			if len(data) == 0 {
				fail("empty body")
				return
			}
			if err := os.WriteFile(path, data, 0644); err != nil {
				mu.Lock()
				failed = append(failed, n+": "+err.Error())
				mu.Unlock()
			}
		}(name, url)
	}
	wg.Wait()

	if len(failed) > 0 {
		sort.Strings(failed)
		return fmt.Errorf("vendor scripts missing and not downloadable: %s", strings.Join(failed, "; "))
	}
	return nil
}

// concatAllBundles builds all bundles and strips every comment (CDN zero-comment policy).
// Bundles land in the deployment's assets/, which is the tree /assets/ serves.
func concatAllBundles() error {
	// Third-party scripts are fetched here, never committed — see vendorDeps.
	if err := ensureVendorDependencies(); err != nil {
		return err
	}

	// JS bundles (modular, loaded on demand)
	if err := concatFiles(coreJSFiles, filepath.Join(RootAssets, "js", "core.bundle.js")); err != nil {
		return err
	}
	if err := concatFiles(homeJSFiles, filepath.Join(RootAssets, "js", "home.bundle.js")); err != nil {
		return err
	}
	if err := concatFiles(browseJSFiles, filepath.Join(RootAssets, "js", "browse.bundle.js")); err != nil {
		return err
	}
	if err := concatFiles(articleJSFiles, filepath.Join(RootAssets, "js", "article.bundle.js")); err != nil {
		return err
	}
	if err := concatFiles(commentJSFiles, filepath.Join(RootAssets, "js", "comment.bundle.js")); err != nil {
		return err
	}
	if err := concatFiles(adminJSFiles, filepath.Join(RootAssets, "js", "admin.bundle.js")); err != nil {
		return err
	}

	if err := concatFiles(editorJSFiles, filepath.Join(RootAssets, "js", "editor.bundle.js")); err != nil {
		return err
	}
	// CSS bundles
	if err := concatFiles(coreCSSFiles, filepath.Join(RootAssets, "css", "core.bundle.css")); err != nil {
		return err
	}
	if err := concatFiles(homeCSSFiles, filepath.Join(RootAssets, "css", "home.bundle.css")); err != nil {
		return err
	}
	if err := concatFiles(browseCSSFiles, filepath.Join(RootAssets, "css", "browse.bundle.css")); err != nil {
		return err
	}
	if err := concatFiles(articleCSSFiles, filepath.Join(RootAssets, "css", "article.bundle.css")); err != nil {
		return err
	}
	if err := concatFiles(commentCSSFiles, filepath.Join(RootAssets, "css", "comment.bundle.css")); err != nil {
		return err
	}
	if err := concatFiles(adminCSSFiles, filepath.Join(RootAssets, "css", "admin.bundle.css")); err != nil {
		return err
	}

	if err := concatFiles(editorCSSFiles, filepath.Join(RootAssets, "css", "editor.bundle.css")); err != nil {
		return err
	}

	if err := generateThemesJSON(); err != nil {
		log.Printf("warn: generateThemesJSON: %v", err)
	}

	if err := generateStaticErrorPages(); err != nil {
		log.Printf("warn: generateStaticErrorPages: %v", err)
	}

	// Re-render the service worker too: it embeds the bundle URLs, which just changed.
	if err := materializeServiceWorker(); err != nil {
		log.Printf("warn: materializeServiceWorker: %v", err)
	}

	log.Printf("concat: all RTT60 bundles built and comments stripped successfully (CSS unified, JS modular)")
	return nil
}
