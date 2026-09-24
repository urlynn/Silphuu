package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// putSrcAsset writes a file under <engine>/src/<rel>: src/ holds authored sources, and the
// render fingerprint covers it.
func putSrcAsset(t *testing.T, engine, rel, content string) {
	t.Helper()
	p := filepath.Join(engine, "src", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// putLayerSrc writes a file under <site>/src/<rel>: a deployment's own sources, which are
// laid over the engine's when a bundle or a theme scan reads them.
func putLayerSrc(t *testing.T, site, rel, content string) {
	t.Helper()
	p := filepath.Join(site, "src", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// The fingerprint decides whether a restart drops the render cache. It has to notice a
// hand-authored source change, and it must NOT notice anything the engine itself wrote into
// the served tree — that happens on every start, and reacting to it would clear the cache
// every boot.
func TestRenderInputFingerprintCoversAuthoredAssetsOnly(t *testing.T) {
	site, engine := withIsolatedRoots(t)

	putSrcAsset(t, engine, "css/style.css", "v1")
	prev := renderInputFingerprint()

	// assets/ is output: rebuilding it is not an input change.
	putAsset(t, site, "css/core.bundle.css", "generated")
	if got := renderInputFingerprint(); got != prev {
		t.Fatalf("a generated asset changed the fingerprint; the render cache would be dropped on every start")
	}

	// Hand-authored engine sources are included, one by one.
	for _, rel := range []string{"css/style.css", "icon/favicon.svg", "image/background/desktop-01.avif"} {
		putSrcAsset(t, engine, rel, "authored")
		got := renderInputFingerprint()
		if got == prev {
			t.Fatalf("editing src/%s did not change the fingerprint", rel)
		}
		prev = got
	}

	// A deployment's own sources count too — they are laid over the engine's.
	putLayerSrc(t, site, "css/fonts.css", "@font-face{}")
	if got := renderInputFingerprint(); got == prev {
		t.Fatalf("adding a deployment source did not change the fingerprint")
	}
}

// Identical bytes must not count as a change even when the mtime moves. This is what makes
// covering the source tree possible at all: the previous mtime+size scheme could not include
// it, because the bundle pipeline rewrites its output on every start.
func TestRenderInputFingerprintIgnoresMtimeOnlyChange(t *testing.T) {
	_, engine := withIsolatedRoots(t)
	putSrcAsset(t, engine, "css/style.css", "body{}")
	before := renderInputFingerprint()

	p := filepath.Join(engine, "src", "css", "style.css")
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if after := renderInputFingerprint(); after != before {
		t.Fatalf("an mtime-only change altered the fingerprint")
	}
}

// Stable across calls, and sensitive to real content changes.
func TestRenderInputFingerprintStableAndContentSensitive(t *testing.T) {
	_, engine := withIsolatedRoots(t)
	putSrcAsset(t, engine, "css/a.css", "a")
	putSrcAsset(t, engine, "css/b.css", "b")

	first := renderInputFingerprint()
	if second := renderInputFingerprint(); second != first {
		t.Fatalf("fingerprint is not stable across calls: %q vs %q", first, second)
	}
	putSrcAsset(t, engine, "css/a.css", "a2")
	if third := renderInputFingerprint(); third == first {
		t.Fatalf("a content change did not alter the fingerprint")
	}
}

// The CDN purge list is derived from public/, so every cached page has to map back to the URL
// a browser would have requested. A page dropped locally but not purged stays stale at the
// edge for a year.
func TestCachedPageURLsMapsCachePathsToPublicURLs(t *testing.T) {
	withIsolatedRoots(t)

	write := func(rel string) {
		t.Helper()
		p := filepath.Join(RootRender, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("index.html")
	write("archive/index.html")
	write("guestbook/index.html")
	write("post/12/index.html")
	write("topic/随笔/index.html")
	write("home/desktop/index-1.html")
	write("home/mobile/index-2.html")
	write("stray.html") // cachePath never writes this; it must not become a URL

	got := cachedPageURLs()
	sort.Strings(got)
	want := []string{"/", "/archive", "/guestbook", "/post/12", "/topic/随笔"}
	sort.Strings(want)

	if len(got) != len(want) {
		t.Fatalf("cachedPageURLs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cachedPageURLs() = %v, want %v", got, want)
		}
	}
}

// A full invalidation must purge exactly what it deleted: the derived cache contents plus
// the handler-served pages that never reach public/. Nothing more — every extra URL is a
// needless origin fetch on a box with no traffic headroom.
func TestFullInvalidationPathsCoversCachedPagesAndHandlerPages(t *testing.T) {
	withIsolatedRoots(t)

	write := func(rel string) {
		t.Helper()
		p := filepath.Join(RootRender, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write("archive/index.html")
	write("post/12/index.html")
	write("topic/随笔/index.html")
	write("home/desktop/index-1.html")

	got := fullInvalidationPaths()
	sort.Strings(got)

	want := dedupePaths(append(
		[]string{"/", "/archive", "/post/12", "/topic/随笔"},
		purgeAlwaysPaths...,
	)...)
	sort.Strings(want)

	if !slices.Equal(got, want) {
		t.Fatalf("fullInvalidationPaths() =\n  %v\nwant\n  %v", got, want)
	}
}

// Category names are Chinese, and the CDN matches a purge against the encoded form the
// browser actually requested. A raw UTF-8 path would match nothing and fail silently.
func TestCDNURLEscapesNonASCIIPathsButKeepsQuery(t *testing.T) {
	cases := []struct{ in, wantSuffix string }{
		{"/archive", "/archive"},
		{"archive", "/archive"},
		{"/topic/随笔", "/topic/%E9%9A%8F%E7%AC%94"},
		{"/api/comment/list?post_id=1&sort=newest", "/api/comment/list?post_id=1&sort=newest"},
	}
	for _, tc := range cases {
		if got := cdnURL(tc.in); !strings.HasSuffix(got, tc.wantSuffix) {
			t.Errorf("cdnURL(%q) = %q, want suffix %q", tc.in, got, tc.wantSuffix)
		}
	}
}
