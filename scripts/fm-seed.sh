#!/usr/bin/env bash
# fm-seed — populate the wash-fm sandbox dir at /tmp/wash-fm-smoke
# (or a path passed as $1) with a small fixture tree.
#
# Pair with `dev-restart.sh --fm-root /tmp/wash-fm-smoke` to bring
# the live router up sandboxed against this tree. Existing contents
# are wiped — this is a fresh seed, not an upsert.

set -euo pipefail

ROOT="${1:-/tmp/wash-fm-smoke}"

rm -rf "$ROOT"
mkdir -p "$ROOT/docs"
printf 'hello world\n'                       > "$ROOT/hello.txt"
printf '\x00\x01\x02\x03\x00\x04'            > "$ROOT/binary.bin"
printf '# readme\n\nfor wash-fm tests.\n'    > "$ROOT/docs/readme.md"

echo "fm-seed: seeded $ROOT"
ls -la "$ROOT"
