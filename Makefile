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
BINS    := wash-router wash-session wash-about wash-term wash-fm wash-bulk wash-edit wash-settings wash-top wash-priv wash-journal wash-launch wash-sudo
TARGETS := $(addprefix $(OUT)/,$(BINS))

# Test app: not part of the default build; built explicitly with
# `make test-app` (or `make TEST_APP=1`). Hidden from the prod
# catalog at runtime via manifest.Hidden.
TEST_APP ?=
ifneq ($(TEST_APP),)
TARGETS += $(OUT)/wash-test
endif

GO_ENV  := CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH)

# go_build wraps `go build` and forces 0755 perms on the output. The
# router's reserved-id trust check rejects group/world-writable
# binaries; default umasks vary across devs (0022 → 0755, 0002 →
# 0775), so normalising here means freshly-built wash-priv passes the
# trust gate without --dev or WASH_TRUSTED_APPS_DIRS. The trust
# branch that accepts this is "owned by current uid + mode 0755" —
# see internal/router/registry.go isTrustedBinary.
go_build = $(GO_ENV) go build $(GOFLAGS) -o $(1) ./$(2) && chmod 0755 $(1)

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

FM_ASSETS      := cmd/wash-fm/assets
FM_STAMP       := $(FM_ASSETS)/.stamp

BULK_ASSETS    := cmd/wash-bulk/assets
BULK_STAMP     := $(BULK_ASSETS)/.stamp

EDIT_ASSETS    := cmd/wash-edit/assets
EDIT_STAMP     := $(EDIT_ASSETS)/.stamp

SETTINGS_ASSETS := cmd/wash-settings/assets
SETTINGS_STAMP  := $(SETTINGS_ASSETS)/.stamp

TOP_ASSETS      := cmd/wash-top/assets
TOP_STAMP       := $(TOP_ASSETS)/.stamp

PRIV_ASSETS     := cmd/wash-priv/assets
PRIV_STAMP      := $(PRIV_ASSETS)/.stamp

JOURNAL_ASSETS  := cmd/wash-journal/assets
JOURNAL_STAMP   := $(JOURNAL_ASSETS)/.stamp

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

.PHONY: web-fm
web-fm: web-deps
	@cd web && $(PNPM) --filter @wash/app-fm run build

.PHONY: web-bulk
web-bulk: web-deps
	@cd web && $(PNPM) --filter @wash/app-bulk run build

.PHONY: web-edit
web-edit: web-deps
	@cd web && $(PNPM) --filter @wash/app-edit run build

.PHONY: web-settings
web-settings: web-deps
	@cd web && $(PNPM) --filter @wash/app-settings run build

.PHONY: web-top
web-top: web-deps
	@cd web && $(PNPM) --filter @wash/app-top run build

.PHONY: web-priv
web-priv: web-deps
	@cd web && $(PNPM) --filter @wash/app-priv run build

.PHONY: web-journal
web-journal: web-deps
	@cd web && $(PNPM) --filter @wash/app-journal run build

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

$(FM_STAMP): web-fm
	$(call embed_dist,web/apps/fm/dist,$(FM_ASSETS))

$(BULK_STAMP): web-bulk
	$(call embed_dist,web/apps/bulk/dist,$(BULK_ASSETS))

$(EDIT_STAMP): web-edit
	$(call embed_dist,web/apps/edit/dist,$(EDIT_ASSETS))

$(SETTINGS_STAMP): web-settings
	$(call embed_dist,web/apps/settings/dist,$(SETTINGS_ASSETS))

$(TOP_STAMP): web-top
	$(call embed_dist,web/apps/top/dist,$(TOP_ASSETS))

$(PRIV_STAMP): web-priv
	$(call embed_dist,web/apps/priv/dist,$(PRIV_ASSETS))

$(JOURNAL_STAMP): web-journal
	$(call embed_dist,web/apps/journal/dist,$(JOURNAL_ASSETS))

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-router)

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-session)

$(OUT)/wash-about: $(ABOUT_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-about)

$(OUT)/wash-test: $(TEST_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-test)

$(OUT)/wash-term: $(TERM_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-term)

$(OUT)/wash-fm: $(FM_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-fm)

$(OUT)/wash-bulk: $(BULK_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-bulk)

$(OUT)/wash-edit: $(EDIT_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-edit)

$(OUT)/wash-settings: $(SETTINGS_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-settings)

$(OUT)/wash-top: $(TOP_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-top)

$(OUT)/wash-priv: $(PRIV_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-priv)

$(OUT)/wash-journal: $(JOURNAL_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-journal)

# wash-launch is a CLI, not an app. No FE bundle, no embedded assets.
$(OUT)/wash-launch: | $(OUT)
	$(call go_build,$@,cmd/wash-launch)

# wash-sudo is also a CLI — the privilege-aware shell wrapper.
$(OUT)/wash-sudo: | $(OUT)
	$(call go_build,$@,cmd/wash-sudo)

# fakesudo: tiny sudo stub for wash-priv e2e tests. Not part of the
# default build; only the e2e harness should ever invoke it. Lives
# in cmd/ so it shares the go module + GOFLAGS settings.
$(OUT)/wash-priv-fakesudo: | $(OUT)
	$(call go_build,$@,cmd/wash-priv-fakesudo)

# Convenience target: build the test app + everything else.
.PHONY: test-app
test-app: $(OUT)/wash-priv-fakesudo
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
# the router at 0.0.0.0:11000. Open http://localhost:5173/ in a
# browser. Editing files under web/shell/src triggers HMR; editing
# Go or app sources still requires re-running `make dev`.
DEV_APPS := /tmp/wash-dev-apps

.PHONY: dev
dev: $(OUT)/wash-router $(OUT)/wash-session $(OUT)/wash-about
	mkdir -p $(DEV_APPS)
	cp -f $(OUT)/wash-session $(OUT)/wash-about $(DEV_APPS)/
	@echo "wash dev: router :11000 + Vite :5173 — open http://localhost:5173/"
	@trap 'kill 0' INT TERM EXIT; \
	  ( WASH_APPS_DIR=$(DEV_APPS) $(OUT)/wash-router ) & \
	  ( cd web && $(PNPM) --filter @wash/shell run dev ) & \
	  wait
