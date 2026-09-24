package main

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/tdewolff/minify/v2"
	"github.com/tdewolff/minify/v2/css"
	"github.com/tdewolff/minify/v2/html"
	"github.com/tdewolff/minify/v2/js"
)

// bundleMinifier is the in-process minifier and the only compression pipeline:
// rendered HTML (cache.go) plus generated JS/CSS bundles (concat.go) all go
// through it.
var bundleMinifier = func() *minify.M {
	m := minify.New()
	m.Add("text/css", &css.Minifier{})
	m.Add("text/javascript", &js.Minifier{})
	m.Add("application/javascript", &js.Minifier{})
	// KeepDocumentTags + KeepEndTags: without them the minifier drops the optional
	// </body></html>, and the cache completeness check (trailing </html>) would
	// reject every minified page, so nothing could ever be warmed into public/
	// — see docs/GOTCHAS.md §html-minify-endtags.
	m.Add("text/html", &html.Minifier{KeepDocumentTags: true, KeepEndTags: true})
	return m
}()

// minifyHTMLString minifies a rendered HTML page or HTMX fragment.
func minifyHTMLString(src string) (string, error) {
	var buf bytes.Buffer
	if err := bundleMinifier.Minify("text/html", &buf, strings.NewReader(src)); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// minifyBundle minifies a generated bundle body by extension (".css" / ".js").
// Output is not source-readable (JS locals get renamed), see docs/GOTCHAS.md §gen-bundle-unreadable.
// On error the caller keeps the unminified content and logs — the bundle stays
// correct, just larger.
func minifyBundle(content, ext string) (string, error) {
	var mt string
	switch ext {
	case ".css":
		mt = "text/css"
	case ".js":
		mt = "text/javascript"
	default:
		return "", fmt.Errorf("unsupported bundle type %q", ext)
	}
	var buf bytes.Buffer
	if err := bundleMinifier.Minify(mt, &buf, strings.NewReader(content)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
