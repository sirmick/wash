// wash-app-settings: singleton settings UI. One built-in pane — Desktop
// (round-trips ~/.config/wash/desktop.json via settings.read/write) —
// plus a section per app-supplied settings panel discovered through
// window.wash.settingsPanels(). The panels (Developer/vscode,
// Network/netd, Display/compositor) live in their owning apps and are
// loaded on demand; settings hosts them over a per-panel port that wraps
// its generic svc.* / settings.* / launch BE verbs (docs/SETTINGS.md).
//
// Architecture: this app never talks to wash-session directly.
// settings.write rewrites desktop.json atomically; the session BE
// fswatches that file and re-ships desktop.config to its FE. Decoupled
// through disk by design (see [no premature service]).

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import {
  FilePicker,
  PANEL_PORT_PROP,
  Row,
  Section,
  Select,
  SmallBtn,
  defineWashApp,
  tokens,
} from '@wash/ui';
import type { SettingsPanelPort } from '@wash/ui';
import { Image as ImageIcon } from 'lucide-solid';

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

// 'desktop' is the built-in pane; any other section value is a panel's
// owning app id, discovered at runtime.
type Section = string;

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [section, setSection] = createSignal<Section>('desktop');

  // Discovered app-supplied panels (catalog `panels` list via the shell).
  const [panels, setPanels] = createSignal<WashPanelDesc[]>(window.wash.settingsPanels());

  // ---- desktop pane form state ----
  const [path, setPath] = createSignal<string>('');
  const [mode, setMode] = createSignal<NonNullable<DesktopConfig['wallpaper']>['mode']>(DEFAULTS.mode);
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

  // ---- panel port machinery ----
  // Panels are mounted inside this app's element, so they can't be their
  // own router instances. Each gets a SettingsPanelPort that funnels its
  // traffic through this app's BE (svc.* / settings.* / launch). We route
  // the BE's replies back to the right panel here.
  const msgSubs = new Map<string, Set<(payload: Record<string, unknown>) => void>>(); // by app id
  const cfgSubs = new Map<string, Set<(value: Record<string, unknown>) => void>>(); // by domain

  const subscribe = <T,>(m: Map<string, Set<T>>, key: string, cb: T): (() => void) => {
    let set = m.get(key);
    if (!set) {
      set = new Set();
      m.set(key, set);
    }
    set.add(cb);
    return () => set!.delete(cb);
  };

  const svcSend = (app: string, payload: Record<string, unknown>) =>
    send({ kind: 'svc.send', app, payload });
  const svcRestart = (app: string) => send({ kind: 'svc.restart', app });

  const buildPort = (appID: string): SettingsPanelPort => ({
    appID,
    send: (payload, app) => svcSend(app ?? appID, payload),
    restart: (app) => svcRestart(app ?? appID),
    onMessage: (cb, app) => subscribe(msgSubs, app ?? appID, cb),
    readConfig: (domain, cb) => {
      const off = subscribe(cfgSubs, domain, cb);
      send({ kind: 'settings.read', domain });
      return off;
    },
    writeConfig: (domain, value) => send({ kind: 'settings.write', domain, value }),
    launch: (app) => send({ kind: 'launch', app }),
  });

  // ---- BE plumbing ----

  const handleBE = (msg: BEMessage) => {
    switch (msg.kind) {
      case 'settings.value': {
        const domain = (msg as { domain?: string }).domain || 'desktop';
        const value = (msg.value || {}) as Record<string, unknown>;
        // Fan a panel-owned domain (e.g. 'network') out to its readConfig
        // subscribers; the desktop pane is handled inline below.
        cfgSubs.get(domain)?.forEach((cb) => cb(value));
        if (domain !== 'desktop') return;
        const v = value as DesktopConfig;
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
        // A background service pushed a reply; route to its panel's
        // onMessage subscribers by app id.
        const app = (msg as { app?: string }).app || '';
        const payload = ((msg as { payload?: Record<string, unknown> }).payload || {});
        msgSubs.get(app)?.forEach((cb) => cb(payload));
        return;
      }
      // svc.restart_done is intentionally ignored: panels drive their own
      // post-restart UI off the service's next state push.
    }
  };

  // ---- desktop save (debounced) ----

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

  // Auto-save on any desktop signal change.
  createEffect(() => {
    path(); mode(); fallback(); format(); showSeconds(); position();
    scheduleSave();
  });

  // ---- lifecycle ----

  onMount(() => {
    const onMsg = (ev: Event) => handleBE((ev as CustomEvent).detail as BEMessage);
    props.host.addEventListener('wash:msg', onMsg);

    const offPanels = window.wash.onSettingsPanels(setPanels);

    // Ask BE for the desktop config + picker root. Panel domains are
    // requested by each panel via port.readConfig on mount.
    send({ kind: 'settings.read', domain: 'desktop' });
    send({ kind: 'fs.root' });

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
      offPanels();
      if (saveTimer) clearTimeout(saveTimer);
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
          <RailItem label="Desktop" active={section() === 'desktop'} onClick={() => setSection('desktop')} />
          <For each={panels()}>
            {(p) => (
              <RailItem
                label={p.section}
                active={section() === p.app_id}
                onClick={() => setSection(p.app_id)}
              />
            )}
          </For>
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
          <For each={panels()}>
            {(p) => (
              <Show when={section() === p.app_id}>
                <PanelHost panel={p} buildPort={buildPort} />
              </Show>
            )}
          </For>
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

// PanelHost loads an app-supplied panel bundle on demand (defining its
// custom element), then mounts the element with a host-built port. The
// element subscribes/unsubscribes itself; remounting on section switch
// re-runs that lifecycle. loadSettingsPanel is idempotent per app id, so
// only the first visit pays the fetch+import.
const PanelHost: Component<{
  panel: WashPanelDesc;
  buildPort: (appID: string) => SettingsPanelPort;
}> = (props) => {
  let container: HTMLDivElement | undefined;
  const [error, setError] = createSignal('');

  onMount(() => {
    let el: HTMLElement | undefined;
    let cancelled = false;
    window.wash
      .loadSettingsPanel(props.panel.app_id)
      .then(() => {
        if (cancelled || !container) return;
        el = document.createElement(props.panel.element);
        (el as HTMLElement & { [PANEL_PORT_PROP]?: SettingsPanelPort })[PANEL_PORT_PROP] =
          props.buildPort(props.panel.app_id);
        container.appendChild(el);
      })
      .catch((e) => setError(String(e)));
    onCleanup(() => {
      cancelled = true;
      el?.remove();
    });
  });

  return (
    <div ref={container} style={{ height: '100%' }}>
      <Show when={error()}>
        <div data-testid="panel-error" style={{ color: '#fca5a5', padding: '4px' }}>
          Failed to load panel: {error()}
        </div>
      </Show>
    </div>
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

const checkboxStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
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

// ---- custom element ----

defineWashApp('wash-app-settings', (props) => <App {...props} />, {
  style: `display:block;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.fontSizeBase} ${tokens.fontSans};box-sizing:border-box`,
});
