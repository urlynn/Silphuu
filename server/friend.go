package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	reScriptTag        = regexp.MustCompile(`(?i)<script[\s\S]*?<\/script>`)
	reForeignObjectTag = regexp.MustCompile(`(?i)<foreignObject[\s\S]*?<\/foreignObject>`)
	reIframeTag        = regexp.MustCompile(`(?i)<iframe[\s\S]*?<\/iframe>`)
	reObjectTag        = regexp.MustCompile(`(?i)<object[\s\S]*?<\/object>`)
	reEmbedTag         = regexp.MustCompile(`(?i)<embed[\s\S]*?<\/embed>`)
	reEventHandler     = regexp.MustCompile(`(?i)\s+on[a-z]+\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reJavascriptURI    = regexp.MustCompile(`(?i)(href|xlink:href)\s*=\s*["']?\s*javascript:[^"'>\s]*["']?`)
	reSlugChars        = regexp.MustCompile(`[^a-z0-9\-]`)
	reMultiHyphen      = regexp.MustCompile(`-+`)
)

// sanitizeSVG cleans raw SVG text by removing scripts, event handlers, and dangerous tags.
func sanitizeSVG(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if len(s) == 0 {
		return "", errors.New("empty SVG")
	}
	if len(s) > 100*1024 {
		return "", errors.New("SVG exceeds maximum size limit (100KB)")
	}

	lower := strings.ToLower(s)
	startIdx := strings.Index(lower, "<svg")
	endIdx := strings.LastIndex(lower, "</svg>")
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return "", errors.New("invalid SVG markup: missing <svg> or </svg> tags")
	}

	svgBlock := s[startIdx : endIdx+len("</svg>")]

	// Strip dangerous elements
	clean := reScriptTag.ReplaceAllString(svgBlock, "")
	clean = reForeignObjectTag.ReplaceAllString(clean, "")
	clean = reIframeTag.ReplaceAllString(clean, "")
	clean = reObjectTag.ReplaceAllString(clean, "")
	clean = reEmbedTag.ReplaceAllString(clean, "")

	// Strip inline event listeners (onload, onclick, onerror, etc.)
	clean = reEventHandler.ReplaceAllString(clean, "")

	// Strip javascript pseudo-protocols
	clean = reJavascriptURI.ReplaceAllString(clean, "")

	clean = strings.TrimSpace(clean)
	if !strings.HasPrefix(strings.ToLower(clean), "<svg") || !strings.HasSuffix(strings.ToLower(clean), "</svg>") {
		return "", errors.New("sanitized SVG is malformed")
	}

	return clean, nil
}

// generateSafeSlug produces a clean filename slug from domain or site name.
func generateSafeSlug(name, siteURL string) string {
	candidate := ""
	if u, err := url.Parse(siteURL); err == nil && u.Host != "" {
		host := strings.ToLower(u.Host)
		if idx := strings.Index(host, ":"); idx != -1 {
			host = host[:idx]
		}
		host = strings.TrimPrefix(host, "www.")
		candidate = strings.ReplaceAll(host, ".", "-")
	}

	if candidate == "" {
		candidate = strings.ToLower(strings.TrimSpace(name))
	}

	candidate = reSlugChars.ReplaceAllString(candidate, "-")
	candidate = reMultiHyphen.ReplaceAllString(candidate, "-")
	candidate = strings.Trim(candidate, "-")

	if len(candidate) > 28 {
		candidate = candidate[:28]
		candidate = strings.TrimRight(candidate, "-")
	}

	if candidate == "" {
		candidate = fmt.Sprintf("site-%d", time.Now().Unix())
	}
	return candidate
}

// FriendApplication is a guest's friend-link submission while it waits for review. It is
// written beside the submitted logo as a .json sidecar, which is what lets the friends
// page show what is pending instead of only mailing it to the admin.
type FriendApplication struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Desc  string `json:"desc"`
	Tag   string `json:"tag"`
	Color string `json:"color,omitempty"`
	Email string `json:"email,omitempty"`
	Time  string `json:"time"`

	// Staged is the sidecar's base name, and the value the approve form posts. HasSVG
	// reports whether a logo came with the application.
	Staged string `json:"-"`
	HasSVG bool   `json:"-"`
}

// writeFriendApplication records a submission next to its logo. The sidecar is the
// application: one without a logo is still an application, and a moderator approving it
// supplies the fallback icon.
func writeFriendApplication(staged string, app FriendApplication) error {
	if err := os.MkdirAll(DirStagingFriend, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(app, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(DirStagingFriend, staged+".json"), data, 0o644)
}

// stagedFriendFile resolves a staged name to one of its files, refusing anything that is
// not a plain name: the value arrives from a form, so it must not name a path.
func stagedFriendFile(staged, ext string) (string, bool) {
	if staged == "" || staged != filepath.Base(staged) || strings.HasPrefix(staged, ".") {
		return "", false
	}
	return filepath.Join(DirStagingFriend, staged+ext), true
}

// loadFriendApplication reads one staged application by the name its form posts.
func loadFriendApplication(staged string) (FriendApplication, bool) {
	path, ok := stagedFriendFile(staged, ".json")
	if !ok {
		return FriendApplication{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return FriendApplication{}, false
	}
	var app FriendApplication
	if err := json.Unmarshal(raw, &app); err != nil {
		log.Printf("warn: parse %s failed: %v", path, err)
		return FriendApplication{}, false
	}
	if strings.TrimSpace(app.URL) == "" {
		return FriendApplication{}, false
	}
	app.Staged = staged
	if svgPath, ok := stagedFriendFile(staged, ".svg"); ok {
		app.HasSVG = fileExists(svgPath)
	}
	return app, true
}

// loadFriendApplications lists what is waiting for review, newest first.
func loadFriendApplications() []FriendApplication {
	entries, err := os.ReadDir(DirStagingFriend)
	if err != nil {
		return nil
	}
	out := make([]FriendApplication, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if app, ok := loadFriendApplication(strings.TrimSuffix(e.Name(), ".json")); ok {
			out = append(out, app)
		}
	}
	// A staged name starts with a Unix timestamp, so descending name order is newest
	// first.
	sort.Slice(out, func(i, j int) bool { return out[i].Staged > out[j].Staged })
	return out
}

// removeStagedFriendApplication deletes the staged pair. Removing it is what marks the
// application as handled, so approving one twice cannot add the link twice.
func removeStagedFriendApplication(staged string) {
	for _, ext := range []string{".json", ".svg"} {
		path, ok := stagedFriendFile(staged, ext)
		if !ok {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("friend: remove staged %s failed: %v", path, err)
		}
	}
}

// friendIconName picks the name an uploaded logo is stored under. A name already taken
// gets a numeric suffix, so adding a link never replaces another link's logo.
func friendIconName(slug string) string {
	if fileExists(homeSrcPath("icon", "friend-"+slug+".svg")) {
		return fmt.Sprintf("friend-%s-%d", slug, time.Now().Unix()%10000)
	}
	return "friend-" + slug
}

// storeFriendIcon writes an uploaded logo into the deployment's src/icon/, the tree
// srcReadPath prefers over the engine's.
func storeFriendIcon(iconName, svg string) error {
	path := homeSrcPath("icon", iconName+".svg")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(svg), 0o644)
}

// handleFriendApply handles both admin friend additions and visitor applications.
func handleFriendApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	siteURL := strings.TrimSpace(r.FormValue("url"))
	desc := strings.TrimSpace(r.FormValue("desc"))
	tag := strings.TrimSpace(r.FormValue("tag"))
	color := strings.TrimSpace(r.FormValue("color"))
	email := strings.TrimSpace(r.FormValue("email"))
	icon := strings.TrimSpace(r.FormValue("icon"))
	svgText := strings.TrimSpace(r.FormValue("svg"))

	if name == "" || siteURL == "" {
		http.Error(w, "Name and URL are required", http.StatusBadRequest)
		return
	}

	if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
		http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
		return
	}

	var cleanSVG string
	var err error
	if svgText != "" {
		cleanSVG, err = sanitizeSVG(svgText)
		if err != nil {
			http.Error(w, "SVG Error: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	admin := isAdmin(r)

	if admin {
		// Admin branch: add the friend link directly
		if cleanSVG != "" {
			iconName := friendIconName(generateSafeSlug(name, siteURL))
			if writeErr := storeFriendIcon(iconName, cleanSVG); writeErr == nil {
				icon = iconName
			}
		}

		if icon == "" {
			icon = "link"
		}

		list := loadFriends()
		list = append(list, FriendLink{
			Name:  name,
			URL:   siteURL,
			Desc:  desc,
			Icon:  icon,
			Tag:   tag,
			Color: color,
		})
		_ = saveFriends(list)
		invalidateCache(PageFriends)

		http.Redirect(w, r, PageFriends, http.StatusSeeOther)
		return
	}

	// Visitor branch: stage the submission for review, then notify the admin by email
	staged := fmt.Sprintf("%d_%s", time.Now().Unix(), generateSafeSlug(name, siteURL))
	tempSVGPath := ""
	if cleanSVG != "" {
		if path, ok := stagedFriendFile(staged, ".svg"); ok {
			if err := os.MkdirAll(DirStagingFriend, 0755); err == nil {
				if err := os.WriteFile(path, []byte(cleanSVG), 0644); err == nil {
					tempSVGPath = path
				}
			}
		}
	}
	if err := writeFriendApplication(staged, FriendApplication{
		Name:  name,
		URL:   siteURL,
		Desc:  desc,
		Tag:   tag,
		Color: color,
		Email: email,
		Time:  time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Printf("friend: stage %s failed: %v", staged, err)
	}

	if icon == "" && tempSVGPath == "" {
		icon = "link"
	}

	go sendFriendApplyNotify(friendApplyEmailData{
		Name:        name,
		URL:         siteURL,
		Desc:        desc,
		Tag:         tag,
		Color:       color,
		Email:       email,
		TempSVGPath: tempSVGPath,
		Icon:        icon,
		SVGContent:  template.HTML(cleanSVG),
		PageURL:     siteBaseURL() + PageFriends,
	})

	http.Redirect(w, r, PageFriends+"?applied=1", http.StatusSeeOther)
}

func friendRedirectTarget(r *http.Request) string {
	if redirect := strings.TrimSpace(r.FormValue("redirect")); redirect != "" {
		if strings.HasPrefix(redirect, "/") && !strings.HasPrefix(redirect, "//") {
			return redirect
		}
	}
	if ref := r.Referer(); strings.Contains(ref, PageFriends) {
		return PageFriends
	}
	return PageFriends
}

func handleAdminFriendEdit(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, PageFriends, http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	idx := -1
	if _, err := fmt.Sscanf(r.FormValue("index"), "%d", &idx); err != nil {
		idx = -1
	}

	name := strings.TrimSpace(r.FormValue("name"))
	siteURL := strings.TrimSpace(r.FormValue("url"))
	desc := strings.TrimSpace(r.FormValue("desc"))
	tag := strings.TrimSpace(r.FormValue("tag"))
	color := strings.TrimSpace(r.FormValue("color"))
	icon := strings.TrimSpace(r.FormValue("icon"))
	svgText := strings.TrimSpace(r.FormValue("svg"))

	if name == "" || siteURL == "" {
		http.Error(w, "Name and URL are required", http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(siteURL, "http://") && !strings.HasPrefix(siteURL, "https://") {
		http.Error(w, "URL must start with http:// or https://", http.StatusBadRequest)
		return
	}

	list := loadFriends()
	if idx < 0 || idx >= len(list) {
		http.Error(w, "Invalid friend index", http.StatusBadRequest)
		return
	}

	if tag == "" {
		color = ""
	} else if color == "" && tag == list[idx].Tag {
		color = list[idx].Color
	}

	if svgText != "" {
		cleanSVG, err := sanitizeSVG(svgText)
		if err != nil {
			http.Error(w, "SVG Error: "+err.Error(), http.StatusBadRequest)
			return
		}
		// An edit keeps the name its own link already uses, so re-editing replaces the
		// logo instead of accumulating one file per save.
		iconName := "friend-" + generateSafeSlug(name, siteURL)
		if writeErr := storeFriendIcon(iconName, cleanSVG); writeErr == nil {
			icon = iconName
		}
	}

	if icon == "" {
		icon = list[idx].Icon
	}
	if icon == "" {
		icon = "link"
	}

	list[idx] = FriendLink{
		Name:  name,
		URL:   siteURL,
		Desc:  desc,
		Icon:  icon,
		Tag:   tag,
		Color: color,
	}
	_ = saveFriends(list)
	invalidateCache(PageFriends)

	target := friendRedirectTarget(r)
	adminRespondRedirect(w, r, target)
}

func handleAdminFriendDelete(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, PageFriends, http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()
	idx := -1
	if _, err := fmt.Sscanf(r.FormValue("index"), "%d", &idx); err != nil {
		idx = -1
	}
	list := loadFriends()
	if idx >= 0 && idx < len(list) {
		list = append(list[:idx], list[idx+1:]...)
		_ = saveFriends(list)
		invalidateCache(PageFriends)
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		adminRespond(w, r, "")
		return
	}
	target := friendRedirectTarget(r)
	adminRespondRedirect(w, r, target)
}

// handleAdminFriendApprove publishes a staged application: its logo moves into the
// deployment's src/icon/ and the entry joins friend.json. Approval asks nothing of the
// moderator — reviewing the submission is the whole decision.
func handleAdminFriendApprove(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, PageFriends, http.StatusSeeOther)
		return
	}
	_ = r.ParseForm()

	staged := strings.TrimSpace(r.FormValue("staged"))
	app, ok := loadFriendApplication(staged)
	if !ok {
		http.Error(w, "Unknown friend application", http.StatusBadRequest)
		return
	}

	icon := "link"
	if app.HasSVG {
		svgPath, _ := stagedFriendFile(staged, ".svg")
		raw, err := os.ReadFile(svgPath)
		if err != nil {
			http.Error(w, "Read staged logo failed", http.StatusInternalServerError)
			return
		}
		iconName := friendIconName(generateSafeSlug(app.Name, app.URL))
		if err := storeFriendIcon(iconName, string(raw)); err != nil {
			http.Error(w, "Store friend logo failed", http.StatusInternalServerError)
			return
		}
		icon = iconName
	}

	list := append(loadFriends(), FriendLink{
		Name:  app.Name,
		URL:   app.URL,
		Desc:  app.Desc,
		Icon:  icon,
		Tag:   app.Tag,
		Color: app.Color,
	})
	_ = saveFriends(list)
	invalidateCache(PageFriends)
	removeStagedFriendApplication(staged)

	if r.Header.Get("X-Requested-With") == "fetch" {
		adminRespond(w, r, "")
		return
	}
	adminRespondRedirect(w, r, friendRedirectTarget(r))
}
