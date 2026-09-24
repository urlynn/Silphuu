package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEchoSecFetchDestProtection(t *testing.T) {
	// 1. fetch/XHR request with Sec-Fetch-Dest: empty should be blocked
	reqEmpty := httptest.NewRequest("GET", "/api/echo", nil)
	reqEmpty.Header.Set("Sec-Fetch-Dest", "empty")
	wEmpty := httptest.NewRecorder()
	handlePublicEcho(wEmpty, reqEmpty)

	if wEmpty.Code != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for Sec-Fetch-Dest: empty, got %d", wEmpty.Code)
	}

	// 2. Normal browser navigation with Sec-Fetch-Dest: document should be allowed
	reqDoc := httptest.NewRequest("GET", "/api/echo", nil)
	reqDoc.Header.Set("Sec-Fetch-Dest", "document")
	wDoc := httptest.NewRecorder()
	handlePublicEcho(wDoc, reqDoc)

	if wDoc.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for Sec-Fetch-Dest: document, got %d", wDoc.Code)
	}

	// 3. curl or tools without Sec-Fetch-Dest should be allowed
	reqCurl := httptest.NewRequest("GET", "/api/echo", nil)
	wCurl := httptest.NewRecorder()
	handlePublicEcho(wCurl, reqCurl)

	if wCurl.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for missing Sec-Fetch-Dest, got %d", wCurl.Code)
	}
}

func TestWrapUA_XSS(t *testing.T) {
	tmpl, err := loadTemplatesSafe()
	if err != nil {
		t.Fatalf("failed to load templates: %v", err)
	}

	var buf bytes.Buffer
	maliciousUA := "<script>alert('XSS')</script> · Windows 10"
	testTmpl, err := tmpl.New("test_ua").Parse(`{{wrapUA .}}`)
	if err != nil {
		t.Fatalf("failed to parse test template: %v", err)
	}

	if err := testTmpl.Execute(&buf, maliciousUA); err != nil {
		t.Fatalf("failed to execute test template: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "<script>") {
		t.Fatalf("XSS vulnerability detected! Raw <script> found in output: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("Expected escaped &lt;script&gt; in output, got: %s", out)
	}
	if !strings.Contains(out, `<span class="cmt-ua-sep"></span>`) {
		t.Fatalf("Expected separator span in output, got: %s", out)
	}
}
