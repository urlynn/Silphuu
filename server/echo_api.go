package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func getClientIP(r *http.Request) string {
	if ip := r.Header.Get("Ali-Real-Ip"); ip != "" {
		return ip
	}
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	return r.RemoteAddr
}

func checkIsAdmin(r *http.Request) bool {
	return r.Header.Get("X-Is-Admin") == "1"
}

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
		"client_ip":   getClientIP(r),
		"protocol":    r.Proto,
		"method":      r.Method,
		"uri":         r.RequestURI,
		"req_headers": headers,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	})
}

func handlePrivateEcho(w http.ResponseWriter, r *http.Request) {
	// Reject async fetches initiated by frontend JS (XSS defense: don't leak reflected content)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		w.WriteHeader(http.StatusForbidden)
		return
	}

	if !checkIsAdmin(r) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"status": "error",
			"error":  "Forbidden",
		})
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
		"is_admin":      true,
		"client_ip":     getClientIP(r),
		"server_socket": r.RemoteAddr,
		"protocol":      r.Proto,
		"method":        r.Method,
		"uri":           r.RequestURI,
		"req_headers":   headers,
		"timestamp":     time.Now().UTC().Format(time.RFC3339),
	})
}
