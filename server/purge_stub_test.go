package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordingOutlet stands in for the outlet a build compiles in: it appends the URLs it is handed
// to a file, so a test reads back what the flush sent, and counts its own invocations so a test
// can pin how many calls the edge was charged for.
type recordingOutlet struct {
	ready  bool
	record string // empty discards
	err    error
	calls  int
}

func (o *recordingOutlet) Ready() bool { return o.ready }

func (o *recordingOutlet) Purge(urls []string) (sent bool, err error) {
	o.calls++
	if o.err != nil {
		return false, o.err
	}
	if o.record == "" {
		return true, nil
	}
	f, err := os.OpenFile(o.record, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(urls, "\n") + "\n"); err != nil {
		return false, err
	}
	return true, nil
}

// stubOutlet replaces the build's outlet for the rest of the test.
func stubOutlet(t *testing.T, o outlet) {
	t.Helper()
	previous := getPurgeOutlet()
	setPurgeOutlet(o)
	t.Cleanup(func() { setPurgeOutlet(previous) })
}

// stubOutletRecord is the common case: a ready outlet that records what it receives.
func stubOutletRecord(t *testing.T, record string) *recordingOutlet {
	t.Helper()
	o := &recordingOutlet{ready: true, record: record}
	stubOutlet(t, o)
	return o
}

// errEdgeRefused stands for an edge that rejected the request.
var errEdgeRefused = errors.New("the edge refused")

// No outlet is not a failure: it means this build has no edge to tell. Reporting it as one would
// make every build without a compiled-in outlet log an error on every change.
func TestRunPurgeOutletSkipsWithoutAnOutlet(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	stubOutlet(t, nil)

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Errorf("an absent outlet is a skip, not a failure: %v", err)
	}
}

// An outlet that is not ready counts as absent: the deployment has not given it what it needs.
func TestRunPurgeOutletSkipsWhenNotReady(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	o := &recordingOutlet{}
	stubOutlet(t, o)

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Errorf("an unconfigured outlet is a skip, not a failure: %v", err)
	}
	if o.calls != 0 {
		t.Errorf("the outlet ran %d time(s) while not ready", o.calls)
	}
}

func TestRunPurgeOutletSkipsNothingToDo(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "got.txt")
	stubOutletRecord(t, record)

	if _, err := runPurgeOutlet(nil); err != nil {
		t.Fatalf("runPurgeOutlet(nil): %v", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the outlet was run for an empty URL set")
	}
}

// A development run must not reach a real account.
func TestRunPurgeOutletSkipsInDevMode(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "1")
	record := filepath.Join(t.TempDir(), "got.txt")
	stubOutletRecord(t, record)

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Fatalf("runPurgeOutlet: %v", err)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("DEV_MODE=1 still ran the outlet")
	}
}

func TestRunPurgeOutletReportsAFailure(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	stubOutlet(t, &recordingOutlet{ready: true, err: errEdgeRefused})

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err == nil {
		t.Error("a failing outlet was reported as success; the edge would keep the old pages " +
			"and nothing would say so")
	}
}

// The operator's brake stops the flush before any outlet sees it. It is the only skip whose cause
// is a decision rather than a missing capability, so it has to be told apart from them: the
// positive control is what makes the zero calls below mean the brake rather than a broken outlet.
func TestRunPurgeOutletSkipsWhenPaused(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	o := &recordingOutlet{ready: true}
	stubOutlet(t, o)

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Fatalf("runPurgeOutlet: %v", err)
	}
	if o.calls != 1 {
		t.Fatalf("the outlet ran %d time(s) with no pause marker, want 1", o.calls)
	}

	if err := setPurgePaused(true); err != nil {
		t.Fatalf("setPurgePaused: %v", err)
	}
	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Errorf("a paused node is a skip, not a failure: %v", err)
	}
	if o.calls != 1 {
		t.Errorf("the outlet ran while paused (%d call(s))", o.calls)
	}
}

func TestDedupeNonEmptyKeepsFirstOccurrenceOrder(t *testing.T) {
	got := dedupeNonEmpty([]string{"b", "", "a", "b", "c", "a"})
	want := []string{"b", "a", "c"}
	if len(got) != len(want) {
		t.Fatalf("dedupeNonEmpty() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupeNonEmpty() = %v, want %v", got, want)
		}
	}
}
