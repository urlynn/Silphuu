package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestOverlayRelPathPrefersDeployment covers the single overlay rule: a file
// present in the deployment wins, anything else falls back to the release copy.
// There is no merging — the resolved file is used as-is.
func TestOverlayRelPathPrefersDeployment(t *testing.T) {
	home := t.TempDir()
	InitRoots(home)
	t.Cleanup(func() { InitRoots("") })

	// Absent from the deployment -> release copy.
	if got := overlayRelPath("templates/navbar.html"); got != filepath.Join("templates", "navbar.html") {
		t.Errorf("overlayRelPath(absent) = %q, want the release path", got)
	}

	// Present in the deployment -> deployment copy wins.
	ownDir := filepath.Join(home, "templates")
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ownDir, "navbar.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := filepath.Join(home, "templates", "navbar.html")
	if got := templatePath("navbar.html"); got != want {
		t.Errorf("templatePath(overridden) = %q, want %q", got, want)
	}
}

// TestTemplateNamesUnionsBothLayers: a deployment can add new page templates, and
// the union must not list a name twice when it overrides an existing one.
func TestTemplateNamesUnionsBothLayers(t *testing.T) {
	home := t.TempDir()
	ownDir := filepath.Join(home, "templates")
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, n := range []string{"navbar.html", "my_custom_page.html"} {
		if err := os.WriteFile(filepath.Join(ownDir, n), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	if err := os.WriteFile(filepath.Join(ownDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}

	InitRoots(home)
	t.Cleanup(func() { InitRoots("") })

	names, err := templateNames()
	if err != nil {
		t.Fatalf("templateNames: %v", err)
	}

	counts := map[string]int{}
	for _, n := range names {
		counts[n]++
	}
	if counts["navbar.html"] != 1 {
		t.Errorf("navbar.html listed %d times, want exactly 1 (overridden, not duplicated)", counts["navbar.html"])
	}
	if counts["my_custom_page.html"] != 1 {
		t.Errorf("a deployment-added template was not picked up: %d", counts["my_custom_page.html"])
	}
	if counts["notes.txt"] != 0 {
		t.Errorf("a non-.html file was listed as a template")
	}
	if counts["status.html"] != 1 {
		t.Errorf("release template status.html missing from the union")
	}
	if !reflect.DeepEqual(names, sortedCopy(names)) {
		t.Errorf("templateNames must be sorted for deterministic parse order: %v", names)
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
