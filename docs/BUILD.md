# Building and running

Silphuu is a single Go binary. Site content lives outside it and is pointed at with `-dir`,
so upgrading the engine means replacing one binary and leaving the site directory alone.

## Requirements

Go is the only hard requirement; see `go.mod` for the version. Everything else is an
optional capability — when a tool is missing the feature degrades and the server still
starts.

| Tool | Used for | Without it |
|---|---|---|
| ImageMagick (`magick` / `convert` / `identify`) | scaling and probing uploads | uploads are stored unscaled |
| `avifenc`, `cjxl`, `djxl` | AVIF / JXL output | the original format is kept |
| `ffmpeg`, `ffprobe` | animation and video | animation is treated as a still image |
| `hb-subset` (HarfBuzz), `woff2_compress` | font subsetting | no subsets are produced; the site falls back to the system font stack |
| `rsync` | pushing a static projection to edge nodes | the sync step is skipped |
| Rust / cargo | `make tools`, the `net-traffic` probe in `tools/` | the engine is unaffected |

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
To view the site locally after `make run`, you need a reverse proxy. See [`examples/nginx/dev.conf`](./examples/nginx/dev.conf) for a working development setup.

## Static Export

```sh
make project-static DEST=/abs/path
```
This renders the public-facing site into a static directory. A web server can serve these files directly, forwarding only dynamic requests (e.g., `/api/`) to the engine.

## Tests

```sh
make test
```

The target moves `server/render` aside first. A stale render cache makes the page-related
assertions read uncompressed output from an earlier run and fail spuriously.

## Licence

The backend and the front end ship under different licences — see [README.md](./README.md).
Third-party code and assets are listed in [NOTICE.md](./NOTICE.md).
