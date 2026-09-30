# Reference configuration

Everything a running engine is configured through: every environment variable the engine and
the status daemon read, and the service definitions that hand them over. Copy the files,
replace the values, and point a reverse proxy at the socket.

Nothing here assumes a server. `make run-site` is enough to try the engine locally; the
variables exist so that a deployment can state its own paths instead of inheriting defaults.

The comments in `conf.d/` are the documentation. This file only says where each one goes.

## What goes where

| This directory | On the host | Read by |
|---|---|---|
| `conf.d/silphuu` | `/etc/conf.d/silphuu` | the engine, through the service script |
| `conf.d/silphd` | `/etc/conf.d/silphd` | the status daemon, and the engine's status page |
| `init.d/silphuu` | `/etc/init.d/silphuu` (needs `chmod +x`) | OpenRC |
| `init.d/silphd` | `/etc/init.d/silphd` (needs `chmod +x`) | OpenRC |
| `systemd/silphuu.service` | `/etc/systemd/system/silphuu.service` | systemd |
| `systemd/silphd.service` | `/etc/systemd/system/silphd.service` | systemd |

Install the pair your host runs. Every service definition here reads the same `conf.d` files, so
a host can move between the two managers without a second configuration.

## The two service managers read `conf.d` differently

`conf.d` is a shell fragment, and each manager reads it its own way.

- **OpenRC sources it.** `openrc-run.sh` does no more than `.` the file, so a variable has to
  be `export`ed to reach the service at all. (`RC_REEXPORT` in the service script is the other
  route, and it is a list of names to keep in step by hand.)
- **systemd's `EnvironmentFile=` is not a shell.** It has no notion of `export`: it reads
  `export FOO=bar` as a variable named `export FOO`, fails the name check, logs
  `invalid variable name ... ignoring` to the journal, and starts the unit anyway with the
  variable unset.

That is why the systemd unit here sources the file through `/bin/sh -c` rather than naming it
with `EnvironmentFile=`. Both unit files in this directory do it that way, and both carry the
reason beside the line.

## The three directories

| Role | Example path | Contents |
|---|---|---|
| Engine | `/usr/local/lib/silphuu` | read-only: `server/`, `templates/`, `src/`, the shipped `config/` |
| Deployment root (`SILPHUU_HOME`) | `/srv/silphuu` | writable, private: `config/`, `state/`, `posts/`, `static/` |
| Served root (`SILPHUU_STATIC`) | `/srv/silphuu-served` | what a browser fetches, and the proxy's `root` |

The deployment root and the served root must not contain one another: the engine refuses to
start when they do. Everything under the served root is reachable by URL, which is why
configuration and state live outside it.

`scripts/init-site.sh` scaffolds the deployment root — `make site DIR=~/my-site` — and the
README it writes there describes what each subdirectory is for.

## Without a service manager

`make run-site DIR=~/my-site` builds the engine and runs it against a site directory with the
socket and the served root set for you. Nothing above is required to try the engine.

## Two binaries

`silphuu` is Go and builds with `make build`. `silphd` is Rust, lives in `tools/silphd`, and is
optional: without it the site works, and the status page reports the endpoint as unavailable.

## Keeping this complete

A variable added to the engine and not to these files is a variable a deployment cannot set. A
test in `server/` reads the names out of the source and fails when one of them is missing from
`conf.d/`, so the two cannot drift apart.
