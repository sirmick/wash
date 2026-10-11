# wash — make commands (the one interface)

Everything is a `make` verb. Ports (defaults live in the binaries): wash-login
**10000** · wash-router **11000** · browser-vm **12000** · qemu-vm **13000**.

## BUILD
```
make wash               multicall layout → out/  (busybox wash + wash-* symlinks; the shipped layout; auto-includes wash-display when wlroots present, WASH_DISPLAY=0/1 overrides)
make wash-standalone    per-app ELFs → out/singlecall/  (one standalone binary per app)
make wash-display       just the native C++/CMake compositor → out/wash-display
make wash-multicall     alias of `make wash`
```

## RUN / DEV
```
make run                run the LAST-BUILT router → https://localhost:11000/?token=… (URL printed at startup; does NOT rebuild — run `make wash` to refresh)
make dev                router :11000 + Vite HMR → http://localhost:5173/      (auto-rebuilds FE; the iteration loop)
```

## TEST   (each builds its own prereqs)
```
make unit-test          go vet + go test ./... (excl wash-vm/vm) + FE-unit (node --test) + component (vitest) + the check-* drift guards
make test-race          the same Go unit packages under -race
make e2e-test           full Playwright suite (multicall layout — the one that ships)
make standalone-smoke   per-app-binary layout's unique surface: build out/singlecall/ + launch/spawn e2e specs against it
make browser-vm-test    boot the in-browser RISC-V VM in chromium, assert the desktop mounts (self-skips without `make browser-image-vm`)
make net-test           net-matrix + vm-net-test + net-vm e2e   (builds openwrt+distro images; needs /dev/kvm)
make disks-test         vm-disks-test real-kernel storage gate   (builds alpine image; needs /dev/kvm)
make all-test           unit + e2e + standalone-smoke + net + disks
make coverage           instrumented build → merged go-unit + e2e coverage report under coverage/
make verify             quick go-only gate: go vet + go test + static-ELF check
make test-all           all-test + all-package (the whole pyramid)
make screenshots        regenerate docs/screenshots/*.png (Playwright capture in e2e/capture/; NOT part of e2e-test; the display shot needs out/wash-display + xclock)
```

## PACKAGE   (hermetic in Docker; live per-row progress; WASH_PKG_JOBS=N concurrency)
```
make all-package                 the whole matrix (WASH_PKG_DISPLAY=1 adds the display leaves) → dist/packages/
make <arch>-<platform>-<pkg>-package     one leaf (17):
    wash:    {amd64,arm64,riscv64} × {ubuntu24,debian13,alpine321}  +  {amd64,arm64} × fedora40
    display: {amd64,arm64} × {ubuntu24,debian13,fedora40}    (deb/rpm only)
    e.g.  make amd64-ubuntu24-wash-package    make arm64-fedora40-display-package
make openwrt-smoke               OpenWRT runtime smoke (opkg/procd; no .ipk)
make gen-pkg-binaries            regenerate packaging/wash.binaries from BINS (the one list deb/rpm/apk install) — run after adding an app
make check-pkg-binaries          fail if packaging/wash.binaries drifted from BINS (runs inside unit-test/CI so a new app can't miss the packages)
make verify-packages             download CI-built packages from GH + install/boot-smoke each on a clean distro container (needs docker + gh; amd64 only; ROWS="ubuntu24 alpine321" to subset)
make run-package                 install a CI-built package in a clean container + SERVE the packaged desktop on a published port to browse it (ROW=ubuntu24 PORT=11000; -no-auth; Ctrl-C stops)
```

## VM — IMAGES  (`<platform>-image-vm`)
```
make alpine-image-vm    host microvm (disks-test + qemu surface)   [bakes the multicall layout]
make ubuntu-image-vm    \
make debian-image-vm     | per-distro net-test images
make fedora-image-vm    /
make openwrt-image-vm   OpenWRT router image (net-matrix / net-demo)
make browser-image-vm   in-browser riscv VM artifacts (kernel+fw+rootfs+wasm) → wash-vm/web/public/tinyemu/
```

## VM — RUN  (`<platform>-run-vm`; each builds its image first)
```
make browser-run-vm     build + serve the in-browser RISC-V VM → http://localhost:12000   (login wash/wash)
make qemu-run-vm        host qemu microvm + serve → http://localhost:13000                (login wash/wash)
make net-demo           3 OpenWRT microvms + browser consoles → :8001-8003
```

## CLEAN
```
make clean              in-tree build artifacts (out/, FE dist, embedded assets, dist/, wash-display build)
make tmp-clean          + /tmp/wash-* runtime junk (sockets kept)
make docker-clean       + the matrix Docker images + buildx cache
make all-clean          everything (+ node_modules + Go build cache)   [spares tmp/ branches/ harbor.config]
```

## PUSH
```
make push               run what CI runs (unit + test-race + e2e + the 4 amd64 packages + openwrt-smoke),
                        then `git push` ONLY if all green.  ARGS= for push args, e.g. make push ARGS="origin HEAD"
```

## CI  (.github/workflows/)
- **ci.yml** — on push/PR: `unit` (make unit-test + test-race) + `e2e` (make e2e-test + standalone-smoke) in
  parallel → `package` (needs tests: amd64 ubuntu/debian/fedora/alpine + arm64 debian wash packages + openwrt-smoke)
  → `release` (publishes wash + wash-login .deb/.rpm/.apk to the Releases page **only on a `vX.Y.Z` tag**, under
  stable names `wash[-login]-<distro>-<arch>.<ext>` plus the native versioned names).
- **demo.yml** — rebuilds + deploys the GitHub-Pages browser-VM demo on **every** push to main (PRs build-only).
- **prebuild.yml** — caches the VM kernel/firmware/wasm blobs; fires only on `wash-vm/image/{firmware,kernel,wasm}` / `tinyemu` changes.

## STILL SCRIPTS (engines make calls / dev helpers — not the interface)
```
packaging/run_matrix.sh         the package matrix engine (the package verbs call it)
packaging/make-source-tarball.sh
wash-vm/run-browser.sh          served by make browser-run-vm (:12000)
wash-vm/run-qemu.sh             served by make qemu-run-vm (:13000)
run.sh                          richer dev router launcher (--fm-seed / --tail / --listen)
scripts/dev-restart.sh          kill+rebuild+restart the live router
scripts/dev-kill.sh             kill every wash process
scripts/host/wash-install [REF] clone/toolchain/build REF, systemd --user unit on :10000; re-run = update  (make install-host-scripts symlinks both)
scripts/host/wash-connect       print that router's URL (with token) for another machine, and what it serves
scripts/fm-seed.sh  scripts/seed-bulk-fixture.sh
```

## TYPICAL FLOWS
```
make wash && make run               # build, then run the router (:11000) — run won't rebuild
make dev                            # fastest FE iteration (HMR :5173)
make unit-test                      # everyday Go+FE check
make all-clean && make all-test     # full from-scratch verification of every tier
make browser-run-vm                 # build + serve the in-browser RISC-V VM (:12000, login wash/wash)
make amd64-ubuntu24-wash-package    # one native package
make push                           # CI-equivalent gate, then git push if green
```
