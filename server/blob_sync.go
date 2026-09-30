package main

// blob_sync.go — the blob face: the bytes named by SILPHUU_SYNC_PATHS travel to the peer over
// the same WebTransport connection the events use.
//
// The event channel carries records; this carries the files those records name. A node renders
// the card as soon as it learns of the record, which is before any node has finished writing the
// bytes, so the two are separate on purpose — and what must not happen is a node that never
// receives them at all.
//
// A transfer is a batch of files in one frame. A single file is a batch of one; a reconciliation
// is the same shape with hundreds. One round trip either way, which is why there is no separate
// bulk path and no delta algorithm: the face has one writer, its names are content-addressed,
// and a file never changes in place.

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Submodes carried in the first byte of a blob payload.
const (
	blobSubRecords  uint8 = 0x00 // a batch of files
	blobSubManifest uint8 = 0x01 // the sender's face, asking what the receiver lacks
)

// maxBlobFile bounds one file. The face's largest is the sticker index at about 68 KB, so this
// is a guard against a peer asking for something enormous rather than a requirement.
const maxBlobFile = 16 << 20

// blobBatchBytes bounds one frame, so a reconciliation of the whole face does not have to be
// held in memory at once.
const blobBatchBytes = 8 << 20

// blobIOTimeout covers one blob round trip. Far longer than the event channel's: a frame can
// carry megabytes, and the receiver writes them to disk before it replies.
const blobIOTimeout = 120 * time.Second

// maxBlobReplyErrors bounds the error list a reply carries, so a peer that sends nothing usable
// cannot make this node build an unbounded string.
const maxBlobReplyErrors = 20

// blobRecord is one file in a batch.
type blobRecord struct {
	Path string
	Data []byte
}

// blobManifestEntry is one face entry as the sender sees it.
type blobManifestEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Hash is the content digest. Size alone cannot tell a file that changed in place from one
	// that did not, and the face holds one such file: the sticker index keeps its name while
	// its bytes move.
	Hash string `json:"hash,omitempty"`
}

// blobPushReply is what the receiver answers, for either submode. The fields are disjoint: a
// record batch reports what it wrote, a manifest reports what it wants.
type blobPushReply struct {
	Written int      `json:"written,omitempty"`
	Errors  []string `json:"errors,omitempty"`
	Want    []string `json:"want,omitempty"`
}

// blobSyncEnabled reports whether this node may ship the face: a face has to be configured, the
// event channel has to be up to carry it, and this node has to be the one that derives it.
//
// Receiving is not gated on any of this. A node that cannot ship still accepts what the writer
// sends; that is the whole point of the face.
func blobSyncEnabled() bool {
	return udsClient != nil && len(syncFacePaths()) > 0 && faceProducer()
}

// derivedFaceRoots names the roots this node builds itself, and what it has to hold to build
// them.
//
// The judgement is about the tree, not the machine. Two nodes with the media toolchain are both
// able to build a thumbnail, but only one of them holds the sticker tree the index is built
// from, so naming what a root is derived from is what decides who ships it — and a path whose
// name carries its own content needs no owner at all.
var derivedFaceRoots = []struct {
	Root     func() string
	CanBuild func() bool
}{
	{func() string { return DirThumbRoot }, func() bool { return magickCmd != "" }},
	{stickerIndexPath, func() bool { return isDir(DirSticker) }},
}

// faceWriter reports whether this node is the writer for one face path: it can build the root
// the path belongs to, or the path is content-addressed and belongs to nobody in particular.
func faceWriter(entry string) bool {
	full := filepath.Join(staticRoot, filepath.FromSlash(entry))
	for _, root := range derivedFaceRoots {
		if !insideTree(root.Root(), full) && filepath.Clean(full) != filepath.Clean(root.Root()) {
			continue
		}
		return root.CanBuild()
	}
	return true
}

// faceProducer reports whether this node ships any part of the face.
//
// The face has one writer per root rather than one writer: a node that can rebuild a root ships
// it, and a node that cannot only ever holds a copy. Reconciling from a copy would push that
// copy over the newer original, so being able to rebuild is what makes a node a writer.
func faceProducer() bool {
	entries := syncFacePaths()
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if faceWriter(entry) {
			return true
		}
	}
	return false
}

// onSyncFace reports whether a clean slash-separated path is inside the face. A face entry names
// either a file or a directory, and a directory covers everything under it.
func onSyncFace(entry string) bool {
	for _, p := range syncFacePaths() {
		if entry == p || strings.HasPrefix(entry, p+"/") {
			return true
		}
	}
	return false
}

// faceRelative turns a path a pipeline produced into a slash-separated path relative to the
// served root, so a caller can pass back whatever it was handed without knowing where the root
// is. Anything outside the served root is refused rather than normalised.
func faceRelative(p string) (string, error) {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		clean = filepath.Join(staticRoot, clean)
	}
	root := filepath.Clean(staticRoot)
	if clean != root && !strings.HasPrefix(clean, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the served root", p)
	}
	rel, err := filepath.Rel(root, clean)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// encodeBlobRecords lays out a batch: the submode byte, then a path and its bytes per file.
func encodeBlobRecords(records []blobRecord) []byte {
	size := 1
	for _, r := range records {
		size += 2 + len(r.Path) + 8 + len(r.Data)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, blobSubRecords)
	for _, r := range records {
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(r.Path)))
		buf = append(buf, r.Path...)
		buf = binary.BigEndian.AppendUint64(buf, uint64(len(r.Data)))
		buf = append(buf, r.Data...)
	}
	return buf
}

// decodeBlobRecords reads a batch back. Every length is checked against what is left before it
// is used, so a truncated or hostile frame is an error rather than a panic.
func decodeBlobRecords(payload []byte) ([]blobRecord, error) {
	if len(payload) == 0 || payload[0] != blobSubRecords {
		return nil, fmt.Errorf("not a blob record frame")
	}
	rest := payload[1:]
	var out []blobRecord
	for len(rest) > 0 {
		if len(rest) < 2 {
			return nil, fmt.Errorf("truncated path length")
		}
		n := int(binary.BigEndian.Uint16(rest[:2]))
		rest = rest[2:]
		if len(rest) < n+8 {
			return nil, fmt.Errorf("truncated path")
		}
		path := string(rest[:n])
		rest = rest[n:]
		size := binary.BigEndian.Uint64(rest[:8])
		rest = rest[8:]
		if uint64(len(rest)) < size {
			return nil, fmt.Errorf("truncated data for %q", path)
		}
		out = append(out, blobRecord{Path: path, Data: rest[:size]})
		rest = rest[size:]
	}
	return out, nil
}

// blobReplyJSON marshals a reply. A reply that cannot be marshalled still has to answer the
// peer, or the push waits out its timeout for no reason.
func blobReplyJSON(reply blobPushReply) []byte {
	data, err := json.Marshal(reply)
	if err != nil {
		return []byte(`{"errors":["reply could not be encoded"]}`)
	}
	return data
}

// blobTargetPath resolves an incoming path to a local file, refusing anything outside the face
// or outside the served root. The path arrives from the peer, so it is input, not configuration.
func blobTargetPath(entry string) (string, error) {
	if !onSyncFace(entry) {
		return "", fmt.Errorf("%q is not on the sync face", entry)
	}
	return syncPathSource(entry)
}

// realDirInsideServedRoot resolves symlinks on the deepest directory that already exists and
// checks it against the served root, so a symlinked directory cannot move a write out of the
// tree. The deepest existing ancestor is what is checked because the directory a new file needs
// may not exist yet, and creating it before the check would be the very thing being guarded.
func realDirInsideServedRoot(full string) bool {
	root := filepath.Clean(staticRoot)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	dir := filepath.Dir(full)
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return real == root || strings.HasPrefix(real, root+string(filepath.Separator))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// writeBlob lands one file through a rename, so a reader never sees a partial file.
func writeBlob(full string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".blobtmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// applyBlobRecords writes a batch, one path at a time. A path that fails is reported and the
// rest of the batch still lands: the peer repairs what it can and retries the remainder.
func applyBlobRecords(payload []byte) blobPushReply {
	records, err := decodeBlobRecords(payload)
	if err != nil {
		return blobPushReply{Errors: []string{err.Error()}}
	}

	var reply blobPushReply
	for _, rec := range records {
		if len(rec.Data) > maxBlobFile {
			reply.addError(fmt.Sprintf("%s: %d bytes exceeds the %d byte limit", rec.Path, len(rec.Data), maxBlobFile))
			continue
		}
		full, err := blobTargetPath(rec.Path)
		if err != nil {
			reply.addError(fmt.Sprintf("%s: %v", rec.Path, err))
			continue
		}
		if !realDirInsideServedRoot(full) {
			reply.addError(fmt.Sprintf("%s: resolves outside the served root", rec.Path))
			continue
		}
		// A pushed path names a file. Stat rather than Lstat, so a link to a directory counts as
		// one: renaming onto a link replaces the link, which would take the directory's contents
		// out of the served tree without any of them being touched.
		if info, err := os.Stat(full); err == nil && info.IsDir() {
			reply.addError(fmt.Sprintf("%s: target is a directory", rec.Path))
			continue
		}
		if err := writeBlob(full, rec.Data); err != nil {
			reply.addError(fmt.Sprintf("%s: %v", rec.Path, err))
			continue
		}
		reply.Written++
	}
	return reply
}

// addError records one refusal, up to the bound.
func (r *blobPushReply) addError(msg string) {
	if len(r.Errors) < maxBlobReplyErrors {
		r.Errors = append(r.Errors, msg)
	} else if len(r.Errors) == maxBlobReplyErrors {
		r.Errors = append(r.Errors, "further errors suppressed")
	}
}

// blobMissingFromManifest answers a manifest with the paths this node lacks or holds at
// different bytes.
//
// The comparison is the content digest, not the size. A file that keeps its name while its bytes
// change is on the face, and two versions of it can be the same length — the size alone would
// read the newer one as "already held" for good.
func blobMissingFromManifest(body []byte) blobPushReply {
	var entries []blobManifestEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return blobPushReply{Errors: []string{"malformed manifest"}}
	}

	var want []string
	for _, e := range entries {
		full, err := blobTargetPath(e.Path)
		if err != nil {
			continue // a path this node would not accept is not one it should ask for
		}
		if !blobMatches(full, e) {
			want = append(want, e.Path)
		}
	}
	return blobPushReply{Want: want}
}

// blobMatches reports whether this node already holds exactly the bytes the manifest names.
//
// A manifest without a digest comes from a sender that cannot say what its bytes are, and the
// size is then the whole of what can be compared.
func blobMatches(full string, e blobManifestEntry) bool {
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return false
	}
	if e.Hash == "" {
		return info.Size() == e.Size
	}
	sum, err := fileSHA256(full)
	return err == nil && sum == e.Hash
}

// handleBlobPush answers one blob request from the peer. The first payload byte is the submode;
// the reply is always JSON, so the caller can return it as it stands.
func handleBlobPush(payload []byte) []byte {
	if len(payload) == 0 {
		return blobReplyJSON(blobPushReply{Errors: []string{"empty blob payload"}})
	}
	switch payload[0] {
	case blobSubRecords:
		return blobReplyJSON(applyBlobRecords(payload))
	case blobSubManifest:
		return blobReplyJSON(blobMissingFromManifest(payload[1:]))
	default:
		return blobReplyJSON(blobPushReply{Errors: []string{"unknown blob submode"}})
	}
}

// sendBlobFrame pushes one frame to the peer and reads its reply.
func sendBlobFrame(payload []byte) (blobPushReply, error) {
	var reply blobPushReply
	if udsClient == nil {
		return reply, fmt.Errorf("sync channel is not running")
	}
	resp, err := udsClient.request(opBlobPush, payload, blobIOTimeout)
	if err != nil {
		return reply, err
	}
	if len(resp) == 0 {
		return reply, fmt.Errorf("peer answered nothing")
	}
	if err := json.Unmarshal(resp, &reply); err != nil {
		return reply, fmt.Errorf("malformed blob reply: %w", err)
	}
	return reply, nil
}

// pushBlobs sends the named files, filtered to the face, in batches bounded by blobBatchBytes.
// Paths outside the face, and paths this node is not the writer for, are skipped rather than
// refused: a pipeline names everything it wrote, and most of it is not this node's to ship.
func pushBlobs(paths []string) (int, error) {
	if !blobSyncEnabled() {
		return 0, fmt.Errorf("no sync face configured")
	}
	if !faceProducer() {
		return 0, fmt.Errorf("this node does not derive the sync face")
	}

	var batch []blobRecord
	var batchBytes int
	sent := 0

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		reply, err := sendBlobFrame(encodeBlobRecords(batch))
		if err != nil {
			return err
		}
		if len(reply.Errors) > 0 {
			return fmt.Errorf("peer refused %s", strings.Join(reply.Errors, "; "))
		}
		sent += reply.Written
		batch = nil
		batchBytes = 0
		return nil
	}

	for _, p := range paths {
		rel, err := faceRelative(p)
		if err != nil || !onSyncFace(rel) || !faceWriter(rel) {
			continue
		}
		full := filepath.Join(staticRoot, filepath.FromSlash(rel))
		data, err := os.ReadFile(full)
		if err != nil {
			return sent, fmt.Errorf("read %s: %w", rel, err)
		}
		if len(data) > maxBlobFile {
			return sent, fmt.Errorf("%s: %d bytes exceeds the %d byte limit", rel, len(data), maxBlobFile)
		}
		if batchBytes+len(data) > blobBatchBytes {
			if err := flush(); err != nil {
				return sent, err
			}
		}
		batch = append(batch, blobRecord{Path: rel, Data: data})
		batchBytes += len(data)
	}
	if err := flush(); err != nil {
		return sent, err
	}
	return sent, nil
}

// faceManifest lists the part of the face this node ships, as this node sees it.
//
// Only the roots this node can rebuild are listed. The manifest is a claim about what the peer
// should hold, and a node holding a copy is not in a position to make that claim.
func faceManifest() ([]blobManifestEntry, error) {
	var out []blobManifestEntry
	for _, entry := range syncFacePaths() {
		if !faceWriter(entry) {
			continue
		}
		full, err := syncPathSource(entry)
		if err != nil {
			return nil, err
		}
		if !fileExists(full) && !isDir(full) {
			continue // this node holds nothing for the entry yet, which is not a failure
		}
		err = filepath.Walk(full, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(staticRoot, p)
			if err != nil {
				return err
			}
			// A file that cannot be read cannot be claimed: an empty digest reads as "not the
			// bytes the peer holds", so the peer asks for it and the push reports the same
			// failure.
			sum, err := fileSHA256(p)
			if err != nil {
				sum = ""
			}
			out = append(out, blobManifestEntry{
				Path: filepath.ToSlash(rel),
				Size: info.Size(),
				Hash: sum,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reconcileFace makes the peer's copy of the face match this node's, without deleting: it sends
// the manifest, and pushes back whatever the peer reports it lacks. A file the peer holds at the
// same size costs nothing, so a reconciliation that finds nothing changed moves no bytes.
func reconcileFace() (int, error) {
	if !blobSyncEnabled() {
		return 0, fmt.Errorf("no sync face configured")
	}
	if !faceProducer() {
		return 0, fmt.Errorf("this node does not derive the sync face")
	}

	entries, err := faceManifest()
	if err != nil {
		return 0, err
	}
	body, err := json.Marshal(entries)
	if err != nil {
		return 0, err
	}

	reply, err := sendBlobFrame(append([]byte{blobSubManifest}, body...))
	if err != nil {
		return 0, err
	}
	if len(reply.Want) == 0 {
		return 0, nil
	}
	return pushBlobs(reply.Want)
}

// commitBlobReady ships the bytes behind a record and then records that they are on disk.
//
// The push comes first, so the peer holds the files by the time it invalidates the page. A push
// that fails is logged, recorded and the event still commits: the page is wrong either way, and
// the recorded failure is what makes the sync loop offer the bytes again.
func commitBlobReady(page string, paths []string) error {
	if blobSyncEnabled() && len(paths) > 0 {
		if _, err := pushBlobs(paths); err != nil {
			log.Printf("[blob sync] 推送失败 (下一次同步补上): %v", err)
			// The first failure of a streak is what an operator hears about: an unreachable peer
			// is one incident, not one per upload.
			if markBlobPending() {
				notifyAdmin(buildBlobFailedMessage(err, paths))
			}
		}
	}
	return CommitEvent(EvBlobReady, blobReadyPayload{Page: page, Paths: paths})
}

// buildBlobFailedMessage is the alert that the peer did not take the bytes. The chat form carries
// the count and not the list: the list is in Fields for a webhook, and a wall of paths is what an
// operator cannot read on a phone.
//
// The line names the node that pushed and, once an event has crossed, the node it pushed to. An
// operator reading two nodes' alerts has to be able to tell which end went quiet.
func buildBlobFailedMessage(err error, paths []string) Message {
	where := nodeIdentity()
	if peer := peerName(); peer != "" {
		where += " -> " + peer
	}
	return Message{
		Event:   EventBlobFailed,
		Subject: "边缘同步失败",
		Body: fmt.Sprintf("%s 推送失败：%v\n\n待推的字节已记账，下一次同步会补投；"+
			"也可以管理页手动触发一次。", where, err),
		Chat: fmt.Sprintf("%s 推送失败：%v\n\n%d 个文件待补投，下次同步重投", where, err, len(paths)),
		Fields: map[string]string{
			"paths": strings.Join(paths, " "),
		},
		Alert: true,
	}
}
