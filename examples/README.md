# examples/

Sample configuration for running the engine, and the site it ships with.

| Directory | What it is |
|---|---|
| `demo/` | The engine's own example site — a few posts and categories, an avatar and the default images. `make run` points at it. |
| `service/` | A reference configuration for running the engine: every environment variable the engine and the status daemon read, plus an OpenRC service script and a systemd unit. Every value is a placeholder. |
| `nginx/` | Reverse-proxy configuration, including `dev.conf` for development. |

### About `demo/` — the default skeleton

`demo/` is also where a new site's skeleton comes from. The engine hardcodes neither
`src/css/site.css` nor `static/image/**`, so `scripts/init-site.sh` copies those from here when
you scaffold a deployment. Editing the styles or the images under `demo/` therefore edits the
starting point of every site created afterwards.

### About the reverse proxy

`nginx/dev.conf` is a working development proxy. The engine listens on a unix socket and nothing
else, so without a reverse proxy there is no URL to open at all. For the security model behind
the admin header, and for how to start the engine, see [`docs/BUILD.md`](../docs/BUILD.md).
