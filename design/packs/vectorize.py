#!/usr/bin/env python3
"""Vectorize a pack's source raster into its wallpaper SVG, keeping the sky smooth.

vtracer is excellent on the clean-edged, cel-shaded subject but flattens a
smooth sky gradient into one or two dull bands. So the sky is split off:

  1. Find the sky: the low-gradient region connected to the top edge
     (Sobel magnitude under a threshold, connected-component labelled)
     whose colors fit a smooth per-row sky model, so flat non-sky shapes
     touching the top edge (a red spire) stay in the trace.
  2. Paint it a key color, trace the whole image with vtracer.
  3. Every traced path whose fill is the key color is re-filled with an
     SVG linearGradient running along the sky's own direction of change
     (often not straight down), stops sampled from the original, so the sky
     is a real gradient, not posterized.

    python3 design/packs/vectorize.py design/packs/src/lisbon-source.png \\
        web/shell/public/wallpapers/lisbon.svg

For a flat poster in a known set of inks, pass them and skip the sky
handling: every pixel snaps to the nearest ink and a 3x3 mode filter clears
the anti-aliased fringe, so vtracer sees exact flat regions and traces them
losslessly.

    python3 design/packs/vectorize.py SRC DST --palette '#13285a,#0b1838,...'

Run under a Python with vtracer, numpy, scipy and Pillow (e.g. ~/.venvs/web).
"""

import re
import sys

import numpy as np
import vtracer
from PIL import Image, ImageFilter
from scipy import ndimage

W = 1920
KEY = (255, 0, 255)
STOPS = 14


def _connected_to_top(sel: np.ndarray) -> np.ndarray:
    lab, _ = ndimage.label(sel)
    top = set(np.unique(lab[0])) - {0}
    return np.isin(lab, list(top))


def _sky_model(rgb: np.ndarray, sel: np.ndarray, band: int = 10) -> np.ndarray:
    """Model the sky as a per-row line across x (color = a(y) + b(y)*x),
    fitted over `sel` in a band of rows and smoothed down the frame. A dusk
    sky is far from linear top to bottom, but within a row it changes
    smoothly (lighter toward the sun side), which is all this assumes.
    Rows with too little sky borrow the nearest fitted row."""
    h, w = sel.shape
    a = np.full((h, 3), np.nan)
    b = np.zeros((h, 3))
    for y in range(0, h, 4):
        ys, xs = np.nonzero(sel[max(0, y - band) : y + band + 1])
        if len(xs) < 200:
            continue
        A = np.stack([np.ones_like(xs), xs], axis=1).astype(np.float64)
        px = rgb[max(0, y - band) + ys, xs].astype(np.float64)
        coef, *_ = np.linalg.lstsq(A, px, rcond=None)
        a[y : y + 4], b[y : y + 4] = coef[0], coef[1]
    good = ~np.isnan(a[:, 0])
    if not good.any():
        return np.full(rgb.shape, np.nan, dtype=np.float32)
    idx = np.where(good, np.arange(h), 0)
    np.maximum.accumulate(idx, out=idx)  # fill down from the last fitted row
    first = int(np.argmax(good))
    idx[:first] = first  # and up from the first
    a, b = a[idx], b[idx]
    a = ndimage.gaussian_filter1d(a, 6, axis=0)
    b = ndimage.gaussian_filter1d(b, 6, axis=0)
    xx = np.arange(w, dtype=np.float32)
    return (a[:, None, :] + b[:, None, :] * xx[None, :, None]).astype(np.float32)


def sky_mask(rgb: np.ndarray, thresh: float = 9.0) -> np.ndarray:
    f = rgb.astype(np.float32)
    lum = ndimage.gaussian_filter(f.mean(axis=2), 1.0)
    mag = np.hypot(ndimage.sobel(lum, 0), ndimage.sobel(lum, 1)) / 8
    smooth = mag < thresh
    # Smooth AND sky-colored: a flat red spire touching the top edge is as
    # smooth as the sky. Model the sky over the smooth top-connected region,
    # drop what is far from the model (the spire), refit on the rest, and
    # keep only pixels near it.
    sel = _connected_to_top(smooth)
    for tol in (140, 50):
        model = _sky_model(f, sel)
        sel = _connected_to_top(smooth & (np.abs(f - model).sum(axis=2) < tol))
    # Close pinholes (stars, snowflakes stay as traced shapes on top), then
    # grow a little so the subject's anti-aliased fringe goes to the trace.
    mask = ndimage.binary_closing(sel, iterations=2)
    return ndimage.binary_dilation(mask, iterations=2)


def gradient(rgb: np.ndarray, mask: np.ndarray) -> str:
    """A linearGradient along the sky's own direction of change (the
    luminance plane's slope), with stops at the sky's median color in
    bands across that direction."""
    f = rgb.astype(np.float32)
    ys, xs = np.nonzero(mask)
    lum = f[ys, xs].mean(axis=1)
    A = np.stack([np.ones_like(xs), xs, ys], axis=1).astype(np.float64)
    (_, bx, by), *_ = np.linalg.lstsq(A, lum, rcond=None)
    n = np.hypot(bx, by) or 1.0
    dx, dy = bx / n, by / n
    t = xs * dx + ys * dy
    t0, t1 = float(t.min()), float(t.max())
    stops = []
    for i in range(STOPS + 1):
        c = t0 + (t1 - t0) * i / STOPS
        sel = np.abs(t - c) < max(6.0, (t1 - t0) / STOPS / 2)
        if not sel.any():
            continue
        r, g, b = (int(v) for v in np.median(f[ys[sel], xs[sel]], axis=0))
        stops.append(f'<stop offset="{i / STOPS:.3f}" stop-color="#{r:02x}{g:02x}{b:02x}"/>')
    return (
        f'<linearGradient id="sky" gradientUnits="userSpaceOnUse" '
        f'x1="{dx * t0:.1f}" y1="{dy * t0:.1f}" x2="{dx * t1:.1f}" y2="{dy * t1:.1f}">'
        + "".join(stops)
        + "</linearGradient>"
    )


def is_key(hexcolor: str) -> bool:
    r, g, b = (int(hexcolor[i : i + 2], 16) for i in (1, 3, 5))
    return abs(r - KEY[0]) + abs(g - KEY[1]) + abs(b - KEY[2]) < 60


def snap(im: Image.Image, palette: list[str]) -> Image.Image:
    """Snap every pixel to the nearest ink, then mode-filter the fringe."""
    inks = np.array([[int(c[i : i + 2], 16) for i in (1, 3, 5)] for c in palette], dtype=np.float32)
    rgb = np.asarray(im, dtype=np.float32)
    idx = np.argmin(((rgb[:, :, None, :] - inks[None, None]) ** 2).sum(axis=3), axis=2).astype(np.uint8)
    p = Image.fromarray(idx, "P")
    p.putpalette(inks.astype(np.uint8).flatten().tolist())
    return p.filter(ImageFilter.ModeFilter(3)).convert("RGB")


def trace_flat(im: Image.Image, palette: list[str], dst: str) -> None:
    tmp_in, tmp_out = "/tmp/vectorize-in.png", "/tmp/vectorize-out.svg"
    snap(im, palette).save(tmp_in)
    vtracer.convert_image_to_svg_py(
        tmp_in, tmp_out, colormode="color", hierarchical="stacked", mode="spline",
        filter_speckle=4, color_precision=8, layer_difference=1, corner_threshold=60,
        length_threshold=4.0, max_iterations=10, splice_threshold=45, path_precision=2,
    )
    svg = open(tmp_out).read()
    svg = re.sub(r"<\?xml[^>]*\?>\s*|<!--[^>]*-->\s*", "", svg)
    svg = re.sub(
        r"<svg [^>]*>",
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{im.width}" height="{im.height}" '
        f'viewBox="0 0 {im.width} {im.height}" preserveAspectRatio="xMidYMid slice">',
        svg, count=1,
    )
    svg = svg.replace(' transform="translate(0,0)"', "")
    open(dst, "w").write(svg)
    print(f"{dst}: {len(svg) // 1024} KB, {len(palette)} inks")


def main() -> None:
    args = sys.argv[1:]
    palette = None
    if "--palette" in args:
        i = args.index("--palette")
        palette = [c.strip() for c in args[i + 1].split(",")]
        del args[i : i + 2]
    src, dst = args
    im = Image.open(src).convert("RGB")
    im = im.resize((W, round(im.height * W / im.width)), Image.LANCZOS)
    if palette:
        trace_flat(im, palette, dst)
        return
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
