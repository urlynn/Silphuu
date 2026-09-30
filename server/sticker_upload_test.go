package main

import (
	"archive/zip"
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// multipartFormWithFile builds the form an upload posts, so the handler's inputs are
// exercised in the shape a browser produces them.
func multipartFormWithFile(t *testing.T, filename string, content []byte) *multipart.Form {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("files", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	form, err := multipart.NewReader(&buf, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("read form: %v", err)
	}
	t.Cleanup(func() { form.RemoveAll() })
	return form
}

func TestStickerGroupNameRejectsWhatAShortcodeCannotCarry(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
	}{
		{"板板", true},
		{"01 板板 01", true},
		{"", false},
		{"a/b", false},
		{"..", false},
		{":x:", false},
		{"[x]", false},
		{".hidden", false},
		{"_pending", false},
		{strings.Repeat("板", stickerNameMaxRunes+1), false},
	}

	for _, c := range cases {
		got, err := stickerGroupName(c.name)
		if c.ok && err != nil {
			t.Errorf("stickerGroupName(%q) rejected a usable name: %v", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("stickerGroupName(%q) accepted it as %q", c.name, got)
		}
	}
}

func TestCollectStickerUploadsFlattensAZip(t *testing.T) {
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for _, name := range []string{"a.gif", "sub/b.jxl", "__MACOSX/._b.png", "notes.txt"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip member %s: %v", name, err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatalf("write zip member %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}

	form := multipartFormWithFile(t, "pack.zip", zipBuf.Bytes())
	scratch := t.TempDir()

	entries, dropped := collectStickerUploads(form.File["files"], scratch)

	found := map[string]string{}
	for _, e := range entries {
		found[e.name] = e.ext
		if _, err := os.Stat(e.path); err != nil {
			t.Errorf("entry %s points at a missing file %s", e.name, e.path)
		}
	}
	if len(entries) != 2 {
		t.Errorf("expected the two images and nothing else, got %v", found)
	}
	if found["a"] != ".gif" {
		t.Errorf("top-level member missing, got %v", found)
	}
	if found["b"] != ".jxl" {
		t.Errorf("a member inside a directory was not flattened out, got %v", found)
	}
	// Of the four members, the Mac resource fork is skipped rather than counted, and the
	// text file is the one dropped entry.
	if dropped != 1 {
		t.Errorf("expected one member to be dropped, got dropped=%d", dropped)
	}
}

func TestPublishStickerUploadLandsInTheGifSourceFolder(t *testing.T) {
	dir := t.TempDir()
	InitRoots(filepath.Join(dir, "home"), filepath.Join(dir, "served"))
	t.Cleanup(func() { InitRoots("") })

	form := multipartFormWithFile(t, "wave.gif", []byte("x"))
	scratch := t.TempDir()
	entries, dropped := collectStickerUploads(form.File["files"], scratch)
	if len(entries) != 1 || dropped != 0 {
		t.Fatalf("expected one entry and nothing dropped, got %d/%d", len(entries), dropped)
	}

	if n := publishStickerUpload("板板", entries); n != 1 {
		t.Fatalf("publishStickerUpload published %d of 1", n)
	}

	want := filepath.Join(DirSticker, "板板", "gif", "wave.gif")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("the source did not land at %s: %v", want, err)
	}
}
