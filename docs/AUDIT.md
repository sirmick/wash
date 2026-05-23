# wash — code audit

A snapshot of patterns worth keeping, duplication worth collapsing,
and a few "this could be smaller" notes. Each item names the files
and (where useful) the call sites, so the work can be picked up
incrementally.

## Things the code does well

- **Transport-not-interpreter discipline is intact.** The router
  rarely peeks into payloads. The places it does (`relayAppMsgToShell`
  in `internal/router/app_session.go`, the cross-app sender stamping)
  are clearly motivated and minimal.
- **Server-authoritative WM state.** `windowSession` in
  `internal/router/session.go` is small and the snapshot/patch model
  keeps multi-shell sync trivial. Locally-mirrored `moveLocal` /
  `raiseLocal` give snappy drag/focus without forking authority.
- **CBOR/JSON pitfall handled.** Comments at every BE → wire boundary
  document why structured fields don't carry `[]byte` /
  `json.RawMessage`. The discipline is uniform.
- **Atomic writes everywhere.** `internal/fs/mutate.go:Write`,
  `internal/router/session.go` settings, wash-settings — all
  tmp+fsync+rename.
- **Reserved-id trust gate is small and explicit.**
  `internal/router/registry.go:reservedIDs` + `isTrustedBinary`. Easy
  to extend, easy to reason about.

## Highest-leverage simplifications

### 1. Collapse the three handshake/teardown paths

`internal/router/router.go:spawnAndRun`,
`internal/router/app_session.go:handleAppOpts`, and
`internal/router/attach.go:startFreshAttach` all run the same
post-handshake bring-up:

```go
r.registerApp(inst)
// if windowed:
patches := r.winSession.createWindow(...)
r.declareAppToAllShells(ctx, inst)
r.broadcastPatches(patches)
_ = inst.WriteEvt(wire.NewEvtWindowMapped(inst.WindowID))
// run inst.loop(ctx)
// teardown:
r.unregisterApp(inst)
r.closeChannelsForApp(inst, "app exited")
r.dropAppMsgWatchers(inst.InstanceID)
r.winSession.dropAppState(inst.InstanceID)
if inst.WindowID != 0 { r.broadcastPatches(r.winSession.destroyWindow(...)) }
```

Three near-identical copies. Extract `bringUp(ctx, inst)` and
`tearDown(inst)` on `*Router`; each caller becomes ~5 lines instead
of ~20. The differences (token-attach uses a pre-minted instance id;
spawnAndRun reaps a `*exec.Cmd`) fit cleanly behind a single
`registerAndRun(ctx, inst)` helper that takes a `Cmd` (may be nil).

### 2. One read-loop helper

Identical "spawn reader goroutine, select on ctx + chan + EOF" code in
`internal/router/app_session.go:handshake`,
`internal/router/app_session.go:loop`,
`internal/router/shell_session.go:loop`, and
`internal/sdk/dispatch.go:Run`. The cancellation/EOF handling is the
same in all four. A small helper:

```go
func readLoop(ctx context.Context, t wire.FrameTransport,
              handle func(wire.Frame) error) error
```

would remove ~80 lines and put the EOF logic in one place. `wire.Mux`
already exists (`internal/wire/mux.go`) with tests and **isn't used
anywhere**. Either teach the router/SDK to use it, or delete it.

### 3. Move CBOR coercion helpers into the SDK

Every BE has its own copy:

```
cmd/wash-priv/main.go:219    toInt64
cmd/wash-priv/main.go:236    toStringMap
cmd/wash-priv/main.go:271    toString
cmd/wash-priv/main.go:280    toStringSlice
cmd/wash-term/main.go:226    toUint
cmd/wash-edit/main.go:286    toUint
cmd/wash-fm/main.go:414      toUint32
cmd/wash-bulk/main.go:221    toStringSlice
cmd/wash-settings/main.go:239 toJSON  (and another in router + sdk)
```

These exist because `cbor.Unmarshal(payload, &m)` lands integers as
`int64`/`uint64`/`float64` depending on the original encoding, and
maps come in as `map[any]any`. The SDK already owns the dispatch path
that produces these shapes; it should ship the small coercion library
that every app currently re-rolls. Suggested home: `internal/sdk/cbor.go`
(or `internal/cborutil`):

```go
func ToString(v any) string
func ToInt64(v any) int64
func ToUint64(v any) uint64
func ToStringSlice(v any) []string
func ToStringMap(v any) map[string]string
func AsMap(data any) map[string]any   // CBOR map[any]any → map[string]any
```

### 4. Dedupe `toJSON` / `toJSONValue`

`internal/router/app_session.go:518` (toJSON) and
`internal/sdk/outbound.go:327` (toJSONValue) are byte-for-byte the
same function — walk a CBOR-decoded value and produce a JSON-marshalable
form. One copy should live in `internal/wire/cborjson.go` and be imported
by both.

### 5. Watch helper duplication

`internal/sdk/filepicker.go` has a refcounted `watchState` with a
lazy Manager and per-path `Sub` registry. `cmd/wash-fm/main.go` has
its own `watchState` with the **same fields but no refcounting**. fm
predates the SDK helper; switching fm to `sdk.EnableFilePicker(c)`
(which it doesn't currently call — its FE talks directly to fm's BE
with `watch`/`unwatch` messages) or extracting the SDK's `watchState`
into a shared internal package would drop ~70 lines from fm and
remove the silent-refcount-bug footgun.

### 6. PTY+raw-channel scaffolding

`cmd/wash-term/main.go:openTab` and `cmd/wash-edit/main.go:openTerm`
are within a few lines of being the same function. The wash-edit
comment literally says:

> Mirrors wash-term's openTab almost exactly — when the pty/term
> primitives are extracted to internal/pty/ both apps will collapse
> onto the same code.

Extract `internal/pty/` (or `internal/term/`) with:

```go
type Session struct{ pty *os.File; cmd *exec.Cmd; ch *sdk.RawChannel }
func Open(c *sdk.Conn, win uint32, cols, rows uint16, argv []string) (*Session, error)
func (s *Session) Resize(cols, rows uint16) error
func (s *Session) Close() error
```

Plus the shared `isPtyTerm(err)` helper (currently duplicated in
both files) and `withWashEnv` from wash-term (env-tweak: TERM,
`WASH_BIN_DIR` prepended to PATH).

### 7. Manifest is duplicated between router and SDK

`internal/router/manifest.go` defines `Manifest`, `WindowHints`,
constants. `internal/sdk/manifest.go` redefines the same shape with a
comment that says "kept here so apps can construct it as a Go literal
without importing the router."

The fix is to move the schema to `internal/wire/manifest.go` and have
both router and SDK import it. The router's `Validate` lives next to
it; the SDK only uses the struct, so the import asymmetry is fine.
Today, adding a manifest field means editing two places without a
compiler error in between.

### 8. Per-app `sendErr` and reply structs

`cmd/wash-fm/main.go:827`, `cmd/wash-edit/main.go:503`, and
`cmd/wash-settings/main.go:231` each have a `sendErr(c, kind, id,
path, code, msg)` that's structurally identical. The result structs
(`errResult`, `listResult`, `readResult`, `writeOK`, `pathOK`) are
duplicated between fm and edit verbatim.

These belong with `internal/fs/` (since the schemas mirror its API)
or as a small `internal/fsapi` package both apps import. Either way
the duplication evaporates and the wire surface gets one definition.

### 9. Custom-element boilerplate in every web app

Every `web/apps/<name>/src/main.tsx` ends with:

```ts
class WashApp<Name> extends HTMLElement {
  private cleanup?: () => void;
  connectedCallback() {
    this.style.cssText = '...';
    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.cleanup = render(() => <App instance={instance} host={this} />, this);
  }
  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}
if (!customElements.get('wash-app-<name>')) {
  customElements.define('wash-app-<name>', WashApp<Name>);
}
```

Identical 12-line shape in 10 apps. A helper in `@wash/ui`:

```ts
defineWashApp('wash-app-fm', (props) => <App {...props} />, {
  style: 'display:flex;flex-direction:column;height:100%;...',
});
```

shaves ~12 lines × 10 apps = ~120 lines of duplicate scaffolding.

### 10. `Window.wash` ambient type is redeclared everywhere

Every app's `main.tsx` repeats `declare global { interface Window { wash: { ... } } }`
with a partial set of the methods it uses. The shell exports the full
declaration in `web/shell/src/main.tsx:482`. Move it to a `.d.ts` in
`@wash/ui` (or a new `@wash/api` package) and the per-app `declare global`
goes away.

## Smaller cleanups

- **`maybePrintManifest` is a for-loop over one entry.** The convention
  is that `--wash-manifest` is invoked as the only arg by the router's
  probe. `internal/sdk/sdk.go:182` could be `if len(os.Args) > 1 &&
  os.Args[1] == "--wash-manifest"` and read identically.

- **`firstNonEmpty` in `cmd/wash-router/main.go:222`** is fine, but
  the body where it's called is 12 lines of `firstNonEmpty(flag, env,
  default)` boilerplate. A `flagOrEnv(name, envKey, dflt)` helper that
  takes the flag pointer would make `main` half its size.

- **Two `os.Executable + EvalSymlinks` calls.** `resolveRouterExe` and
  `defaultAppsDir` in `cmd/wash-router/main.go` are the same dance.
  One helper.

- **`router/session.go` vs `router/session_app.go` vs
  `router/cli_session.go` vs `router/app_session.go` vs
  `router/shell_session.go`.** Five files with "session" in the name,
  three different meanings (WM state, autoboot orchestration, per-app
  conn state, per-shell conn state, CLI back-channel). Worth renaming:
  `session.go` → `wmstate.go`; `session_app.go` → `autoboot.go`. Files
  next to each other right now suggest a relationship that isn't there.

- **`fallbackIndexHTML` in `internal/router/http.go:20`** still
  references "commit C5" — an old build milestone that no longer
  exists. The real shell is always embedded now; the fallback is only
  hit during `go test` of router-only builds. Either delete or update
  the prose.

- **The router has nothing on Channel 0 of the WS labeled "raw".**
  WIRE.md §15 says "Channel ids ≥ 1 are reserved for raw channels" on
  the WS; the comment in `internal/router/router.go:17` matches. But
  channels 0 and 1 on the app socket use "0=ctrl, 1=event" while the
  WS uses "0=ctrl, ≥1=raw." Fine, but the asymmetry surprises every
  reader once and could be a one-line comment in `WIRE.md §3`.

- **Dead-code: `wire.Mux`.** Has tests, no callers. Use it or drop it.

- **`internal/router/spawn.go:Spawn`** writes `WASH_INSTANCE_ID=""` —
  the comment says "the SDK reads instance_id from the IdentityAck."
  Then don't set the env var at all (or document why you set it to
  empty rather than just omitting it).

- **`internal/router/router.go:nextChannel`** starts at 1 and
  `Add(1)+1` returns ids ≥ 2. The off-by-one is intentional but the
  comment ("the allocator starts at 2 so it's safe on both") is
  easier if it just `Store(1)` initial and `Add(1)+1` becomes
  `Add(1)`. Cosmetic only.

- **`cmd/wash-priv-fakesudo`** sits in `cmd/` so it shares GOFLAGS,
  but lives in production layout despite being test-only. The Makefile
  guards it well; consider moving its source under `e2e/` with a
  comment explaining the build dance, so a fresh reader doesn't
  wonder why there's a fake sudo binary in the app catalog. (Today
  it's also hidden by the registry filter, but only at runtime.)

## What not to touch

- **The wire spec itself.** It's tight, versioned, and the codec is
  small.
- **`internal/bulkops/`.** Clean library boundary. Worker model is
  the right shape for adding parallelism later.
- **`internal/fswatch/`.** The refcounted Sub model is exactly the
  pattern the architecture document calls for; replicating it inside
  fm (item 5) is the violation, not this package.
- **The two-stage Makefile.** The "embed errors loudly if web didn't
  build" property is doing real work; don't simplify by removing that
  failure mode.
