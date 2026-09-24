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

	if len(reply) != 1 || reply[0].ID != "reply-1" {
		t.Fatalf("unexpected reply: %+v", reply)
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
