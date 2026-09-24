package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Overlay: a deployment's own files replacing the shipped ones. One rule, no merging:
// a file present in the deployment directory is used; a file that is not present falls
// back to the release copy. Nothing is combined field by field — what you write is what
// you get. See docs/RELEASE-AND-OVERLAY.md.

// overlayRelPath returns the path to READ a shipped file from: the deployment's
// own copy when it exists, otherwise the release copy.
//
// Go's template parser names templates by base name, so an absolute deployment
// path still registers under the expected template name.
func overlayRelPath(rel string) string {
	return dataReadPath(filepath.Join(homeRoot, rel))
}

// templatePath resolves a template file name, preferring the deployment's copy.
func templatePath(name string) string {
	return overlayRelPath(filepath.Join("templates", name))
}

// templateNames returns every template file name, unioning the release set with
// the deployment's own. When a name exists in both, the deployment's copy is what
// templatePath resolves to, so the file is listed once.
func templateNames() ([]string, error) {
	seen := map[string]bool{}
	var names []string
	for _, dir := range []string{filepath.Join(engineRoot, "templates"), filepath.Join(homeRoot, "templates")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".html" || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// overlaySummary lists the files the deployment supplies, for the startup log.
// The paths are the very variables the engine reads with, so the report cannot
// drift from the layout. An empty result means the site runs on the shipped defaults.
func overlaySummary() []string {
	if homeRoot == "." {
		return nil
	}
	var found []string
	for _, own := range []string{
		FileConfig,
		FileSocial,
		FileFontsData,
		FileUIStrings,
		FileUIStringsAdmin,
		FileNavSignatures,
		FileFriendData,
		FileBackgroundData,
		filepath.Join(homeRoot, "templates"),
		DirImagePost,
	} {
		if _, err := os.Stat(own); err != nil {
			continue
		}
		if rel, err := filepath.Rel(homeRoot, own); err == nil {
			found = append(found, filepath.ToSlash(rel))
		}
	}
	return found
}

// overlayLogLine renders the startup summary, or "" when nothing is overridden.
func overlayLogLine() string {
	found := overlaySummary()
	if len(found) == 0 {
		return ""
	}
	return "overlay: using the deployment's own " + strings.Join(found, ", ")
}

// Render cache invalidation: public/ holds pre-rendered HTML. The file watcher
// invalidates it at runtime; the startup check below covers edits made while the
// server was down.

// renderStateFiles are written at runtime and never change page markup, so they
// must not trigger a cache clear.
var renderStateFiles = map[string]bool{
	"sync-state.json":  true,
	"views-local.json": true,
}

// dedupePaths drops repeated paths while preserving order. The fingerprint is built in
// iteration order, so it must stay deterministic.
func dedupePaths(paths ...string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// renderInputFingerprint hashes the inputs that decide what a rendered page looks like:
// configuration, font state, UI strings, templates, posts and the hand-authored assets.
//
// It hashes file *contents*, not mtime and size. Contents are what the render actually
// depends on, and a content hash stays stable when a file is rewritten with identical
// bytes — which is what makes covering the asset tree possible at all.
func renderInputFingerprint() string {
	h := sha256.New()
	addFile := func(path string) {
		sum, err := fileContentHash(path)
		if err != nil {
			return
		}
		fmt.Fprintf(h, "%s\x00%s\x00", path, sum)
	}
	addTree := func(root string, skip func(rel string) bool) {
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			if skip != nil {
				if rel, relErr := filepath.Rel(root, path); relErr == nil && skip(filepath.ToSlash(rel)) {
					return nil
				}
			}
			addFile(path)
			return nil
		})
	}

	// Configuration and state: top-level .json only, minus the counters that never change
	// page markup. The engine ships config/ defaults; state/ is the deployment's alone.
	for _, dir := range dedupePaths(
		filepath.Join(engineRoot, "config"),
		filepath.Join(homeRoot, "config"),
		filepath.Join(homeRoot, "state"),
	) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" || renderStateFiles[e.Name()] {
				continue
			}
			addFile(filepath.Join(dir, e.Name()))
		}
	}

	// Templates and posts.
	for _, root := range dedupePaths(
		filepath.Join(engineRoot, "templates"),
		filepath.Join(homeRoot, "templates"),
		RootPosts,
	) {
		addTree(root, nil)
	}

	// Authored sources: the engine's src/ and the deployment's own, laid over it. assets/
	// holds only build output and site content, so it is never an input.
	for _, root := range assetSourceTrees() {
		addTree(root, nil)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// invalidateCacheIfRenderInputsChanged drops the render cache when the inputs no longer
// match the fingerprint recorded alongside it, and purges those pages from the CDN.
// Returns true when the cache was dropped.
func invalidateCacheIfRenderInputsChanged() bool {
	if os.Getenv("DEV_MODE") == "1" {
		return false
	}
	if _, err := os.Stat(RootRender); err != nil {
		return false // nothing cached yet
	}

	stampPath := filepath.Join(RootRender, ".render-inputs")
	current := renderInputFingerprint()

	prev, err := os.ReadFile(stampPath)
	if err == nil && strings.TrimSpace(string(prev)) == current {
		return false
	}

	// Either the inputs changed, or there is a cache with no stamp (built by a
	// build that predates this check, or by a run that started before any cache
	// existed). Both are treated the same way: rebuild.
	//
	// An earlier version adopted the current inputs as the baseline when the stamp
	// was missing, to save one rebuild. That was a bad trade: the very next restart
	// after a first run then served the previous configuration's pages, which is
	// precisely the confusing symptom this check exists to prevent. One extra
	// rebuild is cheaper than a silently stale page.
	reason := "configuration, templates, posts or assets changed"
	if err != nil {
		reason = "render cache had no input fingerprint"
	}

	// Drop the local cache AND purge the CDN. Only doing the former left the edge serving
	// the previous pages for a year: the CDN caches HTML with s-maxage=31536000, so a page
	// whose inputs changed has to be purged by URL — there is no TTL to fall back on.
	invalidateAllCache()
	if err := os.MkdirAll(RootRender, 0o755); err != nil {
		log.Printf("overlay: cannot recreate %s: %v", RootRender, err)
		return true
	}
	if err := os.WriteFile(stampPath, []byte(current), 0o644); err != nil {
		log.Printf("overlay: cannot write %s: %v", stampPath, err)
	}
	log.Printf("overlay: render cache invalidated (%s)", reason)
	return true
}
