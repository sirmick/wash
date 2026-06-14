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
BINS    := wash-router wash-login wash-session wash-about wash-term wash-fm wash-bulk wash-edit wash-vscode wash-vscode-workbench wash-settings wash-top wash-disks wash-priv wash-journal wash-syslogs wash-services wash-packages wash-launch wash-notify wash-netd wash-net wash-washamp wash-music wash-radio wash-audio wash-remote

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

# --- packaged-binary single source of truth -------------------------------
# packaging/wash.binaries is the ONE list every native package (deb/rpm/apk)
# installs into /usr/bin — derived from BINS, so adding an app ships it
# everywhere automatically. (wash-display is the deliberate exception: its
# dynamic wlroots/wayland deps make it a SEPARATE package, so it's never in
# BINS.) `make gen-pkg-binaries` regenerates the file; `make check-pkg-binaries`
# fails if it's drifted from BINS (wired into CI so a new app can't silently
# miss the packages — the drift that left net/media/vscode out of 0.9.0).
.PHONY: print-bins gen-pkg-binaries check-pkg-binaries
print-bins:
	@printf '%s\n' $(BINS)

gen-pkg-binaries:
	@printf '%s\n' $(BINS) > packaging/wash.binaries
	@echo "wrote packaging/wash.binaries ($(words $(BINS)) entries)"

check-pkg-binaries:
	@printf '%s\n' $(BINS) | diff -u packaging/wash.binaries - >/dev/null \
	  || { echo "packaging/wash.binaries is stale vs BINS — run 'make gen-pkg-binaries'"; \
	       printf '%s\n' $(BINS) | diff -u packaging/wash.binaries - || true; exit 1; }
	@echo "packaging/wash.binaries in sync with BINS ($(words $(BINS)) entries)"

# Privileged escalation CLIs that com.wash.netd runs through wash-priv:
# washnet-read snapshots the box's config, washnet-wifi drives the polkit-gated
# radio/connect/forget. netd locates them next to its own binary, so the host
# build must stage them into out/ alongside wash-netd — without washnet-wifi
# every "Turn on Wi-Fi" fails with "no privileged path for wifi action".
TARGETS += $(OUT)/washnet-read $(OUT)/washnet-wifi

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
# Auto-detect: when WASH_DISPLAY is unset, build the compositor iff a usable
# system wlroots is present (pkg-config) and we're not cross-building riscv64 —
# so `make wash` quietly includes display on a dev box that has the deps, with
# no surprise vendored compile where it doesn't. WASH_DISPLAY=1 forces it
# (vendored wlroots compile if no system one); WASH_DISPLAY=0 skips it.
ifeq ($(origin WASH_DISPLAY),undefined)
ifneq ($(GOARCH),riscv64)
WASH_DISPLAY := $(shell for v in wlroots-0.19 wlroots-0.18 wlroots-0.17 wlroots; do pkg-config --exists $$v 2>/dev/null && { echo 1; break; }; done)
endif
endif
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

DISKS_ASSETS    := apps/disks/be/assets
DISKS_STAMP     := $(DISKS_ASSETS)/.stamp

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

WASHAMP_ASSETS    := apps/washamp/be/assets
WASHAMP_STAMP     := $(WASHAMP_ASSETS)/.stamp
MUSIC_ASSETS      := apps/music/be/assets
MUSIC_STAMP       := $(MUSIC_ASSETS)/.stamp
RADIO_ASSETS      := apps/radio/be/assets
RADIO_STAMP       := $(RADIO_ASSETS)/.stamp

# wash-vscode / wash-netd are background services, but each supplies a
# settings panel (panel.js) embedded in its binary, so they get an
# asset stamp like the windowed apps.
VSCODE_ASSETS   := apps/vscode/be/assets
VSCODE_STAMP    := $(VSCODE_ASSETS)/.stamp

NETD_ASSETS     := apps/netd/be/assets
NETD_STAMP      := $(NETD_ASSETS)/.stamp

.PHONY: all
all: $(TARGETS)

# ----- build verbs -----
# `make wash` is the standalone build for this host: the per-app binaries, plus
# wash-display when its deps are present (auto-detected above; WASH_DISPLAY=0/1
# overrides). `make wash-multicall` is the busybox layout under out/multicall/.
# (all/multicall are the underlying targets; wash/wash-multicall are the verbs.)
.PHONY: wash
wash: all
	@echo "wash: built $(words $(TARGETS)) binaries$(if $(filter $(OUT)/wash-display,$(TARGETS)),  (incl. wash-display),  (no wash-display — set WASH_DISPLAY=1 or install wlroots))"
	@echo "  → $(abspath $(OUT))/   (run: make run)"

.PHONY: wash-multicall
wash-multicall: multicall
	@echo "wash-multicall: busybox layout assembled"
	@echo "  → $(abspath $(OUT))/multicall/   ($(words $(wildcard $(OUT)/multicall/*)) entries: wash + wash-* symlinks)"

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

.PHONY: web-disks
web-disks: web-deps
	@$(PNPM) --filter @wash/app-disks run build

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

.PHONY: web-washamp
web-washamp: web-deps
	@$(PNPM) --filter @wash/app-washamp run build

.PHONY: web-music
web-music: web-deps
	@$(PNPM) --filter @wash/app-music run build

.PHONY: web-radio
web-radio: web-deps
	@$(PNPM) --filter @wash/app-radio run build

.PHONY: web-vscode
web-vscode: web-deps
	@$(PNPM) --filter @wash/app-vscode run build

.PHONY: web-netd
web-netd: web-deps
	@$(PNPM) --filter @wash/app-netd run build

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

$(DISKS_STAMP): web-disks
	$(call embed_dist,apps/disks/fe/dist,$(DISKS_ASSETS))

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

$(WASHAMP_STAMP): web-washamp
	$(call embed_dist,apps/washamp/fe/dist,$(WASHAMP_ASSETS))

$(MUSIC_STAMP): web-music
	$(call embed_dist,apps/music/fe/dist,$(MUSIC_ASSETS))

$(RADIO_STAMP): web-radio
	$(call embed_dist,apps/radio/fe/dist,$(RADIO_ASSETS))

$(VSCODE_STAMP): web-vscode
	$(call embed_dist,apps/vscode/fe/dist,$(VSCODE_ASSETS))

$(NETD_STAMP): web-netd
	$(call embed_dist,apps/netd/fe/dist,$(NETD_ASSETS))

# ----- shared-vendor coherence guard -----
#
# App FE bundles EXTERNALIZE the shared deps (@wash/ui, solid-js,
# xterm): at runtime the shell's import map resolves them to /vendor/*
# files that ship inside wash-router (and wash-login's shell copy) —
# see web/shell/build-vendor.mjs. So a targeted `make out/wash-<app>`
# that picks up changed web/lib sources pairs a fresh app bundle with
# a router still serving the OLD vendor chunk, and the app dies at
# load with "module '@wash/ui' does not provide an export named …".
# This guard rebuilds the vendor carriers first whenever the vendor
# inputs are newer than the wash-router binary. In a full `make` it's
# a no-op: wash-router/wash-login lead BINS and are already fresh by
# the time any app binary's prerequisites run.
.PHONY: vendor-sync
vendor-sync:
	@if [ ! -f $(OUT)/wash-router ] || \
	   [ -n "$$(find web/lib/src web/lib/package.json web/shell/build-vendor.mjs -newer $(OUT)/wash-router -print -quit)" ]; then \
		echo "== web/lib newer than $(OUT)/wash-router: rebuilding shared /vendor carriers (wash-router, wash-login) first"; \
		$(MAKE) --no-print-directory $(OUT)/wash-router $(OUT)/wash-login; \
	fi

# Every binary embedding an FE bundle that externalizes shared deps.
$(OUT)/wash-session $(OUT)/wash-about $(OUT)/wash-test $(OUT)/wash-term \
$(OUT)/wash-fm $(OUT)/wash-edit $(OUT)/wash-vscode $(OUT)/wash-vscode-workbench \
$(OUT)/wash-settings $(OUT)/wash-top $(OUT)/wash-disks $(OUT)/wash-journal \
$(OUT)/wash-syslogs $(OUT)/wash-services $(OUT)/wash-packages $(OUT)/wash-net \
$(OUT)/wash-washamp $(OUT)/wash-music $(OUT)/wash-radio $(OUT)/wash-netd \
$(OUT)/wash-display: vendor-sync

# ----- go stage -----

$(OUT)/wash-router: $(ROUTER_STAMP) | $(OUT)
	$(call go_build,$@,cmd/wash-router)

$(OUT)/wash-session: $(SESSION_STAMP) | $(OUT)
	$(call go_build,$@,apps/session/be/cmd)

$(OUT)/wash-about: $(ABOUT_STAMP) | $(OUT)
	$(call go_build,$@,apps/about/be/cmd)

$(OUT)/wash-test: $(TEST_STAMP) | $(OUT)
	$(call go_build,$@,apps/test/be/cmd)

# wash-display is C++/CMake, not Go. Build the settings panel FE first
# (web-display) so CMake can embed fe/dist/panel.js as raw bytes at
# configure time, then configure + build the project and copy the binary
# into out/. Rebuilds when any source or the panel bundle changes.
.PHONY: web-display
web-display: web-deps
	@$(PNPM) --filter @wash/app-display run build

# Vendored wlroots (wash-display/third_party/wlroots, 0.17.4). WLROOTS_VENDORED=1
# builds it to a private prefix and links wash-display against THAT instead of
# the distro's libwlroots — pinning the compositor to the wlroots API version
# compositor.cpp targets, independent of whatever (if any) wlroots the distro
# ships. The shared lib is bundled in the package under $(WASH_LIBDIR) and
# found at runtime via an rpath. Trimmed to the headless backend + pixman
# renderer + Xwayland (no gles2/vulkan/drm/gbm/session) so it needs only
# wayland(>=1.22)/libdrm/pixman/xkbcommon/xcb/xwayland — no mesa/EGL. The
# build env supplies meson + ninja + those -dev libs (see Dockerfile.build).
WLROOTS_VENDORED ?=
WLROOTS_SRC      := wash-display/third_party/wlroots
WLROOTS_PREFIX   := $(abspath wash-display/.wlroots)
WLROOTS_PC       := $(WLROOTS_PREFIX)/lib/pkgconfig/wlroots.pc
# Private lib dir the package installs the bundled libwlroots.so into, and the
# rpath wash-display carries to find it (absolute; /usr/bin/wash-display →
# /usr/lib/wash). Staged into out/lib/ for the packagers' .install/%files.
WASH_LIBDIR      := /usr/lib/wash

$(WLROOTS_PC):
	meson setup $(WLROOTS_SRC)/build $(WLROOTS_SRC) \
	  -Dexamples=false -Drenderers= -Dallocators= -Dbackends= \
	  -Dsession=disabled -Dxwayland=enabled -Dxcb-errors=disabled \
	  --default-library=shared --libdir=lib --prefix=$(WLROOTS_PREFIX) \
	  --buildtype=release
	ninja -C $(WLROOTS_SRC)/build
	ninja -C $(WLROOTS_SRC)/build install

ifeq ($(WLROOTS_VENDORED),1)
WASH_DISPLAY_PREREQ := $(WLROOTS_PC)
WASH_DISPLAY_PKGCFG := PKG_CONFIG_PATH="$(WLROOTS_PREFIX)/lib/pkgconfig:$$PKG_CONFIG_PATH"
WASH_DISPLAY_RPATH  := -DCMAKE_INSTALL_RPATH=$(WASH_LIBDIR) -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON
endif

# PANEL_PREBUILT=1 means wash-display/fe/dist/panel.js is already present
# (built elsewhere — e.g. on the amd64 build host) so the wash-display build
# must NOT depend on web-display. This is what lets the native-arch display
# build run on riscv64, where Node (and thus the Vite FE build) is unavailable.
ifeq ($(PANEL_PREBUILT),1)
WASH_DISPLAY_WEB :=
else
WASH_DISPLAY_WEB := web-display
endif

$(OUT)/wash-display: $(WASH_DISPLAY_WEB) $(WASH_DISPLAY_PREREQ) $(wildcard wash-display/src/*) $(wildcard wash-display/fe/dist/*) wash-display/CMakeLists.txt | $(OUT)
	$(WASH_DISPLAY_PKGCFG) cmake -S wash-display -B wash-display/build -DCMAKE_BUILD_TYPE=Release $(WASH_DISPLAY_RPATH) >/dev/null
	cmake --build wash-display/build
	cp wash-display/build/wash-display $@ && chmod 0755 $@
ifeq ($(WLROOTS_VENDORED),1)
	mkdir -p $(OUT)/lib
	cp -P $(WLROOTS_PREFIX)/lib/libwlroots.so* $(OUT)/lib/
endif

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

# wash-vscode is a background service that now supplies the settings
# Developer panel (panel.js), so its binary embeds VSCODE_STAMP's assets.
# Still .PHONY (like wash-notify): the stamp prereq stages the FE first,
# and .PHONY forces go_build to re-run so a Go-only change is picked up
# even when the stamp didn't change. go_build is cheap (Go's build cache).
.PHONY: $(OUT)/wash-vscode
$(OUT)/wash-vscode: $(VSCODE_STAMP) | $(OUT)
	$(call go_build,$@,apps/vscode/be/cmd)

$(OUT)/wash-vscode-workbench: $(VSCODE_WB_STAMP) | $(OUT)
	$(call go_build,$@,apps/vscode-workbench/be/cmd)

$(OUT)/wash-settings: $(SETTINGS_STAMP) | $(OUT)
	$(call go_build,$@,apps/settings/be/cmd)

$(OUT)/wash-top: $(TOP_STAMP) | $(OUT)
	$(call go_build,$@,apps/top/be/cmd)

$(OUT)/wash-disks: $(DISKS_STAMP) | $(OUT)
	$(call go_build,$@,apps/disks/be/cmd)

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

# wash-washamp is the windowed Winamp-skinned player (docs/AUDIO.md). It
# embeds the apps/washamp/fe Webamp bundle and serves audio over ingress.
$(OUT)/wash-washamp: $(WASHAMP_STAMP) | $(OUT)
	$(call go_build,$@,apps/washamp/be/cmd)

# wash-music is the native local music player (docs/MUSIC.md): a wash-UI
# window over internal/medialib (scan + ingress serve).
$(OUT)/wash-music: $(MUSIC_STAMP) | $(OUT)
	$(call go_build,$@,apps/music/be/cmd)

# wash-radio is the native internet-radio player (docs/RADIO.md): a wash-UI
# window whose BE reverse-proxies the upstream stream over ingress.
$(OUT)/wash-radio: $(RADIO_STAMP) | $(OUT)
	$(call go_build,$@,apps/radio/be/cmd)

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

# wash-audio is the audio control-plane service (docs/AUDIO.md §3): no
# window, no FE bundle. .PHONY for the same reason as wash-notify.
.PHONY: $(OUT)/wash-audio
$(OUT)/wash-audio: | $(OUT)
	$(call go_build,$@,apps/audio/be/cmd)

# wash-remote is the A-side remote-hosts connectivity service
# (docs/REMOTE.md R2): it brings up wash-router on remote hosts over ssh
# and forwards them locally. No window, no FE bundle. .PHONY for the same
# reason as wash-notify.
.PHONY: $(OUT)/wash-remote
$(OUT)/wash-remote: | $(OUT)
	$(call go_build,$@,apps/remote/be/cmd)

# wash-netd is the privileged networking background service (docs/NET.md
# §2.11): reserved id com.wash.netd. It now supplies the settings Network
# panel (panel.js), so its binary embeds NETD_STAMP's assets. .PHONY +
# stamp prereq for the same reason as wash-vscode above.
.PHONY: $(OUT)/wash-netd
$(OUT)/wash-netd: $(NETD_STAMP) | $(OUT)
	$(call go_build,$@,apps/netd/be/cmd)

# washnet-read / washnet-wifi: netd's privileged helpers (run via wash-priv).
# Pure Go CLIs — no FE, no embedded assets, no stamp — so .PHONY forces a
# rebuild on a source change ([[Makefile .PHONY for Go services]]); go_build is
# cheap. Staged into out/ next to wash-netd, where locateWashnet* find them.
.PHONY: $(OUT)/washnet-read
$(OUT)/washnet-read: | $(OUT)
	$(call go_build,$@,cmd/washnet-read)

.PHONY: $(OUT)/washnet-wifi
$(OUT)/washnet-wifi: | $(OUT)
	$(call go_build,$@,cmd/washnet-wifi)

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
MULTICALL_STAMPS := $(ROUTER_STAMP) $(LOGIN_SHELL_STAMP) $(ABOUT_STAMP) $(SETTINGS_STAMP) $(TOP_STAMP) $(DISKS_STAMP) $(JOURNAL_STAMP) $(SYSLOGS_STAMP) $(SERVICES_STAMP) $(PACKAGES_STAMP) $(SESSION_STAMP) $(FM_STAMP) $(TERM_STAMP) $(EDIT_STAMP) $(VSCODE_WB_STAMP) $(NET_STAMP) $(WASHAMP_STAMP) $(MUSIC_STAMP) $(RADIO_STAMP) $(VSCODE_STAMP) $(NETD_STAMP)

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

# multicall: assemble the busybox layout in its OWN dir (out/multicall/) so it
# never collides with the standalone binaries in out/ — the e2e fixture and
# run.sh resolve the layout there (the out-split, commit f812c0b). The
# dispatcher is hardlinked (discovery stays rooted at out/multicall/); wash-sudo
# + wash-priv-fakesudo are real binaries install-symlinks won't touch, so copy
# them in when present to make out/multicall/ a complete runnable image.
MC_DIR := $(OUT)/multicall
.PHONY: multicall
multicall: $(OUT)/wash
	rm -rf $(MC_DIR) && mkdir -p $(MC_DIR)
	cp -l $(OUT)/wash $(MC_DIR)/wash 2>/dev/null || cp $(OUT)/wash $(MC_DIR)/wash
	@[ -e $(OUT)/wash-priv-fakesudo ] && cp $(OUT)/wash-priv-fakesudo $(MC_DIR)/ || true
	@[ -e $(OUT)/wash-sudo ]          && cp $(OUT)/wash-sudo          $(MC_DIR)/ || true
	./$(MC_DIR)/wash install-symlinks ./$(MC_DIR)

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

# Per-distro backend test-bed images (docs/NET-BACKENDS.md §6): Ubuntu/netplan
# and Debian/ifupdown, alongside Fedora/networkd + Alpine/NM. Each bakes a known
# config; the wash-vm/vm Go tests boot it and assert wash reads/applies it.
.PHONY: vm-image-ubuntu vm-image-debian vm-image-fedora vm-image-openwrt
vm-image-ubuntu: $(OUT)/wash
	sh scripts/build-vm-image-ubuntu.sh
vm-image-debian: $(OUT)/wash
	sh scripts/build-vm-image-debian.sh
vm-image-fedora: $(OUT)/wash
	sh scripts/build-vm-image-fedora.sh
# OpenWRT router image via Image Builder (Docker); no $(OUT)/wash dep — it builds
# its own static washnet CLIs and bakes them in.
vm-image-openwrt:
	sh scripts/build-vm-image-openwrt.sh

# vm-net-test: boot the per-distro images and assert the backends in-guest
# (skips a distro whose image isn't built). The netplan read is the bug-fix gate.
.PHONY: vm-net-test
vm-net-test: $(OUT)/wash
	go test ./wash-vm/vm/ -run 'Ubuntu|Debian|Fedora' -v

# vm-disks-test: the wash-disks Tier-4 real-kernel gate (docs/STORAGE.md) — boot
# the Alpine image with virtio scratch disks, build real md/LVM/btrfs, and assert
# wash-disks' providers parse them via --dump-snapshot. Needs the storage image
# (vm-image bakes the tooling). Set WASH_VM_ZFS=1 (and rebuild vm-image with it)
# to also exercise ZFS. Skips cleanly without kvm/qemu/image.
.PHONY: vm-disks-test
vm-disks-test: $(OUT)/wash
	go test ./wash-vm/vm/ -run TestDisksRealKernel -v -count=1

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

# net-demo: launch 3 OpenWRT microVMs (one wash-configured two-VLAN router + two
# DHCP workstations) on a shared loopback L2 segment, each console in the browser
# on its own port (8001/8002/8003). The interactive sibling of the M0–M3 e2e.
# PHONY binary target: no FE/source prereqs to track, so always rebuild (else
# make silently never picks up source changes — the FE-less Go-binary gotcha).
.PHONY: $(OUT)/washnet-demo
$(OUT)/washnet-demo: | $(OUT)
	$(call go_build,$@,cmd/washnet-demo)
.PHONY: net-demo
net-demo: $(OUT)/washnet-demo
	@test -f $(OUT)/vm/openwrt.img || { echo "missing $(OUT)/vm/openwrt.img — run: make vm-image-openwrt"; exit 1; }
	$(OUT)/washnet-demo --image $(OUT)/vm/openwrt.img --base-port 8001

# net-matrix: the accessibility-matrix gate — boots a wash-configured segmented
# router microVM + one probe per segment (lan/iot/cam, each on its own loopback
# mcast L2) and asserts the isolation matrix (cam quarantined incl. internet,
# iot/cam ↛ lan, lan → cam). Exits non-zero on any policy violation, so it gates.
# PHONY binary target — see net-demo above (FE-less Go-binary rebuild gotcha).
.PHONY: $(OUT)/washnet-matrix
$(OUT)/washnet-matrix: | $(OUT)
	$(call go_build,$@,cmd/washnet-matrix)
.PHONY: net-matrix
net-matrix: $(OUT)/washnet-matrix
	@test -f $(OUT)/vm/openwrt.img || { echo "missing $(OUT)/vm/openwrt.img — run: make vm-image-openwrt"; exit 1; }
	$(OUT)/washnet-matrix --image $(OUT)/vm/openwrt.img --base-port 27300

# ----- meta -----

# Cross-arch convenience targets. The core is CGO_ENABLED=0 static, so these
# are plain GOARCH cross-compiles — no cross toolchain, no qemu. wash-display
# is C++/native and is gated off for riscv64 (see its TARGETS guard above).
.PHONY: linux-amd64 linux-arm64 linux-riscv64
linux-amd64:
	$(MAKE) GOARCH=amd64 all
linux-arm64:
	$(MAKE) GOARCH=arm64 all
linux-riscv64:
	$(MAKE) GOARCH=riscv64 all

# ----- packaging -----
# all-package builds the whole native .deb/.rpm/.apk matrix in the sanitized
# Docker build env (WASH_PKG_JOBS=N concurrency; WASH_PKG_DISPLAY=1 adds display
# rows). The per-leaf targets below build ONE (arch,distro,pkg) row each.
.PHONY: all-package
all-package:
	./packaging/run_matrix.sh

# Per-leaf package targets: <arch>-<platform>-<pkg>-package → one matrix row.
# PKG_MAP pairs the make stem with its run_matrix tag — keep in sync with
# packaging/run_matrix.sh TARGETS. Display leaves auto-set WASH_PKG_DISPLAY=1.
PKG_MAP := \
  amd64-ubuntu24-wash=ubuntu-24.04-amd64 \
  arm64-ubuntu24-wash=ubuntu-24.04-arm64 \
  riscv64-ubuntu24-wash=ubuntu-24.04-riscv64 \
  amd64-debian13-wash=debian-13-amd64 \
  arm64-debian13-wash=debian-13-arm64 \
  riscv64-debian13-wash=debian-13-riscv64 \
  amd64-fedora40-wash=fedora-40-amd64 \
  arm64-fedora40-wash=fedora-40-arm64 \
  amd64-alpine321-wash=alpine-3.21-amd64 \
  arm64-alpine321-wash=alpine-3.21-arm64 \
  riscv64-alpine321-wash=alpine-3.21-riscv64 \
  amd64-ubuntu24-display=ubuntu-24.04-amd64-display \
  arm64-ubuntu24-display=ubuntu-24.04-arm64-display \
  amd64-debian13-display=debian-13-amd64-display \
  arm64-debian13-display=debian-13-arm64-display \
  amd64-fedora40-display=fedora-40-amd64-display \
  arm64-fedora40-display=fedora-40-arm64-display

define PKG_LEAF_RULE
.PHONY: $(1)-package
$(1)-package:
	WASH_PKG_ROWS=$(2) $(if $(findstring -display,$(2)),WASH_PKG_DISPLAY=1 )./packaging/run_matrix.sh
endef
$(foreach m,$(PKG_MAP),$(eval $(call PKG_LEAF_RULE,$(firstword $(subst =, ,$(m))),$(lastword $(subst =, ,$(m))))))

# openwrt-smoke: the OpenWRT runtime row (no native .ipk — exercises the opkg/
# procd backends end-to-end in an OpenWRT container). Not a package leaf.
.PHONY: openwrt-smoke
openwrt-smoke:
	WASH_PKG_ROWS=openwrt-24.10.6-x86_64 ./packaging/run_matrix.sh

# verify-packages: download the CI-built native packages from GitHub Actions
# and install + boot-smoke each on a CLEAN matching-distro container (tests the
# actual released bytes + postinst on a pristine system — stricter than the
# in-build smoke). Needs docker + an authenticated gh. Pass ROWS="ubuntu24
# alpine321" to subset; WASH_PKGTEST_RUN=<id> to pin a run (default: latest
# successful ci.yml on this branch). amd64 only (that's all ci uploads).
.PHONY: verify-packages
verify-packages:
	./packaging/verify-gh-packages.sh $(ROWS)

# run-package: install a CI-built package in a clean container and SERVE the
# packaged desktop on a published port (browse it) — install proof you can
# click around. ROW=ubuntu24|debian13|fedora40|alpine321, PORT=11000. -no-auth.
.PHONY: run-package
run-package:
	ROW=$(ROW) PORT=$(PORT) ./packaging/run-gh-package.sh

# Back-compat alias (deprecated; use all-package).
.PHONY: packages
packages: all-package

# wash-vm: build the full RISC-V Linux VM (kernel + firmware + rootfs)
# and install artifacts where wash-vm/web's index.html expects them.
# Requires Docker (kernel + rootfs both build in containers).
.PHONY: vm
vm:
	$(MAKE) -C wash-vm/image all

# Legacy alias.
.PHONY: rv
rv: vm

# ----- vm verbs -----  (<platform>-image-vm builds an image; <platform>-run-vm serves)
.PHONY: alpine-image-vm ubuntu-image-vm debian-image-vm fedora-image-vm openwrt-image-vm browser-image-vm
alpine-image-vm:  vm-image
ubuntu-image-vm:  vm-image-ubuntu
debian-image-vm:  vm-image-debian
fedora-image-vm:  vm-image-fedora
openwrt-image-vm: vm-image-openwrt
browser-image-vm: vm

.PHONY: browser-run-vm qemu-run-vm
# browser VM: build the riscv artifacts, then the dev server serves them on :12000.
browser-run-vm: browser-image-vm
	wash-vm/run-browser.sh
# qemu (wemu) surface: run-qemu.sh builds vm-image+chrome+washvm-run itself → :13000.
qemu-run-vm:
	wash-vm/run-qemu.sh

# ----- run verb -----  (dev = HMR loop, below; run = built standalone router :11000)
# run does NOT rebuild — it execs whatever `make wash` last produced, so it's
# instant and `make wash && make run` doesn't build twice. Run `make wash` to
# pick up changes; `make dev` is the auto-rebuilding HMR loop.
.PHONY: run
run:
	@test -x $(OUT)/wash-router || { echo "run: $(OUT)/wash-router not built — run 'make wash' first" >&2; exit 1; }
	$(OUT)/wash-router

# ----- clean verbs -----
# Explicit path lists (never rm tmp/ branches/ harbor.config test-net.py .git).
# clean = build artifacts; tmp-clean = + /tmp runtime junk; docker-clean = + the
# matrix images; all-clean = everything (incl. node_modules + Go cache).
.PHONY: clean
clean:
	rm -rf $(OUT)
	rm -rf web/*/dist apps/*/fe/dist wash-display/fe/dist
	rm -rf apps/*/be/assets cmd/*/assets internal/apps/*/assets internal/runner/*/assets internal/login/assets/shell
	rm -rf web/shell/public/vendor
	rm -rf dist packaging/build-ctx
	rm -rf wash-display/build wash-display/third_party/wlroots/build wash-display/.wlroots
	rm -f  wash-display/*.deb
	rm -rf e2e/test-results e2e/playwright-report test-results coverage
	rm -rf wash wash-router image image-rv web/demo .understand-anything
	@echo "clean: build artifacts removed"

.PHONY: tmp-clean
tmp-clean:
	-find /tmp -maxdepth 1 -name 'wash-*' -type d -exec rm -rf {} + 2>/dev/null
	-find /tmp -maxdepth 1 -name 'wash-*' \( -name '*.png' -o -name '*.log' \) -delete 2>/dev/null
	-rm -rf /tmp/wd-smoke
	@echo "tmp-clean: /tmp runtime junk removed (sockets kept)"

.PHONY: docker-clean
docker-clean:
	-docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null | grep -E '^(wash-build|wash-binaries|wash-display-bin|wash-pkg)' | xargs -r docker rmi -f
	-docker buildx prune -f
	@echo "docker-clean: matrix images + buildx cache removed"

.PHONY: all-clean
all-clean: clean tmp-clean docker-clean
	rm -rf node_modules web/*/node_modules e2e/node_modules
	-go clean -cache
	@echo "all-clean: everything removed (node_modules + go cache too)"

# Back-compat alias.
.PHONY: distclean
distclean: all-clean

.PHONY: verify
verify: test-app
	go vet ./...
	go test ./...
	@for f in $(TARGETS); do \
		file "$$f" | grep -qi 'statically' || { echo "$$f is not statically linked"; exit 1; }; \
	done
	@echo "verify: ok"

# ----- test verbs -----
# Each builds its own prereqs and streams output (no redirection). unit-test/
# e2e-test are the standalone layout; all-test also sweeps the multicall layout.
# net-test/disks-test boot real qemu microvms (need /dev/kvm; gates self-skip).

# fe-unit: Node's built-in runner over web/** + apps/** *.test.ts (framework-free
# logic + solid reactive logic via --conditions=browser). Layout-independent.
.PHONY: fe-unit
fe-unit: web-deps
	@files=$$(find web apps -path '*/node_modules' -prune -o -name '*.test.ts' -not -path '*/dist/*' -print); \
	  if [ -z "$$files" ]; then echo "fe-unit: no test files"; else node --test --conditions=browser $$files; fi

# component: vitest + jsdom mounting real Solid components (*.ctest.tsx).
.PHONY: component
component: web-deps
	$(PNPM) exec vitest run --passWithNoTests

# unit-test: go vet + go test ./... + the FE unit tiers. Builds first (the app
# packages //go:embed their assets, so go test won't compile on a bare tree).
# -p 1: the loopback package wires router+sdk over in-memory pipes and mustn't
# race other in-process tests for goroutine scheduling. wash-vm/vm is excluded —
# it's the kvm VM-integration suite (boots real qemu when images are present,
# blowing the unit timeout); cover it via net-test / disks-test instead.
GO_UNIT_PKGS = $$(go list ./... | grep -v '/wash-vm/vm$$')
# Build via test-app (TEST_APP=1) not bare wash: `go vet/test ./...` compiles
# apps/test/be, which //go:embeds assets only produced under TEST_APP=1 — without
# them vet fails "pattern all:assets: no matching files found" on a clean tree.
.PHONY: unit-test
unit-test: test-app fe-unit component
	$(MAKE) -s check-pkg-binaries
	go vet ./...
	go test -count=1 -p 1 -timeout 120s $(GO_UNIT_PKGS)

# e2e-test: the full Playwright suite (standalone layout); builds the test app.
# e2e/ is NOT a workspace member, so --ignore-workspace is required to install
# its own deps (incl. playwright) into e2e/node_modules.
.PHONY: e2e-test
e2e-test: test-app
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	# WASH_E2E_SKIP_VM=1: mirror CI (which has no VM artifacts) — the heavy
	# KVM-backed net-vm tiers run under `make net-test` / `make e2e-vm`, not
	# the standalone suite, so a local VM image can't make push diverge + flake.
	cd e2e && WASH_E2E_SKIP_VM=1 $(PNPM) test

# screenshots: regenerate the docs/screenshots/*.png marketing shots by posing
# real app windows in a throwaway router and capturing them with Playwright.
# Driven by its OWN config (NOT part of e2e-test). Deterministic (seeded window
# layout). The `display.png` shot is best-effort: it needs out/wash-display +
# an X client (xclock) on the host — run `make wash` first to build the
# compositor, otherwise that one shot self-skips.
.PHONY: screenshots
screenshots: test-app
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) exec playwright test -c playwright.screenshots.config.ts
	@echo "screenshots: wrote → $(abspath docs/screenshots)/"
	@ls -1 docs/screenshots/*.png 2>/dev/null | sed 's,^,  ,'

# net-test: the kvm network tier — builds the openwrt + per-distro images, then
# runs the segmentation gate + per-distro backend read/apply + browser net-vm e2e.
.PHONY: net-test
net-test: vm-image-openwrt vm-image-ubuntu vm-image-debian vm-image-fedora
	$(MAKE) net-matrix
	$(MAKE) vm-net-test
	$(MAKE) e2e-vm

# disks-test: the real-kernel storage gate (md/LVM/btrfs); builds the alpine image.
.PHONY: disks-test
disks-test: vm-image
	$(MAKE) vm-disks-test

# all-test: every test, BOTH layouts (standalone + multicall). Does NOT package.
# multicall-smoke: the multicall layout's UNIQUE risk surface — argv[0] dispatch
# through the wash-<app> symlinks — NOT a re-run of the whole suite. App logic is
# identical to standalone, and the kvm/browser VM tiers already BOOT multicall in
# real Linux (their images bake out/multicall), so deep integration is covered
# there. Here: build the layout, the one package the multicall tag changes
# (cmd/wash), and the bundle-registration + a launch/spawn spec.
.PHONY: multicall-smoke
multicall-smoke: test-app
	$(MAKE) TEST_APP=1 multicall
	go test -count=1 -tags=multicall ./cmd/wash/...
	cd e2e && $(PNPM) install --ignore-workspace --silent && $(PNPM) exec playwright install chromium
	cd e2e && WASH_E2E_MULTICALL=1 $(PNPM) exec playwright test single-file.spec.ts kiosk-test-app.spec.ts --workers=1

# all-test: the full suite ONCE (standalone) + an early multicall smoke + the kvm
# net/disks gates (which boot the multicall layout in-VM = real multicall
# integration). No full-suite duplication across layouts.
.PHONY: all-test
all-test: unit-test multicall-smoke e2e-test net-test disks-test
	@echo "all-test: green — standalone full + multicall smoke + kvm VM gates (multicall in-VM)"

# coverage: instrumented build (COVER=1 → go build -cover) → go-unit + e2e
# counters merged into one module-wide report under $(COVERDIR). App BEs with no
# _test.go still show real coverage because the e2e suite drives them — each
# spawned process flushes covmeta/covcounters on SIGTERM (internal/sdk/coverage.go).
COVERDIR ?= $(CURDIR)/coverage
.PHONY: coverage
coverage:
	rm -rf "$(COVERDIR)"; mkdir -p "$(COVERDIR)/unit" "$(COVERDIR)/e2e" "$(COVERDIR)/merged"
	COVER=1 $(MAKE) test-app   # instrumented binaries the e2e will exercise
	go test -count=1 -p 1 -timeout 120s -coverpkg=./... ./... -args -test.gocoverdir="$(COVERDIR)/unit"
	cd e2e && $(PNPM) install --ignore-workspace --silent && $(PNPM) exec playwright install chromium
	cd e2e && GOCOVERDIR="$(COVERDIR)/e2e" $(PNPM) test
	go tool covdata merge   -i="$(COVERDIR)/unit,$(COVERDIR)/e2e" -o="$(COVERDIR)/merged"
	go tool covdata textfmt -i="$(COVERDIR)/merged" -o="$(COVERDIR)/coverage.txt"
	@echo "── module total (merged unit + e2e) ──"
	go tool covdata percent -i="$(COVERDIR)/merged" | sort
	go tool cover -func="$(COVERDIR)/coverage.txt" | tail -1
	@echo "coverage profile: $(COVERDIR)/coverage.txt"

# test-all: the whole pyramid — every test (both layouts + kvm net/disks gates)
# then the full packaging matrix. = all-test + all-package. Long; needs docker +
# /dev/kvm + qemu. WASH_PKG_DISPLAY=1 includes the display package rows.
.PHONY: test-all
test-all: all-test
	WASH_PKG_DISPLAY=$(or $(WASH_PKG_DISPLAY),1) $(MAKE) all-package

# push: the pre-push gate — run exactly what .github/workflows/ci.yml runs
# (unit + e2e, then the amd64 wash packages + the OpenWRT smoke), and `git push`
# ONLY if all of it passes. So a red build never reaches the remote / GH Actions.
# Each step is sequential and fail-fast: the first failure aborts before push.
# Override the push target with ARGS, e.g. `make push ARGS="origin HEAD"`.
.PHONY: push
push:
	$(MAKE) unit-test
	$(MAKE) e2e-test
	$(MAKE) amd64-ubuntu24-wash-package
	$(MAKE) amd64-debian13-wash-package
	$(MAKE) amd64-fedora40-wash-package
	$(MAKE) amd64-alpine321-wash-package
	$(MAKE) openwrt-smoke
	@echo "════ push: CI-equivalent gate passed ✓ — pushing ════"
	git push $(ARGS)

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
