package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A variable the code reads and the service example does not name is a variable nobody can
// set, and nothing else reports it: the engine falls back to its default, or to nothing at
// all. The example is the only place a deployment learns a name exists, so the two sets are
// checked against each other here.

var (
	// The engine and the edge plugin it links reach the environment through these two.
	reGoEnvCall = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z0-9_]+)"\)`)

	// A reader that goes through a named constant leaves the name here instead. The name is
	// read from the constant's own value, so an indirection is still covered.
	reGoEnvName = regexp.MustCompile(`(?m)^\s*(?:const\s+)?env[A-Za-z0-9]+\s*=\s*"([A-Z0-9_]+)"`)

	// The status daemon is Rust and reads the environment one way.
	reRustEnvName = regexp.MustCompile(`env::var\("([A-Z0-9_]+)"\)`)

	// Both example files state a variable the same way. A commented line counts: the daemon's
	// peer link is a group of values a single-node deployment leaves out, and it is still a
	// name a deployment has to be able to find.
	reExampleExport = regexp.MustCompile(`(?m)^\s*#?\s*export\s+([A-Z0-9_]+)=`)
)

// envNamesIn collects the environment variable names the non-test files matching patterns
// carry, and refuses to return an empty set: a pattern that stopped matching, or a regex
// that stopped applying, would otherwise read as "nothing to check".
func envNamesIn(t *testing.T, root string, patterns []string, re ...*regexp.Regexp) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	files := 0
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("bad pattern %q: %v", pattern, err)
		}
		for _, path := range matches {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			files++
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			for _, r := range re {
				for _, m := range r.FindAllStringSubmatch(string(src), -1) {
					names[m[1]] = true
				}
			}
		}
	}
	if files == 0 {
		t.Fatalf("no source file matched %v under %s; the check would pass vacuously", patterns, root)
	}
	if len(names) == 0 {
		t.Fatalf("%d file(s) under %s matched %v and named no variable; the patterns no longer read them", files, root, patterns)
	}
	return names
}

// exampleNames collects the variables one example file states.
func exampleNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	names := map[string]bool{}
	for _, m := range reExampleExport.FindAllStringSubmatch(string(src), -1) {
		names[m[1]] = true
	}
	if len(names) == 0 {
		t.Fatalf("%s states no variable; it is empty, or the pattern no longer reads it", path)
	}
	return names
}

func missingFrom(want, have map[string]bool) []string {
	var out []string
	for name := range want {
		if !have[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestServiceExampleNamesEveryVariable(t *testing.T) {
	engineFile := filepath.Join("..", "examples", "service", "conf.d", "silphuu")
	daemonFile := filepath.Join("..", "examples", "service", "conf.d", "silphd")

	engineNames := exampleNames(t, engineFile)
	daemonNames := exampleNames(t, daemonFile)

	// The engine's file sources the daemon's, so a name either one states is one the engine's
	// deployment can set.
	both := map[string]bool{}
	for name := range engineNames {
		both[name] = true
	}
	for name := range daemonNames {
		both[name] = true
	}

	engineReads := envNamesIn(t, "..", []string{"server/*.go", "plugins/*/*.go"}, reGoEnvCall, reGoEnvName)
	if missing := missingFrom(engineReads, both); len(missing) > 0 {
		t.Errorf("the engine reads these and examples/service/conf.d states none of them: %s", strings.Join(missing, ", "))
	}

	// The daemon's own variables belong in its own file: it is the file the daemon sources,
	// and the engine only ever sees them through it.
	daemonReads := envNamesIn(t, "..", []string{"tools/silphd/src/*.rs"}, reRustEnvName)
	if missing := missingFrom(daemonReads, daemonNames); len(missing) > 0 {
		t.Errorf("the status daemon reads these and examples/service/conf.d/silphd states none of them: %s", strings.Join(missing, ", "))
	}
}
