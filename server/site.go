package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Site identity helpers. Every outbound URL (notification emails, CDN cache
// purges, unsubscribe links) must be built from these instead of hardcoding
// a domain, so a deployment only needs to change its configuration.

// siteBaseURL returns the public origin of this site (scheme + host, no
// trailing slash). Sourced from base_url in config/site.json, with the
// BASE_URL environment variable as highest priority.
func siteBaseURL() string {
	if u := strings.TrimSuffix(os.Getenv("BASE_URL"), "/"); u != "" {
		return u
	}
	if u := strings.TrimSuffix(loadAppConfig().BaseURL, "/"); u != "" {
		return u
	}
	return "http://localhost"
}

// cdnBaseURL returns the public origin targeted by CDN cache purges.
// Defaults to siteBaseURL; set CDN_BASE_URL when the CDN serves a
// different domain than the site origin.
func cdnBaseURL() string {
	if u := strings.TrimSuffix(os.Getenv("CDN_BASE_URL"), "/"); u != "" {
		return u
	}
	return siteBaseURL()
}

// cdnURL joins a root-relative path onto cdnBaseURL.
//
// The path is percent-encoded. Category names are Chinese, and the CDN matches a purge
// against the encoded form the browser actually requested — a raw UTF-8 path would match
// nothing and fail silently. Query strings are passed through untouched.
func cdnURL(path string) string {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	rawPath, query, hasQuery := strings.Cut(path, "?")
	out := cdnBaseURL() + (&url.URL{Path: rawPath}).EscapedPath()
	if hasQuery {
		out += "?" + query
	}
	return out
}

// notifyEmailFrom builds the email From header from the configured site,
// e.g. "Silphuu <noreply@example.com>".
func notifyEmailFrom() string {
	host := siteBaseURL()
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	return fmt.Sprintf("%s <noreply@%s>", loadAppConfig().Author, host)
}

// notifyAdminEmail returns the recipient address for admin notification
// emails, configured via the ADMIN_NOTIFY_EMAIL environment variable.
func notifyAdminEmail() string {
	return strings.TrimSpace(os.Getenv("ADMIN_NOTIFY_EMAIL"))
}

// externalLink returns s when it is usable as an href, else "".
//
// Configuration values reach href attributes, so the scheme must be constrained:
// a javascript: or data: URL there is an injection. Returning "" rather than an
// error keeps the caller honest — an unusable link degrades to plain text instead
// of taking its surrounding content down with it.
func externalLink(s string) string {
	s = strings.TrimSpace(s)
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return s
	}
	return ""
}

// siteName returns the configured site name (the "site_name" field of
// config/site.json, falling back to "author" if empty). Page titles and metadata are built from this rather than
// hardcoding a brand, so a deployment only needs to change its configuration.
func siteName() string {
	cfg := loadAppConfig()
	if cfg.SiteName != "" {
		return cfg.SiteName
	}
	return cfg.Author
}

// pageTitle builds a browser/OG title as "<page> - <site>". An empty page
// argument yields the bare site name, used by the home page.
func pageTitle(page string) string {
	if page == "" {
		return siteName()
	}
	return page + " - " + siteName()
}

// copyrightNotice builds the copyright line the RSS channel carries.
//
// The holder is the configured author, not the site name: the notice names who owns the
// writing, and a deployment that runs a brand name as its site_name still has a person
// behind the text. Both halves are derived rather than written into the engine — a literal
// line would ship one deployment's name inside every other deployment's feed.
//
// The year comes from the clock, which the feed otherwise avoids for lastBuildDate. The two
// are not the same case: a clock-derived lastBuildDate would change the bytes on every
// rebuild, while this changes once a year, so rebuilding still reproduces the same file.
func copyrightNotice() string {
	return fmt.Sprintf("© %d %s", time.Now().Year(), loadAppConfig().Author)
}
