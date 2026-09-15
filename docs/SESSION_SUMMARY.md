# Session Summary (experimental)

Session Summary is an optional, manually invoked window that answers “what am
I doing?” across the current wash session. It depends on the optional AI
Provider service; opening it does not run inference, and content leaves wash
only after **Summarize session** is clicked.

## Content API

Every app defined with `defineWashApp` receives:

```ts
props.provideContent(() => ({ /* JSON-compatible app context */ }))
```

Registering a provider is optional. The shell resolves content in this order:

1. the live app provider, when it returns a value;
2. that instance's router-backed `app_state` blob;
3. no content.

Returning `undefined` selects the fallback. A thrown provider error is reported
as metadata and also falls back. Window metadata (app/instance IDs, title,
origin, focus and state) is kept separate from content. The shell exposes the
snapshot as `window.wash.windowContexts()`; HTML is never used as an implicit
fallback.

The contract is synchronous and read-only. Providers should return a compact,
current, JSON-compatible snapshot and must not perform I/O. Future app-specific
providers can expose useful semantic structure—for example terminal tabs plus
recent scrollback, or editor tabs and document content—without changing the
session summarizer.

## Reduction flow

One explicit run:

1. snapshots all other open windows;
2. bounds each serialized context to 64 KiB, at most 64 windows and 1 MiB total;
3. asks the configured inference provider for a compact summary of each window,
   sequentially;
4. asks it once more to combine those summaries into a task-oriented briefing.

Results are ephemeral and plain text. The button becomes **Cancel** while a run
is active; cancellation propagates to the current provider request. No periodic
or background summarization exists in this experiment.
