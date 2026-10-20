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
    "Flat graphic screen-print poster for a 16:9 desktop wallpaper, in the style of the "
    "attached reference image. Take ONLY its style; do not copy, include or collage any of "
    "its content (no buildings, signs or windows from it). ONE flat background color covering most of the frame, the "
    "subject built from bold flat shapes in a few solid inks, abstract and graphic rather than "
    "realistic. Strictly flat color: no gradients, no shading, no lighting effects, no "
    "texture, no grain, no outlines in extra colors, no anti-aliasing tricks, no text, no "
    "logos, no people. Use ONLY the listed colors. Generous empty background, calm upper-left."
)

# The reference image for "flat" packs: Tokyo's source, whose style traces perfectly.
FLAT_REF = pathlib.Path(__file__).resolve().parent / "src" / "solarized-dark-source.png"

PROMPTS = {
    "isfahan": ("flat",
        "Subject: the Shah Mosque in Isfahan at night as a poster. The great dome and a tall "
        "pointed iwan arch with two slender minarets, as bold flat silhouettes; the dome and "
        "iwan carry simple flat eight-pointed star and arabesque motifs; a thin crescent moon "
        "and a few small stars; a long reflecting pool below rendered as broken horizontal "
        "reflection stripes, like the puddle reflections in the reference. "
        "Exactly five colors: background deep lapis #13285a; dark navy #0b1838; turquoise "
        "#2bb3b9; gold #d9a93f; ivory #efe6d2."
    ),
    "moscow": ("flat",
        "Subject: St Basil's Cathedral at night in snow, as a poster. A cluster of onion domes "
        "on towers as bold flat silhouettes, each dome with a simple flat pattern (diagonal "
        "stripes, zigzags, scales) in the listed inks; gold crosses as simple flat shapes; a few "
        "lit windows; snow as flat white drifts along the bottom and a scatter of round flakes. "
        "Exactly six colors: background deep night blue #1d2f63; brick red #c0392b; emerald "
        "#2f8a4a; gold #e0aa2a; cobalt #3b78d8; snow white #f4efe6."
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
OUT = pathlib.Path(__file__).resolve().parent / "src"


def _ref_jpeg(path: pathlib.Path) -> str:
    """The reference, downscaled: the model needs its style, not 8K pixels."""
    import io

    from PIL import Image

    im = Image.open(path).convert("RGB")
    im.thumbnail((1280, 1280))
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=88)
    return "data:image/jpeg;base64," + base64.b64encode(buf.getvalue()).decode()


def generate(style: str, prompt: str, model: str) -> bytes:
    key = (pathlib.Path.home() / "openrouter.token").read_text().strip()
    text = f"{STYLE_FLAT if style == 'flat' else STYLE_3D}\n\n{prompt}"
    content: list = [{"type": "text", "text": text}]
    if style == "flat":
        content.append({"type": "image_url", "image_url": {"url": _ref_jpeg(FLAT_REF)}})
    body = {
        "model": model,
        "modalities": ["image", "text"],
        "image_config": {"aspect_ratio": "16:9", "image_size": "2K"},
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
    ap.add_argument("--model", default=MODEL)
    a = ap.parse_args()
    OUT.mkdir(exist_ok=True)
    for i in range(a.n):
        p = OUT / f"{a.pack}-{i + 1}.png"
        p.write_bytes(generate(*PROMPTS[a.pack], a.model))
        print(p)


if __name__ == "__main__":
    main()
