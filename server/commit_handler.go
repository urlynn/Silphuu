package main

import (
	"log"
	"net/http"
	"strings"
)

// handlePhotoAnnounce records an album entry before its bytes exist.
//
// The caller announces the id it is about to upload, so every node can render the card and
// publish the media URLs immediately; the upload lands on those URLs later. Announcing is what
// lets a node that will never hold the bytes show the photo without waiting for them.
//
// The id comes from the caller because two requests reference the same one: this one and the
// upload. See ProcessMediaPipeline.
//
// It arrives in the query, the same way the upload's does. Reading it out of the request body
// instead means parsing the multipart form here, and ParseForm on its own never fills it for that
// content type — the endpoint then answers 400 to every caller.
func handlePhotoAnnounce(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		http.Error(w, "announce requires an id in the query", http.StatusBadRequest)
		return
	}
	if err := CommitEvent(EvPhotoAdded, Photo{ID: id}); err != nil {
		log.Printf("commit: %s commit failed: %v", EvPhotoAdded, err)
		http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
		return
	}
	writeJSONResponse(w, map[string]string{"id": id})
}

// handleCommitGateway is the write gateway for the whole site: every write or modify
// operation converges here. It runs no permission check of its own — the gateway fronting
// /admin/ decides who reaches it.
func handleCommitGateway(w http.ResponseWriter, r *http.Request) {

	category := r.PathValue("category")
	action := r.PathValue("action")
	if action == "" {
		action = r.FormValue("action")
	}

	contentType := r.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(contentType, "multipart/form-data")

	// An upload action without a multipart body is a client error, never the other operation
	// its category performs: /admin/commit/post/upload must not fall through to the article
	// save at /admin/commit/post.
	if _, isMedia := CommitPresets[category]; isMedia && action == "upload" && !isMultipart {
		http.Error(w, "upload requires a multipart/form-data body", http.StatusBadRequest)
		return
	}

	// 1. Media upload commits (post/photo/comment)
	//
	// Backgrounds are excluded on purpose. A background upload has to append a
	// BgItem (device / tone / offset) to config/background.json; the pipeline only
	// returns a URL, so routing it here both drops the image from the list and —
	// because the form posts a multi-file "files" field while the pipeline reads a
	// single "file" — answers 500 "未找到上传文件字段 'file'" before anything is
	// written. handleAdminBgUpload (below) handles the whole job.
	//
	// The action is part of the test too. A media category carries an upload only for the
	// bare category the older forms post to, or for the "upload" path that names the
	// operation; any other action under the same category sends a field list rather than a
	// file and has to reach its own handler.
	isUpload := action == "" || action == "upload"
	if preset, isMedia := CommitPresets[category]; isMedia && isMultipart && isUpload && category != "background" {
		// A caller that announced the record first passes the id it announced, so the bytes
		// land on the URL already published. An empty id means one request does both.
		//
		// It arrives as a query parameter rather than a form field on purpose: reading a form
		// field would parse the body here, before the pipeline has bounded it.
		id := strings.TrimSpace(r.URL.Query().Get("id"))

		// The pipeline writes the media file straight into the served tree, without going
		// through writeServedFile, so it takes the lease itself: a URL it publishes must not be
		// fingerprinted while its bytes are still being written.
		beginServedWrite()
		res, err := ProcessMediaPipeline(r, preset, id)
		endServedWrite()
		if err != nil {
			// The pipeline's errors name server internals (tool paths, decode failures), so
			// they go to the log and the page gets copy. This endpoint is reachable from the
			// sticker upload form on a public page, and the caller writes the body straight
			// into the modal.
			log.Printf("commit: media pipeline failed: %v", err)
			http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
			return
		}

		// Register the file reference (in-memory refcount, used for orphan cleanup)
		registerFileRef(res.URL)

		if category == "photo" {
			// The record is metadata: it goes through the log so every node that renders
			// /about learns of it, not only the node that took the upload. Announcing it
			// separately is optional, and this is idempotent with that path.
			if err := CommitEvent(EvPhotoAdded, Photo{ID: res.ID}); err != nil {
				log.Printf("commit: %s commit failed: %v", EvPhotoAdded, err)
				http.Error(w, uiText("upload.server"), http.StatusInternalServerError)
				return
			}
			// The card was rendered while the bytes were still being written, so the page is
			// invalidated again now that they are there. The URLs it embeds do not change.
			_ = commitBlobReady(PageAbout, res.FacePaths)
			// The wall reloads itself as soon as this answers, so the album page has to be the new
			// one by then: the events above only queued it for the edge.
			warmPageNow(PageAbout)
		}

		writeJSONResponse(w, res)
		return
	}

	// 2. Structured-data commits (posts/background/album/config/fonts/sponsors)
	switch category {
	case "post":
		switch action {
		case "delete":
			handleAdminPostDelete(w, r)
		case "pin", "toggle-pin":
			handleAdminPostTogglePin(w, r)
		default:
			handleAdminSavePost(w, r)
		}
	case "background":
		switch action {
		case "add":
			handleAdminBgAdd(w, r)
		case "delete":
			handleAdminBgDelete(w, r)
		case "toggle-mode":
			handleAdminBgToggleMode(w, r)
		case "set-offset":
			handleAdminBgSetOffset(w, r)
		case "set-avatar":
			handleAdminBgSetAvatar(w, r)
		case "set-tone":
			handleAdminBgSetTone(w, r)
		case "upload":
			handleAdminBgUpload(w, r)
		default:
			if r.FormValue("url") != "" {
				handleAdminBgAdd(w, r)
			} else {
				http.Error(w, "Unknown background action", http.StatusBadRequest)
			}
		}
	case "photo":
		switch action {
		case "add":
			handlePhotoAdd(w, r)
		case "announce":
			handlePhotoAnnounce(w, r)
		case "upload":
			handlePhotoUpload(w, r)
		case "delete":
			handlePhotoDelete(w, r)
		case "batch-delete":
			handlePhotoBatchDelete(w, r)
		case "reorder":
			handlePhotoReorder(w, r)
		default:
			handlePhotoAdd(w, r)
		}
	case "config":
		handleAdminConfigSave(w, r)
	case "hero":
		switch action {
		case "avatar":
			handleAdminHeroAvatar(w, r)
		default:
			handleAdminHeroSave(w, r)
		}
	case "sponsor":
		switch action {
		case "delete":
			handleAdminSponsorDelete(w, r)
		case "add":
			handleAdminSponsorAdd(w, r)
		case "edit":
			handleAdminSponsorEdit(w, r)
		default:
			handleAdminSponsorAdd(w, r)
		}
	case "friend":
		switch action {
		case "delete":
			handleAdminFriendDelete(w, r)
		case "edit":
			handleAdminFriendEdit(w, r)
		case "approve":
			handleAdminFriendApprove(w, r)
		case "add":
			handleFriendApply(w, r)
		default:
			handleFriendApply(w, r)
		}
	case "font":
		switch action {
		case "save":
			handleAdminFontPresetSave(w, r)
		case "activate":
			handleAdminFontPresetActivate(w, r)
		case "delete":
			handleAdminFontPresetDelete(w, r)
		case "create":
			handleAdminFontPresetCreate(w, r)
		case "rebuild":
			handleAdminFontRebuild(w, r)
		default:
			handleAdminFontPresetSave(w, r)
		}
	case "media", "upload-image":
		handleAdminUploadImage(w, r)
	case "notify":
		// The one category here that records no event. A route is a property of this server
		// — the channels it can send on are its own — so it must not travel to a peer the way
		// every other write in this switch does. See notify_routes.go.
		//
		// The panel's save posts no action, so the default is the save. A test changes
		// nothing and is therefore not a write at all.
		switch action {
		case "test":
			handleAdminNotifyTest(w, r)
		default:
			handleAdminNotifySave(w, r)
		}
	case "refresh-stickers":
		handleRefreshStickers(w, r)
	case "rebuild":
		handleCommitRebuild(w, r)
	case "purge":
		// The deliberate override of the automatic invalidation path: the administrator
		// names what the edge should drop, and it goes whether or not the bytes moved.
		// The two named actions are the pause switch; a bare purge is the purge itself.
		switch action {
		case "disable":
			handleCommitPurgeSwitch(w, r, true)
		case "enable":
			handleCommitPurgeSwitch(w, r, false)
		default:
			handleCommitPurge(w, r)
		}
	default:
		http.Error(w, "Unknown commit category: "+category, http.StatusBadRequest)
	}
}
