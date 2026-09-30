package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A tree is scanned by whoever holds it; every other node reads the result. Publishing from
// a node that does not hold the tree puts its emptiness over the copy that was synced to it,
// and the picker and every article's shortcodes go with it.
func TestLoadStickersReadsThePublishedIndexWithoutTheTree(t *testing.T) {
	useExampleSite(t)

	prevCache, prevMap := stickerCache, shortcodeMap
	t.Cleanup(func() { stickerCache, shortcodeMap = prevCache, prevMap })

	sets := []StickerSet{{
		Name:        "miku",
		DisplayName: "初音未来",
		Stickers: []Sticker{{
			AVIFURL: "/static/sticker/miku/avif/01.avif",
			JXLURL:  "/static/sticker/miku/jxl/01.jxl",
			Name:    "01",
		}},
	}}
	data, err := json.Marshal(sets)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(stickerIndexPath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(stickerIndexPath(), data, 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	// This node holds no sticker tree — the shape of a node that only renders.
	if err := os.RemoveAll(DirSticker); err != nil {
		t.Fatalf("remove %s: %v", DirSticker, err)
	}

	loadStickers()

	if len(stickerCache) != 1 || len(stickerCache[0].Stickers) != 1 {
		t.Fatalf("stickerCache = %+v, want the published index", stickerCache)
	}
	after, err := os.ReadFile(stickerIndexPath())
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !bytes.Equal(data, after) {
		t.Errorf("loadStickers rewrote the index it had just read:\n  before %s\n  after  %s", data, after)
	}
	// The map is what the article renderer looks shortcodes up in: without it every
	// `:group name:` in every article comes out as plain text.
	if _, ok := shortcodeMap["初音未来_01"]; !ok {
		t.Errorf("shortcodeMap = %v, want 初音未来_01 built from the published index", shortcodeMap)
	}
}

// An existing but empty tree is the site saying it ships no stickers. That is a different
// answer from "not my tree", and it is still published: the picker's URL has to resolve.
func TestLoadStickersPublishesAnEmptyIndexForAnEmptyTree(t *testing.T) {
	useExampleSite(t)

	prevCache := stickerCache
	t.Cleanup(func() { stickerCache = prevCache })

	if err := os.MkdirAll(DirSticker, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", DirSticker, err)
	}
	if err := os.MkdirAll(filepath.Dir(stickerIndexPath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A stale index, to prove the empty result is written rather than left in place.
	if err := os.WriteFile(stickerIndexPath(), []byte(`[{"Name":"stale"}]`), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}

	loadStickers()

	if len(stickerCache) != 0 {
		t.Fatalf("stickerCache = %+v, want nothing", stickerCache)
	}
	data, err := os.ReadFile(stickerIndexPath())
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if got := string(bytes.TrimSpace(data)); got != "[]" {
		t.Errorf("published index = %s, want an empty array", got)
	}
}
