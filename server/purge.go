package main

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

// The invalidation outlet.
//
// The engine's job ends at "these URLs changed". How the edge is told is the deployment's own
// business, so the telling is an outlet rather than engine code: the command outlet runs a
// script the deployment supplies, and a build may instead compile one in. Exactly one outlet is
// compiled per build — the two implementations carry complementary build constraints — so "no
// fallback" is a property of the build rather than a branch here.
type outlet interface {
	// Ready reports whether this node has what the outlet needs. An outlet that is not ready
	// counts as absent: the flush is skipped and the ledger is left alone.
	Ready() bool
	// Purge hands the URLs over. sent is false when the edge was not told.
	Purge(urls []string) (sent bool, err error)
}

// purgeOutlet is the outlet this build compiled in, or nil. purge_shell.go and purge_esa.go each
// install it from an init function. The lock is there for the tests that stand in for one: the
// invalidation worker is a goroutine that outlives them and reads this on every flush.
var (
	purgeOutletMu sync.RWMutex
	purgeOutlet   outlet
)

func setPurgeOutlet(o outlet) {
	purgeOutletMu.Lock()
	defer purgeOutletMu.Unlock()
	purgeOutlet = o
}

func getPurgeOutlet() outlet {
	purgeOutletMu.RLock()
	defer purgeOutletMu.RUnlock()
	return purgeOutlet
}

var (
	purgeNoOutletNotice sync.Once
	purgeNotReadyNotice sync.Once
	purgePausedNotice   sync.Once
)

// purgePausedMarkerName is the file whose presence pauses purging on this node.
//
// A marker rather than a value: the state is "paused or not", so the file's existence is the
// whole answer and its contents are ignored. A truncated or empty marker still reads as paused,
// which a JSON document could not promise.
//
// The name is deliberately not *.json. The file watcher treats a .json change under state/ as a
// data change and rebuilds the font subset, and pausing is not a data change.
const purgePausedMarkerName = ".purge-paused"

func purgePausedPath() string { return filepath.Join(RootState, purgePausedMarkerName) }

// purgePaused reads the marker from disk every time. Caching it would make "survives a restart"
// false by construction, and this is read once per flush, not per request.
func purgePaused() bool {
	_, err := os.Stat(purgePausedPath())
	return err == nil
}

// setPurgePaused applies the brake or releases it. Pausing writes the marker; resuming removes
// it, and a marker that is already gone is not an error.
func setPurgePaused(paused bool) error {
	if !paused {
		if err := os.Remove(purgePausedPath()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(RootState, 0o755); err != nil {
		return err
	}
	return os.WriteFile(purgePausedPath(), []byte("purging is paused on this node\n"), 0o644)
}

// runPurgeOutlet hands the URLs to this build's outlet.
//
// sent reports whether the edge was actually told. It is false when there is nothing to tell —
// no outlet in this build, one that is not ready, purging paused by an operator, or a
// development run — and the caller must then leave its ledger alone. Recording a purge that did
// not happen is worse than not purging at all: the ledger is what stops the next flush from
// re-sending, so a skip that records pins the edge to whatever copy it holds until someone
// notices by eye.
//
// A non-nil error means the outlet ran and the edge refused. The ledger is left alone then too,
// so the next flush tries the same URLs again.
func runPurgeOutlet(urls []string) (sent bool, err error) {
	clean := dedupeNonEmpty(urls)
	if len(clean) == 0 {
		return false, nil
	}

	o := getPurgeOutlet()
	if o == nil {
		purgeNoOutletNotice.Do(func() {
			log.Printf("purge: this build has no invalidation outlet, so this node tells no edge " +
				"about its changes (pages are still rendered and served from here)")
		})
		return false, nil
	}
	if !o.Ready() {
		purgeNotReadyNotice.Do(func() {
			log.Printf("purge: the invalidation outlet is not configured on this node, so it tells " +
				"no edge about its changes (pages are still rendered and served from here)")
		})
		return false, nil
	}

	// The operator's brake. It sits after the two above because those say this node has no edge
	// at all, and before DEV_MODE because that is a build-time accident rather than a decision.
	// It lives here rather than in the outlet implementations because it is a policy of the
	// invalidation path, not a property of one vendor's edge — and because the outlets report
	// their own sent, which they could report as true and break the ledger contract.
	if purgePaused() {
		purgePausedNotice.Do(func() {
			log.Printf("purge: purging is paused on this node, so it tells no edge about its " +
				"changes until it is resumed")
		})
		return false, nil
	}

	// A development run has no edge to invalidate, and the outlet would be a real call
	// against a real account.
	if os.Getenv("DEV_MODE") == "1" {
		log.Printf("purge: DEV_MODE=1, skipping %d URL(s): %v", len(clean), clean)
		return false, nil
	}

	return o.Purge(clean)
}

// dedupeNonEmpty drops empty entries and repeats, keeping the first occurrence's order.
func dedupeNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
