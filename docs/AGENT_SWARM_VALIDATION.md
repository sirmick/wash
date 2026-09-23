# Workspace implementation validation

Date: 2026-09-22. Base: `e6e78fbbcd95291f51e0fc50ac0ff165caec71be`.
Implementation branch: `agent-workspace-mcp`, isolated checkout
`/data/wash-agent-swarm/src`. The running Wash desktop was not installed over,
restarted, or stopped. Builds, temporary files, browser state, provider probe homes,
and caches were placed under `/data/wash-agent-swarm`. Temporary provider homes
and their copied authentication files were removed after the probes.

## Checks and results

| Check | Result |
| --- | --- |
| Multicall build with test app | Passed |
| Standalone agentd build and stdio MCP initialize/tools/list | Passed |
| Repository-wide `go vet ./...` | Passed, including final source |
| Repository Go unit suite, excluding the separate `wash-vm/vm` integration tier | Passed |
| Multicall dispatcher unit checks | Passed |
| Final affected Go packages under `-race` | Passed: swarm state, MCP bridge, ACP, agentd, Agent backend, fake adapter |
| Frontend unit suite | 618 passed |
| Frontend component suite | 193 passed across 23 files |
| Sidebar component and plan-delta checks after refinements | Passed |
| E2E TypeScript check | Passed |
| Design tokens, interaction targets, versions, imports and package binary guards | Passed |
| Complete multicall browser suite | 687 passed, 17 skipped, four failures investigated below |
| Subsequent Agent/syslogs regression run | 66 passed; workspace toast test corrected and passed separately; syslogs failure reproduced on unchanged source |
| Final workspace browser scenario | Passed, including desktop notification visibility and source-window focus |
| Real Codex ACP 1.13.0 | Passed: actual injected MCP call, session/load, another actual MCP call |
| Real Claude ACP 0.81.0 | Session creation succeeded; prompt blocked by expired OAuth credentials that could not refresh |

The feature browser test runs the actual built stdio MCP executable from the fake
ACP adapter. It never bypasses the bridge to call agentd's private API or write
its state. It verifies ordinary-window setup, compact keyed updates, external
Markdown edits and atomic replacement, resident questions/answers, ephemeral
work across an early reply, explicit completion and retirement, paused inbox
retention, human attribution, read-only live and archived member previews, assignment results, decisions, status/emoji,
desktop flashes with another app focused, notification click-through, browser
reload, teardown, file preservation, and a second setup in the same conversation.

Go checks additionally cover persistence failure without successful acceptance,
concurrent retry deduplication, recipient authorization, paused recovery and
uncertain delivery, stale plan revisions, invalid schemas, concurrent decision
receipts, member teardown, delegated results, bounded documents, provenance after
ACP replay, and preventing inbox submission before session/load completes.

## Failures investigated

- Two adapter-configuration E2E assertions counted only user-configured MCP
  servers. Updated them to include the built-in workspace server. Both passed
  in the later regression run.
- The fake ACP adapter removed a response channel when the response arrived,
  before its waiter necessarily obtained the channel. A fast filesystem reply
  could therefore be lost. The fixture now retains it until consumption; a
  deterministic early-response regression and the filesystem E2E checks pass.
- The workspace flash test initially clicked the notification-history row, which
  only marks an entry read. It now clicks the actionable desktop toast and
  verifies agentd's source-window focus routing. The final scenario passed.
- `syslogs.spec.ts` / “selecting a file starts tailing and emits lines” times out
  with the view waiting for entries on this host. The same assertion failed in
  an isolated router built from the unchanged base source. A proposed test-only
  adjustment did not resolve it and was reverted. No syslogs changes are included.

The 17 skipped full-suite cases require unavailable optional environment features;
the separate VM integration tiers were not run. This is not a claim of an entirely
green full product E2E run. The real-provider check is a bounded read-only MCP
probe, not a real Redoubt package implementation or a complete multi-provider
swarm exercise. Claude's actual tool round trip remains unverified until its
credentials are refreshed.

## Reproduction

Use a separate checkout/build/session; do not restart a desktop that hosts active
agent conversations. In this run the test environment was:

```sh
export TMPDIR=/data/wash-agent-swarm/tmp
export GOCACHE=/data/wash-agent-swarm/go-cache
export npm_config_cache=/data/wash-agent-swarm/npm-cache
export GIT_CEILING_DIRECTORIES=/data/wash-agent-swarm
unset CODEX_PATH
```

`GIT_CEILING_DIRECTORIES` is needed here because `/data` is itself a Git repository;
otherwise a test's temporary “non-repository” folder discovers that ancestor.
`CODEX_PATH` was removed for the existing adapter unit test that controls its own
Codex executable lookup.

Dependencies were copied from the existing checkout. Builds used
`pnpm_config_verify_deps_before_run=false make -o web-deps TEST_APP=1 multicall`
to avoid reinstalling copied dependencies, plus the existing
`out/e2e/codex-acp` and `out/wash-priv-fakesudo` targets. Final incremental builds
rebuilt the Agent bundle and dispatcher directly.

```sh
go vet ./...
go test -count=1 -p 1 -timeout 120s $(go list ./... | rg -v '/wash-vm/vm$')
go test -tags=multicall ./cmd/wash/...
go test -race ./internal/swarm ./internal/workspacemcp ./internal/acp \
  ./apps/agentd/be ./apps/ai/be ./e2e/fixtures/acp-fake
pnpm_config_verify_deps_before_run=false make -o web-deps fe-unit component \
  check-design check-interactive check-versions check-imports check-pkg-binaries
```

From `e2e/`, run `node node_modules/typescript/bin/tsc --noEmit`, then:

```sh
WASH_E2E_MULTICALL=1 WASH_E2E_SKIP_VM=1 \
  node node_modules/@playwright/test/cli.js test --workers=4
WASH_E2E_MULTICALL=1 \
  node node_modules/@playwright/test/cli.js test tests/agent-workspace.spec.ts --workers=1
```

The opt-in provider test is `TestWorkspaceMCPAgainstRealAdapter` in
`internal/acp/workspace_real_test.go`. Set `WASH_ACP_ADAPTER` to the adapter
executable and `WASH_WORKSPACE_BINARY` to the isolated built Wash binary. Give
probes a separate HOME/CODEX_HOME/CLAUDE_CONFIG_DIR and provide authentication
without exposing it in logs. It issues only two `swarm_status` calls against a
read-only test server, with a 120-second deadline.

Command logs are retained under `/data/wash-agent-swarm/test-results/`, including
`e2e-full.log`, `agent-e2e-final.log`, `workspace-e2e-final.log`,
`syslogs-baseline.log`, `workspace-race.log`, `frontend-checks.log`, `go-unit.log`,
`codex-provider.log` and `claude-provider.log`.


## Workspace JSON and named profiles follow-up (2026-09-22)

The follow-up adds `workspace_get`, atomic `workspace_configure`, named launch
profiles, provider/model/thinking/config overrides, durable launch snapshots and
sidebar launch details. Ordinary successful conversation turns no longer change
the workspace revision when no durable state changed, so a read/modify/write
sequence can cross that turn boundary. Profile settings are applied and verified
before role instructions or assignments are queued.

Final checks for this change, in the same isolated `/data` checkout:

- Race-enabled Go tests passed for `internal/swarm`, `internal/workspacemcp`,
  `apps/agentd/be`, and `e2e/fixtures/acp-fake`. Coverage includes atomic rollback,
  authorization, profile persistence/replacement/deletion, concurrent revision
  guards, launch snapshot isolation, model-before-thinking dependencies,
  unsupported settings, provider errors/coercion, JSON option metadata and bounded
  history pagination.
- **15 browser tests passed** across `agent-workspace.spec.ts`,
  `agent-session.spec.ts`, and `agent-adapter-config.spec.ts`. The profile test
  goes through the injected stdio MCP executable and verifies aliases, defaults,
  overrides, actual adapter config responses, unchanged existing members after
  profile edits, stale revision rejection, invalid launch cleanup and teardown.
- The Agent Vite build and isolated multicall build passed. The existing sidebar
  component suite passed (3 tests; the existing jsdom canvas warning remains).
- Go vet for the changed packages, E2E TypeScript, design-token and interactive
  element checks passed.

The first profile E2E run exposed Markdown formatting of JSON in the fake
adapter's output; it now fences its JSON verbatim. A subsequent run exposed the
unnecessary turn-end revision increment described above; that was fixed and has
a store regression test. The final 15-test run is green. These are deterministic
ACP fixture tests; real-provider authentication/tool evidence and the unrelated
full-suite syslogs limitation remain as documented above. No live model profile
launch or full product suite rerun is claimed for this follow-up.

Logs: `profiles-race.log`, `profiles-e2e-final.log`, `profiles-component.log`,
`profiles-build.log`, and `profiles-guards.log` under
`/data/wash-agent-swarm/test-results/`. The active desktop was not restarted or
replaced. Build caches, temporary files and browser artifacts remained on `/data`;
root free space stayed at about 6.5 GiB.


## Sidebar telemetry and human-message visibility (2026-09-23)

Validated in the isolated checkout without restarting/installing over the active
desktop:

- Race tests passed for `apps/agentd/be`, `internal/swarm` and the ACP fixture.
  Regressions cover overlapping tool calls, late events after turn end, permission
  and lifecycle precedence, durable usage after member retirement, and protecting
  archived counts when an orchestrator conversation starts another workspace.
- **13 browser tests passed** across workspace and Agent-session suites. The new
  test observes thinking → tool → responding → idle from ACP notifications,
  checks context counts and persisted checkpoints, verifies reduced-motion CSS,
  then checks awaiting-message state and usage after a browser reload. The existing
  collaboration test now verifies token counts on a retired ephemeral member.
- **28 component tests** passed for the workspace sidebar and shared AgentSession;
  **10 existing shared status tests** passed. Go vet, E2E TypeScript, design tokens,
  interaction markers, and version checks passed.
- Shared UI/shell, Agent frontend and isolated multicall builds passed. The browser
  screenshot was visually reviewed for human-message contrast and sidebar layout.

Evidence logs under `/data/wash-agent-swarm/test-results/` use the `activity-`
prefix (`race`, `e2e`, `components`, `status`, `guards`, `build`). The screenshot is
`src/e2e/test-results/agent-workspace-sidebar-sh-8d2fc-an-messages-remain-distinct-chromium/workspace-activity.png`.
These activity checks use deterministic ACP fixtures. They do not change the
previously documented real-Claude authentication or broader syslogs limitations.
Root free space remained about 6.5 GiB; builds and artifacts stayed on `/data`.

## Main-panel workspace tabs and sidebar resizing (2026-09-23)

Plan progress/live Markdown and member inspection now open in main-panel tabs.
The sidebar contains navigation, team status, decisions and message activity.
Conversation stays mounted; per-member inbox drafts survive switching tabs.
The shared divider supports dragging plus keyboard resizing and remembers its
width locally. Preview subscriptions follow the selected tab and are cleared
when a reloaded window starts at Conversation.

Validation in the isolated `/data/wash-agent-swarm/src` checkout:

- **30 component tests passed** (workspace layout and shared AgentSession).
  Coverage includes live updates without resetting selection, independent drafts,
  deduplication, close/keyboard navigation, selecting the owning conversation,
  teardown while inspecting a teammate, workspace replacement, removed members,
  preview subscription clearing, and bounded/persisted resizing.
- **14 browser tests passed** across workspace and Agent-session suites. The
  workspace suite verifies main-panel placement, real drag resizing, keyboard
  navigation, no layout overflow, reload width persistence, live Markdown updates,
  resident/retired transcript inspection, inbox messaging, profiles and telemetry.
  After the final preview-on-reload correction, all four workspace browser tests
  and all 30 component tests passed again.
- Shared UI/shell, Agent frontend and isolated multicall builds passed. Browser
  test TypeScript, design-token, interaction-marker and version guards passed.
  The member-tab screenshot was visually reviewed. The standalone app `tsc`
  command remains affected by existing import-extension, test-type and other
  baseline diagnostics; it is not reported as a passing check.

An initial browser run timed out because the test's About window covered the
Plan button after reload. The test now closes that fixture window before
continuing; the final workflow run passes. Component tests retain the existing
jsdom canvas warning. These are deterministic ACP fixture tests, with no new
real-provider or full-product-suite claim.

Evidence is under `/data/wash-agent-swarm/test-results/`: `tabs-build.log`,
`tabs-components.log`, `tabs-e2e-final.log`, `tabs-e2e-preview.log`,
`tabs-guards.log`, and `tabs-types.log`. Screenshot:
`src/e2e/test-results/agent-workspace-workspace--3a596-resizes-without-overflowing-chromium/workspace-tabs.png`.
The live desktop was not restarted or replaced. Builds, caches and artifacts
remained on `/data`; root free space remained about 6.5 GiB.

## About discovery and shared operating guide (2026-09-23)

Added `workspace_get({"view":"about"})` before or after setup, without creating
or changing a workspace. MCP initialization and about share one concise operating
guide and API version. Discovery reports implemented capabilities, current tool
names, configuration/context semantics, caller identity/role and approval metadata.
It explicitly reports unknown provider filesystem enforcement and unsupported bulk
configuration/reviewer capability profiles.

- Race-enabled tests passed for `internal/workspacemcp` and `apps/agentd/be`.
  New coverage checks shared initialization instructions, pre-setup/null behavior,
  attached identity, unchanged store snapshots/settings, and invalid view/options.
- All four workspace browser tests passed. About is exercised through the injected
  MCP bridge before setup and after attachment; ordinary workspace_get and sidebar
  activation behavior remain unchanged.
- Isolated multicall build, focused Go vet, and browser-test TypeScript passed.

Logs under `/data/wash-agent-swarm/test-results/`: `about-race.log`,
`about-build.log`, `about-e2e.log`, `about-vet.log`, `about-types.log`.
No real-provider or whole-product-suite rerun is claimed. The running Wash session
was not restarted or replaced; builds and test artifacts remained on `/data`.

## Bulk MCP and durable QA (2026-09-23)

API 2 advertises the agreed twelve tools. Configuration reserves keyed members in
an atomic store transaction, then reports separate process outcomes. Durable request
receipts deduplicate retries. Resident package workers keep their sessions through
follow-up assignments. QA uses attributed append-only events and guarded transitions;
Wash generates a live Questions tab and links actual human decisions. Teammate tabs
now expose actionable approval requests. Hidden v1 calls remain for compatibility.

Validation in the isolated `/data/wash-agent-swarm/src` checkout:

- Race-enabled tests pass for `internal/swarm`, `internal/workspacemcp` and
  `apps/agentd/be`. Coverage includes 24 concurrent QA replies, revision conflicts,
  package-specific resolution authority, owner-decision gates, persistence/recovery,
  duplicate request receipts, disk-write rollback, preview isolation, failed bulk
  rollback, preserving a separately configured project root, and bounded readback.
- **31 component tests pass** across workspace layout and shared AgentSession,
  including Questions/main-panel placement, live QA updates and member approvals.
- **15 browser tests pass** across workspace and Agent-session suites. New coverage
  drives the injected MCP bridge through bulk preview/setup/retry, stable member
  identities, follow-up resident assignments, teammate approval reaching the adapter,
  linked teammate QA answers, attributed human decisions, browser refresh, revision
  rejection/resolution, package ending and teardown. Test children now use the new
  acknowledgment/reporting/assignment tools; v1 caller compatibility remains covered.
- Agent frontend, isolated multicall and ACP fixture builds pass. Focused Go vet,
  browser-test TypeScript, design-token, interaction and version checks pass.
  The QA screenshot was visually reviewed. A final backend-only adjustment preserves
  visible GUI errors from per-member resume outcomes; focused race/vet pass afterward.

The initial component/browser approval selectors failed because they expected different
Allow text or omitted its keyboard hint; corrected selectors pass. Initial backend
checks exposed/fixed top-level null handling and a duplicated opening QA event.
One unrelated Git lookup check needed GIT_CEILING_DIRECTORIES because `/data` is itself
another repository. These failures were investigated, not treated as successful runs.

Browser tests use deterministic ACP fixtures. Backend recovery is exercised by store
reopening; this is not a new real-provider/full-process crash test. No new real-provider
or whole-product-suite claim is made. Existing real-Claude authentication, full-suite
syslogs and standalone app TypeScript limitations remain as documented above. Scoped
reviewer filesystem capability profiles remain unsupported; permission authority is
unchanged and discovery states that limit. Bulk calls do not bypass human approval.

Evidence under `/data/wash-agent-swarm/test-results/`: `bulk-race-final.log`,
`bulk-components.log`, `bulk-e2e-final.log`, `bulk-build.log`, `bulk-build-final.log`,
`bulk-vet.log`, `bulk-types.log`, `bulk-guards.log`. Screenshot:
`src/e2e/test-results/agent-workspace-bulk-works-5a7df-resh-with-owner-attribution-chromium/workspace-qa.png`.

Updated Redoubt's PROJECT and SWARM instructions for resident package teams, QA
ownership, exact bulk tool usage, refresh/recovery and approvals. Verified that SWARM's
Claims section and all following ledger/evidence sections are byte-for-byte preserved
from the pre-edit file. Its existing BUILD-PLAN changes were untouched. Redoubt edits
remain uncommitted; the matching PROJECT example is included in Wash.

The running Wash desktop was not restarted, installed over or replaced. Builds,
caches, logs and artifacts stayed on `/data`; root free space remained about 6.4 GiB.

## Remove legacy MCP compatibility (2026-09-23)

This supersedes the compatibility notes above: only the twelve current tools are
advertised or accepted. Deleted the old catalog, dispatch cases, incremental plan
mutator, legacy spawn path and wrapper calls. The GUI retains its private human
operations; those are not MCP tools. Current docs and provider probes use the new API.

Protocol tests reject all 21 removed tool names before invocation; a backend test
also rejects direct legacy calls. Migrated browser coverage uses bulk setup/member
configuration, keyed plan/document patches, combined status/waiting and lifecycle
control. It explicitly checks unknown-tool errors through the injected MCP bridge.

This migration exposed a bulk-launch bug: persistence omits an empty settings map,
so inheriting the parent's model could write to a nil map. The launch path now
initializes that map, and the browser flow verifies inherited model settings without
a named profile. The initial two browser failures were fixed; the final run passed
all 15 workspace/Agent-session browser tests. Focused race tests passed for swarm,
MCP, agentd and ACP; focused vet, browser-test TypeScript, isolated multicall build,
design, interaction and version guards passed. No frontend source changed and no
new real-provider test run is claimed.

Logs: `/data/wash-agent-swarm/test-results/remove-legacy-{race,vet,types,build,guards}.log`
and `remove-legacy-e2e-final.log`. Live Wash was not restarted or replaced; all build
artifacts stayed on `/data`. Redoubt's instructions already use the twelve tools.

## Configured live QA Markdown file (2026-09-23)

`workspace_configure.qa_document` registers the Markdown output path/title at setup
or later. Wash writes the full attributed history, event IDs and timestamps through
one serialized atomic writer after updates, including GUI human answers. The Questions
tab shows the configured path and any write error. Backend records remain authoritative;
write errors are reported separately and retried, and missing output regenerates after
backend recovery. Preview writes nothing; detach/teardown preserve the file. Unchanged
records do not rewrite it. Existing unrelated files and symlinks are rejected.

- Focused race tests passed for swarm, MCP and agentd. New coverage checks forty
  concurrent replies without lost or truncated history, actual human answer export,
  missing-file recovery after store reopening, no unchanged-file rewrite, detachment,
  preview isolation, protection of existing files/symlinks, and write-error recovery
  without loss of committed records.
- All 31 focused component tests passed, including QA path/error display and recovery.
- All 15 workspace/Agent-session browser tests passed, including configured file creation,
  live question/answer writes, visible path, refresh and resolved-state output.
- Agent frontend/multicall builds, focused Go vet, browser-test TypeScript and repository
  design/interaction/version guards passed. Browser coverage uses deterministic fixtures;
  no new real-provider or full-process crash validation is claimed.

Evidence: `/data/wash-agent-swarm/test-results/qa-file-{race,components,e2e,build,vet,types,guards}.log`.
Updated Redoubt's PROJECT/SWARM setup to use `docs/WORKSPACE-QA.md` and removed manual-export
instructions. Its Claims section and subsequent evidence ledger remained byte-for-byte
unchanged. The running Wash desktop was not restarted or replaced; artifacts stayed on
`/data`. These notes supersede earlier statements that automatic QA export was absent.
