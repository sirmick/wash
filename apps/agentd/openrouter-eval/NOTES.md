# Notes

Newest first. Prices are OpenRouter's, USD per million tokens, input /
output; a model's listed price is its headline, and OpenRouter routes tool
calls to the providers with the best tool-call record (Auto Exacto, on by
default since 2026-03-12), so what a run pays is somewhere between the
cheapest endpoint and the headline. Everything below was released after the
assistant's training data, so every claim about a model is from these runs or
from the sources named.

## 2026-09-27: team runs in the jail

The same shakedown, now in the jail (`jail/team.sh`): wash headless in an
Alpine container, driven over the control socket, nothing of the host
mounted but the output folder; several runs in parallel. Every run passed
**18/18**, every audit found nothing outside the project (OpenCode's own
`tool-output` spill folder is now allowed). The 3 Wash reminders each run
got are the ones the script provokes (nobody on C, M2 done, M3 next).

| Mix (orchestrator / members) | Minutes | Owner prods | Orchestrator $ | Members $ |
|---|---|---|---|---|
| DeepSeek V4-Pro-0813 / DeepSeek V4.1-Flash | 2.5 | 0 | 0.79 (68 calls) | 0.018 |
| DeepSeek V4-Pro-0813 / MiniMax M3 | 4.4, 5.2 | 0, 0 | 0.92 (67 calls) | 0.045 |
| Kimi K3 / DeepSeek V4.1-Flash | 4.6 | 0 | 1.43 (73 calls) | 0.017 |
| GLM-5.3 / GLM-5.3-Flash | 5.5 | 1 | about 0.55 (38 calls, from the host run) | 0.009 |
| GLM-5.3 / DeepSeek V4.1-Flash | 5.5 | 1 | not measured | not measured |

Costs are OpenCode's own per-call accounting (`audit.py --costs`), exact
per run; the first batch measured the shared key instead, which counts the
parallel runs too, so its costs are left out. $6.12 spent on the key in all,
screens and every run included.

- **The orchestrator is the cost**, 95–98% of every run; members on any of
  the cheap models cost cents. Choose the orchestrator on quality and price,
  the members on speed and reliability.
- **GLM-5.3 stops after answering "status?"** in both of its jail runs, as on
  the host: it needs one prod. DeepSeek V4-Pro and Kimi K3 carried on alone.
- **The shakedown is saturated:** every mix passes. It proves the mixes keep
  Wash's process; it cannot rank their coding. That needs a harder team
  benchmark with real work, implementers on `coding` and reviewers on
  `small`.
- DeepSeek V4-Pro dug through a large `workspace_get` result (which OpenCode
  spilled to a file) to find the pending owner question: the team view could
  make what waits on the owner easier to find.

## 2026-09-27: team runs

The shakedown (`e2e/shakedown`, 18 checks) through `team.sh`, orchestrator on
`frontier`, every member on `small`.

**Run 1, GLM-5.3 / GLM-5.3-Flash** (`results/2026-09-27-1522-team-glm53-glm53flash`):
timed out at 40 min with 15/18 checks, $0.41. Not the models' fault, twice:

- A 24-minute silence in b-impl's first turn was the owner's PC sleeping, not
  a stalled provider. The scorecard now reports `pause_minutes` (gaps over
  five minutes in which no session recorded anything) and `active_minutes`.
- Steps 1–8 were all correct: plan, staffing, the QA thread and its wake-up,
  both review rounds, accepting A with trailers, the refused then overridden
  start of C, the owner's answer, the failure on purpose. At step 9 GLM-5.3
  kept to "do not poll": it set `waiting` for c-impl's owner question and
  ended its turn, and the note Wash leaves when a member asks the owner never
  woke it, so it sat until the deadline. Sonnet had got through by polling.
  **Fixed in Wash:** a note now wakes a member idle in a plain wait (no
  waiting set); a busy one, or one holding a waiting set, still gets it with
  its next turn (`pickDelivery`, `TestANoteWakesAPlainWait`).

**Run 2, GLM-5.3 / GLM-5.3-Flash, with the note fix**
(`results/2026-09-27-1602-team-glm53-glm53flash`, saved by hand; the run
was stopped): **18/18 checks**, about $0.16, steps 1–11 in about 6 minutes
(16:02–16:08). Step 9 now works: the note woke the orchestrator, it ended
c-impl, the "nobody on it" nudge arrived. Then GLM-5.3 answered "status?"
(step 11) and ended its turn, as if that finished the job: steps 12 (end and
resume) and 13 (report) never ran; `check.sh` does not cover them. Sonnet
carried on. The harness now plays an owner who prods: after three quiet
minutes with the orchestrator idle it types "Continue with the script.", and
the scorecard counts `owner_prods`.

The audit (`audit.py`, OpenCode's own record of every tool call) found
nothing outside the project in either run: the shell commands were `ls`,
`cat`, `od`, `grep`, `sleep`, and the two commits the script asks for; the
reviewers used only read, glob and Wash's tools.

## 2026-09-27: the field, and the easy screen

**The field.** OpenRouter lists 458 models; 140 have open weights (a Hugging
Face id) and tool calling. The candidates, all in OpenCode 1.18.32's list:

| Tier | Model | Headline | Context | Providers |
|---|---|---|---|---|
| top | Kimi K3 `moonshotai/kimi-k3` | 3 / 15 | 1M | 16 |
| top | GLM-5.3 `z-ai/glm-5.3` | 1.40 / 4.40 (cheapest 0.36 / 1.12) | 1.3M | 40 |
| top | DeepSeek V4-Pro-0813 `deepseek/deepseek-v4-pro-0813` | 0.29 / 3.50 | 1M | 22 |
| top | Qwen3.8 2.4T `qwen/qwen3.8-2.4t-a95b` | 2 / 6 | 1M | 7 |
| mid | MiniMax M3 `minimax/minimax-m3` | 0.30 / 1.20 | 1M | 12 |
| mid | MiMo-V2.6-Pro `xiaomi/mimo-v2.6-pro` | 0.44 / 0.87 | 1M | 3 |
| mid | Kimi K2.7-Code `moonshotai/kimi-k2.7-code` | 0.66 / 3.30 | 262k | 16 |
| mid | Qwen3.8-27B `qwen/qwen3.8-27b` | 0.42 / 3 | 1M | 16 |
| cheap | DeepSeek V4.1-Flash `deepseek/deepseek-v4.1-flash` | 0.04 / 0.29 | 1M | 26 |
| cheap | GLM-5.3-Flash `z-ai/glm-5.3-flash` | 0.05 / 0.14 | 1.3M | 33 |
| cheap | MiMo-V2.6-Flash, Qwen3.8-Flash, Nemotron 3.5 Lightning, Laguna-S-2.1 | 0.08–0.15 / 0.18–0.47 | 1M | 2–? |

Vendor benchmark claims (Terminal-Bench 2.1, DeepSWE, SWE-bench) put K3,
GLM-5.3 and DeepSeek V4-Pro at the top and V4.1-Flash, GLM-5.3-Flash and
MiniMax M3 as the strongest cheap ones; they are vendor-reported and on
different suites, so they only chose what to screen.

**The easy screen** (`results/2026-09-27-easy-screen-run1`, `-run2`; $0.55
for both). Effort high where offered.

| Model | Hidden | Wall s | $ | Notes |
|---|---|---|---|---|
| Kimi K3 | 29/29 | 30–63 | 0.09–0.11 | checks its work with extra tests in OpenCode's scratch dir |
| GLM-5.3 | 29/29 | 20–41 | 0.05 | the same |
| DeepSeek V4-Pro-0813 | 29/29 | 33 | 0.03 | clean, no failed tools |
| Qwen3.8 2.4T | 29/29 | 44–82 | 0.05–0.07 | offers low/medium/xhigh/default, not high; slow |
| MiniMax M3 | 29/29 | 44 | 0.03 | no effort option; one malformed `todowrite` call, recovered |
| MiMo-V2.6-Pro | 29/29 | 115 | 0.01 | 62 s before its first reply |
| Kimi K2.7-Code | 27/29 | 31 | 0.04 | 3 tool calls aborted; no effort option |
| Qwen3.8-27B | 29/29 | 97–127 | 0.03–0.04 | **streams its reasoning and draft code as the reply** (`qwen3.8-27b-leak-transcript.md`) |
| DeepSeek V4.1-Flash | 29/29 | 19 | 0.006 | fastest; thinking stayed in thought chunks |
| GLM-5.3-Flash | 29/29 | 11–36 | 0.002 | cheapest |
| MiMo-V2.6-Flash | 29/29 | 89 | 0.002 | slow |
| Qwen3.8-Flash | 29/29 | 97 | 0.007 | 2 tool calls aborted |
| Nemotron 3.5 Lightning | 28/29 | 31 | 0.01 | narrates between every tool call |
| Laguna-S-2.1 | 29/29 | 42 | 0.004 | clean |

Wall times vary up to 2× between runs of the same model: rank on two runs or
more.

**Dropped for now:** Qwen3.8-27B (thinking in the reply), Qwen3.8-Flash and
Kimi K2.7-Code (aborted tool calls), both MiMo (slow), Qwen3.8 2.4T (slow,
no `high`). **Shortlist:** orchestrator K3, GLM-5.3 or DeepSeek V4-Pro;
implementer DeepSeek V4.1-Flash or GLM-5.3-Flash; reviewer MiniMax M3 (a
different family from the implementers), Laguna-S as the cheap spare.

**DeepSeek V4.1-Flash mixing thinking into the conversation**, as the owner
saw it, did not reproduce here: its thinking arrived as `agent_thought_chunk`
throughout. Open: where it was seen (Agent window? which adapter?).

**Harness lessons.**

- OpenCode ends its turn when a permission is refused. K3 and GLM-5.3 lost
  their `CHANGES.md` in run 1 because the harness refused their scratch-dir
  tests; the screen now allows the project and `/tmp/opencode`. The same
  holds in Wash: a refused ask stops an OpenCode member's turn.
- The OpenRouter key's usage lags by more than the gap between two models;
  per-model cost is OpenCode's own `usage_update.cost`, and the key's usage
  only totals a run.
- Effort: OpenRouter models on OpenCode offer `low | high | max | default`,
  except the Qwen3.8 family (`low | medium | xhigh | default`) and MiniMax
  M3 and Kimi K2.7-Code (no effort option at all). A slot must match, or the
  launch fails.

**Reviewers on OpenCode.** Wash refused `capability: reviewer` outside Claude
Code. It now launches an OpenCode reviewer with its write, edit, patch, bash,
task, webfetch and skill tools removed and denied (`opencodeReviewer`),
pinned to OpenCode 1.18.32. Probed with GLM-5.3-Flash asked to create a file
by any means: it listed read, glob, grep, todowrite and created nothing, also
with a project `opencode.json` turning the tools back on.
