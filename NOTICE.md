# Third-Party Notices

This document lists the third-party code and assets used in this repository and their applicable licenses.

Two kinds of third-party dependencies reach a deployment:

- **Front-end scripts**: Fetched dynamically at build time and bundled into the final assets. To satisfy open-source redistribution requirements (such as the MIT clause to include notices in all copies), their full license texts are listed below. The build pipeline automatically injects these notices into the headers of the bundled files.
- **Go module and Rust crate dependencies**: Since this project is distributed as source only, backend dependencies are managed via package managers. Their licenses travel with the native modules and are pinned by `go.mod` and `Cargo.lock`, so they are not duplicated here.

*Note: The project's own icons (`server/src/icon/*.svg`) are original artwork. The engine does not ship any typefaces; deployments supply their own fonts.*

---

## fzstd

- **Files**: `server/src/js/vendor/fzstd.min.js`
- **Version**: 0.1.1
- **Upstream**: <https://github.com/101arrowz/fzstd>
- **Licence**: MIT

Used by the status page to decompress zstd-compressed telemetry history.

```
MIT License

Copyright (c) 2020 Arjun Barrett

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

## htmx

- **Files**: `server/src/js/vendor/htmx.min.js`, `server/src/js/vendor/hx-head.min.js`,
  `server/src/js/vendor/hx-preload.min.js`
- **Version**: 4.0.0. Nothing else has shipped on the 4.x line, and npm's `latest`
  dist-tag still points at 2.0.11, which is why the fetch URLs are pinned to `@4`.
- **Upstream**: <https://htmx.org>
- **Licence**: Zero-Clause BSD (0BSD)

0BSD places no condition on redistribution, so no notice is required. It is listed
anyway so the provenance of a minified file is not a mystery to whoever finds it.

## marked

- **Files**: `server/src/js/vendor/marked.umd.js`
- **Version**: 18.x. marked ships no `.min.js` at 18.x, and jsDelivr's unversioned path
  serves a stale v15, so the URL pins the major and takes the UMD build.
- **Upstream**: <https://github.com/markedjs/marked>
- **Licence**: MIT

Renders Markdown in the editor preview.

```
Copyright (c) 2018+, MarkedJS (https://github.com/markedjs/)
Copyright (c) 2011-2018, Christopher Jeffrey (https://github.com/chjj/)

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

## Prism

- **Files**: `server/src/js/vendor/prism.min.js` and the ten grammar components
  `prism-{go,bash,json,rust,python,zig,yaml,toml,nginx}.min.js`
- **Version**: 1.30.0
- **Upstream**: <https://prismjs.com>
- **Licence**: MIT

Syntax highlighting in the editor and in rendered code blocks.

Note: the minified build jsDelivr serves carries **no** copyright line of its own — its
Terser pass drops it. That is precisely why the text below has to live here.

```
MIT LICENSE

Copyright (c) 2012 Lea Verou

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
```

## DOMPurify

- **Files**: `server/src/js/vendor/purify.min.js`
- **Version**: 3.4.15
- **Upstream**: <https://github.com/cure53/DOMPurify>
- **Licence**: Apache-2.0 **or** MPL-2.0, at your option

Sanitises editor output before it reaches the DOM.

DOMPurify is dual-licensed and this project relies on the **Apache-2.0** branch. The full
text is the standard Apache License 2.0 reproduced in `LICENSE-APACHE`, which ships with
the repository — so the licence travels with the copy without being duplicated here.

Unlike marked and Prism, this file keeps its own notice: DOMPurify writes it with the
`/*!` marker, which `isPreservedComment` (`server/strip_comments.go`) deliberately keeps.
