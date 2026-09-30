package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// logBytes is the size of the event log on disk.
func logBytes(t *testing.T) int64 {
	t.Helper()
	fi, err := os.Stat(FileEventJSONL)
	if err != nil {
		t.Fatalf("stat %s: %v", FileEventJSONL, err)
	}
	return fi.Size()
}

// logRecordCount counts the records the log holds.
func logRecordCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := scanEvents(func(*Event) bool { n++; return true }); err != nil {
		t.Fatalf("scan %s: %v", FileEventJSONL, err)
	}
	return n
}

// resetWatermarks puts the sync watermarks back to those of a node that has never paired, so a
// guard reasons about the cutoff from a state it set itself.
func resetWatermarks() {
	syncMu.Lock()
	peerAck = map[string]uint64{}
	peerSeen = map[string]uint64{}
	syncMu.Unlock()
}

// commitRemoteEvent appends an event the peer produced, the way a sync batch does.
func commitRemoteEvent(t *testing.T, origin string, seq uint64, typ string, payload any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal %s payload: %v", typ, err)
	}
	eventLogMu.Lock()
	defer eventLogMu.Unlock()
	evt := &Event{
		ID:      NewULID(),
		Origin:  origin,
		Seq:     seq,
		TS:      time.Now().UnixMilli(),
		Type:    typ,
		Payload: body,
	}
	if err := appendRemoteEvent(evt); err != nil {
		t.Fatalf("appendRemoteEvent(%s/%d): %v", origin, seq, err)
	}
}

// TestTheThresholdCompactsOnlyAnOvergrownLog: the loop's own trigger, which is neither the
// startup pass nor the admin button. A log under the threshold has to be left alone — a
// compaction that fires on every tick rewrites the whole log for nothing — and one over it has
// to shrink.
func TestTheThresholdCompactsOnlyAnOvergrownLog(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	writeTestFile(t, FileEventJSONL,
		`{"id":"probe000000000000000000001","origin":"probe","seq":1,"type":"view_delta","payload":{}}`+"\n")

	old := eventCompactionBytes
	t.Cleanup(func() { eventCompactionBytes = old })

	eventCompactionBytes = 1 << 30
	compactIfLarge()
	if n := logRecordCount(t); n != 1 {
		t.Errorf("a log under the threshold holds %d record(s), want the 1 it had", n)
	}

	eventCompactionBytes = 1
	compactIfLarge()
	if n := logRecordCount(t); n != 0 {
		t.Errorf("a log over the threshold holds %d record(s), want 0", n)
	}
	if _, err := os.Stat(FileSnapshot); err != nil {
		t.Errorf("no snapshot after the threshold compaction: %v", err)
	}
}

// TestPendingEventsDoesNotPayForTheWholeLog: this runs on every sync exchange, so what it costs
// has to be set by the batch it was asked for rather than by how long the log has grown. The
// bytes allocated while collecting the same batch are compared across two logs, one four times
// the other: a whole-log read allocates in proportion to the log, and a bounded one does not.
func TestPendingEventsDoesNotPayForTheWholeLog(t *testing.T) {
	useExampleSite(t)

	measure := func(records int) uint64 {
		t.Helper()
		var b strings.Builder
		for i := 1; i <= records; i++ {
			fmt.Fprintf(&b, `{"id":"probe%026d","origin":"probe","seq":%d,"type":"comment_added","payload":{}}`+"\n", i, i)
		}
		writeTestFile(t, FileEventJSONL, b.String())

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got := pendingEvents(map[string]uint64{}, syncBatchEvents+1)
		runtime.ReadMemStats(&after)
		if len(got) != syncBatchEvents+1 {
			t.Fatalf("pendingEvents returned %d record(s) from a %d-record log, want %d", len(got), records, syncBatchEvents+1)
		}
		return after.TotalAlloc - before.TotalAlloc
	}

	small := measure(8000)
	large := measure(32000)
	if large >= small*2 {
		t.Errorf("collecting one batch allocated %d bytes from a 32000-record log and %d from an 8000-record one; the cost follows the log rather than the batch", large, small)
	}
}

// TestCompactionKeepsTheStateItFoldsAway: the snapshot is the only place the records a
// truncated log no longer carries are written down, so a restart after a compaction has to
// reproduce the projection exactly.
//
// Likes are the reason this is not a formality. comment_liked is cumulative and has no id of
// its own, so a record that reaches the projection twice counts twice: a baseline the replay
// does not skip against turns three likes into six, and a restart is where that shows.
func TestCompactionKeepsTheStateItFoldsAway(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	kept := addComment("probe", "", "", "", "kept", 0, "test", "")
	if kept == nil {
		t.Fatal("the comment was not committed; this guard would run against an empty log")
	}
	for i := 0; i < 3; i++ {
		likeComment(kept.ID, 1)
	}
	removed := addComment("probe", "", "", "", "removed", 0, "test", "")
	if removed == nil {
		t.Fatal("the second comment was not committed")
	}
	deleteComment(removed.ID)

	wantLikes := cmLikeCount(kept.ID)
	if wantLikes != 3 {
		t.Fatalf("likes = %d before compaction, want 3; the fixture never exercised the cumulative path", wantLikes)
	}
	if n := cmTotalCount(); n != 1 {
		t.Fatalf("comments = %d before compaction, want 1", n)
	}

	before := logBytes(t)
	if err := compactEventLog(); err != nil {
		t.Fatalf("compactEventLog: %v", err)
	}
	if _, err := os.Stat(FileSnapshot); err != nil {
		t.Fatalf("no snapshot after compaction: %v", err)
	}
	after := logBytes(t)
	if after >= before {
		t.Fatalf("the log is %d bytes after compaction, was %d; nothing was truncated, so the restart below proves nothing", after, before)
	}
	// No peer has ever been paired with here, so every applied record may go.
	if n := logRecordCount(t); n != 0 {
		t.Fatalf("%d record(s) left in the log of a node with no peer, want 0", n)
	}

	closeEventStore()
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore after compaction: %v", err)
	}
	if n := cmTotalCount(); n != 1 {
		t.Errorf("comments = %d after a restart, want 1", n)
	}
	if got := cmLikeCount(kept.ID); got != wantLikes {
		t.Errorf("likes = %d after a restart, want %d; the snapshot and the replayed log disagreed", got, wantLikes)
	}
	if cmGet(removed.ID) != nil {
		t.Error("the deleted comment came back from the snapshot")
	}
}

// TestCompactionKeepsWhatThePeerHasNotConfirmed: the log is the sync channel as well as the
// local record, so a record the peer has not acknowledged has nowhere else to travel from and
// cannot be truncated away.
//
// The acknowledgement is read from the file the sync loop persists rather than from the
// in-memory map: this node's sync loop may not have run at all, and a cutoff computed from an
// empty map would drop exactly the records the peer is waiting for.
func TestCompactionKeepsWhatThePeerHasNotConfirmed(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	for i := 0; i < 5; i++ {
		if c := addComment("probe", "", "", "", "probe", 0, "test", ""); c == nil {
			t.Fatalf("comment %d was not committed", i+1)
		}
	}
	// The seq a record lands at is read back rather than assumed: it is per-origin and
	// monotonic for the life of the process, not for the life of the log.
	var seqs []uint64
	_ = scanEvents(func(evt *Event) bool { seqs = append(seqs, evt.Seq); return true })
	if len(seqs) != 5 {
		t.Fatalf("the log holds %d record(s), want the 5 the fixture committed", len(seqs))
	}
	// The peer reported that it holds the first three, and the report is on disk only.
	ack, err := json.Marshal(map[string]any{"peer_ack": map[string]uint64{localOrigin: seqs[2]}})
	if err != nil {
		t.Fatalf("marshal the peer report: %v", err)
	}
	writeTestFile(t, FileSyncState, string(ack))

	if err := compactEventLog(); err != nil {
		t.Fatalf("compactEventLog: %v", err)
	}

	got := pendingEvents(ackSnapshot(), 0)
	if len(got) != 2 {
		t.Fatalf("pendingEvents returned %d record(s), want the 2 the peer has not confirmed", len(got))
	}
	for i, evt := range got {
		if want := seqs[i+3]; evt.Seq != want {
			t.Errorf("pending record %d is seq %d, want %d", i, evt.Seq, want)
		}
	}
}

// TestCompactionDoesNotLowerTheWatermarkForAPeer: a truncated log no longer holds the records
// the watermark was taken at, so a watermark derived from the log alone falls back. A peer
// reading a lower watermark re-delivers records this node already applied, and a like applied
// twice counts twice — the corruption is visible, not just a wasted round trip.
func TestCompactionDoesNotLowerTheWatermarkForAPeer(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	const peer = "probe-peer"
	const id = "zzzz000000000042"
	commitRemoteEvent(t, peer, 1, EvCommentAdded, Comment{ID: id, Content: "from the peer", PostID: 0})
	commitRemoteEvent(t, peer, 2, EvCommentLiked, map[string]any{"id": id, "delta": 1})

	if got := cmLikeCount(id); got != 1 {
		t.Fatalf("likes = %d before compaction, want 1; the fixture never exercised the cumulative path", got)
	}

	if err := compactEventLog(); err != nil {
		t.Fatalf("compactEventLog: %v", err)
	}
	if n := logRecordCount(t); n != 0 {
		t.Fatalf("%d record(s) left; the peer's records were not truncated, so the watermark below is not tested", n)
	}

	initSyncState()
	syncMu.Lock()
	seen := peerSeen[peer]
	syncMu.Unlock()
	if seen != 2 {
		t.Fatalf("peerSeen[%s] = %d after compaction, want 2; the watermark fell back to what the truncated log still holds", peer, seen)
	}

	// The peer re-delivers, as it would after reading a lower watermark.
	applyRemoteBatch([]Event{
		{
			ID: "peerredelivery00000000001", Origin: peer, Seq: 1, Type: EvCommentAdded,
			Payload: []byte(`{"id":"` + id + `","content":"from the peer","post_id":0}`),
		},
		{
			ID: "peerredelivery00000000002", Origin: peer, Seq: 2, Type: EvCommentLiked,
			Payload: []byte(`{"id":"` + id + `","delta":1}`),
		},
	})
	if got := cmLikeCount(id); got != 1 {
		t.Errorf("likes = %d after the peer re-delivered, want 1", got)
	}
	if n := cmTotalCount(); n != 1 {
		t.Errorf("comments = %d after the peer re-delivered, want 1", n)
	}
}

// TestCompactionLeavesTheHandleOnTheNewLog: the rewrite replaces the file the store appends
// through, so a compaction that does not reopen the handle sends every later comment into an
// inode nothing reads. Nothing reports the loss at the time; the comment is simply absent
// after the next restart, which is what this checks.
func TestCompactionLeavesTheHandleOnTheNewLog(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	if c := addComment("probe", "", "", "", "before", 0, "test", ""); c == nil {
		t.Fatal("the first comment was not committed")
	}
	if err := compactEventLog(); err != nil {
		t.Fatalf("compactEventLog: %v", err)
	}
	after := addComment("probe", "", "", "", "after", 0, "test", "")
	if after == nil {
		t.Fatal("the comment after the compaction was not committed")
	}
	if n := logRecordCount(t); n != 1 {
		t.Fatalf("the log holds %d record(s) after the post-compaction commit, want 1", n)
	}

	closeEventStore()
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	if cmGet(after.ID) == nil {
		t.Error("the comment written after the compaction did not survive a restart; the append handle was left on the replaced file")
	}
	if n := cmTotalCount(); n != 2 {
		t.Errorf("comments = %d after a restart, want 2", n)
	}
}

// TestASnapshotWithoutATruncationStillRestarts: a compaction that dies between writing the
// snapshot and rewriting the log is the common crash window, and it must not need a repair
// step. The log still holds every record, so the restart has to skip exactly the ones the
// snapshot already carries rather than applying them a second time.
func TestASnapshotWithoutATruncationStillRestarts(t *testing.T) {
	useExampleSite(t)
	resetWatermarks()

	c := addComment("probe", "", "", "", "kept", 0, "test", "")
	if c == nil {
		t.Fatal("the comment was not committed")
	}
	likeComment(c.ID, 1)
	wantLikes := cmLikeCount(c.ID)
	if wantLikes == 0 {
		t.Fatal("the fixture recorded no like")
	}

	if err := writeEventSnapshot(map[string]uint64{localOrigin: localSeq}, storeSnapshot()); err != nil {
		t.Fatalf("writeEventSnapshot: %v", err)
	}
	before := logRecordCount(t)
	if before == 0 {
		t.Fatal("the log is empty; the replay below would skip nothing")
	}

	closeEventStore()
	if err := InitEventStore(); err != nil {
		t.Fatalf("InitEventStore: %v", err)
	}
	if got := logRecordCount(t); got != before {
		t.Fatalf("the log holds %d record(s), want the %d the interrupted compaction left", got, before)
	}
	if got := cmLikeCount(c.ID); got != wantLikes {
		t.Errorf("likes = %d after a restart, want %d; the replayed records were applied on top of the snapshot", got, wantLikes)
	}
	if n := cmTotalCount(); n != 1 {
		t.Errorf("comments = %d after a restart, want 1", n)
	}
}

// TestAnUnknownSnapshotVersionRefusesToStart: the log a snapshot summarizes has already been
// truncated, so a build that cannot read the snapshot must refuse to start. Starting without
// it would present exactly the comments the truncation moved into the file as deleted.
func TestAnUnknownSnapshotVersionRefusesToStart(t *testing.T) {
	useExampleSite(t)
	closeEventStore()

	writeTestFile(t, FileSnapshot, `{"version":999,"baseline":{},"comments":[]}`)
	if err := InitEventStore(); err == nil {
		t.Error("InitEventStore accepted a snapshot version this build does not know")
	}
}
