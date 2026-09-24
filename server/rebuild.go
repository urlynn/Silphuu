package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// handleCommitRebuild handles site-wide maintenance rebuild commands: POST /commit/rebuild?target={all|posts|sticker|font|bundle|sync}
func handleCommitRebuild(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		target = strings.TrimSpace(r.FormValue("target"))
	}

	var msg string
	switch target {
	case "posts":
		rebuildPostIndex()
		msg = "文章与搜索内存索引已重新扫描构建"

	case "sticker":
		refreshStickers()
		TriggerStaticSync()
		msg = "表情包缓存与缩略图已刷新"

	case "font":
		RunIncrementalFontSubset(true)
		TriggerStaticSync()
		msg = "字体子集化已全量重构"

	case "bundle":
		if err := concatAllBundles(); err != nil {
			log.Printf("[rebuild warn] concatAllBundles error: %v", err)
			msg = "静态 Bundle 打包失败: " + err.Error()
		} else {
			TriggerStaticSync()
			msg = "全站静态 Bundle 资源已重新打包并净化"
		}

	case "sync":
		TriggerStaticSync()
		msg = "已向边缘节点触发静态热同步信号"

	case "all":
		rebuildPostIndex()
		if err := concatAllBundles(); err != nil {
			log.Printf("[rebuild warn] concatAllBundles error: %v", err)
		}
		refreshStickers()
		RunIncrementalFontSubset(true)
		generateAllBlurThumbs()
		TriggerStaticSync()
		msg = "全站文章索引、静态资源与字体子集已全量重建"

	default:
		http.Error(w, "Invalid target (options: all, posts, sticker, font, bundle, sync)", http.StatusBadRequest)
		return
	}

	adminRespond(w, r, msg)
}

func writeJSONResponse(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(data)
}
