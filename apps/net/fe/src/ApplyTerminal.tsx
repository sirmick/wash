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

import { For, Show } from "solid-js";

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
      <div class="wash-net-rail" data-testid="apply-rail">
        <For each={RAIL}>
          {(chip) => (
            <span class="wash-net-chip" data-phase={chip.key} data-state={railState(chip.key)}>
              {chip.label}
            </span>
          )}
        </For>
      </div>

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

      <Show
        when={awaiting()}
        fallback={
          // Applies are initiated per-connection from the wizards (each Create
          // runs its own commit-confirm), so the terminal just reflects the
          // resulting status; there is no standalone Apply button.
          <div class="wash-net-applybar">
            <span class="wash-net-status" data-status={props.status()}>{props.status()}</span>
          </div>
        }
      >
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
