package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"html/template"
	"log"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
)

// Types.

var maxPostID int

type Post struct {
	ID    int
	Slug  string
	Title string
	// Category is the display name. Templates print it and key CategoryAliases by it, so
	// every producer must fill it from the registry — never from the posts/ directory name.
	// — see docs/GOTCHAS.md §post-category-display-name
	Category   string
	Tags       []string
	Date       time.Time
	UpdatedAt  time.Time
	Content    template.HTML
	Summary    template.HTML
	SummaryStr string
	Body       string
	Words      int
	Views      int
}

type PageData struct {
	Title         string
	Nickname      string
	Motto         string
	HomeMotto     string
	Posts         []Post
	CurrentPost   *Post
	Categories    []string
	AllTags       []string
	CurrentCat    string
	CategoryPosts []CategoryWithPosts
	Year          int
	Backgrounds   []BgItem
	Interval      int
	Mode          string
	// HeroBg is the home-selected background image (with anchor/tone); only home.html uses it
	HeroBg BgItem
	// HeroBgMobile is the narrow-viewport background. Which of the two a visitor gets is
	// decided by a CSS media query, never by the server — see pickHomeBackgrounds.
	// Equal to HeroBg when no mobile background is configured.
	HeroBgMobile    BgItem
	IsAdmin         bool
	IsPreview       bool
	Flash           string
	Sponsors        []Sponsor
	Friends         []FriendLink
	FriendPending   []FriendApplication
	PostComments    []Comment
	Sort            string
	Photos          []Photo
	ArchiveGroups   []ArchiveGroup
	IsArchive       bool
	IsList          bool // /posts: the whole post set, filtered only by the browser
	TopicDescs      map[string]string
	CategoryAliases map[string]string
	// CategoryOptions is the editor dropdown's source: the registry, key and display name.
	CategoryOptions []Category
	VisitStats      VisitStats
	Theme           string
	NavLabel        string
	HTMXRequest     bool
	BaseURL         string
	PinnedPost      *Post
	LatestPost      *Post
	PinnedPostID    int
	// UI is the single data source of static UI strings (config/ui_strings.json +
	// config/ui_strings_admin.json): a nested page->module->slug->{text,role} tree; templates
	// reference it as {{.UI.page.module.slug}}.
	// role is machine metadata for font subsetting only; humans read by page/module
	// (organized by page/module, not by font role).
	UI map[string]interface{}
}

type ArchiveGroup struct {
	Year   int
	Posts  []Post
	Months []ArchiveMonth
	Open   bool
}

type ArchiveMonth struct {
	Month int
	Posts []Post
}

type CategoryWithPosts struct {
	Name  string
	Posts []Post
	Open  bool
}

type BgConfig struct {
	Backgrounds []BgItem `json:"backgrounds"`
	Interval    int      `json:"interval"`
	Mode        string   `json:"mode"`
}

// BgItem describes a background image and its hero layout parameters.
type BgItem struct {
	Src       string  `json:"src"`
	Device    string  `json:"device"`     // "desktop" | "mobile" (required)
	Tone      string  `json:"tone"`       // "light" | "dark" (required)
	OffsetX   float64 `json:"offset_x"`   // card center relative to hero width, 0~1
	OffsetY   float64 `json:"offset_y"`   // card center relative to hero height, 0~1
	AvatarPos string  `json:"avatar_pos"` // "left" | "right" (avatar on the card's left/right)
}

// normalizedDevice returns a valid device value (invalid or empty -> "desktop").
func (b BgItem) normalizedDevice() string {
	if b.Device == "mobile" {
		return "mobile"
	}
	return "desktop"
}

// normalizedTone returns a valid tone value (invalid or empty -> "light").
func (b BgItem) normalizedTone() string {
	if b.Tone == "dark" {
		return "dark"
	}
	return "light"
}

// normalizedAvatarPos returns a valid avatar position (invalid or empty -> "left").
func (b BgItem) normalizedAvatarPos() string {
	if b.AvatarPos == "right" {
		return "right"
	}
	return "left"
}

// AppConfig is the hand-written half of the site configuration, persisted in
// config/site.json. Every field here is something a person types.
//
// Machine-generated font measurements deliberately do NOT live here: they are
// written wholesale by the font workshop and belong in state/fonts.json (see
// fontState in config_store.go). Mixing the two in one file meant every font
// swap rewrote the human fields and every hand edit risked clobbering the
// measurements. The fields are kept on this struct as the in-memory merged view,
// so callers keep reading cfg.FontPresets without caring where it came from.
type AppConfig struct {
	Password  string `json:"password"`
	Author    string `json:"author"`
	SiteName  string `json:"site_name"`
	Motto     string `json:"motto"`
	HomeMotto string `json:"home_motto"`
	// Credit / CreditURL are the deployment's own footer credit, rendered on the same
	// line as the project's attribution. A deployment that wants its own name there
	// gets it without removing the project's — the two are peers.
	Credit       string `json:"credit"`
	CreditURL    string `json:"credit_url"`
	BaseURL      string `json:"base_url"`
	ESASiteID    int64  `json:"esa_site_id"`
	PinnedPostID int    `json:"pinned_post_id"`

	// Machine-generated; persisted in state/fonts.json, never in site.json.
	FontPresets      []FontPreset `json:"-"`
	ActiveFontPreset string       `json:"-"`
}

// FontMetrics stores pre-measured font metrics (measured on save in admin, read directly
// at runtime, no canvas work).
type FontMetrics struct {
	Shift        float64 `json:"shift"`          // ink center offset (em)
	InkHeight    float64 `json:"ink_height"`     // actual ink height / font-size (em)
	InkWeight    float64 `json:"ink_weight"`     // average stroke weight / font-size (em); measured canvas coverage, not the font's declared weight
	InkLeftDelta float64 `json:"ink_left_delta"` // fixed left-edge ink delta between CJK and Latin (em, positive = CJK further right); measured via browser Canvas, used for status dropdown alignment
	// Latin supplement: the **Latin text's own** shift / inkHeight for the same font
	// (sample 'Hx0', no descenders).
	// Use case: Latin text running in a CJK font (e.g. a comment's UA text rendered with
	// --font-caption but purely Latin) — compensation computed from CJK samples does not
	// apply to it. Measured and written by assets/js/font-metrics-measure.js.
	ShiftLatin     float64 `json:"shift_latin"`
	InkHeightLatin float64 `json:"ink_height_latin"`
	// CJK/kana mixed-text compensation calibration: ink center (em above the baseline,
	// textBaseline convention) and per-char advance (em).
	// Use case: after measuring both the display_i18n (hanzi = LemiMuhe) and
	// display_i18n_jp (kana = MapleMono) roles, buildInkVarsCSS derives the kana run's
	// scale/shift/tracking so both segments' ink aligns strictly (Δ≤0.001em).
	InkCenter  float64 `json:"ink_center"`
	InkAdvance float64 `json:"ink_advance"`
}

// FontPreset describes one font preset: each element (display/body/mono/serif) picks its
// own font independently.
type FontPreset struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Fonts      map[string]string      `json:"fonts"`                 // key: display/body/mono/serif -> font-family CSS value
	Metrics    map[string]FontMetrics `json:"metrics,omitempty"`     // measured on save in admin, read directly at runtime
	MetricsVer int                    `json:"metrics_ver,omitempty"` // metrics version: 2=DOM-measured, 1/empty=canvas
	// JPMarginLeft: left-edge compensation of the display_i18n_jp kana run (em, value
	// frozen from the measurement workbench).
	// Maple's kana glyphs have more built-in left side bearing than LemiMuhe's hanzi, and
	// tracking only affects what follows a character, not the run's left edge — hence a
	// separate constant. Not in Metrics: it is a two-font boundary pairing quantity, not
	// measurable on a single font via canvas; putting it in Metrics would get overwritten
	// by admin re-measurement.
	JPMarginLeft float64 `json:"jp_margin_left,omitempty"`
}

type BgEntry struct {
	URL    string
	Name   string
	Device string
	Tone   string
}

type Sponsor struct {
	Date   string `json:"date"`
	Nick   string `json:"nick"`
	Amount string `json:"amount"`
	Msg    string `json:"msg"`
}

type FriendLink struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Desc  string `json:"desc"`
	Icon  string `json:"icon"`
	Tag   string `json:"tag"`
	Color string `json:"color,omitempty"`
}

type PhotoLabel struct {
	Text    string `json:"text"`
	Kaomoji string `json:"kaomoji"`
}

type Photo struct {
	ID    string     `json:"id"`
	Color string     `json:"color"`
	Label PhotoLabel `json:"label"`
	Order int        `json:"order"`
}

type Comment struct {
	ID      string `json:"id"` // ULID, globally unique (two nodes, no coordinator)
	Nick    string `json:"nick"`
	Email   string `json:"email"`
	Website string `json:"website"`
	Avatar  string `json:"avatar"` // visitor-supplied image URL; "" renders the site default
	Content string `json:"content"`
	Date    string `json:"date"`
	Rid     string `json:"rid"` // parent comment ULID; "" = top level
	Likes   int    `json:"likes"`
	PostID  int    `json:"post_id"` // 0 = guestboard
	UA      string `json:"ua"`
}

// RenderedContent returns the Markdown-rendered comment content (template helper).
func (c *Comment) RenderedContent() template.HTML {
	return renderMarkdown(c.Content)
}

// UAShort extracts a short "browser version · OS" summary from the User-Agent.
func (c *Comment) UAShort() string {
	ua := c.UA
	if ua == "" {
		return ""
	}

	// Browser
	browser := ""
	switch {
	case strings.Contains(ua, "Chrome/") && !strings.Contains(ua, "Edg/") && !strings.Contains(ua, "OPR/"):
		browser = "Chrome " + extractAfter(ua, "Chrome/")
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox " + extractAfter(ua, "Firefox/")
	case strings.Contains(ua, "Edg/"):
		browser = "Edge " + extractAfter(ua, "Edg/")
	case strings.Contains(ua, "Safari/") && !strings.Contains(ua, "Chrome/"):
		browser = "Safari " + extractAfter(ua, "Safari/")
	default:
		browser = ua
	}

	// Operating system
	os := ""
	switch {
	case strings.Contains(ua, "iPhone") || strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Macintosh") || strings.Contains(ua, "Mac OS X"):
		os = "macOS " + extractMacOSVersion(ua)
	case strings.Contains(ua, "Windows NT"):
		os = "Windows " + extractAfter(ua, "Windows NT ")
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}

	if browser != "" && os != "" {
		return browser + " · " + os
	}
	if browser != "" {
		return browser
	}
	return os
}

func extractAfter(s, prefix string) string {
	i := strings.Index(s, prefix)
	if i < 0 {
		return ""
	}
	rest := s[i+len(prefix):]
	end := strings.IndexAny(rest, " ;)")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

func extractMacOSVersion(ua string) string {
	i := strings.Index(ua, "Mac OS X ")
	if i < 0 {
		// try "like Mac OS X" pattern (Safari style)
		i = strings.Index(ua, "like Mac OS X")
		if i < 0 {
			return ""
		}
		// Safari format: "Mac OS X 10_15_7" -> extract the version part
		rest := ua[i+len("like Mac OS X"):]
		rest = strings.TrimSpace(rest)
		end := strings.IndexAny(rest, " ;)")
		if end < 0 {
			return strings.ReplaceAll(strings.TrimSpace(rest), "_", ".")
		}
		return strings.ReplaceAll(strings.TrimSpace(rest[:end]), "_", ".")
	}
	rest := ua[i+len("Mac OS X "):]
	end := strings.IndexAny(rest, " ;)")
	if end < 0 {
		return strings.ReplaceAll(strings.TrimSpace(rest), "_", ".")
	}
	return strings.ReplaceAll(strings.TrimSpace(rest[:end]), "_", ".")
}

// Embedded SVG icons.

//go:embed src/icon/*.svg
var iconsFS embed.FS

// icon-ink-map.json: "visual center" offsets of svg icons (unit = percentage of the
// icon's own height).
// Independent of the font ink variables: icons have no font role, and their visual center
// ≠ geometric center (a calendar's ring or a comment bubble's tail shifts the geometric
// center).
// Values are an alpha-weighted centroid of each icon's raster, not a bounding-box center.
// Applied ONLY to icons that explicitly declare `data-ink-center` — a large offset does
// not automatically mean it should be compensated (e.g. the search magnifier is offset
// 4.78% by design and must never be compensated).
//
//go:embed src/icon-ink-map.json
var iconInkMapFS embed.FS

var iconInkShift map[string]float64

var iconSVGs map[string]template.HTML

func init() {
	// The icon visual-center table MUST be loaded **before** the icon loop below —
	// the loop looks up this table, and an empty one injects nothing: every icon then
	// renders uncompensated and nothing in the logs says so.
	iconInkShift = make(map[string]float64)
	if data, err := iconInkMapFS.ReadFile("src/icon-ink-map.json"); err != nil {
		log.Printf("warn: icon-ink-map.json not found, icon ink centering falls back to 0: %v", err)
	} else if err := json.Unmarshal(data, &iconInkShift); err != nil {
		log.Printf("warn: parse icon-ink-map.json failed: %v", err)
	}

	iconSVGs = make(map[string]template.HTML)
	entries, _ := iconsFS.ReadDir("src/icon")
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".svg")
		data, err := iconsFS.ReadFile("src/icon/" + e.Name())
		if err != nil {
			continue
		}
		iconSVGs[name] = injectIcon(name, data)
	}
	// Warm the static UI strings (execute hot-reloads under DEV_MODE; non-DEV_MODE uses this cache)
	loadUIStrings()
}

// injectIcon is the single place an icon's markup is assembled, whether it came from the
// embedded set or from a deployment's own src/icon file.
//
// Visual-center compensation applies only to icons that declare data-ink-center: the value
// is injected as an inline --icon-shift (see icons.css), so undeclared icons cannot be
// affected by accident. A second reader that skipped this would render one icon two ways.
func injectIcon(name string, data []byte) template.HTML {
	extra := ""
	if strings.Contains(string(data), "data-ink-center") {
		if v, ok := iconInkShift[name]; ok {
			extra = fmt.Sprintf(` style="--icon-shift:%g%%"`, v)
		} else {
			log.Printf("warn: 图标 %q 声明了 data-ink-center 但 icon-ink-map.json 里没有它的测量值", name)
		}
	}
	return template.HTML(strings.Replace(string(data), "<svg", `<svg data-icon="`+name+`"`+extra, 1))
}

// siteIconSVG reads one icon from the deployment's src/, by its path relative to src/.
// The embedded set is fixed at build time, so an icon added afterwards — an uploaded
// friend-link logo, for one — exists only on disk. The path comes from configuration or
// from a template, so it must not leave a src/ root.
func siteIconSVG(rel string) (template.HTML, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("icon path is empty")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("icon path %q must be relative to src/", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("icon path %q escapes src/", rel)
	}

	siteIconMu.Lock()
	cached, ok := siteIconCache[clean]
	siteIconMu.Unlock()
	if ok {
		return cached, nil
	}

	path := srcReadPath(clean)
	data, err := os.ReadFile(path)
	if err != nil {
		// A miss is cached as well: short names now look here first, so without it a
		// deployment that overrides none would stat the disk for every icon on every page.
		siteIconMu.Lock()
		siteIconCache[clean] = ""
		siteIconMu.Unlock()
		return "", fmt.Errorf("icon %q is not readable (%s)", rel, path)
	}
	svg := injectIcon(strings.TrimSuffix(filepath.Base(clean), ".svg"), data)

	siteIconMu.Lock()
	siteIconCache[clean] = svg
	siteIconMu.Unlock()
	return svg, nil
}

var (
	siteIconMu    sync.Mutex
	siteIconCache = map[string]template.HTML{}
)

// ResolveIcon unifies icon lookups, combining the ease of short names (from friends.json)
// with the flexibility of full paths (from social.json).
func ResolveIcon(id string) (template.HTML, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("icon name/path is empty")
	}

	// If it looks like a path (contains a slash or .svg), resolve it directly in src/.
	if strings.Contains(id, "/") || strings.HasSuffix(strings.ToLower(id), ".svg") {
		return siteIconSVG(id)
	}

	// A short name is the deployment's to win: src/icon/<name>.svg overrides the engine's
	// embedded icon of the same name, so a site restyles an icon without editing a template.
	if svg, err := siteIconSVG("icon/" + id + ".svg"); err == nil && len(svg) > 0 {
		return svg, nil
	}

	if res, ok := iconSVGs[id]; ok && len(res) > 0 {
		return res, nil
	}
	return "", fmt.Errorf("icon %q is in neither the deployment's src/icon nor the engine", id)
}

// resetSiteIconCache drops the parsed icons so an edited SVG is picked up without a
// restart. The file watcher calls it when src/ changes.
func resetSiteIconCache() {
	siteIconMu.Lock()
	siteIconCache = map[string]template.HTML{}
	siteIconMu.Unlock()
}

// Config loaders.

// loadBgConfig returns the backgrounds the site operates on — the same list both
// rendering and the update-type write actions (set-offset / set-avatar / set-tone /
// delete / toggle-mode) read.
//
// A deployment that has never written config/background.json gets defaultBg; a file that
// exists and lists none is honoured as-is, and the home page then falls back to the
// engine's own default wallpaper (see engineDefaultBgItem).
//
// Append-type write actions (add / upload) must use loadStoredBgConfig instead —
// see there.
func loadBgConfig() BgConfig {
	cfg, stored := loadStoredBgConfig()
	if !stored {
		cfg = defaultBg()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 15
	}
	return cfg
}

// loadStoredBgConfig reads config/background.json exactly as stored — no placeholder.
// The bool reports whether a usable stored file was read at all.
//
// Append-type write actions (add / upload) must use this, not loadBgConfig: seeding
// storage with the placeholder makes an upload produce two entries, one of
// them pointing at a file that was never uploaded.
func loadStoredBgConfig() (BgConfig, bool) {
	data, err := os.ReadFile(FileBackgroundData)
	if err != nil {
		return BgConfig{}, false
	}
	var cfg BgConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return BgConfig{}, false
	}
	return cfg, true
}

// defaultBg is the configuration a deployment starts from: no background listed, so the
// hero falls back to the engine's own default wallpaper (see engineDefaultBgItem).
func defaultBg() BgConfig {
	return BgConfig{
		Interval: 15,
		Mode:     "sequential",
	}
}

func loadAppConfig() AppConfig {
	// Two files, two writers. site.json is hand-written and short enough to
	// replace wholesale; fonts.json is produced by the font workshop. Neither
	// writer can clobber the other's file.
	var cfg AppConfig
	if p := dataReadPath(FileConfig); p != "" {
		if data, err := os.ReadFile(p); err == nil {
			if err := json.Unmarshal(data, &cfg); err != nil {
				log.Printf("config: %s is not valid JSON: %v", p, err)
			}
		}
	}
	loadFontState(&cfg)
	if cfg.Author == "" {
		cfg.Author = "Your Name"
	}
	if cfg.Motto == "" {
		cfg.Motto = "把这里换成你的个人签名"
	}
	if cfg.HomeMotto == "" {
		cfg.HomeMotto = cfg.Motto
	}
	// No preset fallback here on purpose. The engine ships no fonts and declares no
	// @font-face, so an unconfigured site falls through to the system font stack
	// declared in tokens.css. A hardcoded preset would name fonts that may not exist
	// and apply shifts measured for them, which misaligns instead of aligning.
	if cfg.BaseURL == "" {
		cfg.BaseURL = os.Getenv("BASE_URL")
		if cfg.BaseURL == "" && os.Getenv("DEV_MODE") != "1" {
			cfg.BaseURL = "http://localhost"
		}
	}
	return cfg
}

// parseFontValGo parses "family|weight" or "family|weight|bevl" -> (family, weight, bevl)
func parseFontValGo(v string) (string, string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "", ""
	}
	parts := strings.Split(v, "|")
	if len(parts) >= 3 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	} else if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ""
	}
	return v, "", ""
}

// trimFontQuotes strips quotes from both sides of a font name
func trimFontQuotes(f string) string {
	return strings.Trim(f, "'\"")
}

// Ink alignment derived assets (in-memory cache): both the role-level :root variables and
// the element-level families table depend only on the "currently active font preset", not
// on page content — pure function derivatives. Both are injected inline into every page,
// so they are rebuilt in memory and never touch disk. Restart-safe: content can always be
// recomputed from config/site.json + embed tables.

// buildInkVarsCSS generates :root{} role-level variables (family/weight/shift/ink-height/
// ink-scale) from the active preset.
func buildInkVarsCSS(cfg AppConfig) string {
	var active FontPreset
	found := false
	for _, p := range cfg.FontPresets {
		if p.ID == cfg.ActiveFontPreset {
			active = p
			found = true
			break
		}
	}
	var b strings.Builder
	b.WriteString(":root{")
	if !found {
		// No preset configured: emit no font variables at all. The engine ships no fonts,
		// so the page runs on the system font stack from tokens.css, which needs no ink
		// compensation — every consumer reads var(--font-*-shift, 0em) and falls back to 0.
		// Writing the embed table here would inject shifts measured for fonts not in use.
		b.WriteString("}")
		return b.String()
	}
	// Font family / weight / bevl (decided server-side)
	// The cssBase below is the only place a config role key (snake_case) becomes a CSS variable
	// name (kebab-case), and the list is written out by hand — see docs/GOTCHAS.md §font-role-registry.
	setFont := func(cssBase, val string) {
		if fam, weight, bevl := parseFontValGo(val); fam != "" {
			b.WriteString(fmt.Sprintf("--font-%s:%s;", cssBase, fam))
			if weight != "" {
				b.WriteString(fmt.Sprintf("--font-%s-weight:%s;", cssBase, weight))
			}
			if bevl != "" {
				b.WriteString(fmt.Sprintf("--font-%s-bevl:%s;", cssBase, bevl))
			}
			// Page-group variants (home/admin): file-level subset isolation, used by the
			// -Home/-Admin subsets of the home/admin pages.
			// The preview feature depends on this: admin preview sentences are injected into
			// every subset, so all page-group subsets contain the preview chars.
			b.WriteString(fmt.Sprintf("--font-%s-home:'%s-Home';", cssBase, trimFontQuotes(fam)))
			b.WriteString(fmt.Sprintf("--font-%s-admin:'%s-Admin';", cssBase, trimFontQuotes(fam)))
		}
	}
	setFont("heading", active.Fonts["heading"])
	setFont("subheading", active.Fonts["subheading"])
	setFont("display-heading", active.Fonts["display_heading"])
	setFont("display-body", active.Fonts["display_body"])
	setFont("display-mono", active.Fonts["display_mono"])
	setFont("display-jp", active.Fonts["display_jp"])
	setFont("display-i18n", active.Fonts["display_i18n"])
	setFont("display-i18n-jp", active.Fonts["display_i18n_jp"])
	setFont("article", active.Fonts["article"])
	setFont("body", active.Fonts["body"])
	setFont("caption", active.Fonts["caption"])
	setFont("footer", active.Fonts["footer"])
	setFont("mono", active.Fonts["mono"])
	setFont("serif", active.Fonts["serif"])
	setFont("symbol", active.Fonts["symbol"])
	setFont("fun-pill", active.Fonts["fun_pill"])
	setFont("fun-title", active.Fonts["fun_title"])
	setFont("fun-title-sym", active.Fonts["fun_title_sym"])
	setFont("fun-desc", active.Fonts["fun_desc"])
	setFont("fun-btn", active.Fonts["fun_btn"])
	setFont("fun-reroll", active.Fonts["fun_reroll"])
	if cf, cw, _ := parseFontValGo(active.Fonts["cmt"]); cf != "" {
		b.WriteString(fmt.Sprintf("--font-cmt:'%s-Cmt';", trimFontQuotes(cf)))
		if cw != "" {
			b.WriteString(fmt.Sprintf("--font-cmt-weight:%s;", cw))
		}
		// cmt has no home/admin variants (it uses the separate GB2312 subset); page groups reuse -Cmt
		b.WriteString(fmt.Sprintf("--font-cmt-home:'%s-Cmt';", trimFontQuotes(cf)))
		b.WriteString(fmt.Sprintf("--font-cmt-admin:'%s-Cmt';", trimFontQuotes(cf)))
	}
	var dstack []string
	var dstackHome []string
	var dstackAdmin []string
	for _, k := range []string{"data_override_en", "data_override_num", "data_override_sym", "data"} {
		if fam, _, _ := parseFontValGo(active.Fonts[k]); fam != "" {
			dstack = append(dstack, fam)
			dstackHome = append(dstackHome, trimFontQuotes(fam)+"-Home")
			dstackAdmin = append(dstackAdmin, trimFontQuotes(fam)+"-Admin")
		}
	}
	if len(dstack) > 0 {
		b.WriteString(fmt.Sprintf("--font-data:%s;", strings.Join(dstack, ", ")))
		b.WriteString(fmt.Sprintf("--font-data-home:%s;", strings.Join(dstackHome, ", ")))
		b.WriteString(fmt.Sprintf("--font-data-admin:%s;", strings.Join(dstackAdmin, ", ")))
	}
	// Offsets / ink heights — from the active preset's admin-measured Metrics only.
	// A role with no measurement yields ok=false and no CSS variable is emitted, so every
	// consumer reads its var(--font-*-shift, 0em) fallback. That is the honest value: the
	// engine ships no fonts, so any role the site has not measured is running on the system
	// stack and needs no compensation.
	metricFor := func(base string) (s, ih float64, ok bool) {
		if m, exists := active.Metrics[base]; exists && m.InkHeight > 0 {
			return m.Shift, m.InkHeight, true
		}
		return 0, 0, false
	}
	// Latin supplement: the Latin text's own shift / inkHeight for the same font (written
	// during admin measurement).
	// Use case: Latin text running in a CJK role font (e.g. .cmt-ua's UA text uses
	// --font-caption but is pure Latin).
	// Returns 0 when not measured — **no fallback**: consumers decide for themselves
	// (substituting the CJK values is forbidden).
	metricLatinFor := func(base string) (sl, ihl float64) {
		if m, exists := active.Metrics[base]; exists && m.ShiftLatin != 0 {
			return m.ShiftLatin, m.InkHeightLatin
		}
		return 0, 0
	}
	// Fixed CJK-vs-Latin left-edge ink delta (only present when measured in the admin UI;
	// consumed by the status dropdown)
	inkLeftFor := func(base string) (float64, bool) {
		if m, exists := active.Metrics[base]; exists && m.InkLeftDelta != 0 {
			return m.InkLeftDelta, true
		}
		return 0, false
	}
	bases := []string{
		"heading", "subheading", "display-heading", "display-body", "display-mono", "display-jp", "display-i18n", "display-i18n-jp",
		"article", "body", "caption", "footer", "data", "data-num", "mono", "serif", "cmt", "symbol",
		"fun-pill", "fun-title", "fun-title-sym", "fun-desc", "fun-btn", "fun-reroll",
	}
	type mrec struct{ s, ih, sl, ihl float64 }
	recs := map[string]mrec{}
	for _, base := range bases {
		if s, ih, ok := metricFor(base); ok {
			sl, ihl := metricLatinFor(base)
			recs[base] = mrec{s, ih, sl, ihl}
			b.WriteString(fmt.Sprintf("--font-%s-shift:%.5fem;--font-%s-ink-height:%.5fem;", base, s, base, ih))
			// Latin supplement (the Latin text's own compensation for the same font); see the
			// FontMetrics.ShiftLatin comment
			if sl != 0 {
				b.WriteString(fmt.Sprintf("--font-%s-shift-latin:%.5fem;--font-%s-ink-height-latin:%.5fem;", base, sl, base, ihl))
			}
		}
		if d, ok := inkLeftFor(base); ok {
			b.WriteString(fmt.Sprintf("--font-%s-ink-left-delta:%.5fem;", base, d))
		}
	}
	// footer & symbol & fun-pill: digit/symbol/pill stroke weights (em, measured canvas coverage)
	if m, exists := active.Metrics["footer"]; exists && m.InkHeight > 0 && m.InkHeight < 0.85 {
		if m.InkWeight > 0 {
			b.WriteString(fmt.Sprintf("--font-footer-ink-weight:%.5fem;", m.InkWeight))
		}
	}
	if m, exists := active.Metrics["symbol"]; exists && m.InkWeight > 0 {
		b.WriteString(fmt.Sprintf("--font-symbol-ink-weight:%.5fem;", m.InkWeight))
	}
	if m, exists := active.Metrics["fun-pill"]; exists && m.InkWeight > 0 {
		b.WriteString(fmt.Sprintf("--font-fun-pill-ink-weight:%.5fem;", m.InkWeight))
	} else if m, exists := active.Metrics["display-mono"]; exists && m.InkWeight > 0 {
		b.WriteString(fmt.Sprintf("--font-fun-pill-ink-weight:%.5fem;", m.InkWeight))
	} else if m, exists := active.Metrics["mono"]; exists && m.InkWeight > 0 {
		b.WriteString(fmt.Sprintf("--font-fun-pill-ink-weight:%.5fem;", m.InkWeight))
	} else {
		b.WriteString("--font-fun-pill-ink-weight:0.136em;")
	}
	// ink-scale: caption/body/display-jp align to the data baseline (1.0); others stay 1
	if d, ok := recs["data"]; ok {
		if c, ok2 := recs["caption"]; ok2 && c.ih > 0 {
			sc := math.Round((d.ih/c.ih)*10000) / 10000
			b.WriteString(fmt.Sprintf("--font-caption-ink-scale:%v;", sc))
		}
		if bd, ok3 := recs["body"]; ok3 && bd.ih > 0 {
			sc := math.Round((d.ih/bd.ih)*10000) / 10000
			b.WriteString(fmt.Sprintf("--font-body-ink-scale:%v;", sc))
		}
		if jp, ok4 := recs["display-jp"]; ok4 && jp.ih > 0 {
			sc := math.Round((d.ih/jp.ih)*10000) / 10000
			b.WriteString(fmt.Sprintf("--font-display-jp-ink-scale:%v;", sc))
		}
	}
	// Kaomoji (.sym) equal height: the system symbol font is taller than the caption round
	// body -> scale down to the caption ink-height ratio
	if sy, ok4 := recs["symbol"]; ok4 && sy.ih > 0 {
		if c, ok5 := recs["caption"]; ok5 && c.ih > 0 {
			sc := math.Round((c.ih/sy.ih)*10000) / 10000
			b.WriteString(fmt.Sprintf("--font-symbol-ink-scale:%v;", sc))
		}
	}
	// display_i18n CJK/kana mixed text: the kana run (MapleMono, display-i18n-jp) is
	// compensated to strictly match the hanzi run's (LemiMuhe, display-i18n) ink —
	// scale/shift/tracking derive from the two roles' measured Metrics
	// (cross-validated against workbench-frozen values, Δ≤0.0002em; auto-refreshed after
	// admin re-measurement).
	if lm, ok := active.Metrics["display-i18n"]; ok && lm.InkHeight > 0 {
		if jp, ok2 := active.Metrics["display-i18n-jp"]; ok2 && jp.InkHeight > 0 {
			sc := math.Round((lm.InkHeight/jp.InkHeight)*10000) / 10000
			b.WriteString(fmt.Sprintf("--font-display-i18n-jp-scale:%v;", sc))
			b.WriteString(fmt.Sprintf("--font-display-i18n-jp-shift:%.5fem;", lm.InkCenter-jp.InkCenter*sc))
			b.WriteString(fmt.Sprintf("--font-display-i18n-jp-tracking:%.5fem;", lm.InkAdvance-jp.InkAdvance*sc))
			b.WriteString(fmt.Sprintf("--font-display-i18n-jp-ml:%.5fem;", active.JPMarginLeft))
		}
	}
	b.WriteString("}")
	return b.String()
}

// inkAssetsT is the in-memory cache of the role-level ink vars CSS.
type inkAssetsT struct {
	varsCSS string
}

var (
	inkAssetsMu  sync.RWMutex
	inkAssetsVal inkAssetsT
)

// picSrcGo is the Go version of the picSrc template function: returns the converted
// format (JXL/AVIF) URL.
// Only for local /assets/ or /static/ paths where the file exists, otherwise "".
// Shared by the 103 middleware and templates.
// picSrcGo resolves src to its sibling variant in ext (e.g. .avif to .jxl), provided
// that sibling exists. /assets/ sources resolve inside the served tree; /static/ sources
// stay CWD-relative.
func picSrcGo(src, ext string) string {
	if !strings.HasPrefix(src, "/static/") && !strings.HasPrefix(src, "/assets/") {
		return ""
	}
	base := strings.TrimSuffix(src, filepath.Ext(src))
	candidate := base + "." + ext
	var check string
	if strings.HasPrefix(candidate, "/assets/") {
		check = filepath.Join(RootAssets, strings.TrimPrefix(candidate, "/assets/"))
	} else {
		check = "." + candidate
	}
	if _, err := os.Stat(check); err != nil {
		return ""
	}
	return candidate
}

// rebuildInkAssets recomputes the derived assets from the current config + embed tables.
// Called once at startup; called again on font preset save/activate/delete/create
// (via saveFontConfig).
func rebuildInkAssets() {
	cfg := loadAppConfig()
	css := buildInkVarsCSS(cfg)
	inkAssetsMu.Lock()
	inkAssetsVal = inkAssetsT{varsCSS: css}
	inkAssetsMu.Unlock()
}

func getInkAssets() inkAssetsT {
	inkAssetsMu.RLock()
	defer inkAssetsMu.RUnlock()
	return inkAssetsVal
}

// saveBgConfig writes the background list to the deployment's own data file. It creates
// the data directory first — every caller ignores the returned error, so a failed write
// must at least be logged here to stay visible.
func saveBgConfig(cfg BgConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(FileBackgroundData), 0755); err != nil {
		log.Printf("background: cannot create %s: %v", filepath.Dir(FileBackgroundData), err)
		return err
	}
	if err := os.WriteFile(FileBackgroundData, data, 0644); err != nil {
		log.Printf("background: cannot write %s: %v", FileBackgroundData, err)
		return err
	}
	TriggerStaticSync()
	return nil
}

// Sponsor data.

func loadSponsors() []Sponsor {
	data, err := os.ReadFile(FileSponsorData)
	if err != nil {
		return nil
	}
	var list []Sponsor
	json.Unmarshal(data, &list)
	return list
}

func saveSponsors(list []Sponsor) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(FileSponsorData, data, 0644)
	TriggerStaticSync()
	return err
}

// Friend link data.

func loadFriends() []FriendLink {
	data, err := os.ReadFile(FileFriendData)
	if err != nil {
		return nil
	}
	var list []FriendLink
	json.Unmarshal(data, &list)
	return list
}

func saveFriends(list []FriendLink) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(FileFriendData, data, 0644)
	TriggerStaticSync()
	return err
}

// Photo data.

var photoLabels = []PhotoLabel{
	{Text: "绝密照片", Kaomoji: "(✧ω✧)"},
	{Text: "禁忌领域", Kaomoji: "(>_<)"},
	{Text: "人家才...才不给你看呢", Kaomoji: "(///)"},
	{Text: "黑历史回收站", Kaomoji: "(ﾟ∀ﾟ)"},
	{Text: "内部资料", Kaomoji: "(;´Д`)"},
	{Text: "加密文件夹", Kaomoji: "(°Д°)"},
	{Text: "禁止访问", Kaomoji: "(｀ε´)"},
	{Text: "非请勿入", Kaomoji: "(￣ε￣)"},
	{Text: "机密文件", Kaomoji: "(｀∀´)"},
	{Text: "谁要看啊", Kaomoji: "(╯‵□′)╯"},
	{Text: "不要看啦!", Kaomoji: "(>_<)"},
}

var labelIdx = 0

func randomPhotoLabel() PhotoLabel {
	label := photoLabels[labelIdx%len(photoLabels)]
	labelIdx++
	return label
}

func loadPhotos() []Photo {
	data, err := os.ReadFile(FilePhotoData)
	if err != nil {
		return nil
	}
	var list []Photo
	json.Unmarshal(data, &list)
	// Drop broken records whose files no longer exist
	var valid []Photo
	for _, p := range list {
		for _, ext := range []string{".png", ".jpg", ".avif", ".jxl", ".webp", ".heic"} {
			if _, err := os.Stat(filepath.Join(DirPhoto, p.ID+ext)); err == nil {
				valid = append(valid, p)
				break
			}
		}
	}
	if len(valid) != len(list) {
		savePhotos(valid)
	}
	return valid
}

func savePhotos(list []Photo) error {
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	err = os.WriteFile(FilePhotoData, data, 0644)
	TriggerStaticSync()
	return err
}

// postRef is a located post file: the category it is filed under, the posts/ subdirectory
// holding it, and its slug.
//
// The category and the directory are separate fields on purpose, because they differ: the
// directory is named after the registry key, while the category carries the display name
// the templates print and key CategoryAliases by. Returning one string for both is how the
// post page's category pill once rendered "essay" and linked to a 404.
// — see docs/GOTCHAS.md §post-category-display-name
type postRef struct {
	Category Category
	Dir      string
	Slug     string
}

// Path is the Markdown file's path.
func (p postRef) Path() string { return filepath.Join(p.Dir, p.Slug+".md") }

// findPostByID locates the file carrying a post ID, reporting false when no file has it.
func findPostByID(id int) (postRef, bool) {
	for _, d := range postDirs() {
		entries, _ := os.ReadDir(d.Path)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			body, _ := os.ReadFile(filepath.Join(d.Path, e.Name()))
			_, _, _, _, _, postID, _ := parseFrontmatter(string(body))
			if postID == id {
				return postRef{Category: d.Category, Dir: d.Path, Slug: strings.TrimSuffix(e.Name(), ".md")}, true
			}
		}
	}
	return postRef{}, false
}

// Session management (in-memory, single server).

var (
	sessions   = map[string]bool{}
	sessionsMu sync.RWMutex
)

func createSession() string {
	b := make([]byte, 32)
	rand.Read(b)
	tok := hex.EncodeToString(b)
	sessionsMu.Lock()
	sessions[tok] = true
	sessionsMu.Unlock()
	return tok
}

func validSession(tok string) bool {
	sessionsMu.RLock()
	ok := sessions[tok]
	sessionsMu.RUnlock()
	return ok
}

func deleteSession(tok string) {
	sessionsMu.Lock()
	delete(sessions, tok)
	sessionsMu.Unlock()
}

func hashPassword(pw string) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

func getSessionToken(r *http.Request) string {
	c, err := r.Cookie("session")
	if err != nil {
		return ""
	}
	return c.Value
}

func isAdmin(r *http.Request) bool {
	return r.Header.Get("X-Is-Admin") == "1"
}

// logMiddleware logs each request's method, path, and duration
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if dur := time.Since(start); dur > 100*time.Millisecond {
			log.Printf("slow: %s %s (%v)", r.Method, r.URL.Path, dur)
		}
	})
}

// Templates.

// sharedTemplateFiles is the list of shared/partial templates that contain no page-level
// block defines.
// These templates provide the {{template "xxx"}} defaults referenced by base_layout and
// never include page-level overrides like {{define "header"}}/{{define "content"}}, so a
// page's block define (e.g. admin.html's drawer header) can't leak into other pages.
var sharedTemplateFiles = []string{
	"_base_layout.html",
	"head.html",
	"navbar.html",
	"posts_header.html",
	"footer.html",
	"footer_content.html",     // user-editable footer body; rendered above the attribution
	"footer_attribution.html", // the project's attribution line, in its own file so that a
	// deployment copying footer.html for layout reasons does not carry it along
	"bg_shapes.html",
	"tangram_shapes.html", // the 18-shape tangram set, shared by the global layer and the home second screen
	"theme_fab.html",
	"comment_form.html",
	"comment_fragment.html",
	"page_header.html", // shared title area for friends/sponsor/guestbook
	"components.html",
	"profile_card.html",    // shared profile card for author/posts
	"motto_edit.html",      // shared motto-editing JS for home/author
	"hero_section.html",    // shared hero stage for home / admin preview
	"fun_error_stage.html", // shared random-lalafell card stage for fun_error / admin preview
	"hover_layers.html",    // unified hover-glow layer engine (hg-*) shared by all glass cards
	"sponsor_card.html",    // single source of the public sponsor card: sponsor page / admin font workshop preview
}

func loadTemplatesSafe() (*template.Template, error) {
	t := template.New("").Option("missingkey=zero").Funcs(template.FuncMap{
		"asset":             asset,
		"avatarURL":         resolveAvatarURL,
		"URLImagePost":      URLImagePost,
		"URLImageComment":   URLImageComment,
		"URLPhoto":          URLPhoto,
		"URLThumbPhotoBlur": URLThumbPhotoBlur,
		"URLThumbSticker":   URLThumbSticker,
		"URLCommit":         URLCommit,
		"URLPost":           URLPost,
		"URLPostEdit":       URLPostEdit,
		"URLTopic":          URLTopic,
		"URLTag":            URLTag,
		"URLArchive":        URLArchive,
		"URLAbout":          URLAbout,
		"URLFriends":        URLFriends,
		"URLFriendApply":    URLFriendApply,
		"URLSponsor":        URLSponsor,
		"URLGuestbook":      URLGuestbook,
		"URLFunError":       URLFunError,
		"URLSearch":         URLSearch,
		"URLUnifiedComment": URLUnifiedComment,
		"URLAdminNewPost":   func() string { return RouteAdminNewPost },
		"URLAdminDashboard": func() string { return RouteAdminDashboard },
		"URLAdminStatus":    func() string { return RouteAdminStatus },
		"URLCommentList":    func() string { return RouteCommentList },
		"URLCommentAdd":     func() string { return RouteCommentAdd },
		"URLErrorImage":     errorImageURL,
		"siteURLsJSON": func() template.JS {
			urls := buildSiteURLs()
			b, _ := json.Marshal(urls)
			return template.JS(b)
		},
		"staticURL": staticURL,
		"safeHTML": func(s any) template.HTML {
			if s == nil {
				return ""
			}
			if str, ok := s.(string); ok {
				return template.HTML(str)
			}
			if m, ok := s.(map[string]interface{}); ok {
				if ts, ok := m["text"].(string); ok {
					return template.HTML(ts)
				}
			}
			return template.HTML(fmt.Sprint(s))
		},
		"default": func(val, def string) string {
			if strings.TrimSpace(val) != "" {
				return val
			}
			return def
		},
		"mod": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a % b
		},
		"div": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"formatWords": func(w int) string {
			if w >= 1000 {
				return fmt.Sprintf("%.1fk", float64(w)/1000.0)
			}
			return fmt.Sprintf("%d", w)
		},
		// svg resolves an icon by name: the embedded set first, then the deployment's own
		// src/icon/<name>.svg. Both go through injectIcon, so an icon that arrives after
		// the build renders exactly like a shipped one.
		"svg": func(name string) template.HTML {
			res, err := ResolveIcon(name)
			if err != nil {
				log.Printf("warn: resolve icon %q: %v", name, err)
				return ""
			}
			return res
		},
		// socialLinks resolves the deployment's config/social.json. The engine ships no
		// default list, so an unconfigured deployment renders an empty row.
		"socialLinks": loadSocialLinks,
		"stickerSets": func() []StickerSet { return stickerCache },
		"fontPresets": func() []FontPreset { return loadAppConfig().FontPresets },
		"fontPresetsJSON": func() template.JS {
			data, _ := json.Marshal(loadAppConfig().FontPresets)
			return template.JS(data)
		},
		// navSignaturesJSON: injects config/nav_signatures.json (per-page nav signatures,
		// text + kaomoji as separate fields) for navbar.html to fill PAGES; the subset
		// script reads the same file — single source of truth.
		"navSignaturesJSON": func() template.JS {
			data, err := os.ReadFile(dataReadPath(FileNavSignatures))
			if err != nil {
				return template.JS("[]")
			}
			return template.JS(data)
		},
		"activeFontPreset": func() string { return loadAppConfig().ActiveFontPreset },
		"author":           func() string { return loadAppConfig().Author },
		"creditName":       func() string { return strings.TrimSpace(loadAppConfig().Credit) },
		"creditURL":        func() string { return externalLink(loadAppConfig().CreditURL) },
		// Upstream project identity for the footer attribution. Constants, not
		// configuration — see server/project.go.
		"projectName": func() string { return ProjectName },
		"projectURL":  func() string { return ProjectURL },
		"wrapNum": func(s string) template.HTML {
			parts := strings.Fields(s)
			if len(parts) > 1 {
				var sb strings.Builder
				sb.WriteString(template.HTMLEscapeString(parts[0]))
				for _, p := range parts[1:] {
					sb.WriteString("<i>" + template.HTMLEscapeString(p) + "</i>")
				}
				return template.HTML(sb.String())
			}
			return template.HTML(template.HTMLEscapeString(s))
		},
		"wrapUA": func(s string) template.HTML {
			var result []string
			for _, part := range strings.Split(s, "·") {
				part = strings.TrimSpace(part)
				if part != "" {
					// MUST wrap in <i>: a bare text node is an "anonymous flex item" that no
					// element-level rule can reach -> the only
					// compensation channel would be shifting the whole parent .cmt-ua, dragging
					// the separator dot along. Measured consequence: the dot's ink center
					// missed the text's by 1.799px (the dot shares the parent with .cmt-ua, so
					// a parent shift moves both equally — the bias can neither be created nor
					// fixed that way). Wrapped as elements, each part takes its own role rule,
					// the parent no longer shifts, and the dot keeps its flex
					// centering = lands on the optical line.
					// (The date's month/year in the same file do exactly this; compare.)
					result = append(result, "<i>"+template.HTMLEscapeString(part)+"</i>")
				}
			}
			if len(result) > 1 {
				// The separator uses a pure CSS dot (same as footer-sep), not the U+00B7 char:
				// a character is a text leaf that takes the row's ink shift, and the dot's tiny
				// ink makes that offset severe; a CSS dot has no text/ink, and .cmt-ua's flex
				// gap provides manual spacing — consistent with the footer.
				return template.HTML(strings.Join(result, `<span class="cmt-ua-sep"></span>`))
			}
			return template.HTML(template.HTMLEscapeString(s))
		},
		// picSrc returns the converted format (JXL/AVIF) URL; only for local /static/ paths
		// where the file exists, otherwise ""
		"picSrc": func(src, ext string) string {
			return picSrcGo(src, ext)
		},
		// split_script CJK/kana mixed-text segmentation: splits by character script, wrapping
		// consecutive kana runs (U+3040-30FF, including the prolonged sound mark) in
		// <span class="fnt-jp"> to use MapleMono + the ink compensation variables (.fnt-jp,
		// see fonts.css); all other characters are escaped and output as-is. Character-for-
		// character equivalent to the debug-i18n-mix workbench's JS classification
		// (content-agnostic; dynamic nicknames are segmented live on each render, no
		// hardcoded word list).
		"split_script": func(s string) template.HTML {
			var b strings.Builder
			var run strings.Builder
			runJP := false
			flush := func() {
				if run.Len() == 0 {
					return
				}
				if runJP {
					b.WriteString(`<span class="fnt-jp">` + template.HTMLEscapeString(run.String()) + `</span>`)
				} else {
					b.WriteString(template.HTMLEscapeString(run.String()))
				}
				run.Reset()
			}
			for _, r := range s {
				jp := r >= 0x3040 && r <= 0x30FF
				if jp != runJP {
					flush()
					runJP = jp
				}
				run.WriteRune(r)
			}
			flush()
			return template.HTML(b.String())
		},
		// picture generic image helper: implements the site-wide dual-format policy
		// (JXL first, AVIF fallback, original formats forbidden).
		// base is an extensionless /static/ path; extra appends additional attributes (e.g. onerror).
		"picture": func(base, alt, class, extra string) template.HTML {
			jxlSrc := base + ".jxl"
			avifSrc := base + ".avif"
			if strings.HasPrefix(base, AssetPrefix+"/") {
				logicalPath := strings.TrimPrefix(base, AssetPrefix+"/")
				jxlSrc = asset(logicalPath + ".jxl")
				avifSrc = asset(logicalPath + ".avif")
			} else if strings.HasPrefix(base, "/assets/") { // fallback for hardcoded /assets/
				logicalPath := strings.TrimPrefix(base, "/assets/")
				jxlSrc = asset(logicalPath + ".jxl")
				avifSrc = asset(logicalPath + ".avif")
			}

			b := strings.Builder{}
			b.WriteString("<picture>")
			b.WriteString(`<source srcset="` + jxlSrc + `" type="image/jxl">`)
			b.WriteString(`<source srcset="` + avifSrc + `" type="image/avif">`)
			b.WriteString(`<img src="` + avifSrc + `" alt="` + template.HTMLEscapeString(alt) + `" loading="lazy" class="` + template.HTMLEscapeString(class) + `"`)
			if extra != "" {
				b.WriteString(" " + extra)
			}
			b.WriteString(">")
			b.WriteString("</picture>")
			return template.HTML(b.String())
		},
		// CSS/JS runtime inlining.
		// inlineCSS recursively resolves @import and returns the inlined CSS
		"inlineCSS": func(path string) template.CSS {
			return template.CSS(resolveCSS(path))
		},
		// inlineJS reads a JS file and returns it inlined
		"inlineJS": func(path string) template.JS {
			return template.JS(readAsset(path))
		},
		// inkVarsInline returns the in-memory ink-vars CSS
		"inkVarsInline": func() template.CSS {
			return template.CSS(getInkAssets().varsCSS)
		},
		// siteCSSInline returns the deployment's stylesheet override, inlined into every page
		// as <style id="site-css">. It is a deployment *source* — src/css/site.css, laid over
		// the engine tree by srcReadPath — and never a served bundle: src/ is already a render
		// input, so editing it invalidates the render cache with no extra wiring, and an
		// inlined layer has no URL that can go stale behind a ?v=. See docs/DIRECTORY-MODEL.md.
		"siteCSSInline": func() template.CSS {
			data, err := os.ReadFile(srcReadPath("css/site.css"))
			if err != nil {
				return template.CSS("") // a deployment with no override is valid
			}
			return template.CSS(data)
		},
		// pageResourcesInline returns the asset table (injected into window.__PAGE_RESOURCES__).
		// Inlined rather than served: it is needed before the first paint, and a request for it
		// would block on the network for a few hundred bytes.
		"pageResourcesInline": func() template.JS {
			data, err := json.Marshal(buildPageResources())
			if err != nil {
				log.Printf("warn: pageResourcesInline: %v", err)
				return template.JS("{}")
			}
			return template.JS(data)
		},
		// dict wraps arbitrary key/value pairs into a map[string]interface{} for template
		// params: {{template "name" (dict "k1" v1 "k2" v2)}}.
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict: 奇数个参数, 需要 key/value 成对")
			}
			m := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				k, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key #%d 不是 string", i/2)
				}
				m[k] = values[i+1]
			}
			return m, nil
		},
		// heimu moved into the UI copy markers: ||word|| in config/ui_strings.json
		// renders the same .heimu span without a second key. See uitext.go.
	})
	for _, name := range sharedTemplateFiles {
		var err error
		t, err = t.ParseFiles(templatePath(name))
		if err != nil {
			return nil, fmt.Errorf("parse shared template %s: %w", name, err)
		}
	}
	return t, nil
}

func loadTemplates() *template.Template {
	t, err := loadTemplatesSafe()
	if err != nil {
		panic(err)
	}
	return t
}

// CSS/JS runtime inlining.

var (
	assetCacheMu sync.RWMutex
	assetCache   = map[string]string{}
)

// readAsset reads file content and caches it (after startup, changes require a restart
// or triggering rebuildAssets).
func readAsset(path string) string {
	assetCacheMu.RLock()
	if v, ok := assetCache[path]; ok {
		assetCacheMu.RUnlock()
		return v
	}
	assetCacheMu.RUnlock()
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("warn: read asset %s: %v", path, err)
		return ""
	}
	s := string(data)
	assetCacheMu.Lock()
	assetCache[path] = s
	assetCacheMu.Unlock()
	return s
}

var cssImportRe = regexp.MustCompile(`@import\s+url\(["']?([^"')]+)["']?\)\s*;?`)

// resolveCSS recursively resolves @import in a CSS file, returning a fully inlined CSS string.
// Query params (?v=xxx) are stripped; paths resolve relative to the referencing file's dir.
func resolveCSS(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("warn: resolveCSS read %s: %v", path, err)
		return ""
	}
	css := string(data)
	dir := filepath.Dir(path)
	css = cssImportRe.ReplaceAllStringFunc(css, func(match string) string {
		sub := cssImportRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		importPath := sub[1]
		if idx := strings.Index(importPath, "?"); idx >= 0 {
			importPath = importPath[:idx]
		}
		fullPath := filepath.Join(dir, importPath)
		return resolveCSS(fullPath)
	})
	return css
}

// rebuildAssets clears the asset cache; the next read reloads from disk.
func rebuildAssets() {
	assetCacheMu.Lock()
	assetCache = map[string]string{}
	assetCacheMu.Unlock()
}

const defaultTheme = "forest-light"

// tmpl is the shared base template set (shared/partials only, no page-level block defines).
var (
	tmplMu        sync.RWMutex
	tmpl          *template.Template
	pageTemplates map[string]*template.Template
)

func initTemplates() {
	if err := reloadTemplatesSafe(); err != nil {
		log.Fatalf("initTemplates failed: %v", err)
	}
}

// reloadTemplatesSafe reloads in a shadow copy: a mid-edit syntax error doesn't crash the
// process; the previous good template set keeps serving.
func reloadTemplatesSafe() error {
	base, err := loadTemplatesSafe()
	if err != nil {
		return err
	}
	m := map[string]*template.Template{}
	shared := map[string]bool{}
	for _, s := range sharedTemplateFiles {
		shared[s] = true
	}
	names, err := templateNames()
	if err != nil {
		return err
	}
	for _, name := range names {
		if shared[name] {
			continue
		}
		cloned, err := base.Clone()
		if err != nil {
			return err
		}
		cloned, err = cloned.ParseFiles(templatePath(name))
		if err != nil {
			return fmt.Errorf("parse page template %s: %w", name, err)
		}
		m[name] = cloned
	}
	tmplMu.Lock()
	tmpl = base
	pageTemplates = m
	tmplMu.Unlock()
	return nil
}

func init() {
	initTemplates()
}

// navSignatureEntry is one entry of config/nav_signatures.json.
type navSignatureEntry struct {
	Path  string `json:"path"`
	Label struct {
		Text    string `json:"text"`
		Kaomoji string `json:"kaomoji"`
	} `json:"label"`
}

// loadNavSignatures maps a request path to its nav signature text (label plus kaomoji).
func loadNavSignatures() map[string]string {
	data, err := os.ReadFile(dataReadPath(FileNavSignatures))
	if err != nil {
		return nil
	}
	var entries []navSignatureEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		out[e.Path] = e.Label.Text + e.Label.Kaomoji
	}
	return out
}

// navLabelForPath returns the nav signature text for the request path.
//
// The source is config/nav_signatures.json — the same file the navbar script reads, so the
// server-rendered label and the one the script swaps in cannot disagree. A path with no
// entry falls back to "/", which is the script's own rule. The engine's copy carries no
// /topic/ entry: a category's label belongs to the site that declares it.
func navLabelForPath(path string) string {
	sigs := loadNavSignatures()
	if s, ok := sigs[path]; ok {
		return s
	}
	return sigs["/"]
}

func structToMap(data interface{}) map[string]interface{} {
	if data == nil {
		return nil
	}
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	out := make(map[string]interface{})
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath == "" { // exported fields only
			out[field.Name] = v.Field(i).Interface()
		}
	}
	return out
}

// Static UI string single data source (config/ui_strings.json + config/ui_strings_admin.json).
// Structure: { "<page>": { "<module>": { "<slug>": {"text":"...","role":"..."}, ... }, ... }, "_comment":"..." }
// Templates reference it nested as {{.UI.page.module.slug}}; role is machine metadata for
// font subsetting only, humans read by page/module.
// The admin page uses its own file (ui_strings_admin.json), merged at runtime into the
// same .UI tree. Hot-reloaded on every render under DEV_MODE.
var (
	uiStrings   map[string]interface{} // nested page->module->slug->{text,role}
	uiStringsMu sync.RWMutex
)

// flattenUILeaves recursively flattens leaf nodes {"text":"...","role":"..."} into their
// text string while keeping intermediate nodes (page/module) as maps. This way
// {{.UI.page.module.slug}} in templates yields the copy, not a map (html/template errors
// on maps, and element-level references like the footer would fail wholesale).
// role is only for the subset script reading the JSON directly; the runtime doesn't need it.
// Effect markers are resolved here, once, so no template has to opt in (uitext.go).
func flattenUILeaves(node interface{}) interface{} {
	switch v := node.(type) {
	case map[string]interface{}:
		if t, ok := v["text"]; ok {
			if ts, ok := t.(string); ok {
				return uiCopyRender(ts)
			}
		}
		out := make(map[string]interface{}, len(v))
		for k, val := range v {
			out[k] = flattenUILeaves(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(v))
		for i, val := range v {
			out[i] = flattenUILeaves(val)
		}
		return out
	default:
		return node
	}
}
func handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := loadAppConfig()
	data := PageData{
		Title:    "实例流量与状态中心",
		Nickname: cfg.Author,
		Motto:    cfg.Motto,
		Year:     time.Now().Year(),
		IsAdmin:  isAdmin(r),
	}
	execute(w, r, "status.html", data)
}

// loadUIStrings builds the UI string table from the shipped defaults, using the
// deployment's own copy when it supplies one.
//
// This is a whole-file choice, not a field-by-field merge: ui_strings.json is
// engine-authored content, and a deployment that wants different labels replaces
// the file. Merging was tried and removed — it made the resolved value depend on
// two files, which is harder to reason about than "this file or that file".
func loadUIStrings() {
	combined := make(map[string]interface{})
	for _, rel := range []string{"config/ui_strings.json", "config/ui_strings_admin.json"} {
		p := overlayRelPath(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			if rel == "config/ui_strings.json" {
				log.Printf("warn: %s not found; UI strings will be missing", p)
			}
			continue
		}
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			log.Printf("warn: parse %s failed: %v", p, err)
			continue
		}
		delete(raw, "_comment")
		for k, v := range raw {
			combined[k] = flattenUILeaves(v)
		}
	}
	uiStringsMu.Lock()
	uiStrings = combined
	uiStringsMu.Unlock()
}

func currentUIStrings() map[string]interface{} {
	if os.Getenv("DEV_MODE") == "1" {
		loadUIStrings()
	}
	uiStringsMu.RLock()
	defer uiStringsMu.RUnlock()
	return uiStrings
}

func execute(w http.ResponseWriter, r *http.Request, name string, data interface{}) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// DEV_MODE: HTML is marked no-store -> the SW detects it, skips caching, and drops old
	// entries, so developers always see the freshest output (pairs with the SW's pure-SWR mode)
	if os.Getenv("DEV_MODE") == "1" {
		w.Header().Set("Cache-Control", "no-store")
	}
	isHTMX := r.Header.Get("HX-Request") == "true"

	var m map[string]interface{}

	if data != nil {
		if mapData, ok := data.(map[string]interface{}); ok {
			m = mapData
		} else if pd, ok := data.(PageData); ok {
			pd.HTMXRequest = isHTMX
			if pd.Theme == "" {
				pd.Theme = defaultTheme
			}
			if pd.NavLabel == "" {
				pd.NavLabel = navLabelForPath(r.URL.Path)
			}
			if pd.BaseURL == "" {
				pd.BaseURL = loadAppConfig().BaseURL
			}
			if pd.Motto == "" {
				pd.Motto = loadAppConfig().Motto
			}
			// Categories/tags the navbar depends on must be site-wide consistent: fall back
			// when missing (complete for both visitors and the admin page)
			if pd.Categories == nil {
				pd.Categories = getCategories()
			}
			if pd.AllTags == nil {
				pd.AllTags = getAllTags()
			}
			if pd.CategoryAliases == nil {
				pd.CategoryAliases = getCategoryAliases()
			}
			if pd.CategoryOptions == nil {
				pd.CategoryOptions = categoryOptions()
			}
			pd.UI = currentUIStrings()
			data = pd
		} else {
			// For other structs or struct pointers (e.g. the anonymous structs in admin/editor), convert to a map to prevent missing-key panics
			m = structToMap(data)
		}
	}

	if m != nil {
		if _, exists := m["Theme"]; !exists || m["Theme"] == "" {
			m["Theme"] = defaultTheme
		}
		if _, exists := m["NavLabel"]; !exists || m["NavLabel"] == "" {
			m["NavLabel"] = navLabelForPath(r.URL.Path)
		}
		if _, exists := m["BaseURL"]; !exists || m["BaseURL"] == "" {
			m["BaseURL"] = loadAppConfig().BaseURL
		}
		if _, exists := m["Motto"]; !exists || m["Motto"] == "" {
			m["Motto"] = loadAppConfig().Motto
		}
		// Categories/tags the navbar depends on must be site-wide consistent: fall back when missing
		if _, exists := m["Categories"]; !exists {
			m["Categories"] = getCategories()
		}
		if _, exists := m["AllTags"]; !exists {
			m["AllTags"] = getAllTags()
		}
		if _, exists := m["CategoryAliases"]; !exists {
			m["CategoryAliases"] = getCategoryAliases()
		}
		// The editor dropdown is the registry, never a literal list in the template.
		if _, exists := m["CategoryOptions"]; !exists {
			m["CategoryOptions"] = categoryOptions()
		}
		m["HTMXRequest"] = isHTMX
		m["UI"] = currentUIStrings()
		data = m
	}

	tmplMu.RLock()
	t, ok := pageTemplates[name]
	if !ok {
		t = tmpl
	}
	tmplMu.RUnlock()

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := minifyAssetBytes(buf.Bytes())
	w.Write(out)
}

// scanThemes reads the theme names declared by [data-theme] selectors in the authored
// css/themes/*.css. It scans src/, not the served tree: themes are bundle input, so no
// loose stylesheet reaches assets/.
func scanThemes() []string {
	var matches []string
	for _, root := range assetSourceTrees() {
		found, err := filepath.Glob(filepath.Join(root, "css", "themes", "*.css"))
		if err != nil {
			continue
		}
		matches = append(matches, found...)
	}
	if len(matches) == 0 {
		return nil
	}
	re := regexp.MustCompile(`\[data-theme="([^"]+)"\]`)
	seen := map[string]bool{}
	var themes []string
	for _, f := range matches {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range re.FindAllSubmatch(data, -1) {
			name := string(m[1])
			if !seen[name] {
				seen[name] = true
				themes = append(themes, name)
			}
		}
	}
	sort.Strings(themes)
	return themes
}

// generateThemesJSON writes the static assets/themes.json from the served theme stylesheets.
func generateThemesJSON() error {
	themes := scanThemes()
	data, err := json.Marshal(map[string]interface{}{"themes": themes})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(RootAssets, 0755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(RootAssets, "themes.json"), data, 0644); err != nil {
		return err
	}
	log.Printf("themes: generated %s (%d themes)", filepath.Join(RootAssets, "themes.json"), len(themes))
	return nil
}

type staticErrorCardData struct {
	Code       string
	TitleTag   string
	Accent     template.CSS
	PillBg     template.CSS
	PillBorder template.CSS
	BoxShadow  template.CSS
	ImgScale   template.CSS
	Glitch     bool
	Link       string
	Pill       string
	Title      string
	Desc       string
	Btn        string
}

// generateStaticErrorPages extracts error-card data from config/ui_strings.json and renders
// static error/{code}.html via templates/error_standalone.html
func generateStaticErrorPages() error {
	raw, err := os.ReadFile(overlayRelPath("config/ui_strings.json"))
	if err != nil {
		return err
	}
	var root map[string]interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	funErr, ok := root["fun_error"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("fun_error not found in ui_strings.json")
	}
	cardsRaw, ok := funErr["cards"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("fun_error.cards not found in ui_strings.json")
	}

	tplContent, err := os.ReadFile(templatePath("error_standalone.html"))
	if err != nil {
		return fmt.Errorf("read templates/error_standalone.html: %w", err)
	}
	t, err := template.New("error_standalone").Funcs(template.FuncMap{
		"URLErrorImage": errorImageURL,
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
		// Same resolver as the page templates, so the deployment's overrides apply here too.
		"svg": func(name string) template.HTML {
			res, err := ResolveIcon(name)
			if err != nil {
				log.Printf("warn: resolve icon %q: %v", name, err)
				return ""
			}
			return res
		},
	}).Parse(string(tplContent))
	if err != nil {
		return fmt.Errorf("parse templates/error_standalone.html: %w", err)
	}

	if err := os.MkdirAll(RootError, 0755); err != nil {
		return err
	}

	for code, cRaw := range cardsRaw {
		cMap, ok := cRaw.(map[string]interface{})
		if !ok {
			continue
		}
		var d staticErrorCardData
		d.Code = code
		if s, ok := cMap["title_tag"].(string); ok {
			d.TitleTag = s
		}
		if s, ok := cMap["accent"].(string); ok {
			d.Accent = template.CSS(s)
		}
		if s, ok := cMap["pill_bg"].(string); ok {
			d.PillBg = template.CSS(s)
		}
		if s, ok := cMap["pill_border"].(string); ok {
			d.PillBorder = template.CSS(s)
		}
		if s, ok := cMap["box_shadow"].(string); ok {
			d.BoxShadow = template.CSS(s)
		}
		if s, ok := cMap["img_scale"].(string); ok {
			d.ImgScale = template.CSS(s)
		}
		if b, ok := cMap["glitch"].(bool); ok {
			d.Glitch = b
		}
		if s, ok := cMap["link"].(string); ok {
			d.Link = s
		}
		if d.Link == "" {
			d.Link = "/"
		}

		getText := func(key string) string {
			if v, ok := cMap[key]; ok {
				if m, ok := v.(map[string]interface{}); ok {
					if ts, ok := m["text"].(string); ok {
						return fmt.Sprint(uiCopyRender(ts))
					}
				}
				if ts, ok := v.(string); ok {
					return fmt.Sprint(uiCopyRender(ts))
				}
			}
			return ""
		}

		formatWithSpaces := func(s string) string {
			parts := strings.Split(s, " · ")
			for i, p := range parts {
				words := strings.Fields(p)
				parts[i] = strings.Join(words, `<span class="fun-space"></span>`)
			}
			return strings.Join(parts, `<span class="fun-dot">·</span>`)
		}
		d.Pill = formatWithSpaces(getText("pill"))
		d.Title = strings.ReplaceAll(getText("title"), "！", "<span class=\"sym\">！</span>")
		d.Desc = formatWithSpaces(getText("desc"))
		d.Btn = getText("btn")

		var buf bytes.Buffer
		if err := t.Execute(&buf, d); err != nil {
			log.Printf("warn: execute error template for %s: %v", code, err)
			continue
		}
		outPath := filepath.Join(RootError, code+".html")
		if err := os.WriteFile(outPath, buf.Bytes(), 0644); err != nil {
			log.Printf("warn: write %s: %v", outPath, err)
			continue
		}
	}
	log.Printf("error_pages: generated static error pages in %s from config/ui_strings.json", RootError)
	return nil
}

// Markdown.

var (
	gm          = goldmark.New(goldmark.WithExtensions(extension.GFM, highlighting.NewHighlighting(highlighting.WithStyle("monokai"), highlighting.WithFormatOptions(chromahtml.WithClasses(true)))))
	policy      = bluemonday.UGCPolicy()
	shortcodeRe = regexp.MustCompile(`:(\[[^\]]+\]|[^:\[\]]+):`)
)

func init() {
	policy.AllowAttrs("class").OnElements("code", "pre", "span", "div", "img", "picture")
	// Allow dual-format <picture>/<source>: the default UGCPolicy strips them wholesale,
	// which would mean the JXL primary format never gets served.
	policy.AllowElements("picture", "source")
	policy.AllowAttrs("srcset", "type", "media").OnElements("source", "picture")
	// Lazy/decoding/responsive attributes on img (not allowed by default UGCPolicy; would
	// be silently stripped).
	policy.AllowAttrs("srcset", "loading", "decoding").OnElements("img")
}

func renderMarkdown(s string) template.HTML {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "---\n") {
		if end := strings.Index(s[4:], "\n---"); end != -1 {
			s = strings.TrimSpace(s[end+8:])
		}
	}
	var buf bytes.Buffer
	if err := gm.Convert([]byte(s), &buf); err != nil {
		return template.HTML("<p>render error</p>")
	}
	// Replace shortcodes (after goldmark's HTML conversion, so goldmark won't escape img tags)
	// :[name]: -> large image (128px)  :name: -> small image (64px)
	html := shortcodeRe.ReplaceAllStringFunc(buf.String(), func(m string) string {
		if len(m) < 3 {
			return m
		}
		inner := m[1 : len(m)-1]
		px := 64
		cls := ""
		if inner[0] == '[' && inner[len(inner)-1] == ']' {
			px = 128
			cls = " sticker-lg"
			inner = inner[1 : len(inner)-1]
		}
		if ref, ok := shortcodeMap[inner]; ok {
			imgSrc := url.PathEscape(ref.URL)
			imgSrc = strings.ReplaceAll(imgSrc, "%2F", "/")
			// Stickers are all animated: use anim-picture. The jxl/avif/gif three sources are
			// natively negotiated by <picture> MIME (jxl > avif > gif); on decode failure the
			// browser falls back to the next source — no JS probing.
			// base must be derived from the already-encoded imgSrc (spaces/CJK -> %20/%xx),
			// otherwise buildPicture outputs unencoded src/srcset URLs which bluemonday
			// validation drops, and the img never loads.
			base := strings.TrimSuffix(imgSrc, ".avif")
			imgTag := fmt.Sprintf(`<img src="%s" class="sticker-inline%s" width="%d" height="%d" alt="sticker">`, imgSrc, cls, px, px)
			return buildPicture(imgTag, base, true)
		}
		return m
	})
	safe := policy.SanitizeBytes([]byte(html))
	// Heading anchors: id = slug built from the heading's plain text (no toc- prefix), so
	// deep links like /post/{id}#heading work.
	// Kept consistent with templates/post.html's rebuildTOC tocSlug logic.
	// Note: can't use ReplaceAllFunc (its callback receives the whole match, not subgroup
	// slices) — FindAllSubmatchIndex is required.
	used := map[string]bool{}
	idx := 0
	{
		locs := tocHeadingRe.FindAllSubmatchIndex(safe, -1)
		var out []byte
		last := 0
		for _, loc := range locs {
			// loc: [fullStart fullEnd g1S g1E g2S g2E g3S g3E]
			tag := string(safe[loc[2]:loc[3]])
			attrs := string(safe[loc[4]:loc[5]])
			inner := string(safe[loc[6]:loc[7]])
			slug := tocSlug(stripHeadingText(inner), idx, used)
			idx++
			out = append(out, safe[last:loc[0]]...)
			out = append(out, []byte("<"+tag+attrs+` id="`+slug+`">`+inner+"</"+tag+">")...)
			last = loc[1]
		}
		out = append(out, safe[last:]...)
		safe = out
	}
	safe = WrapAllImages(safe)
	// All content images get loading="lazy" (article body images + shortcode stickers +
	// comment images)
	// The loading system skips lazy images, so they don't block the loading overlay;
	// setupLazyImages() adds the spinner
	safe = bytes.ReplaceAll(safe, []byte("<img "), []byte(`<img loading="lazy" `))
	return template.HTML(safe)
}

// stripHeadingText extracts the plain text of a heading's inner HTML (strip tags +
// unescape entities), for tocSlug.
func stripHeadingText(inner string) string {
	s := headingTagRe.ReplaceAllString(inner, "")
	return html.UnescapeString(s)
}

var (
	// tocHeadingRe matches full heading open+close tags; groups: 1=tag name 2=attrs 3=inner HTML.
	// Must use FindAllSubmatchIndex for subgroups (ReplaceAllFunc's callback receives the
	// whole match, not subgroups).
	tocHeadingRe = regexp.MustCompile(`(?s)<(h[1-3])([^>]*)>(.*?)</h[1-3]>`)
	headingTagRe = regexp.MustCompile(`(?s)<[^>]*>`)
)

// tocSlug generates the HTML anchor id from a heading's plain text (used directly as the
// URL fragment, no toc- prefix).
// Rules: keep ASCII alphanumerics + CJK/kana/fullwidth; collapse other consecutive chars
// into a single '-'; empty headings fall back to sec-N; dedupe with -2/-3.
// Kept consistent with templates/post.html's rebuildTOC tocSlug (changes must be made on
// both sides).
func tocSlug(text string, idx int, used map[string]bool) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Sprintf("sec-%d", idx)
	}
	var b strings.Builder
	dash := false
	for _, r := range text {
		switch {
		case r == ' ' || r == '\t' || r == '\u3000' || r == '-' || r == '_':
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		case isTocSafeRune(r):
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return fmt.Sprintf("sec-%d", idx)
	}
	base := slug
	for n := 2; used[slug]; n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	used[slug] = true
	return slug
}

// isTocSafeRune reports whether a rune can be used in an anchor id (matches the JS
// tocSlug regex character class).
func isTocSafeRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r >= 0x3040 && r <= 0x30FF: // hiragana / katakana
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK extension A
		return true
	case r >= 0x4E00 && r <= 0x9FFF: // CJK unified ideographs
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // fullwidth forms (incl. fullwidth punctuation/digits/letters)
		return true
	}
	return false
}

// isManagedImage reports whether src belongs to the body/comment illustration pool
// governed by the dual-format policy.
// Only the current singular directories /static/image/{post,comment}/ are recognized
// (the legacy /static/images/ is fully retired).
func isManagedImage(src string) bool {
	return strings.HasPrefix(src, "/"+filepath.ToSlash(DirImagePost)+"/") ||
		strings.HasPrefix(src, "/"+filepath.ToSlash(DirImageComment)+"/")
}

// WrapAllImages uniformly wraps body/comment illustrations in the markdown output as
// <picture> (JXL primary + AVIF fallback + <img avif> base), implementing the site-wide
// dual-format policy and forbidding original-format/GIF fallbacks.
//   - Already .avif/.jxl (incl. ?anim) or legacy .jpg/.jpeg/.png/.webp: add JXL+AVIF
//     siblings (static images).
//   - .avif with the ?anim marker: animated; by default only an AVIF <source> is emitted
//     (dodging Safari's trap of decoding static JXL but not animating it); a live probe in
//     main.js decides whether the browser can truly animate JXL and, if so, injects the
//     JXL <source> first. No UA sniffing anywhere.
//   - Legacy .gif (never migrated): kept as-is (not part of the dual-format output).
//   - Stickers (/static/sticker/) and external URLs: untouched.
func WrapAllImages(html []byte) []byte {
	re := regexp.MustCompile(`<img\b([^>]*?)>`)
	return []byte(re.ReplaceAllStringFunc(string(html), func(tag string) string {
		m := regexp.MustCompile(`src="([^"]+)"`).FindStringSubmatch(tag)
		if m == nil {
			return tag
		}
		src := m[1]
		if !isManagedImage(src) {
			return tag
		}
		// Split the query (the ?anim marker)
		rawSrc := src
		var query string
		if idx := strings.Index(src, "?"); idx >= 0 {
			rawSrc = src[:idx]
			query = src[idx:]
		}
		low := strings.ToLower(rawSrc)
		isAnim := strings.Contains(query, "anim")
		if strings.HasSuffix(low, ".avif") || strings.HasSuffix(low, ".jxl") {
			base := strings.TrimSuffix(rawSrc, filepath.Ext(rawSrc))
			return buildPicture(tag, base, isAnim)
		}
		if regexp.MustCompile(`\.(jpg|jpeg|png|webp)$`).MatchString(low) {
			base := strings.TrimSuffix(rawSrc, filepath.Ext(rawSrc))
			return buildPicture(tag, base, false)
		}
		return tag
	}))
}

// buildPicture wraps a single <img> into a <picture>. base is an extensionless /static/ path.
// Pure MIME negotiation, no JS probing: the browser picks top-down by support,
// jxl > avif > gif, falling back to the next source on decode failure.
func buildPicture(tag, base string, isAnim bool) string {
	// Apply asset cache-busting if applicable
	jxlSrc := base + ".jxl"
	avifSrc := base + ".avif"
	if strings.HasPrefix(base, AssetPrefix+"/") {
		logicalPath := strings.TrimPrefix(base, AssetPrefix+"/")
		jxlSrc = asset(logicalPath + ".jxl")
		avifSrc = asset(logicalPath + ".avif")
	} else if strings.HasPrefix(base, "/assets/") {
		logicalPath := strings.TrimPrefix(base, "/assets/")
		jxlSrc = asset(logicalPath + ".jxl")
		avifSrc = asset(logicalPath + ".avif")
	}

	if !isAnim {
		return `<picture><source srcset="` + jxlSrc + `" type="image/jxl"><source srcset="` + avifSrc + `" type="image/avif">` + tag + `</picture>`
	}
	// Animated: natively negotiated across the jxl > avif > gif three sources.
	// Stickers have all three siblings (jxl+avif+gif): Chrome picks jxl, Firefox picks
	// avif, Safari skips jxl and can't decode 444 avif -> falls back to gif.
	// Comment/body animations only have avif (no animated jxl, no gif) -> pure avif; Safari
	// has no fallback (known limitation).
	isSticker := strings.HasPrefix(base, AssetPrefix+"/sticker/") || strings.HasPrefix(base, "/static/sticker/")
	if !isSticker {
		imgTag := regexp.MustCompile(`src="[^"]+"`).ReplaceAllString(tag, `src="`+avifSrc+`"`)
		return `<picture class="anim-picture"><source srcset="` + avifSrc + `" type="image/avif">` + imgTag + `</picture>`
	}
	// For stickers, they have jxl/avif/gif subdirectories
	jxlStickerSrc := strings.Replace(base, "/avif/", "/jxl/", 1) + ".jxl"
	gifStickerSrc := strings.Replace(base, "/avif/", "/gif/", 1) + ".gif"

	if strings.HasPrefix(jxlStickerSrc, AssetPrefix+"/") {
		jxlStickerSrc = asset(strings.TrimPrefix(jxlStickerSrc, AssetPrefix+"/"))
	} else if strings.HasPrefix(jxlStickerSrc, "/assets/") {
		jxlStickerSrc = asset(strings.TrimPrefix(jxlStickerSrc, "/assets/"))
	}

	if strings.HasPrefix(gifStickerSrc, AssetPrefix+"/") {
		gifStickerSrc = asset(strings.TrimPrefix(gifStickerSrc, AssetPrefix+"/"))
	} else if strings.HasPrefix(gifStickerSrc, "/assets/") {
		gifStickerSrc = asset(strings.TrimPrefix(gifStickerSrc, "/assets/"))
	}

	imgTag := regexp.MustCompile(`src="[^"]+"`).ReplaceAllString(tag, `src="`+gifStickerSrc+`"`)
	var b strings.Builder
	b.WriteString(`<picture class="anim-picture">`)
	b.WriteString(`<source srcset="` + jxlStickerSrc + `" type="image/jxl">`)
	b.WriteString(`<source srcset="` + avifSrc + `" type="image/avif">`)
	b.WriteString(`<source srcset="` + gifStickerSrc + `" type="image/gif">`)
	b.WriteString(imgTag)
	b.WriteString(`</picture>`)
	return b.String()
}

func parseFrontmatter(s string) (title string, tags []string, modTime time.Time, body string, words int, id int, summary string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	body = s
	title = "Untitled"
	// Formerly `time.Now()`. That made "frontmatter missing date" look like "just
	// published" — and with nanosecond precision, callers **cannot reliably identify** the
	// placeholder (posts.go once used `modTime.Equal(time.Now())` for that, but two
	// time.Now() calls essentially never compare equal -> that fallback **never actually
	// fired** -> posts missing a date showed the index-rebuild time). Now returns the zero
	// value; callers uniformly fall back to the file mtime.
	modTime = time.Time{}
	id = 0
	summary = ""
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "---\n") {
		if strings.HasPrefix(s, "# ") {
			idx := strings.Index(s, "\n")
			if idx == -1 {
				title = strings.TrimPrefix(s, "# ")
				body = ""
				return
			}
			title = strings.TrimPrefix(s[:idx], "# ")
			body = s[idx+1:]
			return
		}
		return
	}
	endIdx := strings.Index(s[4:], "\n---")
	if endIdx == -1 {
		return
	}
	endIdx += 4
	fm := s[4:endIdx]
	body = strings.TrimSpace(s[endIdx+4:])
	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "title: ") {
			title = strings.TrimPrefix(line, "title: ")
			title = strings.Trim(title, "\"'")
		} else if strings.HasPrefix(line, "summary: ") {
			summary = strings.TrimPrefix(line, "summary: ")
			summary = strings.Trim(summary, "\"'")
		} else if strings.HasPrefix(line, "description: ") {
			summary = strings.TrimPrefix(line, "description: ")
			summary = strings.Trim(summary, "\"'")
		} else if strings.HasPrefix(line, "date: ") {
			ds := strings.TrimPrefix(line, "date: ")
			ds = strings.TrimSpace(ds)
			if t, err := time.Parse("2006-01-02", ds); err == nil {
				modTime = t
			} else if t, err := time.Parse(time.RFC3339, ds); err == nil {
				modTime = t
			}
		} else if strings.HasPrefix(line, "tags: ") {
			rest := strings.TrimPrefix(line, "tags: ")
			rest = strings.TrimSpace(rest)
			if strings.HasPrefix(rest, "[") {
				inner := strings.Trim(rest, "[]")
				for _, t := range strings.Split(inner, ",") {
					t = strings.TrimSpace(t)
					t = strings.Trim(t, "\"'")
					if t != "" {
						tags = append(tags, t)
					}
				}
			} else {
				tags = append(tags, rest)
			}
		} else if strings.HasPrefix(line, "words: ") {
			wStr := strings.TrimSpace(strings.TrimPrefix(line, "words: "))
			fmt.Sscanf(wStr, "%d", &words)
		} else if strings.HasPrefix(line, "id: ") {
			idStr := strings.TrimSpace(strings.TrimPrefix(line, "id: "))
			fmt.Sscanf(idStr, "%d", &id)
		}
	}
	if title == "Untitled" && strings.HasPrefix(body, "# ") {
		idx := strings.Index(body, "\n")
		if idx == -1 {
			title = strings.TrimPrefix(body, "# ")
		} else {
			title = strings.TrimPrefix(body[:idx], "# ")
		}
	}
	return
}

func extractSummary(s string) string {
	return ""
}

// Post loading: thin wrappers over the DB-backed store.

func getAllTags() []string {
	return getAllTagsFromDB()
}

func loadPosts() ([]Post, error) {
	return loadPostsFromDB(), nil
}

func loadPostsByCategory(cat string) ([]Post, error) {
	return loadPostsByCategoryFromDB(cat), nil
}

func loadPostsFull() ([]Post, error) {
	return loadPostsFromDB(), nil
}

// Main.

// backfillPostIDs guarantees every post has a stable numeric ID (unique identifier).
// An id already written into the frontmatter is never changed; only posts missing an id
// get max+1 assigned in date-ascending order.
func backfillPostIDs() {
	type postEntry struct {
		dir  string
		slug string
		date time.Time
		body string
		id   int
	}
	var all []postEntry
	for _, d := range postDirs() {
		dir := d.Path
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			body, _ := os.ReadFile(path)
			text := string(body)
			_, _, modTime, _, _, id, _ := parseFrontmatter(text)
			slug := strings.TrimSuffix(e.Name(), ".md")
			if modTime.IsZero() {
				if fi, _ := e.Info(); fi != nil {
					modTime = fi.ModTime()
				}
			}
			all = append(all, postEntry{dir, slug, modTime, text, id})
		}
	}

	// First count used IDs (duplicates are anomalies: later occurrences get reassigned)
	used := make(map[int]bool)
	maxID := 0
	for i := range all {
		if all[i].id > 0 {
			if used[all[i].id] {
				log.Printf("backfill ID: duplicate id %d on %s/%s, will reassign", all[i].id, filepath.Base(all[i].dir), all[i].slug)
				all[i].id = 0
				continue
			}
			used[all[i].id] = true
			if all[i].id > maxID {
				maxID = all[i].id
			}
		}
	}

	// Assign max+1 to posts missing an id, in date-ascending order
	missing := make([]*postEntry, 0)
	for i := range all {
		if all[i].id == 0 {
			missing = append(missing, &all[i])
		}
	}
	sort.Slice(missing, func(i, j int) bool {
		return missing[i].date.Before(missing[j].date)
	})
	for _, p := range missing {
		maxID++
		p.id = maxID
		newText := setIDInFrontmatter(p.body, p.id)
		path := filepath.Join(p.dir, p.slug+".md")
		if err := os.WriteFile(path, []byte(newText), 0644); err != nil {
			log.Fatalf("backfill ID: write %s: %v", path, err)
		}
	}
	maxPostID = maxID
}

func setIDInFrontmatter(text string, id int) string {
	if !strings.HasPrefix(text, "---\n") {
		return text
	}
	endIdx := strings.Index(text[4:], "\n---")
	if endIdx == -1 {
		return text
	}
	endIdx += 4
	fm := text[4:endIdx]
	// Replace or insert the id: line
	lines := strings.Split(fm, "\n")
	replaced := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "id: ") {
			lines[i] = fmt.Sprintf("id: %d", id)
			replaced = true
			break
		}
	}
	newFM := strings.Join(lines, "\n")
	if !replaced {
		newFM = fm + fmt.Sprintf("\nid: %d", id)
	}
	return text[:4] + newFM + text[endIdx:]
}

func main() {
	// -dir / SILPHUU_HOME point the content roots (config/, state/, posts/, static/,
	// render/, public/) at a deployment directory, keeping personal content and
	// configuration out of the source tree. Engine assets still resolve relative to the
	// working directory. See docs/RELEASE-AND-OVERLAY.md.
	dirFlag := flag.String("dir", "", "deployment root holding config/, state/, posts/, static/, render/ and public/ (default: a scratch directory under the user cache; env SILPHUU_HOME)")
	projectStaticFlag := flag.String("project-static", "", "build the tree a static web server serves at this path, then exit")
	flag.Parse()
	explicitHome := resolveHomeFlag(*dirFlag) != ""
	InitRoots(resolveHomeFlag(*dirFlag))
	// The engine tree is read-only: pointing the deployment root at it makes every
	// runtime write land in the repository. See docs/GOTCHAS.md §runtime-output-roots.
	if sameDir(homeRoot, engineRoot) {
		log.Fatalf("refusing to use the engine directory (%s) as the deployment root; pass -dir or SILPHUU_HOME", homeRoot)
	}
	if !explicitHome {
		log.Printf("warn: no -dir or SILPHUU_HOME given; using the scratch deployment root %s", homeRoot)
	}
	// Package init() already loaded UI strings and templates from the default
	// roots, because a flag cannot be read that early. Re-read them now that the
	// deployment directory is known.
	ReloadPathDependentCaches()
	// Drop pre-rendered HTML when the inputs changed while the server was down —
	// otherwise a freshly edited overlay keeps serving the previous site's pages.
	invalidateCacheIfRenderInputsChanged()

	// The home page is a single file at the cache root (see pickHomeBackgrounds). A
	// leftover per-device/background shard under public/home/ is stale cache that would
	// otherwise be published next to the real page and served from its own URL —
	// see docs/GOTCHAS.md §home-shard-stale.
	if stale := filepath.Join(RootRender, "home"); func() bool {
		_, err := os.Stat(stale)
		return err == nil
	}() {
		if err := os.RemoveAll(stale); err != nil {
			log.Printf("warn: could not remove the stale home cache shard %s: %v", stale, err)
		} else {
			log.Printf("cache: removed the stale home shard %s (the home page is now a single file)", stale)
		}
	}

	// Report which avatar the pages will carry, and complain about formats that are
	// present but unsupported. The resolution itself happens per render (avatar.go).
	logAvatarResolution()

	// The home page is rendered once into a single file so a static server can publish
	// "/" (see pickHomeBackground). Extra backgrounds are configured but never rendered —
	// say so out loud rather than dropping them silently.
	if n := len(loadBgConfig().Backgrounds); n > 1 {
		log.Printf("backgrounds: %d configured but only the first is rendered; client-side rotation is not implemented yet", n)
	}

	// Record the category set as the baseline for runtime invalidation: the category list is
	// rendered into the navbar, which is on every page, so a directory appearing later has to
	// invalidate the whole site. Recorded after the startup invalidation above, so it matches
	// what was actually rendered.
	initCategorySet()

	// Build the bundles into assets/ (startup fallback; the watcher repeats it in real
	// time during development).
	if err := concatAllBundles(); err != nil {
		log.Printf("warn: concatAllBundles: %v", err)
	}

	// Register JXL/AVIF MIME: in direct-Go mode (http.FileServer) static serving must
	// return the right Content-Type, or the browser skips <source type="image/jxl">
	// (treated as octet-stream). Caddy mode has its own MIME and is unaffected.
	mime.AddExtensionType(".jxl", "image/jxl")
	mime.AddExtensionType(".avif", "image/avif")

	loadStickers()

	// Initialize the static asset manifest (zero-lock reads)
	if err := initAssetManifest(); err != nil {
		log.Printf("warn: initAssetManifest: %v", err)
	}

	// Initialize the event-sourcing store and in-memory projections
	if err := InitEventStore(); err != nil {
		log.Printf("warn: InitEventStore: %v", err)
	}
	go syncLoop() // two-node sync (no-op when SYNC_UDS_SOCK is unset)

	initViews() // in-memory views/UV/PV layer
	initEmailTemplates()
	backfillPostIDs() // ensure every .md has a stable ID first
	backfillPosts()   // fully rebuild the in-memory index from .md files
	rebuildRefs()     // rebuild file refcounts from comment + post content
	// Generate missing thumbnails asynchronously in the background; don't block startup
	go generateStickerThumbnails()
	go generateAllBlurThumbs()

	// Pre-generate the Ink alignment derived assets (role-level CSS + element-level
	// families table) into memory
	rebuildInkAssets()

	// Scan themes and statically generate assets/themes.json (enables strong CDN caching
	// and auto-discovery)
	if err := generateThemesJSON(); err != nil {
		log.Printf("warn: generateThemesJSON: %v", err)
	}

	// Render the service worker into render/, where the projection finds it. Runs after the
	// bundles, the asset manifest and the post index, all of which it reads.
	if err := materializeServiceWorker(); err != nil {
		log.Printf("warn: materializeServiceWorker: %v", err)
	}

	mux := http.NewServeMux()

	registerAPIRoutes(mux)

	handleFavicon := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=2592000, immutable")
		http.ServeFile(w, r, filepath.Join(RootPublic, "favicon.svg"))
	}
	mux.HandleFunc("GET "+RouteFaviconSvg, handleFavicon)
	mux.HandleFunc("GET "+RouteFaviconIco, handleFavicon)

	// Page routes (HTML rendering).
	mux.HandleFunc("GET "+PageHome, handleHome)
	mux.HandleFunc("GET "+PageAbout, handleAuthor)
	mux.HandleFunc("GET "+PageGuestbook, handleMessage)
	mux.HandleFunc("GET "+PageFunError, handleFunError)
	mux.HandleFunc("GET "+PageFeed, handleFeed)
	mux.HandleFunc("GET "+PageSitemap, handleSitemap)
	mux.HandleFunc("GET "+PageRobots, handleRobots)
	mux.HandleFunc("GET "+PageArchive, handleArchive)
	mux.HandleFunc("GET "+PageSponsor, handleSponsor)
	mux.HandleFunc("GET "+PageFriends, handleFriends)
	mux.HandleFunc("GET "+PageCategory, handleCategory)
	mux.HandleFunc("GET "+PagePosts, handlePosts)
	mux.HandleFunc("GET "+PagePost, handlePostByID)
	mux.HandleFunc("GET "+PagePostSlash, handlePostByID)
	mux.HandleFunc("GET "+PageSearch, handleSearchPage)
	mux.HandleFunc("GET "+RouteAdminStatus, handleStatus)
	mux.HandleFunc("GET "+RouteAdminEcho, handlePrivateEcho)
	mux.HandleFunc("GET "+RouteAdminSentinel, handleStatusSentinel)

	// Admin Pages.
	mux.HandleFunc("GET "+RouteAdminDashboard, handleAdmin)
	mux.HandleFunc("GET "+RouteAdminNewPost, handleAdminNewPost)
	mux.HandleFunc("GET "+RouteAdminEditPost, handleAdminEditPost)

	// SW: serve at root path /sw.js to ensure scope=/ (intercepts all navigations + font
	// requests). The file is materialised into render/ with every placeholder already
	// resolved — a projection publishes it verbatim — so this only reads and sets headers.
	mux.HandleFunc("GET "+RouteSwJs, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache") // recheck sw.js on every navigation; upgrades must not be delayed by HTTP caching
		src, err := os.ReadFile(filepath.Join(RootRender, "sw.js"))
		if err != nil {
			http.Error(w, "sw.js missing", http.StatusInternalServerError)
			return
		}
		w.Write(src)
	})

	// /assets/ serves the deployment's assets/ as-is: the bundles built from src/, plus the
	// generated subsets, thumbnails and indexes, plus the site's own site.css and images.
	mux.Handle("GET "+PrefixAssets+"/", http.StripPrefix(PrefixAssets+"/", http.FileServer(http.Dir(RootAssets))))
	mux.Handle("GET "+PrefixStatic+"/", http.StripPrefix(PrefixStatic+"/", http.FileServer(http.Dir(RootStatic))))
	mux.Handle("GET /debug/", http.StripPrefix("/debug/", http.FileServer(http.Dir("debug"))))

	// Pre-generate the site-wide body subset at startup (ui_strings.json static copy +
	// posts/dynamic data)
	// Must run before initCommentFontSubset so it isn't skipped when contending for
	// fontSubsetMu with the comment subset
	initFontSubset()

	// Pre-generate the active comment font's 3500-char subset at startup (if configured)
	initCommentFontSubset()

	// Pre-generate the admin-page font subset at startup (file-level isolation for the admin UI)
	initAdminFontSubset()

	// Parse fonts.css to build the font-family -> URL map (for 103 Early Hints)
	initFontURLMap()

	// Pre-generate the home subset fonts at startup (if a home cache already exists)
	initHomeSubsetAndWBN()

	// fsnotify file watching: JS/CSS changes refresh the inlined caches; posts/ changes
	// auto-invalidate caches
	startWatcher()

	// Pre-render the /search shell. It carries every post's card, so it has to come after the
	// post index is built (backfillPosts above) — and after the startup invalidation, otherwise
	// the freshly rendered shell would be wiped with the rest of the cache.
	warmSearchShell()

	// Cache pre-warming (warm) reuses the registered page handlers (warmCache.go)
	globalMux = mux

	// -project-static materialises the tree a static web server can serve outright.
	// It runs here, at the end of startup, so it sees the generated bundles, themes.json
	// and the render cache rather than a half-built assets/.
	//
	// The list pages are warmed synchronously first: the warm started by cache
	// invalidation runs in a goroutine, and projecting while it is mid-flight would ship
	// a tree with pages missing.
	if *projectStaticFlag != "" {
		warmPages()
		if err := projectStaticTree(*projectStaticFlag); err != nil {
			log.Fatalf("project-static: %v", err)
		}
		log.Printf("project-static: wrote %s", *projectStaticFlag)
		return
	}

	handler := logMiddleware(earlyHintsMiddleware(cacheMiddleware(mux)))

	srv := &http.Server{Handler: handler}

	// Only unix socket hosting is supported (for Nginx etc. reverse proxies). SILPHUU_SOCK
	// must be set; falling back to TCP is not allowed — silent fallbacks only introduce
	// errors and nondeterminism.
	sockPath := os.Getenv("SILPHUU_SOCK")
	if sockPath == "" {
		log.Fatal("SILPHUU_SOCK not set; refusing to start (unix socket required)")
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		log.Fatalf("create socket dir %s: %v", sockPath, err)
	}
	// Remove a leftover socket file from the previous run, or bind fails with
	// "address already in use"
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		log.Fatalf("remove stale socket %s: %v", sockPath, err)
	}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		log.Fatalf("listen unix %s: %v", sockPath, err)
	}
	// Let the reverse proxy (caddy) connect: grant rw to group and others
	if err := os.Chmod(sockPath, 0o666); err != nil {
		log.Printf("warn: chmod socket %s failed: %v", sockPath, err)
	}
	log.Printf("server starting on unix socket %s", sockPath)

	if err := srv.Serve(l); err != nil {
		log.Fatal(err)
	}
}
