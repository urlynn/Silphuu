package main

import (
	"os"
	"path/filepath"
	"testing"
)

// The blanket invalidation set may only carry URLs whose bytes are on THIS node.
//
// A URL without bytes here cannot be compared, and the fingerprint sends what it cannot compare
// on EVERY flush. One such URL is affordable; a family of them is a quota bomb.
//
// The shape of the bug this pins: the hero record is written on whichever node took the upload,
// and the peer reads the same record while holding no such file. Appending it to the set
// unconditionally puts /static/image/avatar.avif on the edge from the node that does not hold the
// media tree, on every flush. Scale that to the sticker family — 1073 files whose URLs are FIXED
// because a post or a comment names them, so a change has to be purged — and one flush would cost
// the day's whole quota, every one of them a wrong-origin fetch.
func TestStaticImageURLsCarryOnlyFilesThisNodeHolds(t *testing.T) {
	site, _ := withIsolatedRoots(t)

	// A record naming a file this node does not have must not enter the set.
	if err := os.MkdirAll(filepath.Join(site, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := saveHeroConfig(HeroConfig{Avatar: []string{"/static/image/avatar.avif"}}); err != nil {
		t.Fatalf("saveHeroConfig: %v", err)
	}
	if got := staticImageURLs(); len(got) != 0 {
		t.Errorf("staticImageURLs returned %v for a node holding no image tree: a URL with no "+
			"bytes here has nothing to compare, so every flush would send it", got)
	}
	for _, p := range staticImageURLs() {
		if _, ok := servedNameForURL(cdnURL(p)); !ok {
			t.Errorf("staticImageURLs returned %q, which does not resolve to a file on this node", p)
		}
	}

	// The positive half: a file this node does hold is in the set, so an avatar uploaded here
	// still reaches the edge.
	img := filepath.Join(RootStatic, "image", "avatar.avif")
	if err := os.MkdirAll(filepath.Dir(img), 0o755); err != nil {
		t.Fatalf("mkdir image: %v", err)
	}
	if err := os.WriteFile(img, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := staticImageURLs()
	if len(got) != 1 || got[0] != "/static/image/avatar.avif" {
		t.Fatalf("staticImageURLs = %v, want exactly the one file this node holds", got)
	}
	if _, ok := servedNameForURL(cdnURL(got[0])); !ok {
		t.Errorf("%q is in the set but does not resolve to a file: the fingerprint could not "+
			"compare it, so it would be sent on every flush", got[0])
	}
}
