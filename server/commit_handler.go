package main

import (
	"net/http"
	"strings"
)

// handleCommitGateway is the admin-only write gateway for the whole site.
// It strictly requires admin permission; all write/modify operations converge here.
func handleCommitGateway(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	category := r.PathValue("category")
	action := r.PathValue("action")
	if action == "" {
		action = r.FormValue("action")
	}

	contentType := r.Header.Get("Content-Type")
	isMultipart := strings.HasPrefix(contentType, "multipart/form-data")

	// 1. Media upload commits (post/photo/comment)
	//
	// Backgrounds are excluded on purpose. A background upload has to append a
	// BgItem (device / tone / offset) to config/background.json; the pipeline only
	// returns a URL, so routing it here both drops the image from the list and —
	// because the form posts a multi-file "files" field while the pipeline reads a
	// single "file" — answers 500 "未找到上传文件字段 'file'" before anything is
	// written. handleAdminBgUpload (below) handles the whole job.
	if preset, isMedia := CommitPresets[category]; isMedia && isMultipart && category != "background" {
		res, err := ProcessMediaPipeline(r, preset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Register the file reference (in-memory refcount, used for orphan cleanup)
		registerFileRef(res.URL)

		if category == "photo" {
			photos := loadPhotos()
			photos = append(photos, Photo{ID: res.ID, Color: "", Label: PhotoLabel{}, Order: len(photos)})
			savePhotos(photos)
			invalidateCache("/about")
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
	case "refresh-stickers":
		handleRefreshStickers(w, r)
	case "rebuild":
		handleCommitRebuild(w, r)
	default:
		http.Error(w, "Unknown commit category: "+category, http.StatusBadRequest)
	}
}
