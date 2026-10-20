#!/usr/bin/env python3
"""Vectorize a pack's source raster into its wallpaper SVG, keeping the sky smooth.

vtracer is excellent on the clean-edged, cel-shaded subject but flattens a
smooth sky gradient into one or two dull bands. So the sky is split off:

  1. Find the sky: the low-gradient region connected to the top edge
     (Sobel magnitude under a threshold, connected-component labelled).
  2. Paint it a key color, trace the whole image with vtracer.
  3. Every traced path whose fill is the key color is re-filled with an
     SVG linearGradient whose stops are the original sky's mean color per
     row band, so the sky is a real gradient, not posterized.

    python3 design/packs/vectorize.py design/packs/src/isfahan-2.png \\
        web/shell/public/wallpapers/isfahan.svg

Run under a Python with vtracer, numpy, scipy and Pillow (e.g. ~/.venvs/web).
"""

import re
import sys

import numpy as np
import vtracer
from PIL import Image
from scipy import ndimage

W = 1920
KEY = (255, 0, 255)
STOPS = 14


def sky_mask(rgb: np.ndarray, thresh: float = 9.0) -> np.ndarray:
    lum = rgb.astype(np.float32).mean(axis=2)
    lum = ndimage.gaussian_filter(lum, 1.0)
    mag = np.hypot(ndimage.sobel(lum, 0), ndimage.sobel(lum, 1)) / 8
    # Smooth AND sky-colored: a flat red spire touching the top edge is as
    # smooth as the sky, so also require each pixel to be near the sky's
    # color in its own row (the median of the frame's left edge, where the
    # prompts keep negative space).
    ref = np.median(rgb[:, :40].astype(np.float32), axis=1)[:, None, :]
    near = np.abs(rgb.astype(np.float32) - ref).sum(axis=2) < 60
    smooth = (mag < thresh) & near
    lab, _ = ndimage.label(smooth)
    top = set(np.unique(lab[0])) - {0}
    mask = np.isin(lab, list(top))
    # Close pinholes (stars, snowflakes stay as traced shapes on top), then
    # grow a little so the subject's anti-aliased fringe goes to the trace.
    mask = ndimage.binary_closing(mask, iterations=2)
    return ndimage.binary_dilation(mask, iterations=2)


def gradient(rgb: np.ndarray, mask: np.ndarray) -> str:
    h = rgb.shape[0]
    rows = np.where(mask.any(axis=1))[0]
    lo, hi = int(rows.min()), int(rows.max())
    stops = []
    for i in range(STOPS + 1):
        y = lo + (hi - lo) * i / STOPS
        band = slice(max(lo, int(y) - 6), min(hi + 1, int(y) + 7))
        sel = mask[band]
        px = rgb[band][sel]
        if len(px) == 0:
            continue
        r, g, b = (int(v) for v in np.median(px, axis=0))
        stops.append(f'<stop offset="{y / h:.3f}" stop-color="#{r:02x}{g:02x}{b:02x}"/>')
    return (
        f'<linearGradient id="sky" gradientUnits="userSpaceOnUse" x1="0" y1="0" x2="0" y2="{h}">'
        + "".join(stops)
        + "</linearGradient>"
    )


def is_key(hexcolor: str) -> bool:
    r, g, b = (int(hexcolor[i : i + 2], 16) for i in (1, 3, 5))
    return abs(r - KEY[0]) + abs(g - KEY[1]) + abs(b - KEY[2]) < 60


def main() -> None:
    src, dst = sys.argv[1], sys.argv[2]
    im = Image.open(src).convert("RGB")
    im = im.resize((W, round(im.height * W / im.width)), Image.LANCZOS)
    rgb = np.asarray(im)
    mask = sky_mask(rgb)
    keyed = rgb.copy()
    keyed[mask] = KEY
    tmp_in, tmp_out = "/tmp/vectorize-in.png", "/tmp/vectorize-out.svg"
    Image.fromarray(keyed).save(tmp_in)
    vtracer.convert_image_to_svg_py(
        tmp_in, tmp_out, colormode="color", hierarchical="stacked", mode="spline",
        filter_speckle=6, color_precision=6, layer_difference=12, corner_threshold=60,
        length_threshold=4.0, max_iterations=10, splice_threshold=45, path_precision=2,
    )
    svg = open(tmp_out).read()
    svg = re.sub(r'fill="(#[0-9A-Fa-f]{6})"', lambda m: 'fill="url(#sky)"' if is_key(m.group(1)) else m.group(0), svg)
    svg = re.sub(r"<\?xml[^>]*\?>\s*|<!--[^>]*-->\s*", "", svg)
    h = rgb.shape[0]
    svg = re.sub(
        r"<svg [^>]*>",
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{h}" viewBox="0 0 {W} {h}" '
        f'preserveAspectRatio="xMidYMid slice"><defs>{gradient(rgb, mask)}</defs>',
        svg, count=1,
    )
    svg = svg.replace(' transform="translate(0,0)"', "")
    open(dst, "w").write(svg)
    print(f"{dst}: {len(svg) // 1024} KB, sky {mask.mean():.0%} of frame")


if __name__ == "__main__":
    main()
