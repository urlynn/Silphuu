package main

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The queue's job is to send what the edge has not been told about, and only that. Every test
// here drives the flush directly rather than through the worker, so the assertion is about the
// decision and not about timing.

func writePageFile(t *testing.T, urlPath, body string) string {
	t.Helper()
	cp := cachePath(urlPath)
	if err := os.MkdirAll(filepath.Dir(cp), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(cp, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", cp, err)
	}
	return cdnURL(urlPath)
}

func sentURLs(t *testing.T, record string) []string {
	t.Helper()
	data, err := os.ReadFile(record)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Callers name root-relative paths; the queue holds absolute URLs, so no outlet has to know the
// site's origin.
func TestInvalidatePathsQueuesAbsoluteURLs(t *testing.T) {
	withIsolatedRoots(t)
	takeDirtyURLs() // drop anything a previous test left behind

	invalidatePaths([]string{"/about", "/post/1"})

	got := takeDirtyURLs()
	if len(got) != 2 {
		t.Fatalf("queued %v, want two URLs", got)
	}
	for _, u := range got {
		if !strings.HasPrefix(u, "http") {
			t.Errorf("queued %q, which is not an absolute URL", u)
		}
	}
}

// The whole point of comparing bytes: a URL that did not move costs nothing. Without this the
// eight invalidations one restart produces would each spend the edge's quota on the same 24
// URLs, and a full invalidation would spend it on five URLs that never change.
func TestFlushSendsOnlyTheURLsWhoseBytesMoved(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)

	about := writePageFile(t, "/about", "one")
	friends := writePageFile(t, "/friends", "two")

	flushInvalidations([]string{about, friends})
	if got := sentURLs(t, record); len(got) != 2 {
		t.Fatalf("first flush sent %v, want both URLs", got)
	}

	// Nothing moved.
	flushInvalidations([]string{about, friends})
	if got := sentURLs(t, record); len(got) != 2 {
		t.Errorf("a flush with unchanged bytes sent %v", got[2:])
	}

	// One page moved, and only that URL goes out.
	if err := os.WriteFile(cachePath("/about"), []byte("one, edited"), 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	flushInvalidations([]string{about, friends})
	got := sentURLs(t, record)
	if len(got) != 3 || got[2] != about {
		t.Errorf("after one page changed the outlet received %v, want just %q", got[2:], about)
	}
}

// A flush that fails has to leave the ledger alone. Recording it anyway would be a page that
// never reaches the edge and nothing to notice it by.
func TestAFailedFlushIsRetried(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	about := writePageFile(t, "/about", "one")

	stubOutlet(t, &recordingOutlet{ready: true, err: errEdgeRefused})
	flushInvalidations([]string{about})

	stubOutletRecord(t, record)
	flushInvalidations([]string{about})

	if got := sentURLs(t, record); len(got) != 1 || got[0] != about {
		t.Errorf("the URL a failed flush dropped was not retried: %v", got)
	}
}

// A URL with no file behind it cannot be compared, so it is always sent. Leaving it out would
// pin the edge to a copy nothing can prove is current.
func TestAURLWithNoFileIsAlwaysSent(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)

	api := cdnURL("/api/comment/list?post_id=0&sort=newest")
	flushInvalidations([]string{api})
	flushInvalidations([]string{api})

	if got := sentURLs(t, record); len(got) != 2 {
		t.Errorf("a URL with no file behind it was sent %d time(s), want 2", len(got))
	}
}

// The served tree reaches the site's own media through links. Hashing the link rather than its
// target would hash the same inode on every run, which reads as "changed" forever.
func TestServedFileForURLFollowsLinks(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	target := filepath.Join(site, "public", "favicon.svg")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("<svg/>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(RootRender, "favicon.svg")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, ok := servedFileForURL(cdnURL("/favicon.svg"))
	if !ok {
		t.Fatalf("%s did not resolve to a file", cdnURL("/favicon.svg"))
	}
	// Compare resolved to resolved: on macOS the temp directory is itself reached through a
	// link (/var -> /private/var), so the target's own spelling is not the answer.
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", target, err)
	}
	if got != want {
		t.Errorf("resolved to %q, want the link target %q", got, want)
	}
	if got == link {
		t.Errorf("resolved to the link itself (%q), so its bytes would never change", link)
	}
}

// The worker stops while pages are being rewritten. Fingerprinting then would read the version
// being replaced, call it unchanged, and skip the flush for good.
func TestTheWorkerWaitsForRendersToFinish(t *testing.T) {
	withIsolatedRoots(t)
	startInvalidationWorker()
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)
	about := writePageFile(t, "/about", "one")

	inflightRenders.Add(1)
	invalidateURLs([]string{about})
	time.Sleep(200 * time.Millisecond)
	if got := sentURLs(t, record); len(got) != 0 {
		t.Fatalf("the worker flushed while a render was in flight: %v", got)
	}

	inflightRenders.Add(-1)
	wakeInvalidationWorker()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sentURLs(t, record)) == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the flush never happened after the render finished: %v", sentURLs(t, record))
}

// The favicons are two files with two types, and each has its own bytes — which is what keeps
// them out of every full invalidation. Serving one from the other leaves /favicon.ico with
// nothing to compare, so it goes to the edge on every flush; and a single always-sent URL is
// enough to undo the merging for all the rest.
func TestFaviconIcoResolvesToItsOwnFile(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	ico := filepath.Join(site, "public", "favicon.ico")
	if err := os.MkdirAll(filepath.Dir(ico), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ico, []byte("\x00\x00\x01\x00\x01\x00"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// public/ reaches the served root as a member link, and that link is what puts the file at
	// the URL — the same reconciliation the engine runs on every start.
	if err := os.Symlink(ico, filepath.Join(RootRender, "favicon.ico")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, ok := servedFileForURL(cdnURL(RouteFaviconIco))
	if !ok {
		t.Fatalf("%s did not resolve to a file", RouteFaviconIco)
	}
	want, err := filepath.EvalSymlinks(ico)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", ico, err)
	}
	if got != want {
		t.Errorf("resolved to %q, want %q", got, want)
	}
}

// A flush with no outlet has to leave the ledger alone.
//
// Recording it would be a purge that never happened, and the ledger is what stops the next flush
// from re-sending — so the edge would keep its copy until someone noticed by eye. This is also
// what makes "unset SILPHUU_PURGE_CMD" a usable way to stop purging while working: everything
// that changes while it is off goes out the moment it is configured again.
func TestASkippedFlushIsNotRecorded(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	about := writePageFile(t, "/about", "one")

	// No outlet: the flush runs, decides the URL changed, and sends nothing.
	stubOutlet(t, &recordingOutlet{})
	flushInvalidations([]string{about})

	// The outlet appears. The URL still counts as changed, because nothing told the edge.
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)
	flushInvalidations([]string{about})

	if got := sentURLs(t, record); len(got) != 1 || got[0] != about {
		t.Errorf("a URL skipped while the outlet was unset was not sent once it was set: %v", got)
	}
}

// The operator's brake follows the same contract as an absent outlet, with a different cause: the
// flush decides the URL changed, sends nothing, and leaves the ledger alone — so everything that
// changed while it was paused goes out the moment it is resumed.
//
// The recorded kind is part of the assertion: a paused node reported as having no outlet
// contradicts the switch the administrator just used.
func TestAPausedFlushIsNotRecordedAndResumes(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)
	about := writePageFile(t, "/about", "one")

	if err := setPurgePaused(true); err != nil {
		t.Fatalf("setPurgePaused: %v", err)
	}
	flushInvalidations([]string{about})
	if got := sentURLs(t, record); len(got) != 0 {
		t.Fatalf("a paused flush sent %v", got)
	}
	if kind := lastInvalidationState().Kind; kind != "invalidate.paused" {
		t.Errorf("a paused flush recorded %q, want the pause", kind)
	}

	if err := setPurgePaused(false); err != nil {
		t.Fatalf("setPurgePaused: %v", err)
	}
	flushInvalidations([]string{about})
	if got := sentURLs(t, record); len(got) != 1 || got[0] != about {
		t.Errorf("the URL skipped while paused was not sent after resuming: %v", got)
	}
}

// Every render path funnels through warmPage, so one balanced pair there is what keeps the
// worker from waiting forever.
func TestInflightRendersReturnsToZero(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()

	oldMux := globalMux
	defer func() { globalMux = oldMux }()
	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	registerPageRoutes(mux)
	globalMux = mux

	before := inflightRenders.Load()
	warmPage("/about", cachePath("/about"), nil)
	if got := inflightRenders.Load(); got != before {
		t.Errorf("inflightRenders = %d after a render, want %d", got, before)
	}
}

// The worker waits for the whole served tree to stop moving, not only for a render. A change
// that writes several files — a bundle rebuild, a font publish, a sticker refresh — would
// otherwise be fingerprinted halfway through, and the ledger would record a generation that
// never existed.
func TestTheWorkerWaitsForServedWrites(t *testing.T) {
	withIsolatedRoots(t)
	startInvalidationWorker()
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "sent.txt")
	stubOutletRecord(t, record)
	about := writePageFile(t, "/about", "one")

	beginServedWrite()
	// Anti-vacuity: the assertion below has to be about a lease that is really held.
	if servedWriteLease.Load() == 0 {
		t.Fatal("the lease is not held, so the wait it is meant to prove is untested")
	}
	invalidateURLs([]string{about})
	time.Sleep(200 * time.Millisecond)
	if got := sentURLs(t, record); len(got) != 0 {
		t.Fatalf("the worker flushed while the served tree was being written: %v", got)
	}

	endServedWrite()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(sentURLs(t, record)) == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("the flush never happened after the tree went quiet: %v", sentURLs(t, record))
}

// wireTestMux registers the page and API routes and publishes them as globalMux, which is what
// warming and rendering read.
func wireTestMux(t *testing.T) {
	t.Helper()
	oldMux := globalMux
	t.Cleanup(func() { globalMux = oldMux })
	mux := http.NewServeMux()
	registerAPIRoutes(mux)
	registerPageRoutes(mux)
	globalMux = mux
}

// renderThroughMux renders a path the way a visitor's request renders it, and returns the bytes
// without touching the served tree.
func renderThroughMux(t *testing.T, path string) []byte {
	t.Helper()
	if globalMux == nil {
		t.Fatal("globalMux is not wired up")
	}
	lw := &cacheWriter{ResponseWriter: nopWriter{}, buf: &bytes.Buffer{}, cp: ""}
	globalMux.ServeHTTP(lw, warmSyntheticRequest(path))
	if lw.buf.Len() == 0 {
		t.Fatalf("rendering %s produced no bytes", path)
	}
	return lw.buf.Bytes()
}

// Served bytes have to be a function of content and configuration. A live counter rendered into
// the page makes every render produce different bytes, so the fingerprint moves for a change
// that is not a change: the URL is purged again and the edge refetches a page whose content is
// identical.
func TestServedPageBytesIgnoreLiveCounters(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	wireTestMux(t)

	// main() builds these maps at startup; the test moves the counters on purpose.
	vst.mu.Lock()
	if vst.postViews == nil {
		vst.postViews = map[int]int64{}
	}
	if vst.daily == nil {
		vst.daily = map[string]*viewAgg{}
	}
	vst.mu.Unlock()

	// Anti-vacuity: the counters have to be moving, or two renders would agree for a reason
	// that has nothing to do with whether the pages read them.
	beforePost, beforePV := postViewCount(1), computeStats().TotalPV
	recordView("/post/1", "hash-a")
	recordView("/", "hash-b")
	if postViewCount(1) == beforePost || computeStats().TotalPV == beforePV {
		t.Fatalf("the counters did not move (%d->%d, %d->%d), so the renders below would agree "+
			"whether or not the page reads them",
			beforePost, postViewCount(1), beforePV, computeStats().TotalPV)
	}

	first := renderThroughMux(t, "/post/1")
	// Anti-vacuity: the bytes have to be a post page, or a 404 would compare equal to a 404.
	if !bytes.Contains(first, []byte("article-views__num")) {
		t.Fatalf("the rendered bytes are not a post page:\n%s", firstLines(string(first), 5))
	}
	if bytes.Contains(first, []byte("data-views=")) {
		t.Error("the served post page still carries the live view count")
	}
	recordView("/post/1", "hash-c")
	if second := renderThroughMux(t, "/post/1"); !bytes.Equal(first, second) {
		t.Error("two renders of one post produced different bytes: a visitor's arrival changed the page")
	}

	firstHome := renderThroughMux(t, "/")
	if !bytes.Contains(firstHome, []byte("stat-card--visits")) {
		t.Fatalf("the rendered bytes are not the home page:\n%s", firstLines(string(firstHome), 5))
	}
	recordView("/", "hash-d")
	if secondHome := renderThroughMux(t, "/"); !bytes.Equal(firstHome, secondHome) {
		t.Error("two renders of the home page produced different bytes: a visitor's arrival changed the page")
	}
}

// The lease is taken by the caller, not by the goroutine it protects: a warm scheduled without
// it leaves the worker free in the window between the enqueue that asked for the warm and the
// warm itself, which is the window the purge of a whole site would fall into.
func TestWarmPagesAsyncHoldsTheLeaseSynchronously(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	wireTestMux(t)

	before := servedWriteLease.Load()
	warmPagesAsync()
	if got := servedWriteLease.Load(); got <= before {
		t.Fatalf("the lease is %d right after warmPagesAsync returned, want more than %d: it was "+
			"taken inside the goroutine, which leaves open the window it exists to close", got, before)
	}

	// The loop has to hand the lease back. A writer that took it and never released it would
	// stop the worker for the life of the process.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if servedWriteLease.Load() == before {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("the lease is %d, want %d: the warm never released it", servedWriteLease.Load(), before)
}

// nginx answers a client that accepts brotli with the .br and everyone else with the source, so
// the digest the ledger compares has to move when either one does. A digest over the source
// alone cannot see an artifact that is empty, truncated or one generation behind — which is the
// state the edge keeps, under a cache policy that has it revalidate once a year.
func TestServedDigestCoversEveryVariant(t *testing.T) {
	requireBrotliForTest(t)
	withIsolatedRoots(t)
	cp := cachePath("/about")
	url := cdnURL("/about")
	write := func(body string) {
		t.Helper()
		if err := writeServedFile(cp, []byte(body), 0o644); err != nil {
			t.Fatalf("writeServedFile: %v", err)
		}
	}

	write("<!DOCTYPE html><html><body>one</body></html>")
	first, ok := servedDigestForURL(url)
	if !ok {
		t.Fatalf("%s has no digest", url)
	}
	// Anti-vacuity: without an artifact the second half of the digest is never exercised.
	if _, err := os.Stat(cp + brotliSuffix); err != nil {
		t.Fatalf("no artifact was written, so the digest's artifact half is untested: %v", err)
	}

	write("<!DOCTYPE html><html><body>two</body></html>")
	second, _ := servedDigestForURL(url)
	if second == first {
		t.Error("the source moved and the digest did not")
	}

	// Only the artifact moves. That is the state the deployment served: a .br that no longer
	// describes its source, with the source itself untouched.
	artifact, err := os.ReadFile(cp + brotliSuffix)
	if err != nil {
		t.Fatal(err)
	}
	artifact[0] ^= 0xff
	if err := os.WriteFile(cp+brotliSuffix, artifact, 0o644); err != nil {
		t.Fatal(err)
	}
	third, _ := servedDigestForURL(url)
	if third == second {
		t.Error("the artifact moved and the digest did not, so the edge would keep the broken one")
	}

	// A type the tree never pre-compresses has no artifact, and its digest still tracks the
	// source. Without this control a digest that ignored the source would pass both assertions.
	plain := filepath.Join(RootRender, "shot.avif")
	if err := os.WriteFile(plain, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	plainURL := cdnURL("/shot.avif")
	before, ok := servedDigestForURL(plainURL)
	if !ok {
		t.Fatalf("%s has no digest", plainURL)
	}
	if err := os.WriteFile(plain, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, _ := servedDigestForURL(plainURL)
	if before == after {
		t.Error("a URL with no artifact does not track its source")
	}
	if _, err := os.Stat(plain + brotliSuffix); !os.IsNotExist(err) {
		t.Error("an image was pre-compressed, so the digest would compare something nginx never sends")
	}
}

// The ledger's entries are digests, and a digest computed a different way is not comparable.
// Read as if it were, every URL would look unchanged and the edge would keep the copy the
// upgrade was meant to replace.
func TestALedgerFromAnotherVersionIsDiscarded(t *testing.T) {
	withIsolatedRoots(t)
	if err := os.MkdirAll(RootState, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "abc\thttps://example.test/\n"
	if err := os.WriteFile(purgeLedgerPath(), []byte("#purge-ledger-1\n"+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPurgeLedger(); len(got) != 0 {
		t.Errorf("a ledger from another version was read as current: %v", got)
	}

	// Anti-vacuity: the same file under the current version has to be read, or "nothing was
	// read" would satisfy the assertion above for the wrong reason.
	if err := os.WriteFile(purgeLedgerPath(), []byte(purgeLedgerHeader()+"\n"+entry), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadPurgeLedger(); got["https://example.test/"] != "abc" {
		t.Errorf("a ledger under the current version was not read: %v", got)
	}
}
