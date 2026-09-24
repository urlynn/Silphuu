package main

import "testing"

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
