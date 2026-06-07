// ApplyTerminal — the apply panel (docs/NET.md §7.2, B3). Renders the
// commit-confirm transaction the in-guest netd runs: a progress rail of phase
// chips, a streamed phase-event log, and — while awaiting confirmation — a live
// countdown to the autonomous auto-revert with Keep / Discard. The events are
// netd's real txn phase stream (PLAN→RENDER→APPLY→VERIFY→AWAIT-CONFIRM→COMMITTED,
// §7.1), relayed over the wire. Raw backend log bytes (nft / networkctl output)
// stream in here too once the real backend lands (B4); today the fake Applier
// emits only the phase events.
//
// Pure renderer — all state lives in the App (main.tsx).

import { For, Show, Switch, Match } from "solid-js";

export interface ApplyEvent {
  seq: number;
  phase: string;
  level: string; // info | warn | error
  msg: string;
}

// The canonical happy-path rail. Terminal states (committed/reverted/failed)
// are shown as the rail's end-cap colour, not as their own chip.
const RAIL: { key: string; label: string }[] = [
  { key: "plan", label: "Plan" },
  { key: "render", label: "Render" },
  { key: "apply", label: "Apply" },
  { key: "verify", label: "Verify" },
  { key: "await-confirm", label: "Confirm" },
];

export interface ApplyTerminalProps {
  status: () => string; // idle | await-confirm | committed | reverted | failed
  events: () => ApplyEvent[];
  remainingMs: () => number; // countdown while await-confirm; 0 otherwise
  windowMs: () => number; // the full confirm window, for the bar ratio
  onKeep: () => void;
  onDiscard: () => void;
}

function reachedPhases(events: ApplyEvent[]): Set<string> {
  return new Set(events.map((e) => e.phase));
}

// Friendly status text for the footer line (the raw values are netd txn states).
const STATUS_LABEL: Record<string, string> = {
  idle: "Ready",
  applying: "Applying…",
  "await-confirm": "Awaiting confirmation",
  committed: "Applied",
  reverted: "Reverted",
  failed: "Failed",
};

export function ApplyTerminal(props: ApplyTerminalProps) {
  const reached = () => reachedPhases(props.events());
  const awaiting = () => props.status() === "await-confirm";
  const railState = (key: string): string => {
    const st = props.status();
    if (key === "await-confirm" && awaiting()) return "active";
    if (st === "committed") return "done";
    if ((st === "reverted" || st === "failed") && reached().has(key)) return "bad";
    return reached().has(key) ? "done" : "pending";
  };
  const secs = () => Math.ceil(props.remainingMs() / 1000);
  const barPct = () => {
    const w = props.windowMs();
    return w > 0 ? Math.max(0, Math.min(100, (props.remainingMs() / w) * 100)) : 0;
  };

  return (
    <div class="wash-net-apply" data-status={props.status()}>
      {/* A stepper — connected nodes that tick from Plan → Confirm — rather than
          a row of pills: the phases are an ordered sequence, so a progress rail
          (check / spinner / ✕ per node, connectors filling as it advances) reads
          the transaction far more naturally than disconnected chips. */}
      <ol class="wash-net-steps" data-testid="apply-rail" data-status={props.status()}>
        <For each={RAIL}>
          {(step, i) => {
            const st = () => railState(step.key);
            return (
              <li class="wash-net-step" data-phase={step.key} data-state={st()}>
                <span class="wash-net-stepnode" aria-hidden="true">
                  <Switch fallback={<span class="wash-net-stepnum">{i() + 1}</span>}>
                    <Match when={st() === "done"}><span class="wash-net-stepglyph">✓</span></Match>
                    <Match when={st() === "bad"}><span class="wash-net-stepglyph">✕</span></Match>
                    <Match when={st() === "active"}><span class="wash-net-spin" /></Match>
                  </Switch>
                </span>
                <span class="wash-net-steplabel">{step.label}</span>
              </li>
            );
          }}
        </For>
      </ol>

      <Show when={props.events().length > 0}>
        <div class="wash-net-log" data-testid="apply-log">
          <For each={props.events()}>
            {(e) => (
              <div class="wash-net-logline" data-level={e.level} data-phase={e.phase}>
                <span class="wash-net-logphase">{e.phase}</span>
                <span class="wash-net-logmsg">{e.msg}</span>
              </div>
            )}
          </For>
        </div>
      </Show>

      {/* Status line stays in normal flow at the bottom of the terminal.
          Applies are initiated per-connection from the wizards (each Create
          runs its own commit-confirm), so the terminal just reflects the
          resulting status; there is no standalone Apply button. */}
      <div class="wash-net-applybar">
        <span class="wash-net-statdot" data-status={props.status()} aria-hidden="true" />
        <span class="wash-net-status" data-status={props.status()}>
          {STATUS_LABEL[props.status()] ?? props.status()}
        </span>
      </div>

      {/* The Keep/Discard prompt is a banner pinned to the TOP of the app
          (.wash-net-confirm is position:absolute against .wash-net-app), not a
          bar at the bottom of the terminal: a window's bottom edge can be
          clipped under the taskbar on a short screen (e.g. the VM-served
          desktop), which left the bottom-anchored buttons unclickable. A
          window's top is never off-screen, so a top banner is always
          reachable. */}
      <Show when={awaiting()}>
        <div class="wash-net-confirm" data-testid="apply-confirm">
          <div class="wash-net-countdown" data-testid="apply-countdown">
            <div class="wash-net-countbar" style={{ width: `${barPct()}%` }} />
            <span class="wash-net-counttext">
              Keep changes? Auto-reverting in {secs()}s
            </span>
          </div>
          <button class="wash-net-btn" data-testid="discard-button" onClick={() => props.onDiscard()}>Discard</button>
          <button class="wash-net-btn primary" data-testid="keep-button" onClick={() => props.onKeep()}>Keep</button>
        </div>
      </Show>
    </div>
  );
}
