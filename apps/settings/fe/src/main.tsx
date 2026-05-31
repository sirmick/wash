// wash-app-settings: singleton settings UI. Three panes — Desktop
// (round-trips ~/.config/wash/desktop.json via settings.read/write),
// Developer (controls the wash-vscode background service), and Display
// (controls the wash-display compositor) — the latter two host-rendered
// over the BE's svc.* relay (docs/SETTINGS.md §4).
//
// Architecture: this app never talks to wash-session directly.
// settings.write rewrites desktop.json atomically; the session BE
// fswatches that file and re-ships desktop.config to its FE. Decoupled
// through disk by design (see [no premature service]). The service
// panels subscribe via svc.send and render the snapshots the services
// push back (relayed by the BE as svc.recv).

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { FilePicker, defineWashApp, tokens } from '@wash/ui';
import {
  Image as ImageIcon,
  RotateCcw,
  Play,
  Square as StopIcon,
  Download,
  CircleAlert,
} from 'lucide-solid';

// Background-service app ids the panels drive over the svc.* relay.
const VSCODE_APP = 'com.wash.vscode';
const DISPLAY_APP = 'com.wash.display';

// vscode service status snapshot (apps/vscode/be statusPayload).
interface VscodeStatus {
  installed: boolean;
  version: string;
  managed: boolean;
  arch_ok: boolean;
  latest: string;
  running: boolean;
  installing: boolean;
}

// compositor state snapshot (apps/.../display, commit 7). Absent until
// the service answers; the panel shows "not installed" until then.
interface DisplayState {
  running: boolean;
  wayland_display: string;
  window_count: number;
}

// DesktopConfig mirrors cmd/wash-session/config.go schema. Optional
// everywhere — defaults are applied for missing fields so an empty
// file behaves the same as a never-written one.
interface DesktopConfig {
  wallpaper?: {
    path?: string;
    mode?: 'cover' | 'contain' | 'tile' | 'center';
    fallback_color?: string;
  };
  clock?: {
    format?: '12h' | '24h';
    show_seconds?: boolean;
  };
  taskbar?: {
    position?: 'top' | 'bottom';
  };
}

const DEFAULTS: Required<{
  mode: NonNullable<NonNullable<DesktopConfig['wallpaper']>['mode']>;
  fallback: string;
  format: '12h' | '24h';
  showSeconds: boolean;
  position: 'top' | 'bottom';
}> = {
  mode: 'cover',
  fallback: '#0a0a18',
  format: '24h',
  showSeconds: false,
  position: 'bottom',
};

interface BEMessage {
  kind: string;
  [k: string]: unknown;
}

type Section = 'desktop' | 'developer' | 'display';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [section, setSection] = createSignal<Section>('desktop');

  // ---- service-panel state (Developer + Display) ----
  const [vscode, setVscode] = createSignal<VscodeStatus | null>(null);
  const [vscodeLog, setVscodeLog] = createSignal<string>('');
  // Display: null = still checking; 'absent' = no reply (not installed /
  // dead); otherwise the live state snapshot.
  const [display, setDisplay] = createSignal<DisplayState | null>(null);
  const [displayAbsent, setDisplayAbsent] = createSignal(false);

  // Form state. Initialized to defaults; overwritten when the BE
  // ships settings.value on mount.
  const [path, setPath] = createSignal<string>('');
  const [mode, setMode] = createSignal<DesktopConfig['wallpaper']['mode']>(DEFAULTS.mode);
  const [fallback, setFallback] = createSignal<string>(DEFAULTS.fallback);
  const [format, setFormat] = createSignal<'12h' | '24h'>(DEFAULTS.format);
  const [showSeconds, setShowSeconds] = createSignal<boolean>(DEFAULTS.showSeconds);
  const [position, setPosition] = createSignal<'top' | 'bottom'>(DEFAULTS.position);

  const [pickerOpen, setPickerOpen] = createSignal(false);
  const [pickerStart, setPickerStart] = createSignal('/');
  const [status, setStatus] = createSignal('');

  // Hydration guard: don't write the config back until we've loaded
  // the existing one, otherwise the first effect tick would overwrite
  // it with defaults.
  let hydrated = false;

  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  // ---- BE plumbing ----

  const handleBE = (msg: BEMessage) => {
    switch (msg.kind) {
      case 'settings.value': {
        const v = (msg.value || {}) as DesktopConfig;
        setPath(v.wallpaper?.path ?? '');
        setMode(v.wallpaper?.mode ?? DEFAULTS.mode);
        setFallback(v.wallpaper?.fallback_color ?? DEFAULTS.fallback);
        setFormat(v.clock?.format ?? DEFAULTS.format);
        setShowSeconds(v.clock?.show_seconds ?? DEFAULTS.showSeconds);
        setPosition(v.taskbar?.position ?? DEFAULTS.position);
        hydrated = true;
        return;
      }
      case 'settings.write_ok':
        setStatus('saved');
        window.setTimeout(() => setStatus(''), 1_200);
        return;
      case 'settings.write_err':
      case 'settings.read_err':
        setStatus(`error: ${(msg as { msg?: string }).msg || msg.kind}`);
        return;
      case 'fs.root_ok':
        // Picker uses this as the sandbox root. The wallpaper folder
        // lives at <repo>/out/desktop-images; with no fs sandbox the
        // root is "" and the picker opens at "/" — user navigates.
        setPickerStart((msg as { root?: string }).root || '/');
        return;
      case 'svc.recv': {
        // A background service pushed a reply; route by app id.
        const app = (msg as { app?: string }).app;
        const p = ((msg as { payload?: BEMessage }).payload || {}) as BEMessage;
        if (app === VSCODE_APP) handleVscode(p);
        else if (app === DISPLAY_APP) handleDisplay(p);
        return;
      }
      case 'svc.restart_done': {
        const m = msg as { app?: string; ok?: boolean; error?: string };
        if (m.app === DISPLAY_APP && !m.ok) {
          // Restart of an unregistered/dead compositor → not installed.
          setDisplay(null);
          setDisplayAbsent(true);
        }
        return;
      }
    }
  };

  // ---- service relay helpers ----

  const svcSend = (app: string, payload: Record<string, unknown>) =>
    send({ kind: 'svc.send', app, payload });
  const svcRestart = (app: string) => send({ kind: 'svc.restart', app });

  // handleVscode folds the vscode service's pushes into panel state.
  const handleVscode = (p: BEMessage) => {
    switch (p.kind) {
      case 'status':
        setVscode(p as unknown as VscodeStatus);
        return;
      case 'log': {
        // base64 chunk of install output; append decoded tail.
        const b64 = (p as { bytes?: string }).bytes;
        if (b64) setVscodeLog((cur) => (cur + decodeUtf8(b64)).slice(-8000));
        return;
      }
      case 'error':
        setVscodeLog((cur) => cur + `\n[error] ${(p as { msg?: string }).msg ?? ''}\n`);
        return;
      // ready / exited only matter to workbench windows; ignore here.
    }
  };

  // handleDisplay folds the compositor's pushes into panel state. Any
  // reply at all means the service is alive, so clear the absent flag.
  const handleDisplay = (p: BEMessage) => {
    setDisplayAbsent(false);
    if (p.kind === 'display.state' || p.kind === 'display_ready') {
      setDisplay({
        running: (p as { running?: boolean }).running ?? true,
        wayland_display: (p as { wayland_display?: string }).wayland_display ?? '',
        window_count: (p as { window_count?: number }).window_count ?? 0,
      });
    }
  };

  // ---- save (debounced) ----

  let saveTimer = 0;
  const scheduleSave = () => {
    if (!hydrated) return;
    if (saveTimer) clearTimeout(saveTimer);
    saveTimer = window.setTimeout(doSave, 250);
  };

  const doSave = () => {
    saveTimer = 0;
    const value: DesktopConfig = {
      wallpaper: {
        path: path() || undefined,
        mode: mode(),
        fallback_color: fallback(),
      },
      clock: { format: format(), show_seconds: showSeconds() },
      taskbar: { position: position() },
    };
    send({ kind: 'settings.write', domain: 'desktop', value });
    setStatus('saving…');
  };

  // Auto-save on any signal change.
  createEffect(() => {
    path(); mode(); fallback(); format(); showSeconds(); position();
    scheduleSave();
  });

  // ---- lifecycle ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);

    // Ask BE for current values + picker root.
    send({ kind: 'settings.read', domain: 'desktop' });
    send({ kind: 'fs.root' });

    // Subscribe to the service panels. vscode is a registered
    // background app (spawns on demand), so it replies with status.
    // display ships as a separate package — when absent the subscribe
    // is dropped router-side and no reply arrives; flip to "not
    // installed" after a grace window.
    svcSend(VSCODE_APP, { kind: 'subscribe' });
    svcSend(DISPLAY_APP, { kind: 'subscribe' });
    const displayProbe = window.setTimeout(() => {
      if (display() === null) setDisplayAbsent(true);
    }, 1_500);

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      if (saveTimer) clearTimeout(saveTimer);
      window.clearTimeout(displayProbe);
      svcSend(VSCODE_APP, { kind: 'unsubscribe' });
      svcSend(DISPLAY_APP, { kind: 'unsubscribe' });
    });
  });

  // ---- view ----

  const openPicker = () => setPickerOpen(true);
  const cancelPicker = () => setPickerOpen(false);
  const confirmPicker = (p: string) => {
    setPath(p);
    setPickerOpen(false);
  };

  return (
    <>
      <div style={layoutStyle}>
        <div style={railStyle}>
          <RailItem
            label="Desktop"
            active={section() === 'desktop'}
            onClick={() => setSection('desktop')}
          />
          <RailItem
            label="Developer"
            active={section() === 'developer'}
            onClick={() => setSection('developer')}
          />
          <RailItem
            label="Display"
            active={section() === 'display'}
            onClick={() => setSection('display')}
          />
        </div>
        <div style={paneStyle}>
          <Show when={section() === 'desktop'}>
            <DesktopPane
              path={path()}
              mode={mode() || DEFAULTS.mode}
              fallback={fallback()}
              format={format()}
              showSeconds={showSeconds()}
              position={position()}
              onChoose={openPicker}
              onClearPath={() => setPath('')}
              onModeChange={setMode}
              onFallbackChange={setFallback}
              onFormatChange={setFormat}
              onShowSecondsChange={setShowSeconds}
              onPositionChange={setPosition}
            />
          </Show>
          <Show when={section() === 'developer'}>
            <DeveloperPane
              status={vscode()}
              log={vscodeLog()}
              onStart={() => svcSend(VSCODE_APP, { kind: 'start' })}
              onStop={() => svcSend(VSCODE_APP, { kind: 'stop' })}
              onInstall={() => svcSend(VSCODE_APP, { kind: 'install', version: '' })}
              onUpdate={() => svcSend(VSCODE_APP, { kind: 'update' })}
              onRestart={() => svcRestart(VSCODE_APP)}
            />
          </Show>
          <Show when={section() === 'display'}>
            <DisplayPane
              state={display()}
              absent={displayAbsent()}
              onRestart={() => svcRestart(DISPLAY_APP)}
            />
          </Show>
        </div>
      </div>
      <div data-testid="settings-status" style={statusStyle}>{status()}</div>
      <FilePicker
        open={pickerOpen()}
        mode="open"
        host={props.host}
        hostInstanceID={props.instance}
        start={pickerStart()}
        filters={[
          { label: 'Images', re: '\\.(png|jpe?g|webp|gif|avif|svg)$' },
          { label: 'All files', re: '.*' },
        ]}
        defaultFilter={0}
        onConfirm={confirmPicker}
        onCancel={cancelPicker}
        data-testid="settings-picker"
      />
    </>
  );
};

const RailItem: Component<{ label: string; active: boolean; onClick: () => void }> = (props) => (
  <button
    type="button"
    onClick={props.onClick}
    data-active={props.active}
    style={{
      display: 'block',
      width: '100%',
      'text-align': 'left',
      padding: '8px 12px',
      background: props.active ? tokens.bgRowSelected : 'transparent',
      color: tokens.fg,
      border: 'none',
      cursor: 'pointer',
      font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
      'border-radius': `${tokens.radiusMd}px`,
    }}
  >
    {props.label}
  </button>
);

const DesktopPane: Component<{
  path: string;
  mode: NonNullable<DesktopConfig['wallpaper']>['mode'];
  fallback: string;
  format: '12h' | '24h';
  showSeconds: boolean;
  position: 'top' | 'bottom';
  onChoose: () => void;
  onClearPath: () => void;
  onModeChange: (v: NonNullable<DesktopConfig['wallpaper']>['mode']) => void;
  onFallbackChange: (v: string) => void;
  onFormatChange: (v: '12h' | '24h') => void;
  onShowSecondsChange: (v: boolean) => void;
  onPositionChange: (v: 'top' | 'bottom') => void;
}> = (props) => {
  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="Wallpaper">
        <div style={{ display: 'flex', gap: '12px', 'align-items': 'flex-start' }}>
          <Thumbnail color={props.fallback} />
          <div style={{ flex: 1, display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
            <div style={pathRowStyle}>
              <Show when={props.path} fallback={<span style={{ opacity: 0.55 }}>no image — fallback color shown</span>}>
                <span style={pathStyle} title={props.path}>{props.path}</span>
              </Show>
            </div>
            <div style={{ display: 'flex', gap: '6px' }}>
              <SmallBtn onClick={props.onChoose} data-testid="settings-choose-image">
                <ImageIcon size={14} /> Choose image…
              </SmallBtn>
              <Show when={props.path}>
                <SmallBtn onClick={props.onClearPath}>Clear</SmallBtn>
              </Show>
            </div>
          </div>
        </div>
      </Section>

      <Row label="Fit">
        <Select
          value={props.mode || 'cover'}
          options={[
            ['cover', 'Cover (fill, crop edges)'],
            ['contain', 'Contain (fit inside, letterbox)'],
            ['tile', 'Tile'],
            ['center', 'Center (no scale)'],
          ]}
          onChange={(v) => props.onModeChange(v as 'cover' | 'contain' | 'tile' | 'center')}
        />
      </Row>

      <Row label="Fallback color">
        <div style={{ display: 'flex', gap: '6px', 'align-items': 'center' }}>
          <input
            type="color"
            value={normalizeHex(props.fallback)}
            onInput={(e) => props.onFallbackChange(e.currentTarget.value)}
            style={{ width: '32px', height: '24px', border: 'none', background: 'transparent', cursor: 'pointer' }}
          />
          <input
            type="text"
            value={props.fallback}
            onInput={(e) => props.onFallbackChange(e.currentTarget.value)}
            style={textInputStyle}
          />
        </div>
      </Row>

      <Row label="Clock">
        <div style={{ display: 'flex', gap: '12px', 'align-items': 'center' }}>
          <Select
            value={props.format}
            options={[['24h', '24-hour'], ['12h', '12-hour']]}
            onChange={(v) => props.onFormatChange(v as '12h' | '24h')}
          />
          <label style={checkboxStyle}>
            <input
              type="checkbox"
              checked={props.showSeconds}
              onChange={(e) => props.onShowSecondsChange(e.currentTarget.checked)}
            />
            <span>seconds</span>
          </label>
        </div>
      </Row>

      <Row label="Taskbar">
        <Select
          value={props.position}
          options={[['bottom', 'Bottom'], ['top', 'Top']]}
          onChange={(v) => props.onPositionChange(v as 'top' | 'bottom')}
        />
      </Row>
    </div>
  );
};

// DeveloperPane controls the wash-vscode background service: install /
// update code-server, start / stop it, and a hard restart. Host-rendered
// over the service's status / log pushes (relayed as svc.recv).
const DeveloperPane: Component<{
  status: VscodeStatus | null;
  log: string;
  onStart: () => void;
  onStop: () => void;
  onInstall: () => void;
  onUpdate: () => void;
  onRestart: () => void;
}> = (props) => {
  const s = () => props.status;
  const updatable = () => {
    const v = s();
    return !!v && v.installed && !!v.latest && v.latest !== v.version;
  };
  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="VS Code (code-server)">
        <Show when={s()} fallback={<div style={{ opacity: 0.6 }}>Connecting to service…</div>}>
          <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
            <Row label="Status">
              <ServiceBadge
                tone={
                  !s()!.installed ? 'absent'
                    : s()!.installing ? 'busy'
                    : s()!.running ? 'on'
                    : 'off'
                }
                label={
                  !s()!.installed ? 'not installed'
                    : s()!.installing ? 'installing…'
                    : s()!.running ? 'running'
                    : 'stopped'
                }
              />
            </Row>
            <Show when={s()!.installed}>
              <Row label="Version">
                <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>
                  {s()!.version || '—'}
                  <Show when={updatable()}>
                    <span style={{ opacity: 0.6 }}> → {s()!.latest} available</span>
                  </Show>
                </span>
              </Row>
            </Show>
            <Show when={!s()!.arch_ok}>
              <div style={warnStyle}><CircleAlert size={13} /> Unsupported CPU architecture for code-server.</div>
            </Show>
            <div style={{ display: 'flex', gap: '6px', 'flex-wrap': 'wrap' }}>
              <Show when={!s()!.installed && s()!.arch_ok}>
                <SmallBtn onClick={props.onInstall} data-testid="dev-install">
                  <Download size={14} /> Install
                </SmallBtn>
              </Show>
              <Show when={updatable()}>
                <SmallBtn onClick={props.onUpdate} data-testid="dev-update">
                  <Download size={14} /> Update to {s()!.latest}
                </SmallBtn>
              </Show>
              <Show when={s()!.installed}>
                <Show
                  when={s()!.running}
                  fallback={
                    <SmallBtn onClick={props.onStart} data-testid="dev-start">
                      <Play size={14} /> Start
                    </SmallBtn>
                  }
                >
                  <SmallBtn onClick={props.onStop} data-testid="dev-stop">
                    <StopIcon size={14} /> Stop
                  </SmallBtn>
                </Show>
                <SmallBtn onClick={props.onRestart} data-testid="dev-restart">
                  <RotateCcw size={14} /> Restart
                </SmallBtn>
              </Show>
            </div>
          </div>
        </Show>
      </Section>

      <Show when={props.log}>
        <Section title="Install log">
          <pre data-testid="dev-log" style={logStyle}>{props.log}</pre>
        </Section>
      </Show>
    </div>
  );
};

// DisplayPane controls the wash-display compositor: live status + a
// restart. When the package isn't installed the service never answers,
// so the panel renders a not-installed hint instead.
const DisplayPane: Component<{
  state: DisplayState | null;
  absent: boolean;
  onRestart: () => void;
}> = (props) => {
  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="X / Wayland compositor">
        <Show
          when={!props.absent}
          fallback={
            <div data-testid="display-absent" style={{ display: 'flex', 'flex-direction': 'column', gap: '8px' }}>
              <ServiceBadge tone="absent" label="not installed" />
              <div style={{ opacity: 0.7, font: `${tokens.fontSizeMd} ${tokens.fontSans}`, 'max-width': '420px', 'line-height': 1.5 }}>
                The compositor ships as a separate package. Install <code>wash-display</code> to run native
                X11 / Wayland apps inside wash.
              </div>
            </div>
          }
        >
          <Show when={props.state} fallback={<div style={{ opacity: 0.6 }}>Checking…</div>}>
            <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
              <Row label="Status">
                <ServiceBadge tone={props.state!.running ? 'on' : 'off'} label={props.state!.running ? 'running' : 'stopped'} />
              </Row>
              <Show when={props.state!.wayland_display}>
                <Row label="Wayland display">
                  <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>{props.state!.wayland_display}</span>
                </Row>
              </Show>
              <Row label="Native windows">
                <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>{props.state!.window_count}</span>
              </Row>
              <div>
                <SmallBtn onClick={props.onRestart} data-testid="display-restart">
                  <RotateCcw size={14} /> Restart compositor
                </SmallBtn>
              </div>
            </div>
          </Show>
        </Show>
      </Section>
    </div>
  );
};

// ServiceBadge is the small status pill shared by both service panels.
const ServiceBadge: Component<{ tone: 'on' | 'off' | 'busy' | 'absent'; label: string }> = (props) => {
  const palette: Record<string, { bg: string; fg: string }> = {
    on: { bg: '#1c3d24', fg: '#86efac' },
    off: { bg: '#1f1f2a', fg: tokens.fgDim },
    busy: { bg: '#3a3a1c', fg: '#fde047' },
    absent: { bg: '#3d1c1c', fg: '#fca5a5' },
  };
  const c = palette[props.tone] ?? palette.off;
  return (
    <span
      data-testid="service-badge"
      data-tone={props.tone}
      style={{
        display: 'inline-flex',
        'align-items': 'center',
        padding: '2px 10px',
        'border-radius': `${tokens.radiusSm}px`,
        font: `${tokens.fontSizeSm} ${tokens.fontSans}`,
        background: c.bg,
        color: c.fg,
      }}
    >
      {props.label}
    </span>
  );
};

const Section: Component<{ title: string; children: JSX.Element }> = (props) => (
  <div>
    <div style={sectionTitleStyle}>{props.title}</div>
    {props.children}
  </div>
);

const Row: Component<{ label: string; children: JSX.Element }> = (props) => (
  <div style={{ display: 'grid', 'grid-template-columns': '140px 1fr', gap: '12px', 'align-items': 'center' }}>
    <div style={{ opacity: 0.7, font: `${tokens.fontSizeBase} ${tokens.fontSans}` }}>{props.label}</div>
    {props.children}
  </div>
);

const Select: Component<{
  value: string;
  options: [string, string][];
  onChange: (v: string) => void;
}> = (props) => (
  <select
    value={props.value}
    onInput={(e) => props.onChange(e.currentTarget.value)}
    style={selectStyle}
  >
    <For each={props.options}>{([v, l]) => <option value={v}>{l}</option>}</For>
  </select>
);

const SmallBtn: Component<{
  onClick: () => void;
  'data-testid'?: string;
  children: JSX.Element;
}> = (props) => (
  <button
    type="button"
    data-testid={props['data-testid']}
    onClick={props.onClick}
    style={smallBtnStyle}
  >
    {props.children}
  </button>
);

const Thumbnail: Component<{ color: string }> = (props) => (
  <div
    style={{
      width: '120px',
      height: '72px',
      background: props.color,
      border: `1px solid ${tokens.borderMenu}`,
      'border-radius': `${tokens.radiusMd}px`,
      'flex-shrink': 0,
    }}
  />
);

// decodeUtf8 turns a base64 chunk (the service's log bytes) into text.
// Best-effort: malformed input yields "".
function decodeUtf8(b64: string): string {
  try {
    const bin = atob(b64);
    const bytes = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i);
    return new TextDecoder().decode(bytes);
  } catch {
    return '';
  }
}

// normalizeHex coerces #fff / random text into a valid 7-char hex
// for <input type=color> (which only accepts #rrggbb).
function normalizeHex(s: string): string {
  if (/^#[0-9a-fA-F]{6}$/.test(s)) return s;
  if (/^#[0-9a-fA-F]{3}$/.test(s)) {
    const r = s[1], g = s[2], b = s[3];
    return `#${r}${r}${g}${g}${b}${b}`;
  }
  return '#0a0a18';
}

// ---- styles ----

const layoutStyle: JSX.CSSProperties = {
  display: 'grid',
  'grid-template-columns': '180px 1fr',
  height: '100%',
  'box-sizing': 'border-box',
  background: tokens.bgWindow,
  color: tokens.fg,
};

const railStyle: JSX.CSSProperties = {
  'border-right': `1px solid ${tokens.borderMenu}`,
  padding: '8px 6px',
  display: 'flex',
  'flex-direction': 'column',
  gap: '2px',
  background: tokens.bgMenu,
};

const paneStyle: JSX.CSSProperties = {
  padding: '14px 18px',
  overflow: 'auto',
};

const sectionTitleStyle: JSX.CSSProperties = {
  font: `600 ${tokens.fontSizeBase} ${tokens.fontSans}`,
  opacity: 0.7,
  'text-transform': 'uppercase',
  'letter-spacing': '0.04em',
  'margin-bottom': '8px',
};

const pathRowStyle: JSX.CSSProperties = {
  font: `${tokens.fontSizeMd} ${tokens.fontMono}`,
  opacity: 0.85,
  'white-space': 'nowrap',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
};

const pathStyle: JSX.CSSProperties = {
  display: 'inline-block',
  'max-width': '100%',
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
};

const textInputStyle: JSX.CSSProperties = {
  flex: 1,
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  padding: '4px 8px',
  font: `${tokens.fontSizeBase} ${tokens.fontMono}`,
  outline: 'none',
};

const selectStyle: JSX.CSSProperties = {
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  padding: '4px 8px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const checkboxStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const smallBtnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '5px',
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  padding: '4px 10px',
  font: `${tokens.fontSizeBase} ${tokens.fontSans}`,
  cursor: 'pointer',
};

const statusStyle: JSX.CSSProperties = {
  position: 'absolute',
  right: '12px',
  bottom: '6px',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
  opacity: 0.6,
  'pointer-events': 'none',
};

const logStyle: JSX.CSSProperties = {
  margin: 0,
  'max-height': '180px',
  overflow: 'auto',
  background: '#10101a',
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  padding: '8px 10px',
  font: `${tokens.fontSizeSm} ${tokens.fontMono}`,
  color: tokens.fgDim,
  'white-space': 'pre-wrap',
  'word-break': 'break-word',
};

const warnStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '6px',
  color: '#fca5a5',
  font: `${tokens.fontSizeMd} ${tokens.fontSans}`,
};

// ---- custom element ----

defineWashApp('wash-app-settings', (props) => <App {...props} />, {
  style: `display:block;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
