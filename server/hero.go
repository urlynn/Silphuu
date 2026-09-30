package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
)

// HeroConfig is the hero's own settings: the tag row, and the avatar files the site
// actually holds.
//
// The engine ships a default at config/hero.json and a site replaces the whole file;
// see dataReadPath. Nothing here is merged field by field.
type HeroConfig struct {
	// Tags is the hero's tag row, in render order, any length.
	Tags []string `json:"tags"`

	// Avatar lists the avatar files that were written, most preferred first. The last
	// entry is what every browser gets; the ones before it are offered through
	// <source type>, which a browser skips without a request when it cannot decode
	// them. An empty list means the site has no avatar, and the page renders none.
	//
	// The list is a record, not a guess: whoever writes the files writes this. That is
	// why the renderer never has to ask a disk which formats exist.
	Avatar []string `json:"avatar"`
}

// loadHeroConfig reads the hero settings, falling back to the engine's shipped file.
func loadHeroConfig() HeroConfig {
	path := dataReadPath(FileHeroData)
	data, err := os.ReadFile(path)
	if err != nil {
		return HeroConfig{}
	}
	var cfg HeroConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("hero: %s is not valid JSON: %v", path, err)
		return HeroConfig{}
	}
	return cfg
}

// saveHeroConfig writes the hero settings to the deployment's own file.
//
// Always the deployment's path, never dataReadPath: the engine's copy is a default to
// be read, and a site that overrides only its tags still has to keep the avatar it
// set. Keys this build does not know about are preserved, so a hand-written comment
// survives a save from the panel.
func saveHeroConfig(cfg HeroConfig) error {
	if err := os.MkdirAll(RootConfig, 0o755); err != nil {
		return err
	}
	own, err := structToJSONMap(cfg)
	if err != nil {
		return err
	}
	out := map[string]any{}
	if raw, err := os.ReadFile(dataReadPath(FileHeroData)); err == nil {
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
	return os.WriteFile(FileHeroData, data, 0o644)
}

// heroAvatarBaseName is the avatar's file name without extension. Fixed rather than
// generated: a page that has already been published names one URL, and a second upload has
// to land on it rather than on a new one.
const heroAvatarBaseName = "avatar"

// handleAdminHeroSave replaces the hero's tag row with the posted one.
//
// The posted order is the render order. An empty post is an empty row: the engine's shipped
// hero.json is what a fresh deployment starts from, not a floor every save is padded up to,
// so a site that wants no tags gets none.
func handleAdminHeroSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		adminError(w, r, "bad form", http.StatusBadRequest)
		return
	}
	// The tag input serializes its chips into one comma-separated field, the shape the editor's
	// tag field submits too; the input strips commas from what is typed, so none can sit inside
	// a tag.
	var tags []string
	for _, t := range strings.Split(r.FormValue("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	if err := commitConfig(EvHeroTags, heroTagsPayload{Tags: tags}); err != nil {
		adminError(w, r, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	adminRespond(w, r, "")
}

// handleAdminHeroAvatar stores the uploaded avatar and records the files it wrote.
//
// The record is the point of this handler: pages copy the list into markup instead of asking
// a disk which formats exist, so whoever writes the bytes is the only one who has to know
// what they are. The pipeline decides the rest — svg passes through verbatim (rasterizing a
// vector is irreversible) and so do jxl / avif (re-encoding a target format only loses
// quality), while anything else is transcoded to the jxl + avif pair.
func handleAdminHeroAvatar(w http.ResponseWriter, r *http.Request) {
	res, err := ProcessMediaPipeline(r, CommitPresets["hero"], heroAvatarBaseName)
	if err != nil {
		// The pipeline's errors name server internals (tool paths, decode failures), so
		// they go to the log and the panel gets copy.
		log.Printf("hero: avatar upload failed: %v", err)
		adminError(w, r, uiText("home.hero.panel.err_upload"), http.StatusBadRequest)
		return
	}

	record := heroAvatarRecord(res.Format)
	if err := commitConfig(EvHeroAvatar, heroAvatarPayload{Avatar: record}); err != nil {
		adminError(w, r, "save error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Superseded files stay where they are: a page the invalidation has not replaced yet
	// still names them, and a name that no longer resolves is the broken image this record
	// exists to avoid. The base name is fixed, so the set is bounded by the formats and a
	// later upload of the same format overwrites its file.
	writeJSONResponse(w, map[string][]string{"urls": record})
}

// heroAvatarRecord turns the pipeline's output into the record: the files that were written,
// most preferred first.
//
// A transcode writes both formats and reports "jxl+avif"; a passthrough writes the single
// format it was handed, which res.Format names. The last entry is the one every browser
// gets, so the pair is ordered jxl first — the higher-quality format — with avif behind it
// for browsers that cannot decode jxl. That is negotiation by capability, not by 404: a
// browser skips a <source> it cannot decode without making a request.
func heroAvatarRecord(format string) []string {
	gen := CommitPresets["hero"].URLGenerator
	if format == "jxl+avif" {
		return []string{gen(heroAvatarBaseName + ".jxl"), gen(heroAvatarBaseName + ".avif")}
	}
	return []string{gen(heroAvatarBaseName + "." + format)}
}
