#!/usr/bin/env bash
# check-undefined-names — the "it built fine and then blew up in the browser" guard.
#
# `vite build` does NOT typecheck. An identifier that was never imported is
# not a build error to esbuild — it compiles to a bare global reference and
# the bundle ships happily. The failure only appears when that line executes
# in a browser, as `Uncaught ReferenceError: X is not defined`, which in a
# desktop full of lazily-mounted apps can mean a pane nobody opened during
# testing. That is exactly how a missing WASH_ROW_CLASS import reached a
# running router once, past a green build AND a green component-test run.
#
# So: run the real compiler and fail on TS2304 ("Cannot find name") only.
#
# Why only TS2304, when tsc reports plenty else? Because these tsconfigs are
# not built to pass a bare `tsc` — vite owns module resolution, so extension
# imports (TS5097), node:test types (TS2307) and some cross-package structural
# mismatches are expected noise, and gating on them would mean a check that
# can never go green and therefore never gets run. TS2304 is the subset that
# is unambiguously a bug, is clean today, and is precisely the class that a
# bundler will happily ship. Keep the scope tight so the gate stays honest.
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
  echo "check-undefined-names: no real tsc found in the workspace" >&2
  echo "check-undefined-names: run 'pnpm install' first" >&2
  exit 2
fi

hits=""
checked=0
for d in apps/*/fe web/shell web/lib; do
  [ -f "$d/tsconfig.json" ] || continue
  checked=$((checked + 1))
  out=$("$TSC" --noEmit -p "$d/tsconfig.json" 2>&1 | grep -E 'error TS2304' || true)
  [ -n "$out" ] && hits="${hits}${out}"$'\n'
done

if [ -n "$hits" ]; then
  echo "check-undefined-names: identifier used but never imported (the bundler will NOT catch this):"
  printf '%s' "$hits" | sed 's/^/  /'
  echo "check-undefined-names: add the missing import — these are runtime ReferenceErrors, not type nits"
  exit 1
fi
echo "check-undefined-names: no undefined identifiers in FE source ($checked packages checked)"
