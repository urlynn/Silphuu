package main

// stats.go — visit statistics (fully in-memory, no SQLite).
//
// Writing/aggregation for visit_log and post views live in the views.go memory
// layer; this file keeps the stats-related validation helpers and the two
// stats endpoints.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Data exposed to templates: PV/UV x today/total = 4 values
type VisitStats struct {
	TodayUV int
	TodayPV int
	TotalUV int
	TotalPV int
}

// Path prefixes ignored (not counted as page views)
var skipPrefixes = []string{
	PrefixStatic + "/", PrefixAssets + "/", "/admin",
	"/feed.xml", "/sitemap.xml", "/robots.txt",
	"/debug/", "/api/", "/sw.js",
}

func isPageView(path string) bool {
	for _, p := range skipPrefixes {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

func ipUaHash(ip, ua string) string {
	h := sha256.Sum256([]byte(ip + "|" + ua))
	return fmt.Sprintf("%x", h[:8])
}

// Real client IP: normalized to X-Real-IP / X-Forwarded-For by the Nginx gateway layer.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(ip)
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if idx := strings.IndexByte(xff, ','); idx >= 0 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if idx := strings.LastIndexByte(host, ':'); idx >= 0 {
		host = host[:idx]
	}
	return host
}

// Common crawler user agents; excluded from stats
var botUA = regexp.MustCompile(`(?i)(bot|crawl|spider|slurp|bingpreview|googlebot|baiduspider|yandex|duckduck|facebookexternalhit|preview|headless|curl|wget|python-requests|go-http|http-client)`)

// Post page path: /post/{id} (used to attribute read counts)
var postIDRe = regexp.MustCompile(`^/post/(\d+)`)

// Called by template rendering: returns live aggregates (in-memory)
func getVisitStats() VisitStats {
	return computeStats()
}

// POST /api/view — beacon counter (the site's only counting source, in-memory).
func handleViewAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("p")
	if path == "" {
		path = r.URL.Path
	}
	if !isPageView(path) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	ua := r.Header.Get("User-Agent")
	if botUA.MatchString(ua) {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	hash := ipUaHash(clientIP(r), ua)
	latest := recordView(path, hash)

	resp := map[string]interface{}{"ok": true}
	if latest > 0 {
		resp["views"] = latest
	}
	json.NewEncoder(w).Encode(resp)
}

// GET /api/stats — returns live aggregates (in-memory).
func handleStatsAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(computeStats())
}
