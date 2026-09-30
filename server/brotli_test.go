package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requireBrotliForTest fails with the reason rather than letting an assertion below report a
// missing file: the pre-compressor is a deployment prerequisite, not a code path.
func requireBrotliForTest(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath(brotliBinary()); err != nil {
		t.Fatalf("the served tree is pre-compressed with %q and it is not installed: %v", brotliBinary(), err)
	}
}

// decompressBR returns what the compressor wrote beside path, so a test can compare content
// rather than only presence.
func decompressBR(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command(brotliBinary(), "-d", "-c", path+brotliSuffix).Output()
	if err != nil {
		t.Fatalf("decompress %s%s: %v", path, brotliSuffix, err)
	}
	return string(out)
}

func TestWriteBRCompressesTextAndLeavesTheSource(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "core.bundle.css")
	body := "body{color:#fff}" + string(make([]byte, 0))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := writeBR(path); err != nil {
		t.Fatalf("writeBR: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the source was removed: %v", err)
	}
	if got := decompressBR(t, path); got != body {
		t.Fatalf("the .br does not decode to the source: %q", got)
	}
}

// A second compression of changed content must replace the first one. Without -f the
// compressor refuses to overwrite, and the stale .br would then be served in preference to
// the file it no longer matches.
func TestWriteBRReplacesAnExistingArtifact(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.js")
	for _, body := range []string{"var a=1;", "var a=2;var b=3;"} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeBR(path); err != nil {
			t.Fatalf("writeBR: %v", err)
		}
		if got := decompressBR(t, path); got != body {
			t.Fatalf("the .br still holds the previous content: %q", got)
		}
	}
}

func TestWriteBRSkipsWhatIsNotText(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	for _, name := range []string{"shot.avif", "font.woff2", "clip.gif", "noext"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("binary"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := writeBR(path); err != nil {
			t.Fatalf("writeBR(%s): %v", name, err)
		}
		if _, err := os.Stat(path + brotliSuffix); !os.IsNotExist(err) {
			t.Errorf("%s was compressed; only text is worth it", name)
		}
	}
}

func TestWriteServedFileProducesBoth(t *testing.T) {
	requireBrotliForTest(t)
	path := filepath.Join(t.TempDir(), "index.html")
	body := "<!DOCTYPE html><p>hi</p>"
	if err := writeServedFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writeServedFile: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != body {
		t.Fatalf("source = %q, %v", raw, err)
	}
	if got := decompressBR(t, path); got != body {
		t.Fatalf(".br = %q", got)
	}
}

// A served write publishes by rename, so no name is ever observable while its content is still
// being written. The temporary it goes through is dot-prefixed, which is what keeps it out of
// every walk that lists served files and out of the asset manifest, which gives URLs.
func TestServedWriteLeavesNoTemporaryName(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	body := "<!DOCTYPE html><html><body>hi</body></html>"
	if err := writeServedFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writeServedFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Anti-vacuity: the two names the write publishes have to be there, or an empty directory
	// would satisfy the assertion below for the wrong reason.
	if len(entries) == 0 {
		t.Fatal("the directory is empty — the write published nothing, so there is nothing to check")
	}
	if _, err := os.Stat(path + brotliSuffix); err != nil {
		t.Fatalf("the artifact was not published: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) && e.Name() != filepath.Base(path)+brotliSuffix {
			t.Errorf("%s was left behind: a served write publishes by rename and leaves no other name", e.Name())
		}
	}
}

// A .br that no longer describes its source is served in preference to the source, so a
// compressor that fails has to leave no artifact behind at all: without one nginx falls back to
// the source, and the bytes are merely larger.
func TestABrokenCompressorLeavesNoArtifact(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "app.js")
	if err := os.WriteFile(path, []byte("var a=1;"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeBR(path); err != nil {
		t.Fatalf("writeBR: %v", err)
	}
	// Anti-vacuity: the artifact has to exist and be a real stream before the failure, or
	// "there is no artifact afterwards" would hold for the wrong reason.
	if info, err := os.Stat(path + brotliSuffix); err != nil || info.Size() == 0 {
		t.Fatalf("no usable artifact to begin with: %v", err)
	}

	// A compressor that always exits non-zero. It has to be a real executable: an absent one
	// is a different failure, and one that never reaches the artifact.
	failing, err := exec.LookPath("false")
	if err != nil {
		t.Fatalf("no `false` on this host to stand in for a broken compressor: %v", err)
	}
	t.Setenv("SILPHUU_BROTLI", failing)
	if err := writeBR(path); err == nil {
		t.Fatal("the compressor failed and writeBR reported success")
	}
	if _, err := os.Stat(path + brotliSuffix); !os.IsNotExist(err) {
		t.Error("a stale artifact survived a failed compression, and nginx serves it in preference to the source")
	}
}

// The artifact beside a file is either a complete stream of that file or nothing at all. The two
// states that are neither are repaired at startup: a brotli stream is never zero bytes, and an
// artifact older than its source describes content that is gone.
func TestRepairBRRegeneratesUnusableArtifacts(t *testing.T) {
	requireBrotliForTest(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.css")
	empty := filepath.Join(dir, "empty.css")
	stale := filepath.Join(dir, "stale.css")
	inStep := filepath.Join(dir, "in-step.css")
	const body = "a{color:red}"
	for _, p := range []string{missing, empty, stale, inStep} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(empty+brotliSuffix, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale+brotliSuffix, []byte("not a stream"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale+brotliSuffix, old, old); err != nil {
		t.Fatal(err)
	}
	if err := writeBR(inStep); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(inStep + brotliSuffix)
	if err != nil {
		t.Fatal(err)
	}

	// Anti-vacuity: both damaged artifacts have to be in the state the repair looks for.
	if info, err := os.Stat(empty + brotliSuffix); err != nil || info.Size() != 0 {
		t.Fatalf("the empty artifact is not empty, so there is nothing for the repair to fix: %v", err)
	}
	if info, err := os.Stat(stale + brotliSuffix); err != nil || !info.ModTime().Before(old.Add(time.Minute)) {
		t.Fatalf("the stale artifact is not older than its source: %v", err)
	}

	repairBR(dir)

	if got := decompressBR(t, missing); got != body {
		t.Errorf("the missing artifact was not filled in: %q", got)
	}
	if got := decompressBR(t, empty); got != body {
		t.Errorf("the empty artifact was not regenerated: %q", got)
	}
	if got := decompressBR(t, stale); got != body {
		t.Errorf("the stale artifact was not regenerated: %q", got)
	}
	after, err := os.Stat(inStep + brotliSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an artifact already in step was rewritten; the whole tree would be re-compressed on every start")
	}
}

// The class the served tree has to hold to: after a build, every .br is a complete stream and
// none of them is older than its source.
//
// nginx serves a .br on existence alone, so an empty one reaches a client as a 200 with no body
// and an older one reaches it as the previous content. Both are checked over the tree a build
// just produced, so a writer that publishes the pair apart fails here rather than at the edge.
func TestNoUnusableBrotliArtifactInTheBuiltTree(t *testing.T) {
	requireBrotliForTest(t)
	useExampleSite(t)
	rebuildPostIndex()
	if err := concatAllBundles(); err != nil {
		t.Fatalf("concatAllBundles: %v", err)
	}
	if err := generateStaticErrorPages(); err != nil {
		t.Fatalf("generateStaticErrorPages: %v", err)
	}
	generateServedRootFiles("test")
	page := cachePath("/about")
	if err := writeServedFile(page, []byte("<!DOCTYPE html><html><body>about</body></html>"), 0o644); err != nil {
		t.Fatalf("writeServedFile: %v", err)
	}

	artifacts := 0
	for _, root := range []string{RootAssets, RootRender} {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !isBrotliArtifact(info.Name()) {
				return nil
			}
			source := strings.TrimSuffix(path, brotliSuffix)
			srcInfo, statErr := os.Stat(source)
			if statErr != nil {
				return nil // a link whose target lives on another node is absent here by design
			}
			artifacts++
			if info.Size() == 0 {
				t.Errorf("%s is empty: nginx serves it as a 200 with no body", path)
			}
			if info.ModTime().Before(srcInfo.ModTime()) {
				t.Errorf("%s is older than %s: it describes content that is gone", path, source)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// Anti-vacuity: with no artifact under either root the walk asserts nothing.
	if artifacts == 0 {
		t.Fatal("the built tree holds no .br at all — the guard checked nothing")
	}
}

// The asset manifest is what gives a file a URL. A .br that got one would be served as
// compressed bytes under a name that promises the uncompressed type.
func TestAssetManifestSkipsBrotliArtifacts(t *testing.T) {
	requireBrotliForTest(t)
	site, _ := withIsolatedRoots(t)
	if err := os.MkdirAll(filepath.Join(RootAssets, "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeServedFile(filepath.Join(RootAssets, "css", "core.bundle.css"), []byte("a{color:red}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = site

	if err := initAssetManifest(); err != nil {
		t.Fatalf("initAssetManifest: %v", err)
	}
	manifest := assetManifest.Load()
	if manifest == nil {
		t.Fatal("asset manifest was never published")
	}
	if _, ok := (*manifest)["css/core.bundle.css"]; !ok {
		t.Error("the bundle itself is missing from the manifest")
	}
	for logical := range *manifest {
		if isBrotliArtifact(logical) {
			t.Errorf("%s got a URL; a derived file is not addressable", logical)
		}
	}
}
