# Redoubt workspace instructions for Wash

This is an example project instruction file for the orchestrator, not a file that
Wash parses. Start an Agent conversation in the project, then ask it to read this
file and configure its workspace through the `wash_workspace` MCP tools.

Read the project's current `docs/SWARM.md`, `docs/BUILD-PLAN.md`, `docs/STATUS.md`,
and the applicable role instructions in `.pi/agents/`. Those documents and the
owner's current instructions govern work. Do not copy this example's package names
into a claim ledger without checking the current project state.

1. Call `setup_workspace` with the project name/root, a keyed progress list derived
   from current deliverables, `max_active: 4`, and `max_members: 16`. Four is the
   limit on active child inbox turns; the orchestrator has its own turn. Idle
   residents remain hosted without spending model turns. The member limit counts
   the orchestrator and every live or failed member until explicitly ended.
2. Register `docs/BUILD-PLAN.md` with `document_set`. Keep the detailed plan a normal
   project file. Update the small progress items with `plan_update_item` as work
   passes its actual acceptance gates.
3. Spawn a resident architect with explicit role instructions, relevant specification
   paths, and `can_spawn: false`. Ask it to resolve specification questions and
   use `decision_request` for genuine owner choices, following the project's
   architect-QA instructions. A role name is not a permission grant.
4. Allocate package worktrees with ordinary development tools. Enforce the kernel
   single-writer rule, check dependencies against merged work, and keep claims
   current in the project ledger. Wash does not enforce these project rules.
5. Spawn ephemeral implementers with their assigned worktree `cwd`, fresh explicit
   instructions, owned paths, tests and acceptance criteria, and a `task`. They
   must acknowledge inbox messages, ask the architect when blocked, call
   `member_wait` and finish the turn while awaiting answers, and explicitly report
   through `assignment_complete` or `assignment_fail`.
6. An answer may arrive before the waiting turn ends; Wash retains it. Do not poll
   inboxes with repeated model turns. Include `reply_to` and `assignment_id` when
   replying about a particular question or assignment. Use `request_id` on retried
   sends/assignments to prevent duplicate acceptance.
7. Launch ephemeral reviewers for the project's defensive, simplification and
   code/documentation reviews. Their instructions must be read-only. Evaluate
   their findings before accepting a package; completing an agent assignment is
   not proof that a package is ready to merge.
8. Integrate one package at a time, rebase and retest, record evidence, and apply
   all of Redoubt's acceptance gates. Only then mark that progress item `done`.
   `member_set_status` reports short human-readable activity and emoji; it does
   not change assignment or runtime state. Use `flash_message` for a milestone
   that should appear on the desktop even when the Agent window is hidden.
9. A stopped member retains pending mail. Resume it explicitly. Following a
   service restart, resume the orchestrator from Agent History, then resume
   members deliberately. Reconcile any `uncertain` message before calling
   `message_retry`; its side effects may already have happened.
10. When asked to dismantle the team, call `teardown_workspace`. The lead
    conversation and all project files/worktrees remain. Child sessions end,
    final state stays in Wash's workspace archive, and the sidebar disappears.

Example progress item (choose actual package IDs from the project):

```json
{"id":"K5","text":"Timer implementation and acceptance","emoji":"⏳","state":"pending"}
```

Incremental update:

```json
{"id":"K5","state":"done","emoji":"✅","expected_revision":1}
```

Use the revision returned by `plan_get`, not this illustrative value. A stale
revision fails without changing the plan. For a resident's next task, use
`assignment_create` with its existing member ID instead of spawning another copy.
