#!/usr/bin/env bash
# clean.sh — remove wash build artifacts for a from-scratch rebuild.
#
# Tiers (composable; default = in-tree build artifacts only):
#   (default)     out/ (binaries + VM images), all FE dist/, embedded BE
#                 assets, web vendor/, packaging output (dist/ + build-ctx/),
#                 wash-display native build dirs, e2e + coverage results,
#                 stray root binaries / image / demo / tool output.
#   --tmp         also /tmp/wash-* + /run/user/$UID/wash* runtime junk
#                 (test temp dirs, sockets, smoke-harness scratch).
#   --docker      also the matrix Docker images (wash-build / wash-binaries /
#                 wash-display-bin / wash-pkg) + buildx cache + dangling.
#   --deep        also node_modules (root + web + e2e) and the Go build cache.
#                 NB: makes the next build slow (pnpm install + full recompile).
#   --all         --tmp --docker --deep (true from-scratch).
#   -n|--dry-run  print what would be removed; remove nothing.
#   -h|--help     this header.
#
# NEVER touched (precious / not build output): tmp/ (mac-phoenix, greenfield,
# the compositor smoke harness), branches/ (git worktrees), harbor.config,
# test-net.py, .git/. node_modules survives unless --deep.
#
# Artifact map: see docs in this header + the README; mirrors .gitignore.
set -uo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO"

do_tmp=0 do_docker=0 do_deep=0 dry=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --tmp)        do_tmp=1; shift;;
    --docker)     do_docker=1; shift;;
    --deep)       do_deep=1; shift;;
    --all)        do_tmp=1; do_docker=1; do_deep=1; shift;;
    -n|--dry-run) dry=1; shift;;
    -h|--help)    sed -n '2,/^set -uo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'; exit 0;;
    *) echo "clean.sh: unknown flag: $1" >&2; exit 2;;
  esac
done

shopt -s nullglob

# rm helper — honors --dry-run, prints each path, tolerates absent paths.
gone() {
  for p in "$@"; do
    [[ -e "$p" || -L "$p" ]] || continue
    if [[ "$dry" == "1" ]]; then
      printf '  would remove  %s  (%s)\n' "$p" "$(du -sh "$p" 2>/dev/null | cut -f1 || echo '?')"
    else
      printf '  rm  %s\n' "$p"
      rm -rf -- "$p"
    fi
  done
}

echo "════ clean.sh: in-tree build artifacts ════"
# Compiled output + VM images.
gone out
# FE bundles (Vite) + embedded BE assets (//go:embed copies).
gone web/*/dist apps/*/fe/dist wash-display/fe/dist
gone apps/*/be/assets cmd/*/assets internal/apps/*/assets \
     internal/runner/*/assets internal/login/assets/shell
gone web/shell/public/vendor
# Packaging: matrix output + per-row build contexts.
gone dist packaging/build-ctx
# wash-display native build (CMake + vendored-wlroots meson + install prefix).
gone wash-display/build wash-display/third_party/wlroots/build wash-display/.wlroots
gone wash-display/*.deb
# Test + coverage artifacts.
gone e2e/test-results e2e/playwright-report test-results coverage
# Stray dev binaries / out-of-tree image + demo + tooling output.
gone wash wash-router image image-rv web/demo .understand-anything

if [[ "$do_tmp" == "1" ]]; then
  echo "════ clean.sh: /tmp + runtime junk ════"
  gone /tmp/wash-* /tmp/wd-smoke
  # run_matrix + smoke mktemp dirs (only ours: those holding a wash tree).
  for d in /tmp/tmp.*; do [[ -e "$d/wash-"* ]] && gone "$d"; done
  gone "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/wash" \
       "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}"/wash-*.sock
fi

if [[ "$do_docker" == "1" ]]; then
  echo "════ clean.sh: Docker matrix images + buildx cache ════"
  if command -v docker >/dev/null 2>&1; then
    imgs=$(docker images --format '{{.Repository}}:{{.Tag}}' 2>/dev/null \
            | grep -E '^(wash-build|wash-binaries|wash-display-bin|wash-pkg)(:|-disp:)' || true)
    if [[ -n "$imgs" ]]; then
      if [[ "$dry" == "1" ]]; then echo "$imgs" | sed 's/^/  would docker rmi  /'
      else echo "$imgs" | xargs -r docker rmi -f >/dev/null 2>&1; echo "  removed $(echo "$imgs" | wc -l) images"; fi
    else echo "  (no wash matrix images)"; fi
    [[ "$dry" == "1" ]] || { docker buildx prune -f >/dev/null 2>&1 || true; docker container prune -f >/dev/null 2>&1 || true; }
  else echo "  (docker not installed)"; fi
fi

if [[ "$do_deep" == "1" ]]; then
  echo "════ clean.sh: deep (node_modules + Go cache) ════"
  gone node_modules web/*/node_modules e2e/node_modules
  if [[ "$dry" == "1" ]]; then echo "  would: go clean -cache"
  else go clean -cache 2>/dev/null && echo "  go build cache cleared"; fi
fi

echo
[[ "$dry" == "1" ]] && echo "clean.sh: dry run — nothing removed." || echo "clean.sh: done. Rebuild with: make all   (or ./build.sh)"
