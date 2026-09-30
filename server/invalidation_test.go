package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
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

// A post page is dropped only when its post is gone. A page whose post is still there is
// overwritten by the warm that follows, and that write is a rename — so a reader sees the
// previous page or the new one, never a missing file. Deleting the page first is what
// published a 404 for the width of a render.
//
// Dropping the page has to queue its URL in the same breath. The edge is still serving that
// page, and once the file is gone nothing on disk names it, so a fingerprint can never see
// that the edge is wrong: the purge is the only thing that can correct it.
func TestPrunePostPagesDropsOnlyThePagesWithNoPost(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	withTheWorkerParked(t)

	live := loadPostsFromDB()
	if len(live) == 0 {
		t.Fatal("the example site has no posts, so this fixture would pass vacuously")
	}
	liveID := strconv.Itoa(live[0].ID)

	writePage := func(rel string) {
		t.Helper()
		p := filepath.Join(RootRender, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	writePage("post/" + liveID + "/index.html")
	writePage("post/9999/index.html") // left behind by a post that no longer exists

	takeDirtyURLs()
	if n := prunePostPages(); n != 1 {
		t.Fatalf("prunePostPages() removed %d page(s), want 1", n)
	}
	if _, err := os.Stat(filepath.Join(RootRender, "post", liveID, "index.html")); err != nil {
		t.Errorf("the page of a post that still exists was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(RootRender, "post", "9999")); !os.IsNotExist(err) {
		t.Errorf("the page of a post that is gone survived")
	}

	if got := takeDirtyURLs(); !slices.Equal(got, []string{cdnURL("/post/9999")}) {
		t.Errorf("the pruned page queued %v, want just its own URL — the edge keeps a post the "+
			"site no longer has otherwise", got)
	}
}

// A purge only reaches the edge as fresh bytes if the origin re-renders the page. The edge's
// refetch reads this tree, so a listing left stale here is cached again and the purge buys
// nothing — the deleted post keeps appearing on its category page for as long as the policy
// says. The invalidation that names a URL therefore has to rewrite it.
func TestDeletingAPostRewritesTheListingPagesItWasOn(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	// A delete that empties a category escalates to a full invalidation, which would rewrite the
	// page below for a reason that has nothing to do with the invalidation under test.
	initCategorySet()
	t.Cleanup(initCategorySet)
	wireTestMux(t)
	t.Setenv("DEV_MODE", "")
	// The font pipeline republishes the whole site on a new character, which is the same
	// confounder one level down.
	writeTestFile(t, FileFontMap, "{}")

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("the example site has no posts, so this fixture would pass vacuously")
	}
	victim := posts[0]
	catURL := topicURLs(victim.Category)[0]

	// The listing has to exist and name the post, or "it changed" would hold for a page that was
	// never rendered.
	warmPage(catURL, cachePath(catURL), nil)
	before, err := os.ReadFile(cachePath(catURL))
	if err != nil {
		t.Fatalf("the category page was not rendered: %v", err)
	}
	if !strings.Contains(string(before), victim.Title) {
		t.Fatalf("the category page does not name %q, so a rewrite is untestable", victim.Title)
	}

	form := url.Values{}
	form.Set("post_id", strconv.Itoa(victim.ID))
	req := httptest.NewRequest("POST", "/admin/delete-post", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAdminPostDelete(httptest.NewRecorder(), req)

	// Anti-vacuity: the category has to survive the delete. If it did not, the category set
	// would have changed and the invalidation would have escalated to a full one, which
	// rewrites the page below for a reason that has nothing to do with this case.
	if !slices.Contains(getCategories(), victim.Category) {
		t.Fatalf("%s is gone, so the delete changed the category set and the invalidation escalated",
			victim.Category)
	}

	// The warm runs behind the response.
	deadline := time.Now().Add(10 * time.Second)
	var after []byte
	for time.Now().Before(deadline) {
		after, err = os.ReadFile(cachePath(catURL))
		if err == nil && !strings.Contains(string(after), victim.Title) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the category page still names the deleted post, so the edge's refetch reads it and "+
		"caches it again — the purge bought nothing:\n%s", firstLines(string(after), 5))
}

// One post delete is one logical change, and the edge charges per call against a daily quota.
// The delete invalidates in two rounds — the post's own URL first, then the listings it appeared
// on — and the queue is a set, so both rounds have to reach the edge as one batch, once.
func TestOnePostDeleteProducesOneOutletCall(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	initCategorySet()
	t.Cleanup(initCategorySet)
	// The font pipeline republishes the whole site, which is a second full invalidation.
	writeTestFile(t, FileFontMap, "{}")
	// The worker takes no batch while the tree is held busy, so what the delete queued is this
	// test's to read rather than something the worker may have already drained.
	withTheWorkerParked(t)
	t.Setenv("DEV_MODE", "")

	recorded := stubOutletRecord(t, filepath.Join(t.TempDir(), "outlet.txt"))

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("the example site has no posts, so this fixture would pass vacuously")
	}
	victim := posts[0]

	form := url.Values{}
	form.Set("post_id", strconv.Itoa(victim.ID))
	req := httptest.NewRequest("POST", "/admin/delete-post", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	handleAdminPostDelete(httptest.NewRecorder(), req)

	queued := takeDirtyURLs()
	own := cdnURL(URLPost(victim.ID))
	if !slices.Contains(queued, own) {
		t.Fatalf("the delete queued no invalidation for %s, so nothing below is exercised: %v", own, queued)
	}
	// Anti-vacuity for the merge: the delete has to have queued the listings as well, or "one
	// call" would hold because there was only ever one round.
	if len(queued) < 2 {
		t.Fatalf("the delete queued %v — its own round only, so the merge below is untested", queued)
	}

	flushInvalidations(queued)
	if got := recorded.calls; got != 1 {
		t.Fatalf("the delete produced %d outlet call(s), want 1", got)
	}

	// Control: the record can see a second call, so "one" above is not a counter that never moves.
	edited := writePageFile(t, "/about", "one")
	flushInvalidations([]string{edited})
	if got := recorded.calls; got != 2 {
		t.Errorf("the control produced %d outlet call(s), want 2: the assertions above would hold "+
			"even if the outlet were never called", got)
	}
}

// An invalidation must not take a page down. A page whose URL still has a route is overwritten
// by the warm that follows, and that write is a rename — so a reader sees the previous page or
// the new one. Wiping the tree instead made a visitor see 404 for every page the warm had not
// reached yet, and a second invalidation landing mid-warm left those pages with nothing to
// render them back.
func TestInvalidateAllCacheKeepsThePagesThatStillHaveARoute(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	writeAt := func(abs string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
		return abs
	}
	// The paths come from the engine's own URL -> file mapping rather than being spelled out,
	// so the test cannot drift from the routes it is supposed to describe.
	kept := []string{
		writeAt(cachePath("/")),       // the home page
		writeAt(cachePath(PageAbout)), // a fixed page
	}
	cats := getCategories()
	if len(cats) == 0 {
		t.Fatal("the example site has no categories, so this fixture would pass vacuously")
	}
	kept = append(kept, writeAt(cachePath(topicURLs(cats[0])[0]))) // a category page

	gone := writeAt(cachePath("/post/9999")) // no such post

	// globalMux is nil in a fixture, so the warm is skipped. That is the point: this asserts
	// the removal rule on its own, which is what decides whether a page exists at all.
	invalidateAllCache()

	for _, p := range kept {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("a page whose URL still has a route was removed: %v", err)
		}
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("the page of a post that is gone survived")
	}
	if _, err := os.Stat(filepath.Join(RootRender, "post", "9999")); !os.IsNotExist(err) {
		t.Errorf("the directory of a removed page survived")
	}
}

// The administrator who saves a post is redirected to that post's page, so the page has to be
// rendered before the response is written. The invalidation's own warm runs behind the
// response, which would answer this one request with the version that was just replaced.
func TestWarmPostPageRendersBeforeItReturns(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	oldMux := globalMux
	defer func() { globalMux = oldMux }()
	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	registerPageRoutes(mux)
	globalMux = mux

	posts := loadPostsFromDB()
	if len(posts) == 0 {
		t.Fatal("the example site has no posts, so this fixture would pass vacuously")
	}
	cp := cachePath(URLPost(posts[0].ID))
	if err := os.RemoveAll(filepath.Dir(cp)); err != nil {
		t.Fatalf("clear the page: %v", err)
	}

	warmPostPage(posts[0].ID)

	if _, err := os.Stat(cp); err != nil {
		t.Errorf("warmPostPage returned without the page on disk: %v", err)
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

// withTheWorkerParked holds the served-tree lease so the invalidation worker stops taking URLs
// for the rest of the test, and clears whatever was already queued.
//
// The worker runs for the whole test binary once any test has started it, so a test that wants
// to see what an action queued has to take the queue out of the worker's reach. Holding the
// lease is what parks it: the worker takes no batch while the tree is busy.
func withTheWorkerParked(t *testing.T) {
	t.Helper()
	beginServedWrite()
	t.Cleanup(func() {
		// Leave nothing for the worker to flush against a tree the fixture is about to remove.
		takeDirtyURLs()
		endServedWrite()
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		takeDirtyURLs()
		time.Sleep(20 * time.Millisecond)
		if len(takeDirtyURLs()) == 0 {
			return
		}
	}
	t.Fatal("the worker kept taking URLs while the tree was held busy")
}

// A post that moves between categories leaves a page behind: the one it used to be listed on.
// The index has already been rebuilt by the time the invalidation runs, so the post no longer
// names the category it came from — the save handler is the only place that still knows it, and
// a save that names only the new category leaves the old page stale at the edge for a year.
func TestSavingAPostIntoAnotherCategoryPurgesTheCategoryItLeft(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	// The category set is what escalates a save into a full invalidation, so the baseline has to
	// match the tree this fixture starts from.
	initCategorySet()
	t.Cleanup(initCategorySet)
	// The font pipeline republishes the whole site, which is a legitimate full invalidation and
	// would swamp the set under test. An empty font map stops it at its first check, so the save
	// below is the only thing that invalidates anything.
	writeTestFile(t, FileFontMap, "{}")
	withTheWorkerParked(t)

	t.Setenv("DEV_MODE", "")

	// Two categories the registry already declares, so the move below leaves the category set
	// alone. An undeclared destination would escalate to a full invalidation, and the
	// assertions would hold for a reason that has nothing to do with the category left behind.
	const from, to = "essay", "software"
	dir := filepath.Join(RootPosts, from)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var oldPath, slug string
	var movedID int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		candidate := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if _, _, _, _, _, id, _ := parseFrontmatter(string(body)); id > 0 {
			oldPath, slug, movedID = candidate, strings.TrimSuffix(e.Name(), ".md"), id
			break
		}
	}
	if movedID == 0 {
		t.Fatalf("no post is filed under %s, so this fixture has nothing to move", from)
	}
	if postDirFor(to) == from {
		t.Fatalf("%s and %s resolve to one directory, so nothing moves", from, to)
	}

	form := url.Values{}
	form.Set("category", to)
	form.Set("slug", slug)
	form.Set("body", "moved between categories")
	form.Set("old_path", oldPath)
	req := httptest.NewRequest("POST", "/admin/save-post", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Is-Admin", "1")

	handleAdminSavePost(httptest.NewRecorder(), req)
	queued := takeDirtyURLs()

	// Anti-vacuity: the save has to have run and queued the post's own page.
	own := cdnURL(URLPost(movedID))
	if !slices.Contains(queued, own) {
		t.Fatalf("the save queued no invalidation for %s, so nothing below is exercised: %v", own, queued)
	}

	left, _ := categoryForDir(from)
	if want := cdnURL(topicURLs(left.Name)[0]); !slices.Contains(queued, want) {
		t.Errorf("the save queued %v, which omits %s — the page the post left keeps listing it "+
			"until the edge's copy expires", queued, want)
	}

	// And it has to stay narrow. Naming the category left behind is what makes the rest of the
	// categories unnecessary; purging them all on every save is what this replaced.
	other, _ := categoryForDir("hardware")
	if unwanted := cdnURL(topicURLs(other.Name)[0]); slices.Contains(queued, unwanted) {
		t.Errorf("the save purged %s, which is neither the old nor the new category: %v", unwanted, queued)
	}
}
