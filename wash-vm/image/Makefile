# wash — RISC-V demo image pipeline (image-rv).
#
# Sibling of image/ — produces artifacts for the TinyEMU/jslinux
# RISC-V demo (web/demo/tinyemu.html) instead of the v86 x86 demo.
# Same shape (kernel + rootfs.ext2) so wiring into the demo is
# uniform — only the FE adapter differs.
#
# Targets:
#
#   make rootfs   — buildroot-musl riscv64 + wash GOARCH=riscv64 → rootfs.ext2
#   make kernel   — Linux 6.6 LTS for ARCH=riscv → Image
#   make firmware — OpenSBI 1.5 generic riscv64 → fw_jump.bin
#   make all      — rootfs + kernel + firmware + install
#   make install  — copy current dist/ → wash-vm/web/public/tinyemu/
#
# Host prerequisites: bash, docker. The cross-compile toolchain
# runs entirely inside Docker containers.

REPO_ROOT := $(abspath ../..)
DIST      := $(CURDIR)/dist
ROOTFS    := $(DIST)/rootfs.ext2
KERNEL    := $(DIST)/Image
FIRMWARE  := $(DIST)/fw_jump.bin
WASM_JS   := $(DIST)/riscvemu64-wasm.js
WASM_BIN  := $(DIST)/riscvemu64-wasm.wasm
TINYEMU   := $(REPO_ROOT)/wash-vm/tinyemu

# install target = the dir the live tinyemu.html cfg reads from
# (web/demo/public/tinyemu/wash-riscv64.cfg). Filenames are renamed
# on install so they match the cfg verbatim (wash-kernel.bin,
# wash-rootfs.ext2) — host-absolute /tinyemu-rv/* paths trip the
# Bellard EMSCRIPTEN load_file abort; see docs/TINYEMU.md.
DEMO_PUB  := $(REPO_ROOT)/wash-vm/web/public/tinyemu

.PHONY: all rootfs kernel firmware wasm install clean help

help:
	@echo "Targets:"
	@echo "  all       — rootfs + kernel + firmware + wasm + install (full demo build)"
	@echo "  rootfs    — buildroot-musl riscv64 + wash → rootfs.ext2"
	@echo "  kernel    — Linux 6.6 riscv → Image"
	@echo "  firmware  — OpenSBI 1.5 generic riscv64 → fw_jump.bin"
	@echo "  wasm      — TinyEMU emcc build → riscvemu64-wasm.{js,wasm}"
	@echo "  install   — copy current dist/ into $(DEMO_PUB)/ with cfg names"
	@echo "  clean     — wipe dist/"

all: rootfs kernel firmware wasm install

rootfs: $(ROOTFS)

kernel: $(KERNEL)

firmware: $(FIRMWARE)

wasm: $(WASM_BIN)

$(FIRMWARE): firmware/Dockerfile
	mkdir -p $(DIST)
	cd firmware && docker build -t wash-firmware-rv-builder .
	docker run --rm -v $(DIST):/out wash-firmware-rv-builder \
	  cp /fw_jump.bin /out/fw_jump.bin

# WASM build. Context is wash-vm/tinyemu/ (the TinyEMU sources +
# Makefile.js + js/lib.js); Dockerfile lives in image/wasm/. Rebuild
# when any .c/.h in the tinyemu tree changes or when js/lib.js
# (the WASM↔JS shim) changes.
$(WASM_BIN): wasm/Dockerfile $(wildcard $(TINYEMU)/*.c) $(wildcard $(TINYEMU)/*.h) \
             $(TINYEMU)/js/lib.js $(TINYEMU)/Makefile.js
	mkdir -p $(DIST)
	docker build -t wash-wasm-rv-builder -f wasm/Dockerfile $(TINYEMU)
	docker run --rm -v $(DIST):/out wash-wasm-rv-builder bash -c \
	  'cp js/riscvemu64-wasm.js js/riscvemu64-wasm.wasm /out/'

$(ROOTFS): rootfs/build.sh rootfs/Dockerfile rootfs/wash_riscv64_defconfig \
           rootfs/overlay/etc/init.d/S99wash-router
	mkdir -p $(DIST)
	REPO_ROOT=$(REPO_ROOT) DIST=$(DIST) ./rootfs/build.sh

$(KERNEL): kernel/Dockerfile kernel/config.fragment
	mkdir -p $(DIST)
	cd kernel && docker build -t wash-kernel-rv-builder .
	docker run --rm -v $(DIST):/out wash-kernel-rv-builder \
	  cp /build/linux/arch/riscv/boot/Image /out/Image

# splitimg: TinyEMU's drive0 path expects a JSON manifest pointing at
# chunked block files, NOT a raw ext2. Without re-splitting whenever
# the rootfs is rebuilt, the kernel reads stale chunks of an old
# image and either hangs or fails to mount.
# Built from the TinyEMU source if not already present in /tmp.
SPLITIMG := $(REPO_ROOT)/wash-vm/tinyemu/splitimg
$(SPLITIMG):
	cd $(REPO_ROOT)/wash-vm/tinyemu && \
	  gcc -O2 -DCONFIG_VERSION='"2019-12-21"' -o $(SPLITIMG) splitimg.c cutils.c

install: $(SPLITIMG)
	mkdir -p $(DEMO_PUB)
	@if [ -f $(KERNEL) ]; then cp -v $(KERNEL) $(DEMO_PUB)/wash-kernel.bin; fi
	@if [ -f $(FIRMWARE) ]; then cp -v $(FIRMWARE) $(DEMO_PUB)/opensbi-fw_jump.bin; fi
	@if [ -f $(WASM_JS) ]; then cp -v $(WASM_JS) $(DEMO_PUB)/riscvemu64-wasm.js; fi
	@if [ -f $(WASM_BIN) ]; then cp -v $(WASM_BIN) $(DEMO_PUB)/riscvemu64-wasm.wasm; fi
	@if [ -f $(ROOTFS) ]; then \
	  cp -v $(ROOTFS) $(DEMO_PUB)/wash-rootfs.ext2; \
	  echo "  splitting rootfs into 2 MiB blocks for TinyEMU drive0..."; \
	  rm -rf $(DEMO_PUB)/wash-rootfs && mkdir $(DEMO_PUB)/wash-rootfs; \
	  $(SPLITIMG) $(DEMO_PUB)/wash-rootfs.ext2 $(DEMO_PUB)/wash-rootfs/ 2048; \
	  (cd $(DEMO_PUB)/wash-rootfs && ls blk*.bin > .preload); \
	  echo "  wrote $$(ls $(DEMO_PUB)/wash-rootfs/blk*.bin | wc -l) chunks + .preload"; \
	fi
	@echo "installed → $(DEMO_PUB)/{wash-kernel.bin,wash-rootfs/blk.txt,opensbi-fw_jump.bin,riscvemu64-wasm.{js,wasm}}"

clean:
	rm -rf $(DIST)
