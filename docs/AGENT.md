# wash-agent (`com.wash.agent`) — AI that operates the desktop

An AI service that doesn't just *talk beside* wash but **drives it**:
opens windows, calls app actions, clicks and types into surfaces that have
no API. This is the concrete form of wash's "AI as a day-1 service" thesis
— not a chat panel, an operator.

Unbuilt — design only. Depends on the display layer (`docs/DISPLAY.md`,
`docs/DISPLAY_ENV.md`) and the background-service tier
(`docs/ARCHITECTURE.md`).

## 0. The core idea — control-surface tiers

A GUI-operating agent's hard infrastructure problems are (1) perceive the
target and (2) inject input into it. wash already solves both for the
human: the per-surface compositor streams frames over WS and synthesizes
pointer/keyboard events into surfaces. Feeding those same two channels
from an *agent* instead of a person is the whole mechanism.

But pixels are the **worst** way to drive anything wash controls. The
design principle is: **always operate a surface at the highest tier it
supports.** wash already knows a window's tier — the router has the
metadata (registered BE gateway? embedded web? raw compositor surface?).

### Tier 1 — native wash apps → router intents (no pixels, no model needed)

`wash-fm`, `washamp`, `settings`, `wash-edit`, etc. already speak
structured intents over the router (the same CBOR/JSON messages the FE
emits). An agent drives them by emitting `fm.upload`, `fm.sort`,
`playback.next` — **not** `click(412, 308)`.

Strictly better on every axis:
- **Deterministic** — no coordinate/DPI fragility, no screenshot→inference
  round-trip in the loop.
- **Already testable** — this is exactly the `e2e` pattern: router-log
  assertions are "did the right intent fire" (`docs/TESTING.md`).
- **No GPU, no VLM** — a small tool-calling model (or none) suffices,
  because the app exposes a clean tool surface. This tier ships inside the
  **hermetic CPU build** with no external model dependency.

Requirement: native apps expose their intents as an **agent tool
manifest** (the BE gateway already defines the verbs; the agent service
reads them as callable tools). The app contract is "expose your intents,"
not "be screenshotted."

### Tier 2 — embedded HTML we don't own → DOM / accessibility (text, no pixels)

Ingress-embedded web apps (`code-server` first — `docs/`/ingress plan) and
arbitrary web content have a **DOM and an accessibility tree**. Drive them
via CDP / Playwright-style, text-anchored ("click the button labeled
Save") — far more robust than coordinate grounding, and the path the
pixel-agent ecosystem itself reaches for (e.g. Midscene.js). Text-in /
text-out, just not *our* API. Still no GPU strictly required.

### Tier 3 — opaque native surfaces → pixel grounding (the VLM tier)

Raw X11/Wayland apps (`xclock`, GIMP, a game) have no DOM and no intent
bus — **only pixels**. This is the genuine fallback, and the only tier
that needs a vision-language GUI agent.

The reference engine here is an open **native GUI-agent VLM** (e.g.
ByteDance **UI-TARS**, Apache-2.0): screenshot in, `Thought:` + `Action:`
out, where actions are coordinate-grounded primitives —
`click(x,y)`, `double_click`, `right_click`, `drag`, `type`, `hotkey`,
`scroll`. Self-hosted behind an OpenAI-compatible endpoint
(vLLM / TensorRT-LLM); a grounding-only mode (coords without reasoning)
exists for cheap locate-an-element calls.

wash is an unusually clean substrate for this — better than a generic
desktop host — because surfaces are **already isolated, already encoded,
already remote**. The agent drives **one window in its own compositor
sandbox** while the human keeps using the rest of the desktop; input is
scoped to the target surface, so a hallucinated click can't escape it.

## 1. The service — `com.wash.agent`

A `surface=background` service (`docs/ARCHITECTURE.md`, the M1–M7 tier):

1. **Tier resolution.** For a target window, ask the router its tier.
2. **Tier 1/2 dispatch.** Resolve the app's tool manifest (intents) or
   attach via CDP; execute structured actions; observe results from the
   event bus / router log. No frame capture.
3. **Tier 3 loop.** Subscribe to the window's **existing** WS frame stream
   (no new capture path) → call the VLM endpoint with frame + task →
   translate the returned `Action:` into compositor input events scoped to
   that surface → re-observe. Surface the `Thought:` trace in a side panel.
4. **Model endpoint** is an **external/optional service**, never bundled —
   bind `0.0.0.0` (LAN), same posture as the VM/proxy servers. Model-,
   vendor- and accelerator-agnostic: anything serving the
   OpenAI-compatible GUI-agent API works.

## 2. Dependencies & the real risk

The model is not the hard part — the **encode/transport loop** is. Tier 3
is screenshot → infer → action → re-screenshot. With a capable accelerator
the inference step is not the bottleneck; the loop is bounded by wash's
frame path, which today has known gaps:

- **No damage tracking** — full frame every commit. The agent loop is the
  forcing function to add damage-tracked encode.
- **`window.resize` not emitted** — the agent must know exact frame
  dimensions it's grounding against, or tier-3 clicks land wrong.
- **DPI fixed at 1.0** — coordinate grounding is resolution-sensitive;
  scaling must be reported with the frame.

(See `docs/DISPLAY.md` "known gaps".) Fixing these is the tier-3
prerequisite — not GPU sizing.

## 3. Packaging

- Tier 1 (+ much of tier 2) needs **no GPU and no model endpoint** → ships
  in the base **hermetic CPU packages** (`docs/`/pkg-hermetic). "AI can
  operate wash" is mostly true without any accelerator.
- Tier 3 drags in the VLM endpoint → an **optional accelerator sidecar**
  the router talks to (container/NIM/TRT-LLM/vLLM, deployer's choice),
  cleanly outside the base build. Same opt-in discipline as wash-display
  in deb/rpm/apk (`docs/` CI notes): base stays CPU-only; the agent tier
  lights up wherever an endpoint is configured.

## 4. wash as the eval harness (bonus)

A scriptable DE with deterministic apps + router-log assertions is *also*
a GUI-agent benchmark environment. Tasks like "open wash-fm, upload this
folder, sort by size" are exactly GUI-agent-shaped — and success is
**assertable from the router log**, which most pixel-agent benchmarks
can't do. This doubles as a regression gate for the agent tier and a
genuinely novel eval surface.

## 5. Open questions

- Tool-manifest format for tier-1 intents — derive from the existing BE
  gateway verb registry, or a separate declaration?
- Tier-2: CDP attach into ingress-embedded apps — reuse the ingress proxy
  token, or a dedicated debug channel?
- Scoping/safety: input strictly fenced to the target surface; confirmation
  gates for destructive actions (the agent shouldn't `rm` via a terminal
  surface unprompted).
- Concurrency: many agent-driven windows through one model server —
  batching + per-surface KV isolation.
