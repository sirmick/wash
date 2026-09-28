# Instructions

- Following Playwright test failed.
- Explain why, be concise, respect Playwright best practices.
- Provide a snippet of code with the fix, if possible.

# Test info

- Name: agent-shakedown-live.spec.ts >> the shakedown, live
- Location: tests/agent-shakedown-live.spec.ts:30:1

# Error details

```
Error: the shakedown did not finish in time

expect(received).toBe(expected) // Object.is equality

Expected: false
Received: true
```

# Page snapshot

```yaml
- generic [ref=e2]:
  - generic [ref=e4]:
    - generic:
      - generic:
        - generic: furnace.home.arpa
        - generic: mick
        - generic: · mick
      - generic: 32 cores · 92 GB
      - generic:
        - generic: enp12s0 172.16.16.58 2601:646:8781:5e30:3a71:d7a9:dfb6:f00e
      - generic:
        - generic: wash-router v0.16.0
    - generic [ref=e5]:
      - generic [ref=e6]:
        - generic "Current user @ this session's host" [ref=e7]: mick@furnace
        - button "›" [ref=e8] [cursor=pointer]
      - generic [ref=e9]:
        - generic [ref=e10]:
          - button "▶ Viewport" [expanded] [ref=e11] [cursor=pointer]:
            - generic [ref=e12]: ▶
            - img [ref=e14]
            - generic [ref=e16]: Viewport
          - generic [ref=e18]:
            - generic "Agent" [ref=e22] [cursor=pointer]
            - generic [ref=e31]: Ctrl+Alt+arrows to pan
        - button "▶ About" [ref=e33] [cursor=pointer]:
          - generic [ref=e34]: ▶
          - img [ref=e36]
          - generic [ref=e38]: About
        - generic [ref=e39]:
          - button "▶ Notifications 2" [expanded] [ref=e40] [cursor=pointer]:
            - generic [ref=e41]: ▶
            - img [ref=e43]
            - generic [ref=e45]: Notifications
            - generic [ref=e46]: "2"
          - generic [ref=e48]:
            - generic "From com.wash.agentd — click to mark read" [ref=e49] [cursor=pointer]:
              - generic [ref=e50]:
                - generic [ref=e51]: Shakedown needs you
                - generic [ref=e52]: now
              - generic [ref=e53]: Ready to end?
              - generic [ref=e54]: agentd
            - generic "From com.wash.agentd — click to mark read" [ref=e55] [cursor=pointer]:
              - generic [ref=e56]:
                - generic [ref=e57]: Shakedown needs you
                - generic [ref=e58]: now
              - generic [ref=e59]: Which word goes in words/gamma.txt?
              - generic [ref=e60]: agentd
            - button "clear all" [ref=e61] [cursor=pointer]
        - button "▶ Timeline" [ref=e63] [cursor=pointer]:
          - generic [ref=e64]: ▶
          - img [ref=e66]
          - generic [ref=e68]: Timeline
        - button "▶ Bulk Ops" [ref=e70] [cursor=pointer]:
          - generic [ref=e71]: ▶
          - img [ref=e73]
          - generic [ref=e75]: Bulk Ops
        - button "▶ Privilege" [ref=e77] [cursor=pointer]:
          - generic [ref=e78]: ▶
          - img [ref=e80]
          - generic [ref=e82]: Privilege
        - button "▶ Network" [ref=e84] [cursor=pointer]:
          - generic [ref=e85]: ▶
          - img [ref=e87]
          - generic [ref=e89]: Network
        - button "▶ Remote" [ref=e91] [cursor=pointer]:
          - generic [ref=e92]: ▶
          - img [ref=e94]
          - generic [ref=e96]: Remote
        - button "▶ Audio" [ref=e98] [cursor=pointer]:
          - generic [ref=e99]: ▶
          - img [ref=e101]
          - generic [ref=e103]: Audio
        - button "▶ Agents" [ref=e105] [cursor=pointer]:
          - generic [ref=e106]: ▶
          - img [ref=e108]
          - generic [ref=e110]: Agents
        - button "▶ Clipboard" [ref=e112] [cursor=pointer]:
          - generic [ref=e113]: ▶
          - img [ref=e115]
          - generic [ref=e117]: Clipboard
    - generic [ref=e118]:
      - button "wash" [ref=e119] [cursor=pointer]:
        - img "wash" [ref=e120]
      - button "Search apps (Ctrl+Space)" [ref=e121] [cursor=pointer]:
        - img [ref=e122]
      - button "Agent" [ref=e127] [cursor=pointer]:
        - img [ref=e128]
        - generic [ref=e130]: Agent
      - button "Screenshot" [ref=e131] [cursor=pointer]:
        - img [ref=e132]
      - button "Toggle sidebar (Ctrl+Alt+S)" [ref=e135] [cursor=pointer]:
        - img [ref=e136]
      - generic [ref=e139]: 16:02
  - generic [ref=e140]:
    - generic [ref=e141]:
      - img [ref=e142]
      - generic [ref=e144]: Agent
      - button "Minimize window" [ref=e145] [cursor=pointer]:
        - img [ref=e146]
      - button "Maximize window" [ref=e147] [cursor=pointer]:
        - img [ref=e148]
      - button "Close window" [ref=e153] [cursor=pointer]:
        - img [ref=e154]
    - generic [ref=e159]:
      - generic [ref=e160]:
        - button "File" [ref=e161] [cursor=pointer]
        - button "Edit" [ref=e162] [cursor=pointer]
        - button "Session" [ref=e163] [cursor=pointer]
      - generic [ref=e165]:
        - generic [ref=e166]:
          - tablist "Workspace views" [ref=e167]:
            - tab "Conversation" [active] [selected] [ref=e168] [cursor=pointer]:
              - generic [ref=e169]: Conversation
            - tab "Plan" [ref=e170] [cursor=pointer]:
              - generic [ref=e171]: Plan
            - tab "C · Implementer ● Close tab" [ref=e172] [cursor=pointer]:
              - generic [ref=e173]: C · Implementer ●
              - button "Close tab" [ref=e174]: ×
          - tabpanel "Conversation" [ref=e175]:
            - generic [ref=e176]:
              - generic [ref=e177]:
                - generic [ref=e180]: Auto-approval (yolo) is ON — wash will approve tool requests without asking. (launched with auto-approve on)
                - generic [ref=e181]: Read SCRIPT.md and run the shakedown. Commit with git -c user.name=shakedown -c user.email=shakedown@localhost.
                - generic [ref=e184]: Let me read SCRIPT.md first.
                - button "read SCRIPT.md" [ref=e186] [cursor=pointer]:
                  - generic [ref=e187]: read
                  - generic [ref=e188]: SCRIPT.md
                - generic [ref=e192]: "Let me start the shakedown. First, step 1: workspace_configure from file. Let me also check what's in the workspace directory."
                - button "execute ls -la && git log --oneline -5 2>/dev/null; cat .wash/workspace.toml 2>/dev/null wash-shakedown-live-3gIwUt" [ref=e194] [cursor=pointer]:
                  - generic [ref=e195]: execute
                  - generic [ref=e196]: ls -la && git log --oneline -5 2>/dev/null; cat .wash/workspace.toml 2>/dev/null
                  - generic [ref=e197]: wash-shakedown-live-3gIwUt
                - generic [ref=e199]:
                  - generic [ref=e200]: ✓ Auto-approved
                  - generic "Bash" [ref=e201]
                  - generic "ls -la && git log --oneline -5 2>/dev/null; cat .wash/workspace.toml 2>/dev/null" [ref=e202]
                  - generic "yolo" [ref=e203]
                - generic [ref=e206]: Let me set up the workspace from the file. Also note there's a words/ dir. Let me check check.sh briefly and .wash contents.
                - button "read check.sh" [ref=e208] [cursor=pointer]:
                  - generic [ref=e209]: read
                  - generic [ref=e210]: check.sh
                - button "execute ls -la .wash words && git status --short --untracked-files=all wash-shakedown-live-3gIwUt" [ref=e213] [cursor=pointer]:
                  - generic [ref=e214]: execute
                  - generic [ref=e215]: ls -la .wash words && git status --short --untracked-files=all
                  - generic [ref=e216]: wash-shakedown-live-3gIwUt
                - generic [ref=e218]:
                  - generic [ref=e219]: ✓ Auto-approved
                  - generic "Bash" [ref=e220]
                  - generic "ls -la .wash words && git status --short --untracked-files=all" [ref=e221]
                  - generic "yolo" [ref=e222]
                - generic [ref=e225]: "Now step 1: workspace_configure from file. Set up a deviation log file too. Let me start."
                - generic [ref=e228]: "Starting the shakedown. Step 1: configure from the file."
                - button "other wash_workspace_workspace_configure" [ref=e230] [cursor=pointer]:
                  - generic [ref=e231]: other
                  - generic [ref=e232]: wash_workspace_workspace_configure
                - generic [ref=e236]: "Workspace configured. Now plan_set three milestones: M1 \"Plan\" (milestone, active), M2 \"Build\" (needs M1), M3 \"Ship\" (needs M2)."
                - button "other wash_workspace_plan_set" [ref=e238] [cursor=pointer]:
                  - generic [ref=e239]: other
                  - generic [ref=e240]: wash_workspace_plan_set
                - generic [ref=e243]:
                  - generic [ref=e244]: "Step 2: Plan the Build milestone. In one plan_set: M1 state done; A \"alpha.txt\", B \"beta.txt\", C \"gamma.txt\", each parent M2; C needs A and B. Bodies:"
                  - generic [ref=e245]:
                    - generic [ref=e246]:
                      - generic [ref=e247]: •
                      - generic [ref=e248]: A "words/alpha.txt says alpha"
                    - generic [ref=e249]:
                      - generic [ref=e250]: •
                      - generic [ref=e251]: B "words/beta.txt says beta"
                    - generic [ref=e252]:
                      - generic [ref=e253]: •
                      - generic [ref=e254]: C "words/gamma.txt says the word the owner picks"
                - button "other wash_workspace_plan_set" [ref=e256] [cursor=pointer]:
                  - generic [ref=e257]: other
                  - generic [ref=e258]: wash_workspace_plan_set
                - generic [ref=e261]:
                  - generic [ref=e262]: "Step 3: Staff A and B with workspace_configure members. Per the script:"
                  - generic [ref=e263]:
                    - generic [ref=e264]:
                      - generic [ref=e265]: •
                      - generic [ref=e266]:
                        - code [ref=e267]: a-impl
                        - text: ": name \"Implementer\", node A, role implementer, lifetime resident, instructions \"Write words/alpha.txt.\", task \"Create words/alpha.txt containing exactly one line: alpha\". Model \"small\" for every member."
                    - generic [ref=e268]:
                      - generic [ref=e269]: •
                      - generic [ref=e270]:
                        - code [ref=e271]: a-red
                        - text: ": name \"Red team\", node A, role reviewer, capability reviewer, lifetime resident, instructions \"Review node A's file when assigned.\""
                    - generic [ref=e272]:
                      - generic [ref=e273]: •
                      - generic [ref=e274]:
                        - code [ref=e275]: b-impl
                        - text: ": name \"Implementer\", node B, role implementer, lifetime resident, instructions \"Write words/beta.txt.\", task \"First ask a-impl, on a new QA thread, whether words/alpha.txt is lower case: message"
                        - emphasis [ref=e276]: "send {recipient:'a-impl', type:'question', body:'Is words/alpha.txt lower case?', qa:{id:'B-case', action:'open', node:'B', title:'Alpha case'}}, then set waiting and end your turn. When the answer arrives: start `sleep 45 && touch words/.beta-done` with Bash in the background (run"
                        - text: "in_background), create words/beta.txt containing exactly one line: beta, and report complete.\""
                  - generic [ref=e277]: All model "small". I'll configure all three members in one call.
                - generic [ref=e280]: "Staffing A and B (step 3):"
                - button "other wash_workspace_workspace_configure" [ref=e282] [cursor=pointer]:
                  - generic [ref=e283]: other
                  - generic [ref=e284]: wash_workspace_workspace_configure
                - generic [ref=e287]:
                  - generic [ref=e288]: "All three launched. Now I wait: a-impl will create alpha.txt and report; b-impl will open QA thread B-case and wait; the answer will come via a-impl presumably answering (b-impl asks a-impl the question). Then when a-impl answers, b-impl wakes, does the background sleep, writes beta.txt, reports complete."
                  - generic [ref=e289]: "Actually per script flow: b-impl asks a-impl the question on thread B-case. a-impl answers (addressed to... b-impl? Whoever). Then b-impl wakes, starts the background sleep 45, writes beta.txt, reports complete."
                  - generic [ref=e290]:
                    - text: I should wait now. Set waiting with member
                    - emphasis [ref=e291]: update and end turn. Wait for what reason? I'm waiting for the members to do their work. I'll set waiting with a reason. The member
                    - text: update waiting has reason and until
                    - emphasis [ref=e292]: assignments. I haven't created assignments, so just plain waiting. But plain waiting "then END YOUR TURN; it clears your status unless this call sets one. waiting.until
                    - text: assignments (ones you created) holds their results...".
                  - generic [ref=e293]: I'll just set waiting with a reason "a-impl and b-impl are doing their initial work".
                - generic [ref=e296]: All three members launched. Waiting for a-impl (alpha.txt) and b-impl (opens the B-case question, then waits).
                - button "other wash_workspace_member_update" [ref=e298] [cursor=pointer]:
                  - generic [ref=e299]: other
                  - generic [ref=e300]: wash_workspace_member_update
                - generic [ref=e304]: Waiting for members.
                - button "other wash_workspace_workspace_get" [ref=e306] [cursor=pointer]:
                  - generic [ref=e307]: other
                  - generic [ref=e308]: wash_workspace_workspace_get
                - generic [ref=e312]: Members are working. I should end my turn now and wait for messages. The system says I'll be woken. Let me end turn without further polling. I already set waiting. Just stop here.
                - generic [ref=e315]: Members are running. Ending turn to wait for their results.
                - generic [ref=e317]:
                  - generic [ref=e318]: Implementer (babcbd4951ae2b4c9142d96f093ff137) · result
                  - generic [ref=e320]: "Created words/alpha.txt containing exactly one line: alpha (assignment 7c31a8117c7c82dd6a3f2540b8d1e271)."
                - generic [ref=e322]:
                  - generic [ref=e323]: "a-impl reported complete: alpha.txt created. Node A should go reported. Now I'm still waiting on b-impl: it should have opened B-case QA thread and be waiting for a-impl's answer. But wait — b-impl asked a-impl the question; a-impl should answer it. a-impl's result came in; did a-impl answer the B-case question? The question was addressed to a-impl. a-impl may have answered already (the answer reaches b-impl, waking it). Or a-impl may answer in its turn."
                  - generic [ref=e324]: "Let me check the workspace state: check if B-case.md exists and the QA status. Also verify alpha.txt. Let me look."
                - button "execute cat words/alpha.txt; ls -la .wash/qa/ 2>/dev/null; ls -la words/ wash-shakedown-live-3gIwUt" [ref=e326] [cursor=pointer]:
                  - generic [ref=e327]: execute
                  - generic [ref=e328]: cat words/alpha.txt; ls -la .wash/qa/ 2>/dev/null; ls -la words/
                  - generic [ref=e329]: wash-shakedown-live-3gIwUt
                - generic [ref=e331]:
                  - generic [ref=e332]: ✓ Auto-approved
                  - generic "Bash" [ref=e333]
                  - generic "cat words/alpha.txt; ls -la .wash/qa/ 2>/dev/null; ls -la words/" [ref=e334]
                  - generic "yolo" [ref=e335]
                - button "other wash_workspace_workspace_get" [ref=e337] [cursor=pointer]:
                  - generic [ref=e338]: other
                  - generic [ref=e339]: wash_workspace_workspace_get
                - generic [ref=e342]:
                  - generic [ref=e343]: alpha.txt is created correctly. But b-impl hasn't opened the B-case thread yet — QA threads is empty and .wash/qa is empty. b-impl is working; it may still be composing its message. I should wait for b-impl. b-impl will set waiting after opening the question. I'm waiting for b-impl's question to appear (then a-impl answers, etc.).
                  - generic [ref=e344]: "Note: a-impl's result arrived but I haven't reported/accepted anything. Node A should now be \"reported\". I just need to wait for b-impl. Set waiting and end turn again? I'm already in waiting status. Just end turn."
                - generic [ref=e347]: alpha.txt is correct. B-case thread not open yet — b-impl is still working on its question. Ending turn to wait.
                - button "other wash_workspace_member_update" [ref=e349] [cursor=pointer]:
                  - generic [ref=e350]: other
                  - generic [ref=e351]: wash_workspace_member_update
                - generic [ref=e355]: Waiting for b-impl's QA question.
                - generic [ref=e357]:
                  - generic [ref=e358]: Implementer (8893eb66d7d8ef20ec5c4689a36551e0) · result
                  - generic [ref=e360]:
                    - text: "Answer arrived: words/alpha.txt is lower case. Started"
                    - code [ref=e361]: sleep 45 && touch words/.beta-done
                    - text: "in background; created words/beta.txt containing exactly one line: beta."
                - generic [ref=e363]:
                  - generic [ref=e364]: "b-impl completed — it got the answer, started the background sleep, and created beta.txt. Interesting: I expected b-impl to set waiting after opening the question and then wake on the answer; it seems the whole flow completed in one go (the answer already arrived from a-impl before b-impl's turn ended, or the QA thread answered quickly). Let me check state: B-case thread should exist, beta.txt should exist, and node B reported."
                  - generic [ref=e365]: "Note deviation: I never saw b-impl pause waiting; it all happened in one result. That's fine — maybe expected since a-impl answered quickly. Let me verify on disk and via QA view."
                - button "execute cat words/beta.txt; ls -la words/ .wash/qa/; cat .wash/qa/B-case.md 2>/dev/null wash-shakedown-live-3gIwUt" [ref=e367] [cursor=pointer]:
                  - generic [ref=e368]: execute
                  - generic [ref=e369]: cat words/beta.txt; ls -la words/ .wash/qa/; cat .wash/qa/B-case.md 2>/dev/null
                  - generic [ref=e370]: wash-shakedown-live-3gIwUt
                - generic [ref=e372]:
                  - generic [ref=e373]: ✓ Auto-approved
                  - generic "Bash" [ref=e374]
                  - generic "cat words/beta.txt; ls -la words/ .wash/qa/; cat .wash/qa/B-case.md 2>/dev/null" [ref=e375]
                  - generic "yolo" [ref=e376]
                - button "other wash_workspace_plan_get" [ref=e378] [cursor=pointer]:
                  - generic [ref=e379]: other
                  - generic [ref=e380]: wash_workspace_plan_get
                - generic [ref=e383]:
                  - generic [ref=e384]: "Interesting: b-impl's result says \"Answer arrived\" — but the B-case.md answer author is \"babcbd4951ae2b4c91428d96f...\" wait no, the answer author is a-impl's id (babcbd...7137 = a-impl). Good — a-impl answered on the thread, b-impl woke and completed. The background sleep was started and words/.beta-done already exists — so the sleep finished (or it ran 45s: created_at 22:46 question, b-impl result at 15:47... alpha.txt at 15:22... hmm the timestamps: b-impl asked at 22:46:50Z (15:46 local), answered 22:46:55, and beta-done touched 15:47. So sleep was started around 15:46:55 + 45s = 15:47:40. It exists now)."
                  - generic [ref=e385]: Wait — actually the sleep finished already? The file words/.beta-done exists (0 bytes, 15:47). The current time... b-impl's result message created_at 1790549267773 = 15:47:47 local. So sleep done. Good.
                  - generic [ref=e386]: "Deviation note: the script says B-case appears as .wash/qa/B-case.md ✓; b-impl wakes when a-impl answers ✓ (it did — \"Answer arrived\"); \"While the background sleep runs, b-impl shows Background: sleep 45… in the sidebar\" — I didn't observe that since it all happened while I was waiting; minor, I can note it. Actually I could check workspace_get now but sleep is done. I'll log as \"not observed\" deviation."
                  - generic [ref=e387]: Also B-case thread is still "open" — the thread was never resolved. The script doesn't ask to resolve it. check.sh only needs B-case.md to exist with "lower case". Fine.
                  - generic [ref=e388]: "Now step 4: Review A, round 1. assignment_update create for a-red: text \"Round 1. Report CHANGES: this round always asks for changes (a test), as your first line.\" with wait: {reason: \"A review round 1\"} in the same call."
                - generic [ref=e391]: "Both A and B are reported; B-case thread holds the question and a-impl's answer. Step 4: review round 1."
                - button "other wash_workspace_assignment_update" [ref=e393] [cursor=pointer]:
                  - generic [ref=e394]: other
                  - generic [ref=e395]: wash_workspace_assignment_update
                - generic [ref=e398]:
                  - generic [ref=e399]: "I made a typo in the assignment text: \"as your first line\" got cut? Let me look: I wrote \"Round 1. Report CHANGES: this round always asks for changes (a test), as your first line.\" — actually that matches the script. Good."
                  - generic [ref=e400]: Now waiting for the result. End turn.
                - generic [ref=e403]: Round 1 assigned to a-red; waiting for the result. Ending turn.
                - button "other wash_workspace_member_update" [ref=e405] [cursor=pointer]:
                  - generic [ref=e406]: other
                  - generic [ref=e407]: wash_workspace_member_update
                - generic [ref=e411]: Waiting for round 1 review.
                - generic [ref=e413]:
                  - generic [ref=e414]: Red team (9f6fbe32c3a6253a5cf3f66c62f2da38) · result
                  - generic [ref=e416]: "CHANGES: words/alpha.txt contains only 'alpha' with no trailing newline and no header context; for a package file, add a one-line title/description and end with a newline so the file is self-describing."
                - generic [ref=e418]:
                  - generic [ref=e419]: "Round 1 result: CHANGES as expected (the reviewer asks for changes as a test). Node A reported. Now the fix round: create for a-impl \"Check words/alpha.txt is exactly 'alpha' and report complete; change nothing if it is.\" This doesn't need wait per script — script says \"Then a fix round: create for a-impl ... Then round 2 for a-red ... (with wait again)\". So fix round without wait, then round 2 with wait. But I might as well chain: create fix assignment, wait for it, then create round 2 with wait."
                  - generic [ref=e420]: "Actually can I create both in one call with wait on both? The script says fix round then round 2 with wait. To be safe and follow the script, I could create the fix assignment first (no wait), then round 2 (with wait). But round 2 should only start after the fix is done. Hmm, \"Expect: A active again during each round, reported after it.\" So sequential. But the wait mechanism: if I create both at once, a-red might review before the fix is applied. Better sequential: create fix assignment with wait (the script only says \"with wait again\" for round 2, but I need to wait for the fix anyway to sequence properly). Actually the script for the fix round doesn't mention wait. But if I don't wait, I'd have to end my turn and get woken by the result message anyway (results come as inbox messages). That's the same thing. Let me create fix round with wait for it, then round 2."
                  - generic [ref=e421]: "Actually simpler: create the fix assignment without wait, end turn, get woken by its result, then create round 2 with wait. That follows the script most literally. But that costs extra turns. The script says keep your own turns short. Using wait on the fix round is fine and equivalent. I'll do: create fix assignment with wait on it (reason \"A fix round\"). Then round 2 with wait."
                - generic [ref=e424]: "Round 1: CHANGES (as scripted). Fix round for a-impl:"
                - button "other wash_workspace_assignment_update" [ref=e426] [cursor=pointer]:
                  - generic [ref=e427]: other
                  - generic [ref=e428]: wash_workspace_assignment_update
                - button "other wash_workspace_member_update" [ref=e431] [cursor=pointer]:
                  - generic [ref=e432]: other
                  - generic [ref=e433]: wash_workspace_member_update
                - generic [ref=e437]: Waiting for the fix round.
                - generic [ref=e439]:
                  - generic [ref=e440]: Implementer (babcbd4951ae2b4c9142d96f093ff137) · result
                  - generic [ref=e442]: Verified words/alpha.txt contains exactly 'alpha' (od shows 'a l p h a \n'); changed nothing (assignment cb7dce4bd76f90d4f5f8fec22cf25782).
                - generic [ref=e445]: "Fix round done (no change needed). Round 2 for a-red:"
                - button "other wash_workspace_assignment_update" [ref=e447] [cursor=pointer]:
                  - generic [ref=e448]: other
                  - generic [ref=e449]: wash_workspace_assignment_update
                - button "other wash_workspace_member_update" [ref=e452] [cursor=pointer]:
                  - generic [ref=e453]: other
                  - generic [ref=e454]: wash_workspace_member_update
                - generic [ref=e458]: Waiting for round 2.
                - generic [ref=e460]:
                  - generic [ref=e461]: Red team (9f6fbe32c3a6253a5cf3f66c62f2da38) · result
                  - generic [ref=e463]: OK
                - generic [ref=e466]: "Round 2: OK. Step 5 — verify the gate and accept A:"
                - button "execute grep -qx alpha words/alpha.txt; echo $? wash-shakedown-live-3gIwUt" [ref=e468] [cursor=pointer]:
                  - generic [ref=e469]: execute
                  - generic [ref=e470]: grep -qx alpha words/alpha.txt; echo $?
                  - generic [ref=e471]: wash-shakedown-live-3gIwUt
                - generic [ref=e473]:
                  - generic [ref=e474]: ✓ Auto-approved
                  - generic "Bash" [ref=e475]
                  - generic "grep -qx alpha words/alpha.txt; echo $?" [ref=e476]
                  - generic "yolo" [ref=e477]
                - button "other wash_workspace_plan_accept" [ref=e479] [cursor=pointer]:
                  - generic [ref=e480]: other
                  - generic [ref=e481]: wash_workspace_plan_accept
                - 'button "execute git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -m \"A: alpha.txt\" -m \"Plan-Node: A Gates: grep -qx alpha words/alpha.txt 0 Reviewed-by: Red team: OK\" && git show --stat --format=full HEAD | head -30 && git ls-files | grep ''^\\.wash/local'' || true wash-shakedown-live-3gIwUt" [ref=e484] [cursor=pointer]':
                  - generic [ref=e485]: execute
                  - generic [ref=e486]: "git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -m \"A: alpha.txt\" -m \"Plan-Node: A Gates: grep -qx alpha words/alpha.txt 0 Reviewed-by: Red team: OK\" && git show --stat --format=full HEAD | head -30 && git ls-files | grep '^\\.wash/local' || true"
                  - generic [ref=e487]: wash-shakedown-live-3gIwUt
                - generic [ref=e489]:
                  - generic [ref=e490]: ✓ Auto-approved
                  - generic "Bash" [ref=e491]
                  - generic "git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commi …" [ref=e492]
                  - generic "yolo" [ref=e493]
                - generic [ref=e495]:
                  - generic [ref=e496]:
                    - text: "Committed. Note:"
                    - code [ref=e497]: git add -A
                    - text: staged words/.beta-done too — the script said commit with
                    - code [ref=e498]: git add -A
                    - text: ", so that's per the script. Nothing under .wash/local committed ✓."
                  - generic [ref=e499]: "Step 6: Start C before B is accepted. Staff c-impl: name \"Implementer\", node C, role implementer, lifetime resident, instructions \"Write words/gamma.txt.\" (no task), model small. Then message_send progress to c-impl opening thread C-owner. Then create assignment — expect refusal naming B, then create with override."
                - generic [ref=e502]:
                  - text: A accepted and committed with trailers (
                  - code [ref=e503]: .wash/local
                  - text: "untouched). Step 6 — staff C and open thread C-owner:"
                - button "other wash_workspace_workspace_configure" [ref=e505] [cursor=pointer]:
                  - generic [ref=e506]: other
                  - generic [ref=e507]: wash_workspace_workspace_configure
                - button "other wash_workspace_message_send" [ref=e510] [cursor=pointer]:
                  - generic [ref=e511]: other
                  - generic [ref=e512]: wash_workspace_message_send
                - generic [ref=e516]: "The message_send tool has schema issues — I included both top-level fields and messages. Let me use only the messages array (the qa field goes inside each message per schema). Looking at schema: each message has recipient, type, body, qa. Top-level also has recipient/type/body/qa. I sent both — conflict. Let me send only via messages array."
                - button "other wash_workspace_message_send" [ref=e518] [cursor=pointer]:
                  - generic [ref=e519]: other
                  - generic [ref=e520]: wash_workspace_message_send
                - generic [ref=e524]: Thread C-owner opened. Now create the assignment for c-impl — expect a refusal because C needs B and B is reported, not done.
                - generic [ref=e527]: "Thread C-owner opened. Now the C assignment — expecting a refusal naming B:"
                - button "other wash_workspace_assignment_update" [ref=e529] [cursor=pointer]:
                  - generic [ref=e530]: other
                  - generic [ref=e531]: wash_workspace_assignment_update
                - generic [ref=e535]: "Refused as expected, naming B (reported). Now create again with override \"shakedown: C starts before B is accepted\". The override field goes on the update."
                - generic [ref=e538]: "Refused as expected (names B). Retrying with the override:"
                - button "other wash_workspace_assignment_update" [ref=e540] [cursor=pointer]:
                  - generic [ref=e541]: other
                  - generic [ref=e542]: wash_workspace_assignment_update
                - generic [ref=e545]:
                  - generic [ref=e546]: "Assignment created with the override. Now step 7: the owner answers. Expect c-impl's tab shows \"Needs you\", sidebar lists the question, a desktop notification appears — all without me doing anything. The owner (the user of this session) answers in the panel. I should verify the override is recorded on node C, then wait — the decision_request is pending with the owner. I should end my turn and wait for... the owner's answer wakes c-impl, not me. c-impl will write gamma.txt and report."
                  - generic [ref=e547]: Let me verify the override is recorded in plan.toml, then set waiting and end turn. The owner needs to answer the decision request — that appears as a pending decision. I'll just end my turn; the notification goes to the owner.
                - generic [ref=e550]: "Assignment created with override. Verifying the override is recorded on C:"
                - button "execute grep -n -A3 \"overrides\\|C \" .wash/plan.toml | head -30 wash-shakedown-live-3gIwUt" [ref=e552] [cursor=pointer]:
                  - generic [ref=e553]: execute
                  - generic [ref=e554]: grep -n -A3 "overrides\|C " .wash/plan.toml | head -30
                  - generic [ref=e555]: wash-shakedown-live-3gIwUt
                - generic [ref=e557]:
                  - generic [ref=e558]: ✓ Auto-approved
                  - generic "Bash" [ref=e559]
                  - generic "grep -n -A3 \"overrides\\|C \" .wash/plan.toml | head -30" [ref=e560]
                  - generic "yolo" [ref=e561]
                - generic [ref=e564]: Override recorded ✓. Now I wait for the owner to answer the decision request (step 7). The decision_request from c-impl will appear to the owner. I end my turn with waiting status. c-impl will wake with the answers and complete.
                - generic [ref=e567]: "Override recorded on C. Step 7: waiting for the owner to answer c-impl's questions."
                - button "other wash_workspace_member_update" [ref=e569] [cursor=pointer]:
                  - generic [ref=e570]: other
                  - generic [ref=e571]: wash_workspace_member_update
                - generic [ref=e575]: Ending turn — the owner's answer will wake c-impl to finish gamma.txt.
                - generic [ref=e577]:
                  - generic [ref=e578]: 2 messages
                  - generic [ref=e579]:
                    - generic [ref=e580]: wash · note
                    - generic [ref=e581]: "Implementer asked the owner and waits for the answer: Which word goes in words/gamma.txt?"
                    - generic [ref=e582]: Implementer (f9ef67743fdc82ecc2a163735e7ddce1) · result
                    - generic [ref=e583]: Wrote words/gamma.txt with the single line 'gamma' (lowercase, per owner's answers).
                - generic [ref=e586]: c-impl asked the owner, the owner answered (in the panel — I didn't see the answer, but c-impl reports writing gamma.txt per the owner's answers). Verify the file and the C-owner.md contents.
                - button "execute cat words/gamma.txt; echo ---; cat .wash/qa/C-owner.md wash-shakedown-live-3gIwUt" [ref=e588] [cursor=pointer]:
                  - generic [ref=e589]: execute
                  - generic [ref=e590]: cat words/gamma.txt; echo ---; cat .wash/qa/C-owner.md
                  - generic [ref=e591]: wash-shakedown-live-3gIwUt
                - generic [ref=e593]:
                  - generic [ref=e594]: ✓ Auto-approved
                  - generic "Bash" [ref=e595]
                  - generic "cat words/gamma.txt; echo ---; cat .wash/qa/C-owner.md" [ref=e596]
                  - generic "yolo" [ref=e597]
                - generic [ref=e599]:
                  - generic [ref=e600]: "Owner answered: gamma, note \"lower case, like alpha and beta\". c-impl wrote gamma.txt. All per expectations."
                  - generic [ref=e601]: "Step 8: A failure on purpose. Create for b-impl: \"This assignment tests failure: report it failed with the body 'failed on purpose'. Change nothing.\" Expect node B shows failed. Then plan_set B done (words/beta.txt says beta)."
                - generic [ref=e604]: "Owner answered (gamma); gamma.txt written. Step 8 — the deliberate failure on B:"
                - button "other wash_workspace_assignment_update" [ref=e606] [cursor=pointer]:
                  - generic [ref=e607]: other
                  - generic [ref=e608]: wash_workspace_assignment_update
                - button "other wash_workspace_member_update" [ref=e611] [cursor=pointer]:
                  - generic [ref=e612]: other
                  - generic [ref=e613]: wash_workspace_member_update
                - generic [ref=e617]: Waiting for b-impl's failure report.
                - generic [ref=e619]:
                  - generic [ref=e620]: Implementer (8893eb66d7d8ef20ec5c4689a36551e0) · result
                  - generic [ref=e622]: failed on purpose
                - generic [ref=e624]:
                  - generic [ref=e625]:
                    - text: b-impl reported failure. Node B should show failed. Now plan
                    - emphasis [ref=e626]: "set B done (body: words/beta.txt says beta). Then step 9: create for c-impl an assignment asking the owner \"Ready to end?\" — expect refused? No wait — step 9's assignment: c-impl asks the owner one question. When the question shows, don't answer it, instead member"
                    - text: "control end c-impl. Expect: question disappears; I get a nudge that node C is active with nobody on it. Then plan_set C done."
                  - generic [ref=e627]: First mark B done. Let me check node B state shows failed.
                - button "other wash_workspace_plan_get" [ref=e629] [cursor=pointer]:
                  - generic [ref=e630]: other
                  - generic [ref=e631]: wash_workspace_plan_get
                - generic [ref=e635]: "Node B shows failed ✓. Now plan_set B done (body: words/beta.txt says beta)."
                - generic [ref=e638]: "B shows failed ✓. Marking B done:"
                - button "other wash_workspace_plan_set" [ref=e640] [cursor=pointer]:
                  - generic [ref=e641]: other
                  - generic [ref=e642]: wash_workspace_plan_set
                - generic [ref=e646]: "B done. Step 9 — c-impl's second question assignment:"
                - button "other wash_workspace_assignment_update" [ref=e648] [cursor=pointer]:
                  - generic [ref=e649]: other
                  - generic [ref=e650]: wash_workspace_assignment_update
                - generic [ref=e654]:
                  - text: Now I need to wait until the question shows, then NOT answer it, and end c-impl with member
                  - emphasis [ref=e655]: control. I need to know when the question is showing. c-impl will ask and end its turn; I'll get a note message ("Implementer asked the owner and waits..."). Then I do member
                  - text: control end c-impl. So set waiting now for that note.
                - button "other wash_workspace_member_update" [ref=e657] [cursor=pointer]:
                  - generic [ref=e658]: other
                  - generic [ref=e659]: wash_workspace_member_update
                - generic [ref=e663]: Waiting for c-impl's question to appear.
              - generic [ref=e664]:
                - textbox "Ask, or drop a file from wash-fm…" [ref=e665]
                - button "Attach…" [ref=e667] [cursor=pointer]
              - generic [ref=e668]:
                - 'generic "Session: done" [ref=e669]': done
                - 'generic "Provider: opencode" [ref=e671]': opencode
                - generic [ref=e672]: ·
                - 'generic "Working folder: /tmp/wash-shakedown-live-3gIwUt" [ref=e673]': wash-shakedown-live-3gIwUt
                - 'combobox "Model: OpenRouter/GLM-5.3" [ref=e674] [cursor=pointer]':
                  - option "OpenRouter/Aion 3.5"
                  - option "OpenRouter/Aion 3.5 Mini"
                  - option "OpenRouter/Aion-2.0"
                  - option "OpenRouter/Aion-3.0"
                  - option "OpenRouter/Aion-3.0-Mini"
                  - option "OpenRouter/Aion-RP 1.0 (8B)"
                  - option "OpenRouter/Auto Router"
                  - option "OpenRouter/Body Builder (beta)"
                  - option "OpenRouter/Claude Fable 5"
                  - option "OpenRouter/Claude Fable 5.1"
                  - option "OpenRouter/Claude Fable Latest"
                  - option "OpenRouter/Claude Haiku 4.5 (latest)"
                  - option "OpenRouter/Claude Haiku Latest"
                  - option "OpenRouter/Claude Opus 4.1 (latest)"
                  - option "OpenRouter/Claude Opus 4.5 (latest)"
                  - option "OpenRouter/Claude Opus 4.6"
                  - option "OpenRouter/Claude Opus 4.7"
                  - option "OpenRouter/Claude Opus 4.8"
                  - option "OpenRouter/Claude Opus 5"
                  - option "OpenRouter/Claude Opus 5.5"
                  - option "OpenRouter/Claude Opus Latest"
                  - option "OpenRouter/Claude Sonnet 4"
                  - option "OpenRouter/Claude Sonnet 4.5 (latest)"
                  - option "OpenRouter/Claude Sonnet 4.6"
                  - option "OpenRouter/Claude Sonnet 5"
                  - option "OpenRouter/Claude Sonnet Latest"
                  - option "OpenRouter/Codestral 2508"
                  - option "OpenRouter/Command A"
                  - option "OpenRouter/Command A+"
                  - option "OpenRouter/Command R"
                  - option "OpenRouter/Command R+"
                  - option "OpenRouter/Command R7B"
                  - option "OpenRouter/Cydonia 24B V4.1"
                  - option "OpenRouter/DeepSeek Chat"
                  - option "OpenRouter/DeepSeek Flash Latest"
                  - option "OpenRouter/DeepSeek Pro Latest"
                  - option "OpenRouter/DeepSeek V3 0324"
                  - option "OpenRouter/DeepSeek V3.1"
                  - option "OpenRouter/DeepSeek V3.1 Terminus"
                  - option "OpenRouter/DeepSeek V3.2"
                  - option "OpenRouter/DeepSeek V3.2 Exp"
                  - option "OpenRouter/DeepSeek V4 Flash"
                  - option "OpenRouter/DeepSeek V4 Flash 0731"
                  - option "OpenRouter/DeepSeek V4 Flash Latest"
                  - option "OpenRouter/DeepSeek V4 Flash Vision Exp"
                  - option "OpenRouter/DeepSeek V4 Pro"
                  - option "OpenRouter/DeepSeek V4 Pro 0813"
                  - option "OpenRouter/DeepSeek V4.1 Flash"
                  - option "OpenRouter/DeepSeek-R1"
                  - option "OpenRouter/Devstral 2"
                  - option "OpenRouter/Dots3-Note Preview (free)"
                  - option "OpenRouter/Ember-1"
                  - option "OpenRouter/ERNIE 4.5 VL 424B A47B"
                  - option "OpenRouter/Free Models Router"
                  - option "OpenRouter/Fugu Max"
                  - option "OpenRouter/Fugu Ultra"
                  - option "OpenRouter/Fugu Ultra v2"
                  - option "OpenRouter/Fusion"
                  - option "OpenRouter/Gemini 2.5 Flash"
                  - option "OpenRouter/Gemini 2.5 Flash-Lite"
                  - option "OpenRouter/Gemini 2.5 Pro"
                  - option "OpenRouter/Gemini 2.5 Pro Preview 06-05"
                  - option "OpenRouter/Gemini 3 Flash Preview"
                  - option "OpenRouter/Gemini 3.1 Flash Lite"
                  - option "OpenRouter/Gemini 3.1 Flash Lite Preview"
                  - option "OpenRouter/Gemini 3.1 Pro Preview"
                  - option "OpenRouter/Gemini 3.1 Pro Preview Custom Tools"
                  - option "OpenRouter/Gemini 3.5 Flash"
                  - option "OpenRouter/Gemini 3.5 Flash Lite"
                  - option "OpenRouter/Gemini 3.6 Flash"
                  - option "OpenRouter/Gemini 3.7 Flash"
                  - option "OpenRouter/Gemini 3.8 Flash"
                  - option "OpenRouter/Gemini Flash Latest"
                  - option "OpenRouter/Gemini Pro Latest"
                  - option "OpenRouter/Gemma 2 27B"
                  - option "OpenRouter/Gemma 3 12B IT"
                  - option "OpenRouter/Gemma 3 27B IT"
                  - option "OpenRouter/Gemma 3 4B IT"
                  - option "OpenRouter/Gemma 4 26B A4B (free)"
                  - option "OpenRouter/Gemma 4 26B A4B IT"
                  - option "OpenRouter/Gemma 4 31B (free)"
                  - option "OpenRouter/Gemma 4 31B IT"
                  - option "OpenRouter/GLM 5.3 FlashX"
                  - option "OpenRouter/GLM 5.3 Prime"
                  - option "OpenRouter/GLM Flash Latest"
                  - option "OpenRouter/GLM Latest"
                  - option "OpenRouter/GLM-4.5"
                  - option "OpenRouter/GLM-4.5-Air"
                  - option "OpenRouter/GLM-4.5V"
                  - option "OpenRouter/GLM-4.6"
                  - option "OpenRouter/GLM-4.6V"
                  - option "OpenRouter/GLM-4.7"
                  - option "OpenRouter/GLM-4.7-Flash"
                  - option "OpenRouter/GLM-5"
                  - option "OpenRouter/GLM-5-Turbo"
                  - option "OpenRouter/GLM-5.1"
                  - option "OpenRouter/GLM-5.2"
                  - option "OpenRouter/GLM-5.3" [selected]
                  - option "OpenRouter/GLM-5.3-Flash"
                  - option "OpenRouter/GLM-5V-Turbo"
                  - option "OpenRouter/GPT Astra Latest"
                  - option "OpenRouter/GPT Audio"
                  - option "OpenRouter/GPT Audio Mini"
                  - option "OpenRouter/GPT Chat Latest"
                  - option "OpenRouter/GPT Luna Latest"
                  - option "OpenRouter/GPT Mini Latest"
                  - option "OpenRouter/GPT OSS 120B"
                  - option "OpenRouter/GPT OSS 20B"
                  - option "OpenRouter/GPT OSS Safeguard 20B"
                  - option "OpenRouter/GPT Sol Latest"
                  - option "OpenRouter/GPT Terra Latest"
                  - option "OpenRouter/GPT-3.5 Turbo (older v0613)"
                  - option "OpenRouter/GPT-3.5 Turbo 16k"
                  - option "OpenRouter/GPT-3.5 Turbo Instruct"
                  - option "OpenRouter/GPT-3.5-turbo"
                  - option "OpenRouter/GPT-4"
                  - option "OpenRouter/GPT-4 Turbo"
                  - option "OpenRouter/GPT-4.1"
                  - option "OpenRouter/GPT-4.1 mini"
                  - option "OpenRouter/GPT-4.1 nano"
                  - option "OpenRouter/GPT-4o"
                  - option "OpenRouter/GPT-4o (2024-05-13)"
                  - option "OpenRouter/GPT-4o (2024-08-06)"
                  - option "OpenRouter/GPT-4o (2024-11-20)"
                  - option "OpenRouter/GPT-4o mini"
                  - option "OpenRouter/GPT-4o-mini (2024-07-18)"
                  - option "OpenRouter/GPT-5"
                  - option "OpenRouter/GPT-5 Image"
                  - option "OpenRouter/GPT-5 Image Mini"
                  - option "OpenRouter/GPT-5 Mini"
                  - option "OpenRouter/GPT-5 Nano"
                  - option "OpenRouter/GPT-5 Pro"
                  - option "OpenRouter/GPT-5.1"
                  - option "OpenRouter/GPT-5.1 Codex"
                  - option "OpenRouter/GPT-5.1 Codex Max"
                  - option "OpenRouter/GPT-5.1 Codex mini"
                  - option "OpenRouter/GPT-5.2"
                  - option "OpenRouter/GPT-5.2 Chat"
                  - option "OpenRouter/GPT-5.2 Codex"
                  - option "OpenRouter/GPT-5.2 Pro"
                  - option "OpenRouter/GPT-5.3 Codex"
                  - option "OpenRouter/GPT-5.4"
                  - option "OpenRouter/GPT-5.4 Image 2"
                  - option "OpenRouter/GPT-5.4 mini"
                  - option "OpenRouter/GPT-5.4 nano"
                  - option "OpenRouter/GPT-5.4 Pro"
                  - option "OpenRouter/GPT-5.5"
                  - option "OpenRouter/GPT-5.5 Pro"
                  - option "OpenRouter/GPT-5.6 Luna"
                  - option "OpenRouter/GPT-5.6 Luna Pro"
                  - option "OpenRouter/GPT-5.6 Sol"
                  - option "OpenRouter/GPT-5.6 Sol Pro"
                  - option "OpenRouter/GPT-5.6 Terra"
                  - option "OpenRouter/GPT-5.6 Terra Pro"
                  - option "OpenRouter/GPT-6 Astra"
                  - option "OpenRouter/GPT-6 Astra Pro"
                  - option "OpenRouter/GPT-6 Luna"
                  - option "OpenRouter/GPT-6 Luna Pro"
                  - option "OpenRouter/GPT-6 Sol"
                  - option "OpenRouter/GPT-6 Sol Pro"
                  - option "OpenRouter/Granite 4.0 Micro"
                  - option "OpenRouter/Granite 4.2 8B"
                  - option "OpenRouter/Grok 4.20"
                  - option "OpenRouter/Grok 4.20 Multi-Agent"
                  - option "OpenRouter/Grok 4.3"
                  - option "OpenRouter/Grok 4.5"
                  - option "OpenRouter/Grok 4.6"
                  - option "OpenRouter/Grok 4.7"
                  - option "OpenRouter/Grok Build 0.1"
                  - option "OpenRouter/Grok Latest"
                  - option "OpenRouter/Hermes 3 405B Instruct"
                  - option "OpenRouter/Hermes 3 70B Instruct"
                  - option "OpenRouter/Hermes 4 405B"
                  - option "OpenRouter/Hunyuan A13B Instruct"
                  - option "OpenRouter/Hy-MT2-1.8B"
                  - option "OpenRouter/Hy-MT2-30B-A3B"
                  - option "OpenRouter/Hy-MT2-7B"
                  - option "OpenRouter/Hy3"
                  - option "OpenRouter/Hy3 preview"
                  - option "OpenRouter/Hy4 preview"
                  - option "OpenRouter/Inkling"
                  - option "OpenRouter/Inkling (free)"
                  - option "OpenRouter/Inkling Small"
                  - option "OpenRouter/Inkling Small (free)"
                  - option "OpenRouter/KAT-Coder-Pro V2.5"
                  - option "OpenRouter/Kimi K2 0711"
                  - option "OpenRouter/Kimi K2 0905"
                  - option "OpenRouter/Kimi K2 Thinking"
                  - option "OpenRouter/Kimi K2.5"
                  - option "OpenRouter/Kimi K2.6"
                  - option "OpenRouter/Kimi K2.7 Code"
                  - option "OpenRouter/Kimi K3"
                  - option "OpenRouter/Kimi Latest"
                  - option "OpenRouter/Laguna S 2.1"
                  - option "OpenRouter/Laguna S 2.1 (free)"
                  - option "OpenRouter/Laguna XS 2.1"
                  - option "OpenRouter/Laguna XS 2.1 (free)"
                  - option "OpenRouter/LFM2.5-2.6B (free)"
                  - option "OpenRouter/Ling 3.0 Flash"
                  - option "OpenRouter/Ling 3.0 Flash Fin"
                  - option "OpenRouter/Ling 3.0 Flash Fin (free)"
                  - option "OpenRouter/Ling 3.0 Flash Sante (free)"
                  - option "OpenRouter/Ling 3.0 Flash VL"
                  - option "OpenRouter/Llama 3 8B Lunaris"
                  - option "OpenRouter/Llama 3.1 Euryale 70B v2.2"
                  - option "OpenRouter/Llama 3.2 1B Instruct"
                  - option "OpenRouter/Llama 3.2 3B Instruct"
                  - option "OpenRouter/Llama 3.3 Euryale 70B"
                  - option "OpenRouter/Llama 4 Maverick"
                  - option "OpenRouter/Llama 4 Scout"
                  - option "OpenRouter/Llama Guard 4 12B"
                  - option "OpenRouter/Llama-3.1-70B-Instruct"
                  - option "OpenRouter/Llama-3.1-8B-Instruct"
                  - option "OpenRouter/Llama-3.3-70B-Instruct"
                  - option "OpenRouter/LongCat 2.0"
                  - option "OpenRouter/Lyria 3 Clip Preview"
                  - option "OpenRouter/Lyria 3 Pro Preview"
                  - option "OpenRouter/Magnum v4 72B"
                  - option "OpenRouter/Mercury 2"
                  - option "OpenRouter/Mercury 2.5"
                  - option "OpenRouter/MiMo-V2.5"
                  - option "OpenRouter/MiMo-V2.5-Pro"
                  - option "OpenRouter/MiMo-V2.6-Flash"
                  - option "OpenRouter/MiMo-V2.6-Pro"
                  - option "OpenRouter/MiMo-V2.6-Pro-UltraSpeed"
                  - option "OpenRouter/MiniMax M1"
                  - option "OpenRouter/MiniMax-01"
                  - option "OpenRouter/MiniMax-M2"
                  - option "OpenRouter/MiniMax-M2 Her"
                  - option "OpenRouter/MiniMax-M2.1"
                  - option "OpenRouter/MiniMax-M2.5"
                  - option "OpenRouter/MiniMax-M2.7"
                  - option "OpenRouter/MiniMax-M3"
                  - option "OpenRouter/Ministral 3 14B 2512"
                  - option "OpenRouter/Ministral 3 3B 2512"
                  - option "OpenRouter/Ministral 3 8B 2512"
                  - option "OpenRouter/Mistral Large"
                  - option "OpenRouter/Mistral Large 2407"
                  - option "OpenRouter/Mistral Large 3"
                  - option "OpenRouter/Mistral Medium 3"
                  - option "OpenRouter/Mistral Medium 3.1"
                  - option "OpenRouter/Mistral Medium 3.5"
                  - option "OpenRouter/Mistral Nemo"
                  - option "OpenRouter/Mistral Small 3"
                  - option "OpenRouter/Mistral Small 3.1 24B"
                  - option "OpenRouter/Mistral Small 3.2 24B"
                  - option "OpenRouter/Mistral Small 4"
                  - option "OpenRouter/Mixtral 8x22B Instruct"
                  - option "OpenRouter/Morph V3 Fast"
                  - option "OpenRouter/Morph V3 Large"
                  - option "OpenRouter/Muse Glimmer 30B"
                  - option "OpenRouter/Muse Spark 1.1"
                  - option "OpenRouter/Muse Spark 1.2"
                  - option "OpenRouter/Muse Spark 1.2 Contributor"
                  - option "OpenRouter/Muse Spark 1.3"
                  - option "OpenRouter/Muse Spark 1.3 Contributor"
                  - option "OpenRouter/MythoMax 13B"
                  - option "OpenRouter/Nano Banana"
                  - option "OpenRouter/Nano Banana 2"
                  - option "OpenRouter/Nano Banana 2 Lite"
                  - option "OpenRouter/Nano Banana 2 Preview"
                  - option "OpenRouter/Nano Banana Pro"
                  - option "OpenRouter/Nano Banana Pro Preview"
                  - option "OpenRouter/Nemotron 3 Nano 30B A3B"
                  - option "OpenRouter/Nemotron 3 Nano Omni (free)"
                  - option "OpenRouter/Nemotron 3 Super (free)"
                  - option "OpenRouter/Nemotron 3 Super 120B A12B"
                  - option "OpenRouter/Nemotron 3 Ultra (free)"
                  - option "OpenRouter/Nemotron 3 Ultra 550B A55B"
                  - option "OpenRouter/Nemotron 3.5 Content Safety"
                  - option "OpenRouter/Nemotron 3.5 Content Safety (free)"
                  - option "OpenRouter/Nemotron 3.5 Lightning (free)"
                  - option "OpenRouter/Nemotron 3.5 Lightning 30B A3B"
                  - option "OpenRouter/North Mini Code (free)"
                  - option "OpenRouter/Nova 2 Lite"
                  - option "OpenRouter/Nova Lite 1.0"
                  - option "OpenRouter/Nova Micro 1.0"
                  - option "OpenRouter/Nova Premier 1.0"
                  - option "OpenRouter/Nova Pro 1.0"
                  - option "OpenRouter/o1"
                  - option "OpenRouter/o1-pro"
                  - option "OpenRouter/o3"
                  - option "OpenRouter/o3 Mini High"
                  - option "OpenRouter/o3-mini"
                  - option "OpenRouter/o3-pro"
                  - option "OpenRouter/o4 Mini High"
                  - option "OpenRouter/o4-mini"
                  - option "OpenRouter/Palmyra X5"
                  - option "OpenRouter/Pareto"
                  - option "OpenRouter/Pareto Code Router"
                  - option "OpenRouter/Perceptron Mk1"
                  - option "OpenRouter/Perceptron Mk1.5"
                  - option "OpenRouter/Phi 4"
                  - option "OpenRouter/Qwen 3.8 Max Prime"
                  - option "OpenRouter/Qwen Plus"
                  - option "OpenRouter/Qwen Plus 0728"
                  - option "OpenRouter/Qwen2.5 72B Instruct"
                  - option "OpenRouter/Qwen2.5 7B Instruct"
                  - option "OpenRouter/Qwen2.5 Coder 32B Instruct"
                  - option "OpenRouter/Qwen2.5 VL 72B Instruct"
                  - option "OpenRouter/Qwen3 14B"
                  - option "OpenRouter/Qwen3 235B A22B Instruct 2507"
                  - option "OpenRouter/Qwen3 235B A22B Thinking 2507"
                  - option "OpenRouter/Qwen3 235B-A22B"
                  - option "OpenRouter/Qwen3 30B A3B"
                  - option "OpenRouter/Qwen3 30B A3B Instruct 2507"
                  - option "OpenRouter/Qwen3 30B A3B Thinking 2507"
                  - option "OpenRouter/Qwen3 32B"
                  - option "OpenRouter/Qwen3 8B"
                  - option "OpenRouter/Qwen3 Coder 480B A35B"
                  - option "OpenRouter/Qwen3 Coder Flash"
                  - option "OpenRouter/Qwen3 Coder Next"
                  - option "OpenRouter/Qwen3 Coder Plus"
                  - option "OpenRouter/Qwen3 Max"
                  - option "OpenRouter/Qwen3 Max Thinking"
                  - option "OpenRouter/Qwen3 VL 235B A22B Instruct"
                  - option "OpenRouter/Qwen3 VL 235B A22B Thinking"
                  - option "OpenRouter/Qwen3 VL 30B A3B Instruct"
                  - option "OpenRouter/Qwen3 VL 30B A3B Thinking"
                  - option "OpenRouter/Qwen3 VL 32B Instruct"
                  - option "OpenRouter/Qwen3 VL 8B Instruct"
                  - option "OpenRouter/Qwen3 VL 8B Thinking"
                  - option "OpenRouter/Qwen3-Coder 30B-A3B Instruct"
                  - option "OpenRouter/Qwen3-Next 80B-A3B (Thinking)"
                  - option "OpenRouter/Qwen3-Next 80B-A3B Instruct"
                  - option "OpenRouter/Qwen3.5 122B-A10B"
                  - option "OpenRouter/Qwen3.5 27B"
                  - option "OpenRouter/Qwen3.5 35B-A3B"
                  - option "OpenRouter/Qwen3.5 397B-A17B"
                  - option "OpenRouter/Qwen3.5 9B"
                  - option "OpenRouter/Qwen3.5 Plus 2026-02-15"
                  - option "OpenRouter/Qwen3.5 Plus 2026-04-20"
                  - option "OpenRouter/Qwen3.5-Flash"
                  - option "OpenRouter/Qwen3.6 27B"
                  - option "OpenRouter/Qwen3.6 35B-A3B"
                  - option "OpenRouter/Qwen3.6 Flash"
                  - option "OpenRouter/Qwen3.6 Max Preview"
                  - option "OpenRouter/Qwen3.6 Plus"
                  - option "OpenRouter/Qwen3.7 Flash"
                  - option "OpenRouter/Qwen3.7 Max"
                  - option "OpenRouter/Qwen3.7 Plus"
                  - option "OpenRouter/Qwen3.8 2.4T A95B"
                  - option "OpenRouter/Qwen3.8 27B"
                  - option "OpenRouter/Qwen3.8 27B (free)"
                  - option "OpenRouter/Qwen3.8 Flash"
                  - option "OpenRouter/Qwen3.8 Max 0902"
                  - option "OpenRouter/Qwen3.8 Omni Flash"
                  - option "OpenRouter/R1 0528"
                  - option "OpenRouter/R1 Distill Llama 70B"
                  - option "OpenRouter/Reka Edge"
                  - option "OpenRouter/Reka Flash 3"
                  - option "OpenRouter/Relace Apply 3"
                  - option "OpenRouter/Relace Search"
                  - option "OpenRouter/ReMM SLERP 13B"
                  - option "OpenRouter/Saba"
                  - option "OpenRouter/Sakana Namazu"
                  - option "OpenRouter/Schematron V2 Small"
                  - option "OpenRouter/Schematron V2 Turbo"
                  - option "OpenRouter/Seed 1.6"
                  - option "OpenRouter/Seed 1.6 Flash"
                  - option "OpenRouter/Seed 2.0 Code"
                  - option "OpenRouter/Seed 2.0 Lite"
                  - option "OpenRouter/Seed 2.0 Mini"
                  - option "OpenRouter/Seed 2.1 Turbo"
                  - option "OpenRouter/Skyfall 36B V2"
                  - option "OpenRouter/Solar Mini 4"
                  - option "OpenRouter/Solar Pro 3"
                  - option "OpenRouter/Solar Pro 4"
                  - option "OpenRouter/Sonar"
                  - option "OpenRouter/Sonar Deep Research"
                  - option "OpenRouter/Sonar Pro"
                  - option "OpenRouter/Sonar Pro Search"
                  - option "OpenRouter/Sonar Reasoning Pro"
                  - option "OpenRouter/Space Bunny Alpha"
                  - option "OpenRouter/Step 3.5 Flash"
                  - option "OpenRouter/Step 3.7 Flash"
                  - option "OpenRouter/Ternary Bonsai 2 27B"
                  - option "OpenRouter/Trinity Large Thinking"
                  - option "OpenRouter/UI-TARS 7B"
                  - option "OpenRouter/Uncensored"
                  - option "OpenRouter/UnslopNemo 12B"
                  - option "OpenRouter/Voxtral Small 24B 2507"
                  - option "OpenRouter/Weaver (alpha)"
                  - option "OpenRouter/WizardLM-2 8x22B"
                  - option "OpenCode Zen/Big Pickle"
                  - option "OpenCode Zen/Ling 3.0 Flash Fin Free"
                  - option "OpenCode Zen/LongCat 2.5 Preview Free"
                  - option "OpenCode Zen/MiMo-V2.6-Flash Free"
                  - option "OpenCode Zen/Muse Spark 1.3 Free"
                  - option "OpenCode Zen/Nemotron 3 Ultra Free"
                  - option "OpenCode Zen/Nemotron 3.5 Lightning Free"
                  - option "OpenCode Zen/Space Bunny Free"
                - generic [ref=e675]: ·
                - 'combobox "Effort: High — Available effort levels for this model" [ref=e676] [cursor=pointer]':
                  - option "Low"
                  - option "High" [selected]
                  - option "Max"
                  - option "Default"
                - generic [ref=e677]: ·
                - 'combobox "Session Mode: build" [ref=e678] [cursor=pointer]':
                  - option "build" [selected]
                  - option "plan"
                - generic [ref=e679]: ·
                - generic "wash is approving this session's tool requests without asking" [ref=e680]: YOLO
                - generic [ref=e681]: ·
                - generic [ref=e682]: ·
                - 'generic "Context: 34,171 / 1,310,720 tokens" [ref=e683]': 34k/1311k
                - generic [ref=e684]: ·
                - 'generic "Git branch: master — uncommitted changes" [ref=e685]': master *
        - separator "Resize workspace sidebar" [ref=e686]
        - complementary "Agent workspace" [ref=e688]:
          - generic [ref=e689]: Shakedown
          - region "Needs you" [ref=e690]:
            - generic [ref=e691]: Needs you
            - 'button "Implementer · Question: Ready to end?" [ref=e692] [cursor=pointer]'
          - button "Plan · 3/6 done" [ref=e693] [cursor=pointer]
          - button "Questions · 2 open" [ref=e694] [cursor=pointer]
          - button "Alpha case B · beta.txt open · Implementer" [ref=e695] [cursor=pointer]:
            - text: Alpha case
            - generic [ref=e696]: B · beta.txt
            - generic [ref=e697]: open · Implementer
          - button "Gamma word C · gamma.txt awaiting-owner · Implementer" [ref=e698] [cursor=pointer]:
            - text: Gamma word
            - generic [ref=e699]: C · gamma.txt
            - generic [ref=e700]: awaiting-owner · Implementer
          - generic [ref=e701]: Team
          - region "Coordination" [ref=e702]:
            - 'button "Orchestrator Awaiting message Context: 34,171 / 1,310,720 tokens waiting for c-impl''s ''Ready to end?'' question to appear before ending the member" [ref=e703] [cursor=pointer]':
              - generic [ref=e706]: Orchestrator
              - text: Awaiting message
              - generic "Provider-reported context usage, not cumulative or billed tokens" [ref=e708]: "Context: 34,171 / 1,310,720 tokens"
              - text: waiting for c-impl's 'Ready to end?' question to appear before ending the member
          - region "A · alpha.txt" [ref=e709]:
            - generic [ref=e710]: A · alpha.txt
            - 'button "Implementer Awaiting message Context: 15,369 / 1,310,720 tokens waiting Verification assignment complete; file unchanged" [ref=e711] [cursor=pointer]':
              - generic [ref=e714]: Implementer
              - text: Awaiting message
              - generic "Provider-reported context usage, not cumulative or billed tokens" [ref=e716]: "Context: 15,369 / 1,310,720 tokens"
              - generic [ref=e717]: waiting
              - text: Verification assignment complete; file unchanged
            - 'button "Red team Awaiting message Context: 8,877 / 1,310,720 tokens waiting Round 2 review reported (OK); awaiting next instruction." [ref=e718] [cursor=pointer]':
              - generic [ref=e721]: Red team
              - text: Awaiting message
              - generic "Provider-reported context usage, not cumulative or billed tokens" [ref=e723]: "Context: 8,877 / 1,310,720 tokens"
              - generic [ref=e724]: waiting
              - text: Round 2 review reported (OK); awaiting next instruction.
          - region "B · beta.txt" [ref=e725]:
            - generic [ref=e726]: B · beta.txt
            - 'button "Implementer Awaiting message Context: 15,492 / 1,310,720 tokens Failure test reported; idle and available for new assignments" [ref=e727] [cursor=pointer]':
              - generic [ref=e730]: Implementer
              - text: Awaiting message
              - generic "Provider-reported context usage, not cumulative or billed tokens" [ref=e732]: "Context: 15,492 / 1,310,720 tokens"
              - text: Failure test reported; idle and available for new assignments
          - region "C · gamma.txt" [ref=e733]:
            - generic [ref=e734]: C · gamma.txt
            - 'button "Implementer Needs you Context: 15,221 / 1,310,720 tokens Waiting for the owner: Ready to end?" [ref=e735] [cursor=pointer]':
              - generic [ref=e738]: Implementer
              - text: Needs you
              - generic "Provider-reported context usage, not cumulative or billed tokens" [ref=e740]: "Context: 15,221 / 1,310,720 tokens"
              - text: "Waiting for the owner: Ready to end?"
          - group [ref=e741]:
            - generic "Messages" [ref=e742] [cursor=pointer]
    - generic "Resize" [ref=e743]
  - generic [ref=e744]:
    - generic [ref=e745]:
      - generic [ref=e746]: router unreachable — reconnecting…
      - generic [ref=e747]: no contact 2s · attempt 4
    - button "Reconnect now" [ref=e748] [cursor=pointer]
```

# Test source

```ts
  21  | // (apps/agentd/openrouter-eval/mixes/); the stored OpenRouter key is copied
  22  | // into the isolated Wash for it, and the scorecard carries the key's spend.
  23  | // WASH_E2E_MINUTES bounds the run (default 85).
  24  | 
  25  | test.skip(process.env.WASH_E2E_LIVE !== '1', 'live run: set WASH_E2E_LIVE=1');
  26  | test.use({ routerOpts: { apps: ['session', 'about', 'agentd', 'agents', 'ai', 'notify'] } });
  27  | 
  28  | const SHAKEDOWN = fileURLToPath(new URL('../shakedown', import.meta.url));
  29  | 
  30  | test('the shakedown, live', async ({ page, router }) => {
  31  |   const minutes = Number(process.env.WASH_E2E_MINUTES ?? 85);
  32  |   test.setTimeout((minutes + 5) * 60_000);
  33  |   const began = Date.now();
  34  |   const project = mkdtempSync(join(tmpdir(), 'wash-shakedown-live-'));
  35  |   cpSync(SHAKEDOWN, project, { recursive: true });
  36  |   execSync('git init -q && git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -qm "shakedown: start"', { cwd: project });
  37  |   mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  38  |   const slot = (model: string) => ({ provider: 'claude', model });
  39  |   const catalogFile = process.env.WASH_E2E_CATALOG;
  40  |   const catalog = catalogFile ? JSON.parse(readFileSync(catalogFile, 'utf8')) : { name: 'Shakedown', slots: { frontier: slot('sonnet'), coding: slot('sonnet'), small: slot('haiku') } };
  41  |   writeFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), JSON.stringify({ catalogs: { shakedown: catalog } }));
  42  |   let key = '';
  43  |   if (catalogFile) {
  44  |     const keys = join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'wash', 'keys.json');
  45  |     key = JSON.parse(readFileSync(keys, 'utf8')).openrouter ?? '';
  46  |     cpSync(keys, join(router.xdgConfigHome, 'wash', 'keys.json'));
  47  |     chmodSync(join(router.xdgConfigHome, 'wash', 'keys.json'), 0o600);
  48  |   }
  49  |   const spend = async () => key ? (await (await fetch('https://openrouter.ai/api/v1/key', { headers: { Authorization: `Bearer ${key}` } })).json()).data.usage as number : 0;
  50  |   const spentBefore = await spend();
  51  |   console.log('shakedown catalog:', JSON.stringify(catalog.slots));
  52  |   console.log('shakedown project:', project);
  53  | 
  54  |   await page.goto(router.url);
  55  |   await expect(page.locator('wash-app-session')).toBeVisible();
  56  |   const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  57  |   await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: {
  58  |     kind: 'agent_start', claim: true, catalog: 'shakedown', model: 'frontier', cwd: project, yolo: true,
  59  |     prompt: 'Read SCRIPT.md and run the shakedown. Commit with git -c user.name=shakedown -c user.email=shakedown@localhost.',
  60  |   } });
  61  |   const app = page.locator('wash-app-ai');
  62  |   const stateFile = join(router.xdgStateHome, 'wash', 'workspaces.json');
  63  |   const state = () => existsSync(stateFile) ? JSON.parse(readFileSync(stateFile, 'utf8')) : { workspaces: [] };
  64  |   const report = test.info().outputPath('orchestrator.txt');
  65  |   let answered = false;
  66  |   const deadline = Date.now() + minutes * 60_000;
  67  |   // The owner: answer the gamma question in its asker's tab; never the
  68  |   // "Ready to end?" one (the script ends that member instead).
  69  |   let timedOut = false;
  70  |   for (;;) {
  71  |     // Out of time still scores: a mix that stalls is a result too.
  72  |     if (Date.now() > deadline) { timedOut = true; break; }
  73  |     const w = state().workspaces.findLast((x: any) => x.state !== 'ended');
  74  |     const gamma = w?.messages?.find((m: any) => m.type === 'decision_request' && m.delivery === 'recorded' && m.body.includes('gamma.txt'));
  75  |     if (gamma && !answered) {
  76  |       const sidebar = app.getByTestId('workspace-sidebar');
  77  |       await sidebar.getByTestId(`workspace-question-for-${gamma.id}`).click();
  78  |       const panel = app.getByTestId('question-panel');
  79  |       await panel.getByRole('radio', { name: /gamma/ }).first().click();
  80  |       const note = panel.getByRole('textbox').nth(1);
  81  |       if (await note.count()) await note.fill('lower case, like alpha and beta');
  82  |       await panel.getByRole('button', { name: /^Submit/ }).click();
  83  |       await expect(panel).toHaveCount(0);
  84  |       await app.getByRole('tab', { name: /^Conversation/ }).click();
  85  |       answered = true;
  86  |       console.log('owner answered', gamma.id);
  87  |     }
  88  |     const text = await app.getByTestId('agent-transcript').first().innerText().catch(() => '');
  89  |     writeFileSync(report, text);
  90  |     // Done when the orchestrator has run check.sh and its turn is over.
  91  |     if (/(ok|FAIL) +A was accepted with trailers/.test(text) && !(await app.getByTestId('agent-stop').count())) break;
  92  |     await page.waitForTimeout(5_000);
  93  |   }
  94  |   const check = (() => { try { return execSync('./check.sh', { cwd: project, encoding: 'utf8' }); } catch (e: any) { return String(e.stdout ?? e); } })();
  95  |   writeFileSync(test.info().outputPath('check.txt'), check);
  96  |   console.log(check);
  97  |   // The scorecard: what a mix is judged on beside check.sh. Nudges are
  98  |   // Wash's reminders (a member that forgot to report, a node with nobody on
  99  |   // it), so fewer means the models kept to the process by themselves.
  100 |   const w = state().workspaces;
  101 |   const messages = w.flatMap((x: any) => x.messages ?? []);
  102 |   const count = (f: (m: any) => boolean) => messages.filter(f).length;
  103 |   await new Promise(r => setTimeout(r, 20_000)); // the key's usage lags
  104 |   // Pauses: stretches over five minutes in which no session recorded
  105 |   // anything (a sleeping laptop, a stalled provider). Counted apart, so a
  106 |   // mix's speed is its active time; the transcripts say which it was.
  107 |   const tdir = join(router.xdgStateHome, 'wash', 'agent-transcripts');
  108 |   const times = (existsSync(tdir) ? readdirSync(tdir) : []).flatMap(f => readFileSync(join(tdir, f), 'utf8').split('\n')
  109 |     .map(l => { try { return JSON.parse(l).at_ms as number; } catch { return undefined; } })
  110 |     .filter((t): t is number => typeof t === 'number' && t >= began)).sort((a, b) => a - b);
  111 |   const pauses = times.slice(1).map((t, i) => t - times[i]).filter(g => g > 5 * 60_000);
  112 |   const pauseMinutes = Math.round(pauses.reduce((a, b) => a + b, 0) / 600) / 100;
  113 |   const scorecard = {
  114 |     catalog: catalog.slots,
  115 |     timed_out: timedOut,
  116 |     checks_ok: (check.match(/^ok /gm) ?? []).length,
  117 |     checks_failed: (check.match(/^FAIL /gm) ?? []).length,
  118 |     minutes: Math.round((Date.now() - began) / 600) / 100,
  119 |     pause_minutes: pauseMinutes,
  120 |     active_minutes: Math.round(((times.at(-1) ?? began) - began) / 600) / 100 - pauseMinutes,
> 121 |     cost_usd: key ? Math.round(((await spend()) - spentBefore) * 10_000) / 10_000 : null,
      |                                                            ^ Error: the shakedown did not finish in time
  122 |     members: w.flatMap((x: any) => x.members ?? []).length,
  123 |     messages: messages.length,
  124 |     wash_reminders: count(m => m.sender === 'wash'),
  125 |     lifecycle: count(m => m.type === 'lifecycle'),
  126 |     failed_assignments: w.flatMap((x: any) => x.assignments ?? []).filter((a: any) => a.state === 'failed').length,
  127 |   };
  128 |   writeFileSync(test.info().outputPath('scorecard.json'), JSON.stringify(scorecard, null, 2));
  129 |   writeFileSync(test.info().outputPath('workspaces.json'), JSON.stringify(state(), null, 2));
  130 |   execSync(`git log --stat > ${JSON.stringify(test.info().outputPath('git-log.txt'))}; cp -a .wash ${JSON.stringify(test.info().outputPath('dot-wash'))}`, { cwd: project });
  131 |   console.log('scorecard:', JSON.stringify(scorecard));
  132 |   expect(timedOut, 'the shakedown did not finish in time').toBe(false);
  133 |   expect(check).not.toContain('FAIL');
  134 | });
  135 | 
```