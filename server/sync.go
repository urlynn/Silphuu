package main

// sync.go — two-node event sync engine (WebTransport via a single UDS gateway).
//
// Mechanism:
//   - The UDS path is specified solely via the SYNC_UDS_SOCK env var (no fallback, no
//     multiple sockets).
//   - Without SYNC_UDS_SOCK, active-active sync is skipped entirely: zero resource use,
//     zero errors.
//   - When configured, Rust listens on that UDS and Go connects as a client over a
//     full-duplex link.
//   - Both ends use a symmetric 1-RTT binary frame protocol (OP_SYNC_REQ=1,
//     OP_SYNC_RESP=2, correlated by req_id).
//   - The peer acks as soon as events land in its in-memory projection, avoiding extra
//     disk fsync latency.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	opSyncReq  uint8 = 1 // sync request
	opSyncResp uint8 = 2 // sync response
)

var (
	syncCfgOnce sync.Once
	syncMu      sync.Mutex
	peerAckSeq  uint64 // local seq sent to the peer and acknowledged
	peerSeenSeq uint64 // highest peer seq received
	flushSec    time.Duration
	syncUDSSock string

	udsClient *singleUDSClient
)

func syncEnabled() bool {
	syncCfgOnce.Do(func() {
		syncUDSSock = os.Getenv("SYNC_UDS_SOCK")
		if syncUDSSock == "" {
			return
		}
		sec := os.Getenv("SYNC_FLUSH_INTERVAL_SEC")
		if sec == "" {
			sec = "60"
		}
		if v, err := strconv.Atoi(sec); err == nil && v > 0 {
			flushSec = time.Duration(v) * time.Second
		} else {
			flushSec = 60 * time.Second
		}
	})
	return syncUDSSock != ""
}

func initSyncState() {
	syncMu.Lock()
	defer syncMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(FileSyncState), 0o755); err != nil {
		return
	}
	data, err := os.ReadFile(FileSyncState)
	if err != nil {
		return
	}
	var st struct {
		PeerAck  uint64 `json:"peer_ack"`
		PeerSeen uint64 `json:"peer_seen"`
	}
	if json.Unmarshal(data, &st) != nil {
		return
	}
	peerAckSeq = st.PeerAck
	peerSeenSeq = st.PeerSeen
}

func persistSyncStateLocked() {
	data, _ := json.Marshal(map[string]any{
		"peer_ack":  peerAckSeq,
		"peer_seen": peerSeenSeq,
	})
	tmp := FileSyncState + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, FileSyncState)
	}
}

// readLocalEventsRange reads events from events.jsonl with origin=local and seq > after.
func readLocalEventsRange(after uint64) []Event {
	lines := readEventLines()
	var out []Event
	for _, evt := range lines {
		if evt.Origin == localOrigin && evt.Seq > after {
			out = append(out, evt)
		}
	}
	return out
}

type singleUDSClient struct {
	mu        sync.Mutex
	conn      net.Conn
	pending   map[uint64]chan []byte
	pendingMu sync.Mutex
	nextReqID uint64
	closed    bool
}

func newSingleUDSClient() *singleUDSClient {
	return &singleUDSClient{
		pending: make(map[uint64]chan []byte),
	}
}

func (c *singleUDSClient) ensureConn() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		return nil
	}
	conn, err := net.Dial("unix", syncUDSSock)
	if err != nil {
		return err
	}
	c.conn = conn
	c.closed = false
	go c.readLoop(conn)
	return nil
}

func (c *singleUDSClient) closeConn() {
	c.mu.Lock()
	conn := c.conn
	c.conn = nil
	c.closed = true
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	c.pendingMu.Lock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		close(ch)
	}
	c.pendingMu.Unlock()
}

func (c *singleUDSClient) readLoop(conn net.Conn) {
	hdr := make([]byte, 13) // 4 len + 1 op + 8 req_id
	for {
		_, err := io.ReadFull(conn, hdr)
		if err != nil {
			c.closeConn()
			return
		}
		pLen := binary.BigEndian.Uint32(hdr[0:4])
		op := hdr[4]
		reqID := binary.BigEndian.Uint64(hdr[5:13])

		payload := make([]byte, pLen)
		if pLen > 0 {
			_, err = io.ReadFull(conn, payload)
			if err != nil {
				c.closeConn()
				return
			}
		}

		switch op {
		case opSyncResp:
			c.pendingMu.Lock()
			ch, ok := c.pending[reqID]
			if ok {
				delete(c.pending, reqID)
			}
			c.pendingMu.Unlock()
			if ok {
				ch <- payload
			}
		case opSyncReq:
			// Peer actively pushed events through the Rust gateway (passive reconciliation)
			go c.handleIncomingReq(reqID, payload)
		}
	}
}

func (c *singleUDSClient) handleIncomingReq(reqID uint64, payload []byte) {
	var reqEvents []Event
	if err := json.Unmarshal(payload, &reqEvents); err == nil && len(reqEvents) > 0 {
		applyRemoteBatch(reqEvents)
	}

	// Prepare a piggybacked response (own incremental events, or [])
	local := getLocalSeq()
	syncMu.Lock()
	start := peerAckSeq
	isBehind := peerAckSeq < local
	syncMu.Unlock()

	var replyEvents []Event
	if isBehind {
		replyEvents = readLocalEventsRange(start)
	}
	if replyEvents == nil {
		replyEvents = []Event{}
	}

	respPayload, _ := json.Marshal(replyEvents)
	_ = c.sendFrame(opSyncResp, reqID, respPayload)

	if len(replyEvents) > 0 {
		syncMu.Lock()
		peerAckSeq = replyEvents[len(replyEvents)-1].Seq
		persistSyncStateLocked()
		syncMu.Unlock()
	}
}

func (c *singleUDSClient) sendFrame(op uint8, reqID uint64, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("uds disconnected")
	}

	frame := make([]byte, 13+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload)))
	frame[4] = op
	binary.BigEndian.PutUint64(frame[5:13], reqID)
	copy(frame[13:], payload)

	_, err := c.conn.Write(frame)
	return err
}

func (c *singleUDSClient) doSync(events []Event) ([]Event, error) {
	if err := c.ensureConn(); err != nil {
		return nil, err
	}

	payload, err := json.Marshal(events)
	if err != nil {
		return nil, err
	}

	reqID := atomic.AddUint64(&c.nextReqID, 1)
	respCh := make(chan []byte, 1)

	c.pendingMu.Lock()
	c.pending[reqID] = respCh
	c.pendingMu.Unlock()

	if err := c.sendFrame(opSyncReq, reqID, payload); err != nil {
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		c.closeConn()
		return nil, err
	}

	select {
	case respPayload, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("connection closed while awaiting sync response")
		}
		var respEvents []Event
		if len(respPayload) > 0 {
			if err := json.Unmarshal(respPayload, &respEvents); err != nil {
				return nil, fmt.Errorf("malformed sync response: %w", err)
			}
		}
		return respEvents, nil
	case <-time.After(15 * time.Second):
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("sync request timed out (15s)")
	}
}

// syncLoop initiates sync periodically in the background. Exits immediately with zero
// overhead when SYNC_UDS_SOCK is not configured.
func syncLoop() {
	if !syncEnabled() {
		return
	}
	initSyncState()
	udsClient = newSingleUDSClient()
	log.Printf("[sync] 单 UDS (%s) 桥接双活同步启动，微批间隔 %v", syncUDSSock, flushSec)

	for {
		time.Sleep(flushSec)

		local := getLocalSeq()
		syncMu.Lock()
		if peerAckSeq >= local {
			syncMu.Unlock()
			continue // zero idle traffic: no new events, don't send packets
		}
		start := peerAckSeq
		syncMu.Unlock()

		events := readLocalEventsRange(start)
		if len(events) == 0 {
			continue
		}

		respEvents, err := udsClient.doSync(events)
		if err != nil {
			log.Printf("[sync] UDS 传输异常 (将在下一轮重试): %v", err)
			continue
		}

		if len(respEvents) > 0 {
			applyRemoteBatch(respEvents)
		}

		syncMu.Lock()
		if len(events) > 0 {
			peerAckSeq = events[len(events)-1].Seq
			persistSyncStateLocked()
		}
		syncMu.Unlock()
	}
}

// applyRemoteBatch applies peer events in batch by (origin, seq). Pure in-memory
// projection; no fsync stalls.
func applyRemoteBatch(events []Event) {
	if len(events) == 0 {
		return
	}

	eventLogMu.Lock()
	defer eventLogMu.Unlock()

	syncMu.Lock()
	defer syncMu.Unlock()

	maxSeen := peerSeenSeq
	for _, evt := range events {
		if evt.Origin == localOrigin {
			continue
		}
		if evt.Seq <= peerSeenSeq {
			continue // idempotent: already applied, drop
		}
		appendRemoteEvent(&evt)
		if evt.Seq > maxSeen {
			maxSeen = evt.Seq
		}
	}

	if maxSeen > peerSeenSeq {
		peerSeenSeq = maxSeen
		persistSyncStateLocked()
	}
}
