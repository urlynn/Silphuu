package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
)

// TriggerStaticSync rsyncs build output to the edge node, then notifies it to hot-reload.
func TriggerStaticSync() {
	edgeUserHost := os.Getenv("EDGE_RSYNC_DEST_USERHOST") // e.g. root@example.com
	edgePort := os.Getenv("EDGE_SSH_PORT")                // e.g. 50022
	edgeDestDir := os.Getenv("EDGE_RSYNC_DEST_DIR")       // e.g. /srv/www/blog/

	// Missing required vars: return silently (no hardcoded fallbacks).
	if edgeUserHost == "" || edgePort == "" || edgeDestDir == "" {
		return
	}

	go func() {
		// Project locally, then ship that one tree whole.
		//
		// Doing it in two steps is what makes --delete safe here: the projection owns
		// every path in its output, so the destination has exactly one origin per file.
		// Syncing several sources straight into one destination (the previous shape)
		// left the edge unable to tell a file of ours from a stale one, and it also
		// shipped state/ into the served root.
		if err := projectStaticTree(RootPublish); err != nil {
			log.Printf("[Sync] 静态投影失败, 跳过同步: %v", err)
			return
		}
		log.Println("[Sync] 开始执行 rsync 增量同步...")

		cmd := exec.Command("rsync", "-avz", "--delete",
			"-e", fmt.Sprintf("ssh -p %s", edgePort),
			RootPublish+"/",
			fmt.Sprintf("%s:%s", edgeUserHost, edgeDestDir), // assembled purely from env vars; never hardcode
		)

		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out

		if err := cmd.Run(); err != nil {
			log.Printf("[Sync] rsync 同步失败: %v\n输出: %s", err, out.String())
			return
		}

		log.Println("[Sync] rsync 同步完成，准备通知热重载...")
		notifyEdgeReload()
	}()
}

// notifyEdgeReload sends a signed HTTP reload signal to the edge node.
func notifyEdgeReload() {
	edgeAPI := os.Getenv("EDGE_API_URL") // e.g. https://example.com
	secret := os.Getenv("SYNC_SECRET")
	if edgeAPI == "" || secret == "" {
		return
	}

	req, _ := http.NewRequest("POST", edgeAPI+RouteAdminSystemReload, nil)
	req.Header.Set("Authorization", "Bearer "+secret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		log.Printf("[Sync] 边缘节点热重载通知失败: %v", err)
		return
	}
	log.Println("[Sync] 边缘节点内存热重载成功！")
}

// handleSystemReload handles the reload signal from the primary node (admin-privileged area).
func handleSystemReload(w http.ResponseWriter, r *http.Request) {
	// Auth: compare against SYNC_SECRET.
	secret := os.Getenv("SYNC_SECRET")
	authHeader := r.Header.Get("Authorization")
	if secret == "" || authHeader != "Bearer "+secret {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// 1. Clear the in-process HTML cache.
	invalidateAllCache()

	// 2. Rebuild in-memory indexes.
	rebuildPostIndex()

	log.Println("[Edge] 收到同步信号，内存已重载，缓存已清空")
	writeJSONResponse(w, map[string]string{"status": "Reloaded"})
}
