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
