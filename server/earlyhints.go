package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
)

// fontURLMap maps font-family names to woff2 URLs, parsed from fonts.css. Each URL carries
// the token of the subset it names, so a 103 preload asks for the same bytes the stylesheet
// does; a preload that names the previous generation warms nothing.
var fontURLMap atomic.Pointer[map[string]string]

// fontFaceRe matches @font-face rules, extracting font-family and src URL.
var fontFaceRe = regexp.MustCompile(`font-family:\s*['"]([^'"]+)['"]\s*;\s*src:\s*url\(['"]([^'"]+)['"]\)`)

// initFontURLMap builds the font-family -> URL map at startup.
func initFontURLMap() {
	refreshFontURLMap()
}

// refreshFontURLMap rebuilds the map from the served fonts.css.
//
// The stylesheet is read from src/, not from the bundle that carries it: the bundle's own
// token changes whenever this map does, so reading it here would be circular.
func refreshFontURLMap() {
	m := make(map[string]string)
	data, err := os.ReadFile(srcReadPath("css/fonts.css"))
	if err != nil {
		log.Printf("warn: earlyhints: read fonts.css: %v", err)
		fontURLMap.Store(&m)
		return
	}
	matches := fontFaceRe.FindAllStringSubmatch(string(data), -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		family, url := match[1], match[2]
		// Keep only the first URL per family (Regular weight)
		if _, exists := m[family]; !exists {
			m[family] = versionedAssetURL(url)
		}
	}
	fontURLMap.Store(&m)
}

// parseFontFamily extracts the family name "LemiMuhe" from a preset value "'LemiMuhe'|400".
func parseFontFamily(val string) string {
	s := strings.TrimSpace(val)
	if len(s) >= 2 && s[0] == '\'' {
		end := strings.Index(s[1:], "'")
		if end >= 0 {
			return s[1 : 1+end]
		}
	}
	return ""
}

// getEarlyHintsFontURLs returns the deduplicated font URLs used by the active preset.
// The "cmt" font is excluded (lazy-loaded by the service worker); empty data_override_*
// entries are excluded too.
func getEarlyHintsFontURLs() []string {
	cfg := loadAppConfig()
	var urls []string
	seen := make(map[string]bool)

	skipRoles := map[string]bool{
		"cmt":               true,
		"data_override_en":  true,
		"data_override_num": true,
		"data_override_sym": true,
	}

	urlsByFamily := fontURLMap.Load()
	if urlsByFamily == nil {
		return nil
	}

	for _, preset := range cfg.FontPresets {
		if preset.ID != cfg.ActiveFontPreset {
			continue
		}
		for role, val := range preset.Fonts {
			if skipRoles[role] {
				continue
			}
			family := parseFontFamily(val)
			if family == "" {
				continue
			}
			url, ok := (*urlsByFamily)[family]
			if !ok || seen[url] {
				continue
			}
			seen[url] = true
			urls = append(urls, url)
		}
		break
	}
	return urls
}

// earlyHintsMiddleware sends 103 Early Hints with font preloads before the final response.
// Applies only to full page loads (not htmx, not static) — including the two editor forms,
// which are the only admin paths that render a page rather than JSON or a fragment.
func earlyHintsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.Header.Get("HX-Request") != "true" {
			path := r.URL.Path
			// Every admin path is excluded except the two editor forms: those are the only
			// ones that render a page, so they are the only ones a preload can help.
			adminPath := strings.HasPrefix(path, PrefixAdmin) && !strings.HasPrefix(path, PrefixAdminEditor)
			if !strings.HasPrefix(path, PrefixStatic+"/") &&
				!strings.HasPrefix(path, PrefixAssets+"/") &&
				!adminPath &&
				!strings.HasPrefix(path, "/api/") &&
				path != "/sw.js" &&
				!strings.HasSuffix(path, ".json") &&
				!strings.HasPrefix(path, "/feed") &&
				!strings.HasPrefix(path, "/sitemap") &&
				!strings.HasPrefix(path, "/debug") {

				// Core universal bundle preload (instant). The deployment's stylesheet
				// override is deliberately not here: head.html inlines it into every page,
				// so there is no request left to preload.
				coreCSS := asset("css/core.bundle.css")
				coreJS := asset("js/core.bundle.js")
				w.Header().Add("Link", "<"+coreCSS+">; rel=preload; as=style")
				w.Header().Add("Link", "<"+coreJS+">; rel=preload; as=script")

				// Page-specific CSS & JS bundle preload
				if path == "/" {
					w.Header().Add("Link", "<"+asset("css/home.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/home.bundle.js")+">; rel=preload; as=script")
				} else if strings.HasPrefix(path, "/post/") {
					w.Header().Add("Link", "<"+asset("css/article.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("css/comment.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/article.bundle.js")+">; rel=preload; as=script")
					w.Header().Add("Link", "<"+asset("js/comment.bundle.js")+">; rel=preload; as=script")
				} else if path == "/guestbook" {
					w.Header().Add("Link", "<"+asset("css/comment.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/comment.bundle.js")+">; rel=preload; as=script")
				} else if path == "/about" {
					// The photo wall is the only page that loads photo.bundle.js, so it cannot
					// share the browse group's preload list.
					w.Header().Add("Link", "<"+asset("css/browse.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/browse.bundle.js")+">; rel=preload; as=script")
					w.Header().Add("Link", "<"+asset("js/photo.bundle.js")+">; rel=preload; as=script")
				} else if path == "/search" || path == "/archive" || path == "/posts" || path == "/friends" || path == "/sponsor" || strings.HasPrefix(path, "/topic/") {
					w.Header().Add("Link", "<"+asset("css/browse.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/browse.bundle.js")+">; rel=preload; as=script")
				} else if path == "/lalafell" {
					// fun_error.html pulls the browse stylesheet but none of its JS, so
					// preloading the script would only earn a "preloaded but not used".
					w.Header().Add("Link", "<"+asset("css/browse.bundle.css")+">; rel=preload; as=style")
				} else if strings.HasPrefix(path, PrefixAdminEditor) {
					w.Header().Add("Link", "<"+asset("css/admin.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("css/editor.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/editor.bundle.js")+">; rel=preload; as=script")
				}
				// Font preload
				fontURLs := getEarlyHintsFontURLs()
				for _, u := range fontURLs {
					w.Header().Add("Link", "<"+u+">; rel=preload; as=font; crossorigin")
				}

				// Home: preload background + avatar; the picked background is stored in context
				// for handleHome/cache reuse
				if path == "/" {
					desktop, mobile := pickHomeBackgrounds()
					r = r.WithContext(context.WithValue(r.Context(), heroBgCtxKey, heroBgs{Desktop: desktop, Mobile: mobile}))

					// Only preload the background when both viewports share one image. With
					// two different ones there is no way to know here which the visitor will
					// get — a Link preload has no media query — and guessing means every
					// mobile visitor downloads a picture it never displays.
					bgURL := ""
					if desktop.Src != "" && desktop.Src == mobile.Src {
						// JXL first, AVIF fallback (consistent with the CSS image-set). picSrcGo
						// does not check which of the two the site shipped — this node may not
						// hold the tree — so the hint names the preferred one and a client that
						// cannot use it simply makes the request the hint would have saved.
						bgURL = picSrcGo(desktop.Src, "jxl")
						if bgURL == "" {
							bgURL = picSrcGo(desktop.Src, "avif")
						}
					}
					if bgURL != "" {
						w.Header().Add("Link", "<"+bgURL+">; rel=preload; as=image")
					}
					// Avatar: preload the URL the templates will render as src — the same
					// function the templates call, so the header and the page can never
					// disagree about which URL is coming.
					if pic := avatarURL(); pic != nil {
						w.Header().Add("Link", "<"+pic.Src+">; rel=preload; as=image")
					}
				}

				// Send 103 only if we have Link headers
				if len(w.Header().Values("Link")) > 0 {
					w.WriteHeader(http.StatusEarlyHints) // 103
					w.Header().Del("Link")
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
