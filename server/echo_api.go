package main

import (
	"encoding/json"
	"net/http"
	"time"
)

func handlePublicEcho(w http.ResponseWriter, r *http.Request) {
	// Reject async fetches initiated by frontend JS (XSS defense: don't leak reflected content)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	headers := make(map[string][]string)
	for k, v := range r.Header {
		headers[k] = v
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "ok",
		"client_ip":   clientIP(r),
		"protocol":    r.Proto,
		"method":      r.Method,
		"uri":         r.RequestURI,
		"req_headers": headers,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
}

// handlePrivateEcho answers on the admin-scoped echo URL. The URL is what scopes it: /admin
// is the gateway's boundary, so this handler carries no identity check of its own.
func handlePrivateEcho(w http.ResponseWriter, r *http.Request) {
	// Reject async fetches initiated by frontend JS (XSS defense: don't leak reflected content)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	headers := make(map[string][]string)
	for k, v := range r.Header {
		headers[k] = v
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "ok",
		"client_ip":     clientIP(r),
		"server_socket": r.RemoteAddr,
		"protocol":      r.Proto,
		"method":        r.Method,
		"uri":           r.RequestURI,
		"req_headers":   headers,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	})
}
