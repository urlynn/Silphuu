package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// MediaProbe holds image sniffing metadata.
type MediaProbe struct {
	Width      int
	Height     int
	FrameCount int
	IsAnimated bool
}

// MediaResult is the result returned by the media submission pipeline.
type MediaResult struct {
	ID       string `json:"id"`
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url,omitempty"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Format   string `json:"format"`
}

// generateSecureID generates a 16-character random hex secure ID.
func generateSecureID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// No-transcode blacklist: two classes of uploads must skip JXL/AVIF transcoding —
// vector images (rasterizing SVG permanently loses vectorness: blurry when scaled,
// impossible to recolor via CSS, often LARGER) and files already in a target format
// (re-encoding is lossy recompression: pure quality loss and wasted CPU).
//
// Two maps because the criteria differ:
var (
	// vectorExts — vector formats. NEVER transcode: rasterization is irreversible semantic loss.
	vectorExts = map[string]bool{
		".svg":  true, // SVG (XML)
		".svgz": true, // gzip-compressed SVG
	}

	// targetExts — already this pipeline's target formats; re-encoding only loses quality.
	// BUT if scaling is needed (exceeds preset.MaxWidth) still transcode —
	// the value then is the scaling, not the format change, so those must not be skipped.
	targetExts = map[string]bool{
		".avif": true,
		".jxl":  true,
	}
)

// saveVerbatim copies the uploaded file verbatim into the output dir (zero transcoding)
// and returns the accessible URL.
//
// Security: SVG can embed <script>. This project always displays images via <img>, a
// context where scripts do NOT execute; static assets are also served with
// X-Content-Type-Options: nosniff.
// If inline display is ever introduced (<object> / innerHTML injection), SVG
// sanitization MUST come first.
func saveVerbatim(src, outputDir string, urlGenerator func(string) string, baseName, ext string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("创建输出目录失败: %w", err)
	}
	dst := filepath.Join(outputDir, baseName+ext)
	in, err := os.Open(src)
	if err != nil {
		return "", fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return "", fmt.Errorf("创建目标文件失败: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return "", fmt.Errorf("写入目标文件失败: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("关闭目标文件失败: %w", err)
	}

	if urlGenerator != nil {
		return urlGenerator(baseName + ext), nil
	}
	return "/" + filepath.ToSlash(dst), nil
}

// SniffMediaInput spools the upload stream and sniffs dimensions and animated frame count.
func SniffMediaInput(file multipart.File, filename string) (string, MediaProbe, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		ext = ".tmp"
	}

	tmpFile, err := os.CreateTemp("", "upload-*"+ext)
	if err != nil {
		return "", MediaProbe{}, fmt.Errorf("创建临时上传文件失败: %w", err)
	}
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, file); err != nil {
		os.Remove(tmpFile.Name())
		return "", MediaProbe{}, fmt.Errorf("保存上传文件失败: %w", err)
	}

	tmpPath := tmpFile.Name()
	probe := MediaProbe{Width: 0, Height: 0, FrameCount: 1, IsAnimated: false}

	// Probe dimensions and frame count: magick identify -format "%w %h %n\n" file[0]
	args := []string{"-format", "%w %h %n\n", tmpPath}
	if strings.HasSuffix(magickCmd, "magick") {
		args = append([]string{"identify"}, args...)
	}

	out, err := exec.Command(magickCmd, args...).Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) > 0 {
			parts := strings.Fields(lines[0])
			if len(parts) >= 2 {
				probe.Width, _ = strconv.Atoi(parts[0])
				probe.Height, _ = strconv.Atoi(parts[1])
			}
			if len(parts) >= 3 {
				probe.FrameCount, _ = strconv.Atoi(parts[2])
			} else {
				probe.FrameCount = len(lines)
			}
			if probe.FrameCount > 1 || ext == ".gif" {
				probe.IsAnimated = true
			}
		}
	}

	return tmpPath, probe, nil
}

// ProcessMediaPipeline is the generic declarative media pipeline entry point.
func ProcessMediaPipeline(r *http.Request, preset MediaPreset) (*MediaResult, error) {
	// 1. Read the uploaded file (50MB limit)
	r.Body = http.MaxBytesReader(nil, r.Body, 50<<20)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		return nil, fmt.Errorf("文件体积过大 (上限 50MB): %w", err)
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, fmt.Errorf("未找到上传文件字段 'file': %w", err)
	}
	defer file.Close()

	// 2. Sniff metadata
	tmpInput, probe, err := SniffMediaInput(file, header.Filename)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpInput)

	// 3. Generate the base filename and directory
	baseName := generateSecureID()
	if preset.DeviceDetect {
		device := "desktop"
		if probe.Height > probe.Width {
			device = "mobile"
		}
		baseName = fmt.Sprintf("%s-%s", device, baseName[:6])
	}

	outputDir := preset.OutputDir()
	if outputDir == "" {
		return nil, fmt.Errorf("输出目录未解析：媒体预设配置错误")
	}
	os.MkdirAll(outputDir, 0755)

	// 3.5 No-transcode blacklist — on a hit, save verbatim and return, skipping transcoding.
	//     Rationale: see the comment at the vectorExts / targetExts definitions.
	{
		ext := strings.ToLower(filepath.Ext(header.Filename))
		needResize := preset.MaxWidth > 0 && probe.Width > preset.MaxWidth
		if vectorExts[ext] || (targetExts[ext] && !needResize) {
			url, err := saveVerbatim(tmpInput, outputDir, preset.URLGenerator, baseName, ext)
			if err != nil {
				return nil, err
			}
			res := &MediaResult{
				ID:     baseName,
				URL:    url,
				Width:  probe.Width,
				Height: probe.Height,
				Format: strings.TrimPrefix(ext, "."),
			}
			// Presets like the album still need a companion thumbnail — kept consistent with
			// the transcode branch
			if preset.GenerateThumb {
				generateBlurThumb(tmpInput, baseName)
				res.ThumbURL = URLThumbPhotoBlur(baseName)
			}
			return res, nil
		}
	}

	// 4. Branch on animated/static and transcode (AVIF + JXL)
	var mainURL string
	if probe.IsAnimated && preset.AllowAnimated {
		// Animated transcode: JXL + AVIF (with per-frame durations)
		_, err := ConvertAnimated(ConvertOptions{
			InputPath:    tmpInput,
			OutputDir:    outputDir,
			URLGenerator: preset.URLGenerator,
			BaseName:     baseName,
		})
		if err != nil {
			return nil, fmt.Errorf("动图转码失败: %w", err)
		}
		if preset.URLGenerator != nil {
			mainURL = preset.URLGenerator(baseName+".avif") + "?anim"
		} else {
			mainURL = fmt.Sprintf("/%s/%s.avif?anim", filepath.ToSlash(outputDir), baseName)
		}
	} else {
		// Static image transcode: optional proportional scaling
		resizeW := 0
		if preset.MaxWidth > 0 && probe.Width > preset.MaxWidth {
			resizeW = preset.MaxWidth
		}

		_, err := ConvertImage(ConvertOptions{
			InputPath:    tmpInput,
			OutputDir:    outputDir,
			URLGenerator: preset.URLGenerator,
			BaseName:     baseName,
			Width:        resizeW,
			Formats:      []string{"jxl", "avif"},
		})
		if err != nil {
			return nil, fmt.Errorf("图片转码失败: %w", err)
		}
		if preset.URLGenerator != nil {
			mainURL = preset.URLGenerator(baseName + ".avif")
		} else {
			mainURL = fmt.Sprintf("/%s/%s.avif", filepath.ToSlash(outputDir), baseName)
		}
	}

	res := &MediaResult{
		ID:     baseName,
		URL:    mainURL,
		Width:  probe.Width,
		Height: probe.Height,
		Format: "jxl+avif",
	}

	// 5. Generate a companion thumbnail if needed (e.g. album: 230px 4:4:4 gaussian blur
	//    into DirThumbPhotoBlur)
	if preset.GenerateThumb {
		generateBlurThumb(tmpInput, baseName)
		res.ThumbURL = URLThumbPhotoBlur(baseName)
	}

	return res, nil
}
