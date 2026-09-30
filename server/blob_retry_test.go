package main

import "testing"

// TestAFailedPushIsRecordedAndToldToTheAdministrator covers what a failed blob push has to leave
// behind: the bytes stay outstanding, and an operator hears about it once.
//
// It fails when a failure is only logged, which is the state the retry exists to end.
func TestAFailedPushIsRecordedAndToldToTheAdministrator(t *testing.T) {
	useExampleSite(t)
	t.Setenv("SILPHUU_SYNC_PATHS", "assets/thumb")

	prevClient, prevMagick := udsClient, magickCmd
	udsClient, magickCmd = newSingleUDSClient(), "magick"
	t.Cleanup(func() { udsClient, magickCmd = prevClient, prevMagick })
	t.Cleanup(clearBlobPending)
	clearBlobPending()

	// The client has no connection, so the push fails before it reaches the peer.
	if err := commitBlobReady(PageAbout, []string{"assets/thumb/x.avif"}); err != nil {
		t.Fatalf("commitBlobReady: %v", err)
	}
	if !blobPendingSet() {
		t.Error("a failed push left nothing behind, so nothing will offer the bytes again")
	}
}

// TestAFailedPushIsRememberedAcrossARestart covers the one piece of the retry that has to be on
// disk: a restart produces no event and kicks nothing, so an in-memory flag would lose the bytes
// a failed push left behind.
func TestAFailedPushIsRememberedAcrossARestart(t *testing.T) {
	useExampleSite(t)
	// The flag is package state, so it outlives a test that set it.
	clearBlobPending()

	if !markBlobPending() {
		t.Fatal("the first failure of a streak was not recorded")
	}
	if markBlobPending() {
		t.Error("a second failure in the same streak reported itself as the first")
	}

	// A restart: the variables are re-read from the file they were written to.
	peerAck = map[string]uint64{}
	blobPending = false
	initSyncState()

	if !blobPendingSet() {
		t.Error("a restart lost the outstanding push")
	}
	clearBlobPending()
	if blobPendingSet() {
		t.Error("clearing the flag left it set")
	}
}
