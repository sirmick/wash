#!/bin/sh
# Assemble the static project site into OUT (default out/site): the hand-written
# pages in site/, plus the screenshots, logo, wallpaper, theme thumbnails and
# font they show, pulled from where the repo already keeps them. No toolchain
# beyond POSIX sh. The Pages workflow puts the browser demo under OUT/demo/.
#
#   site/build.sh [OUT]
set -eu
REPO="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$REPO/out/site}"
VERSION="$(cat "$REPO/VERSION")"

rm -rf "$OUT"
mkdir -p "$OUT/img/thumbs" "$OUT/assets"
cp "$REPO/site/style.css" "$REPO/site/site.js" "$OUT/"
sed "s/__VERSION__/$VERSION/g" "$REPO/site/index.html" > "$OUT/index.html"
cp "$REPO"/docs/screenshots/*.png "$OUT/img/"
cp "$REPO"/web/shell/public/wallpapers/thumbs/*.jpg "$OUT/img/thumbs/"
cp "$REPO/web/shell/public/wash-logo.svg" \
   "$REPO/web/shell/public/wallpapers/midnight.svg" \
   "$REPO/web/shell/public/fonts/JetBrainsMono-Regular.woff2" "$OUT/assets/"
echo "site: built $OUT ($(du -sh "$OUT" | cut -f1))"
