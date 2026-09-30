package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The invalidation queue.
//
// Callers do not talk to the edge. They say "these URLs may have changed" and go on; a single
// worker decides what the edge actually has to be told, and it decides it by comparing the bytes
// on disk against what the edge was last told about. Two things fall out of that:
//
//   - A change that reaches this file eight times sends once. The calls arrive spread over the
//     seconds a re-render takes, so no window can merge them; what merges them is that the
//     second call finds the same bytes the first one already sent.
//   - A URL whose bytes did not move is not sent at all. A full invalidation covers 24 URLs and
//     a font-subset publish changes 19 of them; the other five — the favicon, the sitemap, the
//     feed, robots — would otherwise be purged on every restart.
//
// Both matter because the edge charges by URL against a daily quota: 1000 of them here, and an
// unconditional full invalidation spends 24. Twenty-four URLs per restart is eight restarts a
// day before the quota is gone and the edge keeps serving the previous pages.
var (
	dirtyMu   sync.Mutex
	dirtyURLs = map[string]bool{}

	// invalidateWake is a one-slot signal rather than a queue: the worker drains the whole set
	// each time it runs, so a missed signal is not a missed URL.
	invalidateWake = make(chan struct{}, 1)
)

// inflightRenders counts the renders in progress.
//
// The worker waits for it to reach zero before it fingerprints anything. A page still being
// rendered is a page whose bytes are about to change: fingerprinting it now reads the version
// being replaced, concludes "unchanged", and never looks again — the flush would be skipped for
// good, not delayed.
var inflightRenders atomic.Int64

// servedWriteLease counts the writers that are changing the served tree right now.
//
// The worker takes no fingerprint while it is non-zero. A tree that is being rewritten is not a
// tree: a fingerprint taken across a change reads a mix of the pages being replaced and the ones
// replacing them, and the ledger then records a generation that never existed. Holding the lease
// for a whole pass or a whole rebuild is what makes "the bytes on disk are the bytes to compare"
// true rather than likely.
//
// It is a counter rather than a flag because writers nest: a warm pass holds it while each page
// write inside takes it again.
var servedWriteLease atomic.Int64

// beginServedWrite takes the lease. Callers take it synchronously, before the work it describes
// is scheduled — a lease taken inside the goroutine it protects leaves open the window it exists
// to close.
func beginServedWrite() { servedWriteLease.Add(1) }

// endServedWrite releases the lease and wakes the worker, which is what makes "the tree is quiet
// now" reachable without polling.
func endServedWrite() {
	servedWriteLease.Add(-1)
	wakeInvalidationWorker()
}

// servedTreeBusy reports whether a writer is changing the served tree. A render that has started
// but not written yet counts as one: its page's bytes are about to move.
func servedTreeBusy() bool {
	return inflightRenders.Load() > 0 || servedWriteLease.Load() > 0
}

// invalidateURLs records that these URLs may have changed and wakes the worker.
//
// It is the entry every caller uses, including the ones that already hold full URLs.
func invalidateURLs(urls []string) {
	added := false
	dirtyMu.Lock()
	for _, u := range urls {
		if u == "" || dirtyURLs[u] {
			continue
		}
		dirtyURLs[u] = true
		added = true
	}
	dirtyMu.Unlock()
	if added {
		wakeInvalidationWorker()
	}
}

// invalidatePaths is the root-relative half: callers name the paths they changed, and the URL
// is built here so that no caller has to know the site's origin.
func invalidatePaths(paths []string) {
	urls := make([]string, 0, len(paths))
	for _, p := range paths {
		if u := cdnURL(p); u != "" {
			urls = append(urls, u)
		}
	}
	invalidateURLs(urls)
}

// wakeInvalidationWorker nudges the worker. Called both when the set grows and when a render
// finishes, because the worker stops while renders are in flight and needs telling when that
// stops being true.
func wakeInvalidationWorker() {
	select {
	case invalidateWake <- struct{}{}:
	default: // a wake-up is already pending, and one is enough
	}
}

var startInvalidationWorkerOnce sync.Once

// startInvalidationWorker runs the queue. It is started once, before the first render; a second
// call does nothing, so a test that wants the worker running can simply ask for it.
func startInvalidationWorker() {
	startInvalidationWorkerOnce.Do(func() {
		go func() {
			for range invalidateWake {
				for {
					if servedTreeBusy() {
						// Writers are changing the tree. Stop here: the last one to finish
						// wakes us again, and until then the bytes on disk are not the bytes
						// to compare.
						break
					}
					batch := takeDirtyURLs()
					if len(batch) == 0 {
						break
					}
					flushInvalidations(batch)
				}
			}
		}()
	})
}

// takeDirtyURLs empties the set and returns it, sorted so the log and the tests read the same
// way twice.
func takeDirtyURLs() []string {
	dirtyMu.Lock()
	defer dirtyMu.Unlock()
	if len(dirtyURLs) == 0 {
		return nil
	}
	out := make([]string, 0, len(dirtyURLs))
	for u := range dirtyURLs {
		out = append(out, u)
	}
	sort.Strings(out)
	dirtyURLs = map[string]bool{}
	return out
}

// flushInvalidations sends the URLs whose bytes moved since the last flush.
//
// The ledger is written only after the outlet accepted the URLs. A flush that fails therefore
// leaves the ledger alone and the next one tries the same URLs again — a failure costs a repeat,
// not a page that never reaches the edge.
func flushInvalidations(urls []string) {
	ledger := loadPurgeLedger()

	type candidate struct {
		url     string
		sum     string
		hasFile bool
	}
	candidates := make([]candidate, 0, len(urls))
	changed := make([]string, 0, len(urls))
	for _, u := range urls {
		c := candidate{url: u}
		if sum, ok := servedDigestForURL(u); ok {
			c.sum, c.hasFile = sum, true
		}
		candidates = append(candidates, c)
		if !c.hasFile || ledger[u] != c.sum {
			changed = append(changed, u)
			log.Printf("invalidate: %s changed: hasFile=%v now=%s was=%s",
				u, c.hasFile, shortSum(c.sum), shortSum(ledger[u]))
		}
	}

	if len(changed) == 0 {
		log.Printf("invalidate: %d URL(s) unchanged since the last flush, nothing sent", len(urls))
		recordInvalidation("invalidate.skipped", 0, nil)
		return
	}
	sent, err := runPurgeOutlet(changed)
	if err != nil {
		recordInvalidation("invalidate.failed", len(changed), err)
		// The one failure an operator has to hear about: the edge keeps serving the previous
		// pages for as long as its cache policy says, and nothing else would say so.
		notifyAdmin(buildInvalidateFailedMessage(err, changed))
		return
	}
	if !sent {
		// Nothing was told to the edge, so the ledger stays as it is and the next flush tries
		// again — see runPurgeOutlet. The two reasons read differently to an operator, and a
		// paused node reported as "no outlet" would contradict its own switch.
		if purgePaused() {
			recordInvalidation("invalidate.paused", len(changed), nil)
			return
		}
		recordInvalidation("invalidate.no_outlet", len(changed), nil)
		return
	}
	recordInvalidation("invalidate.ok", len(changed), nil)
	for _, c := range candidates {
		if c.hasFile && contains(changed, c.url) {
			ledger[c.url] = c.sum
		}
	}
	prunePurgeLedger(ledger)
	savePurgeLedger(ledger)
}

// buildInvalidateFailedMessage is the alert that the edge did not accept the URLs. The chat form
// carries the count and not the list: the list is in Fields for a webhook, and a wall of URLs is
// what an operator cannot read on a phone.
//
// The line names the node whose outlet failed: the edge is per-node, and an operator reading two
// nodes' alerts has to be able to tell which one kept serving stale pages.
func buildInvalidateFailedMessage(err error, changed []string) Message {
	where := nodeIdentity()
	return Message{
		Event:   EventInvalidateFailed,
		Subject: "边缘缓存刷新失败",
		Body:    fmt.Sprintf("%s 缓存刷新失败：%v\n\n以下 %d 条 URL 没有被边缘接受，下次 flush 会重试：", where, err, len(changed)),
		Chat:    fmt.Sprintf("%s 缓存刷新失败：%v\n\n%d 条 URL 未被边缘接受，下次 flush 重试", where, err, len(changed)),
		Fields: map[string]string{
			"count": fmt.Sprint(len(changed)),
			"urls":  strings.Join(changed, " "),
		},
		Alert: true,
	}
}

// The last flush, for the administrator's page. Kept in memory rather than in a file: it
// describes this process, and a restart is a new answer to "what happened last".
type invalidationState struct {
	Kind  string
	Count int
	At    time.Time
	Err   string
}

var (
	invalidationMu   sync.Mutex
	lastInvalidation invalidationState
)

func recordInvalidation(kind string, count int, err error) {
	invalidationMu.Lock()
	defer invalidationMu.Unlock()
	lastInvalidation = invalidationState{Kind: kind, Count: count, At: time.Now()}
	if err != nil {
		lastInvalidation.Err = err.Error()
	}
}

// lastInvalidationState is what the page shows. A zero At means nothing has flushed yet.
func lastInvalidationState() invalidationState {
	invalidationMu.Lock()
	defer invalidationMu.Unlock()
	return lastInvalidation
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// servedNameForURL maps a URL to the name the served tree holds for it, without following a
// link. ok is false when there is no such file, and such a URL is always sent: there is nothing
// to compare it against, and leaving it out would pin the edge to a copy nothing can prove is
// current.
//
// The unresolved name is what the digest needs: the pre-compressed companion sits beside the
// name the deployment serves, not beside whatever that name points at.
func servedNameForURL(u string) (string, bool) {
	path := strings.TrimPrefix(u, cdnBaseURL())
	if !strings.HasPrefix(path, "/") {
		return "", false
	}
	path, _, _ = strings.Cut(path, "?")

	// Pages: cachePath answers for every URL the render cache can hold.
	if cp := cachePath(path); cp != "" {
		if info, err := os.Stat(cp); err == nil && !info.IsDir() {
			return cp, true
		}
	}

	// Files the served tree holds under their own name: robots.txt, feed.xml, sitemap.xml,
	// sw.js, and the favicons, which arrive as links into the site's own tree — one per URL,
	// so each has its own bytes to compare.
	if rel := strings.TrimPrefix(path, "/"); rel != "" {
		candidate := filepath.Join(RootRender, filepath.FromSlash(rel))
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// servedFileForURL maps a URL to the file whose bytes decide whether the edge needs to hear
// about it, with links followed.
func servedFileForURL(u string) (string, bool) {
	name, ok := servedNameForURL(u)
	if !ok {
		return "", false
	}
	return resolveLink(name), true
}

// servedDigestForURL fingerprints everything the edge can receive for a URL.
//
// nginx answers a client that accepts brotli with the pre-compressed companion and everyone
// else with the source, so both are part of the answer. A digest over the source alone cannot
// see an artifact that is empty, truncated or one generation behind — and a .br is served on
// existence alone, so such an artifact is exactly what the edge keeps, under a policy that has
// it revalidate once a year.
//
// A URL whose type is never pre-compressed has no companion and digests to its source, which is
// the whole of what the edge can hold for it.
func servedDigestForURL(u string) (string, bool) {
	name, ok := servedNameForURL(u)
	if !ok {
		return "", false
	}
	source, err := fileSHA256(resolveLink(name))
	if err != nil {
		return "", false
	}
	artifact := ""
	if brotliWanted(name) {
		if sum, err := fileSHA256(name + brotliSuffix); err == nil {
			artifact = sum
		}
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s", source, artifact)
	return hex.EncodeToString(h.Sum(nil)), true
}

// resolveLink follows a symlink to what it points at. The served tree reaches the site's own
// media through links, and hashing the link itself would hash the same inode on every run —
// which reads as "changed" forever.
func resolveLink(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// urlForCachePath is the inverse of cachePath: the URL a rendered page answers at, or "" when
// the path is not a page file.
func urlForCachePath(cp string) string {
	rel, err := filepath.Rel(RootRender, cp)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "index.html" {
		return "/"
	}
	if !strings.HasSuffix(rel, "/index.html") {
		return ""
	}
	return "/" + strings.TrimSuffix(rel, "/index.html")
}

// shortSum trims a hash for a log line. Eight hex characters is enough to tell two apart.
func shortSum(sum string) string {
	if len(sum) > 8 {
		return sum[:8]
	}
	return sum
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// The ledger records what the edge was last told, per URL. It lives in state/ rather than in the
// served tree: everything under the served root is reachable by URL, and this is bookkeeping.
const purgeLedgerName = ".purged-content"

// purgeLedgerVersion moves when the meaning of the recorded value moves. Entries written under
// another version are not comparable — they would read as "unchanged" for URLs whose digest is
// computed a different way — so a ledger that does not carry the current version is discarded,
// and the first flush after an upgrade sends every URL once.
const purgeLedgerVersion = "2"

// purgeLedgerHeader is the ledger's first line, and the whole of the version check.
func purgeLedgerHeader() string { return "#purge-ledger-" + purgeLedgerVersion }

func purgeLedgerPath() string { return filepath.Join(RootState, purgeLedgerName) }

func loadPurgeLedger() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(purgeLedgerPath())
	if err != nil {
		return out // no ledger yet: everything counts as changed, which is the safe reading
	}
	lines := strings.Split(string(data), "\n")
	if strings.TrimSpace(lines[0]) != purgeLedgerHeader() {
		log.Printf("invalidate: %s was not written under %s; every URL is sent once",
			purgeLedgerPath(), purgeLedgerHeader())
		return out
	}
	for _, line := range lines[1:] {
		sum, u, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && sum != "" && u != "" {
			out[u] = sum
		}
	}
	return out
}

// prunePurgeLedger drops the entries for URLs that no longer have a file behind them. Post ids
// come and go, and without this the ledger keeps every one of them for the life of the tree.
func prunePurgeLedger(ledger map[string]string) {
	for u := range ledger {
		if _, ok := servedFileForURL(u); !ok {
			delete(ledger, u)
		}
	}
}

func savePurgeLedger(ledger map[string]string) {
	urls := make([]string, 0, len(ledger))
	for u := range ledger {
		urls = append(urls, u)
	}
	sort.Strings(urls)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", purgeLedgerHeader())
	for _, u := range urls {
		fmt.Fprintf(&b, "%s\t%s\n", ledger[u], u)
	}
	if err := os.MkdirAll(RootState, 0o755); err != nil {
		log.Printf("invalidate: cannot create %s: %v", RootState, err)
		return
	}
	// Written through a temp file and renamed, like every other file the engine keeps: a
	// reader — including the next process — sees the previous ledger or the new one.
	tmp := purgeLedgerPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		log.Printf("invalidate: cannot write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, purgeLedgerPath()); err != nil {
		log.Printf("invalidate: cannot replace %s: %v", purgeLedgerPath(), err)
		_ = os.Remove(tmp)
	}
}
