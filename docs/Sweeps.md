# Sweeps ledger

One line per sweep, newest first. The latest sweep's full findings are
always `Review-findings.md`; older ones live in git history.

- 2026-09-08 — **apps: agent, term, edit, fm workflows** (+ cross-app
  seams). 7 P0 data-loss items (edit binary/CRLF/4 MiB/no-close-guard/
  silent save failure, fm paste-into-self, shared atomic-write identity
  clobber), ~30 P1, per-app P2 lists, 5 stale doc/backlog entries.
  Findings only; nothing fixed. → `Review-findings.md`
