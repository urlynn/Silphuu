package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
)

// fontURLMap maps font-family names to woff2 URLs, parsed from fonts.css at startup.
var fontURLMap map[string]string

// fontFaceRe matches @font-face rules, extracting font-family and src URL.
var fontFaceRe = regexp.MustCompile(`font-family:\s*['"]([^'"]+)['"]\s*;\s*src:\s*url\(['"]([^'"]+)['"]\)`)

// initFontURLMap parses the served fonts.css and builds the font-family -> URL map.
func initFontURLMap() {
	fontURLMap = make(map[string]string)
	data, err := os.ReadFile(srcReadPath("css/fonts.css"))
	if err != nil {
		log.Printf("warn: earlyhints: read fonts.css: %v", err)
		return
	}
	matches := fontFaceRe.FindAllStringSubmatch(string(data), -1)
	for _, m := range matches {
		if len(m) >= 3 {
			family := m[1]
			url := m[2]
			// Keep only the first URL per family (Regular weight)
			if _, exists := fontURLMap[family]; !exists {
				fontURLMap[family] = url
			}
		}
	}
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
			url, ok := fontURLMap[family]
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
// Applies only to full page loads (not htmx, not static, not admin).
func earlyHintsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.Header.Get("HX-Request") != "true" {
			path := r.URL.Path
			if !strings.HasPrefix(path, PrefixStatic+"/") &&
				!strings.HasPrefix(path, PrefixAssets+"/") &&
				!strings.HasPrefix(path, "/admin") &&
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
				} else if path == "/search" || path == "/archive" || path == "/posts" || path == "/about" || path == "/friends" || path == "/sponsor" || strings.HasPrefix(path, "/topic/") {
					w.Header().Add("Link", "<"+asset("css/browse.bundle.css")+">; rel=preload; as=style")
					w.Header().Add("Link", "<"+asset("js/browse.bundle.js")+">; rel=preload; as=script")
				} else if path == "/lalafell" {
					// fun_error.html pulls the browse stylesheet but none of its JS, so
					// preloading the script would only earn a "preloaded but not used".
					w.Header().Add("Link", "<"+asset("css/browse.bundle.css")+">; rel=preload; as=style")
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
						// JXL first, AVIF fallback (consistent with the CSS image-set)
						bgURL = picSrcGo(desktop.Src, "jxl")
						if bgURL == "" {
							bgURL = picSrcGo(desktop.Src, "avif")
						}
					}
					if bgURL != "" {
						w.Header().Add("Link", "<"+bgURL+">; rel=preload; as=image")
					}
					// Avatar: preload whatever format resolveAvatarURL picked — the same
					// function the templates call, so the header and the page can never
					// disagree about which URL is coming.
					if u := resolveAvatarURL(); u != "" {
						w.Header().Add("Link", "<"+u+">; rel=preload; as=image")
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
