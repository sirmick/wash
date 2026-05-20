# wash — top-level build
#
# Two stages, wired together:
#   1. web — Vite library builds; brotli precompress (if installed);
#            output copied into cmd/<bin>/assets/ for //go:embed.
#   2. go  — CGO_ENABLED=0 go build -trimpath -ldflags="-s -w".
#
# `make verify` enforces static-ELF output. If the web stage is
# skipped, the go stage's //go:embed pattern errors and the build
# fails — a stale or unbuilt frontend cannot silently ship.

GOOS    ?= linux
GOARCH  ?= amd64
GOFLAGS := -trimpath -ldflags=-s\ -w -tags netgo,osusergo

OUT     := out
BINS    := wash-router wash-session wash-about wash-term
TARGETS := $(addprefix $(OUT)/,$(BINS))

# Test app: not part of the default build; built explicitly with
# `make test-app` (or `make TEST_APP=1`). Hidden from the prod
# catalog at runtime via manifest.Hidden.
TEST_APP ?=
ifneq ($(TEST_APP),)
TARGETS += $(OUT)/wash-test
endif

GO_ENV  := CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH)

PNPM    := pnpm

# Per-binary embed stamps. Each binary's go build depends on its stamp
# so the web stage runs first and updates assets/ if anything changed.
ROUTER_ASSETS  := cmd/wash-router/assets
ROUTER_STAMP   := $(ROUTER_ASSETS)/.stamp

SESSION_ASSETS := cmd/wash-session/assets
SESSION_STAMP  := $(SESSION_ASSETS)/.stamp

ABOUT_ASSETS   := cmd/wash-about/assets
ABOUT_STAMP    := $(ABOUT_ASSETS)/.stamp

TEST_ASSETS    := cmd/wash-test/assets
TEST_STAMP     := $(TEST_ASSETS)/.stamp

TERM_ASSETS    := cmd/wash-term/assets
TERM_STAMP     := $(TERM_ASSETS)/.stamp

.PHONY: all
all: $(TARGETS)

$(OUT):
	mkdir -p $(OUT)

# ----- web stage -----

# pnpm install once; subsequent runs are fast no-ops.
.PHONY: web-deps
web-deps:
	@cd web && $(PNPM) install --silent

.PHONY: web-shell
web-shell: web-deps
	@cd web && $(PNPM) --filter @wash/shell run build

.PHONY: web-session
web-session: web-deps
	@cd web && $(PNPM) --filter @wash/app-session run build

.PHONY: web-about
web-about: web-deps
	@cd web && $(PNPM) --filter @wash/app-about run build

.PHONY: web-test
web-test: web-deps
	@cd web && $(PNPM) --filter @wash/app-test run build

.PHONY: web-term
web-term: web-deps
	@cd web && $(PNPM) --filter @wash/app-term run build

# embed-into-cmd helper. Usage: $(call embed,<src dist dir>,<dst assets dir>)
define embed_dist
	rm -rf $(2)
	mkdir -p $(2)
	cp -R $(1)/. $(2)/
	@if command -v brotli >/dev/null 2>&1; then \
		find $(2) -type f \( -name '*.js' -o -name '*.css' -o -name '*.html' -o -name '*.svg' -o -name '*.json' \) -exec brotli -k -q 11 -f {} + ; \
	else \
		echo "brotli not installed: skipping precompress under $(2)"; \
	fi
	touch $(2)/.stamp
endef

$(ROUTER_STAMP): web-shell
	$(call embed_dist,web/shell/dist,$(ROUTER_ASSETS))

$(SESSION_STAMP): web-session
	$(call embed_dist,web/apps/session/dist,$(SESSION_ASSETS))

$(ABOUT_STAMP): web-about
	$(call embed_dist,web/apps/about/dist,$(ABOUT_ASSETS))

$(TEST_STAMP): web-test
	$(call embed_dist,web/apps/test/dist,$(TEST_ASSETS))

$(TERM_STAMP): web-term
	$(call embed_dist,web/apps/term/dist,$(TERM_ASSETS))

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-router

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-session

$(OUT)/wash-about: $(ABOUT_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-about

$(OUT)/wash-test: $(TEST_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-test

$(OUT)/wash-term: $(TERM_STAMP) | $(OUT)
	$(GO_ENV) go build $(GOFLAGS) -o $@ ./cmd/wash-term

# Convenience target: build the test app + everything else.
.PHONY: test-app
test-app:
	$(MAKE) TEST_APP=1 all

# Full-stack e2e: builds everything (incl. test app), then runs the
# Playwright suite. Browser binary download is one-time and cached.
.PHONY: e2e
e2e: test-app
	cd e2e && $(PNPM) install --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) test

# ----- meta -----

.PHONY: linux-arm64
linux-arm64:
	$(MAKE) GOARCH=arm64 all

.PHONY: clean
clean:
	rm -rf $(OUT)
	rm -rf web/*/dist web/apps/*/dist
	rm -rf cmd/*/assets

.PHONY: verify
verify: all
	go vet ./...
	go test ./...
	@for f in $(TARGETS); do \
		file "$$f" | grep -qi 'statically' || { echo "$$f is not statically linked"; exit 1; }; \
	done
	@echo "verify: ok"

# Dev mode: Vite serves the shell with HMR at :5173 and proxies /ws to
# the router at 127.0.0.1:7681. Open http://localhost:5173/ in a
# browser. Editing files under web/shell/src triggers HMR; editing
# Go or app sources still requires re-running `make dev`.
DEV_APPS := /tmp/wash-dev-apps

.PHONY: dev
dev: $(OUT)/wash-router $(OUT)/wash-session $(OUT)/wash-about
	mkdir -p $(DEV_APPS)
	cp -f $(OUT)/wash-session $(OUT)/wash-about $(DEV_APPS)/
	@echo "wash dev: router :7681 + Vite :5173 — open http://localhost:5173/"
	@trap 'kill 0' INT TERM EXIT; \
	  ( WASH_APPS_DIR=$(DEV_APPS) $(OUT)/wash-router ) & \
	  ( cd web && $(PNPM) --filter @wash/shell run dev ) & \
	  wait
