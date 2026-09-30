package main

import (
	"bytes"
	"os"
	"testing"
	"time"
)

// TestAPeersCommentReRendersTheGuestbook: a comment made on the sibling node has to reach this
// node's page, and the guestbook is the page that needs the render.
//
// The peer's event arrives through the sync batch with no handler of its own, so only the event
// path can ask for the render. The list is server-rendered into the page (guestbook.html ranges
// over the projection), so a page that is not re-rendered shows a list from before the change.
func TestAPeersCommentReRendersTheGuestbook(t *testing.T) {
	useExampleSite(t)
	rebuildPostIndex()
	wireTestMux(t)
	t.Setenv("DEV_MODE", "")

	cached := cachePath(PageGuestbook)
	sentinel := []byte("<html><body>rendered before the peer's comment</body></html>")
	writeTestFile(t, cached, string(sentinel))

	peer := localOrigin + "-peer"
	syncMu.Lock()
	seq := peerSeen[peer] + 1
	syncMu.Unlock()
	applyRemoteBatch([]Event{{
		ID:      "peercomment0000000000000001",
		Origin:  peer,
		Seq:     seq,
		Type:    EvCommentAdded,
		Payload: []byte(`{"id":"zzzz000000000001","nick":"peer","content":"from the peer","post_id":0}`),
	}})

	// The batch drops an event it has already seen, and a guard that never got past that filter
	// would pass by rendering nothing at all.
	if cmGet("zzzz000000000001") == nil {
		t.Fatal("the peer's event was not applied, so this guard tested nothing")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(cached)
		if err == nil && !bytes.Equal(body, sentinel) {
			if !bytes.Contains(body, []byte("</html>")) {
				t.Fatalf("the guestbook was replaced by something that is not a page: %.120s", body)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the guestbook was not re-rendered for the peer's comment")
}

// TestCommentEventPostIDNamesThePage: added and deleted carry the page in their payload, while a
// like names only the comment — so its page has to be read back from the projection. A like for
// a comment the projection does not hold names no page rather than a guessed one.
func TestCommentEventPostIDNamesThePage(t *testing.T) {
	useExampleSite(t)

	storeApply(testEvent(EvCommentAdded, `{"id":"yyyy000000000001","post_id":7}`))

	if id, ok := commentEventPostID(testEvent(EvCommentLiked, `{"id":"yyyy000000000001","delta":1}`)); !ok || id != 7 {
		t.Errorf("like page = %d (ok = %v), want 7 (ok = true)", id, ok)
	}
	if _, ok := commentEventPostID(testEvent(EvCommentLiked, `{"id":"yyyy000000000009"}`)); ok {
		t.Error("a like for a comment the projection does not hold named a page")
	}
	// The guestbook is page 0: a page, not an absent one.
	if id, ok := commentEventPostID(testEvent(EvCommentDeleted, `{"id":"yyyy000000000001","post_id":0}`)); !ok || id != 0 {
		t.Errorf("delete page = %d (ok = %v), want 0 (ok = true)", id, ok)
	}
	if _, ok := commentEventPostID(testEvent(EvBlobReady, `{"page":"/about"}`)); ok {
		t.Error("an event that is not a comment named a comment page")
	}
}
