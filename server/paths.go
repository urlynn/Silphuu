package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Path resolution: every on-disk path is relative to one of three roots.
//
// engineRoot is the release directory (CSS, JS, fonts, icons, built-in templates;
// never user data). homeRoot is the deployment directory — configuration, posts, state,
// and the site's own media under static/. staticRoot is what a static web server serves:
// the rendered pages and the built assets, both of which the engine writes itself.
//
// homeRoot and staticRoot are separate roots rather than one, and the engine writes
// straight into the served layout instead of projecting into it. Everything under
// staticRoot is reachable by URL, so config/ (the comment-signing salt), state/ (the comment
// log) and staging/ (unreviewed submissions) live in homeRoot, outside it. static/ and
// public/ are the other half of that split: the site's own content, which has to be
// reachable by URL without a second copy existing — the served tree holds a link to it
// instead, kept in place by links.go. The two roots must not contain one another;
// main() refuses to start otherwise.
//
// engineRoot is deliberately not configurable: a deployment lacking those files
// cannot serve a page at all.

// runtimePaths are the roots a deployment owns that are not engine content, plus the state
// files the engine writes. None of them may reach the public repo, and the guard derives
// from this list. Most are deployment-relative; assets/, render/ and public/ follow the
// served root instead, and are listed because a deployment whose roots fold into the engine
// tree would otherwise grow them inside the repository.
//
// config/ is absent on purpose — the filter releases it wholesale.
var runtimePaths = []string{
	"assets",
	"render",
	"public",
	"static",
	"posts",
	"Fonts",
	"staging",
	"state/fonts.json",
	"state/events.jsonl",
	"state/events.jsonl.bak",
	"state/events.log",
	"state/views.json",
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
	// staticRoot follows the same rule: resolved during package initialisation, then
	// re-resolved by InitRoots once the flags are parsed.
	staticRoot = envStatic()
)

// envHome returns the deployment root from SILPHUU_HOME, or a scratch directory under the
// user cache. It is never the engine tree.
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

// envStatic returns the served root from SILPHUU_STATIC, or "" when it is unset.
func envStatic() string {
	h := strings.TrimSpace(os.Getenv("SILPHUU_STATIC"))
	if h == "" {
		return ""
	}
	if abs, err := filepath.Abs(h); err == nil {
		return abs
	}
	return h
}

// defaultStaticRoot names the served root for a deployment that configured none: a sibling of
// the deployment root. Sibling rather than child, because everything under the served root is
// reachable by URL — a child would put config/ and state/ behind one.
func defaultStaticRoot(home string) string {
	if home == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(home), filepath.Base(home)+"-static")
}

// envSettingsDir names the settings directory environment variable.
const envSettingsDir = "SILPHUU_SETTINGS_DIR"

// defaultSettingsDir is where settings land when SILPHUU_SETTINGS_DIR names no directory: the
// conventional location for a service's own variable data. A deployment that wants them
// elsewhere — a container with a read-only /var, say — names the directory instead.
const defaultSettingsDir = "/var/lib/silphuu"

// envSettings returns the settings directory from SILPHUU_SETTINGS_DIR, falling back to the
// conventional location.
func envSettings() string {
	h := strings.TrimSpace(os.Getenv(envSettingsDir))
	if h == "" {
		return defaultSettingsDir
	}
	if abs, err := filepath.Abs(h); err == nil {
		return abs
	}
	return h
}

// settingsFilePath joins a name onto the settings root. Every settings file is named this way,
// so adding one never needs another environment variable.
func settingsFilePath(name string) string {
	return filepath.Join(RootSettings, name)
}

// homePath joins path elements onto the deployment root.
func homePath(parts ...string) string {
	return filepath.Join(append([]string{homeRoot}, parts...)...)
}

// staticPath joins path elements onto the served root.
func staticPath(parts ...string) string {
	return filepath.Join(append([]string{staticRoot}, parts...)...)
}

// enginePath joins path elements onto the engine (release) root.
func enginePath(parts ...string) string {
	return filepath.Join(append([]string{engineRoot}, parts...)...)
}

// InitRoots resolves the deployment and served roots and recomputes every derived path.
// An empty home falls back to SILPHUU_HOME, then to the scratch default. An empty static
// falls back to SILPHUU_STATIC, then to a sibling of the deployment root.
func InitRoots(home string, static ...string) {
	homeRoot = resolveRoot(home, envHome)
	explicitStatic := ""
	if len(static) > 0 {
		explicitStatic = static[0]
	}
	staticRoot = resolveRoot(explicitStatic, envStatic)
	if staticRoot == "" {
		staticRoot = defaultStaticRoot(homeRoot)
	}
	RootSettings = resolveRoot("", envSettings)
	initDerivedPaths()
	// Parsed icons are keyed by their path relative to src/, so they are only valid for
	// the home they were read from.
	resetSiteIconCache()
	log.Printf("paths: engine=%s home=%s static=%s settings=%s", engineRoot, homeRoot, staticRoot, RootSettings)
	if line := overlayLogLine(); line != "" {
		log.Print(line)
	}
}

// resolveRoot applies a flag value, then the environment, then the built-in default.
func resolveRoot(value string, fallback func() string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback()
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		log.Printf("paths: cannot resolve %q (%v); falling back to the default root", value, err)
		return fallback()
	}
	return abs
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

// resolveStaticFlag returns the served root from the command line, falling back to the
// environment. Returns "" when neither is set.
func resolveStaticFlag(static string) string {
	if strings.TrimSpace(static) != "" {
		return static
	}
	return os.Getenv("SILPHUU_STATIC")
}

// checkRootsDisjoint rejects a served root that contains the deployment root, or sits inside
// it. The served tree reaches the site's own static/ and public/ through links (links.go), so
// one deliberate crossing exists; nesting the roots wholesale would erase the distinction
// instead, and then "which root owns this path" has no answer to give.
func checkRootsDisjoint(home, static string) error {
	if home == "" || static == "" {
		return nil
	}
	if pathContains(static, home) || pathContains(home, static) {
		return fmt.Errorf("the served root (%s) and the deployment root (%s) must not contain one another; pass -static/SILPHUU_STATIC outside the deployment root", static, home)
	}
	return nil
}

// checkSettingsOutsideEngine rejects a settings directory inside the engine tree. The engine
// tree is what the release is filtered from, so a settings file written there would be
// published along with the sources.
func checkSettingsOutsideEngine() error {
	if pathContains(engineRoot, RootSettings) {
		return fmt.Errorf("the settings directory (%s) is inside the engine tree (%s); pass SILPHUU_SETTINGS_DIR outside it", RootSettings, engineRoot)
	}
	return nil
}

// pathContains reports whether outer is inner, or an ancestor of it. Symlinks are resolved
// when the paths exist, so a root reached through a link is compared where it lands.
func pathContains(outer, inner string) bool {
	o, errO := filepath.Abs(outer)
	i, errI := filepath.Abs(inner)
	if errO != nil || errI != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(o); err == nil {
		o = resolved
	}
	if resolved, err := filepath.EvalSymlinks(i); err == nil {
		i = resolved
	}
	if o == i {
		return true
	}
	rel, err := filepath.Rel(o, i)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
	if staticRoot == "" {
		staticRoot = defaultStaticRoot(homeRoot)
	}

	RootSrc = enginePath("src")
	RootTemplates = enginePath("templates")

	// assets/ is built from src/ and lives under the served root, because a browser fetches
	// it. The engine's own src/ stays read-only, and a site's overrides live in its own src/.
	RootAssets = staticPath("assets")

	// The served root holds everything reachable by URL, and pages are written straight into
	// it: the served layout is the layout below, with no publishing step between them.
	RootRender = staticRoot
	// public/ holds the site's own root-level files: the favicon, and anything a crawler or a
	// registrar asks to be served at /. It belongs to the site, so it lives in the private
	// root, and the served tree holds one link per file, by name — see links.go.
	RootPublic = homePath("public")
	// static/ is the site's own media: uploads, and the images the site ships. It belongs to
	// the site, so it lives in the private root, and the served tree reaches it through the
	// link ensureLinks keeps in place. One set of bytes, two names.
	RootStatic = homePath("static")
	RootError = filepath.Join(RootRender, "error")

	// config/ holds the site's configuration — hand-written, or edited through the admin UI.
	// state/ holds what the engine measures, records or counts on its own. Both stay outside
	// the served root: config/site.json carries the comment-signing salt, state/ the comment log.
	RootConfig = homePath("config")
	RootState = homePath("state")
	RootPosts = homePath("posts")
	RootFontSources = homePath("Fonts")

	// staging/ holds visitor submissions until a moderator moves them into the
	// site. Nothing serves or publishes this root, so an unreviewed file has no
	// URL even though it is already on disk.
	RootStaging = homePath("staging")

	// Media directories (all singular names)
	DirImage = filepath.Join(RootStatic, "image")
	DirImagePost = filepath.Join(RootStatic, "image", "post")
	DirImageComment = filepath.Join(RootStatic, "image", "comment")
	DirPhoto = filepath.Join(RootStatic, "photo")
	DirSticker = filepath.Join(RootStatic, "sticker")
	DirBackground = filepath.Join(RootStatic, "image", "background")
	DirSponsor = filepath.Join(RootStatic, "image", "sponsor")

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
	FileHeroData = filepath.Join(RootConfig, "hero.json")
	FileFontMap = filepath.Join(RootConfig, "font_map.json")
	FileFontExtraChars = filepath.Join(RootConfig, "font_extra_chars.json")
	DirPresets = filepath.Join(RootConfig, "presets")

	// State: what the engine measures, records or counts on its own.
	FileEventLog = filepath.Join(RootState, "events.log")
	FileEventJSONL = filepath.Join(RootState, "events.jsonl")
	FileSyncState = filepath.Join(RootState, "sync-state.json")
	FileSnapshot = filepath.Join(RootState, "snapshot.json")
	FileFontsData = filepath.Join(RootState, "fonts.json")

	// Settings: what the administrator edits through the admin UI and that belongs to the
	// server rather than to the site. Outside both roots above.
	FileNotifyRoutes = settingsFilePath("notify.json")

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
