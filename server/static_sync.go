package main

// static_sync.go — the sync face: paths whose bytes this node may not have.
//
// The face is configuration, not a rule in the engine. It names paths relative to the served
// root, and the engine never inspects what they mean. A deployment that ships the derived
// thumbnails names assets/thumb and assets/stickers.json; one that ships the sources instead
// names static. Both go through the same code, and widening the face is an edit to the setting
// rather than a change here.
//
// The face is shipped to the peer and never deleted, so a file the peer holds and this node does
// not is left alone. That is what makes the mechanism safe with more than one writer: "the peer
// has more than this node" stops meaning anything. What it costs is a file left behind at a peer,
// which is affordable for a path whose bytes can be rebuilt.
//
// This file is the face and its path rules. The transfer is in blob_sync.go.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// syncFacePaths reads the face.
//
// Read per call rather than cached: it changes only in a deployment's configuration, and reading
// it again keeps the mechanism testable.
func syncFacePaths() []string {
	return splitEnvList(os.Getenv("SILPHUU_SYNC_PATHS"))
}

// splitEnvList splits a comma-separated setting, dropping blanks.
func splitEnvList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// syncPathSource resolves one face entry to a local path.
//
// An entry has to be a clean path relative to the served root: the peer receives it as
// "<its own served root>/<entry>", so an absolute entry or one that walks upwards names
// something the peer would not agree with. Both are refused rather than normalised, because
// a setting that only almost works is worse than one that fails at startup.
func syncPathSource(entry string) (string, error) {
	if entry == "" {
		return "", fmt.Errorf("empty sync path")
	}
	rel := filepath.FromSlash(entry)
	if filepath.IsAbs(rel) || rel != filepath.Clean(rel) {
		return "", fmt.Errorf("sync path %q must be a clean path relative to the served root", entry)
	}
	full := filepath.Join(staticRoot, rel)
	if !insideServedRoot(full) {
		return "", fmt.Errorf("sync path %q resolves outside the served root", entry)
	}
	return full, nil
}

// insideServedRoot reports whether p is the served root itself or a descendant of it.
func insideServedRoot(p string) bool {
	root := filepath.Clean(staticRoot)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// insideTree reports whether p is strictly inside root.
func insideTree(root, p string) bool {
	root = filepath.Clean(root)
	return strings.HasPrefix(filepath.Clean(p), root+string(filepath.Separator))
}
