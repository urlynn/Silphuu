package main

// event_compact.go — the event log's snapshot, and the truncation that follows it.
//
// The log is append-only and is also the sync channel, so it cannot simply be rewritten in
// place: a record the peer has not confirmed still has to travel. What it can do is stop
// carrying records that are both already applied and already delivered, once the state they
// produced is written down somewhere else. That state is this snapshot.
//
// Only the comment projection has nowhere else to live — every other domain the log carries
// lands as a file the engine reads at startup (see applyLive). The snapshot therefore holds
// the comments and the per-origin watermark the truncation was taken at, and nothing else.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// eventSnapshotVersion is the shape of state/snapshot.json. A version this build does not
// know is refused rather than ignored: the log it summarizes has already been truncated, so
// treating the snapshot as absent would lose exactly what the truncation removed.
const eventSnapshotVersion = 1

// eventSnapshot is the state a truncated log no longer carries.
type eventSnapshot struct {
	Version  int               `json:"version"`
	Baseline map[string]uint64 `json:"baseline"`
	Comments []Comment         `json:"comments"`
}

// eventBaseline is the per-origin watermark the loaded snapshot was taken at. Replay skips
// records at or below it — they are already in the snapshot, and a like applied twice would
// be counted twice. It is also the floor the sync watermarks start from, because a truncated
// log must not read as "this node holds nothing".
var eventBaseline = map[string]uint64{}

// loadEventSnapshot reads state/snapshot.json. An absent file is not an error: a deployment
// that has never compacted has none.
func loadEventSnapshot() (*eventSnapshot, error) {
	f, err := os.Open(FileSnapshot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var snap eventSnapshot
	if err := json.NewDecoder(f).Decode(&snap); err != nil {
		return nil, fmt.Errorf("parse event snapshot: %w", err)
	}
	return &snap, nil
}

// writeEventSnapshot writes state/snapshot.json atomically. The rename is flushed to the
// directory before returning, because the caller truncates the log right after: a snapshot
// that only exists in the page cache is not a place to have moved the state to.
func writeEventSnapshot(baseline map[string]uint64, comments []Comment) error {
	tmp := FileSnapshot + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(eventSnapshot{
		Version:  eventSnapshotVersion,
		Baseline: baseline,
		Comments: comments,
	})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, FileSnapshot); err != nil {
		return err
	}
	syncDir(FileSnapshot)
	return nil
}

// syncDir flushes a directory entry so a rename survives a crash. The error is dropped: a
// filesystem that cannot flush a directory still holds the file, and the caller has already
// re-read the snapshot to confirm it is complete.
func syncDir(path string) {
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// eventPeerAck returns how far the peer holds each origin.
//
// It prefers the live map but falls back to the persisted one: the live map is filled by the
// sync loop, which does not run at all on a node with no peer configured, and truncating on an
// empty map would drop the records a peer would have asked for.
func eventPeerAck() map[string]uint64 {
	syncMu.Lock()
	live := make(map[string]uint64, len(peerAck))
	for k, v := range peerAck {
		live[k] = v
	}
	syncMu.Unlock()
	if len(live) > 0 {
		return live
	}
	data, err := os.ReadFile(FileSyncState)
	if err != nil {
		return nil
	}
	var st struct {
		PeerAck map[string]uint64 `json:"peer_ack"`
	}
	if json.Unmarshal(data, &st) != nil {
		return nil
	}
	return st.PeerAck
}

// syncStateExists reports whether this deployment has ever paired with a peer. It separates
// "no peer by design" from "a peer that cannot be reached right now": the first may drop
// everything the snapshot holds, the second may not.
func syncStateExists() bool {
	_, err := os.Stat(FileSyncState)
	return err == nil
}

// eventCutoff is the highest seq each origin may drop. A record may go when its effect is in
// the snapshot and the peer no longer needs it, so the cutoff is the lower of the two. An
// origin the peer has never reported on is unknown rather than zero, and keeps everything.
func eventCutoff(baseline, ack map[string]uint64) map[string]uint64 {
	standalone := !syncEnabled() && !syncStateExists()
	cut := make(map[string]uint64, len(baseline))
	for origin, b := range baseline {
		if standalone {
			cut[origin] = b
			continue
		}
		a, known := ack[origin]
		if !known {
			cut[origin] = 0
			continue
		}
		if a < b {
			cut[origin] = a
		} else {
			cut[origin] = b
		}
	}
	return cut
}

// eventCompactionBytes is the log size at which the background loop compacts. It is a var so a
// guard can lower it instead of writing a log that large.
var eventCompactionBytes int64 = 8 << 20

// compactMu serializes the three triggers — startup, the size loop and the admin button — so
// two compactions cannot rewrite the same log at once. It is waited on rather than skipped
// when held: the admin trigger answers a request, and a request answered with success while
// nothing happened is worse than one that waits.
var compactMu sync.Mutex

// compactEventLog writes the comment projection to the snapshot and rewrites the log down to
// the records the peer has not confirmed.
//
// The log is also the sync channel, so a record that is neither applied-and-snapshotted nor
// delivered cannot simply be dropped. Everything else can: its effect lives on, and the log
// stops carrying the history that produced it.
func compactEventLog() error {
	compactMu.Lock()
	defer compactMu.Unlock()

	eventLogMu.Lock()
	defer eventLogMu.Unlock()

	var before int64
	if fi, err := os.Stat(FileEventJSONL); err == nil {
		before = fi.Size()
	}

	ack := eventPeerAck()

	baseline := make(map[string]uint64, len(eventBaseline))
	for k, v := range eventBaseline {
		baseline[k] = v
	}
	if err := scanEventStreamLocked(func(evt *Event) bool {
		if evt.Seq > baseline[evt.Origin] {
			baseline[evt.Origin] = evt.Seq
		}
		return true
	}); err != nil {
		return err
	}

	if err := writeEventSnapshot(baseline, storeSnapshot()); err != nil {
		return err
	}
	// Read it back before anything is removed: the log is about to lose the records this file
	// is the only copy of.
	snap, err := loadEventSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot did not verify: %w", err)
	}
	if snap == nil || snap.Version != eventSnapshotVersion {
		return fmt.Errorf("snapshot did not verify: absent or an unsupported version")
	}

	cut := eventCutoff(baseline, ack)

	tmp := FileEventJSONL + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(out)
	err = scanEventStreamLocked(func(evt *Event) bool {
		if evt.Seq <= cut[evt.Origin] {
			return true
		}
		line, mErr := json.Marshal(evt)
		if mErr != nil {
			err = mErr
			return false
		}
		if _, wErr := w.Write(append(line, '\n')); wErr != nil {
			err = wErr
			return false
		}
		return true
	})
	if err == nil {
		err = w.Flush()
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}

	// The handle points at the inode the rename is about to replace. Appending through it
	// afterwards would write to a file nothing reads.
	if eventLogFile != nil {
		eventLogFile.Close()
		eventLogFile = nil
	}
	if err := os.Rename(FileEventJSONL, FileEventJSONL+".bak"); err != nil && !os.IsNotExist(err) {
		reopenEventLog()
		return err
	}
	if err := os.Rename(tmp, FileEventJSONL); err != nil {
		// Put the old log back rather than leave the path empty: the snapshot holds the
		// comments, but the records the peer has not seen are only in the backup.
		os.Rename(FileEventJSONL+".bak", FileEventJSONL)
		reopenEventLog()
		return err
	}
	syncDir(FileEventJSONL)
	if err := reopenEventLog(); err != nil {
		return err
	}
	eventBaseline = baseline
	var after int64
	if fi, err := os.Stat(FileEventJSONL); err == nil {
		after = fi.Size()
	}
	log.Printf("[EventStore] compacted event log: %d -> %d bytes", before, after)
	return nil
}

// reopenEventLog points the append handle at whatever is at the log's path now. A compaction
// that fails partway still has to leave the node able to record events.
func reopenEventLog() error {
	lf, err := os.OpenFile(FileEventJSONL, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("reopen event log: %w", err)
	}
	eventLogFile = lf
	return nil
}

// eventCompactionInterval is how often the loop looks at the log's size. A var so a guard can
// shorten it.
var eventCompactionInterval = 10 * time.Minute

// eventCompactionLoop compacts once at startup and then only when the log has grown past
// eventCompactionBytes. It runs whether or not a peer is configured: a node with no peer still
// has a log, and it is the one that never advances a watermark.
func eventCompactionLoop() {
	if v := os.Getenv("SILPHUU_EVENT_COMPACT_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			log.Printf("[EventStore] SILPHUU_EVENT_COMPACT_BYTES=%q is not an integer; using %d", v, eventCompactionBytes)
		} else {
			eventCompactionBytes = n
		}
	}
	if v := os.Getenv("SILPHUU_EVENT_COMPACT_SEC"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			log.Printf("[EventStore] SILPHUU_EVENT_COMPACT_SEC=%q is not a positive number of seconds; using %s", v, eventCompactionInterval)
		} else {
			eventCompactionInterval = time.Duration(n) * time.Second
		}
	}
	if eventCompactionBytes <= 0 {
		log.Printf("[EventStore] event log compaction is off (SILPHUU_EVENT_COMPACT_BYTES=%d)", eventCompactionBytes)
		return
	}

	compactIfLarge()
	t := time.NewTicker(eventCompactionInterval)
	defer t.Stop()
	for range t.C {
		compactIfLarge()
	}
}

// compactIfLarge compacts when the log is over the threshold. The size is read rather than
// counted, so a log that grew while the process was down is seen too.
func compactIfLarge() {
	fi, err := os.Stat(FileEventJSONL)
	if err != nil {
		return
	}
	if fi.Size() < eventCompactionBytes {
		return
	}
	if err := compactEventLog(); err != nil {
		log.Printf("[EventStore] event log compaction failed: %v", err)
	}
}
