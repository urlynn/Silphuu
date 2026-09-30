package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultPathsAreUnchanged pins the default path layout: engine roots are relative to
// the working directory (server/), content roots are relative to the default deployment
// root. This test is what stops a refactor from silently moving a file.
func TestDefaultPathsAreUnchanged(t *testing.T) {
	InitRoots("")
	t.Cleanup(func() { InitRoots("") })

	// The default deployment root must not be the engine tree. That collapse is what let
	// runtime output land in the repository.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if sameDir(homeRoot, wd) {
		t.Fatalf("the default deployment root is the working directory (%s)", homeRoot)
	}
	home := homeRoot
	served := staticRoot

	want := map[string]string{
		// engine roots stay put
		"RootSrc":       "src",
		"RootTemplates": "templates",
		// served roots: everything a browser can fetch. Pages are written into the served
		// root itself, so what a browser fetches is the layout the engine writes.
		"RootRender": served,
		"RootAssets": filepath.Join(served, "assets"),
		"RootError":  filepath.Join(served, "error"),
		// deployment roots: the site's own content, none of it written by the engine, all of
		// it outside the served tree except static/, which the served tree links to
		"RootConfig":      filepath.Join(home, "config"),
		"RootState":       filepath.Join(home, "state"),
		"RootPosts":       filepath.Join(home, "posts"),
		"RootFontSources": filepath.Join(home, "Fonts"),
		"RootStaging":     filepath.Join(home, "staging"),
		"RootStatic":      filepath.Join(home, "static"),
		"RootPublic":      filepath.Join(home, "public"),
		// media: the site's own images and uploads are one tree, beside the rest of the
		// site's content rather than in the served tree
		"DirImagePost":    filepath.Join(home, "static", "image", "post"),
		"DirImageComment": filepath.Join(home, "static", "image", "comment"),
		"DirPhoto":        filepath.Join(home, "static", "photo"),
		"DirSticker":      filepath.Join(home, "static", "sticker"),
		"DirBackground":   filepath.Join(home, "static", "image", "background"),
		"DirSponsor":      filepath.Join(home, "static", "image", "sponsor"),
		"DirThumbRoot":    filepath.Join(served, "assets", "thumb"),
		"DirFont":         filepath.Join(served, "assets", "font"),
		// config files: hand-written, or edited through the admin UI
		"FileConfig":          filepath.Join(home, "config", "site.json"),
		"FileSocial":          filepath.Join(home, "config", "social.json"),
		"FilePhotoData":       filepath.Join(home, "config", "photo.json"),
		"FileBackgroundData":  filepath.Join(home, "config", "background.json"),
		"FileSponsorData":     filepath.Join(home, "config", "sponsor.json"),
		"FileFriendData":      filepath.Join(home, "config", "friend.json"),
		"FileTopicData":       filepath.Join(home, "config", "topic.json"),
		"FileStickerData":     filepath.Join(home, "config", "sticker.json"),
		"FileUIStrings":       filepath.Join(home, "config", "ui_strings.json"),
		"FileUIStringsAdmin":  filepath.Join(home, "config", "ui_strings_admin.json"),
		"FilePreviewFixtures": filepath.Join(home, "config", "preview_fixtures.json"),
		"FileNavSignatures":   filepath.Join(home, "config", "nav_signatures.json"),
		"FileFontMap":         filepath.Join(home, "config", "font_map.json"),
		"FileFontExtraChars":  filepath.Join(home, "config", "font_extra_chars.json"),
		"DirPresets":          filepath.Join(home, "config", "presets"),
		// state files: what the engine measures, records or counts
		"FileFontsData":  filepath.Join(home, "state", "fonts.json"),
		"FileEventJSONL": filepath.Join(home, "state", "events.jsonl"),
		"FileSyncState":  filepath.Join(home, "state", "sync-state.json"),
		// staging
		"DirStagingFriend":  filepath.Join(home, "staging", "friend"),
		"DirStagingSticker": filepath.Join(home, "staging", "sticker"),
	}

	got := map[string]string{
		"RootSrc":             RootSrc,
		"RootTemplates":       RootTemplates,
		"RootAssets":          RootAssets,
		"RootStatic":          RootStatic,
		"RootConfig":          RootConfig,
		"RootState":           RootState,
		"RootRender":          RootRender,
		"RootPublic":          RootPublic,
		"RootPosts":           RootPosts,
		"RootError":           RootError,
		"RootFontSources":     RootFontSources,
		"RootStaging":         RootStaging,
		"DirImagePost":        DirImagePost,
		"DirImageComment":     DirImageComment,
		"DirPhoto":            DirPhoto,
		"DirSticker":          DirSticker,
		"DirBackground":       DirBackground,
		"DirSponsor":          DirSponsor,
		"DirThumbRoot":        DirThumbRoot,
		"DirFont":             DirFont,
		"FileConfig":          FileConfig,
		"FileSocial":          FileSocial,
		"FilePhotoData":       FilePhotoData,
		"FileBackgroundData":  FileBackgroundData,
		"FileSponsorData":     FileSponsorData,
		"FileFriendData":      FileFriendData,
		"FileTopicData":       FileTopicData,
		"FileStickerData":     FileStickerData,
		"FileFontsData":       FileFontsData,
		"FileEventJSONL":      FileEventJSONL,
		"FileSyncState":       FileSyncState,
		"FileUIStrings":       FileUIStrings,
		"FileUIStringsAdmin":  FileUIStringsAdmin,
		"FilePreviewFixtures": FilePreviewFixtures,
		"FileNavSignatures":   FileNavSignatures,
		"FileFontMap":         FileFontMap,
		"FileFontExtraChars":  FileFontExtraChars,
		"DirPresets":          DirPresets,
		"DirStagingFriend":    DirStagingFriend,
		"DirStagingSticker":   DirStagingSticker,
	}

	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
}

// TestInitRootsRepointsContentOnly verifies that the two configurable roots each move their
// own half: the deployment root takes config/, state/, posts/, Fonts/, staging/ and static/
// (the site's own media), the served root takes render/, public/ and assets/ (what the engine
// generates). The engine roots (src/, templates/) follow neither — a deployment without the
// release sources cannot serve a page at all.
func TestInitRootsRepointsContentOnly(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	served := filepath.Join(dir, "served")
	InitRoots(home, served)
	t.Cleanup(func() { InitRoots("") })

	absHome, err := filepath.Abs(home)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", home, err)
	}
	absServed, err := filepath.Abs(served)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", served, err)
	}

	deploymentCases := []struct{ name, got, want string }{
		{"RootConfig", RootConfig, filepath.Join(absHome, "config")},
		{"RootState", RootState, filepath.Join(absHome, "state")},
		{"RootPosts", RootPosts, filepath.Join(absHome, "posts")},
		{"RootStaging", RootStaging, filepath.Join(absHome, "staging")},
		{"RootStatic", RootStatic, filepath.Join(absHome, "static")},
		{"RootPublic", RootPublic, filepath.Join(absHome, "public")},
		{"DirImagePost", DirImagePost, filepath.Join(absHome, "static", "image", "post")},
		{"DirBackground", DirBackground, filepath.Join(absHome, "static", "image", "background")},
		{"FileConfig", FileConfig, filepath.Join(absHome, "config", "site.json")},
		{"FileFriendData", FileFriendData, filepath.Join(absHome, "config", "friend.json")},
		{"FileEventJSONL", FileEventJSONL, filepath.Join(absHome, "state", "events.jsonl")},
		{"FileFontsData", FileFontsData, filepath.Join(absHome, "state", "fonts.json")},
	}
	for _, c := range deploymentCases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	servedCases := []struct{ name, got, want string }{
		{"RootRender", RootRender, absServed},
		{"RootAssets", RootAssets, filepath.Join(absServed, "assets")},
		{"DirFont", DirFont, filepath.Join(absServed, "assets", "font")},
	}
	for _, c := range servedCases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}

	engineCases := []struct{ name, got, want string }{
		{"RootSrc", RootSrc, "src"},
		{"RootTemplates", RootTemplates, "templates"},
	}
	for _, c := range engineCases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (engine roots must not follow either configurable root)", c.name, c.got, c.want)
		}
	}
}

// TestStagingRootIsOutsideEveryServedTree: only assets/, static/ and public/ have a URL
// prefix. staging/ holds submissions no moderator has approved, so it must stay outside
// all three — nested inside one, an unreviewed upload is public the moment it lands.
func TestStagingRootIsOutsideEveryServedTree(t *testing.T) {
	dir := t.TempDir()
	InitRoots(filepath.Join(dir, "home"), filepath.Join(dir, "served"))
	t.Cleanup(func() { InitRoots("") })

	for _, served := range []struct{ name, root string }{
		{"assets", RootAssets},
		{"static", RootStatic},
		{"public", RootPublic},
	} {
		rel, err := filepath.Rel(served.root, RootStaging)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			t.Errorf("staging root %q sits inside %s/ (%q), which is served by URL",
				RootStaging, served.name, served.root)
		}
	}
}

// TestResolveHomeFlagPrecedence checks the -dir flag beats SILPHUU_HOME.
func TestResolveHomeFlagPrecedence(t *testing.T) {
	t.Setenv("SILPHUU_HOME", "/from/env")
	if got := resolveHomeFlag("/from/flag"); got != "/from/flag" {
		t.Errorf("resolveHomeFlag(flag set) = %q, want the flag value", got)
	}
	if got := resolveHomeFlag(""); got != "/from/env" {
		t.Errorf("resolveHomeFlag(empty) = %q, want the env value", got)
	}
	t.Setenv("SILPHUU_HOME", "")
	if got := resolveHomeFlag(""); got != "" {
		t.Errorf("resolveHomeFlag(nothing set) = %q, want empty", got)
	}
}

// TestDataReadPathFallsBackToEngineDefaults is the behaviour that makes an
// overlay usable: a deployment directory only needs the files it overrides.
// Without the fallback, a home containing just site.json renders a 500 because
// ui_strings.json is missing.
func TestDataReadPathFallsBackToEngineDefaults(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Only site.json is overridden; everything else must come from the engine.
	ownConfig := filepath.Join(home, "config", "site.json")
	if err := os.WriteFile(ownConfig, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	InitRoots(home, home)
	t.Cleanup(func() { InitRoots("") })

	if got := dataReadPath(FileConfig); got != ownConfig {
		t.Errorf("dataReadPath(config) = %q, want the deployment's own copy %q", got, ownConfig)
	}

	// ui_strings.json does not exist in the deployment -> engine default.
	if _, err := os.Stat(FileUIStrings); err == nil {
		t.Fatalf("test precondition broken: %s unexpectedly exists in the temp home", FileUIStrings)
	}
	got := dataReadPath(FileUIStrings)
	want := filepath.Join("config", "ui_strings.json")
	if got != want {
		t.Errorf("dataReadPath(ui_strings) = %q, want the engine default %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("engine default %q is not readable: %v", got, err)
	}
}
