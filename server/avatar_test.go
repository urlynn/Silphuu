package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withIsolatedRoots points the engine and deployment roots at separate temporary
// directories, so a test never reads the real trees. engineRoot is not settable through
// InitRoots, so it is assigned directly and restored on cleanup.
//
// The served assets tree belongs to the deployment (RootAssets = <site>/assets), so a test
// places files there through putAsset(t, site, ...).
func withIsolatedRoots(t *testing.T) (site, engine string) {
	t.Helper()
	site, engine = t.TempDir(), t.TempDir()
	oldEngine := engineRoot
	engineRoot = engine
	InitRoots(site)
	t.Cleanup(func() {
		engineRoot = oldEngine
		InitRoots("")
	})
	return site, engine
}

// putAsset writes a file under <root>/assets/<rel>.
func putAsset(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, "assets", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestResolveAvatarURLFormatPreference(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  string // expected extension; "" means no avatar at all
	}{
		{"svg only", []string{"avatar.svg"}, ".svg"},
		{"jxl only", []string{"avatar.jxl"}, ".jxl"},
		{"avif only", []string{"avatar.avif"}, ".avif"},
		{"all three prefers svg", []string{"avatar.avif", "avatar.jxl", "avatar.svg"}, ".svg"},
		{"without svg, jxl outranks avif", []string{"avatar.avif", "avatar.jxl"}, ".jxl"},
		{"legacy formats are not served", []string{"avatar.png", "avatar.webp", "avatar.jpg"}, ""},
		{"nothing on disk", nil, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, _ := withIsolatedRoots(t)
			for _, f := range tc.files {
				putAsset(t, site, "image/"+f, "engine-"+f)
			}

			got := resolveAvatarURL()
			if tc.want == "" {
				if got != "" {
					t.Fatalf("resolveAvatarURL() = %q, want no avatar", got)
				}
				return
			}
			wantPrefix := AssetPrefix + "/image/avatar" + tc.want + "?v="
			if !strings.HasPrefix(got, wantPrefix) {
				t.Fatalf("resolveAvatarURL() = %q, want prefix %q", got, wantPrefix)
			}
		})
	}
}

// The URL is content-addressed. /assets/ is served with a one-year immutable
// Cache-Control, so a URL that does not change with the image can never be replaced.
func TestResolveAvatarURLIsContentAddressed(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	putAsset(t, site, "image/avatar.svg", "first")
	first := resolveAvatarURL()
	if first == "" {
		t.Fatalf("resolveAvatarURL() returned nothing with an avatar on disk")
	}

	putAsset(t, site, "image/avatar.svg", "first")
	if again := resolveAvatarURL(); again != first {
		t.Fatalf("identical content produced a different URL: %q vs %q", again, first)
	}

	putAsset(t, site, "image/avatar.svg", "second")
	if second := resolveAvatarURL(); second == first {
		t.Fatalf("replacing the avatar left the URL unchanged (%q)", second)
	}
}

// The URL must resolve to a file that actually exists — that is the whole point of
// deciding the format at render time instead of shipping a <picture> with a <source> per
// format, where a missing file yields a broken image rather than a fallback.
func TestResolveAvatarURLPointsAtAnExistingFile(t *testing.T) {
	site, _ := withIsolatedRoots(t)
	putAsset(t, site, "image/avatar.svg", "only-one")

	got := resolveAvatarURL()
	urlPath := strings.SplitN(got, "?", 2)[0]
	if !strings.HasPrefix(urlPath, AssetPrefix+"/") {
		t.Fatalf("URL %q is not under %s", urlPath, AssetPrefix)
	}
	rel := strings.TrimPrefix(urlPath, AssetPrefix+"/")
	if _, err := os.Stat(filepath.Join(RootAssets, rel)); err != nil {
		t.Fatalf("%s resolves to a file that does not exist: %v", urlPath, err)
	}
}

// One file stands in wherever an avatar is missing: the owner's slot and the error pages
// both resolve to assets/image/default-avatar.*, so a site needs no per-slot art of its own.
func TestAvatarSlotsFallBackToTheDefaultAvatar(t *testing.T) {
	t.Run("owner slot", func(t *testing.T) {
		site, _ := withIsolatedRoots(t)
		putAsset(t, site, "image/default-avatar.avif", "the-fallback")

		want := defaultAvatarURL()
		if want == "" {
			t.Fatal("defaultAvatarURL() returned nothing with image/default-avatar.avif on disk")
		}
		if got := resolveAvatarURL(); got != want {
			t.Fatalf("resolveAvatarURL() = %q, want the default avatar %q", got, want)
		}
	})

	t.Run("the owner's own avatar outranks the default", func(t *testing.T) {
		site, _ := withIsolatedRoots(t)
		putAsset(t, site, "image/avatar.avif", "mine")
		putAsset(t, site, "image/default-avatar.avif", "the-fallback")

		if got := resolveAvatarURL(); !strings.HasPrefix(got, AssetPrefix+"/image/avatar.avif?v=") {
			t.Fatalf("resolveAvatarURL() = %q, want the site's own avatar", got)
		}
	})

	t.Run("error pages", func(t *testing.T) {
		site, _ := withIsolatedRoots(t)
		putAsset(t, site, "image/default-avatar.avif", "the-fallback")

		if got := errorImageURL("404"); got != defaultAvatarURL() {
			t.Fatalf("errorImageURL(404) = %q, want the default avatar", got)
		}
	})

	t.Run("the site's own error art outranks the default", func(t *testing.T) {
		site, _ := withIsolatedRoots(t)
		putAsset(t, site, "image/default-avatar.avif", "the-fallback")
		putAsset(t, site, "image/error/404.avif", "art")

		if got := errorImageURL("404"); got != AssetPrefix+"/image/error/404.avif" {
			t.Fatalf("errorImageURL(404) = %q, want the site's own art", got)
		}
	})
}

// With neither file there is nothing to fall back to. An empty URL is the honest answer:
// a made-up one would point at a 404 and hide the missing file.
func TestAvatarSlotsAreEmptyWithoutAnyFile(t *testing.T) {
	withIsolatedRoots(t)

	if got := defaultAvatarURL(); got != "" {
		t.Errorf("defaultAvatarURL() = %q, want empty", got)
	}
	if got := resolveAvatarURL(); got != "" {
		t.Errorf("resolveAvatarURL() = %q, want empty", got)
	}
	if got := errorImageURL("404"); got != "" {
		t.Errorf("errorImageURL(404) = %q, want empty", got)
	}
}
