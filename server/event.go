package main

// event.go — two-node Event Sourcing append-only log engine (JSONL + ULID).
//
// Design:
//   - Persistence = state/events.jsonl, one event per line, plus state/snapshot.json, which
//     carries the state a truncated log no longer holds (event_compact.go).
//   - Event identity = ULID (globally unique, independent of node count); (origin, seq)
//     is only used for peer watermarks and backfill ordering.
//   - fsync immediately after append; comment-level writes are rare, so this is affordable.
//   - Zero traffic when idle: this file only handles local persistence; cross-node
//     push/backfill lives in sync.go.
//
// Projections (GlobalState/Comments etc.) are built by upper subsystems (comment.go etc.).

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event types carried by the log. A domain belongs here when its state changes at runtime
// and cannot be derived from the site's sources: the comment projection (comment_state.go)
// and the album's records (photo_state.go).
//
// Landing the projection is a separate question from carrying it. Comment state stays in
// memory, while the album's records land on config/photo.json: see applyLive.
const (
	EvCommentAdded   = "comment_added"   // payload: Comment (id=ULID, post_id, rid=ULID or "", ...)
	EvCommentLiked   = "comment_liked"   // payload: {"id":ULID,"delta":int} (post_id is not carried; see commentEventPostID)
	EvCommentDeleted = "comment_deleted" // payload: {"id":ULID,"post_id":int} (tombstone)
	EvViewDelta      = "view_delta"      // payload: {"posts":{post_id:int:delta int},...} (cross-node view merge, lossy by design)
)

// Event is the event envelope (one JSON object per JSONL line).
type Event struct {
	ID      string          `json:"id"`      // ULID, globally unique
	Origin  string          `json:"origin"`  // producing node (NODE_NAME env, defaults to "local")
	Seq     uint64          `json:"seq"`     // per-node monotonically increasing (watermark/backfill only, not identity)
	TS      int64           `json:"ts"`      // UnixMilli
	Type    string          `json:"type"`    // see event type constants above
	Payload json.RawMessage `json:"payload"` // event body
}

// Engine state.

var (
	eventLogMu     sync.Mutex // serializes append + projection
	eventLogFile   *os.File   // open handle to events.jsonl (O_APPEND)
	localOrigin    string     // local node name (NODE_NAME env, defaults to "local")
	localSeq       uint64     // max seq allocated on this node
	evAppliedCount int64      // events applied in this process (startup replay + runtime), for logging
)

// nodeName returns the local node's identity: NODE_NAME when set, the kernel host name
// otherwise.
//
// The identity has to be unique across the cluster and stable across restarts, and nothing
// else: it is what tells this node's events from the peer's. Peers are addressed by the
// daemon's own setting rather than by this name, so it does not have to be routable; a host
// name is enough. A deployment that names its nodes in a way the kernel does not know sets
// NODE_NAME.
func nodeName() string {
	if n := os.Getenv("NODE_NAME"); n != "" {
		return n
	}
	name, err := os.Hostname()
	if err != nil || name == "" {
		log.Printf("node name: os.Hostname failed (%v); falling back to %q", err, "local")
		return "local"
	}
	return name
}

// InitEventStore opens the event log and replays existing events (tolerates a torn final line).
func InitEventStore() error {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()

	if localOrigin == "" {
		localOrigin = nodeName()
	}
	initCommentStore()
	if err := os.MkdirAll(filepath.Dir(FileEventJSONL), 0o755); err != nil {
		return err
	}
	// A compaction that was interrupted left these behind; they are never a source of state.
	os.Remove(FileEventJSONL + ".tmp")
	os.Remove(FileSnapshot + ".tmp")

	// 0) The snapshot holds what the log no longer carries. Loading it first gives the replay
	// below a baseline to skip against, and gives the sync watermarks a floor.
	eventBaseline = map[string]uint64{}
	snap, err := loadEventSnapshot()
	if err != nil {
		return err
	}
	if snap != nil {
		if snap.Version != eventSnapshotVersion {
			return fmt.Errorf("event snapshot version %d is not supported by this build", snap.Version)
		}
		if snap.Baseline != nil {
			eventBaseline = snap.Baseline
		}
		storeRestore(snap.Comments)
		if b := eventBaseline[localOrigin]; b > localSeq {
			localSeq = b
		}
	}

	// 1) Replay existing events to rebuild the localSeq watermark (projections are
	// attached later by subsystems via applyEvent)
	f, err := os.Open(FileEventJSONL)
	if err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			var evt Event
			if err := json.Unmarshal(line, &evt); err != nil {
				// Tolerate a torn final line: stop here (all earlier lines are complete)
				break
			}
			if evt.Seq <= eventBaseline[evt.Origin] {
				continue // already in the snapshot
			}
			if evt.Origin == localOrigin && evt.Seq > localSeq {
				localSeq = evt.Seq
			}
			applyEvent(&evt)
			evAppliedCount++
		}
		if err := sc.Err(); err != nil {
			log.Printf("[EventStore] replay stopped early; later events were not applied: %v", err)
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return err
	}

	// 2) Open the append-only handle
	lf, err := os.OpenFile(FileEventJSONL, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open event log: %w", err)
	}
	eventLogFile = lf

	log.Printf("[EventStore] 事件日志就绪 origin=%s file=%s 已回放 %d 条(seq=%d)",
		localOrigin, FileEventJSONL, evAppliedCount, localSeq)
	return nil
}

// CommitEvent appends a local event and applies the projection.
func CommitEvent(typ string, payload any) error {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	return commitEventLocked(localOrigin, typ, payload)
}

// commitEventLocked allocates ULID+seq, appends to the log (fsync), applies the projection, and
// wakes the sync loop so the peer sees the event without waiting out the flush window. The
// caller must already hold eventLogMu.
func commitEventLocked(origin, typ string, payload any) error {
	if eventLogFile == nil {
		return fmt.Errorf("event store not initialized")
	}
	if origin != localOrigin {
		return fmt.Errorf("commitEventLocked: remote events must use appendRemoteEvent")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
	}
	evt := Event{
		ID:      NewULID(),
		Origin:  origin,
		Seq:     localSeq + 1,
		TS:      time.Now().UnixMilli(),
		Type:    typ,
		Payload: body,
	}
	if err := appendAndApply(&evt); err != nil {
		return err
	}
	kickSync()
	return nil
}

// scanEventStream calls visit for each event in a JSONL stream, in order, and stops when
// visit returns false. A torn final line ends the scan, as it does at startup.
func scanEventStream(r io.Reader, visit func(*Event) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt Event
		if json.Unmarshal(line, &evt) != nil {
			return nil // tolerate a torn final line: all earlier lines are complete
		}
		if !visit(&evt) {
			return nil
		}
	}
	return sc.Err()
}

// scanEvents streams events.jsonl under the log lock. A caller that needs only a prefix of
// the log stops the scan by returning false from visit.
func scanEvents(visit func(*Event) bool) error {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	return scanEventStreamLocked(visit)
}

// scanEventStreamLocked is scanEventStream over events.jsonl. The caller must hold
// eventLogMu.
func scanEventStreamLocked(visit func(*Event) bool) error {
	f, err := os.Open(FileEventJSONL)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	return scanEventStream(f, visit)
}

// appendAndApply appends one line to the log + applies it to the in-memory projection.
// The caller must hold eventLogMu.
func appendAndApply(evt *Event) error {
	line, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("marshal event envelope: %w", err)
	}
	line = append(line, '\n')
	if _, err := eventLogFile.Write(line); err != nil {
		return fmt.Errorf("append event log: %w", err)
	}
	if evt.Origin == localOrigin && evt.Seq > localSeq {
		localSeq = evt.Seq
	}
	evAppliedCount++
	applyEvent(evt)
	applyLive(evt)
	return nil
}

// appendRemoteEvent persists + projects an event pushed by the peer (uses the peer's seq).
// Idempotency/gap checks are done by sync.go before calling. The caller must hold eventLogMu.
func appendRemoteEvent(evt *Event) error {
	if eventLogFile == nil {
		return fmt.Errorf("event store not initialized")
	}
	return appendAndApply(evt)
}

// applyEvent applies one event to the in-memory projection.
func applyEvent(evt *Event) {
	storeApply(evt) // comment projection (comment_state.go)
}

// applyLive runs what an event that just arrived or was just produced does to the served tree:
// the projections that land on the filesystem, and the render a change has to ask for. The
// startup replay calls applyEvent alone: running these from the log would rewrite the site's own
// files on every start, and would purge the CDN for pages that did not change.
func applyLive(evt *Event) {
	applyPhotoEvent(evt)
	applyConfigEvent(evt)
	applyPostEvent(evt)
	applyFontEvent(evt)
	applyBlobReadyEvent(evt)
	applyCommentEvent(evt)
	// Queued rather than run here: the sweep walks the filesystem, and this runs under the log
	// lock. See reclaim.go.
	reclaimOnEvent(evt.Type)
}

// applyCommentEvent re-renders the page a comment change belongs to.
//
// The comment projection lives in memory alone, so a change leaves no file for the watcher to
// notice and the page carrying the list is only as new as its last render. Asking for that render
// here rather than in the handler that produced the event is what makes the peer follow: the peer
// applies the same event without running any handler of its own, and its page is one a reader
// sees.
func applyCommentEvent(evt *Event) {
	postID, ok := commentEventPostID(evt)
	if !ok {
		return
	}
	invalidateCommentCache(postID)
}

// commentEventPostID names the page a comment event belongs to.
//
// Added and deleted carry the page in their payload. A like carries only the comment it names, so
// the page is read back from the projection: applyEvent has already applied the event by the time
// applyLive runs, and a like never removes the record it names.
func commentEventPostID(evt *Event) (int, bool) {
	switch evt.Type {
	case EvCommentAdded, EvCommentDeleted:
		var p struct {
			PostID int `json:"post_id"`
		}
		if json.Unmarshal(evt.Payload, &p) != nil {
			return 0, false
		}
		return p.PostID, true
	case EvCommentLiked:
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(evt.Payload, &p) != nil || p.ID == "" {
			return 0, false
		}
		if c := cmGet(p.ID); c != nil {
			return c.PostID, true
		}
	}
	return 0, false
}

// EvBlobReady says the bytes behind a record are on disk.
//
// It exists because the bytes and the record travel separately: a node renders the card as
// soon as it learns of the record, which is before any node has finished writing the bytes.
// The card it renders names the same URLs either way, so this event invalidates the page
// rather than changing it.
const EvBlobReady = "blob_ready"

// blobReadyPayload names the page to invalidate, and the files this event is about.
//
// Paths is the record of what the producer wrote, which is what the blob face ships. It is
// carried rather than recomputed, because only the producer knows which files a pipeline
// produced.
type blobReadyPayload struct {
	Page  string   `json:"page"`
	Paths []string `json:"paths,omitempty"`
}

// applyBlobReadyEvent invalidates a page whose bytes arrived after it was rendered.
func applyBlobReadyEvent(evt *Event) {
	var p blobReadyPayload
	if json.Unmarshal(evt.Payload, &p) != nil || p.Page == "" {
		return
	}
	invalidateCache(p.Page)
}

// closeEventStore closes the log handle (called before process exit; not required).
func closeEventStore() {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	if eventLogFile != nil {
		eventLogFile.Close()
		eventLogFile = nil
	}
}
