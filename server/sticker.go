package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sticker is a single sticker.
type Sticker struct {
	AVIFURL     string // display canonical: /static/sticker/{group}/avif/{name}.avif
	JXLURL      string // /static/sticker/{group}/jxl/{name}.jxl (fallback, injected on demand by the probe)
	ThumbURL    string // 96x96 static thumbnail path
	AnimURL     string // 96x96 animated thumbnail path (GIF only)
	Name        string // file name without extension
	DisplayName string // readable name (underscores replaced with spaces)
}

// StickerSet is one sticker pack.
type StickerSet struct {
	Name        string    // directory name (group name), used for data-group matching
	DisplayName string    // display name (config "name"; falls back to Name)
	Stickers    []Sticker // all images in this group
}

// stickerConfig is the sticker group config file structure.
type stickerConfig struct {
	Groups []stickerGroupConfig `json:"groups"`
}

type stickerGroupConfig struct {
	Dir   string `json:"dir"`
	Name  string `json:"name"`
	Cover string `json:"cover,omitempty"`
}

// stickerCache is the global in-memory cache, scanned and loaded at startup.
var stickerCache []StickerSet

// shortcodeMap shortcode -> StickerRef
var shortcodeMap map[string]StickerRef

// StickerRef is a shortcode reference.
type StickerRef struct {
	URL string
}

// supportedExt lists supported image extensions.
var supportedExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".avif": true,
}

// loadStickerConfig reads config/sticker.json and returns a dir -> group config map.
func loadStickerConfig() (map[string]stickerGroupConfig, []string) {
	configMap := make(map[string]stickerGroupConfig)
	var orderedDirs []string

	data, err := os.ReadFile(FileStickerData)
	if err != nil {
		log.Printf("sticker: no config file (config/sticker.json), using filesystem order: %v", err)
		return configMap, nil
	}

	var cfg stickerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("sticker: config parse error: %v", err)
		return configMap, nil
	}

	for _, g := range cfg.Groups {
		configMap[g.Dir] = g
		orderedDirs = append(orderedDirs, g.Dir)
	}
	return configMap, orderedDirs
}

// stickerPayloadURL returns the URL of one stored sticker file.
//
// Payload URLs are deliberately not hash-addressed: the shortcode pass writes them into
// article HTML, so a changing URL would invalidate every page that mentions the sticker.
// A rebuild keeps the URL and purges it instead — see refreshStickers.
func stickerPayloadURL(group, format, file string) string {
	return PrefixStatic + "/sticker/" + group + "/" + format + "/" + file
}

// stickerIndexPath is where the published sticker index is written.
func stickerIndexPath() string {
	return filepath.Join(RootAssets, "stickers.json")
}

// stickerIndexURL returns the index's URL, hash-addressed so a rebuilt index reaches
// clients without a purge.
func stickerIndexURL() string {
	url := PrefixAssets + "/stickers.json"
	if h, err := computeFileHash(stickerIndexPath()); err == nil {
		return url + "?v=" + h
	}
	return url
}

// eachStickerSource walks the deployment's sticker groups and calls fn for every GIF
// source. A group without a gif/ subfolder is not published, so it is skipped.
func eachStickerSource(fn func(group, base, gifPath string)) {
	dir := DirSticker
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sticker: read dir %s error: %v", dir, err)
		}
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		group := entry.Name()
		// _pending/ holds unreviewed uploads, so keep them out of the picker.
		if group == "_pending" || strings.HasPrefix(group, ".") {
			continue
		}
		gifDir := filepath.Join(dir, group, "gif")
		files, err := os.ReadDir(gifDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || strings.ToLower(filepath.Ext(f.Name())) != ".gif" {
				continue
			}
			base := strings.TrimSuffix(f.Name(), filepath.Ext(f.Name()))
			fn(group, base, filepath.Join(gifDir, f.Name()))
		}
	}
}

// publishStickerIndex writes the sticker index to disk, empty included: it is the
// published truth for the picker, so the URL has to resolve on every site.
func publishStickerIndex(sets []StickerSet) {
	if sets == nil {
		sets = []StickerSet{}
	}
	data, err := json.Marshal(sets)
	if err != nil {
		log.Printf("sticker: marshal index error: %v", err)
		return
	}
	if err := os.WriteFile(stickerIndexPath(), data, 0644); err != nil {
		log.Printf("sticker: write index error: %v", err)
	}
}

// loadStickers scans the deployment's sticker directory and loads only metadata into the
// cache (no thumbnails). The directory is deployment content — the engine ships none — so
// its absence is the normal case, not an error.
func loadStickers() {
	dir := DirSticker
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("sticker: read dir %s error: %v", dir, err)
		}
		stickerCache = nil
		publishStickerIndex(nil)
		return
	}

	// Read config
	configMap, orderedDirs := loadStickerConfig()

	// Collect sticker data for all folders first
	type groupData struct {
		displayName string
		stickers    []Sticker
	}
	allGroups := make(map[string]groupData)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		groupName := entry.Name()
		groupDir := filepath.Join(dir, groupName)
		gifDir := filepath.Join(groupDir, "gif")

		gifEntries, err := os.ReadDir(gifDir)
		if err != nil {
			// Strictly read only the gif/ subfolder (no root-level fallback); a group
			// without a gif/ source is not published
			continue
		}

		var stickers []Sticker
		for _, f := range gifEntries {
			if f.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(f.Name()))
			if ext != ".gif" {
				continue // gif is the only source; dual formats are generated by generateStickerDualFormat
			}
			baseName := strings.TrimSuffix(f.Name(), ext)

			// Publish only when both formats exist; skip otherwise (no gif fallback),
			// prompting the admin to hit refresh to regenerate
			avifPath := filepath.Join(groupDir, "avif", baseName+".avif")
			jxlPath := filepath.Join(groupDir, "jxl", baseName+".jxl")
			if !fileExists(avifPath) {
				log.Printf("sticker: %s/%s 缺 avif（点「刷新表情包缓存」补生成），跳过发布", groupName, baseName)
				continue
			}
			if !fileExists(jxlPath) {
				log.Printf("sticker: %s/%s 缺 jxl（点「刷新表情包缓存」补生成），跳过发布", groupName, baseName)
				continue
			}

			stickers = append(stickers, Sticker{
				AVIFURL:     stickerPayloadURL(groupName, "avif", baseName+".avif"),
				JXLURL:      stickerPayloadURL(groupName, "jxl", baseName+".jxl"),
				ThumbURL:    URLThumbSticker(groupName, baseName, "preview"),
				AnimURL:     animThumbURL(groupName, baseName, ext),
				Name:        baseName,
				DisplayName: strings.ReplaceAll(baseName, "_", " "),
			})
		}

		if len(stickers) == 0 {
			continue
		}

		sort.Slice(stickers, func(i, j int) bool {
			return stickers[i].Name < stickers[j].Name
		})

		// Apply the cover config: the file named by cover is moved to the first position
		if cfg, ok := configMap[groupName]; ok && cfg.Cover != "" {
			cover := strings.ToLower(cfg.Cover)
			for i, st := range stickers {
				if strings.ToLower(st.Name) == cover {
					if i > 0 {
						coverSticker := stickers[i]
						stickers = append(stickers[:i], stickers[i+1:]...)
						stickers = append([]Sticker{coverSticker}, stickers...)
					}
					break
				}
			}
		}

		displayName := groupName
		if cfg, ok := configMap[groupName]; ok && cfg.Name != "" {
			displayName = cfg.Name
		}

		allGroups[groupName] = groupData{
			displayName: displayName,
			stickers:    stickers,
		}
	}

	// Output in config order
	var sets []StickerSet
	seen := make(map[string]bool)

	for _, dirName := range orderedDirs {
		if gd, ok := allGroups[dirName]; ok {
			sets = append(sets, StickerSet{
				Name:        dirName,
				DisplayName: gd.displayName,
				Stickers:    gd.stickers,
			})
			seen[dirName] = true
		}
	}

	// Folders not present in the config are appended alphabetically
	var remaining []string
	for name := range allGroups {
		if !seen[name] {
			remaining = append(remaining, name)
		}
	}
	sort.Strings(remaining)
	for _, name := range remaining {
		gd := allGroups[name]
		sets = append(sets, StickerSet{
			Name:        name,
			DisplayName: gd.displayName,
			Stickers:    gd.stickers,
		})
	}

	stickerCache = sets
	publishStickerIndex(sets)
	log.Printf("sticker: loaded %d sets, %d total stickers", len(sets), func() int {
		n := 0
		for _, s := range sets {
			n += len(s.Stickers)
		}
		return n
	}())

	// Rebuild the shortcode map
	buildShortcodeMap()
}

// buildShortcodeMap builds the shortcode -> URL map from stickerCache.
func buildShortcodeMap() {
	m := make(map[string]StickerRef)
	for _, set := range stickerCache {
		prefix := set.DisplayName
		for _, st := range set.Stickers {
			sc := prefix + "_" + st.Name
			m[sc] = StickerRef{URL: st.AVIFURL}
		}
	}
	shortcodeMap = m
}

// animThumbURL returns the animated thumbnail path; only GIF has one.
func animThumbURL(groupName, baseName, ext string) string {
	if ext == ".gif" {
		return URLThumbSticker(groupName, baseName, "dynamic")
	}
	return ""
}

// needsRegen reports whether the source is newer than the thumbnail (or the thumbnail
// is missing), i.e. whether regeneration is needed.
func needsRegen(srcPath, thumbPath string) bool {
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false // source missing, skip
	}
	thumbInfo, err := os.Stat(thumbPath)
	if err != nil {
		return true // thumbnail missing, needs generating
	}
	return srcInfo.ModTime().After(thumbInfo.ModTime())
}

// generateStickerThumbnails generates missing/stale thumbnails (128x128 preview +
// 96x96 dynamic).
//
// It walks the filesystem rather than stickerCache: loadStickers hashes each thumbnail
// URL, so the files have to exist before the cache is built.
func generateStickerThumbnails() {
	eachStickerSource(func(groupName, baseName, srcPath string) {
		previewDir := DirThumbStickerPreview(groupName)
		dynamicDir := DirThumbStickerDynamic(groupName)
		os.MkdirAll(previewDir, 0755)
		os.MkdirAll(dynamicDir, 0755)

		// 1. Static 128x128 preview (~1.5KB, preview)
		previewPath := filepath.Join(previewDir, baseName+".avif")
		if needsRegen(srcPath, previewPath) {
			tmpPNG, _ := os.CreateTemp("", "thumb-*.png")
			pngPath := tmpPNG.Name()
			tmpPNG.Close()
			defer os.Remove(pngPath)

			args := []string{srcPath + "[0]", "-resize", "128x128", pngPath}
			if strings.HasSuffix(magickCmd, "magick") {
				args = append([]string{"convert"}, args...)
			}
			if out, err := exec.Command(magickCmd, args...).CombinedOutput(); err != nil {
				log.Printf("sticker: magick error for %s: %s", baseName, strings.TrimSpace(string(out)))
			} else if avifencBin != "" {
				exec.Command(avifencBin, "-c", "aom", "-s", "6", "-j", "all", "-q", "42", "-a", "aq-mode=1", pngPath, previewPath).Run()
			}
		}

		// 2. Dynamic 96x96 micro-thumbnail: per-frame durations come from the source's
		// original frame rate (%T centiseconds)
		dynamicPath := filepath.Join(dynamicDir, baseName+".avif")
		if needsRegen(srcPath, dynamicPath) {
			tmpDir, _ := os.MkdirTemp("", "thumb-anim-*")
			framePattern := filepath.Join(tmpDir, "frame_%04d.png")
			ffmpeg := toolPath("ffmpeg")
			if out, err := exec.Command(ffmpeg, "-i", srcPath, "-vsync", "0", "-vf", "scale=96:96:flags=area", framePattern).CombinedOutput(); err != nil {
				log.Printf("sticker: ffmpeg error for %s: %s", baseName, strings.TrimSpace(string(out)))
			} else if avifencBin != "" {
				frameFiles, _ := filepath.Glob(filepath.Join(tmpDir, "frame_*.png"))
				sort.Strings(frameFiles)
				if len(frameFiles) > 0 {
					delaysCS := getFrameDelaysCS(srcPath, len(frameFiles))
					encArgs := []string{"-c", "aom", "-s", "6", "-j", "all", "-q", "50", "-a", "aq-mode=1", "--timescale", "100"}
					for i, f := range frameFiles {
						d := 3 // default 30ms
						if i < len(delaysCS) {
							d = delaysCS[i]
						}
						if d < 1 {
							d = 1
						}
						encArgs = append(encArgs, "--duration", strconv.Itoa(d), f)
					}
					encArgs = append(encArgs, dynamicPath)
					exec.Command(avifencBin, encArgs...).Run()
				}
			}
			os.RemoveAll(tmpDir)
		}
	})
	log.Println("sticker: thumbnails ready")
}

// refreshStickers regenerates every derived sticker file, republishes the index and
// invalidates whatever kept its URL while changing.
//
// Order matters: thumbnails have to exist before loadStickers, which hashes their URLs.
func refreshStickers() {
	rewritten := generateStickerDualFormat() // 1. regenerate stale jxl+avif from the gif/ sources
	generateStickerThumbnails()              // 2. regenerate stale thumbnails (hash-addressed, no purge)
	loadStickers()                           // 3. rescan and republish the index (hash-addressed, no purge)
	purgeCDN(rewritten)                      // 4. invalidate the payload URLs that changed under a fixed URL
	resetRenderCache()                       // 5. every page embeds sticker URLs, so all of them are stale
}

// fileExists reports whether the file exists and is not a directory.
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// getFrameDurationsMS reads the GIF's raw per-frame delays (seconds) via ffprobe and
// converts to milliseconds; raw per-frame values, no averaging/hardcoding.
func getFrameDurationsMS(input, ffprobeBin string) []int {
	out, err := exec.Command(ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "frame=duration_time", "-of", "csv=p=0", input).CombinedOutput()
	if err != nil {
		return nil
	}
	var ds []int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v, e := strconv.ParseFloat(line, 64)
		if e != nil {
			continue
		}
		ms := int(v*1000 + 0.5)
		if ms < 1 {
			ms = 1
		}
		ds = append(ds, ms)
	}
	return ds
}

// generateStickerDualFormat reads source gifs from each group's gif/ subfolder and
// regenerates the avif+jxl of any whose source is newer (no root-level fallback). It
// returns the payload URLs it rewrote — the purge set for refreshStickers.
//
// Parameters copied verbatim from the final sticker pipeline: original resolution, no
// scaling; JXL = magick -coalesce -> cjxl -d 0 -m 1 -e 10 (OOM degrades to -e 9);
// AVIF = ffmpeg -fps_mode passthrough + alpha-safe hqdn3d -> avifenc q70 444 10bit k1
// timescale1000 with per-frame --duration.
func generateStickerDualFormat() []string {
	ff := ffmpegBin
	fp := toolPath("ffprobe")
	av := avifencBin
	cj := cjxlBin
	magick := magickCmd
	if ff == "" || fp == "" || av == "" || cj == "" || magick == "" {
		log.Printf("sticker: 缺少媒体工具，跳过双格式生成")
		return nil
	}
	vf := "format=rgba,split=2[m][a];[m]hqdn3d=2:1:6:6,format=rgba[dn];[a]alphaextract,format=gray[am];[dn][am]alphamerge"
	var rewritten []string
	total := 0

	eachStickerSource(func(groupName, base, gifPath string) {
		groupDir := filepath.Join(DirSticker, groupName)
		avifOut := filepath.Join(groupDir, "avif", base+".avif")
		jxlOut := filepath.Join(groupDir, "jxl", base+".jxl")
		os.MkdirAll(filepath.Dir(avifOut), 0755)
		os.MkdirAll(filepath.Dir(jxlOut), 0755)

		// AVIF (regenerate when the source is newer).
		if needsRegen(gifPath, avifOut) {
			tmp, _ := os.MkdirTemp("", "stk-anim-*")
			framePattern := filepath.Join(tmp, "frame_%04d.png")
			if out, err := exec.Command(ff, "-y", "-i", gifPath, "-fps_mode", "passthrough", "-vf", vf, framePattern).CombinedOutput(); err != nil {
				log.Printf("sticker: ffmpeg fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
			} else {
				durs := getFrameDurationsMS(gifPath, fp)
				frameFiles, _ := filepath.Glob(filepath.Join(tmp, "frame_*.png"))
				sort.Strings(frameFiles)
				nfrm := len(frameFiles)
				encArgs := []string{"-y", "444", "-q", "70", "-s", "4", "--depth", "10", "-k", "1", "--timescale", "1000"}
				if nfrm > 0 && nfrm == len(durs) {
					for i, fr := range frameFiles {
						d := durs[i]
						if d < 1 {
							d = 1
						}
						encArgs = append(encArgs, "--duration", strconv.Itoa(d), fr)
					}
				} else {
					avg := 33
					if len(durs) > 0 {
						sum := 0
						for _, d := range durs {
							sum += d
						}
						avg = sum / len(durs)
					}
					if avg < 1 {
						avg = 33
					}
					for _, fr := range frameFiles {
						encArgs = append(encArgs, "--duration", strconv.Itoa(avg), fr)
					}
					if nfrm != len(durs) {
						log.Printf("sticker: WARN %s/%s 帧数/延时不匹配 (%d vs %d) -> uniform %d ms", groupName, base, nfrm, len(durs), avg)
					}
				}
				encArgs = append(encArgs, avifOut)
				if out, err := exec.Command(av, encArgs...).CombinedOutput(); err != nil {
					log.Printf("sticker: avifenc fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
				} else {
					total++
					rewritten = append(rewritten, stickerPayloadURL(groupName, "avif", base+".avif"))
				}
			}
			os.RemoveAll(tmp)
		}

		// JXL (regenerate when the source is newer; forced coalesce, OOM degrades to -e 9).
		if needsRegen(gifPath, jxlOut) {
			tmp, _ := os.MkdirTemp("", "stk-jxl-*")
			coalesced := filepath.Join(tmp, "coalesced.gif")
			if out, err := exec.Command(magick, gifPath, "-coalesce", coalesced).CombinedOutput(); err != nil {
				log.Printf("sticker: coalesce fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out)))
			} else if _, err := exec.Command(cj, "-d", "0", "-m", "1", "-e", "10", coalesced, jxlOut).CombinedOutput(); err != nil {
				if out2, err2 := exec.Command(cj, "-d", "0", "-m", "1", "-e", "9", coalesced, jxlOut).CombinedOutput(); err2 != nil {
					log.Printf("sticker: cjxl fail %s/%s: %s", groupName, base, strings.TrimSpace(string(out2)))
				} else {
					total++
					rewritten = append(rewritten, stickerPayloadURL(groupName, "jxl", base+".jxl"))
				}
			} else {
				total++
				rewritten = append(rewritten, stickerPayloadURL(groupName, "jxl", base+".jxl"))
			}
			os.RemoveAll(tmp)
		}
	})
	log.Printf("sticker: generateStickerDualFormat done, %d 生成", total)
	return rewritten
}

// handleRefreshStickers POST /admin/refresh-stickers — manually refresh the sticker cache.
func handleRefreshStickers(w http.ResponseWriter, r *http.Request) {
	if !isAdmin(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	refreshStickers()
	adminRespond(w, r, "表情缓存已刷新")
}

// handleUploadSticker POST /api/sticker/upload — upload a new sticker pack into the review dir.
func handleUploadSticker(w http.ResponseWriter, r *http.Request) {
	// Limit upload size to 256MB
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)

	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, fmt.Sprintf("上传失败: %v", err), http.StatusBadRequest)
		return
	}

	displayName := strings.TrimSpace(r.FormValue("name"))
	if displayName == "" {
		http.Error(w, "表情包名称不能为空", http.StatusBadRequest)
		return
	}

	// Use timestamp+name as the directory name to avoid collisions
	dirName := fmt.Sprintf("%s_%s", time.Now().Format("20060102_150405"), displayName)
	pendingDir := filepath.Join(DirStagingSticker, dirName)

	if err := os.MkdirAll(pendingDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("创建目录失败: %v", err), http.StatusInternalServerError)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		http.Error(w, "请选择至少一个文件", http.StatusBadRequest)
		return
	}

	for _, fh := range files {
		if fh.Size > 256<<20 {
			http.Error(w, fmt.Sprintf("文件 %s 超过 256MB 限制", fh.Filename), http.StatusBadRequest)
			return
		}

		ext := strings.ToLower(filepath.Ext(fh.Filename))
		if !supportedExt[ext] {
			http.Error(w, fmt.Sprintf("不支持的文件格式: %s", ext), http.StatusBadRequest)
			return
		}

		safeName := filepath.Base(fh.Filename)
		// Source gifs land in gif/ so that after a moderator moves the whole pack to
		// static/sticker/<group>/ they naturally sit in the gif/ subfolder
		gifPendingDir := filepath.Join(pendingDir, "gif")
		os.MkdirAll(gifPendingDir, 0755)
		dstPath := filepath.Join(gifPendingDir, safeName)

		src, err := fh.Open()
		if err != nil {
			http.Error(w, fmt.Sprintf("打开上传文件失败: %v", err), http.StatusInternalServerError)
			return
		}

		dst, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			http.Error(w, fmt.Sprintf("保存文件失败: %v", err), http.StatusInternalServerError)
			return
		}

		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			http.Error(w, fmt.Sprintf("写入文件失败: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Save metadata
	meta := map[string]string{
		"name": displayName,
		"time": time.Now().Format(time.RFC3339),
	}
	metaData, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(pendingDir, "meta.json"), metaData, 0644)

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}
