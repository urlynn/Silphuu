package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeTestFile creates parent directories as needed.
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// setupProjectionRoots builds an engine tree and a deployment tree, and points the
// package roots at them.
//
// The engine tree matters: a deployment ships only what it changed, so most of what a
// published site contains comes from the engine. A test that only populated the
// deployment would pass while the projection omitted every engine asset.
func setupProjectionRoots(t *testing.T) string {
	t.Helper()

	engine := t.TempDir()
	writeTestFile(t, filepath.Join(engine, "assets", "css", "core.bundle.css"), "body{}")
	writeTestFile(t, filepath.Join(engine, "assets", "js", "core.bundle.js"), "//js")
	writeTestFile(t, filepath.Join(engine, "assets", "icon", "about.svg"), "<svg/>")
	writeTestFile(t, filepath.Join(engine, "assets", "image", "error", "404.avif"), "ERRIMG")
	// The engine's sources. Nothing under src/ may reach the published tree, and
	// TestProjectStaticTreeShipsNoSources can only assert that if there is something
	// there to leak.
	writeTestFile(t, filepath.Join(engine, "src", "css", "core.css"), ".a{}")
	writeTestFile(t, filepath.Join(engine, "src", "js", "core.js"), "//src")
	writeTestFile(t, filepath.Join(engine, "src", "icon", "about.svg"), "<svg/>")
	writeTestFile(t, filepath.Join(engine, "src", "sw.js"), "//sw")

	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, "render", "index.html"), "<h1>home</h1>")
	writeTestFile(t, filepath.Join(home, "render", "archive", "index.html"), "<h1>archive</h1>")
	writeTestFile(t, filepath.Join(home, "render", "post", "1", "index.html"), "<h1>post</h1>")
	// The render cache's own file, which lives beside the pages it describes.
	writeTestFile(t, filepath.Join(home, "render", ".render-inputs"), "deadbeef")
	writeTestFile(t, filepath.Join(home, "render", "error", "404.html"), "<h1>404</h1>")

	// The site's own uploads, exactly like `make site` produces. Its stylesheet override is
	// not here: that is a src/ source (src/css/site.css), inlined by head.html, never
	// published — the projection lays down build output and site content only.
	writeTestFile(t, filepath.Join(home, "static", "image", "post", "shot.jpg"), "JPEG")
	// A robots.txt a deployment wrote by hand. The projection must generate one instead:
	// a placeholder host would point every crawler at the wrong site.
	writeTestFile(t, filepath.Join(home, "static", "robots.txt"), "Sitemap: https://example.com/sitemap.xml")
	// Root-level files the site wants served at /. Nothing the engine generates may
	// overwrite these, which is why public/ is the last source laid down.
	writeTestFile(t, filepath.Join(home, "public", "favicon.svg"), "<svg>site-icon</svg>")

	// Secrets. Everything under the published root is fetchable by URL.
	writeTestFile(t, filepath.Join(home, "config", "site.json"), `{"password":"hunter2"}`)
	writeTestFile(t, filepath.Join(home, "state", "events.jsonl"), `{"seq":1}`)
	writeTestFile(t, filepath.Join(home, "posts", "hello.md"), "# hello")

	prevEngine := engineRoot
	engineRoot = engine
	InitRoots(home)
	t.Cleanup(func() {
		engineRoot = prevEngine
		InitRoots("")
	})
	return home
}

func TestProjectStaticTreeBuildsTheServableShape(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	// Rendered pages are hoisted to the root: a server maps /archive/ to
	// <root>/archive/index.html, so render/ must not survive as a level.
	for _, rel := range []string{
		"index.html",
		filepath.Join("archive", "index.html"),
		filepath.Join("post", "1", "index.html"),
		filepath.Join("assets", "css", "core.bundle.css"),
		filepath.Join("assets", "js", "core.bundle.js"),
		filepath.Join("static", "image", "post", "shot.jpg"),
		filepath.Join("error", "404.html"),
		"favicon.svg",
		"robots.txt",
		"feed.xml",
		"sitemap.xml",
	} {
		if _, err := os.Stat(filepath.Join(dst, rel)); err != nil {
			t.Errorf("published tree is missing %s: %v", rel, err)
		}
	}

	for _, rel := range []string{"render", "public"} {
		if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
			t.Errorf("%s/ survived as a directory; its contents should be hoisted to the root", rel)
		}
	}
}

// TestProjectStaticTreeCarriesEngineFiles: a deployment directory holds only what the
// site changed. If the projection copied just that, the published site would ship no
// stylesheets, scripts or icons at all.
func TestProjectStaticTreeCarriesEngineFiles(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dst, "assets", "icon", "about.svg"))
	if err != nil {
		t.Fatalf("engine icon missing from the published tree: %v", err)
	}
	if string(body) != "<svg/>" {
		t.Errorf("engine icon content wrong: %q", body)
	}
}

// TestProjectStaticTreeShipsNoSources: the published root is fetchable by URL, so
// anything under it is public — and src/ is the one tree a deployment never serves. The
// served form of a source is the bundle, and of an icon the binary, so nothing under
// src/ has any business here.
func TestProjectStaticTreeShipsNoSources(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dst, "src")); !os.IsNotExist(err) {
		t.Errorf("src/ was published; it must never reach a tree served by URL")
	}

	// A bare .css/.js in the published assets/ means a source was copied there instead
	// of concatenated into its bundle.
	var leaked []string
	assets := filepath.Join(dst, "assets")
	err := filepath.WalkDir(assets, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		switch filepath.Ext(path) {
		case ".css":
			if !strings.HasSuffix(base, ".bundle.css") {
				leaked = append(leaked, path)
			}
		case ".js":
			if !strings.HasSuffix(base, ".bundle.js") {
				leaked = append(leaked, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", assets, err)
	}
	if len(leaked) > 0 {
		t.Errorf("source files reached the published assets/ (a bundle is a source's only served form):\n  %s",
			strings.Join(leaked, "\n  "))
	}

	// Both checks above are satisfied by an empty tree, which is exactly how this guard
	// would fail to guard. Require real input on each side.
	if _, err := os.Stat(filepath.Join(engineRoot, "src", "css", "core.css")); err != nil {
		t.Fatalf("fixture carries no engine src/ tree, so nothing could have leaked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(assets, "css", "core.bundle.css")); err != nil {
		t.Fatalf("published assets/ holds no bundle, so the walk above proved nothing: %v", err)
	}
}

// TestProjectStaticTreeServesTheSitesRootFiles: public/ holds what the site wants served
// at its own root, and is laid down last so nothing the engine generates can overwrite it.
func TestProjectStaticTreeServesTheSitesRootFiles(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dst, "favicon.svg"))
	if err != nil {
		t.Fatalf("favicon.svg missing from the published tree: %v", err)
	}
	if string(body) != "<svg>site-icon</svg>" {
		t.Errorf("favicon.svg is not the site's own file: %q", body)
	}
}

// TestEngineShipsNoStaticContent: static/ is the deployment's own area — uploads and
// stickers. An asset stored there is published at /static/..., while the running server
// serves it from /assets/..., so the two paths drift apart and neither one is the truth.
// That is how a personal favicon, a placeholder robots.txt and a duplicate themes.json
// ended up in the release.
func TestEngineShipsNoStaticContent(t *testing.T) {
	for _, rel := range []string{"favicon.svg", "robots.txt", "themes.json", "sw.js"} {
		if _, err := os.Stat(filepath.Join("static", rel)); err == nil {
			t.Errorf("server/static/%s exists: engine files belong under assets/", rel)
		}
	}
}

// srcSourceExtensions are the extensions an engine source file may carry. Content —
// images, fonts, media — belongs under assets/, where the site layer supplies it and
// where it is served.
var srcSourceExtensions = map[string]bool{
	".css":  true,
	".js":   true,
	".mjs":  true,
	".svg":  true,
	".json": true,
}

// TestEngineSrcHoldsOnlySources: src/ is neither served nor published. A content file
// there reads as a source and gets edited as one, but content belongs under assets/
// where it is served.
func TestEngineSrcHoldsOnlySources(t *testing.T) {
	const root = "src"
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("guard root %s is missing: %v", root, err)
	}

	var bad []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // tolerate unreadable entries
		}
		if d.IsDir() {
			return nil
		}
		base := d.Name()
		// Hidden files are OS artefacts (.DS_Store), not sources. The projection skips
		// the same names, for the same reason.
		if strings.HasPrefix(base, ".") {
			return nil
		}
		scanned++
		if !srcSourceExtensions[strings.ToLower(filepath.Ext(base))] {
			bad = append(bad, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatalf("%s holds no files at all — the guard scanned nothing and would have passed", root)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("found %d non-source files under %s/:\n  %s\n\n"+
			"src/ holds sources and nothing else; content belongs under assets/, supplied "+
			"by the site layer. If a new source kind is intended, add its extension to "+
			"srcSourceExtensions in this file.",
			len(bad), root, strings.Join(bad, "\n  "))
	}
}

// TestProjectStaticTreeNeverPublishesState: the published root is reachable by URL, so
// anything under it is public. config/ holds the admin password, state/ the event log.
func TestProjectStaticTreeNeverPublishesState(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	for _, rel := range []string{
		"state",
		"config",
		filepath.Join("config", "site.json"),
		filepath.Join("state", "events.jsonl"),
		filepath.Join("posts", "hello.md"),
		filepath.Join("render", ".render-inputs"),
		".render-inputs",
	} {
		if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
			t.Errorf("%s was published; it is reachable by URL and must never be", rel)
		}
	}

	// Guard against the checks above passing because nothing was written at all.
	if _, err := os.Stat(filepath.Join(dst, "index.html")); err != nil {
		t.Fatalf("projection wrote nothing: %v", err)
	}
}

// TestProjectStaticTreeGeneratesRobots: the engine's static copy names a placeholder
// host. Publishing it would point every site's crawlers at example.com.
func TestProjectStaticTreeGeneratesRobots(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dst, "robots.txt"))
	if err != nil {
		t.Fatalf("robots.txt missing: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "example.com") {
		t.Errorf("the engine's placeholder robots.txt was published:\n%s", got)
	}
	if got != robotsTxt(siteBaseURL()) {
		t.Errorf("robots.txt does not match the body the dynamic handler serves:\ngot  %q\nwant %q", got, robotsTxt(siteBaseURL()))
	}
}

// TestProjectStaticTreeGeneratesFeedAndSitemap: a published tree has no handler behind it,
// so these have to exist as files. They must also carry the same bytes the handler would
// serve, or the tree and a running server would disagree about the site's own URLs.
func TestProjectStaticTreeGeneratesFeedAndSitemap(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	base := siteBaseURL()
	for _, tc := range []struct {
		name string
		want string
	}{
		{"feed.xml", feedXML(base)},
		{"sitemap.xml", sitemapXML(base)},
	} {
		body, err := os.ReadFile(filepath.Join(dst, tc.name))
		if err != nil {
			t.Errorf("%s missing from the published tree: %v", tc.name, err)
			continue
		}
		if string(body) != tc.want {
			t.Errorf("%s does not match the body the dynamic handler serves:\ngot  %q\nwant %q",
				tc.name, body, tc.want)
		}
	}
}

// swapPostIndex replaces the in-memory post index for the duration of a test.
func swapPostIndex(t *testing.T, posts []Post) {
	t.Helper()
	postIdx.mu.Lock()
	savedList, savedByID := postIdx.list, postIdx.byID
	postIdx.list, postIdx.byID = posts, nil
	postIdx.mu.Unlock()
	t.Cleanup(func() {
		postIdx.mu.Lock()
		postIdx.list, postIdx.byID = savedList, savedByID
		postIdx.mu.Unlock()
	})
}

// TestFeedLastBuildDateComesFromThePostSet: a projection rebuilds the whole tree, so a
// clock-derived timestamp would change the bytes on every rebuild even when nothing was
// published — and the feed's URL is stable, so that would mean a purge for nothing. The
// date has to come from the newest post, and be absent when there is no dated post.
func TestFeedLastBuildDateComesFromThePostSet(t *testing.T) {
	date := time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)
	swapPostIndex(t, []Post{{ID: 1, Title: "dated", Date: date}})

	want := "<lastBuildDate>" + date.Format(time.RFC1123Z) + "</lastBuildDate>"
	if got := feedXML("https://example.com"); !strings.Contains(got, want) {
		t.Errorf("lastBuildDate is not the newest post's date, want %s:\n%s", want, got)
	}

	swapPostIndex(t, []Post{{ID: 2, Title: "undated"}})
	if got := feedXML("https://example.com"); strings.Contains(got, "<lastBuildDate>") {
		t.Errorf("lastBuildDate was written with no dated post; it must be omitted:\n%s", got)
	}
}

// TestProjectStaticTreeResolvesSymlinks: the published tree is served by a web server,
// so it has to be self-contained.
func TestProjectStaticTreeResolvesSymlinks(t *testing.T) {
	setupProjectionRoots(t)
	// A deployment may organise its images with symlinks; the copy has to resolve them.
	link := filepath.Join(engineRoot, "assets", "image", "error", "500.avif")
	if err := os.Symlink("404.avif", link); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	got := filepath.Join(dst, "assets", "image", "error", "500.avif")
	info, err := os.Lstat(got)
	if err != nil {
		t.Fatalf("stat %s: %v", got, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("a symlink was copied as a symlink; the published tree must be self-contained")
	}
	body, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read %s: %v", got, err)
	}
	if string(body) != "ERRIMG" {
		t.Errorf("symlink target content not copied: got %q", body)
	}
}

// TestProjectStaticTreeRebuildsFromScratch: the tree is derived, so a file left behind
// by an earlier projection must not survive one whose source no longer has it.
func TestProjectStaticTreeRebuildsFromScratch(t *testing.T) {
	setupProjectionRoots(t)
	dst := filepath.Join(t.TempDir(), "pub")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	writeTestFile(t, filepath.Join(dst, "post", "999", "index.html"), "<h1>deleted post</h1>")
	writeTestFile(t, filepath.Join(dst, "stale.css"), "x{}")

	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("second projection: %v", err)
	}

	for _, rel := range []string{filepath.Join("post", "999", "index.html"), "stale.css"} {
		if _, err := os.Stat(filepath.Join(dst, rel)); !os.IsNotExist(err) {
			t.Errorf("%s survived a rebuild; the published tree must be a pure projection", rel)
		}
	}
}

// TestProjectStaticTreeNeverPublishesStaging: staging/ holds visitor submissions that no
// moderator has looked at yet. Everything under the published root is fetchable by URL,
// so a staged file that reached it would be public while still unreviewed.
func TestProjectStaticTreeNeverPublishesStaging(t *testing.T) {
	home := setupProjectionRoots(t)
	staged := filepath.Join(home, "staging", "friend", "1700000000_probe.svg")
	writeTestFile(t, staged, `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`)

	dst := filepath.Join(t.TempDir(), "pub")
	if err := projectStaticTree(dst); err != nil {
		t.Fatalf("projectStaticTree: %v", err)
	}

	var leaked []string
	err := filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == "staging" {
			leaked = append(leaked, path)
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dst, err)
	}
	if len(leaked) > 0 {
		t.Errorf("staging/ reached the published tree, so an unreviewed submission has a URL:\n  %s",
			strings.Join(leaked, "\n  "))
	}

	// An empty fixture would make the walk above prove nothing, and so would a
	// projection that wrote no tree at all.
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("fixture carries no staged submission: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "index.html")); err != nil {
		t.Fatalf("projection wrote nothing: %v", err)
	}
}

// TestProjectStaticTreeRefusesForeignDirectory: projection deletes the destination
// before rebuilding it, so it must only ever do that to a tree it generated itself.
func TestProjectStaticTreeRefusesForeignDirectory(t *testing.T) {
	setupProjectionRoots(t)
	dir := t.TempDir()
	precious := filepath.Join(dir, "notes.txt")
	writeTestFile(t, precious, "do not delete")

	if err := projectStaticTree(dir); err == nil {
		t.Fatal("projection into a directory it did not create should fail")
	}
	if _, err := os.Stat(precious); err != nil {
		t.Errorf("the foreign directory was modified: %v", err)
	}
}
