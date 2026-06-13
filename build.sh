#!/usr/bin/env bash
# build.sh — wash build entrypoint.
#
# Layout modes (binary shape; mutually exclusive; default --standalone):
#   --standalone (default) — N separate binaries under out/ (one
#                            per wash-<name>). Production layout.
#   --multicall            — single out/wash binary (busybox-style)
#                            with wash-<name> symlinks in out/. Smaller
#                            footprint; all apps share one Go runtime.
#   --both                 — both of the above (for test.sh --both).
#
# Extra targets (additive; combine with any layout mode):
#   --display              — also build the native wash-display
#                            compositor (C++/CMake/wlroots, its own
#                            project — NOT the Go build). Uses a system
#                            wlroots if pkg-config finds one, else
#                            compiles the vendored 0.17 copy (slow).
#   --vm-helpers           — also build the host VM helper CLIs
#                            (washvm-run, washnet-demo, washnet-matrix).
#                            Pure Go; the --vm / --vm-gates test tiers
#                            need them.
#
# Everything this host can build:
#   --all                  — both layouts + vm helpers + wash-display
#                            (only when a system wlroots is present, so
#                            --all never triggers a surprise wlroots
#                            compile — use --display for that). Does NOT
#                            build the VM *images* (test fixtures; make
#                            vm-image*) or the riscv browser demo
#                            (make rv) — neither is a host binary.
#
# Modifiers:
#   --no-test-app          — exclude wash-test + fakesudo stub
#   --no-sudo              — skip wash-sudo (headless/kiosk)
#   --clean                — make clean first
#   -j <N> | --jobs <N>    — parallel make jobs. Default: nproc.
#   -n | --dry-run         — print the build plan + summary; build nothing.
#   -h | --help            — this header.
#
# Every run ends with a capability summary: what was built, and what
# this host *could* build but didn't (with the command to get it) — so
# a missing compositor or VM image is never a silent surprise later.
# All forms pass TEST_APP=1 unless --no-test-app.

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
    --no-test-app)  test_app=0; shift;;
    --no-sudo)      no_sudo=1; shift;;
    --clean)        clean=1; shift;;
    -n|--dry-run)   dry=1; shift;;
    -j|--jobs)      jobs="$2"; shift 2;;
    -j*)            jobs="${1#-j}"; shift;;
    -h|--help)
      sed -n '1,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
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
        --vm*|--helpers)           echo "  did you mean --vm-helpers?" >&2;;
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
  [[ "$build_multicall"   == "1" ]] && plan_line "✓" "multicall"   "out/wash + symlinks"
  [[ "$build_display"     == "1" ]] && plan_line "✓" "wash-display" "compositor ($([[ $display_vendored == 1 ]] && echo 'vendored wlroots — slow' || echo 'system wlroots'))"
  [[ "$build_display"     != "1" ]] && plan_line "–" "wash-display" "${display_note:-not requested (./build.sh --display)}"
  [[ "$want_vm_helpers"   == "1" ]] && plan_line "✓" "vm helpers"  "washvm-run, washnet-demo, washnet-matrix"
  [[ "$want_vm_helpers"   != "1" ]] && plan_line "–" "vm helpers"  "not requested (./build.sh --vm-helpers)"
  plan_line "–" "vm images" "test fixtures — make vm-image* (or ./test.sh --vm)"
  plan_line "–" "riscv demo" "browser/cross artifact — make rv"
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
  echo "build.sh: multicall (single wash binary + symlinks)"
  # Real binaries the multi-call layout still needs:
  #   wash               — the dispatcher itself (router + launch + apps)
  #   wash-sudo          — CLI; suid-on-symlink no-ops, so this stays its
  #                        own file (skipped with --no-sudo)
  #   wash-priv-fakesudo — test-only sudo stub
  local targets=(out/wash)
  [[ "$test_app" == "1" ]] && targets+=(out/wash-priv-fakesudo)
  [[ "$no_sudo" != "1" ]]  && targets+=(out/wash-sudo)
  make "${make_args[@]}" "${targets[@]}"
  # Clear any stale per-app + CLI binaries left over from a prior
  # standalone build so install-symlinks can lay down its links without
  # "refused: not a symlink" warnings. wash-sudo + wash-priv-fakesudo are
  # real binaries — never replace them.
  local name f
  for name in $(./out/wash list-apps) wash-router wash-launch; do
    f="out/$name"
    [[ -e "$f" || -L "$f" ]] || continue
    [[ -L "$f" ]] && continue
    case "$name" in wash|wash-sudo|wash-priv-fakesudo) continue;; esac
    rm -f "$f"
  done
  # In --both the standalone build already put real binaries at the app
  # paths; install-symlinks refuses to clobber them (correct) — filter
  # that noise but keep the tally.
  if [[ "$build_standalone" == "1" ]]; then
    ./out/wash install-symlinks ./out 2>&1 | grep -v "^refused:" || true
    built+=("multicall"$'\t'"out/wash (symlinks skip real standalone binaries)")
  else
    ./out/wash install-symlinks ./out
    built+=("multicall"$'\t'"out/wash + wash-* symlinks")
  fi
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

# Order: app layouts first (standalone before multicall so --both's
# symlink pass sees the real binaries), then the optional extras.
[[ "$build_standalone" == "1" ]] && build_standalone_layout
[[ "$build_multicall"  == "1" ]] && build_multicall_layout
[[ "$build_display"    == "1" ]] && build_display_binary
[[ "$want_vm_helpers"  == "1" ]] && build_vm_helpers

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
printf '  – %-13s%s\n' "vm images" "test fixtures, not host binaries (make vm-image* / ./test.sh --vm)"
printf '  – %-13s%s\n' "riscv demo" "browser/cross artifact (make rv)"
# Sizes of the two binaries people most often sanity-check.
ls -lh out/wash-router out/wash 2>/dev/null | awk '{printf "  · %s\t%s\n",$5,$9}' || true
echo "build.sh: done"
