#!/usr/bin/env bash
# build.sh — wash build entrypoint.
#
# Modes:
#   --standalone (default) — N separate binaries under out/ (one
#                            per wash-<name>). Same layout the
#                            production install has used since v0.0.
#   --multicall            — single out/wash binary (busybox-style)
#                            with wash-<name> symlinks materialized
#                            in out/. Smaller total disk footprint;
#                            all apps share one Go runtime.
#   --both                 — both of the above. Useful for
#                            running test.sh against both layouts.
#
# Other flags:
#   --no-test-app          — exclude wash-test (defaults: included)
#   --no-sudo              — skip building wash-sudo (terminal `sudo`
#                            wrapper; opt-out for headless/kiosk)
#   --clean                — wipe out/ + cmd/*/assets first
#   -j <N> | --jobs <N>    — parallel make jobs. Default: nproc.
#                            Pass 1 to force serial.
#
# All forms pass TEST_APP=1 to make by default; use --no-test-app
# to opt out for a production-style build.

set -euo pipefail

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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --standalone)   mode=standalone; shift;;
    --multicall|--bb|--busybox)
                    mode=multicall; shift;;
    --both)         mode=both; shift;;
    --no-test-app)  test_app=0; shift;;
    --no-sudo)      no_sudo=1; shift;;
    --clean)        clean=1; shift;;
    -j|--jobs)      jobs="$2"; shift 2;;
    -j*)            jobs="${1#-j}"; shift;;
    -h|--help)
      sed -n '1,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
      exit 0
      ;;
    *)
      echo "build.sh: unknown flag: $1" >&2
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
echo "build.sh: parallel jobs = $jobs"

# Args common to every `make` invocation. Putting -j first so it
# applies to the goal targets even when the call is "make -j<N> tgt".
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

case "$mode" in
  standalone)
    echo "build.sh: standalone (separate per-app binaries)"
    make "${make_args[@]}" all
    ;;
  multicall)
    echo "build.sh: multicall (single wash binary + symlinks)"
    # Real binaries the multi-call layout still needs:
    #   wash               — the dispatcher itself (router + launch +
    #                        12 apps all linked in)
    #   wash-sudo          — CLI; suid-on-symlink no-ops, so this has
    #                        to stay its own file (skipped with --no-sudo)
    #   wash-priv-fakesudo — test-only sudo stub
    mc_targets=(out/wash)
    # wash-priv-fakesudo is a test-only sudo stub; only build it
    # when the test app is being built too.
    if [[ "$test_app" == "1" ]]; then
      mc_targets+=(out/wash-priv-fakesudo)
    fi
    if [[ "$no_sudo" != "1" ]]; then
      mc_targets+=(out/wash-sudo)
    fi
    make "${make_args[@]}" "${mc_targets[@]}"
    # Clear any stale per-app + CLI binaries left over from a prior
    # standalone build so install-symlinks can lay down its links
    # without "refused: not a symlink" warnings. Names come from
    # `wash list-apps` plus the wash-router / wash-launch CLIs that
    # install-symlinks also materializes. wash-sudo +
    # wash-priv-fakesudo are real binaries — never replace them.
    apps_to_link=$(./out/wash list-apps)
    for name in $apps_to_link wash-router wash-launch; do
      f="out/$name"
      [[ -e "$f" || -L "$f" ]] || continue
      [[ -L "$f" ]] && continue
      case "$name" in
        wash|wash-sudo|wash-priv-fakesudo) continue;;
      esac
      rm -f "$f"
    done
    ./out/wash install-symlinks ./out
    ;;
  both)
    echo "build.sh: both layouts"
    make "${make_args[@]}" all
    make "${make_args[@]}" out/wash
    # install-symlinks intentionally refuses to clobber real files —
    # in --both mode those real binaries are exactly the standalone
    # build's output, so the refusal is correct. Filter the message
    # out of stdout (it's noise here) but keep the final tally.
    ./out/wash install-symlinks ./out 2>&1 | grep -v "^refused:" || true
    ;;
esac

echo "build.sh: done"
ls -lh out/wash-router out/wash 2>/dev/null | awk '{print "  "$5"\t"$9}' || true
