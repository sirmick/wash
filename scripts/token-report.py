#!/usr/bin/env python3
"""token-report: what Claude sessions actually cost, from Claude Code's own logs.

Every Claude session (a plain `claude` CLI run, or a wash workspace member,
which is claude-agent-acp over the same Claude Code) records each API
request's billed usage in ~/.claude/projects/<cwd>/<session>.jsonl, and its
subagents' in <session>/subagents/*.jsonl. This sums them per session so a
wash workspace and a plain Claude Code run of the same task compare on the
same meter.

  token-report.py workspace [NAME-OR-ID] [--all]   members of a wash workspace
  token-report.py session SESSION-ID...            any Claude sessions

Dollars are API list prices, so on a subscription they are an
API-equivalent measure of consumption, not a bill.
"""

import argparse
import glob
import json
import os
import sys
from datetime import datetime

PROJECTS = os.path.expanduser("~/.claude/projects")
STORE = os.path.join(os.environ.get("XDG_STATE_HOME") or os.path.expanduser("~/.local/state"), "wash", "workspaces.json")

# $/MTok: input, output, cache read. Cache writes are 1.25x input (5-minute
# entries) and 2x input (1-hour). Longest matching prefix wins, so a "[1m]"
# or dated suffix still prices.
PRICES = {
    "claude-fable-5-1": (10.0, 50.0, 0.25),
    "claude-fable-5": (10.0, 50.0, 1.0),
    "claude-opus-5-5": (4.0, 20.0, 0.20),
    "claude-opus-5": (5.0, 25.0, 0.50),
    "claude-opus-4": (5.0, 25.0, 0.50),
    "claude-sonnet-5": (2.0, 10.0, 0.20),
    "claude-sonnet-4": (3.0, 15.0, 0.30),
    "claude-haiku-4-5": (1.0, 5.0, 0.10),
}


def price(model):
    best = max((k for k in PRICES if model.startswith(k)), key=len, default=None)
    return PRICES.get(best)


class Tally:
    FIELDS = ("requests", "input", "write_5m", "write_1h", "read", "output", "cost", "coord_calls")

    def __init__(self):
        for f in self.FIELDS:
            setattr(self, f, 0)
        self.peak = 0
        self.unpriced = set()
        self.first = self.last = None

    def add(self, other):
        for f in self.FIELDS:
            setattr(self, f, getattr(self, f) + getattr(other, f))
        self.peak = max(self.peak, other.peak)
        self.unpriced |= other.unpriced
        for t in (other.first, other.last):
            if t:
                self.first = min(self.first or t, t)
                self.last = max(self.last or t, t)

    @property
    def hit(self):
        total = self.input + self.write_5m + self.write_1h + self.read
        return self.read / total if total else 0.0


def read_log(path):
    """Tally one transcript file, counting each API request once: Claude Code
    writes one line per content block, each carrying the request's usage."""
    t = Tally()
    requests = {}
    for line in open(path, errors="replace"):
        try:
            d = json.loads(line)
        except ValueError:
            continue
        msg = d.get("message") or {}
        if d.get("type") != "assistant" or not isinstance(msg, dict):
            continue
        for block in msg.get("content") or []:
            if isinstance(block, dict) and block.get("type") == "tool_use" and "wash_workspace" in block.get("name", ""):
                t.coord_calls += 1
        if "usage" in msg:
            requests[d.get("requestId") or d.get("uuid")] = (msg.get("model", ""), msg["usage"], d.get("timestamp"))
    for model, u, ts in requests.values():
        if model == "<synthetic>":
            continue
        cc = u.get("cache_creation") or {}
        w1h = cc.get("ephemeral_1h_input_tokens", 0)
        w5m = cc.get("ephemeral_5m_input_tokens", u.get("cache_creation_input_tokens", 0) - w1h)
        inp, read, out = u.get("input_tokens", 0), u.get("cache_read_input_tokens", 0), u.get("output_tokens", 0)
        t.requests += 1
        t.input += inp
        t.write_5m += w5m
        t.write_1h += w1h
        t.read += read
        t.output += out
        t.peak = max(t.peak, inp + w5m + w1h + read)
        p = price(model)
        if p:
            i, o, r = p
            t.cost += (inp * i + w5m * i * 1.25 + w1h * i * 2 + read * r + out * o) / 1e6
        else:
            t.unpriced.add(model)
        if ts:
            t.first = min(t.first or ts, ts)
            t.last = max(t.last or ts, ts)
    return t


def session(sid):
    """(own, subagents) tallies for a session, or None when no log exists."""
    logs = glob.glob(os.path.join(PROJECTS, "*", sid + ".jsonl"))
    if not logs:
        return None
    own, subs = Tally(), Tally()
    for log in logs:
        own.add(read_log(log))
    for log in glob.glob(os.path.join(PROJECTS, "*", sid, "subagents", "*.jsonl")):
        subs.add(read_log(log))
    return own, subs


def n(v):
    for unit, size in (("G", 1e9), ("M", 1e6), ("k", 1e3)):
        if v >= size:
            return f"{v / size:.1f}{unit}"
    return str(v)


def span(t):
    if not (t.first and t.last):
        return "-"
    parse = lambda s: datetime.fromisoformat(s.replace("Z", "+00:00"))
    mins = (parse(t.last) - parse(t.first)).total_seconds() / 60
    return f"{mins / 60:.1f}h" if mins >= 90 else f"{mins:.0f}m"


def report(title, rows):
    """rows: (label, session id); prints one line per session and a total."""
    print(f"\n{title}")
    head = f"{'':28} {'reqs':>6} {'uncached':>8} {'write5m':>8} {'write1h':>8} {'read':>8} {'output':>7} {'hit':>5} {'peak':>6} {'coord':>5} {'subagt$':>8} {'cost':>9} {'span':>6}"
    print(head)
    print("-" * len(head))
    total, missing = Tally(), []
    for label, sid in rows:
        got = session(sid) if sid else None
        if got is None:
            missing.append(label)
            continue
        own, subs = got
        both = Tally()
        both.add(own)
        both.add(subs)
        total.add(both)
        print(f"{label[:28]:28} {both.requests:>6} {n(both.input):>8} {n(both.write_5m):>8} {n(both.write_1h):>8} {n(both.read):>8} {n(both.output):>7} {both.hit:>5.0%} {n(both.peak):>6} {both.coord_calls:>5} {subs.cost:>8.2f} {both.cost:>9.2f} {span(both):>6}")
    print("-" * len(head))
    print(f"{'TOTAL':28} {total.requests:>6} {n(total.input):>8} {n(total.write_5m):>8} {n(total.write_1h):>8} {n(total.read):>8} {n(total.output):>7} {total.hit:>5.0%} {n(total.peak):>6} {total.coord_calls:>5} {'':>8} {total.cost:>9.2f} {span(total):>6}")
    if missing:
        print(f"no Claude log for: {', '.join(missing)}")
    if total.unpriced:
        print(f"not priced (add to PRICES): {', '.join(sorted(total.unpriced))}")
    return total


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    w = sub.add_parser("workspace", help="members of a wash workspace (default: the most recent)")
    w.add_argument("name", nargs="?", help="workspace name or ID; the most recent match is used")
    w.add_argument("--all", action="store_true", help="every workspace in the store")
    s = sub.add_parser("session", help="any Claude session IDs, e.g. a plain claude CLI run")
    s.add_argument("ids", nargs="+")
    a = ap.parse_args()

    if a.cmd == "session":
        report("sessions", [(sid, sid) for sid in a.ids])
        return
    spaces = json.load(open(STORE))["workspaces"]
    if a.name:
        spaces = [x for x in spaces if a.name in (x["id"], x["name"])]
    if not spaces:
        sys.exit(f"no workspace matches in {STORE}")
    for x in spaces if a.all else spaces[-1:]:
        rows = []
        for m in x["members"]:
            tag = "LEAD " if m["id"] == x["orchestrator"] else ""
            pkg = m.get("package") and m["package"] + " " or ""
            rows.append((f"{tag}{pkg}{m['name']}", m.get("session_id", "")))
        report(f"workspace {x['name']} ({x['id']}, {x['state']}, {len(rows)} members)", rows)
    print("\ncoord = wash_workspace tool calls. peak = largest single-request context. "
          "subagt$ = the member's own Claude subagents, included in its cost.\n"
          "A session reused across workspaces (a resumed orchestrator) counts its whole log each time.")


if __name__ == "__main__":
    main()
