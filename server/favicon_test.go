package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// The favicon answers at two URLs, and they are two files with two types: a real ICO for the one
// browsers ask for by convention, and the SVG for the one that scales.
//
// Serving the SVG at /favicon.ico — which this did — puts an image/svg+xml body at a name every
// client and crawler reads as a Windows icon. It also left that URL with no bytes of its own to
// compare against, so the invalidation worker had to send it to the edge on every flush.
func TestFaviconURLsServeTheirOwnFiles(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	public := filepath.Join(site, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const svgBody = `<svg xmlns="http://www.w3.org/2000/svg"/>`
	const icoBody = "\x00\x00\x01\x00\x01\x00\x10\x10"
	for name, body := range map[string]string{"favicon.svg": svgBody, "favicon.ico": icoBody} {
		if err := os.WriteFile(filepath.Join(public, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	mux := http.NewServeMux()
	registerPageRoutes(mux)

	for _, tc := range []struct{ path, contentType, body string }{
		{RouteFaviconSvg, "image/svg+xml", svgBody},
		{RouteFaviconIco, "image/x-icon", icoBody},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", tc.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != tc.contentType {
			t.Errorf("GET %s Content-Type = %q, want %q", tc.path, got, tc.contentType)
		}
		if got := rec.Body.String(); got != tc.body {
			t.Errorf("GET %s served %q, want the file of that name", tc.path, got)
		}
	}
}

// A deployment that ships only the SVG still has to answer both URLs: an ICO nobody has is
// worse than an SVG under the wrong name.
func TestFaviconIcoFallsBackToTheSVG(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	public := filepath.Join(site, "public")
	if err := os.MkdirAll(public, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const svgBody = `<svg xmlns="http://www.w3.org/2000/svg"/>`
	if err := os.WriteFile(filepath.Join(public, "favicon.svg"), []byte(svgBody), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mux := http.NewServeMux()
	registerPageRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", RouteFaviconIco, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", RouteFaviconIco, rec.Code)
	}
	if got := rec.Body.String(); got != svgBody {
		t.Errorf("GET %s served %q, want the SVG as the fallback", RouteFaviconIco, got)
	}
}
