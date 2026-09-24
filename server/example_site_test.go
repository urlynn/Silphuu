package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// useExampleSite points the roots at a private copy of the bundled example site and
// returns the copy's path. Tests that read or write site data call this so they never
// touch the checkout; engineRoot stays at the working directory.
func useExampleSite(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "silphuu-example-site")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	home := filepath.Join(dir, "site")
	if err := copyTree("../examples/demo", home); err != nil {
		t.Fatalf("copy example site: %v", err)
	}
	prevEngine := engineRoot
	engineRoot = "."
	InitRoots(home)
	t.Cleanup(func() {
		engineRoot = prevEngine
		InitRoots("")
		removeTreeRetry(home)
		removeTreeRetry(dir)
	})
	return home
}

// removeTreeRetry deletes path, retrying while a background writer recreates entries.
// Handlers warm the render cache on their own goroutines, so the tree can still be
// changing when a test body returns. The last attempt is left to the system temp
// reaper rather than failing the test.
func removeTreeRetry(path string) {
	for i := 0; i < 20; i++ {
		if err := os.RemoveAll(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// copyTree copies src to dst, recreating symlinks as symlinks.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
