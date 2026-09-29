#!/usr/bin/env bash
# check-types — the full TypeScript check over every frontend package.
#
# `vite build` does NOT typecheck. An identifier that was never imported is
# not a build error to esbuild — it compiles to a bare global reference and
# the bundle ships happily. The failure only appears when that line executes
# in a browser, as `Uncaught ReferenceError: X is not defined`, which in a
# desktop full of lazily-mounted apps can mean a pane nobody opened during
# testing. That is exactly how a missing WASH_ROW_CLASS import reached a
# running router once, past a green build AND a green component-test run.
#
# This gated on TS2304 alone for as long as a bare tsc could not go green:
# the configs lacked allowImportingTsExtensions and @types/node, which is
# most of what it reported, and a check that can never pass never gets run.
# Both are configured now and every package is clean, so the gate is the
# whole compiler. That is worth more than TS2304 was: the narrow version
# shipped a window switcher styled `font: undefined` (a token that did not
# exist), a taskbar blind to the attention flag it reads, and a delete
# confirmation that ran its paths together — all type errors, none of them
# TS2304.
#
# NOTE ON tsc: `npx tsc` is NOT safe here. There is a decoy package on npm
# named "tsc" that prints "This is not the tsc command you are looking for"
# and exits non-zero WITHOUT compiling anything — if npx resolves that, this
# script silently checks nothing and reports success. We therefore resolve a
# real typescript binary from the workspace and verify it identifies itself.
set -euo pipefail
cd "$(dirname "$0")/.."

# Resolve a real tsc from the workspace, never via npx (see note above).
TSC=""
for cand in web/lib/node_modules/.bin/tsc e2e/node_modules/.bin/tsc \
            node_modules/.bin/tsc node_modules/typescript/bin/tsc; do
  [ -x "$cand" ] || continue
  # A real tsc answers --version with "Version <semver>".
  if "$cand" --version 2>/dev/null | grep -qE '^Version [0-9]'; then
    TSC="$PWD/$cand"; break
  fi
done
if [ -z "$TSC" ]; then
  echo "check-types: no real tsc found in the workspace" >&2
  echo "check-types: run 'pnpm install' first" >&2
  exit 2
fi

hits=""
checked=0
for d in apps/*/fe web/shell web/lib wash-display/fe; do
  [ -f "$d/tsconfig.json" ] || continue
  checked=$((checked + 1))
  out=$("$TSC" --noEmit -p "$d/tsconfig.json" 2>&1 | grep -E 'error TS' || true)
  [ -n "$out" ] && hits="${hits}${out}"$'\n'
done

if [ -n "$hits" ]; then
  echo "check-types: the frontends do not typecheck (vite will NOT catch this):"
  printf '%s' "$hits" | sed 's/^/  /'
  echo "check-types: a TS2304 here is a runtime ReferenceError; the rest ship as silently wrong behaviour"
  exit 1
fi
echo "check-types: FE source typechecks clean ($checked packages checked)"
