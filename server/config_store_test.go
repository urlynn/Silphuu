package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// setupConfigHome points the content roots at an empty temp directory and returns it,
// with config/ and state/ both created. The two files this suite exercises live in
// different roots: site.json is configuration, fonts.json is engine state.
func setupConfigHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for _, dir := range []string{"config", "state"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	InitRoots(home)
	t.Cleanup(func() { InitRoots("") })
	return home
}

// TestSaveFontStateDoesNotTouchConfig is the whole point of the split: the font
// workshop rewrites its own file and leaves the hand-written one alone.
func TestSaveFontStateDoesNotTouchConfig(t *testing.T) {
	home := setupConfigHome(t)
	cfgPath := filepath.Join(home, "config", "site.json")

	handWritten := []byte("{\n  \"author\": \"手写的名字\",\n  \"_comment\": \"别动我\"\n}\n")
	if err := os.WriteFile(cfgPath, handWritten, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cfg := loadAppConfig()
	cfg.FontPresets = []FontPreset{{ID: "p1", Name: "测出来的", MetricsVer: 2}}
	cfg.ActiveFontPreset = "p1"
	if err := saveFontState(cfg); err != nil {
		t.Fatalf("saveFontState: %v", err)
	}

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(handWritten) {
		t.Errorf("site.json was modified by a font save:\n before: %s\n after:  %s", handWritten, after)
	}

	fontsRaw, err := os.ReadFile(filepath.Join(home, "state", "fonts.json"))
	if err != nil {
		t.Fatalf("fonts.json was not written: %v", err)
	}
	var fs fontState
	if err := json.Unmarshal(fontsRaw, &fs); err != nil {
		t.Fatalf("fonts.json is not valid JSON: %v", err)
	}
	if len(fs.Presets) != 1 || fs.Presets[0].ID != "p1" {
		t.Errorf("fonts.json presets = %+v, want the saved preset", fs.Presets)
	}
	if fs.ActivePreset != "p1" {
		t.Errorf("fonts.json active preset = %q, want %q", fs.ActivePreset, "p1")
	}
	if fs.Comment == "" {
		t.Errorf("fonts.json should carry an explanatory _comment")
	}
}

// TestSaveAppConfigDoesNotTouchFonts is the mirror image: saving the hand-written
// half must never rewrite the measurements.
func TestSaveAppConfigDoesNotTouchFonts(t *testing.T) {
	home := setupConfigHome(t)
	fontsPath := filepath.Join(home, "state", "fonts.json")

	measurements := []byte("{\n  \"font_presets\": [{\"id\": \"measured\"}]\n}\n")
	if err := os.WriteFile(fontsPath, measurements, 0o644); err != nil {
		t.Fatalf("seed fonts: %v", err)
	}

	cfg := loadAppConfig()
	cfg.Author = "新名字"
	cfg.Motto = "新签名"
	if err := saveAppConfig(cfg); err != nil {
		t.Fatalf("saveAppConfig: %v", err)
	}

	after, err := os.ReadFile(fontsPath)
	if err != nil {
		t.Fatalf("read fonts: %v", err)
	}
	if string(after) != string(measurements) {
		t.Errorf("fonts.json was modified by a config save:\n before: %s\n after:  %s", measurements, after)
	}

	var written map[string]any
	raw, err := os.ReadFile(filepath.Join(home, "config", "site.json"))
	if err != nil {
		t.Fatalf("site.json was not written: %v", err)
	}
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("site.json is not valid JSON: %v", err)
	}
	if written["author"] != "新名字" {
		t.Errorf("author = %v, want the saved value", written["author"])
	}
	// The font block must not reappear in the hand-written file.
	if _, ok := written["font_presets"]; ok {
		t.Errorf("font_presets leaked back into site.json")
	}
	if _, ok := written["active_font_preset"]; ok {
		t.Errorf("active_font_preset leaked back into site.json")
	}
}

// TestSaveAppConfigPreservesUnknownKeys: the file belongs to the person editing
// it, so a comment or a key from a newer release must survive a save.
func TestSaveAppConfigPreservesUnknownKeys(t *testing.T) {
	home := setupConfigHome(t)
	cfgPath := filepath.Join(home, "config", "site.json")

	seed := `{
  "_comment": "这段注释必须活下来",
  "author": "旧名字",
  "some_future_key": {"nested": true}
}`
	if err := os.WriteFile(cfgPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := loadAppConfig()
	cfg.Author = "新名字"
	if err := saveAppConfig(cfg); err != nil {
		t.Fatalf("saveAppConfig: %v", err)
	}

	var written map[string]any
	raw, _ := os.ReadFile(cfgPath)
	if err := json.Unmarshal(raw, &written); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if written["_comment"] != "这段注释必须活下来" {
		t.Errorf("_comment = %v, want it preserved", written["_comment"])
	}
	if _, ok := written["some_future_key"]; !ok {
		t.Errorf("an unknown key was dropped; the code must not delete what it does not own")
	}
	if written["author"] != "新名字" {
		t.Errorf("author = %v, want the new value", written["author"])
	}
}

// TestLoadFontStateReadsFontsJSON: the machine half comes from fonts.json alone.
func TestLoadFontStateReadsFontsJSON(t *testing.T) {
	home := setupConfigHome(t)
	if err := os.WriteFile(filepath.Join(home, "state", "fonts.json"),
		[]byte(`{"font_presets":[{"id":"from-fonts"}],"active_font_preset":"from-fonts"}`), 0o644); err != nil {
		t.Fatalf("write fonts: %v", err)
	}

	var cfg AppConfig
	loadFontState(&cfg)

	if len(cfg.FontPresets) != 1 || cfg.FontPresets[0].ID != "from-fonts" {
		t.Errorf("presets = %+v, want the preset from fonts.json", cfg.FontPresets)
	}
	if cfg.ActiveFontPreset != "from-fonts" {
		t.Errorf("active preset = %q, want %q", cfg.ActiveFontPreset, "from-fonts")
	}
}

// TestLoadFontStateWithNothingOnDisk must not panic or invent data.
func TestLoadFontStateWithNothingOnDisk(t *testing.T) {
	setupConfigHome(t)

	var cfg AppConfig
	loadFontState(&cfg)

	if cfg.FontPresets != nil {
		t.Errorf("presets = %+v, want nil when nothing is configured", cfg.FontPresets)
	}
	if cfg.ActiveFontPreset != "" {
		t.Errorf("active preset = %q, want empty", cfg.ActiveFontPreset)
	}
}
