# wash — complete command inventory (make + scripts)
# (current commands as they work TODAY. The goal-oriented make redesign
#  we're moving to is in MAKE-PLAN.md. Ports: login 10000 / router 11000 /
#  browser-vm 12000 / qemu-vm 13000.)

## OVERLAP MAP (what wraps what — the redundancy to simplify)
./test.sh   ──calls──▶ ./build.sh ──calls──▶ make all
./run.sh    ──calls──▶ ./build.sh ──calls──▶ make all
./clean.sh  ════════is════════════════════▶ make clean   (make clean → ./clean.sh)
make distclean ═════is════════════════════▶ ./clean.sh --all
make test-all ─is──▶ ./test.sh --both --distro --vm --vm-gates
make packages ─is──▶ ./packaging/run_matrix.sh
make verify   ─is──▶ make all + go vet + go test ./...  (overlaps ./test.sh --no-e2e)
make vm / rv  ─is──▶ make -C wash-vm/image all
# So the real top-level verbs are just: build / test / run / clean / package / vm.

# ───────────────────────────── SCRIPTS ─────────────────────────────
# [entry] = you run it directly.  [internal] = called by a make target, not by hand.

[entry]    ./build.sh                 build binaries → out/   (--standalone|--multicall|--both|--clean|--no-test-app|--no-sudo|-j N)
[entry]    ./test.sh                  build + test   (--both|--no-build|--no-unit|--no-lint|--no-e2e|--distro|--only-distro|--vm|--vm-gates|--coverage|--filter|--workers)
[entry]    ./run.sh                   build + run router :11000   (--standalone|--multicall|--both|--no-build|--listen|--fm-seed|--tail)
[entry]    ./clean.sh                 wipe artifacts   (--tmp|--docker|--deep|--all|--dry-run)
[entry]    wash-vm/run-browser.sh     in-browser RISC-V VM dev server → :12000   (PORT=)
[entry]    wash-vm/run-qemu.sh        host qemu microvm + proxy, same UI → :13000   (extra args → qemu)
[entry]    packaging/run_matrix.sh    deb/rpm/apk matrix directly   (WASH_PKG_DISPLAY=, WASH_PKG_JOBS=)
[entry]    packaging/make-source-tarball.sh   produce the source-only tarball
[entry]    scripts/dev-restart.sh     kill+rebuild+restart live router on :11000
[entry]    scripts/dev-kill.sh        kill every wash process this user runs
[entry]    scripts/fm-seed.sh         seed an fm test fixture tree
[entry]    scripts/seed-bulk-fixture.sh   seed the bulk-op fixture
[internal] scripts/build-display.sh           builds wash-display (called by make web-display / display build)
[internal] scripts/build-vm-image-alpine.sh   ← make vm-image
[internal] scripts/build-vm-image-ubuntu.sh   ← make vm-image-ubuntu
[internal] scripts/build-vm-image-debian.sh   ← make vm-image-debian
[internal] scripts/build-vm-image-fedora.sh   ← make vm-image-fedora
[internal] scripts/build-vm-image-openwrt.sh  ← make vm-image-openwrt
[internal] scripts/lib/wash-vm-payload.sh     shared lib for the vm-image scripts
[internal] wash-vm/image/rootfs/build.sh      ← make -C wash-vm/image rootfs
[internal] wash-vm/image/rootfs/post-build.sh ← buildroot post-build hook
[guest]    wash-vm/tinyemu/netinit.sh, wash-vm/image/rootfs/overlay/etc/profile.d/wash.sh   run INSIDE the VM
[vendored] wash-display/third_party/wlroots/*.sh   upstream wlroots build scripts (not ours)

# ───────────────────────────── MAKE TARGETS (all 53) ─────────────────────────────
# BUILD
make all                build everything: FE (Vite) + Go binaries, standalone layout
make multicall          single out/wash binary + install wash-* symlinks
make multicall-bin      multicall binary only (no symlinks; cross-compile safe)
make linux-amd64        cross-compile core for amd64
make linux-arm64        cross-compile core for arm64
make linux-riscv64      cross-compile core for riscv64
make vendor-sync        sync vendored web runtime (web/shell/public/vendor)
make test-app           build + the wash-test app (TEST_APP=1 all)
make wash-login-caps    set file caps on the wash-login binary
make wash-login-deploy  deploy multi-user wash-login

# BUILD — FE per package/app (23 targets)
make web-deps           install FE deps (pnpm) — prereq of all web-* below
make web-shell          shell bundle
make web-about          \
make web-disks           |
make web-edit            |
make web-fm              |
make web-journal         |
make web-music           |
make web-net             |
make web-netd            |
make web-packages        |  one per app — build that app's FE bundle
make web-radio           |  into apps/<name>/be/assets/ (for //go:embed)
make web-services        |
make web-session         |
make web-settings        |
make web-syslogs         |
make web-term            |
make web-test            |
make web-top             |
make web-vscode          |
make web-vscode-workbench |
make web-washamp         |
make web-display        /   (wash-display settings panel.js)

# TEST
make verify             build + go vet + go test ./... + static-link check
make test-all           EVERYTHING (= ./test.sh --both --distro --vm --vm-gates, WASH_PKG_DISPLAY=1)
make e2e                build test-app + full Playwright suite
make e2e-vm             VM net e2e gate (net-vm-gate + net-vm-multi; needs kvm)
make net-matrix         network segmentation gate (OpenWRT microvms; needs openwrt.img)
make vm-net-test        per-distro backend read/apply (netplan/ifupdown/networkd; needs distro images)
make vm-disks-test      real-kernel storage gate (md/LVM/btrfs; needs alpine vm-image)

# PACKAGE
make packages           deb/rpm/apk matrix, parallel (WASH_PKG_DISPLAY=1 adds display; WASH_PKG_JOBS=N concurrency)

# VM IMAGES
make vm-image           alpine microvm (kernel+initramfs) → net e2e / disks
make vm-image-ubuntu    per-distro net-test image (netplan)
make vm-image-debian    per-distro net-test image (ifupdown)
make vm-image-fedora    per-distro net-test image (networkd)
make vm-image-openwrt   OpenWRT router image → net-matrix / net-demo
make vm                 in-browser RISC-V VM artifacts (kernel+fw+rootfs+wasm) → wash-vm/web/public/tinyemu/
make rv                 alias of make vm
make vm-chrome          minimal host chrome served by run-vm / e2e-vm

# RUN / SERVE
make dev                router :11000 + Vite HMR :5173 → http://localhost:5173/
make run-vm             boot baked alpine image + serve UI on 127.0.0.1:8080
make net-demo           3 OpenWRT microvms w/ browser consoles → :8001/:8002/:8003

# CLEAN
make clean              → ./clean.sh (wipe build artifacts)
make distclean          → ./clean.sh --all (artifacts + node_modules + docker + go cache)

# ───────────────────────────── TYPICAL FLOWS ─────────────────────────────
./build.sh && ./run.sh --no-build      # build, then run the router (:11000)
make dev                               # fastest FE iteration (HMR :5173)
./test.sh                              # everyday: build + lint + unit + e2e
./clean.sh && make test-all            # full from-scratch verification of every tier
make vm && wash-vm/run-browser.sh      # build + serve the in-browser RISC-V VM (:12000)
