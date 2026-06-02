# App FE delivery + supplied settings panels + examples

Branch: `fe-delivery`. Working plan; commit-by-commit, green build per commit
([green build before commit]).

## Goals (from design discussion 2026-06-01)

1. **Kill base64 from bundle delivery.** The only base64 hop today is the
   `--wash-manifest` probe envelope (`bundle_b64` over stdout JSON). Replace
   the probe envelope with a **framed format**: a manifest JSON line followed
   by length-prefixed **raw** bundle blobs on stdout. No base64 anywhere —
   storage is raw (`go:embed`), wire is raw (channels ≥2), and now probe is
   raw-framed too. Applies to the main FE bundle *and* the new panel bundle.
2. **Settings panels supplied by apps, declared in the manifest.** Add a
   `SettingsPanel{Section, Element, Icon?, Order?}` descriptor to the manifest.
   The panel bundle ships raw in the probe, is cached on `Entry.PanelBundle`,
   and is fetched by the settings host over a **raw runtime channel**
   (`panels.bundle`). Settings discovers panels via `panels.list`.
3. **Move panel FE back into the owning apps.** Delete the hardcoded
   vscode/display panel TSX from `apps/settings/fe`; settings becomes a
   built-in Desktop pane + a generic panel host. vscode and wash-display each
   ship their own panel.
4. **Extract a reusable C++ wash SDK.** Lift wash-display's wire layer
   (`wire.hpp`, `wire_conn.{hpp,cpp}`) into `cpp-sdk/`; display consumes it.
   Second consumer (the C++ example) justifies the extraction
   ([no premature service]).
5. **`examples/` templates.** Dead-basic about-style apps — Go and C++ — that
   receive/transmit app_msg signals, render a trivial FE, and have a basic
   Makefile/CMake. Grown over time.

This reverses SETTINGS.md §2's "no app-B-in-app-A" stance for the settings
host specifically; SETTINGS.md and WIRE.md get rewritten (commit 11).

## Commit ladder

| # | Commit | Touches | Green? |
|---|---|---|---|
| 1 | `wire: framed probe (manifest + raw bundle frames), drop bundle_b64; add SettingsPanel manifest field` | `internal/wire`, `internal/router` (ParseProbe), `internal/sdk` (probe writer) | `go test ./internal/...` |
| 2 | `router+shell: panel.read (serve Entry.PanelBundle, mirrors asset.read); catalog carries panel descriptors; window.wash.loadSettingsPanel` | `internal/wire`, `internal/router`, `web/shell` | router tests + build |
| 3 | `sdk(go): RegisterSettingsPanel — declare panel + embed panel bundle, raw probe` | `internal/sdk` | sdk tests + build |
| 4 | `settings: generic panel host (discover via window.wash, mount over svc.* relay); drop hardcoded panels` | `apps/settings` (be+fe), `web/lib` (defineSettingsPanel) | build |

**Delivery is shell-centric (decided 2026-06-01):** panel bundles are JS
that only the shell can blob-import + `customElements.define`. So the
router serves `Entry.PanelBundle` to the *shell* via `panel.read` (a
shell→router verb mirroring `asset.read`), the catalog carries panel
descriptors, and `window.wash.loadSettingsPanel(appID)` lets the settings
FE load+define a panel element. The settings BE stays purely the runtime
`svc.*` relay.
| 5 | `vscode: ship settings panel (relocated control FE)` | `apps/vscode` | build + be_test |
| 6 | `cpp-sdk: extract wash-display wire layer into reusable SDK; framed probe + raw panel bundle (no base64)` | `cpp-sdk/`, `wash-display` | `WASH_DISPLAY=1` build |
| 7 | `wash-display: ship settings panel (raw bundle); base64-free CMake embed` | `wash-display` (fe+CMake+main.cpp) | `WASH_DISPLAY=1` build |
| 8 | `examples: go-about template (signals + FE + Makefile)` | `examples/go-about` | build |
| 9 | `examples: cpp-about template (cpp-sdk + FE + CMake)` | `examples/cpp-about` | build |
| 10 | `e2e: supplied-panel discover/mount/round-trip; raw bundle delivery; not-installed fallback` | `apps/test`, `e2e` | Playwright + router-log |
| 11 | `docs: rewrite SETTINGS.md §2, WIRE.md probe section, add EXAMPLES.md` | `docs` | — |

Commits 1–3 are pure-Go foundation, CI-green before any consumer exists.
Commit 4 is the first user-visible payoff. 6–7 plug C++ into an already-green
contract. The base64 removal for the *main* bundle lands in commit 1 (the
framed probe handles main + panel uniformly) — "do both" satisfied there.

## Open / deferred
- **Not-installed hint.** Discovered panels vanish when their package is
  absent (no descriptor → no panel). Whether settings keeps a small built-in
  "suggested integrations" list for install hints is deferred; default is
  invisible-when-absent.
- **Panel transport.** v1 = `svc.*` relay to the panel's own app only.
  Config-file-only panels (Desktop) stay settings built-ins.
