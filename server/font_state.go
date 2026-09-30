package main

// font_state.go — the font projection: the preset list the event log carries, and the state file
// it lands as.
//
// A preset's metrics are measured in the browser that saved it, so a node that did not take the
// save cannot derive them: the record travels whole. Everything downstream of the numbers — the
// ink variables, the subsets, the rendered pages — stays derived per node, and the file watcher
// is what rebuilds those off the change this projection makes.

import (
	"bytes"
	"encoding/json"
	"log"
	"os"
)

// EvFontPresets carries the whole preset list and which preset is active.
//
// The list travels whole because the file is written whole: the workshop replaces it on every
// save, and the four panel actions — save, add, delete, select — each produce a new list.
const EvFontPresets = "font_presets"

// fontPresetsPayload is the machine-generated half of state/fonts.json.
type fontPresetsPayload struct {
	Presets []FontPreset `json:"font_presets"`
	Active  string       `json:"active_font_preset"`
}

// applyFontEvent lands one preset list.
func applyFontEvent(evt *Event) {
	if evt.Type != EvFontPresets {
		return
	}
	var p fontPresetsPayload
	if json.Unmarshal(evt.Payload, &p) != nil {
		return
	}
	cfg := AppConfig{FontPresets: p.Presets, ActiveFontPreset: p.Active}
	want, err := renderFontState(cfg)
	if err != nil {
		log.Printf("font: %s: %v", EvFontPresets, err)
		return
	}
	// The node that took the save has this file already. Rewriting identical bytes would move
	// the mtime the watcher keys on, and the watcher is what rebuilds the ink variables, the
	// subsets and the pages.
	if current, err := os.ReadFile(FileFontsData); err == nil && bytes.Equal(current, want) {
		return
	}
	if err := saveFontState(cfg); err != nil {
		log.Printf("font: %s: %v", EvFontPresets, err)
	}
}
