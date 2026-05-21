#!/usr/bin/env bash
# seed-bulk-fixture — populate a tree heavy enough to exercise the
# bulk-ops UI: 100 root-level files plus 10 subdirs of 20 files
# each (= 300 files + 10 dirs, ≥ 311 items to walk under the root).
# Pair with `dev-restart.sh --fm-seed PATH` and a non-zero
# WASH_BULKOPS_ITEM_DELAY_MS so the progress bar advances visibly.
#
# Usage: scripts/seed-bulk-fixture.sh [ROOT]
#        ROOT defaults to /tmp/wash-fm-smoke.
#
# Existing contents at ROOT are wiped first — this is a fresh seed.

set -euo pipefail

ROOT="${1:-/tmp/wash-fm-smoke}"

rm -rf "$ROOT"
mkdir -p "$ROOT"

# 100 root-level files. Use printf with %03d so they sort naturally
# in the tree (file-000, file-001, …).
for i in $(seq 0 99); do
  printf 'content-%03d\n' "$i" > "$(printf '%s/file-%03d.txt' "$ROOT" "$i")"
done

# 10 subdirs, each with 20 files.
for d in $(seq 0 9); do
  dir="$(printf '%s/dir-%02d' "$ROOT" "$d")"
  mkdir -p "$dir"
  for f in $(seq 0 19); do
    printf 'dir %d, file %02d\n' "$d" "$f" > "$(printf '%s/leaf-%02d.txt' "$dir" "$f")"
  done
done

# Quick summary so the user can sanity-check the seed.
echo "seed-bulk-fixture: $ROOT seeded"
echo "  root files : $(find "$ROOT" -maxdepth 1 -type f | wc -l)"
echo "  subdirs    : $(find "$ROOT" -mindepth 1 -maxdepth 1 -type d | wc -l)"
echo "  total files: $(find "$ROOT" -type f | wc -l)"
