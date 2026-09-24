package main

import (
	"os"
	"strings"
	"testing"
)

// TestTemplatesCarryNoPageNodeID: no IDE-injected data-page-node-id in templates.
func TestTemplatesCarryNoPageNodeID(t *testing.T) {
	const needle = "data-page-node-id"

	files := guardWalk(t, []string{"templates"}, ".html")
	if len(files) == 0 {
		t.Fatal("no template files found — a guard that scans nothing always passes")
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if n := strings.Count(string(raw), needle); n > 0 {
			t.Errorf("%s: %d occurrence(s) of %s — a page preview wrote this file back; strip it before committing", f, n, needle)
		}
	}
}
