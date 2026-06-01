// wash-app-top: live system monitor.
//
// Layout:
//   [ per-core CPU bars + mem bar | small sparklines for each ]
//   [ toolbar: tree/flat · filter · interval ]
//   [ process list (flat or tree) | optional details side-pane ]
//   [ status bar: proc count · load · threads ]
//
// Snapshots arrive every ~2s from the BE; FE keeps a small ring
// buffer (60 samples ≈ 2 min @ 2s) for the sparklines. Reopens of
// the window start the buffer fresh — that's the [[wash-direction]]-
// aligned simple choice; BE-side history can be added later if
// people miss the curve continuity.

import { For, Index, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { Component, JSX } from 'solid-js';
import { ConfirmDialog, defineWashApp, tokens } from '@wash/ui';
import { filterSortProcs, type SortKey } from './procsort.ts';
import {
  ChevronDown,
  ChevronRight,
  ListTree,
  List as ListIcon,
  Pause,
  Play,
  X as XIcon,
  Skull,
} from 'lucide-solid';

// ---- types (mirror cmd/wash-top wire structs) ----

interface CPURow {
  User: number; Nice: number; System: number; Idle: number;
  IOWait: number; IRQ: number; SoftIRQ: number; Steal: number;
}

interface MemRow {
  Total: number; Available: number; Free: number; Buffers: number; Cached: number;
  SwapTotal: number; SwapFree: number;
}

interface LoadRow {
  L1: number; L5: number; L15: number; Running: number; Threads: number;
}

interface ProcInfo {
  PID: number; PPID: number; UID: number;
  User: string;
  State: string;
  Nice: number; Threads: number; StartUnix: number;
  RSS: number; VSize: number;
  CPU: number; MemPct: number;
  TimeJiff: number;
  Comm: string; Cmd: string;
}

interface NetAgg { RxBytes: number; TxBytes: number; }
interface NetDev { name: string; RxBytes: number; TxBytes: number; real: boolean; }
interface DiskAgg { ReadBytes: number; WriteBytes: number; }
interface DiskDev { name: string; ReadBytes: number; WriteBytes: number; real: boolean; }

interface Snapshot {
  kind: 'snapshot';
  ts: number;
  cpu_count: number;
  cpu: CPURow;
  per_cpu: CPURow[];
  mem: MemRow;
  load: LoadRow;
  net: NetAgg;
  per_net: NetDev[];
  disk: DiskAgg;
  per_disk: DiskDev[];
  procs: ProcInfo[];
  interval_ms: number;
}

interface Details {
  pid: number;
  found: boolean;
  comm?: string;
  cmd?: string;
  exe?: string;
  cwd?: string;
  status?: Record<string, string>;
  io?: Record<string, string>;
  limits?: { name: string; soft: string; hard: string; unit?: string }[];
  cgroup?: string;
  fds?: number;
  wchan?: string;
  oom_score?: string;
}

// HISTORY_LEN is the sparkline ring-buffer depth. 60 samples × 2s
// default = 2 minutes; long enough to see a workload settle, short
// enough that the graphs respond visibly to current activity.
const HISTORY_LEN = 60;

// computeCPUUsed: jiffies-delta → 0..1 "fraction busy" between two
// per-core readings. (Total - idle - iowait) over Total. The FE
// computes this rather than the BE because it has both samples in
// hand and one place to choose the formula.
function computeCPUUsed(prev: CPURow | undefined, cur: CPURow): number {
  if (!prev) return 0;
  const prevTotal = sumCPU(prev);
  const curTotal = sumCPU(cur);
  const dTotal = curTotal - prevTotal;
  if (dTotal <= 0) return 0;
  const dIdle = (cur.Idle + cur.IOWait) - (prev.Idle + prev.IOWait);
  return Math.max(0, Math.min(1, 1 - dIdle / dTotal));
}

function sumCPU(c: CPURow): number {
  return c.User + c.Nice + c.System + c.Idle + c.IOWait + c.IRQ + c.SoftIRQ + c.Steal;
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} K`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(0)} M`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} G`;
}

// fmtRate renders bytes-per-second with units. Used by net + disk
// meters and their popover rows.
function fmtRate(bps: number): string {
  if (bps < 1024) return `${bps.toFixed(0)} B/s`;
  if (bps < 1024 * 1024) return `${(bps / 1024).toFixed(1)} K/s`;
  if (bps < 1024 * 1024 * 1024) return `${(bps / 1024 / 1024).toFixed(1)} M/s`;
  return `${(bps / 1024 / 1024 / 1024).toFixed(2)} G/s`;
}

// rateDelta computes bytes/s from two cumulative-byte samples taken
// `dtMS` apart. Cumulative counters can also reset (driver reload,
// interface flap) — guard against the negative-delta case.
function rateDelta(prev: number | undefined, cur: number, dtMS: number): number {
  if (prev === undefined || dtMS <= 0 || cur < prev) return 0;
  return ((cur - prev) * 1000) / dtMS;
}

// pushRing appends `v` and drops the head if the buffer has grown
// past HISTORY_LEN. Returns a NEW array so Solid sees the change.
function pushRing(buf: number[], v: number): number[] {
  const next = [...buf, v];
  if (next.length > HISTORY_LEN) next.shift();
  return next;
}

function fmtCPU(pct: number): string {
  if (pct < 0.05) return '0.0';
  if (pct < 10) return pct.toFixed(1);
  return pct.toFixed(0);
}

function fmtTime(jiffies: number): string {
  // jiffies → seconds (USER_HZ=100), then H:MM:SS.
  const s = Math.floor(jiffies / 100);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(sec).padStart(2, '0')}`;
  return `${m}:${String(sec).padStart(2, '0')}`;
}

function cpuColor(used: number): string {
  if (used > 0.85) return '#d97757';
  if (used > 0.5) return '#c8a04a';
  return '#4a8ab0';
}

// ---- app ----

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const [snapshot, setSnapshot] = createSignal<Snapshot | null>(null);
  const [prevPerCPU, setPrevPerCPU] = createSignal<CPURow[] | null>(null);
  const [prevCPU, setPrevCPU] = createSignal<CPURow | null>(null);

  // History rings for sparklines: one number per sample. The aggregate
  // ring drives the always-visible meter; the per-core array is shown
  // only inside the click-to-open popover.
  const [aggCPUHistory, setAggCPUHistory] = createSignal<number[]>([]);
  const [perCPUHistory, setPerCPUHistory] = createStore<number[][]>([]);
  const [memHistory, setMemHistory] = createSignal<number[]>([]);

  // Net + disk rate rings. Two arrays per source so the mirror
  // sparkline can render RX above / TX below the axis (and same for
  // disk: read above, write below). Per-device rings are keyed by
  // name; we render only those currently in the latest snapshot.
  const [netRxHistory, setNetRxHistory] = createSignal<number[]>([]);
  const [netTxHistory, setNetTxHistory] = createSignal<number[]>([]);
  const [diskReadHistory, setDiskReadHistory] = createSignal<number[]>([]);
  const [diskWriteHistory, setDiskWriteHistory] = createSignal<number[]>([]);
  const [perNetHistory, setPerNetHistory] = createStore<Record<string, { rx: number[]; tx: number[] }>>({});
  const [perDiskHistory, setPerDiskHistory] = createStore<Record<string, { r: number[]; w: number[] }>>({});

  const [cpuPopover, setCpuPopover] = createSignal(false);
  const [netPopover, setNetPopover] = createSignal(false);
  const [diskPopover, setDiskPopover] = createSignal(false);
  let cpuTriggerEl: HTMLDivElement | undefined;
  let cpuPopoverEl: HTMLDivElement | undefined;
  let netTriggerEl: HTMLDivElement | undefined;
  let netPopoverEl: HTMLDivElement | undefined;
  let diskTriggerEl: HTMLDivElement | undefined;
  let diskPopoverEl: HTMLDivElement | undefined;

  const [view, setView] = createSignal<'flat' | 'tree'>('flat');
  const [sortKey, setSortKey] = createSignal<SortKey>('cpu');
  const [sortDesc, setSortDesc] = createSignal(true);
  const [filter, setFilter] = createSignal('');
  const [paused, setPaused] = createSignal(false);
  const [intervalMS, setIntervalMS] = createSignal(2000);

  const [selectedPID, setSelectedPID] = createSignal<number | null>(null);
  const [collapsed, setCollapsed] = createStore<Record<number, true>>({});
  const [showDetails, setShowDetails] = createSignal(false);
  const [details, setDetails] = createSignal<Details | null>(null);

  const [killTarget, setKillTarget] = createSignal<ProcInfo | null>(null);

  // ---- BE messages ----

  const handleBE = (m: any) => {
    switch (m.kind) {
      case 'snapshot': {
        if (paused()) return;
        const snap = m as Snapshot;
        const prev = snapshot();
        setPrevPerCPU(prev?.per_cpu ?? null);
        setPrevCPU(prev?.cpu ?? null);
        setSnapshot(snap);

        // Aggregate CPU ring — drives the always-visible meter.
        const aggUsed = computeCPUUsed(prev?.cpu, snap.cpu);
        const nextAgg = [...aggCPUHistory(), aggUsed];
        if (nextAgg.length > HISTORY_LEN) nextAgg.shift();
        setAggCPUHistory(nextAgg);

        // Per-core ring — only rendered inside the popover, but kept
        // live regardless so the popover opens with history already
        // in place rather than starting empty.
        const used = snap.per_cpu.map((c, i) =>
          computeCPUUsed(prev?.per_cpu?.[i], c)
        );
        for (let i = 0; i < used.length; i++) {
          const row = perCPUHistory[i] ?? [];
          const next = [...row, used[i]];
          if (next.length > HISTORY_LEN) next.shift();
          setPerCPUHistory(i, next);
        }
        // Trim if cores disappeared (rare).
        if (perCPUHistory.length > used.length) {
          setPerCPUHistory((rows) => rows.slice(0, used.length));
        }

        // Mem usage ring.
        const memUsed = snap.mem.Total > 0
          ? 1 - (snap.mem.Available || snap.mem.Free) / snap.mem.Total
          : 0;
        const nextMem = [...memHistory(), memUsed];
        if (nextMem.length > HISTORY_LEN) nextMem.shift();
        setMemHistory(nextMem);

        // Net + disk rates. interval_ms is what the BE just used; we
        // also gate on prev existing because the very first snapshot
        // has no baseline.
        const dt = snap.interval_ms || 2000;
        if (prev) {
          const nrx = rateDelta(prev.net?.RxBytes, snap.net?.RxBytes ?? 0, dt);
          const ntx = rateDelta(prev.net?.TxBytes, snap.net?.TxBytes ?? 0, dt);
          setNetRxHistory(pushRing(netRxHistory(), nrx));
          setNetTxHistory(pushRing(netTxHistory(), ntx));

          const dr = rateDelta(prev.disk?.ReadBytes, snap.disk?.ReadBytes ?? 0, dt);
          const dw = rateDelta(prev.disk?.WriteBytes, snap.disk?.WriteBytes ?? 0, dt);
          setDiskReadHistory(pushRing(diskReadHistory(), dr));
          setDiskWriteHistory(pushRing(diskWriteHistory(), dw));

          // Per-interface rings — key by name. Build a lookup of the
          // previous snapshot's per-iface entries so we can diff
          // without an O(n²) scan.
          const prevNet = new Map((prev.per_net ?? []).map((n) => [n.name, n] as const));
          for (const n of snap.per_net ?? []) {
            const p = prevNet.get(n.name);
            const rx = rateDelta(p?.RxBytes, n.RxBytes, dt);
            const tx = rateDelta(p?.TxBytes, n.TxBytes, dt);
            const cur = perNetHistory[n.name] ?? { rx: [], tx: [] };
            setPerNetHistory(n.name, {
              rx: pushRing(cur.rx, rx),
              tx: pushRing(cur.tx, tx),
            });
          }

          const prevDisk = new Map((prev.per_disk ?? []).map((d) => [d.name, d] as const));
          for (const d of snap.per_disk ?? []) {
            const p = prevDisk.get(d.name);
            const r = rateDelta(p?.ReadBytes, d.ReadBytes, dt);
            const w = rateDelta(p?.WriteBytes, d.WriteBytes, dt);
            const cur = perDiskHistory[d.name] ?? { r: [], w: [] };
            setPerDiskHistory(d.name, {
              r: pushRing(cur.r, r),
              w: pushRing(cur.w, w),
            });
          }
        }

        setIntervalMS(snap.interval_ms);
        return;
      }
      case 'details_ok': {
        const d = m as Details;
        // Only update if this still matches the selected pid;
        // a click-then-click-elsewhere race could otherwise overwrite.
        if (d.pid === selectedPID()) setDetails(d);
        return;
      }
      case 'signal_ok':
        // BE will push a fresh snapshot shortly; nothing to do.
        return;
      case 'signal_err':
        // Lift the error into the details panel so the user sees it
        // somewhere; cheaper than another toast surface in v1.
        console.warn('wash-top: signal_err', m);
        return;
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    // Click-outside: dismiss the CPU popover when the user clicks
    // anywhere that isn't the popover itself or its trigger meter.
    // Registered on the host (light DOM ancestor of everything we
    // render) so it survives without crossing shadow boundaries.
    const onDocDown = (ev: Event) => {
      const target = ev.target as Node | null;
      if (!target) return;
      // Each open popover dismisses iff the click is outside it AND
      // outside its trigger. Three meters, three independent checks.
      if (cpuPopover() && !cpuPopoverEl?.contains(target) && !cpuTriggerEl?.contains(target)) {
        setCpuPopover(false);
      }
      if (netPopover() && !netPopoverEl?.contains(target) && !netTriggerEl?.contains(target)) {
        setNetPopover(false);
      }
      if (diskPopover() && !diskPopoverEl?.contains(target) && !diskTriggerEl?.contains(target)) {
        setDiskPopover(false);
      }
    };
    props.host.addEventListener('pointerdown', onDocDown, true);
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== 'Escape') return;
      if (cpuPopover()) setCpuPopover(false);
      if (netPopover()) setNetPopover(false);
      if (diskPopover()) setDiskPopover(false);
    };
    window.addEventListener('keydown', onKey);
    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      props.host.removeEventListener('pointerdown', onDocDown, true);
      window.removeEventListener('keydown', onKey);
    });
  });

  // ---- derived ----

  const procs = () => snapshot()?.procs ?? [];

  const filteredFlat = createMemo(() => filterSortProcs(procs(), filter(), sortKey(), sortDesc()));

  // Tree view: build children map; render depth-first.
  const treeRows = createMemo(() => {
    if (view() !== 'tree') return [];
    const rows = procs();
    if (!rows.length) return [];
    const byPID = new Map<number, ProcInfo>();
    const children = new Map<number, ProcInfo[]>();
    for (const p of rows) byPID.set(p.PID, p);
    for (const p of rows) {
      const list = children.get(p.PPID) ?? [];
      list.push(p);
      children.set(p.PPID, list);
    }
    // Roots: pids whose ppid isn't in our snapshot (pid 1, pid 2 kthread).
    const roots = rows.filter((p) => !byPID.has(p.PPID));
    roots.sort((a, b) => a.PID - b.PID);
    const out: { proc: ProcInfo; depth: number; hasKids: boolean }[] = [];
    const f = filter().trim().toLowerCase();
    const matches = (p: ProcInfo) =>
      !f ||
      p.Cmd.toLowerCase().includes(f) ||
      p.Comm.toLowerCase().includes(f) ||
      String(p.PID).includes(f) ||
      (p.User || '').toLowerCase().includes(f);
    const walk = (p: ProcInfo, depth: number) => {
      const kids = (children.get(p.PID) ?? []).slice().sort((a, b) => a.PID - b.PID);
      const hasKids = kids.length > 0;
      // Include the node if it matches OR any descendant matches —
      // otherwise the tree filter would hide context. Cheap two-pass.
      const anyMatch = matches(p) || subtreeMatches(p, children, matches);
      if (anyMatch) {
        out.push({ proc: p, depth, hasKids });
        if (!collapsed[p.PID]) {
          for (const k of kids) walk(k, depth + 1);
        }
      }
    };
    for (const r of roots) walk(r, 0);
    return out;
  });

  const intervalChoices = [1000, 2000, 5000, 10_000];

  const togglePause = () => {
    setPaused(!paused());
  };

  const onSort = (k: SortKey) => {
    if (sortKey() === k) setSortDesc(!sortDesc());
    else { setSortKey(k); setSortDesc(true); }
  };

  const onChangeInterval = (ms: number) => {
    setIntervalMS(ms);
    send({ kind: 'set_interval', ms });
  };

  const selectPID = (pid: number) => {
    setSelectedPID(pid);
    if (showDetails()) {
      send({ kind: 'details', id: `d-${pid}-${Date.now()}`, pid });
    }
  };

  const toggleDetails = () => {
    const next = !showDetails();
    setShowDetails(next);
    const pid = selectedPID();
    if (next && pid != null) {
      send({ kind: 'details', id: `d-${pid}-${Date.now()}`, pid });
    }
  };

  const askKill = () => {
    const pid = selectedPID();
    if (pid == null) return;
    const p = procs().find((q) => q.PID === pid);
    if (p) setKillTarget(p);
  };

  const doKill = (force: boolean) => {
    const t = killTarget();
    if (!t) return;
    send({
      kind: 'signal',
      id: `s-${t.PID}-${Date.now()}`,
      pid: t.PID,
      sig: force ? 'KILL' : 'TERM',
    });
    setKillTarget(null);
  };

  // ---- render ----

  const snap = () => snapshot();

  return (
    <>
      <div style={layoutStyle}>
        <div style={metersStyle}>
          <div
            ref={cpuTriggerEl}
            data-testid="top-cpu-meter"
            onClick={() => setCpuPopover(!cpuPopover())}
            title="Click for per-core breakdown"
            style={meterTriggerStyle}
          >
            <CPUMeter
              cur={snap()?.cpu}
              prev={prevCPU() ?? undefined}
              history={aggCPUHistory()}
              cores={snap()?.cpu_count ?? 0}
            />
          </div>
          <div style={meterCellStyle}>
            <MemMeter mem={snap()?.mem} history={memHistory()} load={snap()?.load} />
          </div>
          <div
            ref={netTriggerEl}
            data-testid="top-net-meter"
            onClick={() => setNetPopover(!netPopover())}
            title="Click for per-interface breakdown"
            style={meterTriggerStyle}
          >
            <NetMeter rx={netRxHistory()} tx={netTxHistory()} />
          </div>
          <div
            ref={diskTriggerEl}
            data-testid="top-disk-meter"
            onClick={() => setDiskPopover(!diskPopover())}
            title="Click for per-disk breakdown"
            style={meterTriggerStyle}
          >
            <DiskMeter r={diskReadHistory()} w={diskWriteHistory()} />
          </div>
        </div>
        <Show when={cpuPopover()}>
          <div ref={cpuPopoverEl} style={cpuPopoverStyle} data-testid="top-cpu-popover">
            <CPUGrid
              cur={snap()?.per_cpu ?? []}
              prev={prevPerCPU() ?? undefined}
              history={perCPUHistory}
            />
          </div>
        </Show>
        <Show when={netPopover()}>
          <div ref={netPopoverEl} style={cpuPopoverStyle} data-testid="top-net-popover">
            <NetGrid devices={snap()?.per_net ?? []} history={perNetHistory} />
          </div>
        </Show>
        <Show when={diskPopover()}>
          <div ref={diskPopoverEl} style={cpuPopoverStyle} data-testid="top-disk-popover">
            <DiskGrid devices={snap()?.per_disk ?? []} history={perDiskHistory} />
          </div>
        </Show>

        <div style={toolbarStyle}>
          <IconBtn
            title={view() === 'tree' ? 'Switch to flat list' : 'Switch to tree view'}
            onClick={() => setView(view() === 'tree' ? 'flat' : 'tree')}
            active={view() === 'tree'}
            data-testid="top-view-toggle"
          >
            {view() === 'tree' ? <ListTree size={14} /> : <ListIcon size={14} />}
          </IconBtn>
          <input
            type="text"
            placeholder="filter…"
            value={filter()}
            onInput={(e) => setFilter(e.currentTarget.value)}
            data-testid="top-filter"
            style={filterStyle}
          />
          <select
            value={intervalMS()}
            onInput={(e) => onChangeInterval(parseInt(e.currentTarget.value, 10))}
            style={selectStyle}
            title="Snapshot interval"
          >
            <For each={intervalChoices}>
              {(ms) => <option value={ms}>{ms / 1000}s</option>}
            </For>
          </select>
          <IconBtn
            title={paused() ? 'Resume' : 'Pause'}
            onClick={togglePause}
            active={paused()}
            data-testid="top-pause"
          >
            {paused() ? <Play size={14} /> : <Pause size={14} />}
          </IconBtn>
          <div style={{ flex: 1 }} />
          <Show when={selectedPID() != null}>
            <button
              type="button"
              onClick={askKill}
              data-testid="top-kill-btn"
              style={dangerBtnStyle}
              title="Send signal to selected process"
            >
              <Skull size={13} /> Kill {selectedPID()}
            </button>
          </Show>
          <IconBtn
            title={showDetails() ? 'Hide details' : 'Show details'}
            onClick={toggleDetails}
            active={showDetails()}
            data-testid="top-details-toggle"
          >
            {showDetails() ? <XIcon size={14} /> : <span style={{ font: `${tokens.fontSizeSm} ${tokens.fontMono}` }}>i</span>}
          </IconBtn>
        </div>

        <div style={bodyStyle}>
          <div style={listScrollStyle}>
            <ProcHeader sortKey={sortKey()} sortDesc={sortDesc()} onSort={onSort} />
            <Show
              when={view() === 'flat'}
              fallback={
                // <Index> (not <For>): each poll hands us a fresh procs
                // array of fresh ProcInfo objects, so a reference-keyed
                // <For> would tear down and rebuild every row's DOM ~every
                // 2s. <Index> keeps the DOM per position and updates the
                // row's props in place — the data the row shows genuinely
                // changes each tick anyway. row() is the per-position
                // accessor; the closures read it at event time.
                <Index each={treeRows()}>
                  {(row) => (
                    <ProcRow
                      proc={row().proc}
                      depth={row().depth}
                      hasKids={row().hasKids}
                      collapsed={!!collapsed[row().proc.PID]}
                      selected={selectedPID() === row().proc.PID}
                      onSelect={() => selectPID(row().proc.PID)}
                      onToggle={() => {
                        if (collapsed[row().proc.PID]) {
                          setCollapsed(row().proc.PID, undefined as unknown as true);
                        } else {
                          setCollapsed(row().proc.PID, true);
                        }
                      }}
                    />
                  )}
                </Index>
              }
            >
              <Index each={filteredFlat()}>
                {(p) => (
                  <ProcRow
                    proc={p()}
                    depth={0}
                    hasKids={false}
                    collapsed={false}
                    selected={selectedPID() === p().PID}
                    onSelect={() => selectPID(p().PID)}
                  />
                )}
              </Index>
            </Show>
          </div>
          <Show when={showDetails()}>
            <DetailsPane
              details={details()}
              proc={procs().find((p) => p.PID === selectedPID()) ?? null}
            />
          </Show>
        </div>

        <StatusBar snap={snap()} visible={view() === 'flat' ? filteredFlat().length : treeRows().length} paused={paused()} />
      </div>

      <Show when={killTarget()}>
        <ConfirmDialog
          title={`Signal PID ${killTarget()!.PID}?`}
          confirmLabel="Send TERM"
          cancelLabel="Cancel"
          onCancel={() => setKillTarget(null)}
          onConfirm={() => doKill(false)}
          data-testid="top-kill-confirm"
          confirmTestid="top-kill-term"
          cancelTestid="top-kill-cancel"
        >
          <div style={killBodyStyle}>
            <div style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}`, opacity: 0.85 }}>
              {killTarget()!.Cmd || killTarget()!.Comm}
            </div>
            <div style={{ 'margin-top': '8px', opacity: 0.7 }}>
              TERM lets the process clean up. Use Force only if it ignores TERM.
            </div>
            <div style={{ 'margin-top': '8px' }}>
              <button
                type="button"
                onClick={() => doKill(true)}
                data-testid="top-kill-force"
                style={dangerBtnStyle}
              >
                Force (SIGKILL)
              </button>
            </div>
          </div>
        </ConfirmDialog>
      </Show>
    </>
  );
};

// subtreeMatches: does any descendant of p satisfy `matches`?
// Used by the tree filter so a leaf match keeps its ancestors visible.
function subtreeMatches(
  p: ProcInfo,
  children: Map<number, ProcInfo[]>,
  matches: (p: ProcInfo) => boolean,
): boolean {
  const kids = children.get(p.PID);
  if (!kids) return false;
  for (const k of kids) {
    if (matches(k)) return true;
    if (subtreeMatches(k, children, matches)) return true;
  }
  return false;
}

// ---- CPU meters ----

// CPUMeter: the always-visible aggregate. Wide sparkline so the meter
// alone is informative; click reveals the per-core grid in a popover.
const CPUMeter: Component<{
  cur: CPURow | undefined;
  prev: CPURow | undefined;
  history: number[];
  cores: number;
}> = (props) => {
  const used = () => props.cur ? computeCPUUsed(props.prev, props.cur) : 0;
  return (
    <div style={{
      display: 'grid',
      'grid-template-columns': '40px 1fr 84px',
      'grid-template-rows': 'auto auto',
      'column-gap': '6px',
      'row-gap': '3px',
      'align-items': 'center',
    }}>
      <span style={cpuLabelStyle}>cpu</span>
      <Bar fraction={used()} color={cpuColor(used())} />
      <span style={cpuPctStyle}>{(used() * 100).toFixed(0)}% · {props.cores} cores</span>
      <div style={{ 'grid-column': '1 / -1' }}>
        <Sparkline data={props.history} stroke={cpuColor(used())} height={22} />
      </div>
    </div>
  );
};

// CPUGrid: the popover content — every core as its own bar+sparkline.
const CPUGrid: Component<{
  cur: CPURow[];
  prev: CPURow[] | undefined;
  history: number[][];
}> = (props) => {
  const used = createMemo(() =>
    props.cur.map((c, i) => computeCPUUsed(props.prev?.[i], c))
  );
  // 2-up at small widths, scales with the popover's grid container.
  const cols = () => {
    const n = props.cur.length;
    if (n <= 4) return 1;
    if (n <= 16) return 2;
    return 4;
  };
  return (
    <div style={{
      display: 'grid',
      'grid-template-columns': `repeat(${cols()}, minmax(220px, 1fr))`,
      gap: '8px 16px',
    }}>
      <For each={used()}>
        {(u, i) => (
          <div style={{ display: 'grid', 'grid-template-columns': '40px 1fr 40px', gap: '6px', 'align-items': 'center' }}>
            <span style={cpuLabelStyle}>cpu{i()}</span>
            <Bar fraction={u} color={cpuColor(u)} />
            <span style={cpuPctStyle}>{(u * 100).toFixed(0)}%</span>
            <div style={{ 'grid-column': '1 / -1', 'margin-top': '-2px' }}>
              <Sparkline data={props.history[i()] ?? []} stroke={cpuColor(u)} height={14} />
            </div>
          </div>
        )}
      </For>
    </div>
  );
};

// NetMeter / DiskMeter: same shape as the aggregate CPU meter — one
// label, one wide mirror sparkline, one rate readout. Both compose
// MirrorSparkline so the colors are the only thing that differs.
const NetMeter: Component<{ rx: number[]; tx: number[] }> = (props) => {
  const lastRx = () => props.rx[props.rx.length - 1] ?? 0;
  const lastTx = () => props.tx[props.tx.length - 1] ?? 0;
  return (
    <div style={rateMeterShellStyle}>
      <span style={cpuLabelStyle}>net</span>
      <span style={cpuPctStyle}>
        ↓{fmtRate(lastRx())} · ↑{fmtRate(lastTx())}
      </span>
      <div style={{ 'grid-column': '1 / -1' }}>
        <MirrorSparkline up={props.tx} down={props.rx} upColor="#c8a04a" downColor="#4a8ab0" height={22} />
      </div>
    </div>
  );
};

const DiskMeter: Component<{ r: number[]; w: number[] }> = (props) => {
  const lastR = () => props.r[props.r.length - 1] ?? 0;
  const lastW = () => props.w[props.w.length - 1] ?? 0;
  return (
    <div style={rateMeterShellStyle}>
      <span style={cpuLabelStyle}>disk</span>
      <span style={cpuPctStyle}>
        r {fmtRate(lastR())} · w {fmtRate(lastW())}
      </span>
      <div style={{ 'grid-column': '1 / -1' }}>
        <MirrorSparkline up={props.w} down={props.r} upColor="#c2772d" downColor="#7a9d4a" height={22} />
      </div>
    </div>
  );
};

// NetGrid / DiskGrid: per-device lists for the popovers. Each row
// renders the device name, current rate, and a small mirror
// sparkline. Sort by total recent activity so noisy interfaces
// surface at the top.
const NetGrid: Component<{
  devices: NetDev[];
  history: Record<string, { rx: number[]; tx: number[] }>;
}> = (props) => {
  const rows = createMemo(() => {
    const out = props.devices.map((d) => {
      const h = props.history[d.name] ?? { rx: [], tx: [] };
      const last = (h.rx[h.rx.length - 1] ?? 0) + (h.tx[h.tx.length - 1] ?? 0);
      return { dev: d, h, last };
    });
    out.sort((a, b) => b.last - a.last);
    return out;
  });
  return (
    <div style={popoverGridStyle}>
      <For each={rows()}>
        {(r) => (
          <div style={popoverRowStyle}>
            <div style={popoverRowHeaderStyle}>
              <span style={{ ...cpuLabelStyle, opacity: r.dev.real ? 1 : 0.55 }}>{r.dev.name}</span>
              <span style={cpuPctStyle}>
                ↓{fmtRate(r.h.rx[r.h.rx.length - 1] ?? 0)} · ↑{fmtRate(r.h.tx[r.h.tx.length - 1] ?? 0)}
              </span>
            </div>
            <MirrorSparkline up={r.h.tx} down={r.h.rx} upColor="#c8a04a" downColor="#4a8ab0" height={18} />
          </div>
        )}
      </For>
    </div>
  );
};

const DiskGrid: Component<{
  devices: DiskDev[];
  history: Record<string, { r: number[]; w: number[] }>;
}> = (props) => {
  const rows = createMemo(() => {
    const out = props.devices.map((d) => {
      const h = props.history[d.name] ?? { r: [], w: [] };
      const last = (h.r[h.r.length - 1] ?? 0) + (h.w[h.w.length - 1] ?? 0);
      return { dev: d, h, last };
    });
    out.sort((a, b) => b.last - a.last);
    return out;
  });
  return (
    <div style={popoverGridStyle}>
      <For each={rows()}>
        {(r) => (
          <div style={popoverRowStyle}>
            <div style={popoverRowHeaderStyle}>
              <span style={{ ...cpuLabelStyle, opacity: r.dev.real ? 1 : 0.55 }}>{r.dev.name}</span>
              <span style={cpuPctStyle}>
                r {fmtRate(r.h.r[r.h.r.length - 1] ?? 0)} · w {fmtRate(r.h.w[r.h.w.length - 1] ?? 0)}
              </span>
            </div>
            <MirrorSparkline up={r.h.w} down={r.h.r} upColor="#c2772d" downColor="#7a9d4a" height={18} />
          </div>
        )}
      </For>
    </div>
  );
};

const MemMeter: Component<{ mem?: MemRow; history: number[]; load?: LoadRow }> = (props) => {
  const used = () => {
    const m = props.mem;
    if (!m || !m.Total) return 0;
    return 1 - (m.Available || m.Free) / m.Total;
  };
  const swapUsed = () => {
    const m = props.mem;
    if (!m || !m.SwapTotal) return 0;
    return 1 - m.SwapFree / m.SwapTotal;
  };
  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
      <div style={{ display: 'grid', 'grid-template-columns': '40px 1fr 80px', gap: '6px', 'align-items': 'center' }}>
        <span style={cpuLabelStyle}>mem</span>
        <Bar fraction={used()} color="#7a9d4a" />
        <span style={cpuPctStyle}>
          {props.mem ? `${fmtBytes((props.mem.Total - (props.mem.Available || props.mem.Free)))} / ${fmtBytes(props.mem.Total)}` : '—'}
        </span>
      </div>
      <Sparkline data={props.history} stroke="#7a9d4a" height={20} />
      <Show when={props.mem && props.mem.SwapTotal > 0}>
        <div style={{ display: 'grid', 'grid-template-columns': '40px 1fr 80px', gap: '6px', 'align-items': 'center' }}>
          <span style={cpuLabelStyle}>swap</span>
          <Bar fraction={swapUsed()} color="#c2772d" />
          <span style={cpuPctStyle}>
            {props.mem ? `${fmtBytes(props.mem.SwapTotal - props.mem.SwapFree)} / ${fmtBytes(props.mem.SwapTotal)}` : '—'}
          </span>
        </div>
      </Show>
      <Show when={props.load}>
        <div style={{ opacity: 0.7, font: `${tokens.fontSizeSm} ${tokens.fontMono}`, 'padding-top': '2px' }}>
          load: {props.load!.L1.toFixed(2)} {props.load!.L5.toFixed(2)} {props.load!.L15.toFixed(2)}
          {' · '}
          {props.load!.Running}/{props.load!.Threads} threads
        </div>
      </Show>
    </div>
  );
};

// Bar: a one-line horizontal usage bar. fraction in 0..1.
const Bar: Component<{ fraction: number; color: string }> = (props) => (
  <div style={{
    height: '10px',
    background: tokens.bgMenu,
    'border-radius': '2px',
    overflow: 'hidden',
    border: `1px solid ${tokens.borderMenu}`,
  }}>
    <div style={{
      width: `${Math.max(0, Math.min(1, props.fraction)) * 100}%`,
      height: '100%',
      background: props.color,
      transition: 'width 0.2s linear',
    }} />
  </div>
);

// MirrorSparkline: iftop-style RX-above / TX-below. Both arrays are
// scaled by the same per-window max so peaks are visually comparable
// across the pair. Empty / all-zero windows render a flat axis.
const MirrorSparkline: Component<{
  up: number[];
  down: number[];
  upColor: string;
  downColor: string;
  height?: number;
}> = (props) => {
  const w = 120;
  const h = () => props.height ?? 24;
  // Floor at 1024 B/s so an idle interface doesn't render trivial
  // jitter as full-height blips.
  const max = () => {
    let m = 1024;
    for (const v of props.up) if (v > m) m = v;
    for (const v of props.down) if (v > m) m = v;
    return m;
  };
  const halfPath = (data: number[], topHalf: boolean): string => {
    if (data.length < 1) return '';
    const step = w / Math.max(1, HISTORY_LEN - 1);
    const startOffset = HISTORY_LEN - data.length;
    const mid = h() / 2;
    const m = max();
    const points: string[] = [];
    for (let i = 0; i < data.length; i++) {
      const x = (i + startOffset) * step;
      const amp = (data[i] / m) * (h() / 2);
      const y = topHalf ? mid - amp : mid + amp;
      points.push(`${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`);
    }
    return points.join(' ');
  };
  return (
    <svg width="100%" height={h()} viewBox={`0 0 ${w} ${h()}`} preserveAspectRatio="none"
      style={{ display: 'block', background: tokens.bgMenu, 'border-radius': '2px', border: `1px solid ${tokens.borderMenu}` }}
    >
      <line x1="0" y1={h() / 2} x2={w} y2={h() / 2} stroke={tokens.borderMenu} stroke-width="0.5" />
      <path d={halfPath(props.down, false)} stroke={props.downColor} fill="none" stroke-width="1" />
      <path d={halfPath(props.up, true)} stroke={props.upColor} fill="none" stroke-width="1" />
    </svg>
  );
};

// Sparkline: a tiny SVG plot of points in [0..1]. Renders a smooth
// polyline; gracefully no-ops while history is empty.
const Sparkline: Component<{ data: number[]; stroke: string; height?: number }> = (props) => {
  const w = 120;
  const h = () => props.height ?? 16;
  const path = () => {
    const d = props.data;
    if (d.length < 2) return '';
    const step = w / Math.max(1, HISTORY_LEN - 1);
    const points: string[] = [];
    const startOffset = HISTORY_LEN - d.length;
    for (let i = 0; i < d.length; i++) {
      const x = (i + startOffset) * step;
      const y = h() - d[i] * h();
      points.push(`${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`);
    }
    return points.join(' ');
  };
  return (
    <svg width="100%" height={h()} viewBox={`0 0 ${w} ${h()}`} preserveAspectRatio="none"
      style={{ display: 'block', background: tokens.bgMenu, 'border-radius': '2px', border: `1px solid ${tokens.borderMenu}` }}
    >
      <path d={path()} stroke={props.stroke} fill="none" stroke-width="1" />
    </svg>
  );
};

// ---- process list ----

const ProcHeader: Component<{
  sortKey: SortKey;
  sortDesc: boolean;
  onSort: (k: SortKey) => void;
}> = (props) => {
  const Cell = (p: { k: SortKey; label: string; width?: string; align?: 'left' | 'right' }) => (
    <button
      type="button"
      onClick={() => props.onSort(p.k)}
      data-testid={`top-sort-${p.k}`}
      data-active={props.sortKey === p.k}
      style={{
        ...headerCellStyle,
        'text-align': p.align ?? 'left',
        width: p.width,
        background: props.sortKey === p.k ? tokens.bgRowSelected : 'transparent',
      }}
    >
      {p.label}{props.sortKey === p.k ? (props.sortDesc ? ' ↓' : ' ↑') : ''}
    </button>
  );
  return (
    <div style={rowStyle(true, false)}>
      <Cell k="pid" label="PID" width="56px" align="right" />
      <Cell k="user" label="USER" width="76px" />
      <span style={{ ...headerCellStyle, width: '28px' }}>ST</span>
      <Cell k="cpu" label="%CPU" width="56px" align="right" />
      <Cell k="mem" label="%MEM" width="56px" align="right" />
      <span style={{ ...headerCellStyle, width: '64px', 'text-align': 'right' }}>RSS</span>
      <Cell k="time" label="TIME" width="60px" align="right" />
      <Cell k="cmd" label="COMMAND" />
    </div>
  );
};

const ProcRow: Component<{
  proc: ProcInfo;
  depth: number;
  hasKids: boolean;
  collapsed: boolean;
  selected: boolean;
  onSelect: () => void;
  onToggle?: () => void;
}> = (props) => {
  const indent = () => props.depth * 12;
  return (
    <div
      style={rowStyle(false, props.selected)}
      data-pid={props.proc.PID}
      data-testid={`top-row-${props.proc.PID}`}
      onClick={props.onSelect}
    >
      <span style={{ ...cellStyle, width: '56px', 'text-align': 'right' }}>{props.proc.PID}</span>
      <span style={{ ...cellStyle, width: '76px' }}>{props.proc.User || '?'}</span>
      <span style={{ ...cellStyle, width: '28px', color: stateColor(props.proc.State) }}>{props.proc.State}</span>
      <span style={{ ...cellStyle, width: '56px', 'text-align': 'right', color: cpuColor(Math.min(1, props.proc.CPU / 100)) }}>
        {fmtCPU(props.proc.CPU)}
      </span>
      <span style={{ ...cellStyle, width: '56px', 'text-align': 'right' }}>{props.proc.MemPct.toFixed(1)}</span>
      <span style={{ ...cellStyle, width: '64px', 'text-align': 'right' }}>{fmtBytes(props.proc.RSS)}</span>
      <span style={{ ...cellStyle, width: '60px', 'text-align': 'right' }}>{fmtTime(props.proc.TimeJiff)}</span>
      <span style={{ ...cellStyle, flex: 1, overflow: 'hidden', 'text-overflow': 'ellipsis', 'white-space': 'nowrap', display: 'flex', 'align-items': 'center' }}>
        <span style={{ 'padding-left': `${indent()}px`, display: 'inline-flex', 'align-items': 'center', gap: '2px' }}>
          <Show when={props.hasKids} fallback={<span style={{ width: '12px', display: 'inline-block' }} />}>
            <button
              type="button"
              onClick={(e) => { e.stopPropagation(); props.onToggle?.(); }}
              style={chevronStyle}
              data-testid={`top-tree-toggle-${props.proc.PID}`}
            >
              {props.collapsed ? <ChevronRight size={12} /> : <ChevronDown size={12} />}
            </button>
          </Show>
          <span>{props.proc.Cmd || props.proc.Comm}</span>
        </span>
      </span>
    </div>
  );
};

function stateColor(state: string): string {
  switch (state) {
    case 'R': return '#7ab06c';
    case 'D': return '#c8a04a';
    case 'Z': return '#d97757';
    case 'T': return '#c8a04a';
    case 'I': return tokens.fgMuted;
    default: return tokens.fg;
  }
}

// ---- details pane ----

const DetailsPane: Component<{ details: Details | null; proc: ProcInfo | null }> = (props) => {
  const Row = (p: { k: string; v: string | number | undefined }) => (
    <Show when={p.v !== undefined && p.v !== ''}>
      <div style={{ display: 'grid', 'grid-template-columns': '110px 1fr', gap: '6px', 'padding-bottom': '2px' }}>
        <span style={{ opacity: 0.6 }}>{p.k}</span>
        <span style={{ 'word-break': 'break-all', font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>{String(p.v)}</span>
      </div>
    </Show>
  );
  return (
    <div style={detailsPaneStyle}>
      <Show
        when={props.proc}
        fallback={<div style={{ opacity: 0.6, padding: '8px' }}>select a process to inspect</div>}
      >
        <div style={{ font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`, 'padding-bottom': '6px' }}>
          PID {props.proc!.PID} — {props.proc!.Comm}
        </div>
        <Row k="user" v={`${props.proc!.User} (uid ${props.proc!.UID})`} />
        <Row k="state" v={props.proc!.State} />
        <Row k="threads" v={props.proc!.Threads} />
        <Row k="nice" v={props.proc!.Nice} />
        <Row k="cpu" v={`${fmtCPU(props.proc!.CPU)}%`} />
        <Row k="mem" v={`${fmtBytes(props.proc!.RSS)} (${props.proc!.MemPct.toFixed(1)}%)`} />
        <Row k="virt" v={fmtBytes(props.proc!.VSize)} />
        <Show when={props.proc!.StartUnix > 0}>
          <Row k="started" v={new Date(props.proc!.StartUnix * 1000).toLocaleString()} />
        </Show>
        <Row k="cmd" v={props.proc!.Cmd} />
        <Show when={props.details}>
          <div style={detailsDividerStyle} />
          <Row k="exe" v={props.details!.exe} />
          <Row k="cwd" v={props.details!.cwd} />
          <Row k="fds" v={props.details!.fds} />
          <Row k="wchan" v={props.details!.wchan} />
          <Row k="oom_score" v={props.details!.oom_score} />
          <Row k="cgroup" v={props.details!.cgroup} />
          <Show when={props.details!.io && Object.keys(props.details!.io!).length > 0}>
            <div style={detailsDividerStyle} />
            <div style={{ opacity: 0.6, 'padding-bottom': '4px' }}>io</div>
            <For each={Object.entries(props.details!.io!)}>
              {([k, v]) => <Row k={k} v={v} />}
            </For>
          </Show>
          <Show when={props.details!.status && Object.keys(props.details!.status!).length > 0}>
            <div style={detailsDividerStyle} />
            <div style={{ opacity: 0.6, 'padding-bottom': '4px' }}>status</div>
            <For each={statusKeysOfInterest}>
              {(k) => <Row k={k} v={props.details!.status?.[k]} />}
            </For>
          </Show>
        </Show>
      </Show>
    </div>
  );
};

// statusKeysOfInterest: /proc/<pid>/status is huge; show the
// commonly-useful subset, in order. Anything else is one expand-click
// away (future: a "show all" toggle).
const statusKeysOfInterest = [
  'State', 'Tgid', 'Pid', 'PPid', 'TracerPid', 'Uid', 'Gid', 'FDSize',
  'Groups', 'VmPeak', 'VmSize', 'VmRSS', 'VmData', 'VmStk', 'VmExe',
  'Threads', 'voluntary_ctxt_switches', 'nonvoluntary_ctxt_switches',
];

// ---- status bar ----

const StatusBar: Component<{ snap: Snapshot | null; visible: number; paused: boolean }> = (props) => (
  <div style={statusBarStyle} data-testid="top-statusbar">
    <span>{props.snap ? `${props.snap.procs.length} procs` : 'loading…'}</span>
    <span style={{ opacity: 0.5 }}>·</span>
    <span>{props.visible} visible</span>
    <Show when={props.snap}>
      <span style={{ opacity: 0.5 }}>·</span>
      <span>{props.snap!.cpu_count} cores</span>
    </Show>
    <Show when={props.paused}>
      <span style={{ opacity: 0.5 }}>·</span>
      <span style={{ color: tokens.borderDanger }}>paused</span>
    </Show>
    <div style={{ flex: 1 }} />
    <Show when={props.snap}>
      <span style={{ opacity: 0.5 }}>updated {new Date(props.snap!.ts * 1000).toLocaleTimeString()}</span>
    </Show>
  </div>
);

// ---- styling ----

const layoutStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-rows': 'auto auto 1fr auto',
  height: '100%',
  background: tokens.bgWindow,
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

const metersStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': 'repeat(auto-fit, minmax(220px, 1fr))',
  gap: '0',
  padding: '8px 10px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  position: 'relative',
};

// meterCellStyle is the static (non-clickable) wrapper used by the
// Mem meter. meterTriggerStyle adds the hover-cursor + the same
// padded slot so clickable + static meters line up identically.
const meterCellStyle: JSX.CSSProperties = {
  padding: '0 12px',
  'border-right': `1px solid ${tokens.borderMenu}`,
};

const meterTriggerStyle: JSX.CSSProperties = {
  ...meterCellStyle,
  cursor: 'pointer',
  'user-select': 'none',
};

// rateMeterShellStyle is the inner grid used by NetMeter / DiskMeter
// (and the CPU meter mirrors its tile shape). Kept side-by-side with
// cpuLabelStyle / cpuPctStyle so all three header meters look uniform.
const rateMeterShellStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '40px 1fr',
  'grid-template-rows': 'auto auto',
  'column-gap': '6px',
  'row-gap': '3px',
  'align-items': 'center',
};

const popoverGridStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': 'repeat(auto-fit, minmax(260px, 1fr))',
  gap: '8px 16px',
};

const popoverRowStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '2px',
};

const popoverRowHeaderStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'baseline',
  'justify-content': 'space-between',
  gap: '8px',
};

// cpuPopoverStyle anchors the per-core grid below the meters row.
// position:absolute against the layoutStyle grid container (which is
// position:relative because metersStyle establishes it via the parent
// host). max-height keeps a 256-core box from blowing past the window.
const cpuPopoverStyle: JSX.CSSProperties = {
  position: 'absolute',
  top: '70px',
  left: '8px',
  right: '8px',
  'max-height': '50%',
  overflow: 'auto',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderFocus}`,
  'border-radius': `${tokens.radiusMd}px`,
  'box-shadow': tokens.shadowMenu,
  padding: '10px 12px',
  'z-index': 10,
  animation: tokens.animPopIn,
};

const toolbarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '4px 8px',
  'border-bottom': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
};

const bodyStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '1fr',
  'grid-auto-flow': 'column',
  'grid-template-rows': '1fr',
  overflow: 'hidden',
};

const listScrollStyle: JSX.CSSProperties = {
  overflow: 'auto',
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
};

const detailsPaneStyle: JSX.CSSProperties = {
  width: '320px',
  'min-width': '320px',
  'border-left': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
  padding: '8px 10px',
  overflow: 'auto',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

const detailsDividerStyle: JSX.CSSProperties = {
  height: '1px',
  background: tokens.borderMenu,
  margin: '8px 0',
};

function rowStyle(header: boolean, selected: boolean): JSX.CSSProperties {
  return {
    display: 'flex',
    'align-items': 'center',
    gap: '6px',
    padding: '2px 8px',
    'border-bottom': header ? `1px solid ${tokens.borderMenu}` : 'none',
    background: header ? tokens.bgMenu : selected ? tokens.bgRowSelected : 'transparent',
    cursor: header ? 'default' : 'pointer',
    'user-select': 'none',
    position: header ? 'sticky' : 'static',
    top: header ? 0 : undefined,
    'z-index': header ? 1 : undefined,
  } as JSX.CSSProperties;
}

const headerCellStyle: JSX.CSSProperties = {
  display: 'inline-block',
  border: 'none',
  background: 'transparent',
  color: tokens.fgMuted,
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
  cursor: 'pointer',
  padding: '0 2px',
};

const cellStyle: JSX.CSSProperties = {
  display: 'inline-block',
  overflow: 'hidden',
  'white-space': 'nowrap',
};

const cpuLabelStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  opacity: 0.7,
};

const cpuPctStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  'text-align': 'right',
  opacity: 0.85,
};

const filterStyle: JSX.CSSProperties = {
  width: '200px',
  background: tokens.bgWindow,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '2px 6px',
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
  outline: 'none',
};

const selectStyle: JSX.CSSProperties = {
  background: tokens.bgWindow,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '2px 4px',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

const iconBtnStyleBase: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  'justify-content': 'center',
  width: '24px',
  height: '22px',
  background: 'transparent',
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusSm}px`,
  cursor: 'pointer',
};

const IconBtn: Component<{
  onClick: () => void;
  active?: boolean;
  title?: string;
  'data-testid'?: string;
  children: JSX.Element;
}> = (props) => (
  <button
    type="button"
    title={props.title}
    onClick={props.onClick}
    data-testid={props['data-testid']}
    style={{ ...iconBtnStyleBase, background: props.active ? tokens.bgRowSelected : 'transparent' }}
  >
    {props.children}
  </button>
);

const chevronStyle: JSX.CSSProperties = {
  border: 'none',
  background: 'transparent',
  color: tokens.fg,
  padding: 0,
  'line-height': 0,
  width: '12px',
  height: '12px',
  cursor: 'pointer',
};

const dangerBtnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
  background: tokens.bgDanger,
  color: tokens.fg,
  border: `1px solid ${tokens.borderDanger}`,
  'border-radius': `${tokens.radiusSm}px`,
  padding: '3px 8px',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const statusBarStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '6px',
  padding: '2px 8px',
  'border-top': `1px solid ${tokens.borderMenu}`,
  background: tokens.bgMenu,
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  opacity: 0.85,
};

const killBodyStyle: JSX.CSSProperties = {
  'max-width': '420px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
};

// ---- custom element ----

defineWashApp('wash-app-top', (props) => <App {...props} />, {
  style: `display:block;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
