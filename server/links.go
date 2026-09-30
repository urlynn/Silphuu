package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// The served tree is what a static web server reads, and the private root holds the site's
// own content. One directory has to be reachable by URL without being duplicated, so it is
// declared here and created as a symlink: one set of bytes, two names, nothing to keep in
// step.
//
// A directory link carries a whole tree, so a file added inside it needs no reconciliation.
// A member link carries one entry into a directory that also holds generated output, which
// is why public/ is declared per member: its files land at the URL root, where the name
// itself is the address.
//
// The set is reconciled rather than applied. A link whose source is gone is removed, a
// name already taken by a real file is an error rather than something to overwrite, and a
// link pointing elsewhere is repaired with a log line rather than replaced in silence:
// which of the two is the site's content has to stay answerable.
var linkSet = []struct {
	from   string // private root relative
	to     string // served tree relative; empty is the served root itself
	member bool   // true links each member of from into to, false links from itself
}{
	{from: "static", to: "static"},
	{from: "public", to: "", member: true},
}

// linkState is what a destination looks like before ensureLinks touches it.
type linkState int

const (
	linkOK       linkState = iota // a symlink already pointing at the declared source
	linkAbsent                    // nothing there
	linkStale                     // a symlink pointing somewhere else
	linkOccupied                  // a real file or directory
)

// linkResult is what one reconciliation did, for the startup log.
type linkResult struct {
	created  int
	repaired int
	removed  int
}

func (r linkResult) String() string {
	return fmt.Sprintf("%d created, %d repaired, %d removed", r.created, r.repaired, r.removed)
}

// ensureLinks reconciles the served tree against linkSet. It is idempotent, so it runs on
// every start and on every change to a link source.
func ensureLinks() (linkResult, error) {
	var res linkResult
	if err := os.MkdirAll(staticRoot, 0o755); err != nil {
		return res, fmt.Errorf("links: mkdir %s: %w", staticRoot, err)
	}
	want, err := declaredLinks()
	if err != nil {
		return res, err
	}

	for dst, src := range want {
		state, target := linkStateOf(dst, src)
		switch state {
		case linkOK:
		case linkAbsent:
			if err := os.Symlink(src, dst); err != nil {
				return res, fmt.Errorf("links: symlink %s -> %s: %w", dst, src, err)
			}
			res.created++
		case linkStale:
			if err := os.Remove(dst); err != nil {
				return res, fmt.Errorf("links: unlink %s: %w", dst, err)
			}
			if err := os.Symlink(src, dst); err != nil {
				return res, fmt.Errorf("links: symlink %s -> %s: %w", dst, src, err)
			}
			log.Printf("links: %s pointed at %s, repointed to %s", dst, target, src)
			res.repaired++
		case linkOccupied:
			return res, fmt.Errorf("links: %s is a real file, so the declared link to %s cannot be made", dst, src)
		}
	}

	res.removed, err = pruneLinks(want)
	if err != nil {
		return res, err
	}
	return res, nil
}

// declaredLinks is the set the served tree must have, as destination -> source, both
// absolute.
func declaredLinks() (map[string]string, error) {
	want := make(map[string]string)
	for _, d := range linkSet {
		src := filepath.Join(homeRoot, d.from)
		if !d.member {
			// The link has to exist before the first upload, or the bytes land where no
			// URL reaches them.
			if err := os.MkdirAll(src, 0o755); err != nil {
				return nil, fmt.Errorf("links: mkdir %s: %w", src, err)
			}
			want[filepath.Join(staticRoot, d.to)] = src
			continue
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue // nothing at the source, nothing to link
			}
			return nil, fmt.Errorf("links: read %s: %w", src, err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue // .DS_Store and other OS artefacts are not site content
			}
			want[filepath.Join(staticRoot, d.to, e.Name())] = filepath.Join(src, e.Name())
		}
	}
	return want, nil
}

// linkStateOf classifies dst, and reports where an existing link points for the log.
func linkStateOf(dst, src string) (linkState, string) {
	fi, err := os.Lstat(dst)
	if err != nil {
		return linkAbsent, ""
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return linkOccupied, ""
	}
	target, err := os.Readlink(dst)
	if err != nil {
		return linkStale, ""
	}
	if target == src {
		return linkOK, target
	}
	return linkStale, target
}

// pruneLinks removes the links this engine owns that the declaration no longer wants. Only
// symlinks are considered, and only ones pointing into a declared source, so generated
// output and anything the site placed in the served tree itself is left alone.
func pruneLinks(want map[string]string) (int, error) {
	entries, err := os.ReadDir(staticRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("links: read %s: %w", staticRoot, err)
	}
	removed := 0
	for _, e := range entries {
		if e.Type()&os.ModeSymlink == 0 {
			continue
		}
		dst := filepath.Join(staticRoot, e.Name())
		if _, ok := want[dst]; ok {
			continue
		}
		target, err := os.Readlink(dst)
		if err != nil || !linkTargetIsOurs(target) {
			continue
		}
		if err := os.Remove(dst); err != nil {
			return removed, fmt.Errorf("links: unlink %s: %w", dst, err)
		}
		removed++
	}
	return removed, nil
}

// linkTargetIsOurs reports whether a link target belongs to a declared source.
func linkTargetIsOurs(target string) bool {
	for _, d := range linkSet {
		src := filepath.Join(homeRoot, d.from)
		if target == src || strings.HasPrefix(target, src+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// runEnsureLinks reconciles the links and reports what changed. A failure here leaves the
// served tree missing content, so it is logged rather than swallowed.
func runEnsureLinks(reason string) {
	res, err := ensureLinks()
	if err != nil {
		log.Printf("[links warn] %s: %v", reason, err)
		return
	}
	if res.created+res.repaired+res.removed > 0 {
		log.Printf("links: %s (%s)", res, reason)
	}
}
