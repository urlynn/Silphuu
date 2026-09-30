package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setupLinkRoots gives a private root and a distinct served root, which is the pair the
// links exist to connect.
func setupLinkRoots(t *testing.T) (home, served string) {
	t.Helper()
	home = t.TempDir()
	served = t.TempDir()
	InitRoots(home, served)
	t.Cleanup(func() { InitRoots("") })
	return home, served
}

func TestEnsureLinksCreatesTheDeclaredLinks(t *testing.T) {
	home, served := setupLinkRoots(t)
	writeTestFile(t, filepath.Join(home, "public", "favicon.svg"), "<svg/>")

	res, err := ensureLinks()
	if err != nil {
		t.Fatalf("ensureLinks: %v", err)
	}
	if res.created != 2 {
		t.Fatalf("created = %d, want 2 (static/ and favicon.svg)", res.created)
	}

	staticDst := filepath.Join(served, "static")
	target, err := os.Readlink(staticDst)
	if err != nil || target != filepath.Join(home, "static") {
		t.Fatalf("static link = %q, %v", target, err)
	}
	faviconDst := filepath.Join(served, "favicon.svg")
	target, err = os.Readlink(faviconDst)
	if err != nil || target != filepath.Join(home, "public", "favicon.svg") {
		t.Fatalf("favicon link = %q, %v", target, err)
	}

	// Reading through the served name is what the link is for.
	body, err := os.ReadFile(faviconDst)
	if err != nil || string(body) != "<svg/>" {
		t.Fatalf("served favicon = %q, %v", body, err)
	}
}

func TestEnsureLinksIsIdempotent(t *testing.T) {
	home, _ := setupLinkRoots(t)
	writeTestFile(t, filepath.Join(home, "public", "favicon.svg"), "<svg/>")

	if _, err := ensureLinks(); err != nil {
		t.Fatalf("first run: %v", err)
	}
	res, err := ensureLinks()
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if res.created+res.repaired+res.removed != 0 {
		t.Fatalf("second run did work: %s", res)
	}
}

func TestEnsureLinksPrunesADeletedSource(t *testing.T) {
	home, served := setupLinkRoots(t)
	icon := filepath.Join(home, "public", "favicon.svg")
	writeTestFile(t, icon, "<svg/>")
	if _, err := ensureLinks(); err != nil {
		t.Fatalf("ensureLinks: %v", err)
	}

	if err := os.Remove(icon); err != nil {
		t.Fatal(err)
	}
	res, err := ensureLinks()
	if err != nil {
		t.Fatalf("ensureLinks: %v", err)
	}
	if res.removed != 1 {
		t.Fatalf("removed = %d, want 1", res.removed)
	}
	if _, err := os.Lstat(filepath.Join(served, "favicon.svg")); !os.IsNotExist(err) {
		t.Fatalf("the link outlived its source: %v", err)
	}
}

func TestEnsureLinksRefusesARealFile(t *testing.T) {
	home, served := setupLinkRoots(t)
	writeTestFile(t, filepath.Join(home, "public", "favicon.svg"), "<svg/>")
	writeTestFile(t, filepath.Join(served, "favicon.svg"), "engine output")

	if _, err := ensureLinks(); err == nil {
		t.Fatal("a real file at the destination was accepted")
	}
}

func TestEnsureLinksRepairsAStaleLink(t *testing.T) {
	home, served := setupLinkRoots(t)
	writeTestFile(t, filepath.Join(home, "public", "favicon.svg"), "<svg/>")
	elsewhere := filepath.Join(t.TempDir(), "other.svg")
	writeTestFile(t, elsewhere, "other")
	if err := os.Symlink(elsewhere, filepath.Join(served, "favicon.svg")); err != nil {
		t.Fatal(err)
	}

	res, err := ensureLinks()
	if err != nil {
		t.Fatalf("ensureLinks: %v", err)
	}
	if res.repaired != 1 {
		t.Fatalf("repaired = %d, want 1", res.repaired)
	}
	target, err := os.Readlink(filepath.Join(served, "favicon.svg"))
	if err != nil || target != filepath.Join(home, "public", "favicon.svg") {
		t.Fatalf("target = %q, %v", target, err)
	}
}

// A dotfile in public/ is an OS artefact, not site content, and must not reach the URL root.
func TestEnsureLinksSkipsDotfiles(t *testing.T) {
	home, served := setupLinkRoots(t)
	writeTestFile(t, filepath.Join(home, "public", ".DS_Store"), "junk")
	writeTestFile(t, filepath.Join(home, "public", "robots.txt"), "User-agent: *")

	if _, err := ensureLinks(); err != nil {
		t.Fatalf("ensureLinks: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(served, ".DS_Store")); !os.IsNotExist(err) {
		t.Fatal(".DS_Store was linked into the served root")
	}
	if _, err := os.Lstat(filepath.Join(served, "robots.txt")); err != nil {
		t.Fatalf("robots.txt was not linked: %v", err)
	}
}
