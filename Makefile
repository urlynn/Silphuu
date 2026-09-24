.PHONY: build run run-site site project-static test test-clean comments tools hooks clean

SILPHUU_SOCK ?= /tmp/silphuu.sock
TRASH_DIR ?= /tmp/silphuu-trash

# Build the server binary (Go package lives in ./server)
build:
	go build -o bin/silphuu ./server

# Run against the bundled example site. Must start with server/ as its working
# directory: the engine's templates and data defaults are resolved relative to
# the CWD.
run: build
	cd server && SILPHUU_SOCK=$(SILPHUU_SOCK) ../bin/silphuu -dir ../examples/demo

# Run against your own site directory (see `make site`).
# Everything under DIR replaces the engine's copy; nothing else changes.
run-site: build
	@test -n "$(DIR)" || { echo "usage: make run-site DIR=~/silphuu-site"; exit 1; }
	cd server && SILPHUU_SOCK=$(SILPHUU_SOCK) ../bin/silphuu -dir "$(DIR)"

# Scaffold a deployment directory you own. Refuses to overwrite an existing config.
site:
	@test -n "$(DIR)" || { echo "usage: make site DIR=~/silphuu-site"; exit 1; }
	@bash scripts/init-site.sh "$(DIR)"

# Materialise the tree a static web server serves. See server/publish.go.
#
# DEST is wiped and rebuilt on every run, so point it at a directory you own — the
# target refuses to touch a directory it did not create. Pass DIR= to project a
# deployment other than the engine's own demo content.
project-static: build
	@test -n "$(DEST)" || { echo "usage: make project-static DEST=/tmp/pub [DIR=~/silphuu-site]"; exit 1; }
	cd server && ../bin/silphuu $(if $(DIR),-dir "$(DIR)") -project-static "$(DEST)"

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

# go test also runs the RTT60 bundle pipeline, which writes bundles into the site's assets/
test: test-clean
	go test ./...

# Comments ship verbatim, so the tree has to clear the comment policy. The gate lives in
# private/, which the public repo does not carry, so it is a target of its own rather than
# a step of `test` — the published Makefile cannot run what it does not ship.
comments:
	cd private/tools/commentguard && go run . "$(CURDIR)" "$(CURDIR)/private/docs/GOTCHAS.md"

# Build the Rust tool crates (workspace: tools/)
tools:
	cd tools && cargo build --release

# Enable the tracked pre-commit hook for this clone.
hooks:
	git config core.hooksPath .githooks
	@echo "pre-commit hook enabled (.githooks)"

clean:
	@if [ -d bin ]; then \
		mkdir -p "$(TRASH_DIR)" && \
		mv bin "$(TRASH_DIR)/bin-$$(date +%Y%m%d-%H%M%S)" && \
		echo "clean: moved bin aside to $(TRASH_DIR)"; \
	else \
		echo "clean: nothing to move"; \
	fi
