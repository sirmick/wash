#!/usr/bin/env python3
"""Copland pack wallpaper: a 90s ray-traced desktop, in the pack's Platinum palette.

Copland was the Mac OS that never shipped; its Platinum look did (8.5/9).
This is the desktop that era would have rendered in Bryce or Kai's Power
Tools overnight: a periwinkle sky fading to a lavender horizon glow, an
infinite perspective checkerboard, and three spheres — chrome, glossy
periwinkle, platinum — with soft contact shadows, under a faint 1-bit Mac
dither. Every colour is taken from (or sits between) the pack's own scheme
(web/lib/src/packs.ts `copland`), so the chrome and the desktop agree.

Original artwork, generated — no source image. Output is plain SVG.

    python3 design/packs/gen-copland.py > web/shell/public/wallpapers/copland.svg
"""

import math

W, H = 2560, 1440
HORIZON = 0.60 * H   # where the floor meets the sky
CX = 0.46 * W        # vanishing point, a little left of centre: the session
                     # sidebar covers the right edge of most desktops

# The pack's palette (packs.ts copland): periwinkle highlight #9aa0d6, accent
# #6c72b8, indigo #5a60a8, focus #606090, drop target #c0c0f0, platinum greys.
SKY_TOP = '#262a5e'
SKY_MID = '#4b5199'
SKY_LOW = '#8a90d0'
GLOW = '#d8d6f4'
FLOOR_A = '#5a60a8'
FLOOR_B = '#9aa0d6'
FLOOR_FAR = '#c0c0f0'


def fmt(v: float, places: int = 1) -> str:
    return f'{v:.{places}f}'.rstrip('0').rstrip('.')


def checker_floor() -> str:
    """Perspective checkerboard: squares of one world unit on a plane below a
    camera at height CAM_H, focal length F. Only the B squares are drawn; the
    A colour is the floor's base fill."""
    cam_h, f = 1.0, 0.62 * H
    quads = []
    z_near, z_far = 0.32, 28.0
    zs = []
    z = z_near
    while z < z_far:
        zs.append(z)
        z += 0.5
    for zi in range(len(zs) - 1):
        z0, z1 = zs[zi], zs[zi + 1]
        y0 = HORIZON + f * cam_h / z0
        y1 = HORIZON + f * cam_h / z1
        if y1 > H + 1:
            continue
        if y0 > H + 40:
            # Cut the row where it leaves the canvas by moving its near edge
            # to the depth at that y — NOT by clamping y alone, which would
            # keep the x of the original depth and bend the row's sides off
            # the vanishing point.
            z0 = f * cam_h / (H + 40 - HORIZON)
            y0 = HORIZON + f * cam_h / z0
        for xi in range(-60, 60):
            if (xi + zi) % 2:
                continue
            x0, x1 = xi * 0.5, (xi + 1) * 0.5
            pts = [
                (CX + f * x0 / z0, y0), (CX + f * x1 / z0, y0),
                (CX + f * x1 / z1, y1), (CX + f * x0 / z1, y1),
            ]
            if max(p[0] for p in pts) < -50 or min(p[0] for p in pts) > W + 50:
                continue
            # 0.01px: at 0.1px, rounding alone tilts the short edges near the
            # horizon by up to half a degree.
            pts = [(float(fmt(x, 2)), float(fmt(y, 2))) for x, y in pts]
            check_edges(pts)
            quads.append('M' + ' L'.join(f'{fmt(x, 2)} {fmt(y, 2)}' for x, y in pts) + 'Z')
    return ''.join(quads)


def check_edges(pts: list) -> None:
    """Every checker side edge must lie on its line to the vanishing point —
    a row whose sides are a few degrees off is the first thing an eye finds
    on a checkerboard. Edges under 8px are too short to see an angle on."""
    for (x0, y0), (x1, y1) in ((pts[0], pts[3]), (pts[1], pts[2])):
        if math.hypot(x1 - x0, y1 - y0) < 8:
            continue
        mx, my = (x0 + x1) / 2, (y0 + y1) / 2
        e = abs((math.atan2(y1 - y0, x1 - x0) - math.atan2(HORIZON - my, CX - mx)) % math.pi)
        deg = math.degrees(min(e, math.pi - e))
        assert deg < 0.1, f'checker edge {fmt(deg, 3)}° off the vanishing point at ({fmt(mx)}, {fmt(my)})'



def chrome_reflection(cx: float, cy: float, r: float, sid: str) -> str:
    """The floor's checker as the chrome sphere's lower half reflects it.
    Sampled on a fine grid over the disk: a point at normalized (nx, ny)
    below the equator sees the floor at a distance that grows as ny shrinks
    toward the horizon, so the squares bend around the sphere and crowd into
    the equator — the curve that sells it as chrome, not a fishbowl."""
    n = 96
    cell = 2 * r / n
    rects = []
    for i in range(n):
        for j in range(n):
            nx = -1 + (i + 0.5) * 2 / n
            ny = -1 + (j + 0.5) * 2 / n
            if nx * nx + ny * ny > 1 or ny < 0.08:
                continue
            nz = (1 - nx * nx - ny * ny) ** 0.5
            depth = 1.0 / (ny + 0.06)
            fx = nx * depth * (0.6 + 0.4 * nz)
            fz = depth
            if (int(fx * 1.6 + 100) + int(fz * 1.6)) % 2:
                continue
            rects.append(f'<rect x="{fmt(cx - r + i * cell)}" y="{fmt(cy - r + j * cell)}" '
                         f'width="{fmt(cell + 0.6)}" height="{fmt(cell + 0.6)}"/>')
    return (
        f'<clipPath id="clip{sid}"><circle cx="{fmt(cx)}" cy="{fmt(cy)}" r="{fmt(r)}"/></clipPath>'
        f'<g clip-path="url(#clip{sid})" fill="{FLOOR_B}" opacity="0.42">' + ''.join(rects) + '</g>'
    )


def sphere(cx: float, cy: float, r: float, kind: str) -> str:
    """A sphere resting on the floor at (cx, cy+r): contact shadow, body,
    rim shading and a specular highlight."""
    sid = f'{kind}{int(cx)}'
    shadow = (
        f'<ellipse cx="{fmt(cx + r * 0.18)}" cy="{fmt(cy + r * 0.98)}" rx="{fmt(r * 1.05)}" ry="{fmt(r * 0.2)}" '
        f'fill="#141633" opacity="0.55" filter="url(#soft)"/>'
    )
    body = f'<circle cx="{fmt(cx)}" cy="{fmt(cy)}" r="{fmt(r)}" fill="url(#{kind})"/>'
    rim = f'<circle cx="{fmt(cx)}" cy="{fmt(cy)}" r="{fmt(r)}" fill="url(#rim)"/>'
    spec = (
        f'<ellipse cx="{fmt(cx - r * 0.34)}" cy="{fmt(cy - r * 0.42)}" rx="{fmt(r * 0.26)}" ry="{fmt(r * 0.16)}" '
        f'transform="rotate(-32 {fmt(cx - r * 0.34)} {fmt(cy - r * 0.42)})" fill="#ffffff" opacity="0.85" filter="url(#glint)"/>'
    )
    extra = ''
    if kind == 'chrome':
        extra = chrome_reflection(cx, cy, r, sid)
    return shadow + body + extra + rim + spec


def main() -> None:
    out = []
    out.append('<?xml version="1.0" encoding="UTF-8"?>')
    out.append('<!-- Copland pack wallpaper: a 90s ray-traced desktop in the Platinum palette. '
               'Generated by design/packs/gen-copland.py (original artwork). -->')
    out.append(f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" '
               'preserveAspectRatio="xMidYMid slice">')
    out.append('<defs>')
    out.append(f'<linearGradient id="sky" x1="0" y1="0" x2="0" y2="1">'
               f'<stop offset="0" stop-color="{SKY_TOP}"/><stop offset="0.55" stop-color="{SKY_MID}"/>'
               f'<stop offset="0.92" stop-color="{SKY_LOW}"/><stop offset="1" stop-color="{GLOW}"/></linearGradient>')
    out.append(f'<radialGradient id="glow" cx="{fmt(CX / W)}" cy="{fmt(HORIZON / H)}" r="0.55" '
               f'gradientTransform="translate({fmt(CX / W)} {fmt(HORIZON / H)}) scale(1 0.35) translate({fmt(-CX / W)} {fmt(-HORIZON / H)})">'
               f'<stop offset="0" stop-color="{GLOW}" stop-opacity="0.9"/><stop offset="1" stop-color="{GLOW}" stop-opacity="0"/></radialGradient>')
    out.append(f'<linearGradient id="haze" x1="0" y1="{fmt(HORIZON)}" x2="0" y2="{fmt(HORIZON + 0.22 * H)}" gradientUnits="userSpaceOnUse">'
               f'<stop offset="0" stop-color="{FLOOR_FAR}" stop-opacity="1"/><stop offset="1" stop-color="{FLOOR_FAR}" stop-opacity="0"/></linearGradient>')
    out.append(f'<linearGradient id="floorshade" x1="0" y1="{fmt(HORIZON)}" x2="0" y2="{H}" gradientUnits="userSpaceOnUse">'
               f'<stop offset="0" stop-color="#000" stop-opacity="0"/><stop offset="1" stop-color="#10122e" stop-opacity="0.45"/></linearGradient>')
    # Chrome: sky above the equator, a hard horizon, the dark floor below.
    out.append('<linearGradient id="chrome" x1="0" y1="0" x2="0" y2="1">'
               '<stop offset="0" stop-color="#f4f4ff"/><stop offset="0.3" stop-color="#b9bce6"/>'
               '<stop offset="0.52" stop-color="#6c72b8"/><stop offset="0.53" stop-color="#1c1f48"/>'
               '<stop offset="0.75" stop-color="#3d4290"/><stop offset="1" stop-color="#9aa0d6"/></linearGradient>')
    out.append('<radialGradient id="peri" cx="0.36" cy="0.32" r="0.75">'
               '<stop offset="0" stop-color="#dcdcfa"/><stop offset="0.35" stop-color="#9aa0d6"/>'
               '<stop offset="0.75" stop-color="#5a60a8"/><stop offset="1" stop-color="#262a5e"/></radialGradient>')
    out.append('<radialGradient id="plat" cx="0.36" cy="0.32" r="0.78">'
               '<stop offset="0" stop-color="#ffffff"/><stop offset="0.4" stop-color="#dddddd"/>'
               '<stop offset="0.8" stop-color="#9a9a9a"/><stop offset="1" stop-color="#555555"/></radialGradient>')
    out.append('<radialGradient id="rim" cx="0.5" cy="0.5" r="0.5">'
               '<stop offset="0.72" stop-color="#000" stop-opacity="0"/><stop offset="1" stop-color="#0c0e2a" stop-opacity="0.55"/></radialGradient>')
    out.append('<filter id="soft" x="-50%" y="-200%" width="200%" height="500%"><feGaussianBlur stdDeviation="14"/></filter>')
    out.append('<filter id="glint" x="-50%" y="-50%" width="200%" height="200%"><feGaussianBlur stdDeviation="6"/></filter>')
    # Classic 1-bit Mac desktop texture: a 50% dither, very faint, 3px pixels.
    out.append('<pattern id="dither" width="6" height="6" patternUnits="userSpaceOnUse">'
               '<rect width="3" height="3" fill="#fff"/><rect x="3" y="3" width="3" height="3" fill="#fff"/></pattern>')
    out.append('</defs>')

    # Sky, horizon glow, dither.
    out.append(f'<rect width="{W}" height="{fmt(HORIZON)}" fill="url(#sky)"/>')
    out.append(f'<rect width="{W}" height="{fmt(HORIZON)}" fill="url(#dither)" opacity="0.045"/>')
    out.append(f'<rect width="{W}" height="{H}" fill="url(#glow)"/>')

    # Floor: base, checker, haze into the horizon, darker toward the viewer.
    out.append(f'<rect y="{fmt(HORIZON)}" width="{W}" height="{fmt(H - HORIZON)}" fill="{FLOOR_A}"/>')
    out.append(f'<path d="{checker_floor()}" fill="{FLOOR_B}"/>')
    out.append(f'<rect y="{fmt(HORIZON)}" width="{W}" height="{fmt(0.22 * H)}" fill="url(#haze)"/>')
    out.append(f'<rect y="{fmt(HORIZON)}" width="{W}" height="{fmt(H - HORIZON)}" fill="url(#floorshade)"/>')
    out.append(f'<rect y="{fmt(HORIZON - 1)}" width="{W}" height="2" fill="#ffffff" opacity="0.5"/>')

    # The spheres: far to near, so nearer ones overlap.
    out.append(sphere(CX + 0.20 * W, HORIZON + 0.055 * H, 0.055 * H, 'plat'))
    out.append(sphere(CX - 0.17 * W, HORIZON + 0.115 * H, 0.105 * H, 'peri'))
    out.append(sphere(CX + 0.02 * W, HORIZON + 0.13 * H, 0.165 * H, 'chrome'))

    out.append('</svg>')
    print('\n'.join(out))


if __name__ == '__main__':
    main()
