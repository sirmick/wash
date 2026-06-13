# wash — make commands (the one interface)

Everything is a `make` verb. Ports (defaults live in the binaries): wash-login
**10000** · wash-router **11000** · browser-vm **12000** · qemu-vm **13000**.

## BUILD
```
make wash               standalone build → out/  (auto-includes wash-display when wlroots present; WASH_DISPLAY=0/1 overrides)
make wash-multicall     busybox layout → out/multicall/  (one wash binary + wash-* symlinks)
```

## RUN / DEV
```
make run                run the LAST-BUILT router → http://localhost:11000/   (does NOT rebuild — run `make wash` to refresh)
make dev                router :11000 + Vite HMR → http://localhost:5173/      (auto-rebuilds FE; the iteration loop)
```

## TEST   (standalone by default; each builds its own prereqs)
```
make unit-test          go vet + go test ./... (excl wash-vm/vm) + FE-unit (node --test) + component (vitest)
make e2e-test           full Playwright suite (standalone layout)
make multicall-smoke    multicall layout's unique surface: build it + go test -tags=multicall ./cmd/wash + bundle/launch e2e specs
make net-test           net-matrix + vm-net-test + net-vm e2e   (builds openwrt+distro images; needs /dev/kvm)
make disks-test         vm-disks-test real-kernel storage gate   (builds alpine image; needs /dev/kvm)
make all-test           unit + multicall-smoke + e2e + net + disks  (the VM tiers boot multicall in-VM = deep multicall coverage)
make coverage           instrumented build → merged go-unit + e2e coverage report under coverage/
make verify             quick go-only gate: go vet + go test + static-ELF check
make test-all           all-test + all-package (the whole pyramid)
```

## PACKAGE   (hermetic in Docker; live per-row progress; WASH_PKG_JOBS=N concurrency)
```
make all-package                 the whole matrix (WASH_PKG_DISPLAY=1 adds the display leaves) → dist/packages/
make <arch>-<platform>-<pkg>-package     one leaf (17):
    wash:    {amd64,arm64,riscv64} × {ubuntu24,debian13,alpine321}  +  {amd64,arm64} × fedora40
    display: {amd64,arm64} × {ubuntu24,debian13,fedora40}    (deb/rpm only)
    e.g.  make amd64-ubuntu24-wash-package    make arm64-fedora40-display-package
make openwrt-smoke               OpenWRT runtime smoke (opkg/procd; no .ipk)
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
make push               run exactly what CI runs (unit + e2e + the 4 amd64 packages + openwrt-smoke),
                        then `git push` ONLY if all green.  ARGS= for push args, e.g. make push ARGS="origin HEAD"
```

## CI  (.github/workflows/)
- **ci.yml** — on push/PR: `unit` (make unit-test) + `e2e` (make e2e-test) in parallel → `package` (needs tests:
  amd64 ubuntu/debian/fedora/alpine wash packages + openwrt-smoke) → `release` (publishes .deb/.rpm/.apk to the
  Releases page **only on a `vX.Y.Z` tag**).
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
