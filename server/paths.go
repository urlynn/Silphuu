package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Path resolution: every on-disk path is relative to one of two roots.
// engineRoot is the release directory (CSS, JS, fonts, icons, built-in templates;
// never user data). homeRoot is the deployment directory (configuration, posts,
// uploaded media, render cache), overridable via -dir or SILPHUU_HOME so personal
// content stays out of the repository. engineRoot is deliberately not configurable:
// a deployment lacking those files cannot serve a page at all.
// See docs/RELEASE-AND-OVERLAY.md.

// runtimePaths are the deployment-relative paths the engine writes at runtime, plus the
// scratch roots a deployment owns. None is engine content, so none may reach the public
// repo: the guard derives from this list, and every path here sits outside the roots the
// filter releases.
//
// config/ is absent on purpose — the filter releases it wholesale.
// See docs/GOTCHAS.md §runtime-output-roots.
var runtimePaths = []string{
	"assets",
	"render",
	"public",
	"publish",
	"static",
	"posts",
	"Fonts",
	"staging",
	"state/fonts.json",
	"state/events.jsonl",
	"state/events.log",
	"state/views-local.json",
	"state/sync-state.json",
	"state/snapshot.json",
}

var (
	engineRoot = "."
	// homeRoot is resolved from SILPHUU_HOME during package initialisation, not
	// in main(), because package-level init() functions already read data files
	// (UI strings, templates). The -dir flag cannot be seen that early, so main()
	// calls InitRoots again and reloads whatever depends on the paths.
	homeRoot = envHome()
)

// envHome returns the deployment root from SILPHUU_HOME, or a scratch directory under the
// user cache. It is never the engine tree — see docs/GOTCHAS.md §runtime-output-roots.
func envHome() string {
	if h := strings.TrimSpace(os.Getenv("SILPHUU_HOME")); h != "" {
		if abs, err := filepath.Abs(h); err == nil {
			return abs
		}
		return h
	}
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "silphuu", "home")
	}
	return filepath.Join(os.TempDir(), "silphuu-home")
}

// homePath joins path elements onto the deployment root.
func homePath(parts ...string) string {
	return filepath.Join(append([]string{homeRoot}, parts...)...)
}

// enginePath joins path elements onto the engine (release) root.
func enginePath(parts ...string) string {
	return filepath.Join(append([]string{engineRoot}, parts...)...)
}

// InitRoots resolves the deployment root and recomputes every derived path.
// An empty home falls back to SILPHUU_HOME, then to the scratch default.
func InitRoots(home string) {
	home = strings.TrimSpace(home)
	if home == "" {
		homeRoot = envHome()
	} else if abs, err := filepath.Abs(home); err != nil {
		log.Printf("paths: cannot resolve %q (%v); falling back to the default root", home, err)
		homeRoot = envHome()
	} else {
		homeRoot = abs
	}
	initDerivedPaths()
	// Parsed icons are keyed by their path relative to src/, so they are only valid for
	// the home they were read from.
	resetSiteIconCache()
	log.Printf("paths: engine=%s home=%s", engineRoot, homeRoot)
	if line := overlayLogLine(); line != "" {
		log.Print(line)
	}
}

// ReloadPathDependentCaches re-reads everything that was loaded during package
// initialisation from the (then-default) roots. Call it after InitRoots changes
// the deployment directory, or the process keeps serving the old site's strings
// and templates.
func ReloadPathDependentCaches() {
	loadUIStrings()
	initTemplates()
}

// resolveHomeFlag returns the deployment root from the command line, falling
// back to the environment. Returns "" when neither is set.
func resolveHomeFlag(dir string) string {
	if strings.TrimSpace(dir) != "" {
		return dir
	}
	return os.Getenv("SILPHUU_HOME")
}

// sameDir reports whether two paths name the same directory. Symlinks are resolved when
// the path exists, so a deployment root reached through a link still compares equal.
func sameDir(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absA); err == nil {
		absA = resolved
	}
	if resolved, err := filepath.EvalSymlinks(absB); err == nil {
		absB = resolved
	}
	return absA == absB
}

// dataReadPath returns the path to READ a file from: the deployment's own copy
// when it exists, otherwise the release copy.
//
// Writes always target the own path (the File* variables), so a deployment only
// needs to contain the files it actually overrides — everything else keeps
// working against the shipped defaults. Without this fallback a deployment would
// have to be a complete copy of config/ just to render a page.
//
// Note this is a plain "present wins, absent falls back" rule, not a merge: the
// returned file is used as-is.
func dataReadPath(own string) string {
	if _, err := os.Stat(own); err == nil {
		return own
	}
	// Default layout: the own path already IS the engine path, so there is
	// nothing to fall back to.
	if homeRoot == "." {
		return own
	}
	rel, err := filepath.Rel(homeRoot, own)
	if err != nil || strings.HasPrefix(rel, "..") {
		return own
	}
	engineDefault := filepath.Join(engineRoot, rel)
	if engineDefault != own {
		if _, err := os.Stat(engineDefault); err == nil {
			return engineDefault
		}
	}
	return own
}

// pathsInitialized computes the derived paths during package variable
// initialisation.
//
// This deliberately is NOT a func init(): Go runs init() functions in
// file-name order within a package, so a `paths.go` init would run *after*
// main.go's init, which already loads templates and UI strings. Package-level
// variables, by contrast, are all initialised before any init() runs, so this
// form guarantees the paths are ready regardless of file naming.
var _ = func() bool {
	initDerivedPaths()
	return true
}()

// initDerivedPaths recomputes every root and derived path from engineRoot and
// homeRoot. Called during package initialisation and again from InitRoots to
// apply a configured deployment directory.
func initDerivedPaths() {
	// The roots' own initialisers may not have run yet depending on ordering;
	// fall back to the default working-directory layout.
	if engineRoot == "" {
		engineRoot = "."
	}
	if homeRoot == "" {
		homeRoot = "."
	}

	RootSrc = enginePath("src")
	RootTemplates = enginePath("templates")

	// assets/ is built from src/ and belongs to the deployment: the engine's own
	// directory stays read-only, and a site's overrides live in its own src/.
	RootAssets = homePath("assets")

	// config/ holds the site's configuration — hand-written, or edited through the admin
	// UI. state/ holds what the engine measures, records or counts on its own. Splitting
	// them is what lets the filter release config/ wholesale without ever publishing state/.
	RootConfig = homePath("config")
	RootState = homePath("state")
	RootRender = homePath("render")
	RootPublic = homePath("public")
	RootPublish = homePath("publish")
	RootPosts = homePath("posts")
	RootStatic = homePath("static")
	RootError = filepath.Join(RootRender, "error")
	RootFontSources = homePath("Fonts")

	// staging/ holds visitor submissions until a moderator moves them into the
	// site. Nothing serves or publishes this root, so an unreviewed file has no
	// URL even though it is already on disk.
	RootStaging = homePath("staging")

	// Media directories (all singular names)
	DirImagePost = filepath.Join(RootStatic, "image", "post")
	DirImageComment = filepath.Join(RootStatic, "image", "comment")
	DirPhoto = filepath.Join(RootStatic, "photo")
	DirSticker = filepath.Join(RootStatic, "sticker")
	DirBackground = filepath.Join(RootAssets, "image", "background")
	DirSponsor = filepath.Join(RootAssets, "image", "sponsor")

	// Thumbnail directories
	DirThumbRoot = filepath.Join(RootAssets, "thumb")
	DirThumbPhotoBlur = filepath.Join(RootAssets, "thumb", "photo", "blur")
	DirThumbStickerRoot = filepath.Join(RootAssets, "thumb", "sticker")

	// Font directory
	DirFont = filepath.Join(RootAssets, "font")

	// Configuration: hand-written, or edited through the admin UI.
	FileConfig = filepath.Join(RootConfig, "site.json")
	FileSocial = filepath.Join(RootConfig, "social.json")
	FileStickerData = filepath.Join(RootConfig, "sticker.json")
	FileTopicData = filepath.Join(RootConfig, "topic.json")
	FilePhotoData = filepath.Join(RootConfig, "photo.json")
	FileBackgroundData = filepath.Join(RootConfig, "background.json")
	FileSponsorData = filepath.Join(RootConfig, "sponsor.json")
	FileFriendData = filepath.Join(RootConfig, "friend.json")
	FileUIStrings = filepath.Join(RootConfig, "ui_strings.json")
	FileUIStringsAdmin = filepath.Join(RootConfig, "ui_strings_admin.json")
	FilePreviewFixtures = filepath.Join(RootConfig, "preview_fixtures.json")
	FileNavSignatures = filepath.Join(RootConfig, "nav_signatures.json")
	FileFontMap = filepath.Join(RootConfig, "font_map.json")
	FileFontExtraChars = filepath.Join(RootConfig, "font_extra_chars.json")
	DirPresets = filepath.Join(RootConfig, "presets")

	// State: what the engine measures, records or counts on its own.
	FileEventLog = filepath.Join(RootState, "events.log")
	FileEventJSONL = filepath.Join(RootState, "events.jsonl")
	FileSyncState = filepath.Join(RootState, "sync-state.json")
	FileSnapshot = filepath.Join(RootState, "snapshot.json")
	FileFontsData = filepath.Join(RootState, "fonts.json")

	// Staging directories, one per flow that accepts visitor submissions.
	DirStagingFriend = filepath.Join(RootStaging, "friend")
	DirStagingSticker = filepath.Join(RootStaging, "sticker")
}

// srcReadPath resolves an authored file under src/, preferring the deployment's own copy.
// `rel` is relative to src/ — e.g. "css/fonts.css" or "js/status.js".
//
// Callers read source at build time: bundling, the theme scan, glyph collection. src/ is
// never served, so nothing here needs a URL.
func srcReadPath(rel string) string {
	if homeRoot != "." {
		if own := filepath.Join(homeRoot, "src", rel); fileExists(own) {
			return own
		}
	}
	return filepath.Join(RootSrc, rel)
}

// homeSrcPath is where an uploaded icon is written. The deployment's own src/ is the
// tree that wins over the engine's, so a site's icon must land there rather than in the
// release directory it happens to be running from.
func homeSrcPath(parts ...string) string {
	return filepath.Join(append([]string{homeRoot, "src"}, parts...)...)
}

// assetSourceTrees lists the src/ roots to consider, lowest priority first: the engine's
// sources, then the deployment's own laid over them.
func assetSourceTrees() []string {
	return dedupePaths(RootSrc, filepath.Join(homeRoot, "src"))
}
