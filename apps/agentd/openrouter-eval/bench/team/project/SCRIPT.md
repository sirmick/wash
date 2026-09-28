# Team benchmark

You are the orchestrator. This module has three small packages of work, each
stubbed in its own file with a doc comment that is the whole specification:

| Node | File | Work |
|---|---|---|
| INI | `ini.go` | `ParseINI` |
| LRU | `lru.go` | the `LRU` cache |
| WRAP | `wrap.go` | `Wrap` |

Run the work with the `wash_workspace` tools, as a team:

1. `workspace_configure {"from": ".wash/workspace.toml"}`. `plan_set` a
   milestone BUILD with the three nodes INI, LRU, WRAP under it (template
   package; no needs between them), and a milestone SHIP that needs BUILD.
2. For each node, one implementer (`model: "coding"`, role implementer,
   lifetime resident, on its node) and one reviewer (`model: "small"`, role
   reviewer, `capability: "reviewer"`, lifetime resident, on its node).
   Implementers write only their own file, and may add a new test file of
   their own; nobody changes an existing `_test.go` file. You do not write
   code yourself.
3. When an implementer reports, assign its node's reviewer a review against
   the doc comment: the first line of the result is `OK` or
   `CHANGES: <what>`. On CHANGES, send the implementer the reviewer's points
   and review again: at most two rounds of changes.
4. Accept a node when its reviewer says OK and `go test ./...` passes: run
   the tests yourself, `plan_accept` with the gate and its exit code, and
   commit with the trailers as the message's last paragraph, one per line
   exactly as returned:
   `git add -A && git commit -m "<node>: <file>" -m "<trailers>"`.
5. When all three are accepted, set SHIP active, end the members with
   `member_control`, then run `./check.sh` and report its output.

Do not ask the owner anything: decide yourself, and say what you decided in
your report. Commit with `git -c user.name=team -c user.email=team@localhost`.
