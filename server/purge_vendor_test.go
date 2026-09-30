package main

import (
	"go/build"
	"os"
	"sort"
	"strings"
	"testing"
)

// The default build must not name an edge product.
//
// Which edge a deployment uses is that deployment's choice, and the default build speaks it
// through one interface: the command outlet runs a script the deployment supplies. An engine
// that knows a vendor's name also knows that vendor's request shape, its batch limit and its
// error codes — and then a deployment on a different edge has to change the engine instead of
// its own command.
//
// A build that does want a vendor compiled in does it from a tagged file (purge_esa.go), which
// this guard leaves alone: the rule is about the default build, and a tagged file is not in it.
//
// This file is skipped by name so the needles can be written out plainly.
const purgeVendorGuardFile = "purge_vendor_test.go"

// purgeVendorNeedles are strings that only a build naming one edge product would carry.
var purgeVendorNeedles = []string{
	"aliyun",      // the CLI a shell script would call
	"PurgeCaches", // that product's API action
	"ESA_SITE_ID", // the env var carrying that product's site id
	"ESASiteID",   // the struct field carrying it in json
	"esa_site_id", // and the json key itself
	"esa.cn-",     // any hostname of that product's API
}

func TestDefaultBuildNamesNoEdgeProduct(t *testing.T) {
	scanned := 0
	var bad []string
	for _, name := range compiledGoFiles(t, nil) {
		if name == purgeVendorGuardFile {
			continue
		}
		scanned++
		data, err := os.ReadFile(name)
		if err != nil {
			continue // tolerate unreadable entries
		}
		for _, needle := range purgeVendorNeedles {
			if strings.Contains(string(data), needle) {
				bad = append(bad, name+": "+needle)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("scanned no Go files — the guard would have passed without looking at anything")
	}
	if len(bad) > 0 {
		t.Errorf("the engine names an edge product:\n  %s\n\n"+
			"A deployment's edge belongs in the command it supplies (SILPHUU_PURGE_CMD), "+
			"not in the engine. A built-in outlet goes in a file behind a build tag, so the "+
			"default build carries none of that vocabulary. See server/purge.go.",
			strings.Join(bad, "\n  "))
	}
}

// The guard only has teeth if the file it forbids is really outside the default build: a vendor
// name compiled in by default must be scanned, and the tagged file that carries one must not be.
// Without this, the guard passes for the wrong reason the moment purge_esa.go loses its tag.
func TestEdgeOutletIsBehindABuildTag(t *testing.T) {
	plain := compiledGoFiles(t, nil)
	if contains(plain, "purge_esa.go") {
		t.Fatal("purge_esa.go is in the default build, so the vendor guard scans it and the " +
			"engine ships that vocabulary to every deployment")
	}
	if !contains(compiledGoFiles(t, []string{"esa"}), "purge_esa.go") {
		t.Fatal("-tags esa does not compile purge_esa.go; the build that asks for the outlet " +
			"would not get it")
	}

	// The excluded file is the one that carries the vendor vocabulary, so excluding it is what
	// makes the guard's silence mean something.
	data, err := os.ReadFile("purge_esa.go")
	if err != nil {
		t.Fatalf("read purge_esa.go: %v", err)
	}
	if !strings.Contains(string(data), `"silphuu/plugins/esa"`) {
		t.Error("purge_esa.go does not pull in the ESA plugin; this control proves nothing about " +
			"what the default build is kept away from")
	}
}

// compiledGoFiles lists the .go files a build with these tags would compile, read from the
// directory rather than from the running test binary's own build.
func compiledGoFiles(t *testing.T, tags []string) []string {
	t.Helper()
	ctx := build.Default
	ctx.BuildTags = tags

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read %s: %v", ".", err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		match, err := ctx.MatchFile(".", name)
		if err != nil {
			t.Fatalf("match %s: %v", name, err)
		}
		if match {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// The guard has to be able to see the files it claims to guard.
func TestPurgeVendorGuardReadsTheEngine(t *testing.T) {
	if _, err := os.Stat("purge.go"); err != nil {
		t.Fatalf("the guard's scan root does not hold the engine: %v", err)
	}
}
