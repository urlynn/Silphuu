package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExternalLinkAcceptsOnlyHTTP: configured URLs reach href attributes, so the
// scheme is the boundary that stops javascript:/data: from being an injection.
//
// An unusable value returns "" — the caller then renders plain text rather than
// dropping whatever the link was attached to.
//
// Schemes are compared case-insensitively: "HTTPS://" is a valid URL and rejecting
// it would be a bug, not caution.
func TestExternalLinkAcceptsOnlyHTTP(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"https", "https://example.com", "https://example.com"},
		{"http", "http://example.com", "http://example.com"},
		{"trimmed", "  https://example.com  ", "https://example.com"},
		{"uppercase scheme", "HTTPS://example.com", "HTTPS://example.com"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"javascript", "javascript:alert(1)", ""},
		{"data", "data:text/html,<script>alert(1)</script>", ""},
		{"protocol relative", "//example.com", ""},
		{"bare host", "example.com", ""},
		{"mailto", "mailto:someone@example.com", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := externalLink(tc.in); got != tc.want {
				t.Errorf("externalLink(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSiteBaseURLTakesBASE_URLOverTheFile: one config file is deployed to every node, so a
// base_url inside it names no node's origin — the environment is the only channel that can.
// A file value must not shadow it, which is what it did when the file carried the engine's
// own "http://localhost" placeholder.
func TestSiteBaseURLTakesBASE_URLOverTheFile(t *testing.T) {
	useExampleSite(t)

	cfg := loadAppConfig()
	cfg.BaseURL = "http://localhost"
	if err := saveAppConfig(cfg); err != nil {
		t.Fatalf("seed site config: %v", err)
	}
	t.Setenv("BASE_URL", "https://probe.example")

	if got := siteBaseURL(); got != "https://probe.example" {
		t.Errorf("siteBaseURL() = %q, want the environment's value", got)
	}
	// The override belongs to the resolver, not to the file: a getter that wrote it back
	// would persist a deployment's origin into the config every node shares.
	if got := loadAppConfig().BaseURL; got != "http://localhost" {
		t.Errorf("loadAppConfig().BaseURL = %q, want the file's own value", got)
	}
}

// TestServedPagesCarryTheEnvironmentsBaseURL: the browser builds every upload URL from the
// base URL in <head>, so a page advertising the file's value posts to the wrong origin.
// Asserted against the rendered page rather than the resolver, because the defect this
// guards against was the template layer reading a different source than siteBaseURL.
func TestServedPagesCarryTheEnvironmentsBaseURL(t *testing.T) {
	useExampleSite(t)

	cfg := loadAppConfig()
	cfg.BaseURL = "http://localhost"
	if err := saveAppConfig(cfg); err != nil {
		t.Fatalf("seed site config: %v", err)
	}
	t.Setenv("BASE_URL", "https://probe.example")

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", handleHome)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if !strings.Contains(string(body), `baseURL:"https://probe.example"`) {
		t.Error("the page does not carry the environment's base URL")
	}
	if strings.Contains(string(body), "localhost") {
		t.Error("the page carries the file's base URL, which the environment overrides")
	}
}
