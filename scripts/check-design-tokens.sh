#!/usr/bin/env bash
# check-design-tokens — the design-language drift guard.
#
# wash's FE chrome is consolidated onto @wash/ui design tokens (web/lib/src/
# tokens.ts): every color goes through `tokens.*` so the 6 theme packs can
# live-reswap it via CSS vars. A raw hex literal that EQUALS a token's
# fallback is therefore drift — it renders fine on the default pack but stays
# frozen on Seoul/Copland/Oslo/etc. This guard fails if any such hex reappears
# in app/shell/lib FE source, so the consolidation can't silently rot.
#
# The hex set is DERIVED from tokens.ts at runtime (the var(--wash-*, #hex)
# fallbacks), so the guard never drifts from the token table it polices.
#
# Deliberately NOT flagged: raw hex that is NOT a token fallback — wallpaper /
# desktop gradients, VS Code brand colors (#007acc/#1e1e1e), and the loud
# white-on-red root/dev alarm cues (#fff ≠ the off-white fg token #eee). Also
# exempt by file: tokens.ts / packs.ts (where the hexes legitimately live) and
# terminal.tsx (the xterm ITheme palette — a real terminal color set, not
# chrome). Those are intentional non-token color and out of scope.
set -euo pipefail
cd "$(dirname "$0")/.."

TOKENS=web/lib/src/tokens.ts

# Extract the fallback hexes from `var(--wash-name, #hex)` declarations.
hexes=$(grep -oE 'var\(--wash-[a-z-]+, *#[0-9a-fA-F]{3,8}\)' "$TOKENS" \
        | grep -oE '#[0-9a-fA-F]{3,8}' | sort -u)
if [ -z "$hexes" ]; then
  echo "check-design-tokens: could not derive token hexes from $TOKENS" >&2
  exit 2
fi
alt=$(printf '%s' "$hexes" | paste -sd'|' -)

# Where to police, and what to never police (token/pack definitions are where
# these hexes legitimately live; *.test.ts / *ctest.ts are fixtures/asserts).
roots="apps web/shell/src web/lib/src"
hits=$(grep -rniE "(^|[^a-zA-Z0-9_-])($alt)([^0-9a-fA-F]|$)" $roots \
         --include='*.ts' --include='*.tsx' 2>/dev/null \
       | grep -vE 'web/lib/src/tokens\.ts|web/lib/src/packs\.ts|web/lib/src/terminal\.tsx' \
       | grep -vE '\.test\.ts|ctest\.ts' \
       | grep -vE 'var\(--wash' || true)

if [ -n "$hits" ]; then
  echo "check-design-tokens: raw hex matching a @wash/ui token fallback (use tokens.* so packs can theme it):"
  echo "$hits" | sed 's/^/  /'
  echo "check-design-tokens: replace each with the matching tokens.<name> (see web/lib/src/tokens.ts)"
  exit 1
fi
echo "check-design-tokens: no token-fallback hex drift in FE source ($(printf '%s' "$hexes" | wc -l | tr -d ' ') token colors policed)"
