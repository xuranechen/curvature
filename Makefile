.PHONY: help dev dev-backend dev-web build-web build install uninstall build-all build-relay dev-relay dist-clean publish-release-notes verify-release release tag

GO ?= go
NPM ?= npm
WEB_DIR ?= web
ADDR ?= :7331
ROOT ?= .
PREFIX ?= $(HOME)/.local

help:
	@printf "%s\n" \
		"Targets:" \
		"  make dev          # run curvature on $(ADDR)" \
		"  make dev-backend  # backend only on $(ADDR)" \
		"  make dev-web      # Vite dev server only" \
		"  make build-web    # build web assets into web/dist" \
		"  make build        # build web assets and CLI binary" \
		"  make build-relay  # build the self-hosted relay server binary" \
		"  make install      # install binary and built static assets into $(PREFIX)" \
		"  make uninstall    # remove installed binary and static assets from $(PREFIX)" \
		"  make build-all    # cross-compile for all platforms into dist/" \
		"  make dist-clean   # remove dist/ directory" \
		"  make start        # run curvature on $(ADDR) with built static assets" \
		"  make start-server # backend entrypoint serving built static assets" \
		"  make test         # run Go tests" \
		"  make tag TAG=v1.2.3  # create and push a git tag" \
		"  make publish-release-notes TAG=v1.2.3  # commit and push release-notes.md if changed" \
		"  make verify-release TAG=v1.2.3  # verify signed release manifest and artifacts in $(DIST_DIR)" \
		"  make release TAG=v1.2.3  # publish notes, build-all, then create GitHub release"

dev:
	$(GO) run ./cli/cmd -addr $(ADDR) $(ROOT)

dev-backend:
	$(GO) run ./server/cmd/curvature-server -addr $(ADDR)

dev-web:
	cd $(WEB_DIR) && $(NPM) run dev

build-web:
	cd $(WEB_DIR) && $(NPM) run build

build: build-web
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o curvature ./cli/cmd

build-relay:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o curvature-relay ./relay-server

dev-relay:
	$(GO) run ./relay-server -addr :8080 -base http://localhost:8080

install: build
	install -d "$(PREFIX)/bin"
	install -d "$(PREFIX)/share/curvature"
	install -m 0755 curvature "$(PREFIX)/bin/curvature"
	install -m 0644 agents.json "$(PREFIX)/share/curvature/agents.json"
	install -m 0644 task_template.json "$(PREFIX)/share/curvature/task_template.json"
	rm -rf "$(PREFIX)/share/curvature/web"
	cp -R "$(WEB_DIR)/dist" "$(PREFIX)/share/curvature/web"

uninstall:
	rm -f "$(PREFIX)/bin/curvature"
	rm -rf "$(PREFIX)/share/curvature"

start:
	$(GO) run ./cli/cmd -addr $(ADDR) $(ROOT)

start-server:
	$(GO) run ./server/cmd/curvature-server -addr $(ADDR)

test:
	$(GO) test ./...

# ── Cross-platform distribution ──────────────────────────────────────────
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
DIST_DIR ?= dist
RELEASE_NOTES_FILE ?= release-notes.md
RELEASE_NOTES_LATEST_FILE ?= $(DIST_DIR)/release-notes-$(TAG).md
RELEASE_UPLOAD_JOBS ?= 4
RELEASE_ARTIFACTS := $(DIST_DIR)/curvature_$(TAG)_*.tar.gz $(DIST_DIR)/curvature_$(TAG)_*.zip $(DIST_DIR)/curvature_$(TAG)_manifest.json

# Targets: OS/ARCH pairs
PLATFORMS := \
	darwin/amd64 \
	darwin/arm64 \
	linux/amd64 \
	linux/arm64 \
	linux/arm \
	windows/amd64 \
	windows/arm64

build-all: build-web
	@bash scripts/build-all.sh "$(VERSION)" "$(DIST_DIR)"

dist-clean:
	rm -rf $(DIST_DIR)

# ── Release ──────────────────────────────────────────────────────────────
# Usage: make tag TAG=v1.2.3
tag:
	@test -n "$(TAG)" || (echo "Usage: make tag TAG=v1.2.3" >&2; exit 1)
	@echo "Tagging $(TAG)"
	git push origin main
	git tag $(TAG)
	git push origin $(TAG)

# Usage: make publish-release-notes TAG=v1.2.3
publish-release-notes:
	@test -n "$(TAG)" || (echo "Usage: make publish-release-notes TAG=v1.2.3" >&2; exit 1)
	@test -f "$(RELEASE_NOTES_FILE)" || (echo "Error: release notes file not found: $(RELEASE_NOTES_FILE)" >&2; exit 1)
	@version="$$(sed -nE '1s/^#[[:space:]]+Curvature[[:space:]]+(v?[0-9]+(\.[0-9]+){1,3}[^[:space:]]*).*$$/\1/p' "$(RELEASE_NOTES_FILE)")"; \
		test "$$version" = "$(TAG)" || (echo "Error: $(RELEASE_NOTES_FILE) first line version '$$version' does not match TAG '$(TAG)'." >&2; exit 1)
	git add "$(RELEASE_NOTES_FILE)"
	@if git diff --cached --quiet -- "$(RELEASE_NOTES_FILE)"; then \
		echo "No release notes changes to commit."; \
	else \
		git commit -m "update release notes"; \
		git push origin main; \
	fi

# Usage: make verify-release TAG=v1.2.3
verify-release:
	@test -n "$(TAG)" || (echo "Usage: make verify-release TAG=v1.2.3" >&2; exit 1)
	@test -n "$(CURVATURE_RELEASE_PUBLIC_KEY)" || (echo "Error: CURVATURE_RELEASE_PUBLIC_KEY is required to verify release manifests." >&2; exit 1)
	@$(GO) run scripts/sign-release-manifest.go -verify -version "$(TAG)" -dist "$(DIST_DIR)" -repo "a9gent/curvature" -public-key "$(CURVATURE_RELEASE_PUBLIC_KEY)"

# Usage: make release TAG=v1.2.3
# Builds desktop/server platforms and creates a GitHub release.
release:
	@command -v gh >/dev/null 2>&1 || (echo "Error: gh (GitHub CLI) is required. https://cli.github.com" >&2; exit 1)
	@test -n "$(TAG)" || (echo "Usage: make release TAG=v1.2.3" >&2; exit 1)
	@test -f "$(RELEASE_NOTES_FILE)" || (echo "Error: release notes file not found: $(RELEASE_NOTES_FILE)" >&2; exit 1)
	@test -n "$(CURVATURE_RELEASE_PUBLIC_KEY)" || (echo "Error: CURVATURE_RELEASE_PUBLIC_KEY is required for signed auto-update builds." >&2; exit 1)
	@if [ -z "$$CURVATURE_RELEASE_PRIVATE_KEY" ] && [ -z "$$CURVATURE_RELEASE_PRIVATE_KEY_FILE" ]; then \
		echo "Error: CURVATURE_RELEASE_PRIVATE_KEY or CURVATURE_RELEASE_PRIVATE_KEY_FILE is required to sign release manifests." >&2; \
		exit 1; \
	fi
	@version="$$(sed -nE '1s/^#[[:space:]]+Curvature[[:space:]]+(v?[0-9]+(\.[0-9]+){1,3}[^[:space:]]*).*$$/\1/p' "$(RELEASE_NOTES_FILE)")"; \
		test "$$version" = "$(TAG)" || (echo "Error: $(RELEASE_NOTES_FILE) first line version '$$version' does not match TAG '$(TAG)'." >&2; exit 1)
	$(MAKE) dist-clean
	mkdir -p "$(DIST_DIR)"
	@awk 'NR > 1 && /^# Curvature[[:space:]]+/ { exit } { print }' "$(RELEASE_NOTES_FILE)" > "$(RELEASE_NOTES_LATEST_FILE)"
	CURVATURE_RELEASE_PUBLIC_KEY="$(CURVATURE_RELEASE_PUBLIC_KEY)" $(MAKE) build-all VERSION="$(TAG)"
	@$(GO) run scripts/sign-release-manifest.go -version "$(TAG)" -dist "$(DIST_DIR)" -repo "a9gent/curvature"
	$(MAKE) verify-release TAG="$(TAG)" CURVATURE_RELEASE_PUBLIC_KEY="$(CURVATURE_RELEASE_PUBLIC_KEY)"
	@echo "Creating draft GitHub release $(TAG)"
	gh release create $(TAG) \
		--draft \
		--title "$(TAG)" \
		--notes-file "$(RELEASE_NOTES_LATEST_FILE)"
	@echo "Uploading release artifacts with $(RELEASE_UPLOAD_JOBS) parallel jobs"
	@set -- $(RELEASE_ARTIFACTS); \
		for artifact do \
			test -f "$$artifact" || { echo "Error: release artifact not found: $$artifact" >&2; exit 1; }; \
		done; \
		printf '%s\n' "$$@" | xargs -n 1 -P "$(RELEASE_UPLOAD_JOBS)" gh release upload "$(TAG)"
	@echo "Publishing GitHub release $(TAG)"
	gh release edit "$(TAG)" --draft=false
	$(MAKE) publish-release-notes TAG="$(TAG)"
