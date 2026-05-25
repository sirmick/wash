# wash-vm — next session prompt

Paste this into the next conversation so we pick up clean.

---

## Pick-up state

The `wash-vm/` tree is the home for everything related to running wash
inside a RISC-V Linux VM. Two launch surfaces share the same kernel +
firmware + rootfs:

| target | how to run | speed | what works |
|---|---|---|---|
| **Browser (WASM)** | `cd wash-vm/web && node server/server.mjs` (already runs, :5180) | slow but real | boots end-to-end; getty on hvc1; multiport virtio-console exposes /dev/vport*p0..2; wash-router opens vport for data plane; partial wash desktop |
| **Native (`temu`)** | `cd wash-vm/native && ../tinyemu/temu wash-riscv64.cfg` | fast but blocked | OpenSBI + early kernel boot OK; **hangs at `futex hash table entries: 16` (uptime 0.246s)** |

Everything is committed under `8dc035c wash-vm: reorg + vendored
TinyEMU + native build path`.

## The thing to debug first

Native temu reaches `futex hash table entries: 16 (order: -4, 384
bytes, linear)` at VM uptime 0.246s and then makes no further kernel
progress. The VM itself is alive: `virt_machine_run` keeps firing
(~600 calls/sec, observable via the `[WASH-NATIVE] heartbeat` lines on
stderr) and `virt_machine_interp` keeps consuming cycles. So the
kernel is busy-spinning inside something we don't emulate correctly.

Things we already know:
- The WASM build does NOT hang here — same kernel + firmware + rootfs
  reach userspace fine. So the bug is in the native code path.
- CSR stubs are in place for the ~33 M-mode CSRs OpenSBI 1.5 probes
  (mhpmcounter3..31, mhpmevent3..31, mcountinhibit, menvcfg, pmpcfg,
  pmpaddr, tselect, tdata). Trap volume dropped from ~50+ to ~5 (just
  some rarely-touched CSRs left: 0x14d, 0x30c, 0x321, 0x7a4, 0xda0).
  They are NOT the hang.
- I tried aliasing CSR 0xc01 (rdtime) to real wallclock via a
  riscv_machine callback. **That made things worse** (kernel stuck even
  earlier at "Memory:" line). Reverted. The insn_counter alias is what
  gets us as far as futex.

Likely directions:
1. The kernel after futex_init runs `riscv_cpufeature_init_check`
   which does a timed unaligned-access measurement. If timer / rdtime
   semantics are off in a subtle way, the loop runs forever. Suspect
   #1.
2. There may be an M-mode trap we deliver incorrectly. Enable
   `DUMP_EXCEPTIONS` in riscv_cpu.c and see what fires post-futex.
3. The native build pulls in different config (no `__EMSCRIPTEN__`).
   Check whether any of our wash patches in riscv_cpu.c /
   riscv_machine.c are silently skipped on native. The most relevant
   is the COUNTEREN_MASK TM-bit fix (line ~654) and the utime alias
   (line ~738) — both present, but maybe an edge case for M-mode.
4. Compare WASM and native by adding the WASM heartbeat to the
   *kernel-side*: write to /dev/kmsg from a tiny init that prints
   every 50ms. If WASM shows progress and native doesn't, the kernel
   really is stuck.

## Things that ARE working (don't break them)

- `make vm` (or `make rv`) rebuilds kernel + firmware + rootfs via
  Docker and installs into `wash-vm/web/public/tinyemu/`.
- Web demo at :5180 loads, kernel boots, supervisor + diag run, wash-
  router opens /dev/vport*p0, writes to it. Desktop doesn't paint
  yet — TX from wash-router reaches the wire (`mp_tx port=0 len=N`
  traces visible), but the FE side `window.washVports[0]` hasn't been
  wired to feed the actual shell module. That's a separate piece of
  glue: in `wash-vm/web/src/tinyemu-bridge.ts`, the `dataVC` set to
  `vports[0]` needs its onOutput plumbed into the shell's wash bus
  the way the old hvc2/washConsoles[1] code did.
- Native temu binary compiles cleanly (`cd wash-vm/tinyemu && make
  temu`). It even runs to the futex hang.
- All TinyEMU source is vendored under `wash-vm/tinyemu/` — no more
  `~/wash-build` external dir.
- `wash-vm/test/repro-wedge.mjs` is a Playwright harness that pulls
  Chromium up to the demo URL; not great for perf testing (headless
  Chrome runs the WASM ~25× slower than your real Firefox) but fine
  for code-path repros.

## Lessons captured (don't relearn)

- Playwright headless Chromium is misleading for VM-perf questions —
  what looks like a wedge can just be browser throttling. Verify in a
  real focused browser tab before chasing it as a VM bug.
- Linux's virtio_console driver creates `/dev/vport{N}p0` for EVERY
  virtio-console device, not just multiport ones. That `N` is the
  global virtio device index — depends on probe order. Detect the
  multiport one by looking for /dev/vport*p2 (only multiport has
  port 2), then derive port 0 and port 1.
- Go's `*os.File` registers fds with the runtime poller (fcntl
  F_SETFL O_NONBLOCK + epoll_ctl). For tty/HVC fds this can wedge
  khvcd. The `rawFD` type in `internal/runner/router/router.go`
  bypasses the poller for the `fd:N` transport scheme. Wasn't a fix
  for the actual wedge we were chasing, but worth keeping for cases
  where it matters.
- Emscripten `-s NO_FILESYSTEM=1` strips `fprintf(stderr, ...)`. Use
  console_write (HTIF) for traces in the WASM target. The portable
  `wash_log_bytes()` helper in virtio.c handles both targets.

## Open questions / nice-to-have

- Per-channel logs for the native launcher: have `temu` map each
  virtio-console to its own pty/file via `-chardev` semantics so
  `tail -f wash-vm/native/logs/{login,wash,supervisor,diag}.log`
  shows each stream separately. Currently temu only wires the legacy
  single console; the wash patches haven't exposed equivalents for
  native yet.
- Input injection for native: named FIFOs (one per virtio-console
  port) writable from the host so a dev can `echo cmd > pipes/login`
  the same way the browser admin API works today.
- A native `make` target at the top level: `make native` should build
  temu + ensure kernel/firmware/rootfs exist + launch with the right
  cfg.

## Files most worth re-reading first

- `wash-vm/tinyemu/riscv_cpu.c` line ~648 onwards (CSR read), ~900
  onwards (CSR write), ~1273 onwards (interp main loop)
- `wash-vm/tinyemu/riscv_machine.c` line ~80 (RTC), ~890 (init), ~915
  (virtio MMIO device instantiation including our multiport)
- `wash-vm/tinyemu/virtio.c` line ~1428 onwards (multiport
  implementation)
- `wash-vm/image/rootfs/overlay/etc/init.d/S99wash-router` (supervisor
  script — detects /dev/vport*p2 to find the multiport device index)
- `wash-vm/web/src/tinyemu-bridge.ts` line ~427 (the vports vs
  washConsoles dispatch you'll need to extend when wiring the wash
  desktop to receive bytes)

Good luck future-self.
