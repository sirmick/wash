#!/usr/bin/env python3
"""Generate a pack wallpaper's source raster with an image model.

The raster is the SOURCE, not the shipped wallpaper: it is drawn in a
stylized, cel-shaded 3D look (crisp edges between flat tone bands, no
texture or grain) precisely so `vtracer` can turn it into a clean SVG.
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

STYLE = (
    "Contemporary stylized 3D illustration for a 16:9 desktop wallpaper. "
    "Soft clay-render / cel-shaded look: smooth rounded forms, gentle volumetric lighting, "
    "a limited harmonious palette, and CRISP clean edges between flat bands of tone. "
    "Absolutely no texture, no grain, no noise, no gradients dithered into speckle, no text, "
    "no logos, no watermark, no people. Calm negative space in the upper-left corner. "
    "Premium, modern, polished, like a high-end OS wallpaper."
)

PROMPTS = {
    "isfahan": (
        "Subject: a Persian mosque interior-exterior scene in the spirit of Isfahan's Shah Mosque. "
        "A grand pointed iwan arch framing a glowing turquoise dome, with layered muqarnas "
        "honeycomb vaulting rendered as clean stepped 3D cells, eight-pointed star tile panels "
        "in turquoise, lapis blue and gold, slender minarets, a reflecting pool in the foreground "
        "mirroring the arch. Deep lapis night-blue sky turning to warm gold near the horizon. "
        "Palette: lapis #13285a, turquoise #1f9aa6, gold #d9a93f, ivory #efe6d2."
    ),
    "moscow": (
        "Subject: a whimsical St Basil's Cathedral style cluster of onion domes, each dome a "
        "different bold pattern (spiral stripes red/green, yellow/green, blue/white, zigzag, "
        "scales), brick-red towers with white arch trim and gold crosses, on a snowy square at "
        "dusk with a few soft snowflakes. Sky from deep blue to warm peach at the horizon. "
        "Rendered like a modern 3D toy diorama: glossy rounded domes, soft shadows. "
        "Palette: brick red #b5372b, emerald #2f8a4a, gold #e0aa2a, cobalt #2456b3, snow #f4efe6."
    ),
}

MODEL = "google/gemini-3-pro-image"
OUT = pathlib.Path(__file__).resolve().parent / "src"


def generate(prompt: str, model: str) -> bytes:
    key = (pathlib.Path.home() / "openrouter.token").read_text().strip()
    body = {
        "model": model,
        "modalities": ["image", "text"],
        "image_config": {"aspect_ratio": "16:9", "image_size": "2K"},
        "messages": [{"role": "user", "content": f"{STYLE}\n\n{prompt}"}],
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
        p.write_bytes(generate(PROMPTS[a.pack], a.model))
        print(p)


if __name__ == "__main__":
    main()
