#!/usr/bin/env python3
"""Generate the Lisbon pack wallpaper: a wall of azulejo tiles.

Procedural, not traced: a 2x2-repeat "padrão" in cobalt on tin-glazed
white, the way the 17th-c. Lisbon pattern tiles read. Three motifs sit on
the tile grid and only make sense across tiles, which is what makes a wall
of them look like azulejo rather than a printed pattern:

  - a rosette at each tile's centre (eight petals, ochre heart);
  - a medallion on every grid intersection — each of the four tiles that
    meet there carries a quarter of it;
  - a lozenge on every edge midpoint, half in each neighbour.

Each tile also gets its own glaze tint and blue strength, seeded so the
output is stable, which is the hand-made unevenness that stops a repeat
from looking like wallpaper. Thin grout lines finish the grid.

    python3 design/packs/gen-lisbon.py > web/shell/public/wallpapers/lisbon.svg
"""

import math
import random

W, H = 1920, 1080
T = 192  # tile size — ten across a 1920 screen
COBALT = "#1f4fa3"
COBALT_LIGHT = "#5f86c8"
OCHRE = "#d19a2a"
GLAZE = (247, 244, 236)
GROUT = "#d8d2c4"

rnd = random.Random(1755)  # the year of the earthquake; any fixed seed will do


def f(x: float) -> str:
    return f"{x:.1f}".rstrip("0").rstrip(".")


def petal_path(r_in: float, r_out: float, half_w: float) -> str:
    """A pointed petal along +y, from r_in to r_out, widest at the middle."""
    mid = (r_in + r_out) / 2
    return (
        f"M0 {f(r_in)} Q{f(half_w)} {f(mid)} 0 {f(r_out)} "
        f"Q{f(-half_w)} {f(mid)} 0 {f(r_in)}Z"
    )


def defs() -> str:
    s = T / 160  # motifs are drawn for a 160px tile
    petal = petal_path(9 * s, 46 * s, 13 * s)
    petal_inner = petal_path(14 * s, 36 * s, 6 * s)
    rosette = [f'<circle r="{f(52 * s)}" fill="none" stroke="{COBALT}" stroke-width="{f(2.5 * s)}"/>']
    for i in range(8):
        a = i * 45
        rosette.append(f'<path d="{petal}" fill="{COBALT}" transform="rotate({a})"/>')
        rosette.append(f'<path d="{petal_inner}" fill="{COBALT_LIGHT}" transform="rotate({a})"/>')
    rosette.append(f'<circle r="{f(9 * s)}" fill="{OCHRE}"/>')
    rosette.append(f'<circle r="{f(4 * s)}" fill="{COBALT}"/>')

    # Medallion on intersections: solid cobalt disc, white ring, a
    # four-leaf cross pointing along the grout lines.
    med = [
        f'<circle r="{f(40 * s)}" fill="{COBALT}"/>',
        f'<circle r="{f(31 * s)}" fill="none" stroke="#fbfaf6" stroke-width="{f(3 * s)}"/>',
    ]
    leaf = petal_path(4 * s, 26 * s, 9 * s)
    for a in (45, 135, 225, 315):
        med.append(f'<path d="{leaf}" fill="#fbfaf6" transform="rotate({a})"/>')
    med.append(f'<circle r="{f(5 * s)}" fill="{OCHRE}"/>')

    # Lozenge on edge midpoints (drawn for a vertical grout line; rotated
    # 90° for horizontal ones): a cobalt diamond with a light core and two
    # tendrils curling toward the rosettes.
    d = 22 * s
    loz = [
        f'<path d="M0 {f(-d)} L{f(d * 0.62)} 0 L0 {f(d)} L{f(-d * 0.62)} 0Z" fill="{COBALT}"/>',
        f'<path d="M0 {f(-d * 0.5)} L{f(d * 0.3)} 0 L0 {f(d * 0.5)} L{f(-d * 0.3)} 0Z" fill="{COBALT_LIGHT}"/>',
    ]
    for sx in (1, -1):
        loz.append(
            f'<path d="M{f(sx * 14 * s)} 0 C{f(sx * 22 * s)} {f(-12 * s)} {f(sx * 30 * s)} {f(-4 * s)} '
            f'{f(sx * 30 * s)} {f(4 * s)}" fill="none" stroke="{COBALT}" stroke-width="{f(2.5 * s)}" '
            f'stroke-linecap="round"/>'
        )

    return (
        "<defs>"
        f'<g id="r">{"".join(rosette)}</g>'
        f'<g id="m">{"".join(med)}</g>'
        f'<g id="l">{"".join(loz)}</g>'
        "</defs>"
    )


def main() -> None:
    cols = math.ceil(W / T) + 1
    rows = math.ceil(H / T) + 1
    # Centre the grid so partial tiles are even on both sides.
    ox = (W - (cols - 1) * T) / 2 - T / 2
    oy = (H - (rows - 1) * T) / 2 - T / 2

    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" '
        f'width="{W}" height="{H}" viewBox="0 0 {W} {H}" preserveAspectRatio="xMidYMid slice">',
        defs(),
        f'<rect width="{W}" height="{H}" fill="{GROUT}"/>',
    ]
    # Glaze: one tint per tile, inset by the grout width.
    for r in range(rows):
        for c in range(cols):
            x, y = ox + c * T, oy + r * T
            k = rnd.uniform(-7, 4)
            warm = rnd.uniform(-2, 2)
            rgb = (
                round(GLAZE[0] + k + warm),
                round(GLAZE[1] + k),
                round(GLAZE[2] + k - warm),
            )
            out.append(
                f'<rect x="{f(x + 1)}" y="{f(y + 1)}" width="{T - 2}" height="{T - 2}" '
                f'fill="rgb({rgb[0]},{rgb[1]},{rgb[2]})"/>'
            )
    # Motifs. Blue strength varies per tile like an uneven firing.
    for r in range(rows):
        for c in range(cols):
            x, y = ox + c * T, oy + r * T
            op = rnd.uniform(0.82, 1.0)
            out.append(f'<use xlink:href="#r" x="{f(x + T / 2)}" y="{f(y + T / 2)}" opacity="{op:.2f}"/>')
    for r in range(rows + 1):
        for c in range(cols + 1):
            x, y = ox + c * T, oy + r * T
            out.append(f'<use xlink:href="#m" x="{f(x)}" y="{f(y)}"/>')
            if r < rows:
                out.append(f'<use xlink:href="#l" x="{f(x)}" y="{f(y + T / 2)}"/>')
            if c < cols:
                out.append(
                    f'<use xlink:href="#l" transform="translate({f(x + T / 2)} {f(y)}) rotate(90)"/>'
                )
    # Grout over the motifs too: a medallion is four painted quarters on
    # four tiles, and the joint runs straight through it.
    for c in range(cols + 1):
        x = f(ox + c * T)
        out.append(f'<path d="M{x} 0V{H}" stroke="{GROUT}" stroke-width="2" opacity="0.8"/>')
    for r in range(rows + 1):
        y = f(oy + r * T)
        out.append(f'<path d="M0 {y}H{W}" stroke="{GROUT}" stroke-width="2" opacity="0.8"/>')
    out.append("</svg>")
    print("\n".join(out))


if __name__ == "__main__":
    main()
