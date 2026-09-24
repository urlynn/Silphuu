package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderFooter renders the shared footer partial for a site configuration and,
// when non-empty, a deployment-supplied templates/footer_content.html override.
func renderFooter(t *testing.T, cfg map[string]any, footerContent string) string {
	t.Helper()
	files := map[string]string{}
	if footerContent != "" {
		files["footer_content.html"] = footerContent
	}
	return renderFooterFiles(t, cfg, files)
}

// renderFooterFiles renders the shared footer partial with the given deployment-side
// files written into <home>/templates. Anything the deployment does not supply falls
// back to the engine's copy — that fallback is the whole point of the overlay.
func renderFooterFiles(t *testing.T, cfg map[string]any, files map[string]string) string {
	t.Helper()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if len(files) > 0 {
		tdir := filepath.Join(home, "templates")
		if err := os.MkdirAll(tdir, 0o755); err != nil {
			t.Fatalf("mkdir templates: %v", err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(tdir, name), []byte(body), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "config", "site.json"), raw, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	InitRoots(home)
	t.Cleanup(func() { InitRoots("") })

	initTemplates()
	loadUIStrings()

	var buf bytes.Buffer
	data := map[string]any{
		"Year": 2026,
		"UI":   currentUIStrings(),
	}
	if err := tmpl.ExecuteTemplate(&buf, "global_footer", data); err != nil {
		t.Fatalf("execute global_footer: %v", err)
	}
	return buf.String()
}

// TestFooterFixedLineAlwaysRenders: the RSS link and the attribution are the one
// part of the footer the engine owns. They must appear regardless of whether the
// deployment supplies any footer content.
func TestFooterFixedLineAlwaysRenders(t *testing.T) {
	out := renderFooter(t, map[string]any{"author": "Some Deployment"}, "")

	if !strings.Contains(out, "RSS") {
		t.Errorf("fixed line is missing the RSS link:\n%s", out)
	}
	if !strings.Contains(out, "Powered by") {
		t.Errorf("fixed line is missing the attribution:\n%s", out)
	}
	if !strings.Contains(out, ProjectURL) {
		t.Errorf("attribution does not link to ProjectURL (%s):\n%s", ProjectURL, out)
	}
	if !strings.Contains(out, ProjectName) {
		t.Errorf("attribution does not show ProjectName (%s):\n%s", ProjectName, out)
	}
}

// TestFooterContentDefaultsToEmpty: the engine presets no footer lines. A fresh
// deployment gets the fixed line and nothing else — no placeholder copyright, no
// assumed filing number.
func TestFooterContentDefaultsToEmpty(t *testing.T) {
	out := renderFooter(t, map[string]any{"author": "Some Deployment"}, "")

	if strings.Contains(out, "footer-text") {
		t.Errorf("default footer content rendered something:\n%s", out)
	}
	if strings.Contains(out, "footer-icp") {
		t.Errorf("default footer content mentions a filing number:\n%s", out)
	}
	// Only the fixed line should be present.
	if n := strings.Count(out, "<p"); n != 1 {
		t.Errorf("expected exactly one <p> (the fixed line), got %d:\n%s", n, out)
	}
}

// TestFooterContentIsDeploymentEditable: the footer body is a template the
// deployment can replace, which is how a filing number, a copyright line or a
// donation note gets in — without the engine knowing what any of them are.
func TestFooterContentIsDeploymentEditable(t *testing.T) {
	const override = `{{define "footer_content"}}
<p class="footer-text">我的版权 {{.Year}}</p>
<p class="footer-text"><a href="https://beian.miit.gov.cn/">粤ICP备TEST号</a></p>
{{end}}`

	out := renderFooter(t, map[string]any{"author": "配置里的名字"}, override)

	if !strings.Contains(out, "我的版权 2026") {
		t.Errorf("override content was not rendered:\n%s", out)
	}
	if !strings.Contains(out, "粤ICP备TEST号") {
		t.Errorf("override content lost the filing line:\n%s", out)
	}
	if !strings.Contains(out, "Powered by") {
		t.Errorf("fixed line disappeared when content was overridden:\n%s", out)
	}
}

// TestFooterContentCanUseTemplateVars: the body is a real template, so the year
// and author come from the engine rather than being hardcoded and left to rot.
func TestFooterContentCanUseTemplateVars(t *testing.T) {
	const override = `{{define "footer_content"}}<p class="footer-text">{{author}}</p>{{end}}`

	out := renderFooter(t, map[string]any{"author": "配置里的名字"}, override)

	if !strings.Contains(out, "配置里的名字") {
		t.Errorf("template variables are not available in footer content:\n%s", out)
	}
}

// TestFooterAttributionIsItsOwnFile: the attribution must not be inlined back into
// footer.html. Restyling the footer starts by copying that file, so an inline
// attribution would ride along into the deployment's tree and risk drift. Keeping it
// in its own file makes removing it a deliberate act on a file named after what it is.
func TestFooterAttributionIsItsOwnFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(engineRoot, "templates", "footer.html"))
	if err != nil {
		t.Fatalf("read templates/footer.html: %v", err)
	}
	layout := string(raw)

	if strings.Contains(layout, "Powered by") {
		t.Error("footer.html contains the attribution inline; it belongs in footer_attribution.html")
	}
	if !strings.Contains(layout, `{{template "footer_attribution" .}}`) {
		t.Error("footer.html no longer renders the footer_attribution partial")
	}

	partial, err := os.ReadFile(filepath.Join(engineRoot, "templates", "footer_attribution.html"))
	if err != nil {
		t.Fatalf("read templates/footer_attribution.html: %v", err)
	}
	if !strings.Contains(string(partial), "Powered by") {
		t.Error("footer_attribution.html does not contain the attribution line")
	}
}

// TestFooterAttributionSurvivesLayoutOverride: a deployment that copies footer.html
// to restyle the footer ships no footer_attribution.html of its own, so the
// attribution must resolve to the engine's copy through the overlay fallback.
func TestFooterAttributionSurvivesLayoutOverride(t *testing.T) {
	// A restyled footer: different markup, same two calls. The deployment supplies
	// nothing else — no footer_content.html, no footer_attribution.html.
	const layoutOverride = `{{define "global_footer"}}
<footer class="footer"><div class="footer-inner">
{{template "footer_content" .}}
<p class="footer-support">{{template "footer_attribution" .}}</p>
</div></footer>
{{end}}`

	out := renderFooterFiles(t, map[string]any{"author": "Some Deployment"},
		map[string]string{"footer.html": layoutOverride})

	for _, want := range []string{"Powered by", ProjectName, ProjectURL} {
		if !strings.Contains(out, want) {
			t.Errorf("layout override lost %q; the engine's attribution partial was not used:\n%s", want, out)
		}
	}
}

// TestFooterCreditIsAPeerOfTheAttribution: a deployment's own credit renders on the
// attribution's line, and neither displaces the other. That is the point of the
// field — the reason to delete the attribution is removed by giving the deployment
// its own place to sign, not by making deletion harder.
func TestFooterCreditIsAPeerOfTheAttribution(t *testing.T) {
	out := renderFooter(t, map[string]any{
		"author":     "Some Deployment",
		"credit":     "© 2026 Example Studio",
		"credit_url": "https://example.com",
	}, "")

	for _, want := range []string{"© 2026 Example Studio", `href="https://example.com"`, "Powered by", ProjectName} {
		if !strings.Contains(out, want) {
			t.Errorf("footer is missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "<p"); n != 1 {
		t.Errorf("the credit must share the attribution's line, got %d <p>:\n%s", n, out)
	}
}

// TestFooterCreditWithoutLink: credit_url is optional. Without one the credit is
// plain text — the name still shows.
func TestFooterCreditWithoutLink(t *testing.T) {
	out := renderFooter(t, map[string]any{"author": "x", "credit": "Example Studio"}, "")

	if !strings.Contains(out, `<span class="footer-credit">Example Studio</span>`) {
		t.Errorf("credit should render as plain text when no URL is configured:\n%s", out)
	}
}

// TestFooterCreditRejectsUnsafeURLScheme: an unusable credit_url degrades to plain
// text. Dropping the credit over a bad link would lose the deployment's name over a
// typo; rendering the link would put javascript: in an href.
func TestFooterCreditRejectsUnsafeURLScheme(t *testing.T) {
	out := renderFooter(t, map[string]any{
		"author":     "x",
		"credit":     "Example Studio",
		"credit_url": "javascript:alert(1)",
	}, "")

	if strings.Contains(out, "javascript:") {
		t.Errorf("an unsafe scheme reached the href:\n%s", out)
	}
	if !strings.Contains(out, "Example Studio") {
		t.Errorf("a bad credit_url must not drop the credit text:\n%s", out)
	}
}

// TestFooterUnchangedWithoutCredit: an unconfigured credit renders nothing, so every
// existing deployment's footer stays byte-for-byte what it was.
func TestFooterUnchangedWithoutCredit(t *testing.T) {
	out := renderFooter(t, map[string]any{"author": "x"}, "")

	if strings.Contains(out, "footer-credit") {
		t.Errorf("an unconfigured credit rendered markup:\n%s", out)
	}
}

// TestProjectURLIsUsable keeps the release checklist honest: a blank or malformed
// upstream URL puts a dead attribution link on every page.
//
// A fork should keep this pointing upstream — that is what attribution means — so
// the check is about validity, not about which repository it names.
func TestProjectURLIsUsable(t *testing.T) {
	if ProjectURL == "" {
		t.Fatal("ProjectURL must not be empty; the footer would render a dead link")
	}
	if !strings.HasPrefix(ProjectURL, "https://") {
		t.Errorf("ProjectURL = %q, want an https URL", ProjectURL)
	}
	if !projectURLIsValid() {
		t.Errorf("projectURLIsValid() = false for %q", ProjectURL)
	}
}
