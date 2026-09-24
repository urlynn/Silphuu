package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
)

// PurgeESACache asynchronously calls the aliyun esa PurgeCaches CLI to purge file caches
// on ESA CDN edge nodes.
// urls accepts one or more full URLs (e.g. https://example.com/post/42).
func PurgeESACache(urls ...string) {
	if len(urls) == 0 {
		return
	}

	// Filter + dedupe
	urlMap := make(map[string]bool)
	var cleanURLs []string
	for _, u := range urls {
		if u != "" && !urlMap[u] {
			urlMap[u] = true
			cleanURLs = append(cleanURLs, u)
		}
	}
	if len(cleanURLs) == 0 {
		return
	}

	siteID := loadAppConfig().ESASiteID
	if envSiteID := os.Getenv("ESA_SITE_ID"); envSiteID != "" {
		if id, err := strconv.ParseInt(envSiteID, 10, 64); err == nil && id > 0 {
			siteID = id
		}
	}
	if siteID == 0 {
		log.Printf("[ESA Purge] no ESA site configured (esa_site_id / ESA_SITE_ID), skipping purge: %v", cleanURLs)
		return
	}

	// In local dev mode no CLI call is needed; just log
	if os.Getenv("DEV_MODE") == "1" {
		log.Printf("[ESA Purge DEV] SiteID: %d, URLs: %v", siteID, cleanURLs)
		return
	}

	go func() {
		contentJSON, err := json.Marshal(map[string][]string{
			"Files": cleanURLs,
		})
		if err != nil {
			log.Printf("[ESA Purge] JSON marshal err: %v", err)
			return
		}

		cmd := exec.Command("aliyun", "esa", "PurgeCaches",
			"--SiteId", fmt.Sprintf("%d", siteID),
			"--Type", "file",
			"--Content", string(contentJSON),
		)

		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("[ESA Purge Error] %v: %s", err, string(out))
		} else {
			log.Printf("[ESA Purge OK] Site: %d, URLs: %v, Res: %s", siteID, cleanURLs, string(out))
		}
	}()
}
