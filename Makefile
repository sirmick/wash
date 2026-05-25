# wash — top-level build
#
# Two stages, wired together:
#   1. web — Vite library builds; output copied into apps/<X>/be/assets/
#            for //go:embed.
#   2. go  — CGO_ENABLED=0 go build -trimpath -ldflags="-s -w".
#
# `make verify` enforces static-ELF output. If the web stage is
# skipped, the go stage's //go:embed pattern errors and the build
# fails — a stale or unbuilt frontend cannot silently ship.

GOOS    ?= linux
GOARCH  ?= amd64
GOFLAGS := -trimpath -ldflags=-s\ -w -tags netgo,osusergo

OUT     := out
BINS    := wash-router wash-session wash-about wash-term wash-fm wash-bulk wash-edit wash-settings wash-top wash-priv wash-journal wash-syslogs wash-launch

# wash-sudo is the CLI face of wash-priv (terminal `sudo`-like
# entrypoint that routes through the browser FE for unlock).
# Opt-out by setting WASH_NO_SUDO=1 — useful for headless / kiosk
# deploys that never need a terminal-driven sudo path. Default is
# on so existing dev flows are unaffected.
WASH_NO_SUDO ?=
ifeq ($(WASH_NO_SUDO),)
BINS += wash-sudo
endif

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
ROUTER_ASSETS  := internal/runner/router/assets
ROUTER_STAMP   := $(ROUTER_ASSETS)/.stamp

SESSION_ASSETS := apps/session/be/assets
SESSION_STAMP  := $(SESSION_ASSETS)/.stamp

ABOUT_ASSETS   := apps/about/be/assets
ABOUT_STAMP    := $(ABOUT_ASSETS)/.stamp

TEST_ASSETS    := apps/test/be/assets
TEST_STAMP     := $(TEST_ASSETS)/.stamp

TERM_ASSETS    := apps/term/be/assets
TERM_STAMP     := $(TERM_ASSETS)/.stamp

FM_ASSETS      := apps/fm/be/assets
FM_STAMP       := $(FM_ASSETS)/.stamp

BULK_ASSETS    := apps/bulk/be/assets
BULK_STAMP     := $(BULK_ASSETS)/.stamp

EDIT_ASSETS    := apps/edit/be/assets
EDIT_STAMP     := $(EDIT_ASSETS)/.stamp

SETTINGS_ASSETS := apps/settings/be/assets
SETTINGS_STAMP  := $(SETTINGS_ASSETS)/.stamp

TOP_ASSETS      := apps/top/be/assets
TOP_STAMP       := $(TOP_ASSETS)/.stamp

PRIV_ASSETS     := apps/priv/be/assets
PRIV_STAMP      := $(PRIV_ASSETS)/.stamp

JOURNAL_ASSETS  := apps/journal/be/assets
JOURNAL_STAMP   := $(JOURNAL_ASSETS)/.stamp

SYSLOGS_ASSETS  := apps/syslogs/be/assets
SYSLOGS_STAMP   := $(SYSLOGS_ASSETS)/.stamp

.PHONY: all
all: $(TARGETS)

$(OUT):
	mkdir -p $(OUT)

# ----- web stage -----

# pnpm install once; subsequent runs are fast no-ops.
.PHONY: web-deps
web-deps:
	@$(PNPM) install --silent

.PHONY: web-shell
web-shell: web-deps
	@$(PNPM) --filter @wash/shell run build

.PHONY: web-session
web-session: web-deps
	@$(PNPM) --filter @wash/app-session run build

.PHONY: web-about
web-about: web-deps
	@$(PNPM) --filter @wash/app-about run build

.PHONY: web-test
web-test: web-deps
	@$(PNPM) --filter @wash/app-test run build

.PHONY: web-term
web-term: web-deps
	@$(PNPM) --filter @wash/app-term run build

.PHONY: web-fm
web-fm: web-deps
	@$(PNPM) --filter @wash/app-fm run build

.PHONY: web-bulk
web-bulk: web-deps
	@$(PNPM) --filter @wash/app-bulk run build

.PHONY: web-edit
web-edit: web-deps
	@$(PNPM) --filter @wash/app-edit run build

.PHONY: web-settings
web-settings: web-deps
	@$(PNPM) --filter @wash/app-settings run build

.PHONY: web-top
web-top: web-deps
	@$(PNPM) --filter @wash/app-top run build

.PHONY: web-priv
web-priv: web-deps
	@$(PNPM) --filter @wash/app-priv run build

.PHONY: web-journal
web-journal: web-deps
	@$(PNPM) --filter @wash/app-journal run build

.PHONY: web-syslogs
web-syslogs: web-deps
	@$(PNPM) --filter @wash/app-syslogs run build

# embed-into-cmd helper. Usage: $(call embed,<src dist dir>,<dst assets dir>)
#
# Files land under cmd/<bin>/assets/ and are picked up by //go:embed
# all:assets. Brotli precompression was removed: the router's
# http.FileServer has no Accept-Encoding negotiation, so .br copies
# only bloated every binary. Re-add a *.br pass + content-encoding
# handling in router/http.go together if HTTP brotli ever lands.
define embed_dist
	rm -rf $(2)
	mkdir -p $(2)
	cp -R $(1)/. $(2)/
	touch $(2)/.stamp
endef

$(ROUTER_STAMP): web-shell
	$(call embed_dist,web/shell/dist,$(ROUTER_ASSETS))

$(SESSION_STAMP): web-session
	$(call embed_dist,apps/session/fe/dist,$(SESSION_ASSETS))

$(ABOUT_STAMP): web-about
	$(call embed_dist,apps/about/fe/dist,$(ABOUT_ASSETS))

$(TEST_STAMP): web-test
	$(call embed_dist,apps/test/fe/dist,$(TEST_ASSETS))

$(TERM_STAMP): web-term
	$(call embed_dist,apps/term/fe/dist,$(TERM_ASSETS))

$(FM_STAMP): web-fm
	$(call embed_dist,apps/fm/fe/dist,$(FM_ASSETS))

$(BULK_STAMP): web-bulk
	$(call embed_dist,apps/bulk/fe/dist,$(BULK_ASSETS))

$(EDIT_STAMP): web-edit
	$(call embed_dist,apps/edit/fe/dist,$(EDIT_ASSETS))

$(SETTINGS_STAMP): web-settings
	$(call embed_dist,apps/settings/fe/dist,$(SETTINGS_ASSETS))

$(TOP_STAMP): web-top
	$(call embed_dist,apps/top/fe/dist,$(TOP_ASSETS))

$(PRIV_STAMP): web-priv
	$(call embed_dist,apps/priv/fe/dist,$(PRIV_ASSETS))

$(JOURNAL_STAMP): web-journal
	$(call embed_dist,apps/journal/fe/dist,$(JOURNAL_ASSETS))

$(SYSLOGS_STAMP): web-syslogs
	$(call embed_dist,apps/syslogs/fe/dist,$(SYSLOGS_ASSETS))

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-router)

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(call go_build,$@,apps/session/be/cmd)

$(OUT)/wash-about: $(ABOUT_STAMP) | $(OUT)
	$(call go_build,$@,apps/about/be/cmd)

$(OUT)/wash-test: $(TEST_STAMP) | $(OUT)
	$(call go_build,$@,apps/test/be/cmd)

$(OUT)/wash-term: $(TERM_STAMP) | $(OUT)
	$(call go_build,$@,apps/term/be/cmd)

$(OUT)/wash-fm: $(FM_STAMP) | $(OUT)
	$(call go_build,$@,apps/fm/be/cmd)

$(OUT)/wash-bulk: $(BULK_STAMP) | $(OUT)
	$(call go_build,$@,apps/bulk/be/cmd)

$(OUT)/wash-edit: $(EDIT_STAMP) | $(OUT)
	$(call go_build,$@,apps/edit/be/cmd)

$(OUT)/wash-settings: $(SETTINGS_STAMP) | $(OUT)
	$(call go_build,$@,apps/settings/be/cmd)

$(OUT)/wash-top: $(TOP_STAMP) | $(OUT)
	$(call go_build,$@,apps/top/be/cmd)

$(OUT)/wash-priv: $(PRIV_STAMP) | $(OUT)
	$(call go_build,$@,apps/priv/be/cmd)

$(OUT)/wash-journal: $(JOURNAL_STAMP) | $(OUT)
	$(call go_build,$@,apps/journal/be/cmd)

$(OUT)/wash-syslogs: $(SYSLOGS_STAMP) | $(OUT)
	$(call go_build,$@,apps/syslogs/be/cmd)

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

# Multi-call build. Compiles cmd/wash with -tags=multicall — the
# resulting binary dispatches by argv[0] to whatever apps are
# linked in via cmd/wash/imports_<name>.go. `wash install-symlinks`
# materializes the wash-<name> symlinks. Standalone per-app
# binaries (BINS above) are unaffected.
#
# Only includes extracted apps (Phase 4+: wash-about so far).
# Adding an app: extract into apps/<name>/be/, drop a
# cmd/wash/imports_<name>.go blank-import, add its asset stamp to
# the dep list below.
MULTICALL_STAMPS := $(ABOUT_STAMP) $(BULK_STAMP) $(SETTINGS_STAMP) $(TOP_STAMP) $(JOURNAL_STAMP) $(SYSLOGS_STAMP) $(PRIV_STAMP) $(SESSION_STAMP) $(FM_STAMP) $(TERM_STAMP) $(EDIT_STAMP)

# Adding wash_test_app to the tags pulls the test app's blank-import
# in (which is otherwise excluded by cmd/wash/imports_test.go's
# wash_test_app build constraint). Mirrors the standalone TEST_APP=1
# convention.
MULTICALL_TAGS := multicall,netgo,osusergo
ifneq ($(TEST_APP),)
MULTICALL_TAGS := $(MULTICALL_TAGS),wash_test_app
MULTICALL_STAMPS += $(TEST_STAMP)
endif

$(OUT)/wash: $(MULTICALL_STAMPS) | $(OUT)
	$(GO_ENV) go build -trimpath -ldflags="-s -w" \
	  -tags=$(MULTICALL_TAGS) \
	  -o $@ ./cmd/wash && chmod 0755 $@

.PHONY: multicall
multicall: $(OUT)/wash
	$(OUT)/wash install-symlinks $(OUT)

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

# One-command RISC-V TinyEMU demo build: GOARCH=riscv64 wash bins +
# Linux Image + alpine-riscv64 rootfs.ext2, all installed into
# web/demo/public/tinyemu/ with the names wash-riscv64.cfg reads.
# Requires Docker (kernel + rootfs both build in containers).
.PHONY: rv
rv:
	$(MAKE) -C image-rv all

.PHONY: clean
clean:
	rm -rf $(OUT)
	rm -rf web/*/dist apps/*/fe/dist

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
	  ( $(PNPM) --filter @wash/shell run dev ) & \
	  wait
