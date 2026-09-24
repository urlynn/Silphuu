package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Image Converter.

// ConvertTarget describes one output file produced by a conversion.
type ConvertTarget struct {
	Format string // "jxl" or "avif"
	Path   string // full path on disk
	URL    string // web-accessible URL
}

// ConvertOptions holds image conversion parameters.
type ConvertOptions struct {
	InputPath    string              // source image path
	OutputDir    string              // output directory
	URLGenerator func(string) string // url generator for generated files
	BaseName     string              // output file name (no extension)
	Width        int                 // resize width (0 = no resize)
	Scale        int                 // resize percentage (0 = no resize)
	Formats      []string            // target formats, e.g. []string{"avif"} or []string{"jxl","avif"}
}

// imagemagickPath prefers a system-installed ImageMagick (Homebrew) over any bundled copy.
func imagemagickPath() string {
	// Check system install paths first
	preferred := []string{
		"/opt/homebrew/bin/magick",
		"/opt/homebrew/bin/convert",
		"/usr/local/bin/magick",
		"/usr/local/bin/convert",
		"/usr/bin/convert",
	}
	for _, p := range preferred {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Fall back to PATH lookup
	for _, name := range []string{"magick", "convert"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return "convert"
}

// toolPath finds a tool's full path in the system PATH and common Homebrew locations.
func toolPath(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	// Common install locations (Apple Silicon / Intel / Linux)
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"} {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return name
}

var (
	magickCmd  = imagemagickPath()
	avifencBin = toolPath("avifenc")
	cjxlBin    = toolPath("cjxl")
	djxlBin    = toolPath("djxl")
	ffmpegBin  = toolPath("ffmpeg")
)

// ConvertImage runs the image conversion: optional resize -> input normalization
// (jxl/heic -> png) -> encode to target formats.
// No sRGB color-space conversion is performed; raw pixel values are preserved to avoid
// shifting colors on HDR/ICC images.
func ConvertImage(opts ConvertOptions) ([]ConvertTarget, error) {
	os.MkdirAll(opts.OutputDir, 0755)

	inputFile := opts.InputPath

	// Resize (optional).
	if opts.Width > 0 || opts.Scale > 0 {
		resizeArg := ""
		if opts.Width > 0 {
			resizeArg = fmt.Sprintf("%d", opts.Width)
		} else {
			resizeArg = fmt.Sprintf("%d%%", opts.Scale)
		}

		tmpResized, err := os.CreateTemp("", "convert-resized-*.png")
		if err != nil {
			return nil, fmt.Errorf("创建临时文件失败: %w", err)
		}
		resizedPath := tmpResized.Name()
		tmpResized.Close()
		defer os.Remove(resizedPath)

		magickArgs := []string{inputFile, "-resize", resizeArg, resizedPath}
		if strings.HasSuffix(magickCmd, "magick") {
			magickArgs = append([]string{"convert"}, magickArgs...)
		}

		if output, err := exec.Command(magickCmd, magickArgs...).CombinedOutput(); err != nil {
			errMsg := strings.TrimSpace(string(output))
			if errMsg == "" {
				errMsg = err.Error()
			}
			if strings.Contains(errMsg, "executable file not found") {
				return nil, fmt.Errorf("未找到 ImageMagick，请先安装：brew install imagemagick / apt install imagemagick")
			}
			return nil, fmt.Errorf("缩放失败: %s", errMsg)
		}
		inputFile = resizedPath
	}

	// Input normalization: jxl/heic -> png (avifenc/cjxl cannot read these directly).
	inputExt := strings.ToLower(filepath.Ext(inputFile))
	needPNG := false
	for _, f := range opts.Formats {
		if f == "avif" && (inputExt == ".jxl" || inputExt == ".heic" || inputExt == ".heif") {
			needPNG = true
		}
		if f == "jxl" && (inputExt == ".heic" || inputExt == ".heif") {
			needPNG = true
		}
	}

	if needPNG {
		tmpPNG, err := os.CreateTemp("", "convert-normalized-*.png")
		if err != nil {
			return nil, fmt.Errorf("创建临时文件失败: %w", err)
		}
		pngPath := tmpPNG.Name()
		tmpPNG.Close()
		defer os.Remove(pngPath)

		var cmd *exec.Cmd
		if inputExt == ".jxl" {
			cmd = exec.Command(djxlBin, inputFile, pngPath)
		} else {
			// HEIC/HEIF
			magickArgs := []string{inputFile, pngPath}
			if strings.HasSuffix(magickCmd, "magick") {
				magickArgs = append([]string{"convert"}, magickArgs...)
			}
			cmd = exec.Command(magickCmd, magickArgs...)
		}
		if output, err := cmd.CombinedOutput(); err != nil {
			errMsg := strings.TrimSpace(string(output))
			if errMsg == "" {
				errMsg = err.Error()
			}
			return nil, fmt.Errorf("输入解码失败: %s", errMsg)
		}
		inputFile = pngPath
	}

	// Encode.
	var targets []ConvertTarget
	for _, format := range opts.Formats {
		outPath := filepath.Join(opts.OutputDir, opts.BaseName+"."+format)
		var cmd *exec.Cmd
		switch format {
		case "jxl":
			if cjxlBin == "" {
				continue
			}
			if err := cjxlEncode(inputFile, outPath); err != nil {
				return targets, err
			}
			// cjxlEncode already wrote outPath directly; no need to run cmd
			urlPath := "/" + filepath.ToSlash(filepath.Join(opts.OutputDir, opts.BaseName+"."+format))
			if opts.URLGenerator != nil {
				urlPath = opts.URLGenerator(opts.BaseName + "." + format)
			}
			targets = append(targets, ConvertTarget{
				Format: format,
				Path:   outPath,
				URL:    urlPath,
			})
			continue
		case "avif":
			if avifencBin == "" {
				continue
			}
			// Lossy + high fidelity (visually lossless): max16 ≈ q88, 10-bit 4:2:0, SSIM tuning, speed 4
			cmd = exec.Command(avifencBin, "-c", "aom", "--min", "0", "--max", "16",
				"-s", "4", "-j", "all", "-d", "10", "-y", "420", "-a", "tune=ssim",
				inputFile, outPath)
		default:
			continue
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := strings.TrimSpace(string(output))
			if errMsg == "" {
				errMsg = err.Error()
			}
			return targets, fmt.Errorf("%s 编码失败: %s", format, errMsg)
		}

		urlPath := "/" + filepath.ToSlash(filepath.Join(opts.OutputDir, opts.BaseName+"."+format))
		if opts.URLGenerator != nil {
			urlPath = opts.URLGenerator(opts.BaseName + "." + format)
		}
		targets = append(targets, ConvertTarget{
			Format: format,
			Path:   outPath,
			URL:    urlPath,
		})
	}

	return targets, nil
}

// cjxlEncode encodes JXL. Uniform lossless scheme: cjxl -d 0 -m 1 -e 10 (auto-degrades
// to -e 9 on OOM). Writes outPath directly.
func cjxlEncode(input, out string) error {
	if cjxlBin == "" {
		return fmt.Errorf("cjxl 不可用")
	}
	run := func(effort string) ([]byte, error) {
		args := []string{"-d", "0", "-m", "1", "-e", effort}
		return exec.Command(cjxlBin, append(args, input, out)...).CombinedOutput()
	}
	if _, err := run("10"); err != nil {
		// On OOM or other failure, degrade to effort 9 (still lossless, just slower)
		if out3, err2 := run("9"); err2 != nil {
			return fmt.Errorf("jxl 编码失败: %s", strings.TrimSpace(string(out3)))
		}
		return nil
	}
	return nil
}

// getFrameDelaysCS returns per-frame durations in centiseconds (the raw GIF %T value;
// 1 cs = 10ms). Callers convert as needed: timescale=1000 -> x10 for ms; timescale=100 ->
// use directly as the time unit.
// Per-frame delays are only read reliably for GIF; other formats (APNG/WebP) return nil
// -> callers fall back to a default duration.
// Never compute a single duration from average fps and apply it to every frame — that
// loses the source's per-frame rhythm (e.g. one 40ms frame in the middle of a clip).
func getFrameDelaysCS(input string, n int) []int {
	if strings.ToLower(filepath.Ext(input)) != ".gif" {
		return nil
	}
	out, err := exec.Command(magickCmd, "identify", "-format", "%T\n", input).CombinedOutput()
	if err != nil {
		return nil
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	var ds []int
	for _, f := range fields {
		if v, e := strconv.Atoi(f); e == nil {
			ds = append(ds, v) // raw centiseconds value
		}
	}
	if len(ds) == 0 {
		return nil
	}
	if len(ds) < n {
		last := ds[len(ds)-1]
		for len(ds) < n {
			ds = append(ds, last)
		}
	} else if len(ds) > n {
		ds = ds[:n]
	}
	return ds
}

// ConvertAnimated generates dual formats for animated images (JXL primary + AVIF fallback).
// JXL: cjxl -d 0 -m 1 -e 10 (OOM degrades to -e 9), reading the animation source directly (GIF/APNG).
// AVIF: ffmpeg denoise (hqdn3d) + alpha isolation -> per-frame PNG -> avifenc with interleaved
// per-frame --duration; no scaling.
// MaxSide is not passed (comment/body animations keep original size). Returns both .jxl and
// .avif targets (only AVIF if JXL fails).
func ConvertAnimated(opts ConvertOptions) ([]ConvertTarget, error) {
	os.MkdirAll(opts.OutputDir, 0755)
	jxlPath := filepath.Join(opts.OutputDir, opts.BaseName+".jxl")
	avifPath := filepath.Join(opts.OutputDir, opts.BaseName+".avif")
	var targets []ConvertTarget

	// JXL (lossless animation).
	if cjxlBin != "" {
		if err := cjxlEncode(opts.InputPath, jxlPath); err != nil {
			log.Printf("warn: 动画 JXL 编码失败 %s: %v（仅输出 AVIF）", opts.BaseName, err)
		} else {
			targets = append(targets, ConvertTarget{"jxl", jxlPath, "/" + filepath.ToSlash(jxlPath)})
		}
	}

	// AVIF: denoise + frame extraction + per-frame durations.
	tmpDir, err := os.MkdirTemp("", "anim-*")
	if err != nil {
		return targets, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	framePat := filepath.Join(tmpDir, "f%04d.png")
	// Alpha isolation: putting hqdn3d in the filter chain drops alpha (RGBA gets converted
	// to no-alpha) -> split to extract alpha first, then alphamerge
	vf := "format=rgba,split=2[m][a];[m]hqdn3d=2:1:6:6,format=rgba[dn];[a]alphaextract,format=gray[am];[dn][am]alphamerge"
	fcmd := exec.Command(ffmpegBin, "-i", opts.InputPath, "-vf", vf, "-vsync", "0", "-start_number", "0", framePat)
	if out, ferr := fcmd.CombinedOutput(); ferr != nil {
		errMsg := strings.TrimSpace(string(out))
		if errMsg == "" {
			errMsg = ferr.Error()
		}
		return targets, fmt.Errorf("avif 帧提取失败: %s", errMsg)
	}
	frames, _ := filepath.Glob(filepath.Join(tmpDir, "f*.png"))
	if len(frames) == 0 {
		return targets, fmt.Errorf("avif 无帧")
	}
	sort.Strings(frames)
	delays := getFrameDelaysCS(opts.InputPath, len(frames))

	args := []string{"-y", "444", "-q", "70", "-s", "4", "-d", "10", "-k", "1", "--timescale", "1000"}
	for i, f := range frames {
		d := 100 // default 100ms (fallback for non-GIF / failed frame reads)
		if i < len(delays) {
			d = delays[i] * 10 // centiseconds -> milliseconds (timescale 1000)
		}
		if d < 1 {
			d = 1
		}
		// --duration applies to all following inputs (sticky), so insert one before each
		// frame -> per-frame timing
		args = append(args, "--duration", strconv.Itoa(d))
		args = append(args, f)
	}
	args = append(args, avifPath)

	acmd := exec.Command(avifencBin, args...)
	if out, aerr := acmd.CombinedOutput(); aerr != nil {
		errMsg := strings.TrimSpace(string(out))
		if errMsg == "" {
			errMsg = aerr.Error()
		}
		return targets, fmt.Errorf("avif 编码失败: %s", errMsg)
	}
	urlPath := "/" + filepath.ToSlash(filepath.Join(opts.OutputDir, opts.BaseName+".avif"))
	if opts.URLGenerator != nil {
		urlPath = opts.URLGenerator(opts.BaseName + ".avif")
	}
	targets = append(targets, ConvertTarget{"avif", avifPath, urlPath})
	return targets, nil
}
