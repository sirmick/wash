#!/usr/bin/env python3
# Procedural synthwave wallpaper for the Retro pack — emitted as crisp,
# tiny vector SVG (no tracing). Palette chosen to sit with the pack's
# Win9x chrome: a navy sky fading to teal at the horizon, a teal neon
# grid (echoing the classic teal desktop), an amber sun, a black skyline.
# Deterministic (seeded) so re-running gives the identical file.

import random

random.seed(7)
W, H = 1920, 1080
horizon = int(H * 0.60)          # 648
cx = W / 2

out = [f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" '
       f'viewBox="0 0 {W} {H}" preserveAspectRatio="xMidYMid slice">']

# --- gradients (sky uses userSpaceOnUse so sun slits filled with url(#sky) blend) ---
out.append('<defs>')
out.append(f'<linearGradient id="sky" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="0" y2="{horizon}">'
           '<stop offset="0" stop-color="#02030f"/>'
           '<stop offset="0.45" stop-color="#0a1a52"/>'
           '<stop offset="0.80" stop-color="#1f4f86"/>'
           '<stop offset="1" stop-color="#2e7e8c"/></linearGradient>')
out.append('<linearGradient id="sun" x1="0" y1="0" x2="0" y2="1">'
           '<stop offset="0" stop-color="#ffd96b"/>'
           '<stop offset="0.5" stop-color="#ff8a3a"/>'
           '<stop offset="1" stop-color="#ff4f72"/></linearGradient>')
out.append(f'<linearGradient id="ground" gradientUnits="userSpaceOnUse" x1="0" y1="{horizon}" x2="0" y2="{H}">'
           '<stop offset="0" stop-color="#06121a"/>'
           '<stop offset="1" stop-color="#01060a"/></linearGradient>')
out.append('</defs>')

# --- sky + ground ---
out.append(f'<rect x="0" y="0" width="{W}" height="{horizon}" fill="url(#sky)"/>')
out.append(f'<rect x="0" y="{horizon}" width="{W}" height="{H - horizon}" fill="url(#ground)"/>')

# --- sun (fully above the horizon; the skyline crosses its lower half) ---
sun_r = int(H * 0.25)
sun_cy = horizon - sun_r - 6
out.append(f'<circle cx="{cx:.0f}" cy="{sun_cy}" r="{sun_r}" fill="url(#sun)"/>')
# horizontal slits in the lower half — filled with the sky so they read as gaps
y, th, gap = sun_cy + int(sun_r * 0.18), 4, 7
while y < sun_cy + sun_r:
    out.append(f'<rect x="{cx - sun_r:.0f}" y="{y}" width="{sun_r * 2}" height="{th}" fill="url(#sky)"/>')
    y += th + gap
    th += 1
    gap += 4

# --- black city skyline along the horizon (random heights) ---
out.append('<g fill="#000000">')
x = 0
while x < W:
    bw = random.randint(20, 50)
    bh = random.randint(24, 210)
    out.append(f'<rect x="{x}" y="{horizon - bh}" width="{bw}" height="{bh}"/>')
    if random.random() < 0.22:                       # occasional antenna
        ah = random.randint(10, 26)
        out.append(f'<rect x="{x + bw // 2 - 1}" y="{horizon - bh - ah}" width="2" height="{ah}"/>')
    x += bw + random.randint(1, 6)
out.append('</g>')

# --- teal neon perspective grid on the ground ---
out.append('<g stroke="#26b8b2" stroke-width="1.6" opacity="0.72">')
for xb in range(-W, 2 * W + 1, 84):                  # verticals fan from the vanishing point
    out.append(f'<line x1="{cx:.0f}" y1="{horizon}" x2="{xb}" y2="{H}"/>')
M = 16
for k in range(1, M + 1):                            # horizontals, denser near the horizon
    yy = horizon + (H - horizon) * ((k / M) ** 1.8)
    out.append(f'<line x1="0" y1="{yy:.1f}" x2="{W}" y2="{yy:.1f}"/>')
out.append('</g>')

# --- bright horizon line ---
out.append(f'<rect x="0" y="{horizon - 2}" width="{W}" height="3" fill="#3fe3da" opacity="0.85"/>')

out.append('</svg>')
open('web/shell/public/wallpapers/retro.svg', 'w').write('\n'.join(out))
print('wrote retro.svg')
