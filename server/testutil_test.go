package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestFile writes body to path, creating the parent directories.
func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// swapPostIndex replaces the post index for one test and restores it afterwards.
func swapPostIndex(t *testing.T, posts []Post) {
	t.Helper()
	postIdx.mu.Lock()
	savedList, savedByID := postIdx.list, postIdx.byID
	postIdx.list, postIdx.byID = posts, nil
	postIdx.mu.Unlock()
	t.Cleanup(func() {
		postIdx.mu.Lock()
		postIdx.list, postIdx.byID = savedList, savedByID
		postIdx.mu.Unlock()
	})
}
