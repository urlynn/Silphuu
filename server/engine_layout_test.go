package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// The engine ships no site content of its own: a deployment supplies it. These two guards
// keep the line between the engine's own files and the site's, which is what lets a site
// override any of them without the engine's copy reappearing underneath.
func TestEngineShipsNoStaticContent(t *testing.T) {
	for _, rel := range []string{"favicon.svg", "robots.txt", "themes.json", "sw.js"} {
		if _, err := os.Stat(filepath.Join("static", rel)); err == nil {
			t.Errorf("server/static/%s exists: the engine ships no served content", rel)
		}
	}
}

// srcSourceExtensions are the extensions an engine source file may carry. Content —
// images, fonts, media — belongs to the site, under its own static/.
var srcSourceExtensions = map[string]bool{
	".css":  true,
	".js":   true,
	".mjs":  true,
	".svg":  true,
	".json": true,
}

// TestEngineSrcHoldsOnlySources: src/ is neither served nor published. A content file
// there reads as a source and gets edited as one, but content belongs under static/,
// where the site layer supplies it and where it is served.
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
		// Hidden files are OS artefacts (.DS_Store), not sources.
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
			"src/ holds sources and nothing else; content belongs under static/, supplied "+
			"by the site layer. If a new source kind is intended, add its extension to "+
			"srcSourceExtensions in this file.",
			len(bad), root, strings.Join(bad, "\n  "))
	}
}

// TestFeedLastBuildDateComesFromThePostSet: the feed answers at a stable URL, so a
// clock-derived timestamp would change its bytes on every rebuild even when nothing was
// published — and that would mean a purge for nothing. The date has to come from the
// newest post, and be absent when there is no dated post.
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

// TestTemplatesResolveSiteMediaThroughStatic: asset() names a file under the engine's built
// assets/, while image/ is the site's own media under static/. A template that asks asset()
// for image/... gets an unhashed /assets/ URL for a file that is not there, which renders as
// a missing image with nothing but a log line to say so — the sponsor QR codes and the
// comment default avatar went that way.
func TestTemplatesResolveSiteMediaThroughStatic(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("templates", "*.html"))
	if err != nil {
		t.Fatalf("glob templates: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no templates found to scan — the guard would have passed without looking")
	}
	bad := regexp.MustCompile(`asset "image/`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if bad.Match(data) {
			t.Errorf("%s asks asset() for image/...; the site's media answers at %s — use staticURL", f, StaticPrefix)
		}
	}
}
