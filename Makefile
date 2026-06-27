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
# -buildvcs=false: Go otherwise stamps the binary with VCS revision + a
# "modified" dirty flag, which makes builds non-reproducible (Debian/Fedora
# reproducible-builds care). -trimpath already strips paths.
#
# VERSION is the single source of truth (root VERSION file); -X stamps it into
# internal/version.Version so no binary carries a hardcoded version literal.
# The package default must match VERSION for a bare `go build`.
VERSION := $(shell cat $(dir $(lastword $(MAKEFILE_LIST)))VERSION)
GOFLAGS := -trimpath -buildvcs=false -ldflags=-s\ -w\ -X\ github.com/sirmick/wash/internal/version.Version=$(VERSION) -tags netgo,osusergo

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
# SC (singlecall) holds the per-app STANDALONE real ELFs (out/singlecall/). The
# default/shipped MULTICALL layout — the `wash` dispatcher + wash-<app> symlinks —
# lives directly in out/. The two can't share a dir (a wash-<app> symlink vs a
# real ELF of the same name collide), so standalone is tucked under out/singlecall/.
SC      := $(OUT)/singlecall

# --- single source of truth: the app roster (CORE_AUDIT §2.2) ---------------
# Adding a windowed app is ONE line below (+ its apps/<app>/{fe,be} tree and a
# manifest icon). Each app's per-app web build, asset-embed stamp, binary rule,
# vendor-sync membership, multicall stamp, BINS+packaging entry, and multicall
# import are all DERIVED from these lists via the $(foreach)/$(eval) templates
# further down — no more ~6 hand-edited Makefile sites per app.
#
#   FE_APPS       windowed apps. Build apps/<app>/fe → embed into
#                 apps/<app>/be/assets → go build apps/<app>/be/cmd. The binary
#                 rule is stamp-gated (rebuild keys on the FE asset bundle), as
#                 windowed apps always have been.
#   FE_PANEL_APPS background services that ALSO ship a settings panel.js, so
#                 they embed FE assets like a windowed app — but their binary
#                 rule is .PHONY so a Go-only change still relinks (the FE stamp
#                 wouldn't catch it; see [[wash makefile phony goservice]]).
#   SVC_APPS      FE-less background services under apps/<app>/be/cmd: binary
#                 rule only (no web/embed), .PHONY for the same reason.
#
# Not in these lists (deliberately hand-written below — they aren't uniform
# apps): wash-router / wash-login embed the shell runtime; wash-launch /
# wash-fswatchd / wash-sudo are cmd/-rooted CLIs; wash-display is the C++/CMake
# compositor; the gated `test` app is woven in where TEST_APP applies.
FE_APPS := session about connect imageview term fm edit vscode-workbench \
           settings top disks journal syslogs services packages net \
           washamp music radio
FE_PANEL_APPS := vscode netd remote
SVC_APPS := bulk priv notify audio fswatch

# Every app that embeds an FE asset bundle (windowed + panel). Drives the
# embed-stamp / vendor-sync / multicall-stamp derivations. The gated `test` app
# is added to these where TEST_APP is set.
ASSET_APPS := $(FE_APPS) $(FE_PANEL_APPS)

# BINS is derived. wash-router / wash-login lead the list because vendor-sync
# relies on them being built first (they carry the shared /vendor chunks); then
# the app roster, then the hand-written CLIs. wash-sudo is appended below.
BINS := wash-router wash-login \
        $(addprefix wash-,$(FE_APPS)) \
        $(addprefix wash-,$(FE_PANEL_APPS)) \
        $(addprefix wash-,$(SVC_APPS)) \
        wash-launch wash-fswatchd

# wash-sudo is the CLI face of wash-priv (terminal `sudo`-like
# entrypoint that routes through the browser FE for unlock).
# Opt-out by setting WASH_NO_SUDO=1 — useful for headless / kiosk
# deploys that never need a terminal-driven sudo path. Default is
# on so existing dev flows are unaffected.
WASH_NO_SUDO ?=
ifeq ($(WASH_NO_SUDO),)
BINS += wash-sudo
endif

# OUT_ONLY_BINS are never folded into the multicall dispatcher and never become
# symlinks: they stay REAL in out/ in BOTH layouts (packaging installs them beside
# the dispatcher; the multicall image just reuses them in place). Everything else
# in BINS is a symlinkable app/CLI, so its standalone ELF builds into out/singlecall/.
OUT_ONLY_BINS := wash-login wash-sudo
SC_BINS       := $(filter-out $(OUT_ONLY_BINS),$(BINS))
TARGETS := $(addprefix $(SC)/,$(SC_BINS)) $(addprefix $(OUT)/,$(filter $(OUT_ONLY_BINS),$(BINS)))

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

# --- multicall imports single source --------------------------------------
# cmd/wash/imports_<app>.go is one blank-import per registry app: its init()
# registers the app into the multicall dispatcher (built with -tags=multicall).
# These are GENERATED from the app roster — gen-imports writes one file per app
# in FE_APPS / FE_PANEL_APPS / SVC_APPS (+ the gated test app), so adding an app
# is one roster line, not a new hand-written file. check-imports (wired into CI
# via unit-test) fails if they've drifted.
#
# Per-app tag `!no_app_<app>`: a build can drop one app with -tags=no_app_<app>
# (the name "-" → "_" so it's a valid Go tag; main.go suggests it on a missing
# app). The test app is special: file imports_apptest.go (a plain
# imports_test.go would be treated as a Go _test.go file) + an extra
# wash_test_app tag (off by default, set by TEST_APP=1 / the e2e build).
IMPORT_APPS := $(FE_APPS) $(FE_PANEL_APPS) $(SVC_APPS)
IMPORTS_DIR ?= cmd/wash
.PHONY: gen-imports check-imports
gen-imports:
	@rm -f $(IMPORTS_DIR)/imports_*.go
	@for app in $(IMPORT_APPS); do \
	  tag=`printf '%s' "$$app" | tr '-' '_'`; \
	  printf '//go:build multicall && !no_app_%s\n\n// Code generated by `make gen-imports` from the Makefile app roster; DO NOT EDIT.\npackage main\n\nimport _ "github.com/sirmick/wash/apps/%s/be"\n' "$$tag" "$$app" \
	    > $(IMPORTS_DIR)/imports_$$tag.go; \
	done
	@printf '//go:build multicall && !no_app_test && wash_test_app\n\n// Code generated by `make gen-imports` from the Makefile app roster; DO NOT EDIT.\npackage main\n\nimport _ "github.com/sirmick/wash/apps/test/be"\n' \
	  > $(IMPORTS_DIR)/imports_apptest.go
	@echo "gen-imports: wrote $(words $(IMPORT_APPS)) app imports + test → $(IMPORTS_DIR)/imports_*.go"

check-imports:
	@tmp=`mktemp -d`; trap 'rm -rf "$$tmp"' EXIT; \
	 $(MAKE) -s gen-imports IMPORTS_DIR="$$tmp" >/dev/null; \
	 mkdir "$$tmp/committed"; cp $(IMPORTS_DIR)/imports_*.go "$$tmp/committed/"; \
	 if diff -rq "$$tmp/committed" "$$tmp" --exclude=committed >/dev/null 2>&1; then \
	   echo "cmd/wash/imports_*.go in sync with app roster ($(words $(IMPORT_APPS)) apps + test)"; \
	 else \
	   echo "cmd/wash/imports_*.go stale vs app roster — run 'make gen-imports':"; \
	   diff -rq "$$tmp/committed" "$$tmp" --exclude=committed || true; exit 1; \
	 fi

# check-icons: assert every registered app's manifest icon(s) exist in the shell
# sprite (web/shell/build-icons.mjs). A missing icon renders blank at runtime
# with no error, so this catches it at build time. Implemented as a multicall-
# tagged Go test (cmd/wash/icons_test.go) where the registry is populated; it
# also runs as part of e2e-test's `go test -tags=multicall ./cmd/wash/...`.
.PHONY: check-icons
check-icons:
	@go test -count=1 -tags=multicall -run TestManifestIconsInSprite ./cmd/wash/

# check-design: the design-language drift guard. FE chrome is consolidated onto
# @wash/ui tokens so the theme packs can live-reswap every color; a raw hex
# that equals a token's fallback renders fine on the default pack but freezes
# on the others. This fails if such drift reappears. The hex set is derived
# from tokens.ts so the guard can't fall out of sync. Wired into unit-test.
.PHONY: check-design
check-design:
	@./scripts/check-design-tokens.sh

# check-versions: the version single-source guard. The root VERSION file is the
# master — the Makefile stamps it into every binary via -ldflags, and packaging
# (run_matrix.sh / make-source-tarball.sh) now defaults its package version to
# it. This asserts the version literals that AREN'T auto-derived — the Go
# bare-build default and the native-package metadata (deb changelog, rpm spec,
# alpine APKBUILD) — all match it, so a bump that misses a file fails CI instead
# of silently shipping a mismatched / unbuildable package (the drift that
# stranded rpm/apk at 0.9.1 while deb/binaries moved on). FE package.json
# versions and the README are cosmetic and intentionally not guarded. Wired into
# unit-test beside check-pkg-binaries / check-imports.
.PHONY: check-versions
check-versions:
	@v=`cat VERSION`; rc=0; \
	 ck() { if [ "$$2" != "$$v" ]; then echo "  version drift: $$1 is '$$2', expected '$$v'"; rc=1; fi; }; \
	 ck internal/version/version.go "`sed -n 's/^var Version = \"\([^\"]*\)\".*/\1/p' internal/version/version.go | head -1`"; \
	 ck debian/changelog            "`sed -n '1s/^wash (\([0-9.]*\)-.*/\1/p' debian/changelog`"; \
	 ck rpm/wash.spec               "`awk '/^Version:/{print $$2; exit}' rpm/wash.spec`"; \
	 ck alpine/APKBUILD             "`sed -n 's/^pkgver=//p' alpine/APKBUILD | head -1`"; \
	 if [ $$rc -eq 0 ]; then echo "check-versions: deb/rpm/apk + Go default all match VERSION ($$v)"; \
	 else echo "check-versions: align the above with the root VERSION file ($$v)"; exit 1; fi

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
TARGETS += $(SC)/wash-test
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
# WASH_DISPLAY_TARGET makes `make wash` (multicall) ALSO build the compositor when
# display is enabled — it's a separate native binary, never folded into the
# dispatcher, so it has to be named as an explicit prereq (it's not in BINS).
WASH_DISPLAY_TARGET := $(OUT)/wash-display
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

# Shared shell-runtime embed stamp. wash-router and wash-login embed the
# SAME bundle from one package (internal/shellassets), so the web stage
# stages web/shell/dist there ONCE and both binaries' go builds depend on
# this single stamp. (Previously the bundle was embedded twice: into
# internal/runner/router/assets, then copy-staged again into
# internal/login/assets/shell.)
SHELL_ASSETS := internal/shellassets/assets
SHELL_STAMP  := $(SHELL_ASSETS)/.stamp

# Per-app asset/stamp paths are uniform — apps/<app>/be/assets/.stamp — and are
# computed inline by the app-rule templates below, so no per-app *_ASSETS /
# *_STAMP variables are needed. (SHELL_STAMP above is the
# exception: it embeds the shell runtime, not an app FE bundle.)

.PHONY: all
all: $(TARGETS)

# ----- build verbs -----
# `make wash` builds the MULTICALL layout — the busybox `wash` dispatcher plus
# wash-<app> symlinks, emitted DIRECTLY into out/, which is exactly what the
# deb/rpm/apk packages ship. Dev defaults to multicall so the inner loop exercises
# the same argv[0]-dispatch + exec-probe paths as production (a standalone-only dev
# build is how the wash-fswatchd --wash-manifest gap hid — see commit ca73b4b).
# wash-sudo is a real separate helper (not folded into the dispatcher) and lives in
# out/ already, so build it first. With WASH_DISPLAY enabled the compositor
# (out/wash-display, a separate native binary) is built too. `make wash-standalone`
# is the per-app-binary layout under out/singlecall/ (the standalone-smoke gate and
# `run.sh --standalone` use it).
.PHONY: wash
wash: $(OUT)/wash-sudo $(WASH_DISPLAY_TARGET)
	$(MAKE) multicall
	@echo "wash: built the multicall layout (busybox wash + wash-* symlinks + wash-sudo$(if $(WASH_DISPLAY_TARGET), + wash-display) — the shipped layout)"
	@echo "  → $(abspath $(OUT))/   (run: make run)"

.PHONY: wash-standalone
wash-standalone: all
	@echo "wash-standalone: built $(words $(TARGETS)) per-app binaries$(if $(filter $(OUT)/wash-display,$(TARGETS)),  (incl. wash-display),  (no wash-display — set WASH_DISPLAY=1 or install wlroots))"
	@echo "  → $(abspath $(SC))/   (+ always-real wash-login/sudo/display in $(abspath $(OUT)))"

# wash-display: build just the native C++/CMake compositor (out/wash-display). It
# is never part of the multicall dispatcher and ships as a SEPARATE package, so it
# gets its own verb. `WASH_DISPLAY=1 make wash` builds it alongside the layout;
# `make wash-display` forces it on its own (vendored wlroots compile if no system
# one — see WLROOTS_VENDORED).
.PHONY: wash-display
wash-display: $(OUT)/wash-display
	@echo "wash-display: built $(abspath $(OUT))/wash-display (compositor)"

# Back-compat alias: `make wash-multicall` == `make wash` now (both build the
# multicall layout in out/). Kept so existing scripts/muscle-memory keep working.
.PHONY: wash-multicall
wash-multicall: wash

$(OUT):
	mkdir -p $(OUT)

$(SC):
	mkdir -p $(SC)

# ----- web stage -----

# pnpm install once; subsequent runs are fast no-ops.
.PHONY: web-deps
web-deps:
	@$(PNPM) install --silent

.PHONY: web-shell
web-shell: web-deps
	@$(PNPM) --filter @wash/shell run build

# Per-app `web-<app>` targets are generated by the app-rule templates below
# (web-shell above is the router's shell bundle; web-display is hand-written
# near the wash-display rule). embed-into-cmd helper. Usage: $(call embed,<src dist dir>,<dst assets dir>)
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

$(SHELL_STAMP): web-shell
	$(call embed_dist,web/shell/dist,$(SHELL_ASSETS))

# ----- app-rule templates (CORE_AUDIT §2.2) -----
# One $(foreach)/$(eval) pass turns the FE_APPS / FE_PANEL_APPS / SVC_APPS lists
# into the per-app web build, embed stamp, and binary rules that used to be
# hand-maintained ~6 sites each. Modelled on the PKG_LEAF_RULE
# $(foreach …,$(eval …)) idiom further down. `$$` defers expansion to rule-eval
# time; `$(1)` is the app name substituted by $(call) at generation time.

# FE build + asset-embed stamp (windowed / panel / the gated test app).
define web_embed_rule
.PHONY: web-$(1)
web-$(1): web-deps
	@$$(PNPM) --filter @wash/app-$(1) run build

apps/$(1)/be/assets/.stamp: web-$(1)
	$$(call embed_dist,apps/$(1)/fe/dist,apps/$(1)/be/assets)
endef

# Windowed-app binary: stamp-gated (the FE bundle is the rebuild key, as
# windowed apps have always been — deliberately NOT .PHONY). vendor-sync runs
# first so a targeted rebuild can't pair a fresh app bundle with a stale
# /vendor carrier (see the vendor-sync rule).
define fe_bin_rule
$$(SC)/wash-$(1): apps/$(1)/be/assets/.stamp vendor-sync | $$(SC)
	$$(call go_build,$$@,apps/$(1)/be/cmd)
endef

# Panel-service binary: embeds a settings panel.js like a windowed app, but
# .PHONY so a Go-only change still relinks ([[wash makefile phony goservice]]).
define panel_bin_rule
.PHONY: $$(SC)/wash-$(1)
$$(SC)/wash-$(1): apps/$(1)/be/assets/.stamp vendor-sync | $$(SC)
	$$(call go_build,$$@,apps/$(1)/be/cmd)
endef

# FE-less Go service: binary rule only (no web/embed), .PHONY for the same
# FE-less-Go reason.
define svc_bin_rule
.PHONY: $$(SC)/wash-$(1)
$$(SC)/wash-$(1): | $$(SC)
	$$(call go_build,$$@,apps/$(1)/be/cmd)
endef

$(foreach a,$(FE_APPS) $(FE_PANEL_APPS) test,$(eval $(call web_embed_rule,$(a))))
$(foreach a,$(FE_APPS) test,$(eval $(call fe_bin_rule,$(a))))
$(foreach a,$(FE_PANEL_APPS),$(eval $(call panel_bin_rule,$(a))))
$(foreach a,$(SVC_APPS),$(eval $(call svc_bin_rule,$(a))))

# ----- shared-vendor coherence guard -----
#
# App FE bundles EXTERNALIZE the shared deps (@wash/ui, solid-js,
# xterm): at runtime the shell's import map resolves them to /vendor/*
# files that ship inside wash-router and wash-login (both embed the
# shared internal/shellassets bundle) — see web/shell/build-vendor.mjs.
# So a targeted `make out/wash-<app>`
# that picks up changed web/lib sources pairs a fresh app bundle with
# a router still serving the OLD vendor chunk, and the app dies at
# load with "module '@wash/ui' does not provide an export named …".
# This guard rebuilds the vendor carriers first whenever the vendor
# inputs are newer than the wash-router binary. In a full `make` it's
# a no-op: wash-router/wash-login lead BINS and are already fresh by
# the time any app binary's prerequisites run.
.PHONY: vendor-sync
vendor-sync:
	@if [ ! -f $(SC)/wash-router ] || \
	   [ -n "$$(find web/lib/src web/lib/package.json web/shell/build-vendor.mjs -newer $(SC)/wash-router -print -quit)" ]; then \
		echo "== web/lib newer than $(SC)/wash-router: rebuilding shared /vendor carriers (wash-router, wash-login) first"; \
		$(MAKE) --no-print-directory $(SC)/wash-router $(OUT)/wash-login; \
	fi

# Every app binary that embeds an FE bundle externalizing shared deps gets a
# vendor-sync prereq inline (see the fe_bin_rule / panel_bin_rule templates).
# wash-display embeds its settings panel the same way but is hand-written below,
# so it's listed here.
$(OUT)/wash-display: vendor-sync

# ----- go stage -----

$(SC)/wash-router: $(SHELL_STAMP) | $(SC)
	$(call go_build,$@,cmd/wash-router)

# The per-app windowed/panel/service binary rules (wash-session, wash-about,
# wash-fm, wash-bulk, wash-vscode, …) are generated by the app-rule templates
# in the web-stage section above. Only the non-app specials remain hand-written
# here: wash-router (above), wash-display (below, C++/CMake), and the cmd/-rooted
# CLIs wash-fswatchd / wash-mount / wash-launch / wash-login / wash-sudo.

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

# wash-fswatchd is the B-side watch daemon for wash-to-wash mounts: it runs
# inotify on the remote wash host and streams change events over ssh stdio to
# the mounting host (the "wash channel"; SFTP carries the bytes). inotify-only,
# no FUSE dependency, so it ships on every wash host. .PHONY for the same
# FE-less-Go-binary reason as wash-notify.
.PHONY: $(SC)/wash-fswatchd
$(SC)/wash-fswatchd: | $(SC)
	$(call go_build,$@,cmd/wash-fswatchd)

# wash-mount is the OPTIONAL standalone FUSE mount CLI (needs the FUSE kmod +
# fusermount3 at runtime — absent in the in-browser VM and locked-down hosts).
# Kept out of BINS/packaging like wash-display; the mount LIBRARY ships inside
# wash-remote. Build explicitly: `make $(OUT)/wash-mount` or `go build ./cmd/wash-mount`.
.PHONY: $(OUT)/wash-mount
$(OUT)/wash-mount: | $(OUT)
	$(call go_build,$@,cmd/wash-mount)

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
$(SC)/wash-launch: | $(SC)
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
$(OUT)/wash-login: $(SHELL_STAMP) | $(OUT)
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
# Adding an app: add it to the app roster (FE_APPS / FE_PANEL_APPS / SVC_APPS at
# the top) and run `make gen-imports`; its multicall import + asset stamp follow
# automatically — no edits here.
# Note SHELL_STAMP: cmd/wash imports internal/runner/router and
# internal/runner/login → internal/login, both of which now embed the
# shared internal/shellassets bundle. Without this stamp in multicall's
# dep list, a clean checkout fails to compile with "pattern all:assets:
# no matching files found" — local dev accidentally works because the
# standalone wash-router build rule already chains through SHELL_STAMP.
# The app stamps are every FE-bundle-embedding app (ASSET_APPS); the
# gated test app is added below.
MULTICALL_STAMPS := $(SHELL_STAMP) \
                    $(foreach a,$(ASSET_APPS),apps/$(a)/be/assets/.stamp)

# Adding wash_test_app to the tags pulls the test app's blank-import
# in (which is otherwise excluded by cmd/wash/imports_apptest.go's
# wash_test_app build constraint). Mirrors the standalone TEST_APP=1
# convention.
MULTICALL_TAGS := multicall,netgo,osusergo
ifneq ($(TEST_APP),)
MULTICALL_TAGS := $(MULTICALL_TAGS),wash_test_app
MULTICALL_STAMPS += apps/test/be/assets/.stamp
endif
# WASH_VMLOGIN=1 compiles in the `wash-vmlogin` dispatch (internal/runner/vmlogin
# → wash-vm/guest, the in-browser/wemu VM login). OFF by default: the distro
# source tarball prunes wash-vm/, so the packaged multicall must build without
# it. The VM image build (wash-vm/image/rootfs/build.sh) sets WASH_VMLOGIN=1.
ifneq ($(WASH_VMLOGIN),)
MULTICALL_TAGS := $(MULTICALL_TAGS),washvmlogin
endif

# .PHONY: the stamps key only on FE assets, so a Go-source change (router,
# vmlogin, any app BE) would otherwise leave out/wash — and thus the baked VM
# image — stale (the FE-less-Go gotcha). Always re-link; Go's cache keeps it
# cheap when nothing changed.
.PHONY: $(OUT)/wash
$(OUT)/wash: $(MULTICALL_STAMPS) | $(OUT)
	$(GO_ENV) go build -trimpath -buildvcs=false -ldflags="-s -w" \
	  -tags=$(MULTICALL_TAGS) \
	  -o $@ ./cmd/wash && chmod 0755 $@

# multicall: assemble the busybox layout DIRECTLY in out/ (the default/shipped
# layout) — the `wash` dispatcher (out/wash) + a wash-<app> symlink per app/CLI.
# No collision: the standalone per-app real ELFs live separately in out/singlecall/
# (the out-split, inverted). The always-real binaries (wash-sudo, wash-login,
# wash-priv-fakesudo, wash-display) already sit in out/ from their own rules and
# are NOT in install-symlinks' name list, so they're left untouched in place — no
# copy step needed. install-symlinks refuses to clobber a non-symlink anyway.
.PHONY: multicall
# Depends on the always-real OUT_ONLY_BINS too: the recipe's comment assumes
# wash-login/wash-sudo "already sit in out/ from their own rules", but on a
# CLEAN tree (fresh worktree / fresh CI checkout) nothing has built them yet,
# so the shipped layout would be incomplete and every login/priv/services/
# packages e2e test fails with "missing binary: out/wash-login". install-
# symlinks refuses to clobber a non-symlink, so the real ELFs are left in place.
multicall: $(OUT)/wash $(OUT)/wash-login $(OUT)/wash-sudo
	./$(OUT)/wash install-symlinks ./$(OUT)

# Cross-compile-friendly variant: builds the multicall binary but
# does NOT run it (no install-symlinks). Used by wash-vm/image/
# rootfs/build.sh when GOARCH=riscv64 — the wash-router/wash-* /usr/
# bin entries inside the rootfs are created by build.sh's own
# symlink loops, and `$(OUT)/wash install-symlinks` would fail when
# the binary's arch differs from the host's (no qemu-user binfmt).
.PHONY: multicall-bin
multicall-bin: $(OUT)/wash

# package-tree: exactly the binaries the native packages (deb/rpm/apk) install.
# The multicall dispatcher (out/wash) holds the Go runtime + wash SDK + crypto/tls
# ONCE; every wash-<app> ships as a symlink to it (created at package time from
# packaging/wash.binaries). Two binaries MUST stay standalone and are built here
# alongside it:
#   - wash-login: its file-capabilities (cap_setuid,cap_setgid,cap_kill) live on
#     the inode, so folding it into the shared multicall would leak setuid to
#     every applet. Keep it a separate binary with its own caps.
#   - wash-sudo:  not part of the multicall (real separate helper).
# wash-display (cgo/wlroots) is built separately when WASH_DISPLAY=1.
.PHONY: package-tree
package-tree: $(OUT)/wash $(OUT)/wash-login $(OUT)/wash-sudo
	@echo "package-tree: out/wash (multicall) + out/wash-login + out/wash-sudo ready"

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
# The Alpine guest runs wash-vmlogin as its login front (build-vm-image-alpine.sh),
# so the baked multicall MUST include the washvmlogin dispatch — build out/wash
# with WASH_VMLOGIN=1 (a sub-make, since MULTICALL_TAGS is fixed at parse time).
# Without it the guest's wash-vmlogin is a no-op and the VM never serves a login.
.PHONY: vm-image
vm-image:
	$(MAKE) WASH_VMLOGIN=1 $(OUT)/wash
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

# mdns-test: the wash-discovery real-multicast gate (docs/DISCOVERY.md) — opens
# a live mDNS socket on this host, advertises, and browses for its own
# announcement. Opt-in (not in the default unit tier) because a sandbox may
# have a multicast iface that drops loopback delivery, which would hang/flake
# the gate. Skips cleanly when no multicast socket is available.
.PHONY: mdns-test
mdns-test:
	WASH_MDNS_INTEGRATION=1 go test ./internal/mdns/ -run TestLoopbackDiscovery -v -count=1

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

# e2e-remote-vm: the wash-remote (R2) two-VM capstone (docs/REMOTE.md). Boots
# VM-A (desktop) + VM-B (ssh host) on a shared mcast L2 and drives wash-connect
# to SSH into B and composite a B app window into A — proving the real ssh
# bring-up the host-process connect-launch spec stubs out. Needs qemu + /dev/kvm
# + the openssh-baked image (RENDER_VER bump); self-skips otherwise.
.PHONY: e2e-remote-vm
e2e-remote-vm: vm-image vm-chrome $(OUT)/washvm-remote-run
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) exec playwright test remote-vm

# e2e-mount-vm: the wash-to-wash MOUNT capstone (docs/MOUNT.md). Same two-VM rig
# as e2e-remote-vm; from A's desktop it mounts one of B's folders (real SFTP +
# FUSE), browses it in a local fm, and asserts a B-side change propagates live
# via the shared watch service — plus a torture test co-driving one folder from
# a local fm (the mount) and a remote fm (on B). Needs the fuse3-baked image.
.PHONY: e2e-mount-vm
e2e-mount-vm: vm-image vm-chrome $(OUT)/washvm-remote-run
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) exec playwright test mount-vm

.PHONY: $(OUT)/washvm-remote-run
$(OUT)/washvm-remote-run: | $(OUT)
	$(call go_build,$@,cmd/washvm-remote-run)

# run-vm: boot the baked image and serve the wash UI for manual poking.
.PHONY: run-vm
run-vm: vm-image vm-chrome $(OUT)/washvm-run
	$(OUT)/washvm-run --chrome $(VM_CHROME) --addr 127.0.0.1:8080

# run-remote-vm: boot both VMs (desktop + ssh host) + serve VM-A for manual
# wash-connect poking against a real second host. The interactive sibling of
# e2e-remote-vm.
.PHONY: run-remote-vm
run-remote-vm: vm-image vm-chrome $(OUT)/washvm-remote-run
	$(OUT)/washvm-remote-run --chrome $(VM_CHROME) --addr 127.0.0.1:8080

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

# debian-packages: the Debian .debs both arches that downstreams actually ship —
# amd64 (Proxmox LXC template) + arm64 (MikroTik OCI image). The homezone build
# scripts consume these straight from dist/packages/debian-13-{amd64,arm64}; this
# is the one verb to (re)build them locally. arm64 runs under qemu (slow but
# hermetic). riscv64 is intentionally excluded — no downstream consumes it.
.PHONY: debian-packages
debian-packages:
	$(MAKE) amd64-debian13-wash-package
	$(MAKE) arm64-debian13-wash-package

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
	rm -rf apps/*/be/assets cmd/*/assets internal/apps/*/assets internal/runner/*/assets internal/login/assets/shell internal/shellassets/assets
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
	$(MAKE) -s check-imports
	$(MAKE) -s check-versions
	$(MAKE) -s check-design
	go vet ./...
	go test -count=1 -p 1 -timeout 120s $(GO_UNIT_PKGS)

# test-race: the Go unit suite under the race detector. The concurrency-dense
# core (router, sdk, fswatch, remotewatch, washmount, bulkops, the per-app
# backends) is clean today; this gate keeps it that way — a future change that
# introduces a data race on a path the tests exercise fails here instead of
# flaking in production. Same package set and -p 1 constraint as unit-test
# (loopback wires router+sdk over in-memory pipes and mustn't race other
# in-process tests for goroutine scheduling); wash-vm/vm is excluded with the
# rest via GO_UNIT_PKGS (its kvm suite boots real qemu and runs minutes under
# -race). Longer timeout: -race adds ~2-10x runtime. Built like unit-test —
# app packages //go:embed assets only produced under TEST_APP=1.
.PHONY: test-race
test-race: test-app
	go test -race -count=1 -p 1 -timeout 600s $(GO_UNIT_PKGS)

# e2e-test: the full Playwright suite (standalone layout); builds the test app.
# e2e/ is NOT a workspace member, so --ignore-workspace is required to install
# its own deps (incl. playwright) into e2e/node_modules.
.PHONY: e2e-test
e2e-test: test-app
	# The multicall layout is what ships, so the FULL suite runs against it
	# (busybox-style wash-<app> symlinks → out/wash, directly in out/). Build the
	# FULL layout — not just the dispatcher — because the fixture resolves every
	# wash-<app> under out/ (its existence checks + binPath read there). `make
	# multicall` (re)installs the symlinks from the current dispatcher, so a
	# newly-added app (e.g. wash-fswatch, wash-imageview) is always present;
	# building bare out/wash leaves a stale layout and the fixture fails with
	# "missing binary: out/wash-<app>". Then run the argv[0]-dispatch unit tests
	# and Playwright with WASH_E2E_MULTICALL=1.
	$(MAKE) TEST_APP=1 multicall
	go test -count=1 -tags=multicall ./cmd/wash/...
	cd e2e && $(PNPM) install --ignore-workspace --silent
	cd e2e && $(PNPM) exec playwright install chromium
	# WASH_E2E_SKIP_VM=1: mirror CI (which has no VM artifacts) — the heavy
	# KVM-backed net-vm tiers run under `make net-test` / `make e2e-vm`.
	cd e2e && WASH_E2E_SKIP_VM=1 WASH_E2E_MULTICALL=1 $(PNPM) test

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

# all-test: every test, BOTH layouts. Does NOT package. The MULTICALL layout is
# what ships now, so it gets the full suite (e2e-test). standalone-smoke covers
# the per-app-binary layout's UNIQUE risk surface — separate-process spawn of
# out/wash-<app> — NOT a re-run of the whole suite (app logic is identical).
# Build the 29 binaries + a launch/spawn + single-file spec against them.
.PHONY: standalone-smoke
standalone-smoke: test-app
	$(MAKE) TEST_APP=1 all
	cd e2e && $(PNPM) install --ignore-workspace --silent && $(PNPM) exec playwright install chromium
	cd e2e && $(PNPM) exec playwright test single-file.spec.ts kiosk-test-app.spec.ts --workers=1

# all-test: the full suite ONCE against the MULTICALL layout (e2e-test) + a
# standalone smoke + the kvm net/disks gates (which boot the multicall layout
# in-VM). No full-suite duplication across layouts.
.PHONY: all-test
all-test: unit-test e2e-test standalone-smoke net-test disks-test
	@echo "all-test: green — multicall full (e2e) + standalone smoke + kvm VM gates"

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
# Go or app sources still requires re-running `make dev`. The router is
# the multicall layout (out/), matching `make run`.
.PHONY: dev
dev: wash
	@echo "wash dev: multicall router :11000 + Vite :5173 — open http://localhost:5173/"
	@trap 'kill 0' INT TERM EXIT; \
	  ( $(OUT)/wash-router ) & \
	  ( $(PNPM) --filter @wash/shell run dev ) & \
	  wait
