package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// containsAll fails when want has an entry missing from got.
func containsAll(t *testing.T, got, want []string) {
	t.Helper()
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

// A post's affected set is derived from its own fields, so it must cover the category page it
// appears on — which the hand-written list this replaced did not (/topic/* was deleted but
// never purged).
//
// Tags are deliberately not in the set: the tag filter travels in a fragment, so /posts#<tag>
// is the same URL as /posts and is purged once through aggregateURLs.
func TestPostDependentURLsCoversItsListings(t *testing.T) {
	p := Post{ID: 12, Category: "随笔", Tags: []string{"Go", "Linux"}}
	got := postDependentURLs(p)

	containsAll(t, got, []string{
		"/post/12",
		"/topic/随笔",
	})
	containsAll(t, got, aggregateURLs())

	// And nothing it does not touch.
	for _, unwanted := range []string{"/topic/教程", "/tag/Go", "/tag/Linux"} {
		if slices.Contains(got, unwanted) {
			t.Errorf("postDependentURLs() purged %q, which this post does not appear on: %v", unwanted, got)
		}
	}
}

func TestPostDependentURLsHandlesEmptyFields(t *testing.T) {
	got := postDependentURLs(Post{ID: 7, Tags: []string{"  ", ""}})

	if !slices.Contains(got, "/post/7") {
		t.Fatalf("post page missing: %v", got)
	}
	for _, u := range got {
		if strings.HasPrefix(u, "/topic/") || strings.HasPrefix(u, "/tag/") {
			t.Errorf("empty category/tag produced %q: %v", u, got)
		}
	}
}

// Every category page has to be purged, not only the ones a given change touched: the
// list-change path cannot know which those are.
func TestListPageURLsIncludesEveryCategory(t *testing.T) {
	withIsolatedRoots(t)
	// A post per directory: with no registry in an isolated root, a directory only counts
	// as a category once it holds one.
	for i, cat := range []string{"随笔", "教程"} {
		dir := filepath.Join(RootPosts, cat)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", cat, err)
		}
		body := fmt.Sprintf("---\nid: %d\n---\n\nbody\n", i+1)
		if err := os.WriteFile(filepath.Join(dir, "one.md"), []byte(body), 0o644); err != nil {
			t.Fatalf("write post in %s: %v", cat, err)
		}
	}

	got := listPageURLs()
	containsAll(t, got, []string{"/topic/随笔", "/topic/教程"})
	containsAll(t, got, aggregateURLs())
}

// The category list is rendered into the navbar, which is on every page, so a category
// appearing or disappearing has to be detectable — that is what escalates a list
// invalidation into a full one.
//
// The registry decides what a category is, so the change that matters is a directory
// holding a post, not the directory alone: an empty undeclared directory adds no nav entry
// and must not escalate.
func TestCategorySetChangedDetectsDirectoryAppearance(t *testing.T) {
	withIsolatedRoots(t)
	initCategorySet()
	t.Cleanup(initCategorySet)

	if categorySetChanged() {
		t.Fatal("nothing changed, but the category set was reported as changed")
	}

	stray := filepath.Join(RootPosts, "新分类")
	if err := os.MkdirAll(stray, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if categorySetChanged() {
		t.Fatal("an empty directory was treated as a category; it would add a nav entry with nothing behind it")
	}

	post := filepath.Join(stray, "one.md")
	if err := os.WriteFile(post, []byte("---\nid: 1\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write post: %v", err)
	}
	if !categorySetChanged() {
		t.Fatal("a new category holding a post was not detected")
	}
	// Recorded, so the next call is a no-op.
	if categorySetChanged() {
		t.Fatal("the new set was not recorded; every later invalidation would escalate")
	}

	if err := os.RemoveAll(stray); err != nil {
		t.Fatalf("rmdir: %v", err)
	}
	if !categorySetChanged() {
		t.Fatal("a removed category directory was not detected")
	}
}

// The purge set has to be read out of the cache before it is dropped, and has to match what
// was dropped.
func TestRemoveCachedPagesReturnsTheURLsItDropped(t *testing.T) {
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
	write("topic/随笔/index.html")
	write("topic/教程/index.html")
	write("post/12/index.html") // must survive: not in the dropped set
	write("home/desktop/index-1.html")

	got := removeCachedPages("archive", "topic")
	sort.Strings(got)

	want := []string{"/archive", "/topic/随笔", "/topic/教程"}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("removeCachedPages() = %v, want %v", got, want)
	}

	for _, rel := range []string{"archive", "topic"} {
		if _, err := os.Stat(filepath.Join(RootRender, rel)); !os.IsNotExist(err) {
			t.Errorf("public/%s was not removed", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(RootRender, "post", "12", "index.html")); err != nil {
		t.Errorf("a page outside the dropped set was removed: %v", err)
	}
}

// aggregateURLs is the one list still maintained by hand; pin it so a change is deliberate.
// /posts is in it because it renders the whole post set — the tag filter is a fragment, so
// every /posts#<tag> is this one URL.
func TestAggregateURLs(t *testing.T) {
	want := []string{"/", "/archive", "/posts", "/feed.xml", "/sitemap.xml"}
	got := aggregateURLs()
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("aggregateURLs() = %v, want %v", got, want)
	}
	// /about is deliberately absent: handleAuthor never reads the post list, it only shares
	// the category-set edge (the navbar).
	if slices.Contains(got, "/about") {
		t.Error("/about does not depend on the post set and must not be in aggregateURLs()")
	}
}
