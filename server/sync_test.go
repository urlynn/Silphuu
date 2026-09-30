package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSyncDisabledByDefault(t *testing.T) {
	os.Unsetenv("SYNC_UDS_SOCK")
	if os.Getenv("SYNC_UDS_SOCK") != "" {
		t.Fatalf("expected SYNC_UDS_SOCK to be empty")
	}
}

func TestSingleUDSClientExchange(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "uds_test_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "test.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	// Mock Rust UDS server
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// 1. Read Go's OP_SYNC_REQ
		hdr := make([]byte, 13)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			t.Errorf("server read hdr err: %v", err)
			return
		}
		pLen := binary.BigEndian.Uint32(hdr[0:4])
		op := hdr[4]
		reqID := binary.BigEndian.Uint64(hdr[5:13])

		if op != opSyncReq {
			t.Errorf("expected opSyncReq (1), got %d", op)
		}

		payload := make([]byte, pLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Errorf("server read payload err: %v", err)
			return
		}

		var reqEvents []Event
		if err := json.Unmarshal(payload, &reqEvents); err != nil {
			t.Errorf("unmarshal err: %v", err)
			return
		}

		// Reply with OP_SYNC_RESP (echo or reply events)
		respEvents := []Event{
			{ID: "reply-1", Origin: "peer-node", Seq: 99},
		}
		respBytes, _ := json.Marshal(respEvents)

		respFrame := make([]byte, 13+len(respBytes))
		binary.BigEndian.PutUint32(respFrame[0:4], uint32(len(respBytes)))
		respFrame[4] = opSyncResp
		binary.BigEndian.PutUint64(respFrame[5:13], reqID)
		copy(respFrame[13:], respBytes)

		if _, err := conn.Write(respFrame); err != nil {
			t.Errorf("server write err: %v", err)
			return
		}
	}()

	client := newSingleUDSClient()
	syncUDSSock = sockPath

	events := []Event{
		{ID: "local-1", Origin: localOrigin, Seq: 1},
	}

	reply, err := client.doSync(events)
	if err != nil {
		t.Fatalf("client.doSync failed: %v", err)
	}

	if len(reply.Events) != 1 || reply.Events[0].ID != "reply-1" {
		t.Fatalf("unexpected reply: %+v", reply)
	}
	// A peer that answers with a bare array carries records but no report, so nothing it says
	// can be read as a delivery: the batch has to stay unacknowledged and be offered again.
	if reply.Seen != nil {
		t.Fatalf("an answer without a report must confirm nothing, got %+v", reply.Seen)
	}

	client.closeConn()
	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server timed out")
	}
}

func TestSingleUDSClientIncomingReq(t *testing.T) {
	// Answering a peer that is behind persists the sync state, which belongs in the
	// deployment rather than the checkout.
	useExampleSite(t)

	tmpDir, err := os.MkdirTemp("", "uds_test_incoming_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "test_incoming.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Server sends an OP_SYNC_REQ to client (remote peer pushed events)
		inEvents := []Event{
			{ID: "remote-100", Origin: "remote-peer", Seq: 100},
		}
		inBytes, _ := json.Marshal(inEvents)
		reqID := uint64(5555)

		reqFrame := make([]byte, 13+len(inBytes))
		binary.BigEndian.PutUint32(reqFrame[0:4], uint32(len(inBytes)))
		reqFrame[4] = opSyncReq
		binary.BigEndian.PutUint64(reqFrame[5:13], reqID)
		copy(reqFrame[13:], inBytes)

		if _, err := conn.Write(reqFrame); err != nil {
			t.Errorf("server write req err: %v", err)
			return
		}

		// Server reads client's OP_SYNC_RESP
		hdr := make([]byte, 13)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			t.Errorf("server read resp hdr err: %v", err)
			return
		}
		pLen := binary.BigEndian.Uint32(hdr[0:4])
		op := hdr[4]
		respReqID := binary.BigEndian.Uint64(hdr[5:13])

		if op != opSyncResp {
			t.Errorf("expected opSyncResp (2), got %d", op)
		}
		if respReqID != reqID {
			t.Errorf("expected reqID %d, got %d", reqID, respReqID)
		}

		payload := make([]byte, pLen)
		if pLen > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				t.Errorf("server read resp payload err: %v", err)
				return
			}
		}
	}()

	client := newSingleUDSClient()
	syncUDSSock = sockPath
	if err := client.ensureConn(); err != nil {
		t.Fatalf("ensureConn failed: %v", err)
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server timed out waiting for client response")
	}
	client.closeConn()
}

// TestPendingEventsCarriesEveryOrigin: the channel must not decide what travels by which origin
// a record carries. A node's own records are stamped with whatever name it had when it wrote
// them, so a name-keyed filter strands every record written before a rename — and a record that
// reached this node and nowhere else still has to reach the peer.
func TestPendingEventsCarriesEveryOrigin(t *testing.T) {
	useExampleSite(t)

	other := localOrigin + "-peer"
	logEvents(t,
		Event{ID: "mine-1", Origin: localOrigin, Seq: 1},
		Event{ID: "mine-2", Origin: localOrigin, Seq: 2},
		Event{ID: "stranded-1", Origin: "an-old-name", Seq: 1},
		Event{ID: "third-1", Origin: "a-third-node", Seq: 1},
		Event{ID: "theirs-1", Origin: other, Seq: 1},
	)

	ids := map[string]bool{}
	for _, evt := range pendingEvents(map[string]uint64{localOrigin: 1}, 0) {
		ids[evt.ID] = true
	}
	if ids["mine-1"] {
		t.Error("a record the peer confirmed was offered again")
	}
	if !ids["mine-2"] || !ids["stranded-1"] || !ids["third-1"] {
		t.Errorf("records the peer has not confirmed were left behind: %+v", ids)
	}

	// The batch bound is what keeps one frame from holding the whole log.
	if got := pendingEvents(map[string]uint64{}, 2); len(got) != 2 {
		t.Errorf("batch bound ignored: got %d records", len(got))
	}
}

// logEvents appends records to the event log as they would have been written.
func logEvents(t *testing.T, events ...Event) {
	t.Helper()
	f, err := os.OpenFile(FileEventJSONL, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	defer f.Close()
	for _, evt := range events {
		line, err := json.Marshal(evt)
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
}

// TestTheAckIsThePeersReport: the ack states what the peer holds, so a report that falls back
// has to take the ack with it. A raised-only watermark would leave the records the peer lost
// unoffered forever, which is the shape a delivery ack must not have.
func TestTheAckIsThePeersReport(t *testing.T) {
	useExampleSite(t)

	origin := localOrigin + "-peer"
	defer func() {
		syncMu.Lock()
		peerAck = map[string]uint64{}
		syncMu.Unlock()
	}()

	if !advancePeerAck(map[string]uint64{origin: 9}) {
		t.Fatal("a report that moved the ack reported no movement")
	}
	if got := ackSnapshot()[origin]; got != 9 {
		t.Fatalf("ack did not follow the report: got %d, want 9", got)
	}

	if !advancePeerAck(map[string]uint64{origin: 4}) {
		t.Fatal("a report that lowered the ack reported no movement")
	}
	if got := ackSnapshot()[origin]; got != 4 {
		t.Fatalf("a peer that lost records still reads as holding them: got %d, want 4", got)
	}

	if advancePeerAck(map[string]uint64{origin: 4}) {
		t.Error("an unchanged report reported movement, which would drive a resend loop")
	}
	if advancePeerAck(nil) {
		t.Error("an absent report must not be read as confirmation")
	}
}

// TestCommitKicksTheSyncLoop: a local commit has to wake the sync loop by itself. Without the
// kick the event sits until the flush window expires, which is the whole difference between a
// real-time channel and a batched one.
func TestCommitKicksTheSyncLoop(t *testing.T) {
	useExampleSite(t)

	// Drain a wakeup another test may have left in the buffer.
	select {
	case <-syncKick:
	default:
	}

	// An empty blob_ready payload: the commit is what this test is about, and this one's
	// projection returns before it touches the cache.
	if err := CommitEvent(EvBlobReady, blobReadyPayload{}); err != nil {
		t.Fatalf("CommitEvent failed: %v", err)
	}

	select {
	case <-syncKick:
	case <-time.After(2 * time.Second):
		t.Fatal("commit did not wake the sync loop")
	}
}
