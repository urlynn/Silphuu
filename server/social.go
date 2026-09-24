package main

import (
	"encoding/json"
	"html/template"
	"log"
	"os"
	"strings"
)

// Social links: config/social.json, hand-written, one entry per icon in the profile card.
//
// The engine ships no defaults, so a deployment without the file renders no icons. An
// entry names its icon by path relative to src/, which lets a site supply its own SVG
// while the engine's copy stays the fallback for the path it does not override.

// SocialLink is one entry resolved for rendering, so the template needs no lookups.
type SocialLink struct {
	URL      string
	Label    string        // tooltip text; empty renders no title attribute
	External bool          // true for http(s), which opens in a new tab
	SVG      template.HTML // icon markup, already carrying data-icon
}

// socialFile mirrors the document so unknown keys survive a re-read by other tools.
type socialFile struct {
	Comment string       `json:"_comment,omitempty"`
	Links   []socialLink `json:"links"`
}

type socialLink struct {
	Icon  string `json:"icon"`
	URL   string `json:"url"`
	Label string `json:"label,omitempty"`
}

// loadSocialLinks resolves config/social.json at render time, so an edit takes effect on
// the next render rather than requiring a restart. An entry that does not resolve is
// dropped and logged — rendering it as an empty button would hide the mistake.
func loadSocialLinks() []SocialLink {
	raw, err := os.ReadFile(dataReadPath(FileSocial))
	if err != nil {
		// Absent is the normal state of a deployment that lists no social links.
		return nil
	}
	var f socialFile
	if err := json.Unmarshal(raw, &f); err != nil {
		log.Printf("warn: parse %s failed: %v", FileSocial, err)
		return nil
	}

	out := make([]SocialLink, 0, len(f.Links))
	for i, in := range f.Links {
		href, external := socialURL(in.URL)
		if href == "" {
			log.Printf("warn: %s link %d: %q is not http(s) or mailto, skipped", FileSocial, i, in.URL)
			continue
		}
		svg, err := ResolveIcon(in.Icon)
		if err != nil {
			log.Printf("warn: %s link %d: %v", FileSocial, i, err)
			continue
		}
		out = append(out, SocialLink{
			URL:      href,
			Label:    strings.TrimSpace(in.Label),
			External: external,
			SVG:      svg,
		})
	}
	return out
}

// socialURL accepts the schemes a social button can carry and reports whether the link
// leaves the site. Anything else — javascript:, data:, a bare word — is refused here
// rather than left to html/template's urlFilter, which degrades instead of dropping.
func socialURL(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return s, true
	case strings.HasPrefix(lower, "mailto:"):
		return s, false
	}
	return "", false
}
