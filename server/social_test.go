package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socialTestHome points the roots at a deployment directory holding the given
// config/social.json body and src/icon files, and restores them when the test ends.
func socialTestHome(t *testing.T, social string, icons map[string]string) string {
	t.Helper()
	home := t.TempDir()
	if social != "" {
		writeTestFile(t, filepath.Join(home, "config", "social.json"), social)
	}
	for name, body := range icons {
		writeTestFile(t, filepath.Join(home, "src", "icon", name), body)
	}

	prevEngine := engineRoot
	engineRoot = "."
	InitRoots(home)
	t.Cleanup(func() {
		engineRoot = prevEngine
		InitRoots("")
	})
	return home
}

// TestSocialLinksResolveFromTheDeploymentFile: a listed entry becomes a renderable link,
// carrying the icon markup and the flag that decides whether it opens a new tab.
func TestSocialLinksResolveFromTheDeploymentFile(t *testing.T) {
	socialTestHome(t, `{"links": [
		{"icon": "icon/probe.svg", "url": "https://example.com/me", "label": "Probe"},
		{"icon": "icon/probe.svg", "url": "mailto:me@example.com"}
	]}`, map[string]string{
		"probe.svg": `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`,
	})

	got := loadSocialLinks()
	if len(got) != 2 {
		t.Fatalf("loadSocialLinks() returned %d entries, want 2", len(got))
	}
	if got[0].URL != "https://example.com/me" || got[0].Label != "Probe" || !got[0].External {
		t.Errorf("entry 0 = %+v, want the https link with its label and External set", got[0])
	}
	if !strings.Contains(string(got[0].SVG), `data-icon="probe"`) {
		t.Errorf("entry 0 icon = %s, want the injected data-icon attribute", got[0].SVG)
	}
	if got[1].External {
		t.Errorf("entry 1 (mailto) External = true, want false: a mail client is not a new tab")
	}
	if got[1].Label != "" {
		t.Errorf("entry 1 Label = %q, want empty when the deployment omits it", got[1].Label)
	}
}

// TestSocialLinksWithoutTheFileRenderNothing: the engine ships no default row, so an
// unconfigured deployment has no icons at all.
func TestSocialLinksWithoutTheFileRenderNothing(t *testing.T) {
	home := socialTestHome(t, "", nil)
	if _, err := os.Stat(filepath.Join(home, "config", "social.json")); err == nil {
		t.Fatal("test precondition broken: social.json unexpectedly exists")
	}
	if got := loadSocialLinks(); len(got) != 0 {
		t.Errorf("loadSocialLinks() = %+v, want nothing without the file", got)
	}
}

// TestSocialLinkSchemesAreRestricted: a scheme the browser would execute is dropped
// rather than escaped into something that still looks like a working button.
func TestSocialLinkSchemesAreRestricted(t *testing.T) {
	socialTestHome(t, `{"links": [
		{"icon": "icon/probe.svg", "url": "javascript:alert(1)"},
		{"icon": "icon/probe.svg", "url": "data:text/html,x"},
		{"icon": "icon/probe.svg", "url": "ftp://example.com"},
		{"icon": "icon/probe.svg", "url": "   "},
		{"icon": "icon/probe.svg", "url": "https://example.com/me"}
	]}`, map[string]string{
		"probe.svg": `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`,
	})

	got := loadSocialLinks()
	if len(got) != 1 {
		t.Fatalf("loadSocialLinks() kept %d entries, want only the http(s) one: %+v", len(got), got)
	}
	if got[0].URL != "https://example.com/me" {
		t.Errorf("surviving entry = %q, want the https link", got[0].URL)
	}
}

// TestSocialIconPathMustStayUnderSrc: the path comes from configuration, so it may not
// climb out of the src/ roots it is resolved against.
func TestSocialIconPathMustStayUnderSrc(t *testing.T) {
	socialTestHome(t, `{"links": [
		{"icon": "../../../etc/passwd", "url": "https://example.com/a"},
		{"icon": "/etc/passwd", "url": "https://example.com/b"},
		{"icon": "", "url": "https://example.com/c"}
	]}`, nil)

	if got := loadSocialLinks(); len(got) != 0 {
		t.Errorf("loadSocialLinks() = %d entries, want 0: escaping paths must be refused", len(got))
	}
}

// TestSocialIconPrefersTheDeploymentCopy: a site that ships its own icon gets it, and the
// engine's file of the same path is the fallback for the site that does not.
func TestSocialIconPrefersTheDeploymentCopy(t *testing.T) {
	siteSVG := `<svg viewBox="0 0 24 24"><circle cx="1" cy="1" r="1"/></svg>`

	// No site copy: the path resolves into the engine's own icon set.
	socialTestHome(t, `{"links": [{"icon": "icon/github.svg", "url": "https://example.com"}]}`, nil)
	fallback := loadSocialLinks()
	if len(fallback) != 1 {
		t.Fatalf("loadSocialLinks() returned %d entries, want 1", len(fallback))
	}
	if !strings.Contains(string(fallback[0].SVG), `data-icon="github"`) {
		t.Errorf("icon = %s, want the engine's own src/icon/github.svg", fallback[0].SVG)
	}

	// A site copy of the same path wins.
	socialTestHome(t, `{"links": [{"icon": "icon/github.svg", "url": "https://example.com"}]}`,
		map[string]string{"github.svg": siteSVG})
	got := loadSocialLinks()
	if len(got) != 1 {
		t.Fatalf("loadSocialLinks() returned %d entries, want 1", len(got))
	}
	if !strings.Contains(string(got[0].SVG), `cx="1"`) {
		t.Errorf("icon = %s, want the deployment's own src/icon/github.svg", got[0].SVG)
	}
}

// TestProfileCardRendersTheSocialRowFromConfiguration: the rendered card carries one
// anchor per resolved entry, and none when the deployment lists none.
func TestProfileCardRendersTheSocialRowFromConfiguration(t *testing.T) {
	loadUIStrings()
	tmpl := loadTemplates()

	render := func(t *testing.T) string {
		t.Helper()
		var buf strings.Builder
		data := map[string]interface{}{"Motto": "motto", "UI": currentUIStrings()}
		// The file's whole body is inside {{define "profile_card"}}, so that is the name
		// to execute — the file name carries no markup of its own.
		if err := tmpl.ExecuteTemplate(&buf, "profile_card", data); err != nil {
			t.Fatalf("execute profile_card: %v", err)
		}
		return buf.String()
	}

	socialTestHome(t, `{"links": [
		{"icon": "icon/probe.svg", "url": "https://example.com/me", "label": "Probe"},
		{"icon": "icon/probe.svg", "url": "mailto:me@example.com"}
	]}`, map[string]string{
		"probe.svg": `<svg viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/></svg>`,
	})
	body := render(t)
	if got := strings.Count(body, `class="social-btn"`); got != 2 {
		t.Errorf("rendered %d social buttons, want 2", got)
	}
	if !strings.Contains(body, `title="Probe"`) {
		t.Error("rendered card is missing the configured tooltip")
	}
	if got := strings.Count(body, `target="_blank"`); got != 1 {
		t.Errorf("rendered %d new-tab links, want only the http(s) entry", got)
	}
	if !strings.Contains(body, `href="mailto:me@example.com"`) {
		t.Error("rendered card is missing the mailto entry")
	}

	socialTestHome(t, "", nil)
	if got := strings.Count(render(t), `class="social-btn"`); got != 0 {
		t.Errorf("rendered %d social buttons without config/social.json, want 0", got)
	}
}
