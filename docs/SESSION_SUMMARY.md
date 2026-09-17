# Session Summary (experimental)

Session Summary is an optional, manually invoked window that answers “what am
I doing?” across the current wash session. It is the first consumer of the
router's **observe** verb and the **activity brief** (docs/COMMANDER.md §4,
§5.2). It depends on the optional AI Provider service; opening it does not run
inference, and content leaves wash only after **Summarize session** is clicked.

## What it reads

For every other open window the window asks that window's router to observe
its instance (`window.wash.observe(origin, instanceID)`). The router answers
from what it already holds, in this order:

1. the app's own export, when its manifest says `observation: export` and it
   answers in time;
2. the tail of the terminal's scrollback ring, stripped of control sequences
   (an instance that owns a pty channel);
3. the instance's saved `app_state` blob;
4. nothing (`none`) — the app is not observable (third-party apps by default;
   priv, settings, inference and login always).

Every observation is redacted by the router before it leaves it (bearer
tokens, `key=`/`password:` assignments, vendor key prefixes), and names its
source, which the window shows beside the briefing. HTML is never read.

## Reduction flow

One explicit run:

1. observes all other open windows and drops the ones that answered `none`;
2. bounds each observation to 64 KiB, at most 64 windows and 1 MiB total;
3. asks the configured inference provider for a **brief** of each window —
   goal, state, now, done, in progress, blockers, next — sequentially, with
   one repair attempt when the answer is not a brief (`pkg/inference/activity`);
4. asks it once more to combine the briefs into a task-oriented briefing.

Results are ephemeral and plain text. The button becomes **Cancel** while a run
is active; cancellation propagates to the current provider request. No periodic
or background summarization exists here — that is the commander service
(docs/COMMANDER.md §5).

## Older Content API

`props.provideContent` and `window.wash.windowContexts()` remain for apps that
registered a live FE provider, but nothing reads them for summaries any more;
an app that wants to refine what a summary sees does so backend-side with
`sdk.HandleObserve` and `observation: export`.
