# OpenSBI 1.x for TinyEMU/RISC-V.
#
# Replaces Bellard's 2018 riscv-pk (bbl64.bin) — that ancient firmware
# only speaks SBI 0.1, and modern Linux kernels (5.x+) make assumptions
# about SBI 0.2+ even when CONFIG_HVC_RISCV_SBI is off. Predicted
# failure mode: kernel panics extremely early, before any console
# bind, so we see zero output and TinyEMU exit(1)s. See
# image-rv/README.md for the diagnosis trail.
#
# OpenSBI generic platform reads the FDT TinyEMU pre-builds (it boots
# the standard a0=hartid, a1=fdt ABI per riscv_machine.c:807-815) and
# jumps to the kernel at FW_JUMP_ADDR (default 0x80200000), which
# matches TinyEMU's 2 MiB-aligned kernel placement.
#
# Output: /fw_jump.bin (no payload — kernel loaded separately by
# TinyEMU as today).

FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential ca-certificates git make python3 \
        gcc-riscv64-linux-gnu \
 && rm -rf /var/lib/apt/lists/*

ARG OPENSBI_VERSION=v1.5
RUN git clone --depth=1 -b $OPENSBI_VERSION \
        https://github.com/riscv-software-src/opensbi /opensbi
WORKDIR /opensbi

# wash-specific OpenSBI patches:
#   1. lib/sbi/sbi_domain.c — bump ROOT_REGION_MAX from 16 → 64.
#      Upstream 16 is exhausted by firmware + HTIF console memranges
#      before IPI MSWI cold-init can register its CLINT region — boot
#      halts with "ipi init failed (error -3)".
#   2. lib/utils/ipi/aclint_mswi.c — keep a single sbi_printf at the
#      end of cold_init. Without it the boot stalls at futex_init
#      under our TinyEMU + HTIF-only environment; see comments in
#      aclint_mswi.c for the theory.
COPY sbi_domain.c    /opensbi/lib/sbi/sbi_domain.c
COPY aclint_mswi.c   /opensbi/lib/utils/ipi/aclint_mswi.c
COPY fdt_ipi_mswi.c  /opensbi/lib/utils/ipi/fdt_ipi_mswi.c

# PLATFORM=generic = DTB-driven, no hardcoded board layout. Reads the
# FDT pointer in a1 at boot and discovers cpus/clint/plic/virtio from
# it — exactly the shape TinyEMU's riscv_build_fdt produces.
#
# FW_OPTIONS=0x1 sets SBI_SCRATCH_NO_BOOT_PRINTS (see include/sbi/
# sbi_scratch.h:151). The four printk sites in lib/sbi/sbi_init.c
# guarded by that bit are the ~50-line "OpenSBI v1.5 / Platform Name
# / Domain0 RegionNN / Firmware Base ..." banner table — every byte
# of which goes char-by-char through HTIF putchar and dominates the
# pre-Linux wall-clock cost. WASH/aclint and WASH/addmem traces in
# our patched aclint_mswi.c use sbi_printf directly and are NOT
# gated by NO_BOOT_PRINTS, so the futex-init workaround survives.
RUN make PLATFORM=generic CROSS_COMPILE=riscv64-linux-gnu- \
        FW_OPTIONS=0x1 -j"$(nproc)" \
 && cp build/platform/generic/firmware/fw_jump.bin /fw_jump.bin \
 && ls -lh /fw_jump.bin
