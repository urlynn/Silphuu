package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

var (
	AssetPrefix  = "/assets"
	StaticPrefix = "/static"
	// assetManifest maps an assets-relative path to its versioned URL. A rebuild replaces the
	// whole table instead of mutating it, so readers take the pointer without a lock and never
	// observe a half-updated manifest.
	assetManifest atomic.Pointer[map[string]string]
)

func init() {
	if prefix := os.Getenv("ASSET_PREFIX"); prefix != "" {
		AssetPrefix = prefix
	}
	if prefix := os.Getenv("STATIC_PREFIX"); prefix != "" {
		StaticPrefix = prefix
	}
}

// initAssetManifest scans the assets directory and publishes the manifest table.
// This is called once at startup in main().
func initAssetManifest() error {
	manifest, err := buildAssetManifest()
	if err != nil {
		return err
	}
	assetManifest.Store(&manifest)
	return nil
}

// refreshAssetManifest republishes the table after a rebuild rewrote files under assets/.
// A bundle's bytes changed, so the token the pages carry for it has to move with them;
// without this every page keeps naming the generation the rebuild just replaced.
func refreshAssetManifest() {
	if err := initAssetManifest(); err != nil {
		log.Printf("asset manifest: %v", err)
	}
}

// buildAssetManifest walks the served assets tree and returns logical path -> versioned URL.
func buildAssetManifest() (map[string]string, error) {
	assetsDir := RootAssets
	manifest := make(map[string]string)
	err := filepath.Walk(assetsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip directories and the names the engine derives rather than serves: a .br is the
		// pre-compressed copy of the file beside it, and a dot-prefixed name is a write in
		// progress. Giving either a URL would serve compressed bytes under a name that
		// promises the uncompressed type.
		if info.IsDir() || isDerivedServedName(info.Name()) {
			return nil
		}

		// Calculate logical path (e.g., "css/style.css")
		logicalPath, err := filepath.Rel(assetsDir, path)
		if err != nil {
			return err
		}

		// Use forward slashes for URLs
		logicalPath = filepath.ToSlash(logicalPath)

		// Compute SHA256 hash
		hashStr, err := computeFileHash(path)
		if err != nil {
			return fmt.Errorf("failed to hash %s: %w", path, err)
		}

		// Store in manifest: /assets/css/style.css?v=hash
		manifest[logicalPath] = fmt.Sprintf("%s/%s?v=%s", AssetPrefix, logicalPath, hashStr)
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to scan assets: %w", err)
	}
	return manifest, nil
}

// fileContentHash returns the sha256 of the file's contents as hex.
//
// The render-input fingerprint wants the whole digest; asset() only needs a short
// token. Both come from here so a file is never hashed two different ways.
func fileContentHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// computeFileHash returns the first 8 hex characters of the content hash — enough for
// cache-busting, not a security use.
func computeFileHash(path string) (string, error) {
	sum, err := fileContentHash(path)
	if err != nil {
		return "", err
	}
	return sum[:8], nil
}

// asset is the template helper function.
func asset(logicalPath string) string {
	// If in DEV_MODE, calculate hash on the fly for hot reloading
	// without writing to the map (which keeps the map read-only and thread-safe)
	if os.Getenv("DEV_MODE") == "1" {
		path := filepath.Join(RootAssets, logicalPath)
		if hashStr, err := computeFileHash(path); err == nil {
			return fmt.Sprintf("%s/%s?v=%s", AssetPrefix, logicalPath, hashStr)
		}
	}

	// In production (or if the on-the-fly hash fails in dev), use the published table
	if m := assetManifest.Load(); m != nil {
		if url, ok := (*m)[logicalPath]; ok {
			return url
		}
	}

	// Not in the manifest. A file added to the served tree after startup is a legitimate
	// override and simply has no hash to offer. A name that exists nowhere is a typo, and an
	// unhashed URL would hide it — that one is worth logging.
	if !assetExistsAnywhere(logicalPath) {
		log.Printf("asset: %q is in no manifest and not in %s", logicalPath, RootAssets)
	}
	return fmt.Sprintf("%s/%s", AssetPrefix, logicalPath)
}

// versionedAssetURL appends the content token to a URL that names a file under the assets
// prefix. Anything else is returned unchanged: a URL carrying a query already has a token,
// one outside the prefix belongs to another tree, and one whose file is missing stays bare
// so the 404 is visible rather than hidden behind a token that promises content.
func versionedAssetURL(u string) string {
	if strings.ContainsAny(u, "?#") {
		return u
	}
	rel, ok := strings.CutPrefix(u, AssetPrefix+"/")
	if !ok {
		return u
	}
	hashStr, err := computeFileHash(filepath.Join(RootAssets, rel))
	if err != nil {
		log.Printf("asset: %s names no file under %s", u, RootAssets)
		return u
	}
	return u + "?v=" + hashStr
}

// assetExistsAnywhere reports whether an assets/-relative path exists in the served tree.
func assetExistsAnywhere(logicalPath string) bool {
	_, err := os.Stat(filepath.Join(RootAssets, logicalPath))
	return err == nil
}

// staticURL names a file under the site's own media, which answers at /static/ — the served
// tree reaches the deployment root's static/ through a link. It carries no version token:
// /static/ answers at a fixed URL, so a replaced file is reached by purging it.
func staticURL(relPath string) string {
	return StaticPrefix + "/" + relPath
}
