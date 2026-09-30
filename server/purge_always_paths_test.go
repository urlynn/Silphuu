package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A named URL whose bytes this node does not hold must not enter the blanket invalidation set.
//
// It has nothing to compare, and the fingerprint sends what it cannot compare on EVERY flush.
// A named list is exactly the shape that grows, so the rule has to hold for it too: an entry
// added for a file this node does not have would be sent on every flush, forever. See
// staticImageURLs for the same rule at the size it can reach there.
func TestPurgeAlwaysPathsCarryOnlyFilesThisNodeHolds(t *testing.T) {
	site, _ := withIsolatedRoots(t)

	// A tree holding none of them: none may be named.
	if got := purgeAlwaysPaths(); len(got) != 0 {
		t.Errorf("purgeAlwaysPaths() = %v for a node holding none of them: a URL with no bytes "+
			"here cannot be compared, so every flush would send it", got)
	}

	// Write them all, and every one must be named — the list must still name the URLs it
	// exists for.
	named := []string{"index.html", "feed.xml", "sitemap.xml", "robots.txt", "favicon.svg", "favicon.ico"}
	for _, rel := range named {
		if err := os.WriteFile(filepath.Join(site, rel), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	got := purgeAlwaysPaths()
	if len(got) != len(named) {
		t.Fatalf("purgeAlwaysPaths() = %v, want all %d once their files are here", got, len(named))
	}

	// The invariant the whole blanket set depends on: every URL it carries resolves to bytes on
	// this node. Nothing downstream has to remember it — this is what makes "uncomparable ⇒ sent
	// on every flush" unreachable from here.
	for _, p := range fullInvalidationPaths() {
		if _, ok := servedNameForURL(cdnURL(p)); !ok {
			t.Errorf("fullInvalidationPaths() carries %q, whose bytes are not on this node: it has "+
				"nothing to compare, so every flush would send it", p)
		}
	}
}
