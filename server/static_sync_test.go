package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncPathSourceAcceptsCleanRelativePaths: the peer receives an entry as
// "<its own served root>/<entry>", so the accepted shape is exactly a clean relative path.
func TestSyncPathSourceAcceptsCleanRelativePaths(t *testing.T) {
	useExampleSite(t)

	for _, entry := range []string{"assets/thumb", "assets/stickers.json", "static", "static/photo"} {
		got, err := syncPathSource(entry)
		if err != nil {
			t.Errorf("syncPathSource(%q) = %v, want a path", entry, err)
			continue
		}
		want := filepath.Join(staticRoot, filepath.FromSlash(entry))
		if got != want {
			t.Errorf("syncPathSource(%q) = %q, want %q", entry, got, want)
		}
	}
}

// TestSyncPathSourceRefusesAnythingThatWouldMeanSomethingElseAtThePeer: an absolute entry or
// one that walks upwards names a path the peer would not agree with, and a setting that only
// almost works is worse than one that fails at startup.
func TestSyncPathSourceRefusesAnythingThatWouldMeanSomethingElseAtThePeer(t *testing.T) {
	useExampleSite(t)

	for _, entry := range []string{
		"",
		"/assets/thumb",
		"../assets/thumb",
		"assets/../../etc",
		"./assets/thumb",
		"assets/thumb/",
		"assets/../static",
	} {
		if got, err := syncPathSource(entry); err == nil {
			t.Errorf("syncPathSource(%q) = %q, want an error", entry, got)
		}
	}
}

// TestSplitEnvListDropsBlanks: a trailing comma or a stray space must not become an entry,
// or a run would try to push the served root itself.
func TestSplitEnvListDropsBlanks(t *testing.T) {
	got := splitEnvList(" assets/thumb , ,assets/stickers.json,")
	want := []string{"assets/thumb", "assets/stickers.json"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitEnvList = %v, want %v", got, want)
	}
	if n := len(splitEnvList("   ")); n != 0 {
		t.Errorf("splitEnvList of blanks = %d entries, want 0", n)
	}
}

// TestSyncTargetIsReachableAndRefusesWhenUnconfigured: the maintenance target exists, and a
// node with no face answers an error naming the setting rather than pushing anything.
func TestSyncTargetIsReachableAndRefusesWhenUnconfigured(t *testing.T) {
	useExampleSite(t)
	t.Setenv("SILPHUU_SYNC_PATHS", "")

	req := httptest.NewRequest("POST", "/commit/rebuild?target=sync", nil)
	req.Header.Set("X-Requested-With", "fetch")
	rec := httptest.NewRecorder()
	handleCommitRebuild(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("target=sync = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	// The message names the setting, so an operator sees what is missing rather than a silent
	// success.
	if body := rec.Body.String(); !strings.Contains(body, "SILPHUU_SYNC_PATHS") {
		t.Errorf("target=sync answered %q, want it to name the setting", body)
	}
}

// TestSyncPathResolutionDoesNotDependOnTheWorkingDirectory: entries resolve against the
// served root, not the process directory, or a run would push from wherever the server was
// started.
func TestSyncPathResolutionDoesNotDependOnTheWorkingDirectory(t *testing.T) {
	useExampleSite(t)
	if err := os.MkdirAll(filepath.Join(staticRoot, "assets", "thumb"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := syncPathSource("assets/thumb")
	if err != nil {
		t.Fatalf("syncPathSource: %v", err)
	}
	if !strings.HasPrefix(got, filepath.Clean(staticRoot)+string(filepath.Separator)) {
		t.Errorf("resolved %q, which is not under the served root %q", got, staticRoot)
	}
}
