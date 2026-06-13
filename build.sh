#!/usr/bin/env bash
# build.sh — build wash for this machine.
#
# No flags builds the standalone per-app binaries into out/ (the production
# layout). Flags below add to or switch what gets built. Every run ends with a
# summary of what was built and what this host could build but didn't.
#
# Layout (pick one; default --standalone):
#   --standalone   per-app binaries in out/                       (production)
#   --multicall    one wash binary + its symlinks in out/multicall/   (smaller)
#   --both         both layouts (used by test.sh --both)
#
# Also build (add to any layout):
#   --display      the native wash-display compositor. Needs wlroots — uses a
#                  system one if present, else compiles the vendored copy (slow).
#   --vm-helpers   the host VM helper CLIs (washvm-run, washnet-demo/-matrix).
#   --browser-vm   the in-browser riscv emulator demo. Needs docker; serve with
#                  `cd wash-vm/web && node server/server.mjs` → http://localhost:12000
#   --all          everything this host can: both layouts + vm-helpers +
#                  wash-display (only with a system wlroots — never a surprise
#                  vendored compile). NOT the browser VM or the VM test images.
#
# Options:
#   --no-test-app  exclude the test app + fakesudo stub
#   --no-sudo      skip wash-sudo (headless / kiosk)
#   --clean        run `make clean` first
#   -j <N>         parallel make jobs (default: nproc)
#   -n, --dry-run  print the plan, build nothing
#   -h, --help     show this text
#
# (TEST_APP=1 is passed unless --no-test-app.)

set -euo pipefail
SECONDS=0

REPO="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO"

mode=standalone
test_app=1
clean=0
# Parallel build jobs. 0 = auto-detect from nproc; -j<N> passed to
# make. Set with --jobs <N> (or -j <N>); --jobs 1 disables parallel
# for serial debugging.
jobs=0
no_sudo=0
want_display=0     # set by --display (force) or --all (if system wlroots)
want_vm_helpers=0  # set by --vm-helpers or --all
want_browser_vm=0  # set by --browser-vm (the in-browser riscv demo)
all=0
dry=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --standalone)   mode=standalone; shift;;
    --multicall|--bb|--busybox)
                    mode=multicall; shift;;
    --both)         mode=both; shift;;
    --all)          all=1; mode=both; want_vm_helpers=1; shift;;
    --display)      want_display=1; shift;;
    --vm-helpers)   want_vm_helpers=1; shift;;
    --browser-vm|--browservm|--bvm)
                    want_browser_vm=1; shift;;
    --no-test-app)  test_app=0; shift;;
    --no-sudo)      no_sudo=1; shift;;
    --clean)        clean=1; shift;;
    -n|--dry-run)   dry=1; shift;;
    -j|--jobs)      jobs="$2"; shift 2;;
    -j*)            jobs="${1#-j}"; shift;;
    -h|--help)
      # Print the header comment block as help (skip the shebang on line 1,
      # stop before `set -euo`).
      sed -n '2,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
      exit 0
      ;;
    *)
      echo "build.sh: unknown flag: $1" >&2
      # Nearest-match hint — the most common typo here was `--all` not
      # existing at all (it does now), so steer the other near-misses too.
      case "$1" in
        --al*|--everything|--full) echo "  did you mean --all?" >&2;;
        --multi*|--busy*|--bb*)    echo "  did you mean --multicall?" >&2;;
        --disp*|--compositor)      echo "  did you mean --display?" >&2;;
        --vm-image*|--images)      echo "  VM images are a test fixture: make vm-image* (or ./test.sh --vm)" >&2;;
        --browser*|--riscv|--demo) echo "  did you mean --browser-vm?" >&2;;
        --vm*|--helpers)           echo "  did you mean --vm-helpers? (or --browser-vm for the in-browser demo)" >&2;;
        --both*|--all-layouts)     echo "  did you mean --both?" >&2;;
      esac
      echo "(use --help to list flags)" >&2
      exit 2
      ;;
  esac
done

# Resolve parallel job count. 0 → nproc (or fall back to 4 if nproc
# isn't installed). Anything else passes through unchanged so the
# caller can pin a smaller number on a busy machine.
if [[ "$jobs" == "0" ]]; then
  if command -v nproc >/dev/null 2>&1; then
    jobs="$(nproc)"
  else
    jobs=4
  fi
fi

# ── capability detection ──────────────────────────────────────────────────
# wash-display is C++/CMake/wlroots, built by its own project (the Go build
# never touches it). Decide whether we *can* build it, and via which path:
#   system   — pkg-config finds wlroots → fast, just cmake+build
#   vendored — only the bundled wlroots/0.17 source → must compile it (slow)
#   no       — missing cmake / C++ / any wlroots source
display_state=no
display_why=""
if ! command -v cmake >/dev/null 2>&1; then
  display_why="cmake not installed"
elif ! { command -v c++ >/dev/null 2>&1 || command -v g++ >/dev/null 2>&1 || command -v clang++ >/dev/null 2>&1; }; then
  display_why="no C++ compiler (c++/g++/clang++)"
elif pkg-config --exists wlroots 2>/dev/null; then
  display_state=system
elif [[ -e wash-display/third_party/wlroots/meson.build ]]; then
  display_state=vendored
else
  display_why="wlroots not found (no system pkg-config entry, no vendored source)"
fi

# Decide whether this run actually builds the compositor, and why/why-not.
build_display=0
display_note=""        # shown in the summary when we DON'T build it
display_vendored=0
if [[ "$want_display" == "1" ]]; then
  # Explicit --display: build it or fail loudly (the user asked by name).
  if [[ "$display_state" == "no" ]]; then
    echo "build.sh: --display requested but can't build wash-display: $display_why" >&2
    exit 2
  fi
  build_display=1
  [[ "$display_state" == "vendored" ]] && display_vendored=1
elif [[ "$all" == "1" ]]; then
  # --all takes the fast path only — never kicks off a vendored wlroots
  # compile behind your back.
  if [[ "$display_state" == "system" ]]; then
    build_display=1
  elif [[ "$display_state" == "vendored" ]]; then
    display_note="skipped by --all (only vendored wlroots) — run ./build.sh --display to compile it (slow)"
  else
    display_note="$display_why — run ./build.sh --display once deps are present"
  fi
fi

# --browser-vm cross-builds the riscv image entirely inside Docker containers,
# so docker is the one hard prereq. Fail fast (before any host build) if asked
# for it without docker.
if [[ "$want_browser_vm" == "1" ]] && ! command -v docker >/dev/null 2>&1; then
  echo "build.sh: --browser-vm needs docker (the riscv image is cross-built in containers)" >&2
  exit 2
fi

# ── build plan (booleans the executor + summary both read) ────────────────
build_standalone=0; build_multicall=0
case "$mode" in
  standalone) build_standalone=1;;
  multicall)  build_multicall=1;;
  both)       build_standalone=1; build_multicall=1;;
esac

# ── dry run: print the plan, then stop ────────────────────────────────────
plan_line() { printf '  %-3s %-16s %s\n' "$1" "$2" "$3"; }
if [[ "$dry" == "1" ]]; then
  echo "build.sh: plan ($(go env GOOS 2>/dev/null)/$(go env GOARCH 2>/dev/null), -j$jobs)"
  [[ "$build_standalone"  == "1" ]] && plan_line "✓" "standalone"  "per-app binaries → out/"
  [[ "$build_multicall"   == "1" ]] && plan_line "✓" "multicall"   "out/multicall/ (wash + symlinks)"
  [[ "$build_display"     == "1" ]] && plan_line "✓" "wash-display" "compositor ($([[ $display_vendored == 1 ]] && echo 'vendored wlroots — slow' || echo 'system wlroots'))"
  [[ "$build_display"     != "1" ]] && plan_line "–" "wash-display" "${display_note:-not requested (./build.sh --display)}"
  [[ "$want_vm_helpers"   == "1" ]] && plan_line "✓" "vm helpers"  "washvm-run, washnet-demo, washnet-matrix"
  [[ "$want_vm_helpers"   != "1" ]] && plan_line "–" "vm helpers"  "not requested (./build.sh --vm-helpers)"
  [[ "$want_browser_vm"   == "1" ]] && plan_line "✓" "browser VM"  "in-browser riscv demo (make vm + shell FE)"
  [[ "$want_browser_vm"   != "1" ]] && plan_line "–" "browser VM"  "not requested (./build.sh --browser-vm)"
  plan_line "–" "vm images" "test fixtures — make vm-image* (or ./test.sh --vm)"
  echo "build.sh: dry run — nothing built."
  exit 0
fi

echo "build.sh: parallel jobs = $jobs"

# Args common to every Go `make` invocation. -j first so it applies to the
# goal targets even when the call is "make -j<N> tgt".
make_args=(-j"$jobs")
if [[ "$test_app" == "1" ]]; then
  make_args+=(TEST_APP=1)
fi
if [[ "$no_sudo" == "1" ]]; then
  make_args+=(WASH_NO_SUDO=1)
fi

if [[ "$clean" == "1" ]]; then
  echo "build.sh: clean"
  make clean
fi

# Track what we actually produce for the closing summary.
built=()   # "label\tdetail" lines for built artifacts

build_standalone_layout() {
  echo "build.sh: standalone (separate per-app binaries)"
  # wash-priv-fakesudo is a test-only sudo stub — not in TARGETS, so
  # `make all` skips it. Build it alongside `all` when the test app is
  # included so a clean checkout (e.g. CI) has everything the
  # priv/services/packages e2e specs need, instead of silently relying
  # on a stale binary in a dev's out/. Mirrors the multicall path.
  local targets=(all)
  [[ "$test_app" == "1" ]] && targets+=(out/wash-priv-fakesudo)
  make "${make_args[@]}" "${targets[@]}"
  built+=("standalone"$'\t'"per-app binaries in out/")
}

build_multicall_layout() {
  echo "build.sh: multicall (single wash binary + symlinks → out/multicall/)"
  # Real binaries the multi-call layout still needs:
  #   wash               — the dispatcher itself (router + launch + apps)
  #   wash-sudo          — CLI; suid-on-symlink no-ops, so this stays its
  #                        own file (skipped with --no-sudo)
  #   wash-priv-fakesudo — test-only sudo stub
  # out/wash stays at the repo-root out/ as the canonical artifact (the VM
  # images, the riscv rootfs, and packaging all bake it from there); we just
  # also assemble a self-contained runnable image under out/multicall/.
  local targets=(out/wash)
  [[ "$test_app" == "1" ]] && targets+=(out/wash-priv-fakesudo)
  [[ "$no_sudo" != "1" ]]  && targets+=(out/wash-sudo)
  make "${make_args[@]}" "${targets[@]}"
  # Assemble the multicall layout in its OWN directory so it never shares a
  # folder with the standalone binaries in out/ — no more "refused: not a
  # symlink" collisions in --both. Hardlink the dispatcher (not a symlink:
  # the router resolves its app dir from its real executable's directory, so
  # a hardlink keeps discovery rooted at out/multicall/ while a symlink would
  # bounce it back to out/). Hardlink costs no disk; fall back to a copy if
  # out/ ever spans a filesystem boundary.
  rm -rf out/multicall && mkdir -p out/multicall
  cp -l out/wash out/multicall/wash 2>/dev/null || cp out/wash out/multicall/wash
  # wash-sudo + wash-priv-fakesudo are real binaries install-symlinks won't
  # touch — copy them in so out/multicall/ is a complete, runnable image.
  [[ "$test_app" == "1" && -e out/wash-priv-fakesudo ]] && cp out/wash-priv-fakesudo out/multicall/
  [[ "$no_sudo" != "1"  && -e out/wash-sudo          ]] && cp out/wash-sudo          out/multicall/
  ./out/multicall/wash install-symlinks ./out/multicall
  built+=("multicall"$'\t'"out/multicall/ (wash + wash-* symlinks)")
}

build_display_binary() {
  echo "build.sh: wash-display (native compositor, $([[ $display_vendored == 1 ]] && echo 'vendored wlroots — this is slow' || echo 'system wlroots'))"
  local dargs=(WASH_DISPLAY=1)
  [[ "$display_vendored" == "1" ]] && dargs+=(WLROOTS_VENDORED=1)
  make "${make_args[@]}" "${dargs[@]}" out/wash-display
  built+=("wash-display"$'\t'"out/wash-display ($([[ $display_vendored == 1 ]] && echo vendored || echo system) wlroots)")
}

build_vm_helpers() {
  echo "build.sh: vm helpers (washvm-run, washnet-demo, washnet-matrix)"
  make "${make_args[@]}" out/washvm-run out/washnet-demo out/washnet-matrix
  built+=("vm helpers"$'\t'"washvm-run, washnet-demo, washnet-matrix")
}

build_browser_vm() {
  echo "build.sh: browser VM (in-browser riscv emulator — docker cross-build, slow first time)"
  # `make vm` cross-builds the riscv rootfs + kernel + firmware + WASM in Docker
  # and installs them into wash-vm/web/public/tinyemu/.
  make "${make_args[@]}" vm
  # The demo server serves the wash shell FE over the wire, so build it too —
  # then the image is ready to serve with no extra step.
  pnpm -F @wash/shell build
  # Drop a convenience launcher at out/wash-vm so it's one command to run. The
  # heredoc is quoted ('LAUNCH') — nothing expands now; the script resolves its
  # own paths at run time.
  cat > out/wash-vm <<'LAUNCH'
#!/usr/bin/env bash
# wash-vm — launch the in-browser RISC-V wash demo. Generated by
# `./build.sh --browser-vm`; lives in out/ (gitignored). Just run it:
#   ./out/wash-vm            # serve at http://localhost:12000
#   PORT=8080 ./out/wash-vm  # serve on another port
# It serves the demo page; the RISC-V machine boots client-side in your
# browser and auto-starts wash inside the guest.
set -euo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/.." && pwd)"
WEB="$REPO/wash-vm/web"
TINYEMU="$WEB/public/tinyemu"
missing=()
for f in wash-kernel.bin wash-rootfs.ext2 riscvemu64-wasm.wasm; do
  [[ -e "$TINYEMU/$f" ]] || missing+=("wash-vm/web/public/tinyemu/$f")
done
[[ -d "$REPO/web/shell/dist" ]] || missing+=("web/shell/dist (the wash UI)")
if (( ${#missing[@]} )); then
  echo "wash-vm: missing build output:" >&2
  printf '  - %s\n' "${missing[@]}" >&2
  echo "wash-vm: run  ./build.sh --browser-vm  first." >&2
  exit 1
fi
# wash-vm/web is NOT a root pnpm-workspace member, so a plain install skips it.
if [[ ! -x "$WEB/node_modules/.bin/vite" ]]; then
  echo "wash-vm: installing web deps (pnpm install --ignore-workspace)…"
  ( cd "$WEB" && pnpm install --ignore-workspace )
fi
PORT="${PORT:-12000}"; HOST="${HOST:-0.0.0.0}"
echo "wash-vm: serving the in-browser RISC-V wash demo"
echo "wash-vm:   → http://localhost:$PORT   (binds $HOST; Ctrl-C to stop)"
if command -v xdg-open >/dev/null 2>&1; then
  ( sleep 1.5; xdg-open "http://localhost:$PORT" >/dev/null 2>&1 || true ) &
fi
cd "$WEB"
exec env PORT="$PORT" HOST="$HOST" node server/server.mjs
LAUNCH
  chmod 0755 out/wash-vm
  built+=("browser VM"$'\t'"./out/wash-vm to serve (→ http://localhost:12000)")
}

# Order: app layouts first (standalone before multicall so --both's
# symlink pass sees the real binaries), then the optional extras.
[[ "$build_standalone" == "1" ]] && build_standalone_layout
[[ "$build_multicall"  == "1" ]] && build_multicall_layout
[[ "$build_display"    == "1" ]] && build_display_binary
[[ "$want_vm_helpers"  == "1" ]] && build_vm_helpers
[[ "$want_browser_vm"  == "1" ]] && build_browser_vm

# ── summary ───────────────────────────────────────────────────────────────
echo
echo "build.sh: summary ($(go env GOOS 2>/dev/null)/$(go env GOARCH 2>/dev/null), ${SECONDS}s)"
for line in "${built[@]}"; do
  printf '  ✓ %-13s%s\n' "${line%%$'\t'*}" "${line#*$'\t'}"
done
# What this host could build but this run didn't — with the command to get it.
if [[ "$build_display" != "1" ]]; then
  printf '  – %-13s%s\n' "wash-display" "${display_note:-not built (./build.sh --display)}"
fi
if [[ "$want_vm_helpers" != "1" ]]; then
  printf '  – %-13s%s\n' "vm helpers" "not built (./build.sh --vm-helpers)"
fi
if [[ "$want_browser_vm" != "1" ]]; then
  printf '  – %-13s%s\n' "browser VM" "in-browser riscv demo (./build.sh --browser-vm)"
fi
printf '  – %-13s%s\n' "vm images" "test fixtures, not host binaries (make vm-image* / ./test.sh --vm)"
# Sizes of the two binaries people most often sanity-check.
ls -lh out/wash-router out/wash 2>/dev/null | awk '{printf "  · %s\t%s\n",$5,$9}' || true
echo "build.sh: done"
