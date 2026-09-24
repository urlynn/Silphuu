package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInitSiteScaffoldsSiteContent asserts scripts/init-site.sh hands a new deployment the
// two things the engine deliberately does not ship: head.html inlines src/css/site.css
// unconditionally, and every avatar slot reads assets/image/, down to the default avatar.
// A site missing the stylesheet ends up with an empty customisation layer; missing the
// images shows no avatar anywhere.
func TestInitSiteScaffoldsSiteContent(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "scripts", "init-site.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "site")
	if out, err := exec.Command("bash", script, dst).CombinedOutput(); err != nil {
		t.Fatalf("init-site.sh failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(dst, "src", "css", "site.css")); err != nil {
		t.Errorf("scaffolded site has no src/css/site.css: %v", err)
	}

	src := filepath.Join("..", "examples", "demo", "assets", "image")
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
		if _, err := os.Stat(filepath.Join(dst, "assets", "image", rel)); err != nil {
			t.Errorf("scaffolded site is missing assets/image/%s: %v", rel, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no example images found — the assertion above would pass vacuously")
	}
}
