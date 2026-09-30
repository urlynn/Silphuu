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
	opBlobPush uint8 = 3 // blob face request (files, or a manifest)
	opBlobResp uint8 = 4 // blob face reply (JSON)
)

// syncIOTimeout bounds one event round trip.
const syncIOTimeout = 15 * time.Second

var (
	syncCfgOnce sync.Once
	syncMu      sync.Mutex
	// peerAck is how far the peer holds each origin. The peer's own report is the authority,
	// so it is assigned rather than raised: a report that falls back has to take the ack with
	// it, or the records it no longer holds are never offered again.
	peerAck = map[string]uint64{}
	// peerSeen is how far this node holds each origin, derived from the event log at startup.
	// A watermark below what the log holds would re-apply records this node already has, and
	// the log is the record of what was applied, so nothing has to be persisted to keep the
	// two in step. Initialised here so a projection can land a record before syncLoop starts.
	peerSeen    = map[string]uint64{}
	flushSec    time.Duration
	syncUDSSock string

	// blobPending is set while a blob push has failed and the face has not been reconciled
	// since. It is persisted because a restart produces no event and kicks nothing: without it
	// the bytes a failed push left behind would never be offered again. One bit is enough — the
	// reconciliation asks the peer what it lacks, which covers a crash and a hand edit as well
	// as a push this node remembers failing.
	blobPending bool

	// Wakes syncLoop the moment a local event is committed, so the peer hears about it
	// without waiting out the flush window. Buffered, and written with a non-blocking send:
	// a full buffer means a wakeup is already pending, and the flush that follows reads every
	// event up to the current seq, so a wakeup is never lost.
	syncKick = make(chan struct{}, 1)

	udsClient *singleUDSClient
)

// syncBatchEvents bounds one frame. A backlog drains over several round trips instead of one
// frame holding the whole log, and the batch size is what keeps a first exchange after a long
// gap from blocking the loop on a single write.
const syncBatchEvents = 128

// syncReply is what one node answers a sync request with: the records it holds that the sender
// may not, and the watermarks it holds, keyed by origin. The watermarks are the acknowledgement
// — a sender learns from them what landed, which writing a frame cannot tell it.
type syncReply struct {
	Events []Event           `json:"events"`
	Seen   map[string]uint64 `json:"seen,omitempty"`
}

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
	// Read the log before taking this lock: scanEvents takes the log's own, and the two are
	// only ever taken in one order across this file.
	marks := map[string]uint64{}
	_ = scanEvents(func(evt *Event) bool {
		if evt.Seq > marks[evt.Origin] {
			marks[evt.Origin] = evt.Seq
		}
		return true
	})

	syncMu.Lock()
	defer syncMu.Unlock()
	peerAck = map[string]uint64{}
	peerSeen = marks
	// A truncated log no longer holds the records the snapshot was taken at, so the scan
	// above cannot see them. Without this floor the node would report a lower watermark than
	// it really holds, the peer would re-deliver, and a like applied twice would count twice.
	for origin, seq := range eventBaseline {
		if seq > peerSeen[origin] {
			peerSeen[origin] = seq
		}
	}
	if err := os.MkdirAll(filepath.Dir(FileSyncState), 0o755); err != nil {
		return
	}
	data, err := os.ReadFile(FileSyncState)
	if err != nil {
		return
	}
	var st struct {
		PeerAck     map[string]uint64 `json:"peer_ack"`
		BlobPending bool              `json:"blob_pending,omitempty"`
	}
	if json.Unmarshal(data, &st) != nil {
		return // a state file in an older shape: the peer's next report rebuilds it
	}
	for origin, seq := range st.PeerAck {
		peerAck[origin] = seq
	}
	blobPending = st.BlobPending
}

func persistSyncStateLocked() {
	data, _ := json.Marshal(map[string]any{
		"peer_ack":     peerAck,
		"blob_pending": blobPending,
	})
	tmp := FileSyncState + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, FileSyncState)
	}
}

// markBlobPending records that a blob push failed. It reports whether this is the first failure
// of the streak, so a caller can tell the administrator once about an incident rather than once
// per upload.
func markBlobPending() bool {
	syncMu.Lock()
	defer syncMu.Unlock()
	if blobPending {
		return false
	}
	blobPending = true
	persistSyncStateLocked()
	return true
}

// clearBlobPending records that the face has been reconciled and nothing is outstanding.
func clearBlobPending() {
	syncMu.Lock()
	defer syncMu.Unlock()
	if !blobPending {
		return
	}
	blobPending = false
	persistSyncStateLocked()
}

// blobPendingSet reports whether a blob push has failed and is waiting for a reconciliation.
func blobPendingSet() bool {
	syncMu.Lock()
	defer syncMu.Unlock()
	return blobPending
}

// pendingEvents lists the records the peer has not confirmed, in log order, up to limit.
//
// It carries every origin this node holds rather than only its own: a record that arrived here
// and nowhere else still has to reach the peer, and a record the peer already holds costs one
// comparison to drop. That is also what makes the channel independent of the node's name —
// which origin a record carries stops deciding whether it travels.
//
// The scan stops as soon as it has limit records rather than reading the log whole: this runs
// on every exchange, so its cost has to stay independent of how long the log has grown.
func pendingEvents(ack map[string]uint64, limit int) []Event {
	var out []Event
	_ = scanEvents(func(evt *Event) bool {
		if evt.Seq <= ack[evt.Origin] {
			return true
		}
		out = append(out, *evt)
		return !(limit > 0 && len(out) >= limit)
	})
	return out
}

// seenSnapshot copies the watermarks this node holds, for the reply that reports them.
func seenSnapshot() map[string]uint64 {
	syncMu.Lock()
	defer syncMu.Unlock()
	out := make(map[string]uint64, len(peerSeen))
	for origin, seq := range peerSeen {
		out[origin] = seq
	}
	return out
}

// ackSnapshot copies the peer's report, for reading without holding the lock.
func ackSnapshot() map[string]uint64 {
	syncMu.Lock()
	defer syncMu.Unlock()
	out := make(map[string]uint64, len(peerAck))
	for origin, seq := range peerAck {
		out[origin] = seq
	}
	return out
}

// nodeIdentity is this node's name, the one its events carry. It answers before the event store
// has opened, when the name is still unset.
func nodeIdentity() string {
	if localOrigin != "" {
		return localOrigin
	}
	return nodeName()
}

// peerName is the peer's node name, read back from the origins in the watermarks this node
// holds. It is empty until an event has crossed in either direction: the peer is addressed by
// the daemon's own setting, which this process does not read.
func peerName() string {
	syncMu.Lock()
	defer syncMu.Unlock()
	for _, marks := range []map[string]uint64{peerAck, peerSeen} {
		for origin := range marks {
			if origin != localOrigin {
				return origin
			}
		}
	}
	return ""
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
		case opSyncResp, opBlobResp:
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
		case opBlobPush:
			// Peer pushed the bytes behind a record. Handled off the read loop so a write to
			// disk never delays the next frame on this socket.
			go c.handleIncomingBlob(reqID, payload)
		}
	}
}

func (c *singleUDSClient) handleIncomingReq(reqID uint64, payload []byte) {
	var reqEvents []Event
	if err := json.Unmarshal(payload, &reqEvents); err == nil && len(reqEvents) > 0 {
		applyRemoteBatch(reqEvents)
	}

	// The reply carries this node's records and the watermarks it holds. Writing the frame is
	// not a delivery, so nothing here moves the ack: the peer learns what landed from the
	// watermarks, and only the peer's own report can say the same about its records.
	respPayload, _ := json.Marshal(syncReply{
		Events: pendingEvents(ackSnapshot(), syncBatchEvents),
		Seen:   seenSnapshot(),
	})
	if err := c.sendFrame(opSyncResp, reqID, respPayload); err != nil {
		log.Printf("[sync] 回复对端失败: %v", err)
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

func (c *singleUDSClient) doSync(events []Event) (syncReply, error) {
	payload, err := json.Marshal(events)
	if err != nil {
		return syncReply{}, err
	}

	respPayload, err := c.request(opSyncReq, payload, syncIOTimeout)
	if err != nil {
		return syncReply{}, err
	}
	if len(respPayload) == 0 {
		return syncReply{}, nil
	}

	var reply syncReply
	if err := json.Unmarshal(respPayload, &reply); err != nil {
		// A peer from before the report answers with a bare array: its records are usable and
		// its confirmation is not, so the batch stays unacknowledged and is offered again.
		var events []Event
		if err := json.Unmarshal(respPayload, &events); err != nil {
			return syncReply{}, fmt.Errorf("malformed sync response: %w", err)
		}
		return syncReply{Events: events}, nil
	}
	return reply, nil
}

// handleIncomingBlob answers a blob frame the peer pushed, and returns the reply on the same
// request id.
func (c *singleUDSClient) handleIncomingBlob(reqID uint64, payload []byte) {
	if err := c.sendFrame(opBlobResp, reqID, handleBlobPush(payload)); err != nil {
		log.Printf("[blob sync] 回复对端失败: %v", err)
	}
}

// request sends one frame and waits for the reply carrying the same request id. The timeout is
// the caller's, because an event batch and a blob batch are not the same size of thing.
func (c *singleUDSClient) request(op uint8, payload []byte, timeout time.Duration) ([]byte, error) {
	if err := c.ensureConn(); err != nil {
		return nil, err
	}

	reqID := atomic.AddUint64(&c.nextReqID, 1)
	respCh := make(chan []byte, 1)

	c.pendingMu.Lock()
	c.pending[reqID] = respCh
	c.pendingMu.Unlock()

	if err := c.sendFrame(op, reqID, payload); err != nil {
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
		return respPayload, nil
	case <-time.After(timeout):
		c.pendingMu.Lock()
		delete(c.pending, reqID)
		c.pendingMu.Unlock()
		return nil, fmt.Errorf("sync request timed out (%v)", timeout)
	}
}

// kickSync wakes syncLoop so a freshly committed event crosses without waiting out the flush
// window. It never blocks: the loop may not be running at all (SYNC_UDS_SOCK unset), in which
// case the buffer simply stays full.
func kickSync() {
	select {
	case syncKick <- struct{}{}:
	default:
	}
}

// syncLoop pushes local events to the peer. A local commit wakes it at once; the flush window
// is the backstop, for a push that failed and has to be retried. Exits immediately with zero
// overhead when SYNC_UDS_SOCK is not configured.
func syncLoop() {
	if !syncEnabled() {
		return
	}
	initSyncState()
	udsClient = newSingleUDSClient()

	// The peer delivers its own events through this same socket, so the client has to be up
	// even when this node has nothing to push. A local socket held open is not network
	// traffic, which is what the idle short-circuit below is about.
	if err := udsClient.ensureConn(); err != nil {
		log.Printf("[sync] UDS 连接失败 (将在下一轮重试): %v", err)
	}
	log.Printf("[sync] 单 UDS (%s) 桥接双活同步启动，本地提交即推送，兜底间隔 %v", syncUDSSock, flushSec)

	timer := time.NewTimer(flushSec)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
		case <-syncKick:
			// Drain a timer that fired in the same instant, so the window restarted below
			// measures from this flush rather than from a tick already spent.
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		timer.Reset(flushSec)

		// Re-dial after a drop: a client that is down cannot receive either.
		if err := udsClient.ensureConn(); err != nil {
			log.Printf("[sync] UDS 连接失败 (将在下一轮重试): %v", err)
			continue
		}

		// A push that failed leaves its bytes for this loop to offer again. It is the same
		// entry the local commit uses, so a node that produces nothing new still converges —
		// and a node whose peer is unreachable retries without a timer of its own.
		if blobPendingSet() && blobSyncEnabled() {
			if _, err := reconcileFace(); err != nil {
				log.Printf("[blob sync] 补投失败 (下一轮重试): %v", err)
			} else {
				clearBlobPending()
			}
		}

		events := pendingEvents(ackSnapshot(), syncBatchEvents+1)
		if len(events) == 0 {
			continue // zero idle traffic: everything is confirmed, don't send packets
		}
		more := len(events) > syncBatchEvents
		if more {
			events = events[:syncBatchEvents]
		}

		reply, err := udsClient.doSync(events)
		if err != nil {
			log.Printf("[sync] UDS 传输异常 (将在下一轮重试): %v", err)
			continue
		}

		if len(reply.Events) > 0 {
			applyRemoteBatch(reply.Events)
		}

		// A batch left over is worth sending at once rather than waiting out the window, but
		// only when the peer's report moved: a peer that confirms nothing leaves the backlog to
		// the flush timer instead of turning this into a loop.
		if more && advancePeerAck(reply.Seen) {
			kickSync()
		}
	}
}

// advancePeerAck records the watermarks the peer reported and reports whether any moved.
//
// The report is assigned rather than merged upward: it is the peer's statement about its own
// log, and a log that lost records has to be able to lower the ack, or the records it no longer
// holds are never offered again.
func advancePeerAck(seen map[string]uint64) bool {
	if len(seen) == 0 {
		return false
	}
	moved := false
	syncMu.Lock()
	defer syncMu.Unlock()
	for origin, seq := range seen {
		if peerAck[origin] != seq {
			peerAck[origin] = seq
			moved = true
		}
	}
	if moved {
		persistSyncStateLocked()
	}
	return moved
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

	for _, evt := range events {
		if evt.Origin == localOrigin {
			continue
		}
		if evt.Seq <= peerSeen[evt.Origin] {
			continue // idempotent: already applied, drop
		}
		if err := appendRemoteEvent(&evt); err != nil {
			// The watermark must not pass a record the log does not hold: the peer reads the
			// watermark as "applied" and never offers that record again. Stopping here leaves
			// the rest of the batch to the next delivery.
			log.Printf("[sync] 记录 %s/%d 落盘失败: %v", evt.Origin, evt.Seq, err)
			break
		}
		peerSeen[evt.Origin] = evt.Seq
	}
}
