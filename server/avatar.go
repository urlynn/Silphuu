package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
)

// The avatar is resolved at render time, and the choice is svg > jxl > avif. A <picture>
// with a <source> per format would be wrong: the HTML spec picks a URL once per <img>, so
// a <source> pointing at a missing file yields a broken image instead of falling through
// (whatwg/html#8916). The URL must carry a content hash (/assets/ is served immutable for
// a year) and asset() cannot provide it — its manifest only covers the engine's assets/ tree.

// avatarCandidates is the avatar's own format preference, in descending order. It is
// deliberately not a site-wide policy: the avatar is the only image resolved this way.
var avatarCandidates = []string{".svg", ".jxl", ".avif"}

// avatarUnsupported are the legacy formats this project deliberately does not serve.
// Their presence is reported rather than ignored — a file that looks installed and never
// appears is the most confusing possible outcome.
var avatarUnsupported = []string{".png", ".jpg", ".jpeg", ".webp", ".gif"}

// defaultAvatarBase names the file every avatar slot falls back to. It sits beside
// avatar.* under assets/image/, so a site that ships no avatar of its own still shows one.
const defaultAvatarBase = "default-avatar"

// avatarOption is one avatar file found on disk.
type avatarOption struct {
	path string
	ext  string
}

// avatarOptions returns the files backing one avatar slot, in preference order. base is the
// file name without its extension: "avatar" for the site owner, "default-avatar" for the
// fallback.
func avatarOptions(base string) []avatarOption {
	var found []avatarOption
	for _, ext := range avatarCandidates {
		p := filepath.Join(RootAssets, "image", base+ext)
		if _, err := os.Stat(p); err == nil {
			found = append(found, avatarOption{path: p, ext: ext})
		}
	}
	return found
}

// avatarFileURL returns the hash-addressed URL of one avatar file, or "" when it cannot be
// hashed. The hash is what makes a replaced image replaceable at all: /assets/ is served
// immutable.
func avatarFileURL(opt avatarOption) string {
	h, err := computeFileHash(opt.path)
	if err != nil {
		log.Printf("avatar: cannot hash %s: %v", opt.path, err)
		return ""
	}
	return AssetPrefix + "/image/" + filepath.Base(opt.path) + "?v=" + h
}

// defaultAvatarURL returns the URL of the site's default avatar, or "" when it ships none.
// This is the single fallback target: the owner's avatar and the error pages both resolve
// to it, so one file stands in wherever an avatar is missing.
func defaultAvatarURL() string {
	for _, opt := range avatarOptions(defaultAvatarBase) {
		if u := avatarFileURL(opt); u != "" {
			return u
		}
	}
	return ""
}

// resolveAvatarURL returns the site owner's avatar URL, or the default avatar when the site
// ships none. Resolution runs on every render, so the builder decides and the rendered page
// ends up carrying one concrete, hash-addressed URL.
//
// Deliberately not a package-level init(): init() runs before flag.Parse(), so under -dir
// it would resolve against the wrong root.
func resolveAvatarURL() string {
	for _, opt := range avatarOptions("avatar") {
		if u := avatarFileURL(opt); u != "" {
			return u
		}
	}
	return defaultAvatarURL()
}

// errorImageURL returns the art for one error page: the site's own file for that code when
// it has one, otherwise the default avatar. The generated error pages draw the avatar, so
// a site needs no per-code art of its own.
func errorImageURL(code string) string {
	if _, err := os.Stat(filepath.Join(RootAssets, "image", "error", code+".avif")); err == nil {
		return AssetPrefix + "/image/error/" + code + ".avif"
	}
	return defaultAvatarURL()
}

// logAvatarResolution reports which avatar will be served and complains about formats that
// are present but unsupported. Called once at startup: the resolution itself happens per
// render, but this is the only place a human will look.
func logAvatarResolution() {
	opts := avatarOptions("avatar")
	fallback := avatarOptions(defaultAvatarBase)
	switch {
	case len(opts) > 0:
		msg := "avatar: serving " + opts[0].path
		if len(opts) > 1 {
			exts := make([]string, 0, len(opts)-1)
			for _, o := range opts[1:] {
				exts = append(exts, o.ext)
			}
			msg += " (also present, skipped: " + strings.Join(exts, ", ") + ")"
		}
		log.Print(msg)
	case len(fallback) > 0:
		log.Printf("avatar: no image/avatar{%s}; pages fall back to %s",
			strings.Join(avatarCandidates, ","), fallback[0].path)
	default:
		log.Printf("avatar: no image/avatar{%s} and no image/%s{%s}; pages render without an avatar",
			strings.Join(avatarCandidates, ","), defaultAvatarBase, strings.Join(avatarCandidates, ","))
	}

	for _, base := range []string{"avatar", defaultAvatarBase} {
		for _, ext := range avatarUnsupported {
			p := filepath.Join(RootAssets, "image", base+ext)
			if _, err := os.Stat(p); err == nil {
				log.Printf("avatar: %s is not served; only %s are (convert or remove it)",
					p, strings.Join(avatarCandidates, "/"))
			}
		}
	}
}
