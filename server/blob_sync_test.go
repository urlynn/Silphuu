package main

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setFace points the sync face at these entries for the duration of one test.
func setFace(t *testing.T, entries ...string) {
	t.Helper()
	t.Setenv("SILPHUU_SYNC_PATHS", strings.Join(entries, ","))
}

// asProducer makes this node the writer of the face for the duration of one test. The media
// toolchain is the signal in production, and a test machine need not have it.
func asProducer(t *testing.T) {
	t.Helper()
	prev := magickCmd
	magickCmd = "convert"
	t.Cleanup(func() { magickCmd = prev })
}

// pushReply runs one payload through the receiving side and decodes the answer.
func pushReply(t *testing.T, payload []byte) blobPushReply {
	t.Helper()
	var reply blobPushReply
	if err := json.Unmarshal(handleBlobPush(payload), &reply); err != nil {
		t.Fatalf("the reply is not JSON: %v", err)
	}
	return reply
}

// TestBlobRecordsLandOnTheFace: a batch writes the files it names, and only those that are on
// the face. A path inside the served root but outside the face is still not this node's to hold.
//
// The face here is the deployment's, a directory entry and a file entry together, so this also
// pins that an entry naming a file is written at that exact path.
func TestBlobRecordsLandOnTheFace(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb", "assets/stickers.json")

	payload := encodeBlobRecords([]blobRecord{
		{Path: "assets/thumb/photo/blur/aaaa0001.avif", Data: []byte("thumb-bytes")},
		{Path: "assets/stickers.json", Data: []byte("index-bytes")},
		{Path: "static/photo/off-face.avif", Data: []byte("not this node's to hold")},
		{Path: "assets/themes.json", Data: []byte("not on this face either")},
	})

	reply := pushReply(t, payload)
	if reply.Written != 2 {
		t.Fatalf("written = %d, want 2 (errors: %v)", reply.Written, reply.Errors)
	}

	for _, want := range []struct{ path, body string }{
		{"assets/thumb/photo/blur/aaaa0001.avif", "thumb-bytes"},
		{"assets/stickers.json", "index-bytes"},
	} {
		got, err := os.ReadFile(filepath.Join(staticRoot, filepath.FromSlash(want.path)))
		if err != nil || string(got) != want.body {
			t.Errorf("%s = %q, %v; want the pushed bytes", want.path, got, err)
		}
	}
	for _, off := range []string{"static/photo/off-face.avif", "assets/themes.json"} {
		if _, err := os.Stat(filepath.Join(staticRoot, filepath.FromSlash(off))); err == nil {
			t.Errorf("%s was written, but it is not on the face", off)
		}
	}
}

// TestBlobRecordsRefuseEveryPathThatWouldEscapeTheFace: the path arrives from the peer, so it is
// input. Each of these names something the sender must not be able to reach, and this check is
// what stands between the two nodes — removing it has to turn this test red.
func TestBlobRecordsRefuseEveryPathThatWouldEscapeTheFace(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	escaped := filepath.Join(filepath.Dir(filepath.Clean(staticRoot)), "escaped.txt")

	for _, entry := range []string{
		"../escaped.txt",
		"assets/thumb/../../escaped.txt",
		"assets/thumb/../../../escaped.txt",
		"/etc/passwd",
		"./assets/thumb/x.avif",
		"assets/thumb/",
		"assets/../static/image/x.avif",
		"static/image/x.avif",
		"assets/thumbX/y.avif",
		"",
	} {
		payload := encodeBlobRecords([]blobRecord{{Path: entry, Data: []byte("nope")}})
		reply := pushReply(t, payload)
		if reply.Written != 0 {
			t.Errorf("%q was written, want a refusal", entry)
		}
		if len(reply.Errors) == 0 {
			t.Errorf("%q was refused silently, want an error naming it", entry)
		}
	}

	if _, err := os.Stat(escaped); err == nil {
		t.Errorf("%s exists, so a path escaped the served root", escaped)
	}
}

// TestBlobRecordsRefuseASymlinkedDirectory: a directory that links out of the served root must
// not become a way to write outside it.
func TestBlobRecordsRefuseASymlinkedDirectory(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	outside := t.TempDir()
	link := filepath.Join(staticRoot, "assets", "thumb", "photo")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	payload := encodeBlobRecords([]blobRecord{
		{Path: "assets/thumb/photo/escaped.avif", Data: []byte("nope")},
	})
	if reply := pushReply(t, payload); reply.Written != 0 {
		t.Errorf("written = %d, want a refusal", reply.Written)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.avif")); err == nil {
		t.Error("a symlinked directory let a write out of the served root")
	}
}

// TestBlobRecordsRefuseADirectoryTarget: a pushed path names a file. Replacing a directory would
// shadow every path under it, and a symlinked directory is where a rename replaces the link
// rather than failing, so both are refused.
func TestBlobRecordsRefuseADirectoryTarget(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	dir := filepath.Join(staticRoot, "assets", "thumb", "photo")
	if err := os.MkdirAll(filepath.Join(dir, "blur"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	kept := filepath.Join(dir, "blur", "kept.avif")
	if err := os.WriteFile(kept, []byte("kept"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	link := filepath.Join(staticRoot, "assets", "thumb", "linked")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	for _, target := range []string{"assets/thumb/photo", "assets/thumb/linked"} {
		payload := encodeBlobRecords([]blobRecord{{Path: target, Data: []byte("nope")}})
		reply := pushReply(t, payload)
		if reply.Written != 0 {
			t.Errorf("%s was replaced by a file, want a refusal", target)
		}
		if len(reply.Errors) == 0 {
			t.Errorf("%s was refused silently, want an error naming it", target)
		}
	}

	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("%s is no longer a symlink: %v, %v", link, info, err)
	}
	if got, err := os.ReadFile(kept); err != nil || string(got) != "kept" {
		t.Errorf("the directory's contents = %q, %v; want them untouched", got, err)
	}
}

// TestBlobRecordsRefuseAFileOverTheLimit: the limit is a guard, so it has to be enforced on the
// side that receives rather than trusted from the side that sends.
func TestBlobRecordsRefuseAFileOverTheLimit(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	payload := encodeBlobRecords([]blobRecord{
		{Path: "assets/thumb/photo/blur/big.avif", Data: make([]byte, maxBlobFile+1)},
	})
	reply := pushReply(t, payload)
	if reply.Written != 0 {
		t.Errorf("written = %d, want a refusal", reply.Written)
	}
	if len(reply.Errors) == 0 {
		t.Error("an oversized file was refused silently, want an error naming it")
	}
}

// TestBlobRecordsLeaveNoTemporaryFileBehind: the write lands through a rename, so nothing reads
// a partial file and nothing is left over.
func TestBlobRecordsLeaveNoTemporaryFileBehind(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	payload := encodeBlobRecords([]blobRecord{{Path: "assets/thumb/a.avif", Data: []byte("x")}})
	if reply := pushReply(t, payload); reply.Written != 1 {
		t.Fatalf("written = %d, want 1 (errors: %v)", reply.Written, reply.Errors)
	}

	left, err := filepath.Glob(filepath.Join(staticRoot, "assets", "thumb", "*.blobtmp"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("temporary files left behind: %v", left)
	}
}

// TestBlobManifestAsksOnlyForWhatDiffers: the digest is the comparison, so a file the receiver
// already holds byte for byte costs nothing, a file that changed in place at the same size is
// asked for, and a path off the face is never asked for.
func TestBlobManifestAsksOnlyForWhatDiffers(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	present := filepath.Join(staticRoot, "assets", "thumb", "held.avif")
	if err := os.MkdirAll(filepath.Dir(present), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(present, []byte("12345"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Same length as held.avif, different bytes: the case a size comparison reads as "already
	// held" and never revisits.
	changed := filepath.Join(staticRoot, "assets", "thumb", "changed.avif")
	if err := os.WriteFile(changed, []byte("abcde"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	presentSum, err := fileSHA256(present)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	// The bytes the sender holds for changed.avif: five long, and not what is on disk here.
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(elsewhere, []byte("zzzzz"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	changedSum, err := fileSHA256(elsewhere)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	body, err := json.Marshal([]blobManifestEntry{
		{Path: "assets/thumb/held.avif", Size: 5, Hash: presentSum},
		{Path: "assets/thumb/changed.avif", Size: 5, Hash: changedSum},
		{Path: "assets/thumb/different.avif", Size: 9},
		{Path: "assets/thumb/missing.avif", Size: 3},
		{Path: "static/photo/off-face.avif", Size: 1},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	reply := pushReply(t, append([]byte{blobSubManifest}, body...))
	want := strings.Join(reply.Want, "|")
	if strings.Contains(want, "held.avif") {
		t.Errorf("want = %v, want it to skip a file held at the same digest", reply.Want)
	}
	if !strings.Contains(want, "changed.avif") {
		t.Errorf("want = %v, want the file that changed in place at the same size", reply.Want)
	}
	if !strings.Contains(want, "different.avif") || !strings.Contains(want, "missing.avif") {
		t.Errorf("want = %v, want the differing and the missing file", reply.Want)
	}
	if strings.Contains(want, "off-face") {
		t.Errorf("want = %v, want no path off the face", reply.Want)
	}
}

// TestTheManifestIsClaimedOnlyForTheRootsThisNodeBuilds refuses a node that holds a copy from
// claiming the peer should hold it too: the manifest is a statement about what the peer's tree
// should be, and only a node that can rebuild a root is in a position to make it.
func TestTheManifestIsClaimedOnlyForTheRootsThisNodeBuilds(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb,assets/stickers.json")

	prevMagick := magickCmd
	magickCmd = ""
	t.Cleanup(func() { magickCmd = prevMagick })

	if err := os.MkdirAll(DirSticker, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", DirSticker, err)
	}

	if faceWriter("assets/thumb") {
		t.Error("a node without the media toolchain claims to ship the thumbnails")
	}
	if !faceWriter("assets/stickers.json") {
		t.Error("a node holding the sticker tree does not claim the index built from it")
	}
	if !faceWriter("assets/whatever.json") {
		t.Error("a content-addressed path was given an owner it does not need")
	}

	entries, err := faceManifest()
	if err != nil {
		t.Fatalf("faceManifest: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Path, "assets/thumb/") {
			t.Errorf("manifest claims %s, which this node cannot rebuild", e.Path)
		}
	}
}

// TestBlobPushOverTheUDSRoundTrips: the request opcode and the reply opcode are a pair, and the
// reply is matched back to the request that asked for it.
func TestBlobPushOverTheUDSRoundTrips(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")
	asProducer(t)

	tmpDir, err := os.MkdirTemp("", "uds_blob_*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "blob.sock")
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	seen := make(chan []byte, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		hdr := make([]byte, 13)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			t.Errorf("server read header: %v", err)
			return
		}
		pLen := binary.BigEndian.Uint32(hdr[0:4])
		op := hdr[4]
		reqID := binary.BigEndian.Uint64(hdr[5:13])
		if op != opBlobPush {
			t.Errorf("op = %d, want opBlobPush (%d)", op, opBlobPush)
		}

		payload := make([]byte, pLen)
		if _, err := io.ReadFull(conn, payload); err != nil {
			t.Errorf("server read payload: %v", err)
			return
		}
		seen <- payload

		reply, _ := json.Marshal(blobPushReply{Written: 1})
		frame := make([]byte, 13+len(reply))
		binary.BigEndian.PutUint32(frame[0:4], uint32(len(reply)))
		frame[4] = opBlobResp
		binary.BigEndian.PutUint64(frame[5:13], reqID)
		copy(frame[13:], reply)
		if _, err := conn.Write(frame); err != nil {
			t.Errorf("server write: %v", err)
		}
	}()

	client := newSingleUDSClient()
	prevClient, prevSock := udsClient, syncUDSSock
	udsClient, syncUDSSock = client, sockPath
	t.Cleanup(func() {
		client.closeConn()
		udsClient, syncUDSSock = prevClient, prevSock
	})

	if err := os.MkdirAll(filepath.Join(staticRoot, "assets", "thumb"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	thumb := filepath.Join(staticRoot, "assets", "thumb", "one.avif")
	if err := os.WriteFile(thumb, []byte("bytes"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Off the face: it must be dropped rather than shipped.
	offFace := filepath.Join(staticRoot, "assets", "stickers.json")
	if err := os.WriteFile(offFace, []byte("nope"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	n, err := pushBlobs([]string{thumb, offFace})
	if err != nil {
		t.Fatalf("pushBlobs: %v", err)
	}
	if n != 1 {
		t.Errorf("pushBlobs sent %d, want 1", n)
	}

	select {
	case payload := <-seen:
		records, err := decodeBlobRecords(payload)
		if err != nil {
			t.Fatalf("the frame the peer received is not a record batch: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("records = %+v, want exactly the one face file", records)
		}
		if records[0].Path != "assets/thumb/one.avif" || string(records[0].Data) != "bytes" {
			t.Errorf("record = %+v, want the face file and its bytes", records[0])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the peer never received the frame")
	}

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server timed out")
	}
}

// TestBlobSyncIsInertWithoutAFace: with no face configured nothing is pushed, and an incoming
// batch lands nowhere. The mechanism has to cost nothing until a deployment asks for it.
func TestBlobSyncIsInertWithoutAFace(t *testing.T) {
	useExampleSite(t)
	setFace(t)

	if blobSyncEnabled() {
		t.Error("blobSyncEnabled() = true with no face")
	}
	if _, err := pushBlobs([]string{"assets/thumb/x.avif"}); err == nil {
		t.Error("pushBlobs() with no face = nil error, want a refusal")
	}

	payload := encodeBlobRecords([]blobRecord{{Path: "assets/thumb/x.avif", Data: []byte("nope")}})
	if reply := pushReply(t, payload); reply.Written != 0 {
		t.Errorf("written = %d with no face, want 0", reply.Written)
	}
}

// TestBlobRecordsRefuseAnUnknownSubmode: a frame this node does not understand is an error, not
// something to guess at.
func TestBlobRecordsRefuseAnUnknownSubmode(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	for _, payload := range [][]byte{nil, {}, {0x7f, 0x01}} {
		reply := pushReply(t, payload)
		if reply.Written != 0 || len(reply.Errors) == 0 {
			t.Errorf("payload %v: reply = %+v, want a refusal", payload, reply)
		}
	}
}

// TestBlobRecordsRefuseATruncatedFrame: every length in a batch is checked against what is left,
// so a frame that stops early is an error rather than a panic.
func TestBlobRecordsRefuseATruncatedFrame(t *testing.T) {
	useExampleSite(t)
	setFace(t, "assets/thumb")

	full := encodeBlobRecords([]blobRecord{{Path: "assets/thumb/a.avif", Data: []byte("12345")}})

	// A frame carrying nothing but the submode is an empty batch, which is what the encoder
	// produces for no files at all.
	if records, err := decodeBlobRecords(full[:1]); err != nil || len(records) != 0 {
		t.Errorf("decodeBlobRecords(submode only) = %v, %v; want an empty batch", records, err)
	}
	for _, cut := range []int{2, 5, len(full) - 1} {
		if _, err := decodeBlobRecords(full[:cut]); err == nil {
			t.Errorf("decodeBlobRecords(truncated to %d bytes) = nil error, want a refusal", cut)
		}
	}
}
