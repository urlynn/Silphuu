package esa

import (
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestSignMatchesOfficialSDK pins the signer to a request the official Alibaba
// Cloud SDK emitted for PurgeCaches. The Authorization header is checked byte
// for byte, so any drift in the canonical request or the string to sign fails.
func TestSignMatchesOfficialSDK(t *testing.T) {
	const (
		host          = "127.0.0.1:56224"
		query         = "Content=%7B%22Files%22%3A%5B%22https%3A%2F%2Fexample.com%2Ftest-url-1%22%2C%22https%3A%2F%2Fexample.com%2Ftest-url-2%22%5D%7D&SiteId=123456789&Type=file"
		date          = "2026-09-27T20:35:21Z"
		nonceValue    = "ad08bf4f8d3072890741b04a0c33bdba"
		provider      = "static_ak"
		wantSignature = "ACS3-HMAC-SHA256 Credential=AKTEST," +
			"SignedHeaders=host;x-acs-action;x-acs-content-sha256;x-acs-credentials-provider;x-acs-date;x-acs-signature-nonce;x-acs-version," +
			"Signature=620f4164a55a2a894788bdbc39aad6e669aa6c59ec6173e7cb4f429bcb7729ad"
	)

	req, err := http.NewRequest(http.MethodPost, "https://"+host+"/?"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-acs-credentials-provider", provider)

	sign(req, "AKTEST", "SKTEST", date, nonceValue)

	if got := req.Header.Get("Authorization"); got != wantSignature {
		t.Fatalf("Authorization mismatch\n got: %s\nwant: %s", got, wantSignature)
	}
	if got := req.Header.Get("x-acs-content-sha256"); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("payload hash mismatch: %s", got)
	}
}

// BenchmarkSign measures preparing one purge request, which is the only work
// this package does in process; the HTTP round trip follows.
func BenchmarkSign(b *testing.B) {
	const (
		url   = "https://" + defaultEndpoint + "/?Content=%7B%22Files%22%3A%5B%22https%3A%2F%2Fexample.com%2Ftest-url-1%22%5D%7D&SiteId=123456789&Type=file"
		date  = "2026-09-27T20:35:21Z"
		value = "ad08bf4f8d3072890741b04a0c33bdba"
	)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			b.Fatal(err)
		}
		sign(req, "AKTEST", "SKTEST", date, value)
	}
}

// TestPurgeCaches hits the live API. It needs credentials plus at least one URL
// that belongs to the site, supplied as a whitespace-separated
// ESA_PURGE_TEST_URLS, so a plain `go test` never purges anything.
func TestPurgeCaches(t *testing.T) {
	client, err := NewClientFromEnv()
	if err != nil {
		t.Skipf("no ESA credentials: %v", err)
	}
	urls := strings.Fields(os.Getenv("ESA_PURGE_TEST_URLS"))
	if len(urls) == 0 {
		t.Skip("ESA_PURGE_TEST_URLS is not set")
	}
	resp, err := client.PurgeCaches(urls)
	if err != nil {
		t.Fatalf("PurgeCaches failed: %v", err)
	}
	if resp.RequestID == "" {
		t.Fatal("PurgeCaches returned no RequestId")
	}
	t.Logf("purged %d URL(s), request %s", len(urls), resp.RequestID)
}
