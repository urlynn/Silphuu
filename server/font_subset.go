package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// collectGoStringChars parses Go sources with go/ast to extract string literals.
func collectGoStringChars() map[rune]struct{} {
	chars := make(map[rune]struct{})
	fset := token.NewFileSet()
	goFiles := []string{"main.go", "handler.go", "status_api.go", "auth.go", "cache.go", "router.go"}
	for _, fn := range goFiles {
		f, err := parser.ParseFile(fset, fn, nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING {
				if s, err := strconv.Unquote(bl.Value); err == nil {
					for _, c := range s {
						if isWebOwnable(c) {
							chars[c] = struct{}{}
						}
					}
				}
			}
			return true
		})
	}
	return chars
}

// homeSubsetMu guards the home subset against concurrent runs.
var homeSubsetMu sync.Mutex

// adminSubsetMu guards the admin-page subset against concurrent runs.
var adminSubsetMu sync.Mutex

// bodySubsetMu guards the site-wide body subset against concurrent runs.
var bodySubsetMu sync.Mutex

// fontSubsetMu guards the comment subset against concurrent runs.
var fontSubsetMu sync.Mutex

// fontSubsetTask defines the subsetting + compression task for a single font.
type fontSubsetTask struct {
	Src          string `json:"src"`
	Chars        string `json:"chars"`
	Out          string `json:"out"`
	RemapZeroToO bool   `json:"remap_zero_to_o,omitempty"`
}

// fontMapInfo mirrors config/font_map.json.
type fontMapInfo struct {
	Src    string `json:"src"`
	Weight string `json:"weight"`
	// Name labels the face in the admin font dropdown; empty falls back to the map key.
	Name string `json:"name"`
	// Bevl is the variable-font bevel range, e.g. "1 100". Non-empty reveals the bevel
	// input for this face in the admin panel.
	Bevl string `json:"bevl"`
	// Order positions the face in the admin dropdown, ties broken by Name. A JSON object
	// has no order of its own, so the dropdown order has to come from the data.
	Order int `json:"order"`
}

// extraCharsConfig mirrors config/font_extra_chars.json.
type extraCharsConfig struct {
	Description   string   `json:"description"`
	Presets       []string `json:"presets"`
	UnicodeRanges []string `json:"unicode_ranges"`
	ExtraChars    string   `json:"extra_chars"`
}

// findSubsettingTools locates the official Google tool paths.
func findSubsettingTools() (string, string, error) {
	hbCandidates := []string{
		"hb-subset",
		"/opt/homebrew/bin/hb-subset",
		"/usr/local/bin/hb-subset",
		"/usr/bin/hb-subset",
	}
	var hbPath string
	for _, p := range hbCandidates {
		if lp, err := exec.LookPath(p); err == nil {
			hbPath = lp
			break
		}
	}
	if hbPath == "" {
		return "", "", fmt.Errorf("required tool 'hb-subset' (HarfBuzz) not found. Please install: brew install harfbuzz (macOS) or apt install libharfbuzz-bin (Linux)")
	}

	woff2Candidates := []string{
		"woff2_compress",
		"/opt/homebrew/bin/woff2_compress",
		"/usr/local/bin/woff2_compress",
		"/usr/bin/woff2_compress",
	}
	var woff2Path string
	for _, p := range woff2Candidates {
		if lp, err := exec.LookPath(p); err == nil {
			woff2Path = lp
			break
		}
	}
	if woff2Path == "" {
		return "", "", fmt.Errorf("required tool 'woff2_compress' (Google WOFF2) not found. Please install: brew install woff2 (macOS) or apt install woff2 (Linux)")
	}

	return hbPath, woff2Path, nil
}

type taskResult struct {
	Out       string
	CharCount int
	OutSize   int64
	Err       error
}

// remapGlyphByRebuildingGlyf parses the TTF cmap to find the Glyph IDs of '0' (0x30) and
// 'O' (0x4F), replaces the '0' glyf outline with the 'O' outline, and rebuilds the
// monotonically increasing loca index and hmtx horizontal metrics.
func remapGlyphByRebuildingGlyf(data []byte) ([]byte, error) {
	if len(data) < 12 {
		return nil, fmt.Errorf("file too short")
	}
	numTables := binary.BigEndian.Uint16(data[4:6])

	type tableRec struct {
		tag      string
		checksum uint32
		offset   uint32
		length   uint32
		recIndex int
	}
	tables := map[string]tableRec{}
	for i := 0; i < int(numTables); i++ {
		off := 12 + i*16
		tag := string(data[off : off+4])
		cs := binary.BigEndian.Uint32(data[off+4 : off+8])
		tOffset := binary.BigEndian.Uint32(data[off+8 : off+12])
		tLength := binary.BigEndian.Uint32(data[off+12 : off+16])
		tables[tag] = tableRec{tag: tag, checksum: cs, offset: tOffset, length: tLength, recIndex: i}
	}

	head, ok := tables["head"]
	if !ok || head.length < 54 {
		return nil, fmt.Errorf("head table not found or too short")
	}
	locaFmt := binary.BigEndian.Uint16(data[head.offset+50 : head.offset+52])

	maxp, ok := tables["maxp"]
	if !ok || maxp.length < 6 {
		return nil, fmt.Errorf("maxp table not found")
	}
	numGlyphs := binary.BigEndian.Uint16(data[maxp.offset+4 : maxp.offset+6])

	cmapT, ok := tables["cmap"]
	if !ok {
		return nil, fmt.Errorf("cmap not found")
	}
	cmap := data[cmapT.offset : cmapT.offset+cmapT.length]
	numSubtables := binary.BigEndian.Uint16(cmap[2:4])

	var zeroGid, oGid uint16
	var foundZero, foundO bool

	for i := 0; i < int(numSubtables); i++ {
		subOffset := binary.BigEndian.Uint32(cmap[4+i*8+4 : 4+i*8+8])
		if int(subOffset) >= len(cmap) {
			continue
		}
		format := binary.BigEndian.Uint16(cmap[subOffset : subOffset+2])
		if format == 4 {
			segCountX2 := binary.BigEndian.Uint16(cmap[subOffset+6 : subOffset+8])
			segCount := int(segCountX2 / 2)
			endCodeOff := int(subOffset + 14)
			startCodeOff := endCodeOff + segCount*2 + 2
			idDeltaOff := startCodeOff + segCount*2
			idRangeOffOff := idDeltaOff + segCount*2

			getGid := func(ch uint16) (uint16, bool) {
				for seg := 0; seg < segCount; seg++ {
					end := binary.BigEndian.Uint16(cmap[endCodeOff+seg*2 : endCodeOff+seg*2+2])
					start := binary.BigEndian.Uint16(cmap[startCodeOff+seg*2 : startCodeOff+seg*2+2])
					if start <= ch && ch <= end {
						delta := int16(binary.BigEndian.Uint16(cmap[idDeltaOff+seg*2 : idDeltaOff+seg*2+2]))
						rangeOffset := binary.BigEndian.Uint16(cmap[idRangeOffOff+seg*2 : idRangeOffOff+seg*2+2])
						if rangeOffset == 0 {
							return uint16(int16(ch) + delta), true
						}
						glyphOff := int(idRangeOffOff+seg*2) + int(rangeOffset) + int(ch-start)*2
						if glyphOff+2 <= len(cmap) {
							gid := binary.BigEndian.Uint16(cmap[glyphOff : glyphOff+2])
							if gid != 0 {
								return uint16(int16(gid) + delta), true
							}
						}
					}
				}
				return 0, false
			}

			if g, ok := getGid(0x0030); ok {
				zeroGid = g
				foundZero = true
			}
			if g, ok := getGid(0x004F); ok {
				oGid = g
				foundO = true
			}
			if foundZero && foundO {
				break
			}
		}
	}

	if !foundZero || !foundO {
		return nil, fmt.Errorf("zeroGid/oGid not found (foundZero=%v, foundO=%v)", foundZero, foundO)
	}

	locaT, ok := tables["loca"]
	if !ok {
		return nil, fmt.Errorf("loca table not found")
	}
	glyfT, ok := tables["glyf"]
	if !ok {
		return nil, fmt.Errorf("glyf table not found")
	}

	getGlyphSlice := func(gid uint16) []byte {
		var start, end uint32
		if locaFmt == 0 {
			start = uint32(binary.BigEndian.Uint16(data[locaT.offset+uint32(gid)*2:locaT.offset+uint32(gid)*2+2])) * 2
			end = uint32(binary.BigEndian.Uint16(data[locaT.offset+uint32(gid+1)*2:locaT.offset+uint32(gid+1)*2+2])) * 2
		} else {
			start = binary.BigEndian.Uint32(data[locaT.offset+uint32(gid)*4 : locaT.offset+uint32(gid)*4+4])
			end = binary.BigEndian.Uint32(data[locaT.offset+uint32(gid+1)*4 : locaT.offset+uint32(gid+1)*4+4])
		}
		if start >= end || glyfT.offset+end > uint32(len(data)) {
			return nil
		}
		return data[glyfT.offset+start : glyfT.offset+end]
	}

	glyphSlices := make([][]byte, numGlyphs)
	for gid := uint16(0); gid < numGlyphs; gid++ {
		if gid == zeroGid {
			glyphSlices[gid] = getGlyphSlice(oGid)
		} else {
			glyphSlices[gid] = getGlyphSlice(gid)
		}
	}

	var newGlyf bytes.Buffer
	newLocaOffsets := make([]uint32, numGlyphs+1)
	for gid := uint16(0); gid < numGlyphs; gid++ {
		newLocaOffsets[gid] = uint32(newGlyf.Len())
		if len(glyphSlices[gid]) > 0 {
			newGlyf.Write(glyphSlices[gid])
			for newGlyf.Len()%4 != 0 {
				newGlyf.WriteByte(0)
			}
		}
	}
	newLocaOffsets[numGlyphs] = uint32(newGlyf.Len())

	var newLoca bytes.Buffer
	useLongLoca := false
	for _, off := range newLocaOffsets {
		if off > 0xFFFF*2 {
			useLongLoca = true
			break
		}
	}
	if locaFmt == 1 || useLongLoca {
		useLongLoca = true
		for _, off := range newLocaOffsets {
			_ = binary.Write(&newLoca, binary.BigEndian, off)
		}
	} else {
		for _, off := range newLocaOffsets {
			_ = binary.Write(&newLoca, binary.BigEndian, uint16(off/2))
		}
	}

	if hmtxT, ok := tables["hmtx"]; ok {
		hheaT, hasHhea := tables["hhea"]
		if hasHhea && hheaT.length >= 36 {
			numOfLongHorMetrics := binary.BigEndian.Uint16(data[hheaT.offset+34 : hheaT.offset+36])
			if zeroGid < numOfLongHorMetrics && oGid < numOfLongHorMetrics {
				offZero := hmtxT.offset + uint32(zeroGid)*4
				offO := hmtxT.offset + uint32(oGid)*4
				copy(data[offZero:offZero+4], data[offO:offO+4])
			}
		}
	}

	var out bytes.Buffer
	out.Write(data[:12+int(numTables)*16])

	if useLongLoca && locaFmt == 0 {
		headRec := tables["head"]
		data[headRec.offset+50] = 0
		data[headRec.offset+51] = 1
	}

	for tag, rec := range tables {
		var tableBytes []byte
		if tag == "loca" {
			tableBytes = newLoca.Bytes()
		} else if tag == "glyf" {
			tableBytes = newGlyf.Bytes()
		} else {
			tableBytes = data[rec.offset : rec.offset+rec.length]
		}

		tableOffset := uint32(out.Len())
		tableLen := uint32(len(tableBytes))
		out.Write(tableBytes)
		for out.Len()%4 != 0 {
			out.WriteByte(0)
		}

		entryOff := 12 + rec.recIndex*16
		binary.BigEndian.PutUint32(out.Bytes()[entryOff+8:entryOff+12], tableOffset)
		binary.BigEndian.PutUint32(out.Bytes()[entryOff+12:entryOff+16], tableLen)
	}

	return out.Bytes(), nil
}

// executeSingleSubsetTask subsets a single font with Google hb-subset
// (--no-hinting --desubroutinize) + woff2_compress.
func executeSingleSubsetTask(hbBin, woff2Bin string, task fontSubsetTask) (taskResult, error) {
	charCount := len([]rune(task.Chars))
	if charCount == 0 {
		return taskResult{Out: task.Out, CharCount: 0, OutSize: 0}, fmt.Errorf("character set is empty")
	}

	// 1. Create temp files
	tmpDir, err := os.MkdirTemp("", "hb_subset_*")
	if err != nil {
		return taskResult{Out: task.Out}, err
	}
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()

	tmpTTF := filepath.Join(tmpDir, "temp.ttf")

	// 2. Run hb-subset (strip hinting and subroutines: --no-hinting --desubroutinize)
	// The char set goes into a temp file to avoid command-line length limits and escaping issues
	txtFile := filepath.Join(tmpDir, "text.txt")
	if err := os.WriteFile(txtFile, []byte(task.Chars), 0644); err != nil {
		return taskResult{Out: task.Out}, fmt.Errorf("write chars file error: %w", err)
	}

	cmdHB := exec.Command(hbBin, task.Src,
		"--text-file="+txtFile,
		"--no-hinting",
		"--desubroutinize",
		"--layout-features=*",
		"--output-file="+tmpTTF,
	)
	if out, err := cmdHB.CombinedOutput(); err != nil {
		return taskResult{Out: task.Out}, fmt.Errorf("hb-subset failed: %v, output: %s", err, string(out))
	}

	// 2.5 If the task specifies RemapZeroToO, remap the TTF's 0 outline to O's before compression
	if task.RemapZeroToO {
		ttfBytes, err := os.ReadFile(tmpTTF)
		if err == nil {
			if remapped, remapErr := remapGlyphByRebuildingGlyf(ttfBytes); remapErr == nil {
				_ = os.WriteFile(tmpTTF, remapped, 0644)
			} else {
				log.Printf("[%s] remapGlyphByRebuildingGlyf note: %v", task.Out, remapErr)
			}
		}
	}

	// 3. Compress to standard WOFF2 with Google woff2_compress
	cmdWoff2 := exec.Command(woff2Bin, tmpTTF)
	if out, err := cmdWoff2.CombinedOutput(); err != nil {
		return taskResult{Out: task.Out}, fmt.Errorf("woff2_compress failed: %v, output: %s", err, string(out))
	}

	tmpWOFF2 := filepath.Join(tmpDir, "temp.woff2")
	fi, err := os.Stat(tmpWOFF2)
	if err != nil {
		return taskResult{Out: task.Out}, fmt.Errorf("compressed woff2 not found: %w", err)
	}

	// 4. Move to the target output path (ensure the parent dir exists)
	if err := os.MkdirAll(filepath.Dir(task.Out), 0755); err != nil {
		return taskResult{Out: task.Out}, err
	}
	data, err := os.ReadFile(tmpWOFF2)
	if err != nil {
		return taskResult{Out: task.Out}, err
	}
	if err := os.WriteFile(task.Out, data, 0644); err != nil {
		return taskResult{Out: task.Out}, err
	}

	return taskResult{
		Out:       task.Out,
		CharCount: charCount,
		OutSize:   fi.Size(),
	}, nil
}

// executeSubsetTasks runs batch subsetting natively, calling the official Google tools
// concurrently via goroutines.
func executeSubsetTasks(title string, tasks []fontSubsetTask) error {
	if len(tasks) == 0 {
		return nil
	}

	hbBin, woff2Bin, err := findSubsettingTools()
	if err != nil {
		return err
	}

	start := time.Now()
	fmt.Printf("[%s] 开始生成 %d 套字体...\n", title, len(tasks))

	results := make([]taskResult, len(tasks))
	var wg sync.WaitGroup
	var errCount int
	var errMu sync.Mutex

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t fontSubsetTask) {
			defer wg.Done()
			res, err := executeSingleSubsetTask(hbBin, woff2Bin, t)
			if err != nil {
				errMu.Lock()
				errCount++
				errMu.Unlock()
				res.Err = err
			}
			results[idx] = res
		}(i, task)
	}
	wg.Wait()

	var totalSize int64
	for i, res := range results {
		if res.Err != nil {
			log.Printf("[%s] 任务 [%d] 失败 (%s): %v", title, i, tasks[i].Out, res.Err)
			continue
		}
		totalSize += res.OutSize
		outName := filepath.Base(res.Out)
		fmt.Printf("  OK: %-32s (%4d 字符, %6.2f KB)\n",
			outName,
			res.CharCount,
			float64(res.OutSize)/1024.0,
		)
	}

	elapsed := time.Since(start)
	fmt.Printf("[%s] 处理完成 - 耗时: %v - 字体数: %d - 总计: %.2f KB - 失败: %d\n",
		title,
		elapsed.Round(time.Millisecond),
		len(tasks),
		float64(totalSize)/1024.0,
		errCount,
	)

	if errCount > 0 {
		return fmt.Errorf("%d font subset tasks failed", errCount)
	}
	return nil
}

// isWebOwnable reports whether a rune belongs in a web font subset.
func isWebOwnable(r rune) bool {
	if r < 0x7F {
		return true
	}
	if (r >= 0x3000 && r <= 0x9FFF) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFF00 && r <= 0xFFEF) {
		return true
	}
	return strings.ContainsRune("，。、；：？！（）【】《》…—·「」『』“”‘’", r)
}

// parseFontFamilyWithWeight parses "family|weight" or "family|weight|bevl" or "'family'|weight".
func parseFontFamilyWithWeight(val string) (string, string) {
	if val == "" {
		return "", ""
	}
	val = strings.TrimSpace(val)
	parts := strings.Split(val, "|")
	if len(parts) >= 2 {
		fam := strings.Trim(strings.TrimSpace(parts[0]), "'\"")
		weight := strings.TrimSpace(parts[1])
		return fam, weight
	}
	return strings.Trim(val, "'\""), ""
}

// loadFontMap reads config/font_map.json.
func loadFontMap() map[string]fontMapInfo {
	res := make(map[string]fontMapInfo)
	data, err := os.ReadFile(dataReadPath(FileFontMap))
	if err != nil {
		return res
	}
	_ = json.Unmarshal(data, &res)
	return res
}

// loadAllExtraChars reads all role configs from config/font_extra_chars.json.
func loadAllExtraChars() map[string]map[rune]struct{} {
	result := make(map[string]map[rune]struct{})
	data, err := os.ReadFile(dataReadPath(FileFontExtraChars))
	if err != nil {
		return result
	}
	var full map[string]extraCharsConfig
	if err := json.Unmarshal(data, &full); err != nil {
		return result
	}
	for role, cfg := range full {
		chars := make(map[rune]struct{})
		for _, preset := range cfg.Presets {
			binFile := dataReadPath(filepath.Join(DirPresets, preset+".bin"))
			if raw, err := os.ReadFile(binFile); err == nil {
				for i := 0; i+1 < len(raw); i += 2 {
					cp := binary.BigEndian.Uint16(raw[i:])
					chars[rune(cp)] = struct{}{}
				}
			}
		}
		for _, r := range cfg.UnicodeRanges {
			parts := strings.Split(r, "-")
			if len(parts) == 2 {
				start, _ := strconv.ParseInt(strings.TrimPrefix(parts[0], "0x"), 16, 32)
				end, _ := strconv.ParseInt(strings.TrimPrefix(parts[1], "0x"), 16, 32)
				for c := rune(start); c <= rune(end); c++ {
					chars[c] = struct{}{}
				}
			}
		}
		for _, c := range cfg.ExtraChars {
			chars[c] = struct{}{}
		}
		result[role] = chars
	}
	return result
}

// loadExtraChars reads a single role from config/font_extra_chars.json.
func loadExtraChars(role string) map[rune]struct{} {
	all := loadAllExtraChars()
	if chars, ok := all[role]; ok {
		return chars
	}
	return make(map[rune]struct{})
}

// walkUILeaves recursively walks the UI string tree, invoking fn for each text/role leaf.
// Text reaches fn with its effect markers resolved (uitext.go), so every caller collects
// the characters the page really renders rather than the raw ~~ and || markers.
func walkUILeaves(node any, fn func(text, role string)) {
	switch v := node.(type) {
	case map[string]any:
		if text, hasText := v["text"].(string); hasText {
			if role, hasRole := v["role"].(string); hasRole {
				if text != "" && role != "" && role != "native" {
					fn(uiCopyPlain(text), role)
				}
				return
			}
		}
		for _, child := range v {
			walkUILeaves(child, fn)
		}
	case []any:
		for _, child := range v {
			walkUILeaves(child, fn)
		}
	}
}

// getActivePresetFamilyRoles returns the active preset's roles -> family mapping.
func getActivePresetFamilyRoles() (map[string][]string, map[string]string) {
	famRoles := make(map[string][]string)
	roleFam := make(map[string]string)

	cfg := loadAppConfig()
	if cfg.ActiveFontPreset == "" {
		return famRoles, roleFam
	}

	for _, preset := range cfg.FontPresets {
		if preset.ID == cfg.ActiveFontPreset {
			for role, val := range preset.Fonts {
				fam, _ := parseFontFamilyWithWeight(val)
				if fam != "" {
					famRoles[fam] = append(famRoles[fam], role)
					roleFam[role] = fam
				}
			}
			break
		}
	}
	return famRoles, roleFam
}

// setToString converts a map[rune]struct{} into an ordered string.
func setToString(m map[rune]struct{}) string {
	runes := make([]rune, 0, len(m))
	for r := range m {
		runes = append(runes, r)
	}
	sort.Slice(runes, func(i, j int) bool { return runes[i] < runes[j] })
	return string(runes)
}

var (
	lastBodyCharsHash string
	lastBodyCharsMu   sync.Mutex
)

// RunIncrementalFontSubset runs incremental font subsetting (force: skip the hash check).
func RunIncrementalFontSubset(force bool) {
	runFontSubset()
}

// collectSiteRoleChars returns, per font role, every character that role draws.
//
// It is the charset source for the site-wide subset, kept as its own function so a test can
// assert role coverage without invoking hb-subset. The role keys mirror the --font-<role>
// names in src/css; a role that draws text but has no entry here yields a subset that cannot
// draw that text, and the browser silently fills the gap from a fallback family
// — see docs/GOTCHAS.md §font-role-coverage.
func collectSiteRoleChars() map[string]map[rune]struct{} {
	roleChars := make(map[string]map[rune]struct{})
	for role := range map[string]bool{
		"heading": true, "subheading": true, "body": true, "article": true,
		"display_body": true, "display_heading": true, "display_jp": true, "display_i18n": true, "display_i18n_jp": true,
		"display_mono": true, "caption": true, "data": true, "footer": true,
		"mono": true, "serif": true,
		"fun_pill": true, "fun_title": true, "fun_title_sym": true, "fun_desc": true, "fun_btn": true, "fun_reroll": true,
	} {
		roleChars[role] = make(map[rune]struct{})
	}

	// 1. site.json motto + author
	cfg := loadAppConfig()
	for _, c := range cfg.Motto + cfg.HomeMotto {
		roleChars["display_body"][c] = struct{}{}
	}
	for _, c := range cfg.Author {
		roleChars["caption"][c] = struct{}{}
	}

	// 2. Scan the posts the site actually renders. postDirs() is the same gate
	// rebuildPostIndex uses — dot-directories are skipped, and an undeclared directory only
	// counts when it holds a post — so a draft under .draft/ or a .md nested below a category
	// never reaches a subset. The renderer never shows those files, so a glyph for them would
	// be bytes paid for and never drawn.
	for _, d := range postDirs() {
		entries, err := os.ReadDir(d.Path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			content, err := os.ReadFile(filepath.Join(d.Path, e.Name()))
			if err != nil {
				continue
			}
			sContent := string(content)
			bodyText := sContent
			if strings.HasPrefix(sContent, "---\n") {
				parts := strings.SplitN(sContent, "---\n", 3)
				if len(parts) >= 3 {
					bodyText = parts[2]
					for _, line := range strings.Split(parts[1], "\n") {
						line = strings.TrimSpace(line)
						if strings.HasPrefix(line, "title:") {
							// One title, three roles, and each role needs its own glyphs:
							// heading (.article-title — the article page h1), subheading
							// (.post-title cards, .search-title, .home-post-card__title) and
							// body (.ap-title archive rows, .cat-post-title sidebar rows).
							// Collecting fewer roles than the templates render leaves the
							// uncovered role to fall back to a system family, so the title
							// comes out with some characters in a different typeface.
							val := strings.Trim(strings.TrimPrefix(line, "title:"), " \"'")
							for _, c := range val {
								roleChars["heading"][c] = struct{}{}
								roleChars["subheading"][c] = struct{}{}
								roleChars["body"][c] = struct{}{}
							}
						} else if strings.HasPrefix(line, "summary:") || strings.HasPrefix(line, "description:") {
							colonIdx := strings.Index(line, ":")
							val := strings.Trim(line[colonIdx+1:], " \"'")
							for _, c := range val {
								roleChars["body"][c] = struct{}{}
							}
						} else if strings.HasPrefix(line, "categories:") || strings.HasPrefix(line, "category:") ||
							strings.HasPrefix(line, "tags:") || strings.HasPrefix(line, "tag:") {
							colonIdx := strings.Index(line, ":")
							val := strings.Trim(line[colonIdx+1:], " \"'")
							for _, c := range val {
								roleChars["caption"][c] = struct{}{}
							}
						}
					}
				}
			}
			for _, line := range strings.Split(bodyText, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "#") {
					for _, c := range strings.TrimLeft(line, "# \t") {
						roleChars["subheading"][c] = struct{}{}
					}
				} else if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "`") {
					for _, c := range line {
						roleChars["mono"][c] = struct{}{}
					}
				} else {
					for _, c := range line {
						roleChars["article"][c] = struct{}{}
					}
				}
			}
		}
	}

	// 3. UI strings static copy
	if data, err := os.ReadFile(dataReadPath(FileUIStrings)); err == nil {
		var tree any
		if err := json.Unmarshal(data, &tree); err == nil {
			walkUILeaves(tree, func(text, role string) {
				if _, ok := roleChars[role]; !ok {
					roleChars[role] = make(map[rune]struct{})
				}
				for _, c := range text {
					roleChars[role][c] = struct{}{}
				}
			})
		}
	}

	// 4. Dynamic data JSON
	if data, err := os.ReadFile(dataReadPath(FileNavSignatures)); err == nil {
		var sigs []struct {
			Label struct {
				Text string `json:"text"`
			} `json:"label"`
		}
		if err := json.Unmarshal(data, &sigs); err == nil {
			for _, s := range sigs {
				for _, c := range s.Label.Text {
					if isWebOwnable(c) {
						roleChars["caption"][c] = struct{}{}
					}
				}
			}
		}
	}
	for _, dj := range []string{FilePhotoData, FileTopicData, FileSponsorData, FileFriendData} {
		if data, err := os.ReadFile(dj); err == nil {
			var v any
			if err := json.Unmarshal(data, &v); err == nil {
				var extractStrings func(node any)
				extractStrings = func(node any) {
					switch n := node.(type) {
					case map[string]any:
						for k, val := range n {
							if k == "kaomoji" {
								continue
							}
							extractStrings(val)
						}
					case []any:
						for _, item := range n {
							extractStrings(item)
						}
					case string:
						for _, c := range n {
							roleChars["body"][c] = struct{}{}
							if dj == FileSponsorData {
								roleChars["display_i18n"][c] = struct{}{}
								// Kana supply: the MapleMono (display_i18n_jp) subset must include
								// nickname characters (the font itself has no kana glyphs; missing
								// chars would fall back to system fonts)
								roleChars["display_i18n_jp"][c] = struct{}{}
							}
						}
					}
				}
				extractStrings(v)
			}
		}
	}

	// 5. font_extra_chars.json supplementary chars (never mix in cmt; cmt is handled
	// strictly by runCommentFontSubset)
	allExtra := loadAllExtraChars()
	for role, chars := range allExtra {
		if role == "cmt" {
			continue
		}
		if _, ok := roleChars[role]; !ok {
			roleChars[role] = make(map[rune]struct{})
		}
		for c := range chars {
			roleChars[role][c] = struct{}{}
		}
	}

	// 7. Go source string literals -> caption
	for c := range collectGoStringChars() {
		roleChars["caption"][c] = struct{}{}
	}

	return roleChars
}

// runFontSubset runs site-wide body subsetting natively (pure Go char extraction +
// Google hb-subset/woff2_compress tools).
func runFontSubset() {
	go func() {
		if !bodySubsetMu.TryLock() {
			log.Printf("[全站正文子集] 已在进行中，跳过本次")
			return
		}
		defer bodySubsetMu.Unlock()

		famRoles, _ := getActivePresetFamilyRoles()
		fontMap := loadFontMap()
		if len(fontMap) == 0 {
			log.Printf("[全站正文子集] font_map.json 为空，跳过")
			return
		}

		roleChars := collectSiteRoleChars()

		// 8. Build per-font tasks
		var tasks []fontSubsetTask
		for fam, info := range fontMap {
			if info.Src == "" {
				continue
			}
			srcPath := filepath.Join(RootFontSources, info.Src)
			if fi, err := os.Stat(srcPath); err != nil || fi.IsDir() {
				continue
			}
			roles := famRoles[fam]
			if len(roles) == 0 && strings.HasSuffix(fam, "-Medium") {
				baseFam := strings.TrimSuffix(fam, "-Medium")
				roles = famRoles[baseFam]
			}

			famChars := make(map[rune]struct{})
			for _, r := range roles {
				if r == "cmt" {
					continue // the cmt comment charset is generated exclusively by runCommentFontSubset
					// into assets/font/cmt/; it must never pollute site fonts
				}
				for c := range roleChars[r] {
					famChars[c] = struct{}{}
				}
			}

			if len(famChars) == 0 {
				continue
			}

			outPath := filepath.Join(DirFont, fam+".woff2")
			task := fontSubsetTask{
				Src:   srcPath,
				Chars: setToString(famChars),
				Out:   outPath,
			}
			if fam == "MapleMono" {
				task.RemapZeroToO = true
			}
			tasks = append(tasks, task)
		}

		if err := executeSubsetTasks("全站正文子集", tasks); err != nil {
			log.Printf("[全站正文子集] 生成失败: %v", err)
			return
		}
	}()
}

// collectLatestPostText finds the latest post's title and summary.
// (Category characters do not go through this function: category pills and the nav
// dropdown render from getCategories() display names; the subset must share the same
// source as the renderer — see runHomeSubsetAndWBN step 5.)
func collectLatestPostText() (string, string) {
	var latestDate string
	var latestID int
	var latestTitle, latestSummary string
	var latestMTime time.Time

	// Same directory gate as the post index: the "latest post" must be one the site actually
	// renders, otherwise the home page reserves glyphs for a file no page shows.
	for _, d := range postDirs() {
		entries, err := os.ReadDir(d.Path)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			content, err := os.ReadFile(filepath.Join(d.Path, e.Name()))
			if err != nil {
				continue
			}
			s := string(content)
			if !strings.HasPrefix(s, "---\n") {
				continue
			}
			parts := strings.SplitN(s, "---\n", 3)
			if len(parts) < 2 {
				continue
			}
			var dateStr, title, summary string
			var postID int
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "date:") || strings.HasPrefix(line, "created_at:") {
					colonIdx := strings.Index(line, ":")
					dateStr = strings.Trim(line[colonIdx+1:], " \"'\t")
				} else if strings.HasPrefix(line, "title:") {
					title = strings.Trim(strings.TrimPrefix(line, "title:"), " \"'\t")
				} else if strings.HasPrefix(line, "id:") {
					postID, _ = strconv.Atoi(strings.Trim(strings.TrimPrefix(line, "id:"), " \"'\t"))
				} else if strings.HasPrefix(line, "summary:") || strings.HasPrefix(line, "description:") {
					colonIdx := strings.Index(line, ":")
					summary = strings.Trim(line[colonIdx+1:], " \"'\t")
				}
			}
			if dateStr == "" {
				dateStr = fi.ModTime().Truncate(time.Second).Format(time.RFC3339)
			}
			mtime := fi.ModTime()
			isNewer := false
			if latestDate == "" {
				isNewer = true
			} else if dateStr > latestDate {
				isNewer = true
			} else if dateStr == latestDate {
				if postID < latestID || (latestID == 0 && mtime.After(latestMTime)) {
					isNewer = true
				}
			}
			if isNewer {
				latestDate = dateStr
				latestID = postID
				latestTitle = title
				latestSummary = summary
				latestMTime = mtime
			}
		}
	}
	return latestTitle, latestSummary
}

// homeSubsetRoles is the role set the home-page subset collects. It is a subset of the
// site-wide set by construction — the home page is one page of the same site — so a role
// added here but not there would render correctly on / and fall back to a system family
// everywhere else. TestHomeRolesAreASubsetOfSiteRoles pins that.
var homeSubsetRoles = map[string]bool{
	"heading": true, "subheading": true, "display_heading": true,
	"display_body": true, "display_mono": true, "display_jp": true,
	"body": true, "caption": true, "data": true, "footer": true, "mono": true,
	"article": true, "serif": true,
}

// runHomeSubsetAndWBN runs the home-page font subset natively.
func runHomeSubsetAndWBN() {
	go func() {
		if !homeSubsetMu.TryLock() {
			log.Printf("[首页极简子集] 已在进行中，跳过本次")
			return
		}
		defer homeSubsetMu.Unlock()

		famRoles, _ := getActivePresetFamilyRoles()
		fontMap := loadFontMap()
		if len(fontMap) == 0 {
			return
		}

		homeRoles := homeSubsetRoles

		roleChars := make(map[string]map[rune]struct{})
		for r := range homeRoles {
			roleChars[r] = make(map[rune]struct{})
		}

		// 1. Config.json home_motto, author
		cfg := loadAppConfig()
		for _, c := range cfg.HomeMotto {
			roleChars["display_body"][c] = struct{}{}
		}
		for _, c := range cfg.Author {
			roleChars["caption"][c] = struct{}{}
			roleChars["display_heading"][c] = struct{}{}
		}
		// 2. Dynamic nav signatures (e.g. "/" -> the home signature string)
		if data, err := os.ReadFile(dataReadPath(FileNavSignatures)); err == nil {
			var sigs []struct {
				Label struct {
					Text string `json:"text"`
				} `json:"label"`
			}
			if err := json.Unmarshal(data, &sigs); err == nil {
				for _, s := range sigs {
					for _, c := range s.Label.Text {
						if isWebOwnable(c) {
							roleChars["caption"][c] = struct{}{}
						}
					}
				}
			}
		}

		// 3. UI strings: scan home-related modules
		if data, err := os.ReadFile(dataReadPath(FileUIStrings)); err == nil {
			var root map[string]any
			if err := json.Unmarshal(data, &root); err == nil {
				homeBlocks := []string{"home", "nav", "footer", "profile", "cat_scheme", "theme", "common"}
				for _, block := range homeBlocks {
					if subTree, ok := root[block]; ok {
						walkUILeaves(subTree, func(text, role string) {
							if homeRoles[role] {
								for _, c := range text {
									roleChars[role][c] = struct{}{}
								}
							}
						})
					}
				}
			}
		}

		// 4. Home-displayed posts (pinned + latest: titles, summaries, tags)
		allPosts := loadPostsFromDB()
		if len(allPosts) == 0 {
			backfillPosts()
			allPosts = loadPostsFromDB()
		}

		var homePosts []*Post
		if cfg.PinnedPostID > 0 {
			if p := idxByID(cfg.PinnedPostID); p != nil {
				homePosts = append(homePosts, p)
			}
		}
		for i := range allPosts {
			if cfg.PinnedPostID <= 0 || allPosts[i].ID != cfg.PinnedPostID {
				homePosts = append(homePosts, &allPosts[i])
				break
			}
		}

		if len(homePosts) == 0 {
			title, summary := collectLatestPostText()
			if title != "" {
				for _, c := range title {
					roleChars["heading"][c] = struct{}{}
					roleChars["subheading"][c] = struct{}{}
				}
			}
			if summary != "" {
				for _, c := range summary {
					roleChars["body"][c] = struct{}{}
					roleChars["article"][c] = struct{}{}
				}
			}
		} else {
			for _, p := range homePosts {
				for _, c := range p.Title {
					roleChars["heading"][c] = struct{}{}
					roleChars["subheading"][c] = struct{}{}
				}
				for _, c := range p.SummaryStr {
					roleChars["body"][c] = struct{}{}
					roleChars["article"][c] = struct{}{}
				}
				for _, tag := range p.Tags {
					for _, c := range tag {
						roleChars["body"][c] = struct{}{}
						roleChars["caption"][c] = struct{}{}
						roleChars["subheading"][c] = struct{}{}
					}
				}
			}
		}

		// 5. Every category display name -> inject into body/heading/caption roles
		for _, name := range getCategories() {
			for _, c := range name {
				roleChars["caption"][c] = struct{}{}
				roleChars["body"][c] = struct{}{}
				roleChars["subheading"][c] = struct{}{}
			}
		}

		// 6. font_extra_chars.json supplementary chars (symbols, unicode ranges, etc.)
		allExtra := loadAllExtraChars()
		for role, chars := range allExtra {
			if role == "cmt" || !homeRoles[role] {
				continue
			}
			for c := range chars {
				roleChars[role][c] = struct{}{}
			}
		}

		var tasks []fontSubsetTask
		for fam, roles := range famRoles {
			info, ok := fontMap[fam]
			if !ok || info.Src == "" {
				continue
			}
			srcPath := filepath.Join(RootFontSources, info.Src)
			if fi, err := os.Stat(srcPath); err != nil || fi.IsDir() {
				continue
			}

			var validRoles []string
			for _, r := range roles {
				if homeRoles[r] {
					validRoles = append(validRoles, r)
				}
			}
			if len(validRoles) == 0 {
				continue
			}

			famChars := make(map[rune]struct{})
			for _, r := range validRoles {
				for c := range roleChars[r] {
					famChars[c] = struct{}{}
				}
			}

			if len(famChars) == 0 {
				continue
			}

			outPath := filepath.Join(DirFont, "home", fam+"-home.woff2")
			tasks = append(tasks, fontSubsetTask{
				Src:   srcPath,
				Chars: setToString(famChars),
				Out:   outPath,
			})
		}

		if err := executeSubsetTasks("首页极简子集", tasks); err != nil {
			log.Printf("[首页极简子集] 生成失败: %v", err)
			return
		}
	}()
}

// runAdminFontSubset runs the admin-page font subset natively.
func runAdminFontSubset() {
	go func() {
		if !adminSubsetMu.TryLock() {
			log.Printf("[管理后台隔离子集] 已在进行中，跳过本次")
			return
		}
		defer adminSubsetMu.Unlock()

		fontMap := loadFontMap()
		if len(fontMap) == 0 {
			return
		}

		adminChars := make(map[rune]struct{})
		// Preview fixtures go through walkUILeaves (same path as ui_strings) — they share
		// the same source as the admin preview rendering, so any char the preview can show
		// automatically joins the admin subset; the invariant holds by construction.
		// Do NOT fold these into the raw byte scan below: fixtures written as \uXXXX
		// escapes would be invisible to a byte scan.
		for _, fn := range []string{dataReadPath(FileUIStringsAdmin), dataReadPath(FileUIStrings), dataReadPath(FilePreviewFixtures)} {
			if data, err := os.ReadFile(fn); err == nil {
				var tree any
				if err := json.Unmarshal(data, &tree); err == nil {
					walkUILeaves(tree, func(text, role string) {
						for _, c := range text {
							if isWebOwnable(c) {
								adminChars[c] = struct{}{}
							}
						}
					})
				}
			}
		}

		// 2. Scan admin templates and their sub-templates (scenario previews, article
		//    paragraphs, code, comments, lalafell copy, etc.)
		//    Includes the status page (status.html template + status.js script): its
		//    dropdowns/buttons/labels hardcode Chinese. Without collecting them, a missing
		//    glyph (e.g. "昨" in "昨日") falls back to system fonts and misaligns the menu
		//    left edge.
		for _, tpl := range []string{
			templatePath("admin.html"),
			templatePath("hero_section.html"),
			templatePath("fun_error_stage.html"),
			templatePath("status.html"),
			srcReadPath("js/status.js"),
		} {
			if data, err := os.ReadFile(tpl); err == nil {
				for _, c := range string(data) {
					if isWebOwnable(c) {
						adminChars[c] = struct{}{}
					}
				}
			}
		}

		charsStr := setToString(adminChars)
		var tasks []fontSubsetTask
		for fam, info := range fontMap {
			if info.Src == "" {
				continue
			}
			srcPath := filepath.Join(RootFontSources, info.Src)
			if fi, err := os.Stat(srcPath); err != nil || fi.IsDir() {
				continue
			}
			outPath := filepath.Join(DirFont, "admin", fam+"-admin.woff2")
			tasks = append(tasks, fontSubsetTask{
				Src:   srcPath,
				Chars: charsStr,
				Out:   outPath,
			})
		}

		if err := executeSubsetTasks("管理后台隔离子集", tasks); err != nil {
			log.Printf("[管理后台隔离子集] 生成失败: %v", err)
			return
		}
	}()
}

// commentBlocks are the ui_strings modules the comment area renders, and commentTemplates
// are the templates that render them. The list exists because a copy leaf's role names
// another family — see docs/GOTCHAS.md §ui-copy-effect-markers.
var (
	commentBlocks    = []string{"comment", "common", "guestbook"}
	commentTemplates = []string{
		"comment_form.html", "comment_fragment.html", "guestbook.html", "guestbook_fragment.html",
	}
)

// collectCommentCopyChars adds the characters of the comment area's copy to chars. Effect
// markers are already resolved by walkUILeaves, so a struck run contributes its U+0336.
func collectCommentCopyChars(root map[string]any, chars map[rune]struct{}) {
	for _, block := range commentBlocks {
		sub, ok := root[block]
		if !ok {
			continue
		}
		walkUILeaves(sub, func(text, _ string) {
			for _, c := range text {
				chars[c] = struct{}{}
			}
		})
	}
}

// runCommentFontSubset generates the comment subset for the given font natively
// (GB2312 common-character library).
func runCommentFontSubset(cmtVal string) {
	go func() {
		if !fontSubsetMu.TryLock() {
			log.Printf("[评论区常用字库] 已在进行中，跳过本次")
			return
		}
		defer fontSubsetMu.Unlock()

		fontMap := loadFontMap()
		if len(fontMap) == 0 {
			return
		}

		cmtChars := loadExtraChars("cmt")
		if data, err := os.ReadFile(dataReadPath(FileUIStrings)); err == nil {
			var root map[string]any
			if err := json.Unmarshal(data, &root); err == nil {
				collectCommentCopyChars(root, cmtChars)
			}
		}
		charsStr := setToString(cmtChars)

		var families []string
		if cmtVal == "--all" || cmtVal == "" {
			for fam := range fontMap {
				families = append(families, fam)
			}
		} else {
			fam, _ := parseFontFamilyWithWeight(cmtVal)
			if fam != "" {
				families = append(families, fam)
			}
		}

		var tasks []fontSubsetTask
		for _, fam := range families {
			info, ok := fontMap[fam]
			if !ok || info.Src == "" {
				continue
			}
			srcPath := filepath.Join(RootFontSources, info.Src)
			if fi, err := os.Stat(srcPath); err != nil || fi.IsDir() {
				continue
			}
			outPath := filepath.Join(DirFont, "cmt", fam+"-cmt.woff2")
			tasks = append(tasks, fontSubsetTask{
				Src:   srcPath,
				Chars: charsStr,
				Out:   outPath,
			})
		}

		if err := executeSubsetTasks("评论区常用字库", tasks); err != nil {
			log.Printf("[评论区常用字库] 生成失败: %v", err)
			return
		}
	}()
}

// initCommentFontSubset checks the active comment font and pre-generates its subset at startup.
func initCommentFontSubset() {
	cfg := loadAppConfig()
	if cfg.ActiveFontPreset == "" {
		return
	}
	for _, p := range cfg.FontPresets {
		if p.ID == cfg.ActiveFontPreset {
			if cmtVal := strings.TrimSpace(p.Fonts["cmt"]); cmtVal != "" {
				runCommentFontSubset(cmtVal)
			}
			return
		}
	}
}

// initAdminFontSubset pre-generates the admin subset at startup.
func initAdminFontSubset() {
	runAdminFontSubset()
}

// initHomeSubsetAndWBN pre-generates the home subset at startup.
func initHomeSubsetAndWBN() {
	runHomeSubsetAndWBN()
}

// initFontSubset pre-generates the site-wide body subset at startup.
func initFontSubset() {
	runFontSubset()
}
