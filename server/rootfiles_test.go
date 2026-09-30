package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// feed.xml and sitemap.xml are computed from the post index, and an unbuilt index reads exactly
// like a site with no posts. Writing them before it exists publishes a feed with no items — and
// the edge then holds that copy for as long as its cache policy says.
//
// The order is what prevents it: generateServedRootFiles runs after backfillPosts, and the
// refusal pinned here makes a future reordering fail loudly instead of quietly.
func TestServedRootFilesRefuseToRunBeforeThePostIndexExists(t *testing.T) {
	useExampleSite(t)
	feed := filepath.Join(RootRender, "feed.xml")
	_ = os.Remove(feed)

	wasReady := postIndexIsReady()
	t.Cleanup(func() { postIndexReady.Store(wasReady) })

	postIndexReady.Store(false)
	generateServedRootFiles("test-before")
	if _, err := os.Stat(feed); err == nil {
		t.Fatal("feed.xml was written with no post index: it would carry no posts, and that is " +
			"the copy the edge would hold")
	}

	rebuildPostIndex()
	generateServedRootFiles("test-after")

	data, err := os.ReadFile(feed)
	if err != nil {
		t.Fatalf("feed.xml was not written once the index existed: %v", err)
	}
	if !strings.Contains(string(data), "<item>") {
		t.Errorf("feed.xml carries no <item> although the index was built:\n%s", firstLines(string(data), 12))
	}
	if !strings.Contains(string(data), "<lastBuildDate>") {
		t.Error("feed.xml carries no <lastBuildDate>: the post dates did not reach it")
	}

	// sitemap.xml lists the post URLs, so it has the same dependency and the same fix.
	sm, err := os.ReadFile(filepath.Join(RootRender, "sitemap.xml"))
	if err != nil {
		t.Fatalf("sitemap.xml was not written once the index existed: %v", err)
	}
	if !strings.Contains(string(sm), "/post/") {
		t.Errorf("sitemap.xml names no post URL:\n%s", firstLines(string(sm), 12))
	}
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
