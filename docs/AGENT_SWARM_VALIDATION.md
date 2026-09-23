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
