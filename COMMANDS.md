# wash — make commands (the one interface)

Everything is a `make` verb. Ports: login **10000** · router **11000** ·
browser-vm **12000** · qemu-vm **13000** (defaults live in the binaries).

## BUILD
```
make wash               standalone build → out/  (auto-includes wash-display when wlroots present; WASH_DISPLAY=0/1 overrides)
make wash-multicall     busybox layout → out/multicall/
```

## RUN / DEV
```
make run                build wash, run the router → http://localhost:11000/
make dev                router :11000 + Vite HMR → http://localhost:5173/
```

## TEST   (standalone by default; each builds its own prereqs)
```
make unit-test          go vet + go test ./... + FE-unit (node --test) + component (vitest)
make e2e-test           Playwright suite (builds the test app)
make net-test           net-matrix + vm-net-test + net-vm e2e   (builds openwrt+distro images; needs /dev/kvm)
make disks-test         vm-disks-test real-kernel storage gate   (builds alpine image; needs /dev/kvm)
make all-test           every test, BOTH layouts (standalone + multicall); no packaging
make test-all           all-test + all-package (the whole pyramid)
make verify             quick go-only gate: go vet + go test + static-ELF check
```

## PACKAGE   (hermetic in Docker; WASH_PKG_JOBS=N concurrency)
```
make all-package                 whole matrix (WASH_PKG_DISPLAY=1 adds the display leaves)
make <arch>-<platform>-<pkg>-package     one leaf (17):
    wash:    {amd64,arm64,riscv64} × {ubuntu24,debian13,alpine321}  +  {amd64,arm64} × fedora40
    display: {amd64,arm64} × {ubuntu24,debian13,fedora40}    (deb/rpm only)
    e.g.  make amd64-ubuntu24-wash-package    make arm64-fedora40-display-package
make openwrt-smoke               OpenWRT runtime smoke (opkg/procd; no .ipk)
```

## VM — IMAGES  (`<platform>-image-vm`)
```
make alpine-image-vm    host microvm (disks-test + qemu surface)
make ubuntu-image-vm    \
make debian-image-vm     | per-distro net-test images
make fedora-image-vm    /
make openwrt-image-vm   OpenWRT router image (net-matrix / net-demo)
make browser-image-vm   in-browser riscv VM artifacts (kernel+fw+rootfs+wasm)
```

## VM — RUN  (`<platform>-run-vm`; each builds its image first)
```
make browser-run-vm     serve the in-browser riscv VM → :12000  (login wash/wash)
make qemu-run-vm        host qemu microvm + serve → :13000       (login wash/wash)
make net-demo           3 OpenWRT microvms + browser consoles → :8001-8003
```

## CLEAN
```
make clean              in-tree build artifacts (out/, FE dist, embedded assets, dist/, wash-display build)
make tmp-clean          + /tmp/wash-* runtime junk (sockets kept)
make docker-clean       + the matrix Docker images + buildx cache
make all-clean          everything (+ node_modules + Go build cache)   [spares tmp/ branches/ harbor.config]
```

## CI
`.github/workflows/ci.yml` runs the same verbs: `make unit-test` + `make e2e-test`
(parallel) → `make <amd64>-<distro>-wash-package` + `make openwrt-smoke`
(needs the tests green). `demo.yml` / `prebuild.yml` build the browser-VM
GitHub-Pages demo (separate pipeline).

## STILL SCRIPTS (engines make calls / dev helpers — not the interface)
```
packaging/run_matrix.sh         the package matrix engine (the package verbs call it)
packaging/make-source-tarball.sh
wash-vm/run-browser.sh          served by make browser-run-vm
wash-vm/run-qemu.sh             served by make qemu-run-vm
run.sh                          richer dev router launcher (--fm-seed / --tail / --listen)
scripts/dev-restart.sh          kill+rebuild+restart the live router
scripts/dev-kill.sh             kill every wash process
scripts/fm-seed.sh  scripts/seed-bulk-fixture.sh
```

## TYPICAL FLOWS
```
make wash && make run               # build, then run the router (:11000)
make dev                            # fastest FE iteration (HMR :5173)
make unit-test                      # everyday Go+FE check
make all-clean && make all-test     # full from-scratch verification
make browser-run-vm                 # build + serve the in-browser RISC-V VM (:12000)
make amd64-ubuntu24-wash-package    # one native package
```
