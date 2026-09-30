//go:build !esa

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The command outlet's contract: the URLs arrive on stdin, one per line, and the exit status is
// the verdict. Both halves are pinned here because a script written against the wrong shape
// would fail on the edge and nowhere else.
func TestShellOutletHandsTheURLsOverOnStdin(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	record := filepath.Join(t.TempDir(), "got.txt")
	t.Setenv(envPurgeCmd, "cat > "+record)

	// Repeats and blanks are dropped before the command sees them: the command's own quota is
	// usually counted in URLs, so sending one twice costs twice.
	if _, err := runPurgeOutlet([]string{"https://a/1", "https://a/2", "https://a/1", ""}); err != nil {
		t.Fatalf("runPurgeOutlet: %v", err)
	}

	got, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the command was not run: %v", err)
	}
	const want = "https://a/1\nhttps://a/2\n"
	if string(got) != want {
		t.Errorf("stdin was %q, want %q", got, want)
	}
}

func TestShellOutletReportsAFailingCommand(t *testing.T) {
	withIsolatedRoots(t)
	t.Setenv("DEV_MODE", "")
	t.Setenv(envPurgeCmd, "cat >/dev/null; exit 3")

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err == nil {
		t.Error("a failing command was reported as success; the edge would keep the old pages " +
			"and nothing would say so")
	}
}

// No command is not a failure: it means this deployment has no edge to tell. Reporting it as
// one would make every dev run and every single-node deployment log an error on every change.
func TestShellOutletSkipsWithoutACommand(t *testing.T) {
	t.Setenv("DEV_MODE", "")
	t.Setenv(envPurgeCmd, "")

	if _, err := runPurgeOutlet([]string{"https://a/1"}); err != nil {
		t.Errorf("an unconfigured outlet is a skip, not a failure: %v", err)
	}
}

func TestPurgeTimeoutReadsSeconds(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", defaultPurgeTimeout},
		{"30", 30 * time.Second},
		{"1", time.Second},
		{"0", defaultPurgeTimeout},
		{"-5", defaultPurgeTimeout},
		{"abc", defaultPurgeTimeout},
	} {
		t.Setenv(envPurgeTimeout, tc.value)
		if got := purgeTimeout(); got != tc.want {
			t.Errorf("purgeTimeout() with %q = %v, want %v", tc.value, got, tc.want)
		}
	}
}
