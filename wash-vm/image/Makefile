# wash — RISC-V demo image pipeline (image-rv).
#
# Sibling of image/ — produces artifacts for the TinyEMU/jslinux
# RISC-V demo (web/demo/tinyemu.html) instead of the v86 x86 demo.
# Same shape (kernel + rootfs.ext2) so wiring into the demo is
# uniform — only the FE adapter differs.
#
# Targets:
#
#   make rootfs   — alpine-riscv64 + wash GOARCH=riscv64 → rootfs.ext2
#   make kernel   — Linux 6.6 LTS for ARCH=riscv → Image
#   make bbl      — re-use Bellard's bbl64.bin (already downloaded
#                   to web/demo/public/tinyemu/) as our firmware
#   make all      — rootfs + kernel + install
#   make install  — copy current dist/ → web/demo/public/tinyemu-rv/
#
# Host prerequisites: bash, docker. The cross-compile toolchain
# runs entirely inside Docker containers.

REPO_ROOT := $(abspath ../..)
DIST      := $(CURDIR)/dist
ROOTFS    := $(DIST)/rootfs.ext2
KERNEL    := $(DIST)/Image
FIRMWARE  := $(DIST)/fw_jump.bin

# install target = the dir the live tinyemu.html cfg reads from
# (web/demo/public/tinyemu/wash-riscv64.cfg). Filenames are renamed
# on install so they match the cfg verbatim (wash-kernel.bin,
# wash-rootfs.ext2) — host-absolute /tinyemu-rv/* paths trip the
# Bellard EMSCRIPTEN load_file abort; see docs/TINYEMU.md.
DEMO_PUB  := $(REPO_ROOT)/wash-vm/web/public/tinyemu

.PHONY: all rootfs kernel firmware install clean help bbl

help:
	@echo "Targets:"
	@echo "  all       — rootfs + kernel + firmware + install (full demo build)"
	@echo "  rootfs    — buildroot-musl riscv64 + wash → rootfs.ext2"
	@echo "  kernel    — Linux 6.6 riscv → Image"
	@echo "  firmware  — OpenSBI 1.5 generic riscv64 → fw_jump.bin"
	@echo "  bbl       — sanity-check vendored bbl64.bin presence (legacy)"
	@echo "  install   — copy current dist/ into $(DEMO_PUB)/ with cfg names"
	@echo "  clean     — wipe dist/"

all: rootfs kernel firmware install

rootfs: $(ROOTFS)

kernel: $(KERNEL)

firmware: $(FIRMWARE)

$(FIRMWARE): firmware/Dockerfile
	mkdir -p $(DIST)
	cd firmware && docker build -t wash-firmware-rv-builder .
	docker run --rm -v $(DIST):/out wash-firmware-rv-builder \
	  cp /fw_jump.bin /out/fw_jump.bin

# bbl is vendored from Bellard (riscvemu64-2019-12-21 release). The
# WASM-side wgetfetch reads it via /tinyemu/bbl64.bin. We don't have
# a reproducible builder for BBL today; this target is a presence
# check so `make all` fails fast if someone wiped public/tinyemu/.
bbl:
	@if [ ! -f $(DEMO_PUB)/bbl64.bin ]; then \
	  echo "ERROR: $(DEMO_PUB)/bbl64.bin missing"; \
	  echo "vendored from https://bellard.org/jslinux/ (riscvemu64-2019-12-21)"; \
	  exit 1; \
	fi
	@echo "bbl64.bin present ($$(stat -c %s $(DEMO_PUB)/bbl64.bin) bytes)"

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

install: bbl $(SPLITIMG)
	mkdir -p $(DEMO_PUB)
	@if [ -f $(KERNEL) ]; then cp -v $(KERNEL) $(DEMO_PUB)/wash-kernel.bin; fi
	@if [ -f $(FIRMWARE) ]; then cp -v $(FIRMWARE) $(DEMO_PUB)/opensbi-fw_jump.bin; fi
	@if [ -f $(ROOTFS) ]; then \
	  cp -v $(ROOTFS) $(DEMO_PUB)/wash-rootfs.ext2; \
	  echo "  splitting rootfs into 2 MiB blocks for TinyEMU drive0..."; \
	  rm -rf $(DEMO_PUB)/wash-rootfs && mkdir $(DEMO_PUB)/wash-rootfs; \
	  $(SPLITIMG) $(DEMO_PUB)/wash-rootfs.ext2 $(DEMO_PUB)/wash-rootfs/ 2048; \
	  (cd $(DEMO_PUB)/wash-rootfs && ls blk*.bin > .preload); \
	  echo "  wrote $$(ls $(DEMO_PUB)/wash-rootfs/blk*.bin | wc -l) chunks + .preload"; \
	fi
	@echo "installed → $(DEMO_PUB)/{wash-kernel.bin,wash-rootfs/blk.txt,opensbi-fw_jump.bin}"

clean:
	rm -rf $(DIST)
