package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestAFontPresetEventLandsTheState: a preset list has to reach state/fonts.json, which is what
// lets a node that never opened the workshop render with the same families and alignment.
func TestAFontPresetEventLandsTheState(t *testing.T) {
	useExampleSite(t)

	applyFontEvent(testEvent(EvFontPresets, `{"font_presets":[{"id":"p1","name":"mine",`+
		`"fonts":{"body":"'A', sans-serif"},"metrics":{"body":{"ink_height":0.8}}}],`+
		`"active_font_preset":"p1"}`))

	cfg := loadAppConfig()
	if cfg.ActiveFontPreset != "p1" {
		t.Fatalf("the active preset did not land: %q", cfg.ActiveFontPreset)
	}
	if len(cfg.FontPresets) != 1 || cfg.FontPresets[0].Name != "mine" {
		t.Fatalf("the preset list did not land: %+v", cfg.FontPresets)
	}
	// The metrics are the half the peer cannot derive, so they have to survive the round trip.
	if got := cfg.FontPresets[0].Metrics["body"].InkHeight; got != 0.8 {
		t.Errorf("the measured metrics did not land: got %v, want 0.8", got)
	}
	body, err := os.ReadFile(FileFontsData)
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if !strings.Contains(string(body), fontStateComment) {
		t.Error("the landed file does not carry the comment the workshop writes")
	}
}

// TestAFontPresetEventLeavesAnUnchangedFileAlone: the node that took the save already wrote the
// file, and the watcher keys on its mtime. A rewrite of identical bytes would rebuild the ink
// variables and the subsets for a change that did not happen.
func TestAFontPresetEventLeavesAnUnchangedFileAlone(t *testing.T) {
	useExampleSite(t)

	payload := `{"font_presets":[{"id":"p2","name":"same","fonts":{"body":"'B', serif"}}],` +
		`"active_font_preset":"p2"}`
	applyFontEvent(testEvent(EvFontPresets, payload))

	stamp := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := os.Chtimes(FileFontsData, stamp, stamp); err != nil {
		t.Fatalf("could not age the state file: %v", err)
	}

	applyFontEvent(testEvent(EvFontPresets, payload))

	fi, err := os.Stat(FileFontsData)
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if !fi.ModTime().Equal(stamp) {
		t.Error("an unchanged delivery rewrote the file, which the watcher reads as a change")
	}

	// A delivery that does differ still has to land, or the guard above would pass by never
	// writing anything.
	applyFontEvent(testEvent(EvFontPresets, `{"font_presets":[{"id":"p2","name":"moved",`+
		`"fonts":{"body":"'B', serif"}}],"active_font_preset":"p2"}`))
	if cfg := loadAppConfig(); cfg.FontPresets[0].Name != "moved" {
		t.Errorf("a changed delivery did not land: %+v", cfg.FontPresets)
	}
}
