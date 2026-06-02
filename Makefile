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

# COVER=1 builds coverage-instrumented binaries (`go build -cover`),
# attributing coverage across the whole module. Used by
# `./test.sh --coverage` to measure how much of the Go tree the e2e
# suite exercises (each spawned app/router writes counters to GOCOVERDIR
# on exit; see internal/sdk/coverage.go). Off by default — normal builds
# are byte-for-byte untouched.
COVER ?=
ifeq ($(COVER),1)
GOFLAGS += -cover -coverpkg=github.com/sirmick/wash/...
endif

OUT     := out
BINS    := wash-router wash-login wash-session wash-about wash-term wash-fm wash-bulk wash-edit wash-vscode wash-vscode-workbench wash-settings wash-top wash-priv wash-journal wash-syslogs wash-services wash-packages wash-launch wash-notify wash-netd wash-net

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

# wash-display: native X/Wayland compositor BE (C++/CMake, separate
# package). Opt-in via WASH_DISPLAY=1; never built for the emulator
# (riscv) target. Built by its own CMake project — NOT the Go build —
# so the native wlroots/libdatachannel deps never touch the
# CGO_ENABLED=0 core (docs/DISPLAY.md §8). The wire client builds with
# just a C++17 compiler; the compositor sources are enabled in
# wash-display/CMakeLists.txt once wlroots/libdatachannel are present.
WASH_DISPLAY ?=
ifeq ($(WASH_DISPLAY),1)
ifneq ($(GOARCH),riscv64)
TARGETS += $(OUT)/wash-display
endif
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

# wash-login embeds the same shell runtime as wash-router so authed
# users hitting wash-login's HTTP root get a working desktop without
# wash-router needing to expose its own HTTP port (it's --listen-unix
# in multi-user mode). The login package's //go:embed picks up
# whatever lands under internal/login/assets/shell/.
LOGIN_SHELL_ASSETS := internal/login/assets/shell
LOGIN_SHELL_STAMP  := $(LOGIN_SHELL_ASSETS)/.stamp

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

EDIT_ASSETS    := apps/edit/be/assets
EDIT_STAMP     := $(EDIT_ASSETS)/.stamp

SETTINGS_ASSETS := apps/settings/be/assets
SETTINGS_STAMP  := $(SETTINGS_ASSETS)/.stamp

TOP_ASSETS      := apps/top/be/assets
TOP_STAMP       := $(TOP_ASSETS)/.stamp

JOURNAL_ASSETS  := apps/journal/be/assets
JOURNAL_STAMP   := $(JOURNAL_ASSETS)/.stamp

SYSLOGS_ASSETS  := apps/syslogs/be/assets
SYSLOGS_STAMP   := $(SYSLOGS_ASSETS)/.stamp

SERVICES_ASSETS := apps/services/be/assets
SERVICES_STAMP  := $(SERVICES_ASSETS)/.stamp

PACKAGES_ASSETS := apps/packages/be/assets
PACKAGES_STAMP  := $(PACKAGES_ASSETS)/.stamp


VSCODE_WB_ASSETS := apps/vscode-workbench/be/assets
VSCODE_WB_STAMP  := $(VSCODE_WB_ASSETS)/.stamp

NET_ASSETS      := apps/net/be/assets
NET_STAMP       := $(NET_ASSETS)/.stamp

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

.PHONY: web-edit
web-edit: web-deps
	@$(PNPM) --filter @wash/app-edit run build

.PHONY: web-vscode-workbench
web-vscode-workbench: web-deps
	@$(PNPM) --filter @wash/app-vscode-workbench run build

.PHONY: web-settings
web-settings: web-deps
	@$(PNPM) --filter @wash/app-settings run build

.PHONY: web-top
web-top: web-deps
	@$(PNPM) --filter @wash/app-top run build

.PHONY: web-journal
web-journal: web-deps
	@$(PNPM) --filter @wash/app-journal run build

.PHONY: web-syslogs
web-syslogs: web-deps
	@$(PNPM) --filter @wash/app-syslogs run build

.PHONY: web-services
web-services: web-deps
	@$(PNPM) --filter @wash/app-services run build

.PHONY: web-packages
web-packages: web-deps
	@$(PNPM) --filter @wash/app-packages run build

.PHONY: web-net
web-net: web-deps
	@$(PNPM) --filter @wash/app-net run build

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

$(LOGIN_SHELL_STAMP): $(ROUTER_STAMP)
	$(call embed_dist,$(ROUTER_ASSETS),$(LOGIN_SHELL_ASSETS))

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

$(EDIT_STAMP): web-edit
	$(call embed_dist,apps/edit/fe/dist,$(EDIT_ASSETS))

$(VSCODE_WB_STAMP): web-vscode-workbench
	$(call embed_dist,apps/vscode-workbench/fe/dist,$(VSCODE_WB_ASSETS))

$(SETTINGS_STAMP): web-settings
	$(call embed_dist,apps/settings/fe/dist,$(SETTINGS_ASSETS))

$(TOP_STAMP): web-top
	$(call embed_dist,apps/top/fe/dist,$(TOP_ASSETS))

$(JOURNAL_STAMP): web-journal
	$(call embed_dist,apps/journal/fe/dist,$(JOURNAL_ASSETS))

$(SYSLOGS_STAMP): web-syslogs
	$(call embed_dist,apps/syslogs/fe/dist,$(SYSLOGS_ASSETS))

$(SERVICES_STAMP): web-services
	$(call embed_dist,apps/services/fe/dist,$(SERVICES_ASSETS))

$(PACKAGES_STAMP): web-packages
	$(call embed_dist,apps/packages/fe/dist,$(PACKAGES_ASSETS))

$(NET_STAMP): web-net
	$(call embed_dist,apps/net/fe/dist,$(NET_ASSETS))

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-router)

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(call go_build,$@,apps/session/be/cmd)

$(OUT)/wash-about: $(ABOUT_STAMP) | $(OUT)
	$(call go_build,$@,apps/about/be/cmd)

$(OUT)/wash-test: $(TEST_STAMP) | $(OUT)
	$(call go_build,$@,apps/test/be/cmd)

# wash-display is C++/CMake, not Go. Configure + build its own project
# and copy the binary into out/. Rebuilds when any source changes.
$(OUT)/wash-display: $(wildcard wash-display/src/*) wash-display/CMakeLists.txt | $(OUT)
	cmake -S wash-display -B wash-display/build -DCMAKE_BUILD_TYPE=Release >/dev/null
	cmake --build wash-display/build
	cp wash-display/build/wash-display $@ && chmod 0755 $@

$(OUT)/wash-term: $(TERM_STAMP) | $(OUT)
	$(call go_build,$@,apps/term/be/cmd)

$(OUT)/wash-fm: $(FM_STAMP) | $(OUT)
	$(call go_build,$@,apps/fm/be/cmd)

# wash-bulk is a background service (M6): no window, no FE bundle,
# no embedded assets. Other apps' enqueue calls land here via cross-
# app app_msg; state ships back via sdk.StateService subscribers.
# .PHONY: see wash-notify for the rationale.
.PHONY: $(OUT)/wash-bulk
$(OUT)/wash-bulk: | $(OUT)
	$(call go_build,$@,apps/bulk/be/cmd)

$(OUT)/wash-edit: $(EDIT_STAMP) | $(OUT)
	$(call go_build,$@,apps/edit/be/cmd)

# wash-vscode is a background service with no FE bundle (its control UI
# lives in the settings app), so its binary has no asset-stamp dep.
# .PHONY (like wash-bulk/wash-notify): with no changing prerequisite,
# make would otherwise treat the binary as up-to-date forever and never
# pick up Go source changes. go_build is cheap (Go's own build cache).
.PHONY: $(OUT)/wash-vscode
$(OUT)/wash-vscode: | $(OUT)
	$(call go_build,$@,apps/vscode/be/cmd)

$(OUT)/wash-vscode-workbench: $(VSCODE_WB_STAMP) | $(OUT)
	$(call go_build,$@,apps/vscode-workbench/be/cmd)

$(OUT)/wash-settings: $(SETTINGS_STAMP) | $(OUT)
	$(call go_build,$@,apps/settings/be/cmd)

$(OUT)/wash-top: $(TOP_STAMP) | $(OUT)
	$(call go_build,$@,apps/top/be/cmd)

# wash-priv is a background service (M7): no window, no FE bundle,
# no embedded assets. Its UI lives in the session sidebar; crypto
# handshake moved into the session FE bundle.
# .PHONY: see wash-notify for the rationale.
.PHONY: $(OUT)/wash-priv
$(OUT)/wash-priv: | $(OUT)
	$(call go_build,$@,apps/priv/be/cmd)

$(OUT)/wash-journal: $(JOURNAL_STAMP) | $(OUT)
	$(call go_build,$@,apps/journal/be/cmd)

$(OUT)/wash-syslogs: $(SYSLOGS_STAMP) | $(OUT)
	$(call go_build,$@,apps/syslogs/be/cmd)

$(OUT)/wash-services: $(SERVICES_STAMP) | $(OUT)
	$(call go_build,$@,apps/services/be/cmd)

$(OUT)/wash-packages: $(PACKAGES_STAMP) | $(OUT)
	$(call go_build,$@,apps/packages/be/cmd)

# wash-net is the windowed network UI (docs/NET.md §2.11). It embeds the
# apps/net/fe bundle and relays to the privileged wash-netd service. The
# NET_STAMP dep stages the FE into apps/net/be/assets before the go build.
$(OUT)/wash-net: $(NET_STAMP) | $(OUT)
	$(call go_build,$@,apps/net/be/cmd)

# wash-notify is a background service: no window, no FE bundle, no
# embedded assets. Other apps' c.Notify() calls land here via the
# router's fan-out (see relayNotify in internal/router/app_session.go).
#
# .PHONY so `make all` always re-runs `go build`. The Go toolchain
# does its own incremental check on source mtimes; without this the
# target's lack of source-stamp dep would let make consider an
# already-built binary up-to-date even after a .go change.
.PHONY: $(OUT)/wash-notify
$(OUT)/wash-notify: | $(OUT)
	$(call go_build,$@,apps/notify/be/cmd)

# wash-netd is the privileged networking background service (docs/NET.md
# §2.11): no window, no FE bundle, reserved id com.wash.netd. The windowed
# com.wash.net app drives it cross-app. .PHONY for the same reason as
# wash-notify (Go-only target, no source-stamp dep).
.PHONY: $(OUT)/wash-netd
$(OUT)/wash-netd: | $(OUT)
	$(call go_build,$@,apps/netd/be/cmd)

# wash-launch is a CLI, not an app. No FE bundle, no embedded assets.
$(OUT)/wash-launch: | $(OUT)
	$(call go_build,$@,cmd/wash-launch)

# wash-login is the multi-user front-door (docs/MULTIUSER.md). It
# embeds its own login + welcome HTML via //go:embed; no Vite stage,
# no FE-bundle stamp dependency.
#
# For multi-user deployments wash-login needs three Linux
# capabilities so it can fork-setuid-exec wash-router as the
# authenticated user and SIGTERM cross-uid routers:
#
#   cap_setuid + cap_setgid → fork → setuid → exec per-user router
#   cap_kill                → end-session SIGTERM across uids
#
# Set them with `make wash-login-caps` (uses sudo). The dev path
# (wash-login + target user are the same uid) doesn't need caps.
$(OUT)/wash-login: $(LOGIN_SHELL_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-login)
	@caps_have=`getcap $@ 2>/dev/null || true`; \
	  case "$$caps_have" in \
	    *cap_setuid*cap_setgid*cap_kill*) echo "  CAPS  $@ ($$caps_have)" ;; \
	    *) echo "  HINT  $@ has no capabilities yet; for multi-user run: sudo make wash-login-caps" ;; \
	  esac

# wash-login-caps applies the three capabilities wash-login needs
# for multi-user operation. Requires sudo because setcap is a
# privileged operation (it writes file-level capabilities into the
# binary's extended attributes).
#
# Idempotent: runs even if caps are already set. Re-run after
# rebuilding wash-login (caps are tied to the file, not the path —
# replacing the file drops them).
.PHONY: wash-login-caps
wash-login-caps: $(OUT)/wash-login
	@command -v setcap >/dev/null 2>&1 || { \
	  echo "setcap not found; install libcap2-bin (Debian/Ubuntu) or libcap (RHEL/Fedora)" >&2; \
	  exit 1; }
	sudo setcap 'cap_setuid,cap_setgid,cap_kill+ep' $(OUT)/wash-login
	@echo "  CAPS  $(OUT)/wash-login → cap_setuid, cap_setgid, cap_kill"
	@getcap $(OUT)/wash-login

# wash-login-deploy: full multi-user OOTB. Adds caps, creates the
# wash group if missing, ensures wash-login's runtime user is in it.
# This target is intentionally interactive (uses sudo prompts) and
# meant for first-time setup, not CI.
.PHONY: wash-login-deploy
wash-login-deploy: wash-login-caps
	@getent group wash >/dev/null 2>&1 || { \
	  echo "  GROUP creating system group 'wash'"; \
	  sudo groupadd --system wash; }
	@echo "  DONE  wash-login ready for multi-user. If you run wash-login as a"
	@echo "        dedicated wash-system user, add it to group wash:"
	@echo "          sudo usermod -aG wash <user-running-wash-login>"

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
# Note ROUTER_STAMP + LOGIN_SHELL_STAMP: cmd/wash imports
# internal/runner/router (which `//go:embed`s internal/runner/
# router/assets) and internal/runner/login → internal/login
# (which `//go:embed`s assets/shell). Without these stamps in
# multicall's dep list, a clean checkout fails to compile with
# "pattern all:assets: no matching files found" — local dev
# accidentally works because the standalone wash-router build
# rule already chains through ROUTER_STAMP.
MULTICALL_STAMPS := $(ROUTER_STAMP) $(LOGIN_SHELL_STAMP) $(ABOUT_STAMP) $(SETTINGS_STAMP) $(TOP_STAMP) $(JOURNAL_STAMP) $(SYSLOGS_STAMP) $(SERVICES_STAMP) $(PACKAGES_STAMP) $(SESSION_STAMP) $(FM_STAMP) $(TERM_STAMP) $(EDIT_STAMP) $(VSCODE_WB_STAMP) $(NET_STAMP)

# Adding wash_test_app to the tags pulls the test app's blank-import
# in (which is otherwise excluded by cmd/wash/imports_test.go's
# wash_test_app build constraint). Mirrors the standalone TEST_APP=1
# convention.
MULTICALL_TAGS := multicall,netgo,osusergo
ifneq ($(TEST_APP),)
MULTICALL_TAGS := $(MULTICALL_TAGS),wash_test_app
MULTICALL_STAMPS += $(TEST_STAMP)
endif

# .PHONY: the stamps key only on FE assets, so a Go-source change (router,
# vmlogin, any app BE) would otherwise leave out/wash — and thus the baked VM
# image — stale (the FE-less-Go gotcha). Always re-link; Go's cache keeps it
# cheap when nothing changed.
.PHONY: $(OUT)/wash
$(OUT)/wash: $(MULTICALL_STAMPS) | $(OUT)
	$(GO_ENV) go build -trimpath -ldflags="-s -w" \
	  -tags=$(MULTICALL_TAGS) \
	  -o $@ ./cmd/wash && chmod 0755 $@

.PHONY: multicall
multicall: $(OUT)/wash
	$(OUT)/wash install-symlinks $(OUT)

# Cross-compile-friendly variant: builds the multicall binary but
# does NOT run it (no install-symlinks). Used by wash-vm/image/
# rootfs/build.sh when GOARCH=riscv64 — the wash-router/wash-* /usr/
# bin entries inside the rootfs are created by build.sh's own
# symlink loops, and `$(OUT)/wash install-symlinks` would fail when
# the binary's arch differs from the host's (no qemu-user binfmt).
.PHONY: multicall-bin
multicall-bin: $(OUT)/wash

# Full-stack e2e: builds everything (incl. test app), then runs the
# Playwright suite. Browser binary download is one-time and cached.
.PHONY: e2e
e2e: test-app
	cd e2e && $(PNPM) install --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) test

# washvm-run: the host-side VM runner/proxy CLI (docs/NET.md §8.2) — boots a
# microvm and fronts it with the chrome + wire tunnel. The wash-net e2e gate
# and `make run-vm` spawn it.
# .PHONY: it has no FE-asset stamp to key on, so without this make would never
# rebuild it on a Go-source change (the FE-less-Go-service gotcha). Go's build
# cache makes the unconditional re-link cheap.
.PHONY: $(OUT)/washvm-run
$(OUT)/washvm-run: | $(OUT)
	$(call go_build,$@,cmd/washvm-run)

# vm-image: the bootable Alpine microvm image baking the real wash desktop.
# Depends on the multicall binary (the script bakes out/wash).
.PHONY: vm-image
vm-image: $(OUT)/wash
	sh scripts/build-vm-image-alpine.sh

# vm-chrome: the minimal host chrome the proxy serves (docs/NET.md §8.3) —
# tabs for Console + Wash. The wash UI (shell.js + app bundles) comes over the
# wire FROM the VM; only the vendored runtimes + this chrome are host-served, so
# we assemble: shell's /vendor + icons + our index.html + chrome.js (NO
# shell.js — it's fetched over the wire to prove the point).
VM_CHROME := $(OUT)/vm-chrome
.PHONY: vm-chrome
vm-chrome: web-shell
	rm -rf $(VM_CHROME) && mkdir -p $(VM_CHROME)
	cp -R web/shell/dist/vendor $(VM_CHROME)/vendor
	cp web/shell/dist/icons.svg web/shell/dist/wash-logo.svg $(VM_CHROME)/
	cp wash-vm/chrome/index.html wash-vm/chrome/chrome.js $(VM_CHROME)/

# e2e-vm: the wash-net B1 exit gate (docs/NET.md §8.3, §11) — Playwright drives
# the VM-served wash UI through the proxy and round-trips a model edit to
# in-guest com.wash.netd. Needs qemu + /dev/kvm; the specs self-skip otherwise.
# (`./test.sh --vm` is the higher-level entry point — it preflights the host.)
.PHONY: e2e-vm
e2e-vm: vm-image vm-chrome $(OUT)/washvm-run
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) exec playwright test net-vm-gate net-vm-multi

# run-vm: boot the baked image and serve the wash UI for manual poking.
.PHONY: run-vm
run-vm: vm-image vm-chrome $(OUT)/washvm-run
	$(OUT)/washvm-run --chrome $(VM_CHROME) --addr 127.0.0.1:8080

# ----- meta -----

.PHONY: linux-arm64
linux-arm64:
	$(MAKE) GOARCH=arm64 all

# wash-vm: build the full RISC-V Linux VM (kernel + firmware + rootfs)
# and install artifacts where wash-vm/web's index.html expects them.
# Requires Docker (kernel + rootfs both build in containers).
.PHONY: vm
vm:
	$(MAKE) -C wash-vm/image all

# Legacy alias.
.PHONY: rv
rv: vm

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
