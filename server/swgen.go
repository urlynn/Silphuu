package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Service Worker cache version.
//
// CACHE_VERSION is rendered into render/sw.js (placeholder @GEN@ in src/sw.js). It has to move
// when the worker's own inputs move and stay put when they do not, and those inputs are exactly
// what materializeServiceWorker hands it below: the bundle URLs, the site's URLs, the no-cache
// prefixes. So the version is their digest, not the site's mtime.
//
// Following the site's files would make every publish look like a new build. The worker answers
// a navigation from the network first, so a published post reaches the reader with no help from
// the version; bumping it instead makes the browser install a new worker, drop every cache the
// client holds, and — through the controllerchange overlay — interrupt the reader with a
// full-screen refresh for a change that needed none.
func serviceWorkerVersion(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// materializeServiceWorker renders src/sw.js into render/sw.js with every placeholder
// resolved.
//
// /sw.js is a static file: a projection publishes render/sw.js verbatim and the web server
// hands it out, so nothing may be left for request time to substitute. It is rewritten
// whenever an input moves — the bundles behind PAGE_RESOURCES, the post index and prefetch
// list, and the content generation itself.
func materializeServiceWorker() error {
	tpl, err := os.ReadFile(srcReadPath("sw.js"))
	if err != nil {
		return fmt.Errorf("read sw.js: %w", err)
	}

	urlsJSON, _ := json.Marshal(buildSiteURLs())
	resJSON, _ := json.Marshal(buildPageResources())
	noCachePrefixes, _ := json.Marshal([]string{
		PrefixAdmin,
		PageDashboard,
		PageStatus,
		RouteStats,
		PrefixAPI,
		"/debug",
		PrefixStatic + "/image",
		PrefixStatic + "/uploads",
		PrefixStatic + "/sticker",
	})

	gen := serviceWorkerVersion(urlsJSON, resJSON, noCachePrefixes)
	if os.Getenv("DEV_MODE") == "1" {
		gen = "dev"
	}

	out := strings.ReplaceAll(string(tpl), "@GEN@", gen)
	out = strings.ReplaceAll(out, `"@SITE_URLS@"`, string(urlsJSON))
	out = strings.ReplaceAll(out, `"@PAGE_RESOURCES@"`, string(resJSON))
	out = strings.ReplaceAll(out, `"@NO_CACHE_PREFIXES@"`, string(noCachePrefixes))

	if err := os.MkdirAll(RootRender, 0755); err != nil {
		return err
	}
	return writeServedFile(filepath.Join(RootRender, "sw.js"), []byte(out), 0644)
}
