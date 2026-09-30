package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pre-compression: the served tree carries a .br beside every text file, and nginx answers
// with it (brotli_static) instead of compressing per request. The level is the highest one,
// which is affordable because the cost is paid once per file rather than once per request.
//
// The compressor is an external command rather than a linked implementation, so its absence
// is a deployment fact the engine has to check for. requireBrotli refuses to start without
// it: an engine that silently serves uncompressed text looks healthy and costs every visitor
// the same bytes over and over, with nothing to point at.
//
// nginx serves a .br on existence alone, with no size or freshness check, so the name must
// never be observable while its content is still being written. Every writer here publishes
// through a temporary name and a rename for that reason.

// brotliBinary is the compressor's name or path. A deployment that installs it outside PATH
// names it here.
func brotliBinary() string {
	if bin := os.Getenv("SILPHUU_BROTLI"); bin != "" {
		return bin
	}
	return "brotli"
}

// brotliQuality is the compression level. 11 is the maximum the format defines.
const brotliQuality = "11"

// brotliCompressible lists the extensions worth pre-compressing. It has to stay in step with
// brotli_types in the deployment's nginx config: nginx only serves the .br for a type it is
// told about, so a file compressed here and absent there is bytes on disk that nobody sends.
var brotliCompressible = map[string]bool{
	".html": true,
	".css":  true,
	".js":   true,
	".mjs":  true,
	".json": true,
	".svg":  true,
	".xml":  true,
	".txt":  true,
}

// brotliSuffix is what the compressor appends, and what every walk over the served tree has
// to recognise as a derived file rather than content.
const brotliSuffix = ".br"

// brotliWanted reports whether a served name is one this tree pre-compresses.
//
// The decision is made on the name the deployment serves, never on a temporary name: a
// temporary carries the write's own suffix, so asking about it would answer no for every file.
func brotliWanted(path string) bool {
	return brotliCompressible[strings.ToLower(filepath.Ext(path))]
}

// requireBrotli refuses to start when the compressor is missing.
func requireBrotli() {
	bin := brotliBinary()
	path, err := exec.LookPath(bin)
	if err != nil {
		log.Fatalf("brotli: %q not found in PATH; the served tree cannot be pre-compressed "+
			"(install it, or name it with SILPHUU_BROTLI)", bin)
	}
	log.Printf("brotli: pre-compressing the served tree with %s (quality %s)", path, brotliQuality)
}

// writeBR pre-compresses one served file into the .br beside it. A file whose extension is not
// worth compressing is left alone rather than reported: the callers write whatever the served
// tree holds, and only some of it is text.
func writeBR(path string) error {
	if !brotliWanted(path) {
		return nil
	}
	return compressFile(path, path+brotliSuffix)
}

// compressFile pre-compresses src into dst.
//
// The compressor writes to a temporary name in dst's directory, which is renamed over dst, so
// dst is either the previous complete stream or the new one and never a prefix of the new one.
// A name that exists while its content is still being written is served as a 200 with an empty
// body, and an edge caches that for as long as the page's policy says.
//
// A failure removes dst. A .br that no longer describes its source is served in preference to
// the source itself, and nothing downstream can tell that it is stale.
func compressFile(src, dst string) error {
	f, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	out, err := exec.Command(brotliBinary(), "-q", brotliQuality, "-f", "-o", tmp, src).CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		os.Remove(dst)
		return fmt.Errorf("brotli %s: %w (%s)", src, err, strings.TrimSpace(string(out)))
	}
	// The artifact carries the source's mtime to the nanosecond. nginx builds its ETag from
	// the mtime and the length of the file it serves, and repairBR reads that pair back; the
	// compressor itself copies only whole seconds.
	if info, statErr := os.Stat(src); statErr == nil {
		_ = os.Chtimes(tmp, info.ModTime(), info.ModTime())
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// writeServedFile writes body into the served tree and publishes the pre-compressed companion
// beside it in the same call. Every writer of served text goes through here, so a file and its
// .br are never produced apart: a .br left over from a previous content is served in
// preference to the file itself, and nothing downstream can tell that it is stale.
//
// Both names are published by a rename from a temporary name in the same directory, so neither
// is ever observable half-written, and the rename stays within one filesystem. The artifact is
// renamed into place before the source, so a process that dies between the two renames leaves
// the newer bytes under the name nginx prefers.
//
// The temporary name is dot-prefixed, which is what every walk over the served tree already
// treats as derived rather than content — see isDerivedServedName.
//
// The write holds the served-tree lease, so the invalidation worker cannot fingerprint a file
// while it is being replaced.
func writeServedFile(path string, body []byte, mode os.FileMode) error {
	beginServedWrite()
	defer endServedWrite()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	var brErr error
	if brotliWanted(path) {
		brErr = compressFile(tmp, path+brotliSuffix)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// A failed artifact is logged rather than returned: the source is published and correct,
	// nginx falls back to it, and the fingerprint covers the artifact — so the URL is purged
	// and the next write retries the pair. Only failing to publish the source is an error.
	if brErr != nil {
		log.Printf("brotli warn: %v", brErr)
	}
	return nil
}

// isBrotliArtifact reports whether a name is a derived .br rather than a file of its own.
func isBrotliArtifact(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), brotliSuffix)
}

// isDerivedServedName reports whether a name under the served tree is produced by the engine
// rather than content: the pre-compressed companion beside a file, and the temporary names a
// served write goes through. A walk that lists served files has to skip both.
func isDerivedServedName(name string) bool {
	return strings.HasPrefix(name, ".") || isBrotliArtifact(name)
}

// repairBR compresses every served text file whose .br is missing or does not describe it.
//
// The writers above keep the pair together from now on, so this only has work to do on a tree
// written before they existed, or on one whose artifact was damaged. An artifact already in
// step is left alone: rewriting it would re-compress the whole tree on every start for no
// change in content.
//
// The repair holds the served-tree lease: it rewrites many files, and a fingerprint taken
// across it would describe a generation of artifacts that never existed.
func repairBR(roots ...string) {
	beginServedWrite()
	defer endServedWrite()
	repaired, failed := 0, 0
	for _, root := range roots {
		filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if isDerivedServedName(info.Name()) || !brotliWanted(path) {
				return nil
			}
			if !brNeedsRepair(path, info) {
				return nil
			}
			if err := writeBR(path); err != nil {
				log.Printf("[brotli warn] %v", err)
				failed++
				return nil
			}
			repaired++
			return nil
		})
	}
	if repaired > 0 || failed > 0 {
		log.Printf("brotli: repaired %d .br (%d failed)", repaired, failed)
	}
}

// brNeedsRepair reports whether the artifact beside a served file is unusable.
//
// Both conditions are structural rather than tuned: a brotli stream is never zero bytes, and
// an artifact older than its source describes content that is gone. The pair is comparable
// because compressFile stamps the artifact with the source's own mtime.
func brNeedsRepair(path string, srcInfo os.FileInfo) bool {
	info, err := os.Stat(path + brotliSuffix)
	if err != nil {
		return true // no artifact at all
	}
	if info.Size() == 0 {
		return true
	}
	return info.ModTime().Before(srcInfo.ModTime())
}
