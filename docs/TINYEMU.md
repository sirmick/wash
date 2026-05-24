# TinyEMU/RISC-V side-by-side experiment

Companion to the v86 demo. Runs Bellard's TinyEMU (riscvemu64-wasm) at
`/tinyemu.html` so we can compare a lighter RISC-V emulator against the
x86 v86 baseline. Bellard's stock buildroot boots end-to-end through the
vite proxy; getting *our* kernel + ext2 rootfs to boot is in-progress.

## Paths

| where | what |
|---|---|
| `web/demo/tinyemu.html` | page entry — URL params, legacy-glue shims, DOM stubs |
| `web/demo/src/tinyemu-bridge.ts` | term tap + Module lifecycle + /cmd /ctl bridge + XHR tracer |
| `web/demo/public/tinyemu/` | Bellard's prod wasm + bbl64.bin + buildroot kernel + cfgs |
| `web/demo/public/tinyemu/wash-riscv64.cfg` | active cfg the page loads by default |
| `web/demo/public/tinyemu/wash-kernel.bin` | our Linux Image (co-located so the relative path resolves) |
| `web/demo/public/tinyemu-rv/` | original location of our kernel/rootfs (host-absolute path, **broken** — see trap below) |
| `web/demo/debug-server.mjs` | port 5181: POST `/log`, GET `/cmd`, GET `/ctl` |
| `image-rv/` | Docker pipeline that builds the riscv64 kernel + alpine rootfs |
| `/home/mick/wash-build/tinyemu/tinyemu-2019-12-21/` | extracted Bellard TinyEMU source (built with emsdk) |

## THE trap: `load_file` is `abort()` under EMSCRIPTEN

Source: `machine.c:448`

```c
#ifdef EMSCRIPTEN
static int load_file(uint8_t **pbuf, const char *filename) {
    abort();
}
```

`config_load_file()` picks between `fs_wget` (URL) and `load_file`
(synchronous filesystem read) based on `is_url(filename)` which only
returns true for `http:`, `https:`, `file:` prefixes (`fs_utils.c:365`).

A host-absolute path like `/tinyemu-rv/Image` is **not** a URL by that
test, so it ends up in the abort stub. `get_file_path()` short-circuits
on leading `/` (`machine.c:432`: `if (filename[0] == '/') goto done`) so
the join-with-cfg-URL never happens. Result: cfg parses, bios fetches
through `fs_wget` fine (relative path joins to a URL), kernel fetch
trips the abort. This is the cryptic `abort(). Build with -s
ASSERTIONS=1 for more info.` we chased for an evening.

### URL resolution per cfg field

| field | resolver | accepts host-absolute `/path`? | accepts relative? |
|---|---|---|---|
| `bios` / `kernel` / `initrd` (strings) | `config_load_file` → `is_url` → `load_file` if not URL | **NO** — `abort()` | yes (joined to cfg-dir) |
| `drive0.file` / `fs0.file` | `block_device_init_http` / `fs_net_init` → `fs_wget` direct | yes | yes (resolved by browser XHR against `<base href="/tinyemu/">`) |

**Rule of thumb:** put files next to the cfg and use bare filenames. If
you must point elsewhere, use a full URL (`http://...`). Never use a
leading `/` for `bios`/`kernel`/`initrd`.

## Other landmines surfaced while debugging

1. **jslinux Ethernet dials `wss://relay.widgetry.org/` during preRun.**
   `Ethernet.openHandler` / `closeHandler` call `net_set_carrier` via
   `cwrap` *before* the WASM runtime is initialized. Release builds
   silently no-op; `-s ASSERTIONS=1` aborts with `native function
   net_set_carrier called before runtime initialization`. Workarounds in
   `tinyemu.html`: force `?net_url=` (empty) param + neuter
   `window.WebSocket` + zero out `Ethernet.prototype.*Handler` after
   jslinux.js loads.

2. **Bellard's wasm is built against pre-1.x Emscripten.** Calls into
   legacy globals that emcc 5.x removed:
   - `Pointer_stringify` → shimmed to delegate to `UTF8ToString`
   - `Browser.safeSetTimeout` / `safeSetInterval` /
     `safeRequestAnimationFrame` / `getNextWgetRequestHandle` /
     `wgetRequests` → minimal stand-in in `tinyemu.html`

3. **`p->eth_count == 1` assert in `init_vm`** fires if the WASM build's
   eth init runs without an `eth0` in cfg. Easy: always include
   `eth0: { driver: "user" }`.

4. **vfsync proxy works.** vite forwards `/vfsync/*` to bellard.org
   (set up in `vite.config.ts`). Bellard's stock buildroot-riscv64.cfg
   boots end-to-end through this — useful as a known-good baseline.

5. **The kernel-format question is moot for now.** Our `Image` is
   PE32+/EFI-wrapped but the wrapper starts with valid RISC-V
   instructions (`c.li s4,-13; j _start_kernel`) so BBL can jump to
   offset 0. Boot has not been *observed* yet, but the kernel never
   ran in our earlier failures — they were the `load_file` trap.

## Working cfg shape

```c
/* place this at web/demo/public/tinyemu/wash-riscv64.cfg
   alongside bbl64.bin, wash-kernel.bin, wash-rootfs.ext2 */
{
    version: 1,
    machine: "riscv64",
    memory_size: 256,
    bios: "bbl64.bin",        /* relative — resolved to /tinyemu/bbl64.bin */
    kernel: "wash-kernel.bin",
    cmdline: "loglevel=7 console=hvc0 root=/dev/vda rootfstype=ext2 rw",
    drive0: { file: "wash-rootfs.ext2" },
    eth0: { driver: "user" }, /* required to satisfy eth_count assert */
}
```

The cfg parser accepts these top-level fields (`machine.c:223-360`):
`version` (int), `machine` (str), `memory_size` (int), `bios` (str),
`vga_bios` (str), `kernel` (str), `initrd` (str — **bare string only**;
`{file: ...}` form is rejected), `cmdline` (str), then loops to find
`drive%d`, `fs%d`, `eth%d` (max 4 each). `display_device`,
`input_device` are also recognized.

## Build a debug TinyEMU

emsdk is at `/home/mick/emsdk/` (emcc 5.0.7). The 2019 source needs a
few flag tweaks for it:

```bash
cd /home/mick/wash-build/tinyemu/tinyemu-2019-12-21
# Provide openssl headers via local symlinks (Bellard ships
# OpenSSL-compatible aes.h/sha256.h):
mkdir -p openssl
ln -sf ../aes.h openssl/aes.h
ln -sf ../sha256.h openssl/sha.h
cat > openssl/evp.h <<'EOF'
typedef struct evp_pkey_st EVP_PKEY;
typedef struct evp_cipher_st EVP_CIPHER;
typedef struct evp_cipher_ctx_st EVP_CIPHER_CTX;
typedef struct evp_md_st EVP_MD;
typedef struct evp_md_ctx_st EVP_MD_CTX;
EOF

# Patches to Makefile.js for emcc 5:
#   EMCFLAGS  drop --llvm-opts 2, add -I. -DEMSCRIPTEN
#   EMLDFLAGS_WASM  drop --memory-init-file, drop BINARYEN_TRAP_MODE,
#                   rename EXTRA_EXPORTED_RUNTIME_METHODS → EXPORTED_RUNTIME_METHODS,
#                   add -s STACK_SIZE=1048576 -s STACK_OVERFLOW_CHECK=0
# (ASSERTIONS=2 false-positives on the first stackAlloc with emcc 5 +
#  this old code — disable the check or shimming explains nothing.)

source /home/mick/emsdk/emsdk_env.sh
make -f Makefile.js js/riscvemu64-wasm.js

# Install (the .prod backups are kept):
cp js/riscvemu64-wasm.{js,wasm} /home/mick/wash/web/demo/public/tinyemu/
```

## Debug loop

```
# Tail debug-server output (the bridge POSTs /log per VM byte and per
# error). Filter to what you care about:
tail -F /tmp/claude-1000/-home-mick-wash/.../tasks/<id>.output \
  | grep -E '\[riscv\]|abort|panic|Module\.|xhr\.error'

# Force the open page to reload (the bridge polls /ctl every 500ms):
echo -n "reload-page" > /tmp/wash-demo-ctl

# Type into the VM (the bridge polls /cmd):
echo "uname -a" > /tmp/wash-demo-cmd
```

If `/tmp/wash-demo-ctl` sits unread for more than a second, the page
isn't open or the bridge crashed before the polling loop started.
