// wash-app-disks: storage information.
//
// Layout:
//   [ left: topology tree — Disks (disk→partition) + Volumes (md/lvm/btrfs/zfs) ]
//   [ right: detail pane for the selected row — identity, fullness, I/O ]
//
// Snapshots arrive every ~3s from the BE (cumulative I/O counters; the FE
// diffs consecutive snapshots for read/write rates, like wash-top). The
// stream pauses while the window is minimized.

import { For, Match, Show, Switch, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { Component, JSX } from 'solid-js';
import { defineWashApp, tokens } from '@wash/ui';
import {
  HardDrive,
  HardDriveDownload,
  Layers,
  Boxes,
  Database,
  FolderTree,
  CircleDot,
} from 'lucide-solid';
import { buildRows, findRow, usagePct, type Row } from './topology.ts';
import type {
  Snapshot,
  Disk,
  Partition,
  Mount,
  MDArray,
  LVMVG,
  LVMLV,
  BtrfsFS,
  BtrfsSubvol,
  ZPool,
  ZVdev,
  ZDataset,
  SmartReport,
} from './types.ts';

// Per-disk SMART state in the FE store.
interface SmartState {
  loading?: boolean;
  report?: SmartReport;
  error?: string;
}

// ---- formatting ----

const HISTORY_LEN = 48;

function fmtBytes(n: number): string {
  if (!n || n < 0) return n === 0 ? '0 B' : '—';
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(i === 0 ? 0 : v < 10 ? 2 : v < 100 ? 1 : 0)} ${u[i]}`;
}

function fmtRate(bps: number): string {
  if (bps < 1) return '0';
  return `${fmtBytes(bps)}/s`;
}

// rateDelta: bytes/s from two cumulative samples dtMS apart. Guards against
// counter resets (returns 0) and missing baselines.
function rateDelta(prev: number | undefined, cur: number, dtMS: number): number {
  if (prev === undefined || cur < prev || dtMS <= 0) return 0;
  return ((cur - prev) / dtMS) * 1000;
}

function pushRing(buf: number[], v: number): number[] {
  const next = [...buf, v];
  if (next.length > HISTORY_LEN) next.shift();
  return next;
}

// ---- row icon ----

function RowIcon(props: { row: Row }): JSX.Element {
  const k = props.row.kind;
  const size = 14;
  switch (k) {
    case 'disk': {
      const d = props.row.data as Disk;
      return d.rotational ? <HardDrive size={size} /> : <HardDriveDownload size={size} />;
    }
    case 'part':
      return <CircleDot size={size} />;
    case 'md':
      return <Layers size={size} />;
    case 'lvm-vg':
    case 'lvm-lv':
      return <Boxes size={size} />;
    case 'btrfs':
    case 'btrfs-subvol':
      return <FolderTree size={size} />;
    case 'zpool':
    case 'zvdev':
    case 'zdataset':
      return <Database size={size} />;
    default:
      return <span />;
  }
}

// ---- fullness bar ----

const FullnessBar: Component<{ used: number; total: number }> = (props) => {
  const pct = () => usagePct(props.used, props.total);
  const color = () => {
    const p = pct();
    if (p > 90) return tokens.borderDanger;
    if (p > 75) return '#c8a04a';
    return '#3a9d8f';
  };
  return (
    <div data-testid="disks-fullness">
      <div
        style={{
          height: '10px',
          width: '100%',
          background: tokens.bgMenu,
          border: `1px solid ${tokens.borderMenu}`,
          'border-radius': '3px',
          overflow: 'hidden',
        }}
      >
        <div style={{ height: '100%', width: `${pct()}%`, background: color() }} />
      </div>
      <div style={{ font: `${tokens.fontSizeSm} ${tokens.fontMono}`, opacity: 0.8, 'padding-top': '3px' }}>
        {fmtBytes(props.used)} / {fmtBytes(props.total)} ({pct().toFixed(0)}%)
      </div>
    </div>
  );
};

// ---- tiny sparkline (disk I/O) ----

const Spark: Component<{ data: number[]; color: string }> = (props) => {
  const w = 220;
  const h = 36;
  const path = createMemo(() => {
    const d = props.data;
    if (d.length < 2) return '';
    const max = Math.max(1, ...d);
    const step = w / Math.max(1, HISTORY_LEN - 1);
    const off = HISTORY_LEN - d.length;
    return d
      .map((v, i) => `${i === 0 ? 'M' : 'L'} ${((off + i) * step).toFixed(1)} ${(h - (v / max) * h).toFixed(1)}`)
      .join(' ');
  });
  return (
    <svg
      width={w}
      height={h}
      viewBox={`0 0 ${w} ${h}`}
      style={{ display: 'block', background: tokens.bgMenu, 'border-radius': '2px', border: `1px solid ${tokens.borderMenu}` }}
    >
      <path d={path()} fill="none" stroke={props.color} stroke-width="1.5" />
    </svg>
  );
};

// ---- detail rows ----

const KV: Component<{ k: string; v: JSX.Element | string }> = (props) => (
  <div style={{ display: 'flex', gap: '10px', padding: '3px 0', 'font-size': tokens.fontSizeBase }}>
    <span style={{ width: '110px', flex: '0 0 110px', opacity: 0.6 }}>{props.k}</span>
    <span style={{ 'word-break': 'break-all', font: `${tokens.fontSizeBase} ${tokens.fontMono}` }}>{props.v}</span>
  </div>
);

function MountBlock(props: { mount: Mount | null }): JSX.Element {
  return (
    <Show when={props.mount} fallback={<KV k="mounted" v="not mounted" />}>
      {(m) => (
        <>
          <KV k="mounted at" v={m().point} />
          <KV k="options" v={m().opts} />
          <Show when={m().fs_total > 0}>
            <div style={{ padding: '6px 0' }}>
              <FullnessBar used={m().fs_used} total={m().fs_total} />
            </div>
          </Show>
        </>
      )}
    </Show>
  );
}

// ---- app ----

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  const [snapshot, setSnapshot] = createSignal<Snapshot | null>(null);
  const [selectedId, setSelectedId] = createSignal<string | null>(null);
  // Per-disk I/O rate rings, keyed by device name.
  const [ioHist, setIoHist] = createStore<Record<string, { r: number[]; w: number[] }>>({});
  // Per-disk SMART results, keyed by device name.
  const [smart, setSmart] = createStore<Record<string, SmartState>>({});

  const checkSmart = (name: string) => {
    setSmart(name, { loading: true });
    send({ kind: 'smart', id: `smart-${name}-${snapshot()?.ts ?? 0}`, name });
  };

  const handleBE = (m: any) => {
    if (m?.kind === 'smart_ok') {
      setSmart(m.name, { report: m.report as SmartReport });
      return;
    }
    if (m?.kind === 'smart_err') {
      setSmart(m.name, { error: String(m.error ?? 'SMART read failed') });
      return;
    }
    if (m?.kind !== 'snapshot') return;
    const snap = m as Snapshot;
    const prev = snapshot();
    const dt = snap.interval_ms || 3000;
    if (prev) {
      const prevDisks = new Map((prev.disks ?? []).map((d) => [d.name, d] as const));
      for (const d of snap.disks ?? []) {
        const p = prevDisks.get(d.name);
        const r = rateDelta(p?.read_bytes, d.read_bytes, dt);
        const w = rateDelta(p?.write_bytes, d.write_bytes, dt);
        const cur = ioHist[d.name] ?? { r: [], w: [] };
        setIoHist(d.name, { r: pushRing(cur.r, r), w: pushRing(cur.w, w) });
      }
    }
    setSnapshot(snap);
    // Default selection: first disk.
    if (!selectedId() && (snap.disks?.length ?? 0) > 0) {
      setSelectedId(`disk:${snap.disks![0].name}`);
    }
  };

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail);
    props.host.addEventListener('wash:msg', onMsg);
    send({ kind: 'request_snapshot' });
    onCleanup(() => props.host.removeEventListener('wash:msg', onMsg));
  });

  const rows = createMemo(() => buildRows(snapshot()));
  const selected = createMemo(() => findRow(rows(), selectedId()));

  return (
    <div style={rootStyle}>
      <div style={listStyle} data-testid="disks-list">
        <For each={rows()}>
          {(row) => (
            <Show
              when={row.kind !== 'section'}
              fallback={<div style={sectionStyle} data-testid={`disks-section-${row.id}`}>{row.name}</div>}
            >
              <div
                style={rowStyle(row, selectedId() === row.id)}
                data-testid={`disks-row-${row.id}`}
                onClick={() => row.selectable && setSelectedId(row.id)}
              >
                <span style={{ display: 'inline-flex', width: `${row.depth * 14}px` }} />
                <RowIcon row={row} />
                <span style={{ 'margin-left': '6px' }}>{row.name}</span>
                <RowTrailing row={row} />
              </div>
            </Show>
          )}
        </For>
      </div>
      <div style={detailStyle}>
        <Show when={selected()} fallback={<div style={{ opacity: 0.5, padding: '20px' }}>Select a device.</div>}>
          {(row) => (
            <Detail
              row={row()}
              ioHist={ioHist}
              smart={smart}
              smartCap={snapshot()?.capabilities?.smart ?? false}
              onCheckSmart={checkSmart}
            />
          )}
        </Show>
      </div>
    </div>
  );
};

// RowTrailing shows a compact size/usage hint on the right of a row.
const RowTrailing: Component<{ row: Row }> = (props) => {
  const text = () => {
    const r = props.row;
    switch (r.kind) {
      case 'disk':
        return fmtBytes((r.data as Disk).size);
      case 'part': {
        const p = r.data as Partition;
        return p.mount ? p.fstype || 'mounted' : fmtBytes(p.size);
      }
      case 'md':
        return (r.data as MDArray).level;
      case 'lvm-vg':
        return fmtBytes((r.data as LVMVG).size);
      case 'lvm-lv':
        return fmtBytes((r.data as LVMLV).size);
      case 'btrfs':
        return fmtBytes((r.data as BtrfsFS).size);
      case 'zpool':
        return (r.data as ZPool).state;
      case 'zdataset':
        return fmtBytes((r.data as ZDataset).used);
      default:
        return '';
    }
  };
  return <span style={{ 'margin-left': 'auto', opacity: 0.55, font: `${tokens.fontSizeSm} ${tokens.fontMono}` }}>{text()}</span>;
};

// ---- detail pane (switches on row kind) ----

const Detail: Component<{
  row: Row;
  ioHist: Record<string, { r: number[]; w: number[] }>;
  smart: Record<string, SmartState>;
  smartCap: boolean;
  onCheckSmart: (name: string) => void;
}> = (props) => {
  return (
    <div style={{ padding: '16px 20px', overflow: 'auto', height: '100%', 'box-sizing': 'border-box' }}>
      <Switch>
        <Match when={props.row.kind === 'disk'}>
          <DiskDetail
            disk={props.row.data as Disk}
            ioHist={props.ioHist}
            smart={props.smart[(props.row.data as Disk).name]}
            smartCap={props.smartCap}
            onCheckSmart={props.onCheckSmart}
          />
        </Match>
        <Match when={props.row.kind === 'part'}><PartDetail part={props.row.data as Partition} /></Match>
        <Match when={props.row.kind === 'md'}><MDDetail a={props.row.data as MDArray} /></Match>
        <Match when={props.row.kind === 'lvm-vg'}><VGDetail vg={props.row.data as LVMVG} /></Match>
        <Match when={props.row.kind === 'lvm-lv'}><LVDetail lv={props.row.data as LVMLV} /></Match>
        <Match when={props.row.kind === 'btrfs'}><BtrfsDetail fs={props.row.data as BtrfsFS} /></Match>
        <Match when={props.row.kind === 'btrfs-subvol'}><Title t={`subvolume ${(props.row.data as BtrfsSubvol).path}`} /></Match>
        <Match when={props.row.kind === 'zpool'}><ZPoolDetail p={props.row.data as ZPool} /></Match>
        <Match when={props.row.kind === 'zvdev'}><ZVdevDetail v={props.row.data as ZVdev} /></Match>
        <Match when={props.row.kind === 'zdataset'}><ZDatasetDetail d={props.row.data as ZDataset} /></Match>
      </Switch>
    </div>
  );
};

const Title: Component<{ t: string }> = (props) => (
  <div data-testid="disks-detail-title" style={{ font: `600 ${tokens.fontSizeMd} ${tokens.fontSans}`, 'padding-bottom': '10px' }}>{props.t}</div>
);

const DiskDetail: Component<{
  disk: Disk;
  ioHist: Record<string, { r: number[]; w: number[] }>;
  smart: SmartState | undefined;
  smartCap: boolean;
  onCheckSmart: (name: string) => void;
}> = (props) => {
  const hist = () => props.ioHist[props.disk.name] ?? { r: [], w: [] };
  const lastR = () => hist().r[hist().r.length - 1] ?? 0;
  const lastW = () => hist().w[hist().w.length - 1] ?? 0;
  return (
    <>
      <Title t={`${props.disk.model || props.disk.name} (${props.disk.path})`} />
      <KV k="size" v={fmtBytes(props.disk.size)} />
      <KV k="type" v={props.disk.rotational ? 'HDD (rotational)' : 'SSD / flash'} />
      <KV k="transport" v={props.disk.transport || 'unknown'} />
      <KV k="removable" v={props.disk.removable ? 'yes' : 'no'} />
      <Show when={props.disk.serial}><KV k="serial" v={props.disk.serial} /></Show>
      <Show when={props.disk.wwn}><KV k="wwn" v={props.disk.wwn} /></Show>
      <Show when={props.disk.fstype}><KV k="filesystem" v={props.disk.fstype} /></Show>
      <Show when={(props.disk.holders?.length ?? 0) > 0}><KV k="used by" v={(props.disk.holders ?? []).join(', ')} /></Show>
      <Show when={props.disk.mount}><MountBlock mount={props.disk.mount} /></Show>
      <div style={{ 'padding-top': '12px' }}>
        <div style={{ opacity: 0.6, 'padding-bottom': '4px', 'font-size': tokens.fontSizeBase }}>
          I/O — read {fmtRate(lastR())} · write {fmtRate(lastW())}
        </div>
        <Spark data={hist().r} color="#4a8ab0" />
        <div style={{ height: '4px' }} />
        <Spark data={hist().w} color="#d97757" />
      </div>
      <Show when={props.smartCap || props.disk.smart_supported}>
        <SmartPanel disk={props.disk} smart={props.smart} onCheck={() => props.onCheckSmart(props.disk.name)} />
      </Show>
    </>
  );
};

const SmartPanel: Component<{ disk: Disk; smart: SmartState | undefined; onCheck: () => void }> = (props) => {
  return (
    <div style={{ 'padding-top': '14px', 'border-top': `1px solid ${tokens.borderMenu}`, 'margin-top': '14px' }}>
      <div style={{ display: 'flex', 'align-items': 'center', gap: '10px', 'padding-bottom': '8px' }}>
        <span style={{ font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}` }}>SMART health</span>
        <button
          data-testid="disks-smart-check"
          disabled={props.smart?.loading}
          onClick={props.onCheck}
          style={smartBtnStyle}
        >
          {props.smart?.loading ? 'Checking…' : props.smart?.report ? 'Re-check' : 'Check health'}
        </button>
        <Show when={props.smart?.report?.have_status}>
          <span
            data-testid="disks-smart-badge"
            style={{
              padding: '1px 8px',
              'border-radius': '3px',
              'font-size': tokens.fontSizeSm,
              background: props.smart!.report!.passed ? '#2c5d4f' : tokens.borderDanger,
              color: '#fff',
            }}
          >
            {props.smart!.report!.passed ? 'PASSED' : 'FAILED'}
          </span>
        </Show>
      </div>
      <Show when={props.smart?.error}>
        <div style={{ color: tokens.borderDanger, 'font-size': tokens.fontSizeBase }}>{props.smart!.error}</div>
      </Show>
      <Show when={props.smart?.report}>
        {(_) => {
          const r = props.smart!.report!;
          return (
            <div data-testid="disks-smart-panel">
              <KV k="temperature" v={`${r.temp_c} °C`} />
              <KV k="power-on" v={`${r.power_on_hours} h`} />
              <KV k="power cycles" v={String(r.power_cycles)} />
              <Show when={(r.attrs?.length ?? 0) > 0}>
                <div style={{ 'padding-top': '8px', overflow: 'auto' }}>
                  <For each={r.attrs ?? []}>
                    {(a) => (
                      <div style={{ display: 'flex', gap: '10px', font: `${tokens.fontSizeSm} ${tokens.fontMono}`, padding: '2px 0' }}>
                        <span style={{ width: '210px', flex: '0 0 210px', opacity: 0.8 }}>{a.name}</span>
                        <span style={{ width: '90px', 'text-align': 'right' }}>{a.raw}</span>
                        <Show when={a.when_failed && a.when_failed !== '-'}>
                          <span style={{ color: tokens.borderDanger }}>failed: {a.when_failed}</span>
                        </Show>
                      </div>
                    )}
                  </For>
                </div>
              </Show>
            </div>
          );
        }}
      </Show>
    </div>
  );
};

const PartDetail: Component<{ part: Partition }> = (props) => (
  <>
    <Title t={props.part.path} />
    <KV k="size" v={fmtBytes(props.part.size)} />
    <KV k="filesystem" v={props.part.fstype || 'unknown'} />
    <Show when={props.part.label}><KV k="label" v={props.part.label} /></Show>
    <Show when={props.part.uuid}><KV k="uuid" v={props.part.uuid} /></Show>
    <Show when={(props.part.holders?.length ?? 0) > 0}><KV k="used by" v={(props.part.holders ?? []).join(', ')} /></Show>
    <MountBlock mount={props.part.mount} />
  </>
);

const MDDetail: Component<{ a: MDArray }> = (props) => (
  <>
    <Title t={`${props.a.path} — ${props.a.level}`} />
    <KV k="state" v={props.a.degraded ? `${props.a.state} (degraded)` : props.a.state} />
    <KV k="size" v={fmtBytes(props.a.size)} />
    <KV k="raid disks" v={String(props.a.raid_disks)} />
    <Show when={props.a.sync_action && props.a.sync_action !== 'idle'}>
      <KV k="sync" v={`${props.a.sync_action} ${props.a.sync_pct.toFixed(1)}%`} />
    </Show>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>members</div>
    <For each={props.a.members ?? []}>
      {(m) => <KV k={m.name} v={`${m.state}${m.slot >= 0 ? ` (slot ${m.slot})` : ''}`} />}
    </For>
  </>
);

const VGDetail: Component<{ vg: LVMVG }> = (props) => (
  <>
    <Title t={`volume group ${props.vg.name}`} />
    <KV k="size" v={fmtBytes(props.vg.size)} />
    <KV k="free" v={fmtBytes(props.vg.free)} />
    <div style={{ padding: '6px 0' }}><FullnessBar used={props.vg.size - props.vg.free} total={props.vg.size} /></div>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>physical volumes</div>
    <For each={props.vg.pvs ?? []}>{(pv) => <KV k={pv.name} v={`${fmtBytes(pv.free)} free / ${fmtBytes(pv.size)}`} />}</For>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>logical volumes</div>
    <For each={props.vg.lvs ?? []}>{(lv) => <KV k={lv.name} v={fmtBytes(lv.size)} />}</For>
  </>
);

const LVDetail: Component<{ lv: LVMLV }> = (props) => (
  <>
    <Title t={props.lv.path} />
    <KV k="size" v={fmtBytes(props.lv.size)} />
    <Show when={props.lv.attr}><KV k="attrs" v={props.lv.attr} /></Show>
    <MountBlock mount={props.lv.mount} />
  </>
);

const BtrfsDetail: Component<{ fs: BtrfsFS }> = (props) => (
  <>
    <Title t={`btrfs ${props.fs.label || props.fs.uuid}`} />
    <Show when={props.fs.mount}><KV k="mounted at" v={props.fs.mount} /></Show>
    <div style={{ padding: '6px 0' }}><FullnessBar used={props.fs.used} total={props.fs.size} /></div>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>devices</div>
    <For each={props.fs.devices ?? []}>
      {(d) => <KV k={d.name} v={`${fmtBytes(d.used)} / ${fmtBytes(d.size)}${d.read_errs + d.write_errs > 0 ? ` · errs r${d.read_errs}/w${d.write_errs}` : ''}`} />}
    </For>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>subvolumes</div>
    <For each={props.fs.subvolumes ?? []}>{(s) => <KV k={`#${s.id}`} v={s.path} />}</For>
  </>
);

const ZPoolDetail: Component<{ p: ZPool }> = (props) => (
  <>
    <Title t={`zpool ${props.p.name}`} />
    <KV k="state" v={props.p.state} />
    <KV k="fragmentation" v={`${props.p.frag}%`} />
    <div style={{ padding: '6px 0' }}><FullnessBar used={props.p.alloc} total={props.p.size} /></div>
    <Show when={props.p.scan_status}><KV k="last scan" v={props.p.scan_status} /></Show>
    <div style={{ 'padding-top': '8px', opacity: 0.6 }}>datasets</div>
    <For each={props.p.datasets ?? []}>{(d) => <KV k={d.name} v={`${fmtBytes(d.used)} used · ${fmtBytes(d.avail)} avail`} />}</For>
  </>
);

const ZVdevDetail: Component<{ v: ZVdev }> = (props) => (
  <>
    <Title t={`${props.v.type} ${props.v.name}`} />
    <KV k="state" v={props.v.state} />
    <KV k="errors" v={`read ${props.v.read_err} · write ${props.v.write_err} · cksum ${props.v.cksum_err}`} />
  </>
);

const ZDatasetDetail: Component<{ d: ZDataset }> = (props) => (
  <>
    <Title t={`dataset ${props.d.name}`} />
    <KV k="used" v={fmtBytes(props.d.used)} />
    <KV k="available" v={fmtBytes(props.d.avail)} />
    <KV k="referenced" v={fmtBytes(props.d.refer)} />
    <Show when={props.d.mountpoint}><KV k="mountpoint" v={props.d.mountpoint} /></Show>
  </>
);

// ---- styles ----

const rootStyle: JSX.CSSProperties = {
  display: 'flex',
  width: '100%',
  height: '100%',
  background: tokens.bgWindow,
  color: tokens.fg,
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  'box-sizing': 'border-box',
};

const listStyle: JSX.CSSProperties = {
  width: '300px',
  'flex-shrink': 0,
  height: '100%',
  overflow: 'auto',
  'border-right': `1px solid ${tokens.borderMenu}`,
  padding: '6px 0',
  'box-sizing': 'border-box',
};

const sectionStyle: JSX.CSSProperties = {
  padding: '8px 12px 4px',
  'font-size': tokens.fontSizeSm,
  'text-transform': 'uppercase',
  'letter-spacing': '0.06em',
  opacity: 0.5,
};

function rowStyle(row: Row, selected: boolean): JSX.CSSProperties {
  return {
    display: 'flex',
    'align-items': 'center',
    gap: '0',
    padding: '4px 12px',
    cursor: row.selectable ? 'pointer' : 'default',
    background: selected ? tokens.bgRowSelected : 'transparent',
    'font-size': tokens.fontSizeBase,
    'white-space': 'nowrap',
  };
}

const detailStyle: JSX.CSSProperties = {
  flex: '1 1 auto',
  height: '100%',
  overflow: 'hidden',
};

const smartBtnStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': '3px',
  padding: '2px 10px',
  cursor: 'pointer',
  font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
};

// ---- custom element ----

defineWashApp('wash-app-disks', (props) => <App {...props} />, {
  style: `display:block;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
