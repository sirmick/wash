# Shakedown script

You are the orchestrator. Run these steps in order with the `wash_workspace`
tools. Each step says what to do and what should happen. Keep a short log of
every deviation (what you expected, what happened, the tool result); at the
end, report the log and the output of `./check.sh`. Do not fix Wash; do not
work around a failure beyond what the step says. Keep your own turns short:
the members do the work, and you wait (`member_update waiting`, end your
turn) whenever you are waiting for them.

Members: every member uses `model: "small"`. Their instructions below are
complete; do not add to them.

## 1. Set up from the file

`workspace_configure {"from": ".wash/workspace.toml"}`.
Expect: the sidebar appears; the Plan tab says the plan is empty.

`plan_set` three milestones:
M1 "Plan" (template milestone, state active), M2 "Build" (milestone, needs
M1), M3 "Ship" (milestone, needs M2).
Expect: the Plan tab shows three columns, M2 and M3 as dashed sketches.

## 2. Plan the Build milestone

You are in M1, "Plan". In one `plan_set`: M1 state done; A "alpha.txt", B
"beta.txt", C "gamma.txt", each parent M2; C needs A and B. Bodies: A "words/alpha.txt
says alpha", B "words/beta.txt says beta", C "words/gamma.txt says the word the
owner picks".
Expect: M2 now holds A, B, C with edges A→C and B→C; `.wash/plan.toml`
changes.

## 3. Staff A and B

`workspace_configure` members:

- `a-impl`: name "Implementer", node A, role implementer, lifetime resident,
  instructions "Write words/alpha.txt.", task "Create words/alpha.txt
  containing exactly one line: alpha".
- `a-red`: name "Red team", node A, role reviewer, capability reviewer,
  lifetime resident, instructions "Review node A's file when assigned."
- `b-impl`: name "Implementer", node B, role implementer, lifetime resident,
  instructions "Write words/beta.txt.", task "First ask a-impl, on a new QA
  thread, whether words/alpha.txt is lower case: message_send
  {recipient:'a-impl', type:'question', body:'Is words/alpha.txt lower case?',
  qa:{id:'B-case', action:'open', node:'B', title:'Alpha case'}}, then set
  waiting and end your turn. When the answer arrives: start `sleep 45 && touch
  words/.beta-done` with Bash in the background (run_in_background), create
  words/beta.txt containing exactly one line: beta, and report complete."

Expect: A goes active then reported when a-impl reports. B-case appears as
`.wash/qa/B-case.md`; b-impl wakes when a-impl answers (whoever the answer
was addressed to). While the background sleep runs, b-impl shows
"Background: sleep 45…" in the sidebar and on the Plan tab, not idle.

## 4. Review A, round 1 asks for changes

`assignment_update` create for a-red, text "Round 1. Report CHANGES: this
round always asks for changes (a test), as your first line.", with
`wait: {reason: "A review round 1"}` in the same call.
Expect: A stays reported; one wake-up brings the result.

Then a fix round: create for a-impl "Check words/alpha.txt is exactly
'alpha' and report complete; change nothing if it is." Then round 2 for
a-red: "Round 2. Check words/alpha.txt contains exactly the line alpha;
report OK or CHANGES as your first line." (with `wait` again).
Expect: A active again during each round, reported after it.

## 5. Accept A

Run `grep -qx alpha words/alpha.txt; echo $?` yourself, then `plan_accept
{node: "A", gates: [{command: "grep -qx alpha words/alpha.txt", exit_code:
<what it printed>}]}`.
Expect: A done; trailers `Plan-Node: A`, `Gates: …`, and `Reviewed-by: Red
team: OK…` (round 2's first line); no `QA:` line, since no thread is on A;
and the files to stage. Commit with the trailers as the message's last
paragraph, one per line exactly as returned (keep the newlines):
`git add -A && git commit -m "A: alpha.txt" -m "<trailers>"`.
Expect: nothing under `.wash/local` is committed.

## 6. Start C before B is accepted

Staff C: `workspace_configure` member `c-impl`: name "Implementer", node C,
role implementer, lifetime resident, instructions "Write words/gamma.txt."
(no task). Open the thread its questions go on: `message_send {recipient:
"c-impl", type: "progress", body: "The owner's questions go on thread
C-owner.", qa: {id: "C-owner", action: "open", node: "C", title: "Gamma
word"}}`.

Create an assignment for c-impl with this text: "Ask the owner with
decision_request, thread_id C-owner, two questions: (1) id word: 'Which word
goes in words/gamma.txt?', options 'gamma' and 'GAMMA', recommended 'gamma';
(2) id note: 'Anything to add?' (free text). End your turn. When the answers
arrive, write the chosen word as the only line of words/gamma.txt and report
complete."
Expect: refused, because C needs B and B is reported, not done; the error
names B and asks for an override. Create it again with `override:
"shakedown: C starts before B is accepted"`.
Expect: the override is recorded on node C (the Plan tab's detail for C).

## 7. The owner answers

Expect, without doing anything: c-impl's tab and node C show "Needs you";
the sidebar lists the question; a desktop notification appears. The owner
answers in the panel in c-impl's tab. c-impl wakes with the answers as its
next message, writes the file and reports. `.wash/qa/C-owner.md` holds the
questions and the owner's answers word for word.

## 8. A failure on purpose

Create for b-impl: "This assignment tests failure: report it failed with the
body 'failed on purpose'. Change nothing."
Expect: node B shows failed. Then `plan_set` B done (words/beta.txt says beta).

## 9. A member ended with its node active

Create for c-impl: "Ask the owner with decision_request one question: 'Ready
to end?' with options yes and no. End your turn." When the question shows,
do not answer it: `member_control {action: "end", member_ids: ["c-impl"]}`.
Expect: the question disappears; you receive a nudge that node C is active
with nobody on it. Then `plan_set` C done (words/gamma.txt is written).

## 10. Build is finished

Expect a nudge that every node in M2 is done. `plan_accept {node: "M2"}`.
Expect a nudge that M3 (Ship) is next and still a sketch. `plan_set` M3 state
active.

## 11. Status

Answer the owner's standing question "status?" now, in two or three lines,
from `plan_get` only.

## 12. End and resume

`workspace_end {}`: expect a refusal naming M3. `workspace_end {confirm:
true}`. Then set up again from the file: `workspace_configure {"from":
".wash/workspace.toml"}`.
Expect: the plan is back (M3 active); B-case and C-owner come back resolved
or open as they were (resolved ones as headers: `workspace_get {view: "qa",
thread_id: …}` still reads their events). End it again with confirm.

## 13. Report

Run `./check.sh`. Report: its output, your deviation log, and anything that
cost you more turns or tokens than it should have.
