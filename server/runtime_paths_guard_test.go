package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimePathsAreExcludedFromThePublicRepo asserts every path the engine writes at
// runtime is excluded by the public-repo filter, which is .gitignore.pub here and
// .gitignore in an export. See docs/GOTCHAS.md §runtime-output-roots.
func TestRuntimePathsAreExcludedFromThePublicRepo(t *testing.T) {
	filter, data := findPublicFilter(t)
	if len(runtimePaths) == 0 {
		t.Fatal("runtimePaths is empty; this check would pass vacuously")
	}

	// git decides ignore semantics. Reimplementing them here would only test the copy.
	probe := t.TempDir()
	if err := os.WriteFile(filepath.Join(probe, ".gitignore"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) (string, error) {
		out, err := exec.Command("git", append([]string{"-C", probe}, args...)...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	if out, err := git("init", "-q"); err != nil {
		t.Fatalf("git init in the probe tree: %v: %s", err, out)
	}

	for _, p := range runtimePaths {
		// Probe a child rather than the entry itself: the filter may exclude a whole
		// directory, and a directory pattern needs something under it to match.
		rel := filepath.Join("server", p, "probe.txt")
		if err := os.MkdirAll(filepath.Dir(filepath.Join(probe, rel)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(probe, rel), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := git("check-ignore", "-q", rel); err != nil {
			t.Errorf("server/%s is not excluded by %s, but the engine writes there at runtime: %s", p, filter, out)
		}
	}
}

// findPublicFilter returns the path and contents of the public-repo filter. A published
// export carries the whitelist installed as .gitignore, so the name alone does not
// identify it — the bare "/*" line does.
func findPublicFilter(t *testing.T) (string, []byte) {
	t.Helper()
	for _, name := range []string{".gitignore.pub", ".gitignore"} {
		p := filepath.Join("..", name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !hasRootExclusion(data) {
			continue
		}
		return p, data
	}
	t.Fatal("no public-repo whitelist found at ../.gitignore.pub or ../.gitignore")
	return "", nil
}

// hasRootExclusion reports whether the file excludes every root entry, which is the line
// the whitelist is identified by. The dev .gitignore excludes paths one at a time instead.
func hasRootExclusion(data []byte) bool {
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "/*" {
			return true
		}
	}
	return false
}
