# wash — Web Application SHell

A browser-delivered Linux desktop environment. A traditional floating desktop
(window manager, taskbar, menu, real windows) served over a single WebSocket
from one static, dependency-free Go binary that runs anywhere — including
embedded Linux.

wash is not a pixel-streamed remote desktop and it does not co-opt an existing
desktop environment. Every surface is real HTML it owns. Applications are
independent one-file programs with a defined contract; the desktop itself is
just another application. The runtime is a microkernel: a transport that knows
nothing about desktops.

- **Router** — one static Go binary (`CGO_ENABLED=0`, no cgo, embeddable).
  Pure transport: multiplexes channels, never interprets them.
- **Apps** — self-contained executables: a backend event-loop program plus an
  embedded frontend web component, discovered by dropping the file in a folder.
- **Frontend** — a compositor that hosts web components in floating windows.
  The desktop chrome is the session app's web component, and is replaceable.

Status: design locked, v1 implementation planned. Not yet built.

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the locked design and rationale.
- [docs/PLAN.md](docs/PLAN.md) — the phased v1 implementation plan.

License: AGPL-3.0.
