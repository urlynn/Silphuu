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
)

var (
	AssetPrefix  = "/assets"
	StaticPrefix = "/static"
	// assetManifest is populated once at startup and is strictly read-only thereafter,
	// providing thread-safe concurrent reads without any locks.
	assetManifest = make(map[string]string)
)

func init() {
	if prefix := os.Getenv("ASSET_PREFIX"); prefix != "" {
		AssetPrefix = prefix
	}
	if prefix := os.Getenv("STATIC_PREFIX"); prefix != "" {
		StaticPrefix = prefix
	}
}

// initAssetManifest scans the assets directory and builds the manifest map.
// This is called once at startup in main().
func initAssetManifest() error {
	assetsDir := RootAssets
	err := filepath.Walk(assetsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip directories and hidden files
		if info.IsDir() || strings.HasPrefix(info.Name(), ".") {
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
		assetManifest[logicalPath] = fmt.Sprintf("%s/%s?v=%s", AssetPrefix, logicalPath, hashStr)
		return nil
	})

	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to scan assets: %w", err)
	}
	return nil
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

	// In production (or if on-the-fly hash fails in dev), use the read-only manifest
	if url, ok := assetManifest[logicalPath]; ok {
		return url
	}

	// Not in the manifest. A file added to the served tree after startup is a legitimate
	// override and simply has no hash to offer. A name that exists nowhere is a typo, and an
	// unhashed URL would hide it — that one is worth logging.
	if !assetExistsAnywhere(logicalPath) {
		log.Printf("asset: %q is in no manifest and not in %s", logicalPath, RootAssets)
	}
	return fmt.Sprintf("%s/%s", AssetPrefix, logicalPath)
}

// assetExistsAnywhere reports whether an assets/-relative path exists in the served tree.
func assetExistsAnywhere(logicalPath string) bool {
	_, err := os.Stat(filepath.Join(RootAssets, logicalPath))
	return err == nil
}

// staticURL is a helper for static files (if any are still using /static)
func staticURL(relPath string) string {
	return fmt.Sprintf("%s/%s", StaticPrefix, relPath)
}
