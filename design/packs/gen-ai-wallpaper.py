#!/usr/bin/env python3
"""Generate a pack wallpaper's source raster with an image model.

The raster is the SOURCE, not the shipped wallpaper, and it is drawn to
survive `vtracer`. Two styles:

  - "flat": a screen-print poster in a handful of exact inks on one flat
    field, with Tokyo's source passed as a style reference. Traces
    losslessly (vectorize.py --palette snaps it to those inks first).
    Prefer this: anything that tries to look really 3D shows its tracing.
  - "3d": a cel-shaded miniature-diorama look. Fine for simple, chunky
    subjects (Lisbon), too lossy for intricate ones.

The prompts live here so a wallpaper can be regenerated or iterated.

    python3 design/packs/gen-ai-wallpaper.py isfahan [--n 2] [--model M]
      -> design/packs/src/<pack>-<i>.png  (the pick is kept as <pack>-source.png)

Needs an OpenRouter key in ~/openrouter.token (never committed).
"""

import argparse
import base64
import json
import pathlib
import urllib.request

STYLE_3D = (
    "Contemporary stylized 3D illustration for a 16:9 desktop wallpaper. "
    "Soft clay-render / cel-shaded look: smooth rounded forms, gentle volumetric lighting, "
    "a limited harmonious palette, and CRISP clean edges between flat bands of tone. "
    "Absolutely no texture, no grain, no noise, no gradients dithered into speckle, no text, "
    "no logos, no watermark, no people. Calm negative space in the upper-left corner. "
    "Premium, modern, polished, like a high-end OS wallpaper."
)

STYLE_FLAT = (
    "Flat graphic screen-print poster for a 16:9 desktop wallpaper (if a reference image is "
    "attached, take ONLY its style; do not copy, include or collage any of its content). "
    "ONE flat background color covering most of the frame, the "
    "subject built from bold flat shapes in a few solid inks, abstract and graphic rather than "
    "realistic. Strictly flat color: no gradients, no shading, no lighting effects, no "
    "texture, no grain, no outlines in extra colors, no anti-aliasing tricks, no text, no "
    "logos, no people. Use ONLY the listed colors. Generous empty background, calm upper-left."
)

STYLE_RASTER = (
    "A striking contemporary abstract art print, made to be a premium ultra-wide desktop "
    "wallpaper shipped as a high-resolution raster (it is not traced, so rich texture, "
    "gradients and painterly or printmaking marks are welcome). Panoramic composition: keep "
    "the important content in the central horizontal band so it survives cropping to both "
    "16:9 and 32:9, and leave calmer space toward the left edge. Gallery quality, bold and "
    "confident, abstract and stylized rather than photographic. No text, no lettering, no "
    "logos, no watermark, no people, no frame or border."
)

# The reference image for "flat" packs: Tokyo's source, whose style traces perfectly.
FLAT_REF = pathlib.Path(__file__).resolve().parent / "src" / "solarized-dark-source.png"

PROMPTS = {
    "isfahan": ("flat",
        "Subject: the Shah Mosque in Isfahan at night, drawn as a highly TECHNICAL, intricate "
        "architectural screen print: precise and dense, like an engraved elevation drawing "
        "printed in flat inks. The great iwan portal fills much of the right two-thirds: its "
        "pointed arch packed with dozens of stepped muqarnas cells drawn as crisp geometric "
        "facets; every panel around it covered in exact girih star-and-polygon tilework and "
        "interlaced arabesque scrolls; a band of thuluth-style calligraphic frieze (abstract, "
        "illegible); the dome behind with a fine spiral of arabesque medallions; two minarets "
        "with balconies of tiny lattice detail; rows of arched alcoves with their own tile "
        "panels. Fine line work and small repeated detail everywhere on the building, while the "
        "sky stays one flat field with a crescent moon and a few stars. Below, the reflecting "
        "pool mirrors it in broken horizontal strokes. "
        "Exactly six flat colors: background deep lapis #13285a; dark navy #0b1838; turquoise "
        "#2bb3b9; light turquoise #7fd6d4; gold #d9a93f; ivory #efe6d2."
    ),
    "moscow": ("flat",
        "Subject: St Basil's Cathedral at night in snow, drawn as a highly TECHNICAL, intricate "
        "architectural screen print: precise and dense, like an engraved elevation drawing "
        "printed in flat inks. The full cathedral on the right two-thirds: the central tent "
        "spire faceted with a fine diamond lattice; eight onion domes each covered in exact "
        "repeated patterns (twisted stripes, chevrons, scales, faceted pineapple knobs) drawn "
        "with crisp small shapes; tiers of kokoshnik gables stacked up every tower; brick "
        "courses, window surrounds, columns, cornices and the galleried base all drawn with "
        "fine, precise detail; gold crosses with chains. Fine line work and small repeated "
        "detail everywhere on the building, while the sky stays one flat field with a scatter "
        "of round snowflakes, and flat snow drifts along the bottom. "
        "Exactly seven flat colors: background deep night blue #1d2f63; dark navy #121c3e; "
        "brick red #c0392b; emerald #2f8a4a; gold #e0aa2a; cobalt #3b78d8; snow white #f4efe6."
    ),
    # Raster concepts (ship as WebP, not traced).
    "isfahan-geometry": ("raster",
        "Subject: Isfahan, abstracted. The turquoise-and-lapis dome and pointed iwan of the "
        "Shah Mosque dissolve into a sweeping field of Persian geometry: girih star tessellations, "
        "muqarnas facets and arabesque spirals that fragment and recombine across the panorama "
        "like a modernist collage, glazed tile textures, gold leaf accents, deep night lapis and "
        "a glowing turquoise core."
    ),
    "isfahan-bridge": ("raster",
        "Subject: Isfahan at dusk, abstracted. The long double-tiered arcade of the Si-o-se-pol "
        "bridge stretches across the panorama, its arches reflected and fractured in the "
        "Zayanderud river into rhythmic geometric bands; a turquoise dome and minarets rise "
        "softly beyond; sky in layered bands of saffron, rose and lapis. Painterly, textured, "
        "geometric, warm light against deep blue."
    ),
    "moscow-constructivist": ("raster",
        "Subject: Moscow through Russian Constructivism and Suprematism (El Lissitzky, Rodchenko, "
        "Malevich): bold red wedges, black and cream planes, dynamic diagonals and circles, "
        "interlocking with the colorful patterned onion domes of St Basil's Cathedral, a Kremlin "
        "tower star, and falling snow, as a graphic avant-garde poster with screen-print "
        "texture. Palette of brick red, black, cream, gold, emerald and cobalt."
    ),
    # Recreates a picked moscow-constructivist candidate (passed with --ref)
    # full-bleed: the first pass came out as a print mounted on a wall.
    "moscow-soviet": ("raster",
        "Recreate the attached artwork as the full image, edge to edge: the artwork itself must "
        "fill the ENTIRE frame with no paper card, mat, mount, border, wall, shadow or margin "
        "around it. Keep its composition and Constructivist/Suprematist style: dynamic red and "
        "black wedges and diagonals, circles, Kremlin towers with red stars, and the patterned "
        "onion domes of St Basil's Cathedral on the right. Remove all snow and snowflakes. "
        "Shift the palette toward classic Soviet poster colors: deep Soviet red #c8102e, black "
        "#151515, warm cream paper #efe4cc, and gold #d4a017 as the dominant colors, with the "
        "domes' greens and blues kept but slightly muted. Screen-print texture."
    ),
    "moscow-domes": ("raster",
        "Subject: Moscow in winter, abstracted. The onion domes of St Basil's Cathedral — "
        "twisted stripes, zigzags and scales in red, emerald, gold and cobalt — rendered as a "
        "rich, almost folk-art abstraction with expressive brush and gouache texture, stacked "
        "and overlapping across the panorama against a deep blue snowy night with soft "
        "falling snow and warm lit windows."
    ),
    "lisbon": ("3d",
        "Subject: Lisbon seen from the water, a wide panoramic view straight across from the "
        "river. Strict horizontal bands: the bottom sixth of the frame is calm deep-blue water "
        "with a few clean stylized ripple highlights; above it a dense hillside city of "
        "whitewashed houses with terracotta-red tiled roofs stacked up the hills, small windows, "
        "a few ochre and pale-blue facades, a castle wall and a church dome on the crest; the "
        "upper half is a clear soft blue daytime sky fading lighter toward the horizon, with "
        "one or two small simple clouds and lots of calm empty sky. "
        "Rendered like a modern 3D miniature diorama: chunky rounded buildings, soft sunlight "
        "from the left, gentle shadows. "
        "Palette: whitewash #f8f7f3, terracotta #c8643b, ochre #c9921f, cobalt #1f4fa3, "
        "sky blue #8ec3ea."
    ),
}

MODEL = "google/gemini-3-pro-image"
# 4K output is only offered on the preview build of the pro model.
MODEL_4K = "google/gemini-3-pro-image-preview"
OUT = pathlib.Path(__file__).resolve().parent / "src"


def _ref_jpeg(path: pathlib.Path) -> str:
    """The reference, downscaled: the model needs its style, not 8K pixels."""
    import io

    from PIL import Image

    im = Image.open(path).convert("RGB")
    im.thumbnail((2048, 2048))
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=88)
    return "data:image/jpeg;base64," + base64.b64encode(buf.getvalue()).decode()


def generate(style: str, prompt: str, model: str, ref: bool = True,
             aspect: str = "16:9", size: str = "2K", ref_path: pathlib.Path | None = None) -> bytes:
    key = (pathlib.Path.home() / "openrouter.token").read_text().strip()
    text = f"{ {'flat': STYLE_FLAT, 'raster': STYLE_RASTER}.get(style, STYLE_3D)}\n\n{prompt}"
    content: list = [{"type": "text", "text": text}]
    if ref_path:
        content.append({"type": "image_url", "image_url": {"url": _ref_jpeg(ref_path)}})
    elif style == "flat" and ref:
        content.append({"type": "image_url", "image_url": {"url": _ref_jpeg(FLAT_REF)}})
    body = {
        "model": model,
        "modalities": ["image", "text"],
        "image_config": {"aspect_ratio": aspect, "image_size": size},
        "messages": [{"role": "user", "content": content}],
    }
    req = urllib.request.Request(
        "https://openrouter.ai/api/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=300) as r:
        d = json.load(r)
    imgs = d["choices"][0]["message"].get("images") or []
    if not imgs:
        raise RuntimeError(f"no image: {str(d['choices'][0]['message'].get('content'))[:300]}")
    return base64.b64decode(imgs[0]["image_url"]["url"].split(",", 1)[1])


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("pack", choices=sorted(PROMPTS))
    ap.add_argument("--n", type=int, default=1)
    ap.add_argument("--model", default=None, help=f"default {MODEL}, or {MODEL_4K} at 4K")
    # The model sometimes collages the reference's own content into the
    # picture (Moscow did, every time); a detailed prompt carries the style alone.
    # Raster wallpapers: panoramic and big enough for an ultrawide (cover-cropped).
    ap.add_argument("--aspect", default=None, help="default 21:9 for raster styles, else 16:9")
    ap.add_argument("--size", default=None, help="default 4K for raster styles, else 2K")
    ap.add_argument("--ref", type=pathlib.Path, help="an image to work from (e.g. a candidate to recreate)")
    ap.add_argument("--no-ref", action="store_true", help="flat style without the Tokyo reference image")
    a = ap.parse_args()
    OUT.mkdir(exist_ok=True)
    for i in range(a.n):
        p = OUT / f"{a.pack}-{i + 1}.png"
        style, prompt = PROMPTS[a.pack]
        raster = style == "raster"
        size = a.size or ("4K" if raster else "2K")
        model = a.model or (MODEL_4K if size == "4K" else MODEL)
        p.write_bytes(generate(style, prompt, model, ref=not a.no_ref, ref_path=a.ref,
                               aspect=a.aspect or ("21:9" if raster else "16:9"), size=size))
        print(p)


if __name__ == "__main__":
    main()
