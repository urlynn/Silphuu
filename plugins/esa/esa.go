// Package esa purges Alibaba Cloud ESA cache entries over the OpenAPI V3
// protocol, signing requests with the standard library alone.
package esa

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultEndpoint = "esa.cn-hangzhou.aliyuncs.com"
	action          = "PurgeCaches"
	apiVersion      = "2024-09-10"
	signAlg         = "ACS3-HMAC-SHA256"

	// The API accepts at most 1000 entries and 10 distinct domains per call.
	maxFilesPerCall = 1000
)

var defaultHTTPClient = &http.Client{Timeout: 15 * time.Second}

// Client purges cache entries on one ESA site.
type Client struct {
	AccessKeyID     string
	AccessKeySecret string
	SiteID          int64
	// Endpoint is host[:port]. Empty means the public ESA endpoint.
	Endpoint string
	// HTTP is the client used for calls. Nil means a client with a 15s timeout.
	HTTP *http.Client
}

// NewClientFromEnv builds a client from ALIBABA_CLOUD_ACCESS_KEY_ID,
// ALIBABA_CLOUD_ACCESS_KEY_SECRET and ESA_SITE_ID. The error names the first
// variable that is missing or malformed.
func NewClientFromEnv() (*Client, error) {
	accessKeyID := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_ID")
	accessKeySecret := os.Getenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	if accessKeyID == "" || accessKeySecret == "" {
		return nil, errors.New("ALIBABA_CLOUD_ACCESS_KEY_ID or ALIBABA_CLOUD_ACCESS_KEY_SECRET is not set")
	}
	siteIDRaw := os.Getenv("ESA_SITE_ID")
	if siteIDRaw == "" {
		return nil, errors.New("ESA_SITE_ID is not set")
	}
	siteID, err := strconv.ParseInt(siteIDRaw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid ESA_SITE_ID: %v", err)
	}
	return &Client{
		AccessKeyID:     accessKeyID,
		AccessKeySecret: accessKeySecret,
		SiteID:          siteID,
	}, nil
}

// PurgeResponse is the subset of the PurgeCaches response a caller needs.
type PurgeResponse struct {
	RequestID string `json:"RequestId"`
	TaskID    string `json:"TaskId"`
}

// PurgeCaches purges urls, splitting them into batches the API accepts. The
// response is the last batch's.
func (c *Client) PurgeCaches(urls []string) (*PurgeResponse, error) {
	last := new(PurgeResponse)
	for start := 0; start < len(urls); start += maxFilesPerCall {
		batch := urls[start:min(start+maxFilesPerCall, len(urls))]
		resp, err := c.purgeBatch(batch)
		if err != nil {
			return nil, err
		}
		last = resp
	}
	return last, nil
}

func (c *Client) purgeBatch(urls []string) (*PurgeResponse, error) {
	content, err := json.Marshal(map[string][]string{"Files": urls})
	if err != nil {
		return nil, err
	}
	query := canonicalQuery(map[string]string{
		"Content": string(content),
		"SiteId":  strconv.FormatInt(c.SiteID, 10),
		"Type":    "file",
	})

	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequest(http.MethodPost, "https://"+endpoint+"/?"+query, nil)
	if err != nil {
		return nil, err
	}
	sign(req, c.AccessKeyID, c.AccessKeySecret,
		time.Now().UTC().Format("2006-01-02T15:04:05Z"), nonce())

	client := c.HTTP
	if client == nil {
		client = defaultHTTPClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ESA PurgeCaches failed: %s: %s", resp.Status, raw)
	}
	out := new(PurgeResponse)
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	return out, nil
}

// sign adds the V3 signature headers. Requests carry no body, so the payload
// hash is always that of the empty string.
func sign(req *http.Request, accessKeyID, accessKeySecret, date, nonce string) {
	payloadHash := sha256Hex(nil)
	req.Header.Set("x-acs-action", action)
	req.Header.Set("x-acs-content-sha256", payloadHash)
	req.Header.Set("x-acs-date", date)
	req.Header.Set("x-acs-signature-nonce", nonce)
	req.Header.Set("x-acs-version", apiVersion)

	names := []string{"host"}
	for name := range req.Header {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-acs-") {
			names = append(names, lower)
		}
	}
	sort.Strings(names)

	var canonicalHeaders strings.Builder
	for _, name := range names {
		value := req.Header.Get(name)
		if name == "host" {
			value = req.URL.Host
		}
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
	}
	signedHeaders := strings.Join(names, ";")
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.Path,
		req.URL.RawQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")
	stringToSign := signAlg + "\n" + sha256Hex([]byte(canonicalRequest))

	mac := hmac.New(sha256.New, []byte(accessKeySecret))
	mac.Write([]byte(stringToSign))
	req.Header.Set("Authorization", signAlg+
		" Credential="+accessKeyID+
		",SignedHeaders="+signedHeaders+
		",Signature="+hex.EncodeToString(mac.Sum(nil)))
}

func nonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}

// canonicalQuery sorts the parameters and percent-encodes them per RFC 3986.
func canonicalQuery(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, escape(key)+"="+escape(params[key]))
	}
	return strings.Join(pairs, "&")
}

// escape leaves the unreserved set alone and upper-cases every other byte.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
