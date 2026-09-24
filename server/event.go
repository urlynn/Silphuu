package main

// event.go — two-node Event Sourcing append-only log engine (JSONL + ULID).
//
// Design:
//   - Persistence = state/events.jsonl, one event per line; the log is the source of
//     truth (no snapshots).
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
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Event types (the full dynamic-domain set; static-domain data never enters the event log)
const (
	EvCommentAdded   = "comment_added"   // payload: Comment (id=ULID, post_id, rid=ULID or "", ...)
	EvCommentLiked   = "comment_liked"   // payload: {"id":ULID,"post_id":int}
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

// nodeName returns the local origin (overridable via the NODE_NAME env var).
func nodeName() string {
	if n := os.Getenv("NODE_NAME"); n != "" {
		return n
	}
	return "local"
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
			if evt.Origin == localOrigin && evt.Seq > localSeq {
				localSeq = evt.Seq
			}
			applyEvent(&evt)
			evAppliedCount++
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

// commitEventLocked allocates ULID+seq, appends to the log (fsync), and applies the
// projection. The caller must already hold eventLogMu.
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
	return nil
}

// readEventLines reads all events in events.jsonl (tolerates a torn final line), for
// sync backfill/catch-up.
func readEventLines() []Event {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	f, err := os.Open(FileEventJSONL)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var evt Event
		if json.Unmarshal(line, &evt) != nil {
			break
		}
		out = append(out, evt)
	}
	return out
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

// closeEventStore closes the log handle (called before process exit; not required).
func closeEventStore() {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	if eventLogFile != nil {
		eventLogFile.Close()
		eventLogFile = nil
	}
}

// getLocalSeq reads the max seq allocated on this node in a thread-safe way.
func getLocalSeq() uint64 {
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	return localSeq
}
