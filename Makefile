.PHONY: build build-esa run run-site site test test-clean tools clean

SILPHUU_SOCK ?= /tmp/silphuu.sock
TRASH_DIR ?= /tmp/silphuu-trash

# The served root: rendered pages, built assets and uploads — what a browser fetches.
# Defaults outside the repository so a `make run` never writes build output into the tree.
STATIC_DIR ?= /tmp/silphuu-served

# Build the server binary (Go package lives in ./server)
build:
	go build -o bin/silphuu ./server

# The same binary with the built-in edge outlet compiled in. A build that does not ask for it
# carries none of that product's vocabulary and reaches its edge through SILPHUU_PURGE_CMD
# instead; see server/purge.go.
build-esa:
	go build -tags esa -o bin/silphuu-esa ./server

# Run against the bundled example site. Must start with server/ as its working directory:
# the engine's src/, templates/ and shipped config/ are resolved relative to the CWD.
#
# The site's authored images (avatar, hero background, sponsor codes) live in its static/image/,
# and the served tree links to that directory rather than holding a copy -- so nothing is
# seeded here. css/, js/, font/ and thumb/ are the engine's own output, built on start.
run: build
	@mkdir -p $(STATIC_DIR)
	cd server && SILPHUU_SOCK=$(SILPHUU_SOCK) ../bin/silphuu -dir ../examples/demo -static $(STATIC_DIR)

# Run against your own site directory (see `make site`). DIR is the deployment root and holds
# config/, state/ and posts/; STATIC_DIR is the served root a browser fetches. The two must
# not contain one another.
run-site: build
	@test -n "$(DIR)" || { echo "usage: make run-site DIR=~/silphuu-site [STATIC_DIR=~/silphuu-served]"; exit 1; }
	@mkdir -p "$(STATIC_DIR)"
	cd server && SILPHUU_SOCK=$(SILPHUU_SOCK) ../bin/silphuu -dir "$(DIR)" -static "$(STATIC_DIR)"

# Scaffold a deployment directory you own. Refuses to overwrite an existing config.
# SERVED names the served root; empty means a sibling of DIR.
site:
	@test -n "$(DIR)" || { echo "usage: make site DIR=~/silphuu-site [SERVED=~/silphuu-served]"; exit 1; }
	@bash scripts/init-site.sh "$(DIR)" $(SERVED)

# The render cache holds pre-rendered HTML. A stale one makes the render
# assertions read uncompressed historical output and fail for the wrong reason,
# so tests always start from an empty cache.
#
# This moves the cache aside rather than deleting it. Nothing here is precious,
# but a reversible operation costs nothing and cannot lose work.
test-clean:
	@if [ -d server/render ]; then \
		mkdir -p "$(TRASH_DIR)" && \
		mv server/render "$(TRASH_DIR)/render-$$(date +%Y%m%d-%H%M%S)" && \
		echo "test-clean: moved server/render aside to $(TRASH_DIR)"; \
	fi

# go test also runs the bundle pipeline, which writes bundles into the site's assets/
test: test-clean
	go test ./...

# Build the Rust tool crates (workspace: tools/)
tools:
	cd tools && cargo build --release

clean:
	@if [ -d bin ]; then \
		mkdir -p "$(TRASH_DIR)" && \
		mv bin "$(TRASH_DIR)/bin-$$(date +%Y%m%d-%H%M%S)" && \
		echo "clean: moved bin aside to $(TRASH_DIR)"; \
	else \
		echo "clean: nothing to move"; \
	fi
