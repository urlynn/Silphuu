package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadPhotosIsAPureRead: reading the album index must not depend on the files being
// present. A node that does not hold static/photo sees every record as broken, and a read
// that rewrote the index there erased the album — which is exactly what the running server
// did at startup, through generateAllBlurThumbs.
func TestLoadPhotosIsAPureRead(t *testing.T) {
	useExampleSite(t)

	records := []Photo{{ID: "aaaa000000000001"}, {ID: "aaaa000000000002"}}
	if err := savePhotos(records); err != nil {
		t.Fatalf("savePhotos: %v", err)
	}
	before, err := os.ReadFile(FilePhotoData)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}

	// No file exists for either record, so a read that pruned would return nothing.
	got := loadPhotos()
	if len(got) != len(records) {
		t.Fatalf("loadPhotos returned %d record(s), want %d: the read must not prune", len(got), len(records))
	}
	after, err := os.ReadFile(FilePhotoData)
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("loadPhotos rewrote the index: %s -> %s", before, after)
	}
}

// TestPrunePhotoRecordsIsTheExplicitCleanup: dropping a record is a separate action, and it
// only drops the records with no file behind them.
func TestPrunePhotoRecordsIsTheExplicitCleanup(t *testing.T) {
	useExampleSite(t)

	keep := Photo{ID: "bbbb000000000001", Order: 0}
	drop := Photo{ID: "bbbb000000000002", Order: 1}
	if err := savePhotos([]Photo{keep, drop}); err != nil {
		t.Fatalf("savePhotos: %v", err)
	}
	if err := os.MkdirAll(DirPhoto, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", DirPhoto, err)
	}
	if err := os.WriteFile(filepath.Join(DirPhoto, keep.ID+".avif"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	dropped, err := prunePhotoRecords()
	if err != nil {
		t.Fatalf("prunePhotoRecords: %v", err)
	}
	if dropped != 1 {
		t.Errorf("pruned %d record(s), want 1", dropped)
	}
	remaining := loadPhotos()
	if len(remaining) != 1 || remaining[0].ID != keep.ID {
		t.Errorf("remaining records = %+v, want only %s", remaining, keep.ID)
	}
}

// TestPrunePhotoRecordsKeepsEverythingWithoutTheAlbumTree: the guard is this node's own
// absence of the tree, not a caller's promise. Records are the album's data and no other
// node can reproduce them, so a node that only renders must not be able to delete them —
// not even by accident.
func TestPrunePhotoRecordsKeepsEverythingWithoutTheAlbumTree(t *testing.T) {
	useExampleSite(t)

	records := []Photo{{ID: "dddd000000000001"}, {ID: "dddd000000000002"}}
	if err := savePhotos(records); err != nil {
		t.Fatalf("savePhotos: %v", err)
	}
	if err := os.RemoveAll(DirPhoto); err != nil {
		t.Fatalf("remove %s: %v", DirPhoto, err)
	}

	dropped, err := prunePhotoRecords()
	if err != nil {
		t.Fatalf("prunePhotoRecords: %v", err)
	}
	if dropped != 0 {
		t.Errorf("pruned %d record(s) on a node with no album tree, want 0", dropped)
	}
	if got := loadPhotos(); len(got) != len(records) {
		t.Errorf("remaining records = %d, want %d: the index has to survive", len(got), len(records))
	}
}

// TestPhotoAddCreatesNoAlbumTreeWithoutImageMagick: a node that cannot generate the image has
// to fail before it touches the filesystem. The album tree is how the engine tells "this site
// has no photos" from "this node holds none of them"; leaving one behind turns the second into
// the first, and the cleanup action then drops every record on that node.
func TestPhotoAddCreatesNoAlbumTreeWithoutImageMagick(t *testing.T) {
	useExampleSite(t)

	restore := magickCmd
	magickCmd = ""
	t.Cleanup(func() { magickCmd = restore })

	if err := os.RemoveAll(DirPhoto); err != nil {
		t.Fatalf("remove %s: %v", DirPhoto, err)
	}

	rec := httptest.NewRecorder()
	handlePhotoAdd(rec, httptest.NewRequest("POST", "/admin/commit/photo/add", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("photo/add without ImageMagick = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	// The failure has to name its cause. An empty detail is what made this endpoint read as a
	// hang rather than as a missing binary.
	if !strings.Contains(rec.Body.String(), "imagemagick") {
		t.Errorf("failure detail = %q, want it to name the missing tool", rec.Body.String())
	}
	if _, err := os.Stat(DirPhoto); !os.IsNotExist(err) {
		t.Errorf("handlePhotoAdd left %s behind on a node that cannot generate a photo", DirPhoto)
	}
}

// TestPrunePhotoRecordsIsANoOpWhenNothingIsMissing: a clean index must survive the cleanup
// untouched, or the action would be unsafe to run at all.
func TestPrunePhotoRecordsIsANoOpWhenNothingIsMissing(t *testing.T) {
	useExampleSite(t)

	keep := Photo{ID: "cccc000000000001"}
	if err := savePhotos([]Photo{keep}); err != nil {
		t.Fatalf("savePhotos: %v", err)
	}
	if err := os.MkdirAll(DirPhoto, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", DirPhoto, err)
	}
	if err := os.WriteFile(filepath.Join(DirPhoto, keep.ID+".jxl"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	dropped, err := prunePhotoRecords()
	if err != nil {
		t.Fatalf("prunePhotoRecords: %v", err)
	}
	if dropped != 0 {
		t.Errorf("pruned %d record(s) from a clean index, want 0", dropped)
	}
	if got := loadPhotos(); len(got) != 1 || got[0].ID != keep.ID {
		t.Errorf("remaining records = %+v, want only %s", got, keep.ID)
	}
}
