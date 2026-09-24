package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// handleStatusSentinel serves only the metadata the browser needs to connect to the
// WebTransport probe: IP, port, and the self-signed certificate SHA-256 fingerprint.
// All hardware metrics (CPU/RAM/load), traffic stats (IPv6/IPv4), CDT reconciliation,
// and historical logs are fully owned by the Rust net-traffic service.
func handleStatusSentinel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	ipV4 := strings.TrimSpace(os.Getenv("SENTINEL_IP_V4"))
	ipV6 := strings.TrimSpace(os.Getenv("SENTINEL_IP_V6"))

	portStr := strings.TrimSpace(os.Getenv("SENTINEL_PORT"))
	port := 0
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}

	certHash := ""
	hashCandidates := []string{
		"/run/net-traffic.hash",
		"/tmp/net-traffic.hash",
		"./net-traffic.hash",
	}
	for _, p := range hashCandidates {
		if data, err := os.ReadFile(p); err == nil {
			certHash = strings.TrimSpace(string(data))
			if certHash != "" {
				break
			}
		}
	}

	ip := ipV4
	if ip == "" {
		ip = ipV6
	}

	enabled := ip != "" && port > 0 && certHash != ""

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"enabled":   enabled,
		"ip":        ip,
		"ip_v4":     ipV4,
		"ip_v6":     ipV6,
		"port":      port,
		"cert_hash": certHash,
	})
}
