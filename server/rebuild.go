package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// handleCommitRebuild handles site-wide maintenance rebuild commands: POST /commit/rebuild?target={all|posts|sticker|font|bundle|photo|sync|reclaim}
func handleCommitRebuild(w http.ResponseWriter, r *http.Request) {

	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		target = strings.TrimSpace(r.FormValue("target"))
	}

	var msg string
	switch target {
	case "posts":
		rebuildPostIndex()
		msg = uiText("admin.maintenance.done_posts")

	case "sticker":
		refreshStickers()
		msg = uiText("admin.maintenance.done_sticker")

	case "sync":
		// The face is reconciled by an explicit request, never by a schedule: a run is only
		// useful once the peer is reachable, and one that finds nothing changed moves no bytes.
		if len(syncFacePaths()) == 0 {
			msg = uiText("admin.maintenance.failed_sync") + ": SILPHUU_SYNC_PATHS is not set"
			break
		}
		pushed, err := reconcileFace()
		if err != nil {
			log.Printf("[rebuild warn] reconcileFace error: %v", err)
			msg = uiText("admin.maintenance.failed_sync") + ": " + err.Error()
		} else {
			log.Printf("[blob sync] reconciled %d path(s)", pushed)
			msg = uiText("admin.maintenance.done_sync")
		}

	case "reclaim":
		// The reclamation sweep. It judges the filesystem against the records, so it belongs to
		// an explicit request and to startup, never to a read path.
		rep := reclaimOrphans()
		logReclaimReport(rep)
		if n := rep.reclaimed(); n > 0 {
			msg = uiText("admin.maintenance.done_reclaim") + ": " + fmt.Sprint(n)
		} else {
			msg = uiText("admin.maintenance.done_reclaim") + ": 0"
		}
		log.Printf("[reclaim] %s", reclaimSummary(rep))

	case "photo":
		// Album record cleanup. The judgement is about the filesystem, so it belongs to the
		// node holding static/photo and to an explicit request, never to a read path.
		dropped, err := prunePhotoRecords()
		if err != nil {
			log.Printf("[rebuild warn] prunePhotoRecords error: %v", err)
			msg = uiText("admin.maintenance.failed_photo") + ": " + err.Error()
		} else {
			log.Printf("photo: pruned %d record(s) with no file", dropped)
			msg = uiText("admin.maintenance.done_photo")
		}

	case "font":
		RunIncrementalFontSubset(true)
		msg = uiText("admin.maintenance.done_font")

	case "bundle":
		if err := concatAllBundles(); err != nil {
			log.Printf("[rebuild warn] concatAllBundles error: %v", err)
			msg = uiText("admin.maintenance.failed_bundle") + ": " + err.Error()
		} else {
			msg = uiText("admin.maintenance.done_bundle")
		}

	case "events":
		// The log is also the sync channel, so this is the one maintenance action whose
		// judgement is about what the peer still needs. It is safe to run at any time: a
		// record the peer has not confirmed is kept.
		if err := compactEventLog(); err != nil {
			log.Printf("[rebuild warn] compactEventLog error: %v", err)
			msg = uiText("admin.maintenance.failed_events") + ": " + err.Error()
		} else {
			msg = uiText("admin.maintenance.done_events")
		}

	case "all":
		rebuildPostIndex()
		if err := concatAllBundles(); err != nil {
			log.Printf("[rebuild warn] concatAllBundles error: %v", err)
		}
		refreshStickers()
		RunIncrementalFontSubset(true)
		generateAllBlurThumbs()
		msg = uiText("admin.maintenance.done_all")

	default:
		http.Error(w, "Invalid target (options: all, posts, sticker, font, bundle, photo, sync, reclaim, events)", http.StatusBadRequest)
		return
	}

	adminRespond(w, r, msg)
}

func writeJSONResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(data)
}
