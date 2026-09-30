package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleRobots(t *testing.T) {
	req := httptest.NewRequest("GET", "/robots.txt", nil)
	req.Host = "example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := httptest.NewRecorder()

	handleRobots(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected Content-Type text/plain, got %s", ct)
	}

	body := rec.Body.String()
	expectedLines := []string{
		"User-agent: *",
		"Allow: /",
		"Sitemap: https://example.com/sitemap.xml",
	}

	for _, line := range expectedLines {
		if !strings.Contains(body, line) {
			t.Errorf("expected body to contain %q, got:\n%s", line, body)
		}
	}
}
