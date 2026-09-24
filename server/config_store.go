package main

import (
	"encoding/json"
	"log"
	"os"
)

// Configuration persistence: two files, split by who writes them.
//
//	config/site.json  hand-written by a person — 9 short fields
//	state/fonts.json  written wholesale by the font workshop — measured values
//
// Keeping them apart means a hand edit to site.json can never destroy font
// measurements, and a font swap touches fonts.json alone.

// fontState is the machine-generated half of the configuration.
type fontState struct {
	Comment      string       `json:"_comment,omitempty"`
	Presets      []FontPreset `json:"font_presets"`
	ActivePreset string       `json:"active_font_preset"`
}

// fontStateComment explains the file to anyone who opens it.
const fontStateComment = "由后台字体工坊自动测量并整体重写，请勿手工编辑。人手填写的配置在 site.json。"

// loadFontState fills the machine-generated fields of cfg from fonts.json.
func loadFontState(cfg *AppConfig) {
	data, err := os.ReadFile(FileFontsData)
	if err != nil {
		return
	}
	var fs fontState
	if err := json.Unmarshal(data, &fs); err != nil {
		log.Printf("config: %s is not valid JSON: %v", FileFontsData, err)
		return
	}
	cfg.FontPresets = fs.Presets
	cfg.ActiveFontPreset = fs.ActivePreset
}

// saveFontState persists the machine-generated font measurements. This is the
// only writer of fonts.json, and it never touches site.json.
func saveFontState(cfg AppConfig) error {
	fs := fontState{
		Comment:      fontStateComment,
		Presets:      cfg.FontPresets,
		ActivePreset: cfg.ActiveFontPreset,
	}
	data, err := json.MarshalIndent(fs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(FileFontsData, data, 0o644)
}

// saveAppConfig persists the hand-written half of the configuration.
//
// Keys this build does not know about are preserved. The file belongs to the
// person editing it: a comment or a key written for a newer release must survive
// a save from an older one.
func saveAppConfig(cfg AppConfig) error {
	own, err := structToJSONMap(cfg)
	if err != nil {
		return err
	}

	out := map[string]any{}
	// Base the document on whatever is currently being read — the deployment's own
	// file when it exists, otherwise the shipped default. That way the first save
	// on a fresh deployment materialises the shipped comments and key order rather
	// than starting from an empty object.
	if raw, err := os.ReadFile(dataReadPath(FileConfig)); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	for k, v := range own {
		out[k] = v
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(FileConfig, data, 0o644)
}

// structToJSONMap renders a struct as a generic map, so the caller can merge it
// into an existing document rather than replacing the document wholesale.
func structToJSONMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
