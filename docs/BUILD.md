# Building and running

Silphuu is a single Go binary. Site content lives outside it and is pointed at with `-dir`,
so upgrading the engine means replacing one binary and leaving the site directory alone.
[SITE.md](./SITE.md) describes that directory: its layout, and how a site's own files replace the
shipped ones.

## Requirements

Go and `brotli` are required, and the engine exits at startup when either is missing — see
`go.mod` for the Go version. `brotli` pre-compresses the served tree, and the engine refuses to
run without it: a server that silently sends uncompressed text looks healthy while costing every
visitor the same bytes over and over. If it is installed outside `PATH`, name it with
`SILPHUU_BROTLI`.

Everything else is an optional capability — when a tool is missing the feature degrades and the
server still starts.

| Tool | Used for | Without it |
|---|---|---|
| ImageMagick (`magick` / `convert` / `identify`) | scaling and probing uploads | uploads are stored unscaled |
| `avifenc`, `cjxl`, `djxl` | AVIF / JXL output | the original format is kept |
| `ffmpeg`, `ffprobe` | animation and video | animation is treated as a still image |
| `hb-subset` (HarfBuzz), `woff2_compress` | font subsetting | no subsets are produced; the site falls back to the system font stack |
| Rust / cargo | `make tools` — the `silphd` daemon in `tools/`, which carries the channel between the nodes of a deployment | the engine is unaffected; the channel stays down |

## Build and start

```sh
make build                      # -> bin/silphuu
make run                        # starts the bundled example site
make run-site DIR=/abs/path     # starts your own site
```

**Use an absolute path for `DIR`.** Every target changes to `server/` first, so a relative
path is resolved from there.

## Socket Listener

The engine listens only on a unix socket defined by `SILPHUU_SOCK`, and exits if unset. There is no TCP port flag.
To view the site locally after `make run`, you need a reverse proxy. See [`examples/nginx/dev.conf`](../examples/nginx/dev.conf) for a working development setup.

## Serving

The served root (`-static` / `SILPHUU_STATIC`) is the tree a web server points at: the engine writes rendered pages and built assets into it directly, and a reverse proxy forwards only the dynamic paths (`/admin/`, `/api/`) to the engine. The site's own media and root-level files stay in the deployment root, reached through links the engine keeps in place — there is no export step, and no second tree to keep in step.

## Configuration

Every environment variable the engine reads is listed, with an explanation of each, in [`examples/service/conf.d/silphuu`](../examples/service/conf.d/silphuu); the status daemon's are in the file beside it. [`examples/service/README.md`](../examples/service/README.md) says where each file goes on a host, and carries an OpenRC service script and a systemd unit.

## Tests

```sh
make test
```

The target moves `server/render` aside first. A stale render cache makes the page-related
assertions read uncompressed output from an earlier run and fail spuriously.

## Licence

The backend and the front end ship under different licences — see [README.md](./README.md).
Third-party code and assets are listed in [NOTICE.md](./NOTICE.md).
