# OpenRouter evaluation

Choosing open-weight models on OpenRouter for Wash's catalogs: one strong
model to orchestrate and architect, cheap ones to implement and review. Two
stages, both repeatable, both spending real money on the stored OpenRouter key
(the one in Wash's `keys.json`):

1. **Screen** each model alone on a small benchmark (`screen/`, `bench/`):
   cheap, minutes per model. Drops the models that are slow, fail tool calls,
   break rules or stream their thinking as the reply.
2. **Team** runs of the survivors, as mixes (`mixes/`), on the workspace
   shakedown (`team.sh`): an orchestrator and members through Wash itself.

What was found, and why the catalogs are what they are, is in
[NOTES.md](NOTES.md). Every run's raw output is under `results/`, one
directory per run, named by date.

| Path | What |
|---|---|
| `screen/` | `model-screen`: runs a benchmark against each model in a fresh OpenCode session over ACP and scores it |
| `bench/` | frozen benchmark projects (their own Go module, so the repository's `go test ./...` skips them) |
| `mixes/` | catalogs to run the team stage with: `frontier` orchestrates, `small` staffs every member |
| `team.sh` | runs the live shakedown with one mix and keeps its results |
| `results/` | every run's scorecards and transcripts |
| `NOTES.md` | findings, decisions, open questions |

## The jail

Models we do not trust run in `jail/`: an Alpine container with wash
headless (`wash-router -no-session`, no browser), driven over the router's
control socket (docs/INTERNALS.md, "Control socket"): `launch` the Agent
app, `msg` it what its frontend would send (`agent_start`, `agent_prompt`,
`agent_answer`, `agent_question_answer`), `watch` it for what comes back.

    make multicall out/e2e/codex-acp
    sg docker -c apps/agentd/openrouter-eval/jail/build.sh

builds `wash-jail` and runs `smoke.sh` in it against the fake ACP adapter:
launch, start, a permission ask answered through `msg`, a second prompt.
Verified 2026-09-27, all checks passing. Still to do: OpenCode in the image,
egress limited to openrouter.ai, and the shakedown driven this way instead
of through Playwright.

## Screen

    make model-screen MODELS=z-ai/glm-5.3,minimax/minimax-m3 BENCH=easy BUDGET=1

or `go run ./apps/agentd/openrouter-eval/screen -h` for the flags (effort,
timeout, a different prompt, a different OpenCode configuration: the last two
are how the reviewer lockdown was probed). Each model gets a copy of the
benchmark's `project/` in a new git repository, OpenCode's config and data
isolated per run, edits and commands allowed inside the project and OpenCode's
scratch directory (`/tmp/opencode`), everything else refused.

Each benchmark directory holds:

- `project/`: what the model gets, with `TASK.md`, which the prompt names.
- `hidden/`: tests copied in after the session and run by the scorer, one
  subtest per scored case. The model never sees them.
- `reference/`: a solution the hidden tests pass on; `make bench-selfcheck`
  checks that.

Never change a benchmark once scores exist for it: add a new one instead, or
the scores stop being comparable.

| Column | Meaning |
|---|---|
| hidden | held-back subtests passed |
| visible | the project's own `go test ./...` passes |
| rules | no `_test.go` edited, and `CHANGES.md` as `TASK.md` asks |
| wall s, first out s, first reply s | time to finish, to the first thinking or reply text, to the first reply text |
| thought/reply chars | text sent as thinking and as reply |
| leaks/code lines | reply lines that read as reasoning ("Wait —", think tags, "the user wants…"), and reply lines inside code fences: both are thinking that arrived as the reply |
| tools (failed) | tool calls, and how many failed |
| asks | permission requests (outside the project) |
| context, $ | context tokens at the end, and OpenCode's cost for the session |

A run leaves, per model: `events.jsonl` (every ACP update, timestamped),
`transcript.md` (thinking, reply and tool calls in order), `work/` (the
finished project) and `opencode.stderr`.

Benchmarks:

- `easy`: three small Go tasks (fix `Clamp`, write `Slug`, write
  `TopWords`) with 29 hidden cases; the untouched stubs pass 9. A screen, not
  a ranking: nearly every model gets 29; what separates them is speed, tool
  calls, rules and where their thinking goes.

## Team

    make test-app && make TEST_APP=1 multicall     # once per wash change
    apps/agentd/openrouter-eval/team.sh glm53-glm53flash 40

runs `e2e/tests/agent-shakedown-live.spec.ts` in an isolated Wash with the mix
as the workspace catalog, the test answering as the owner, and writes to
`results/<date>-team-<mix>/`: `scorecard.json` (check.sh passes and failures,
minutes, the key's spend, Wash's reminders, lifecycle messages), `check.txt`,
`orchestrator.txt` (its transcript, with its deviation log at the end),
`workspaces.json`, `dot-wash/` and `git-log.txt`.

The shakedown puts every member on the `small` slot, so a mix is an
orchestrator plus one member model. The reviewer (`capability: reviewer`) runs
on OpenCode with its write, shell, fetch and delegation tools removed
(`opencodeReviewer` in `apps/agentd/be/adapters.go`).
