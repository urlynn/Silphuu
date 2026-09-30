package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitSiteScaffoldsSiteContent asserts scripts/init-site.sh hands a new deployment the
// things the engine deliberately does not ship: head.html inlines src/css/site.css
// unconditionally, every avatar slot reads static/image/, down to the default avatar, and
// the formats those slots name come from config/hero.json. A site missing the stylesheet
// ends up with an empty customisation layer; missing the images shows no avatar anywhere;
// missing the record shows no avatar either, because a page renders the record and not the
// directory.
//
// The stylesheet is a deployment source and the images are served content, so the two land
// in different roots — see paths.go on why they cannot be the same directory.
func TestInitSiteScaffoldsSiteContent(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "scripts", "init-site.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "site")
	served := filepath.Join(t.TempDir(), "served")
	if out, err := exec.Command("bash", script, dst, served).CombinedOutput(); err != nil {
		t.Fatalf("init-site.sh failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(dst, "src", "css", "site.css")); err != nil {
		t.Errorf("scaffolded site has no src/css/site.css: %v", err)
	}

	// The site's own media is scaffolded into the deployment root, beside the rest of the
	// site's content, and the served tree reaches it through a link.
	src := filepath.Join("..", "examples", "demo", "static", "image")
	checked := 0
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil // dotfiles churn under the example site; they are not site content
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		checked++
		if _, err := os.Stat(filepath.Join(dst, "static", "image", rel)); err != nil {
			t.Errorf("scaffolded site is missing static/image/%s: %v", rel, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no example images found — the assertion above would pass vacuously")
	}

	// Every file the scaffolded record names has to be one the scaffold also wrote. The
	// record is what the pages read, so a copy of the images without it leaves the site
	// serving no avatar while the bytes sit in the tree unasked for.
	raw, err := os.ReadFile(filepath.Join(dst, "config", "hero.json"))
	if err != nil {
		t.Fatalf("scaffolded site has no config/hero.json: %v", err)
	}
	var hero struct {
		Avatar []string `json:"avatar"`
	}
	if err := json.Unmarshal(raw, &hero); err != nil {
		t.Fatalf("scaffolded config/hero.json is not valid JSON: %v", err)
	}
	if len(hero.Avatar) == 0 {
		t.Fatal("scaffolded config/hero.json records no avatar, so the copied images are never named")
	}
	for _, u := range hero.Avatar {
		rel := strings.TrimPrefix(u, StaticPrefix+"/")
		if rel == u {
			t.Errorf("config/hero.json records %s, which is not under %s", u, StaticPrefix)
			continue
		}
		if _, err := os.Stat(filepath.Join(dst, "static", filepath.FromSlash(rel))); err != nil {
			t.Errorf("config/hero.json records %s, which the scaffold does not have: %v", u, err)
		}
	}
}
