package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// stale stamps a file as old enough to be judged. The sweep spares a fresh file because an
// upload lands before the record that names it exists, so every fixture that is meant to be
// reclaimed has to be older than that window.
func stale(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * reclaimGrace)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes %s: %v", path, err)
	}
}

// plant writes a file under dir, creating dir, and stamps it stale.
func plant(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	stale(t, path)
	return path
}

// TestTheSweepReclaimsWhatNoRecordReferences is the guard for the whole mechanism: a byte the
// record still names stays, and a byte it does not name goes.
//
// It fails when the sweep is a no-op (nothing is reclaimed) and when it judges by anything
// other than the record (the named byte is taken too).
func TestTheSweepReclaimsWhatNoRecordReferences(t *testing.T) {
	useExampleSite(t)

	kept := plant(t, DirBackground, "kept.avif")
	orphan := plant(t, DirBackground, "orphan.avif")

	cfg := BgConfig{
		Interval:    15,
		Mode:        "sequential",
		Backgrounds: []BgItem{{Src: PrefixStatic + "/image/background/kept.avif", Device: "desktop", Tone: "dark"}},
	}
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal background.json: %v", err)
	}
	if err := os.WriteFile(FileBackgroundData, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", FileBackgroundData, err)
	}

	rep := reclaimOrphans("background")

	if !fileExists(kept) {
		t.Error("the byte the record still names was reclaimed")
	}
	if fileExists(orphan) {
		t.Error("the byte no record names survived the sweep")
	}
	if got := rep.reclaimed(); got != 1 {
		t.Errorf("reclaimed %d file(s), want 1: %s", got, reclaimSummary(rep))
	}
}

// TestTheSweepKeepsEverythingWhenTheRecordCannotBeRead is safety rail one.
//
// "Nothing is referenced" and "the record could not be read" are different statements, and only
// the first one makes bytes garbage. A fixture that drops the record must therefore lose nothing.
func TestTheSweepKeepsEverythingWhenTheRecordCannotBeRead(t *testing.T) {
	useExampleSite(t)

	orphan := plant(t, DirBackground, "orphan.avif")
	if err := os.Remove(FileBackgroundData); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", FileBackgroundData, err)
	}

	rep := reclaimOrphans("background")

	if !fileExists(orphan) {
		t.Error("a class with no readable record reclaimed a byte")
	}
	if len(rep.Classes) != 1 || rep.Classes[0].Skipped == "" {
		t.Errorf("report did not say the class was skipped: %s", reclaimSummary(rep))
	}
}

// TestTheSweepKeepsEverythingWhenTheRootIsNotHere is safety rail two: a node that holds none of
// a class's bytes must not report an empty class.
func TestTheSweepKeepsEverythingWhenTheRootIsNotHere(t *testing.T) {
	useExampleSite(t)

	rep := reclaimOrphans("photo-blur-thumb")

	if len(rep.Classes) != 1 {
		t.Fatalf("report has %d class(es), want 1", len(rep.Classes))
	}
	if rep.Classes[0].Skipped == "" {
		t.Errorf("a missing root was swept instead of skipped: %s", reclaimSummary(rep))
	}
	if got := rep.reclaimed(); got != 0 {
		t.Errorf("reclaimed %d file(s) under a root this node does not have", got)
	}
}

// TestTheSweepLeavesAFreshFileAlone covers the upload window: the editor writes the image, the
// person then submits the body that references it, and a sweep in between must not take it.
func TestTheSweepLeavesAFreshFileAlone(t *testing.T) {
	useExampleSite(t)

	if err := os.MkdirAll(DirImageComment, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", DirImageComment, err)
	}
	fresh := filepath.Join(DirImageComment, "0123456789abcdef.avif")
	if err := os.WriteFile(fresh, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", fresh, err)
	}

	reclaimOrphans("comment-image")

	if !fileExists(fresh) {
		t.Error("a file written inside the grace window was reclaimed")
	}
}

// TestTheSweepSkipsDotEntries keeps a stray state directory or a desktop metadata file under a
// root from being judged: dot entries are not content.
func TestTheSweepSkipsDotEntries(t *testing.T) {
	useExampleSite(t)

	hidden := plant(t, filepath.Join(DirPhoto, ".thumb"), "0123456789abcdef.avif")
	meta := plant(t, DirPhoto, ".DS_Store")
	record := []Photo{{ID: "0123456789abcdef"}}
	body, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal photo.json: %v", err)
	}
	if err := os.WriteFile(FilePhotoData, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", FilePhotoData, err)
	}

	reclaimOrphans("photo-source")

	if !fileExists(hidden) {
		t.Error("a dot directory under a root was descended into")
	}
	if !fileExists(meta) {
		t.Error("a dot file under a root was reclaimed")
	}
}

// TestEveryRegisteredRootIsInsideARoot refuses a registry entry whose root is a root itself.
// The sweep deletes what a root does not account for, so a root that names the whole served tree
// or the whole deployment would delete the site.
func TestEveryRegisteredRootIsInsideARoot(t *testing.T) {
	useExampleSite(t)

	for _, kind := range resourceKinds {
		root := filepath.Clean(kind.Root())
		if root == filepath.Clean(homeRoot) || root == filepath.Clean(staticRoot) {
			t.Errorf("%s: root %s is a root itself", kind.Name, root)
			continue
		}
		if !insideServedRoot(root) && !insideTree(homeRoot, root) {
			t.Errorf("%s: root %s is outside both the deployment and the served tree", kind.Name, root)
		}
	}
}

// TestNoRegisteredRootIsUnderASourceTree refuses a class whose root is a source tree.
//
// A file under src/ is named by a template as a string, so no record can enumerate the names in
// use, and a spare file there is a legitimate thing for a site to have rather than an orphan. A
// class registered over a source tree therefore deletes the site's own overrides: every icon a
// template names but no record mentions reads as unreferenced.
func TestNoRegisteredRootIsUnderASourceTree(t *testing.T) {
	useExampleSite(t)

	sources := map[string]string{
		"the site's src/":   homeSrcPath(),
		"the engine's src/": RootSrc,
		"the font sources":  RootFontSources,
	}
	for _, kind := range resourceKinds {
		root := filepath.Clean(kind.Root())
		for label, tree := range sources {
			if filepath.Clean(tree) == root || insideTree(tree, root) {
				t.Errorf("%s: root %s is inside %s, which is a source tree", kind.Name, root, label)
			}
		}
	}
}

// TestTheFontSweepKeepsEveryFamilyThePanelOffers pins the boundary to the admin-selectable set.
//
// The panel previews every family the map names, so a subset file for one of them stays whether
// or not its source is on disk and whether or not the current preset uses it. Only a family the
// map no longer names is reclaimed — the files a removed face left behind.
func TestTheFontSweepKeepsEveryFamilyThePanelOffers(t *testing.T) {
	useExampleSite(t)

	fontMap := loadFontMap()
	if len(fontMap) == 0 {
		t.Fatal("font_map.json is empty, so this test proves nothing")
	}
	offered := ""
	for fam := range fontMap {
		offered = fam
		break
	}

	kept := plant(t, DirFont, offered+".woff2")
	orphan := plant(t, DirFont, "NotInTheMapAtAll.woff2")

	reclaimOrphans("font-subset")

	if !fileExists(kept) {
		t.Errorf("the subset for %s was reclaimed, but the panel still offers it", offered)
	}
	if fileExists(orphan) {
		t.Error("the subset of a family the map does not name survived")
	}
}

// TestTheSweepNamesEveryClass refuses a class that a sweep silently drops: every registered
// class has to appear in the report, either swept or skipped.
func TestTheSweepNamesEveryClass(t *testing.T) {
	useExampleSite(t)

	rep := reclaimOrphans()
	if len(rep.Classes) != len(resourceKinds) {
		t.Fatalf("report has %d class(es), registry has %d", len(rep.Classes), len(resourceKinds))
	}
	for _, c := range rep.Classes {
		if c.Name == "" || c.Root == "" {
			t.Errorf("report entry without a name or a root: %+v", c)
		}
	}
}

// TestAnEventQueuesTheClassesItCanChange covers the trigger mapping: it lives in the registry, so
// a class that declares no triggers is never queued and one that does is never missed.
func TestAnEventQueuesTheClassesItCanChange(t *testing.T) {
	useExampleSite(t)

	takeReclaimQueue()
	reclaimOnEvent(EvPhotoRemoved)
	got := takeReclaimQueue()

	want := map[string]bool{}
	for _, kind := range resourceKinds {
		if contains(kind.Triggers, EvPhotoRemoved) {
			want[kind.Name] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("no class declares EvPhotoRemoved, so this test proves nothing")
	}
	if len(got) != len(want) {
		t.Fatalf("queued %v, want %d class(es)", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("queued %s, which does not declare EvPhotoRemoved", name)
		}
	}
}

// TestTheSubsetKeysGroupVariants checks the two keys that are not a plain stem: the four
// variants of one font family, and the two thumbnail forms of one sticker.
func TestTheSubsetKeysGroupVariants(t *testing.T) {
	fonts := map[string]string{
		"LemiMuhe.woff2":                "LemiMuhe",
		"home/LemiMuhe-home.woff2":      "LemiMuhe",
		"admin/LemiMuhe-admin.woff2":    "LemiMuhe",
		"cmt/LemiMuhe-cmt.woff2":        "LemiMuhe",
		"MaokenPearl-Medium.woff2":      "MaokenPearl-Medium",
		"admin/SharkHealth-admin.woff2": "SharkHealth",
	}
	for rel, want := range fonts {
		if got := fontSubsetKey(rel); got != want {
			t.Errorf("fontSubsetKey(%q) = %q, want %q", rel, got, want)
		}
	}

	stickers := map[string]string{
		"板板/preview/wave.avif": "板板/wave",
		"板板/dynamic/wave.avif": "板板/wave",
		"板板/loose.avif":        "板板/loose",
	}
	for rel, want := range stickers {
		if got := stickerThumbKey(rel); got != want {
			t.Errorf("stickerThumbKey(%q) = %q, want %q", rel, got, want)
		}
	}
}
