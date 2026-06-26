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
  createAppBus,
  defaultPackId,
  defineWashApp,
  getPack,
  packs,
  tokens,
} from '@wash/ui';
import type { Pack, SettingsPanelPort } from '@wash/ui';
import { Image as ImageIcon } from 'lucide-solid';

// DesktopConfig mirrors cmd/wash-session/config.go schema. Optional
// everywhere — defaults are applied for missing fields so an empty
// file behaves the same as a never-written one.
interface DesktopConfig {
  pack?: string;
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
  const [pack, setPack] = createSignal<string>(defaultPackId);
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

  // Persisted blob = { section } — the active panel id (built-in
  // 'desktop' or a panel's owning app_id). onState (restore) can fire
  // before the panels catalog is populated, so stash the desired section
  // and apply it once we can confirm the panel exists; until then we set
  // it optimistically and let the renderer no-op a missing panel.
  let pendingRestore: string | null = null;

  const sectionExists = (id: string) =>
    id === 'desktop' || panels().some((p) => p.app_id === id);

  // applyRestore selects the persisted section if its panel still exists.
  // 'desktop' always exists. A section whose panel is gone is dropped.
  const applyRestore = (id: string) => {
    if (sectionExists(id)) {
      setSection(id);
      pendingRestore = null;
    } else {
      pendingRestore = id; // retry once panels load
    }
  };

  // navigate switches the active section and persists it (debounced).
  const navigate = (id: string) => {
    setSection(id);
    bus.saveState({ section: id });
  };

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
        setPack(v.pack ?? defaultPackId);
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
      case 'svc.restart_done': {
        // Restart/start is initiated by a hosted panel but completed by the
        // settings BE, so fan the completion back through the same per-app
        // message subscription path as service pushes.
        const app = (msg as { app?: string }).app || '';
        if (!app) return;
        msgSubs.get(app)?.forEach((cb) => cb(msg as Record<string, unknown>));
        return;
      }
    }
  };

  // Shared transport + view-state persistence. onState (restore) fires on
  // every (re)mount = reconnect; onMsg replaces the hand-rolled
  // 'wash:msg' addEventListener. saveState ships { section } so reconnect
  // reopens on the same panel (BE: sdk.HandlePersist).
  const bus = createAppBus(props, {
    onMsg: (m) => handleBE(m as BEMessage),
    onState: (state) => {
      const s = (state as { section?: unknown } | null)?.section;
      if (typeof s === 'string') applyRestore(s);
    },
  });
  const send = bus.send;

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
      pack: pack(),
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
    pack(); path(); mode(); fallback(); format(); showSeconds(); position();
    scheduleSave();
  });

  // Picking a pack clears any custom wallpaper override so the pack's
  // own wallpaper shows — the gallery is the "give me this whole look"
  // path; "Choose image…" is the escape hatch that re-overrides.
  const selectPack = (id: string) => {
    setPack(id);
    setPath('');
  };

  // ---- lifecycle ----

  onMount(() => {
    // wash:msg/wash:state listeners (+ teardown) are owned by createAppBus.
    const offPanels = window.wash.onSettingsPanels((p) => {
      setPanels(p);
      // A restore that arrived before the panels catalog was ready can
      // now be re-evaluated against the freshly-loaded panel list.
      if (pendingRestore) applyRestore(pendingRestore);
    });

    // Ask BE for the desktop config + picker root. Panel domains are
    // requested by each panel via port.readConfig on mount.
    send({ kind: 'settings.read', domain: 'desktop' });
    send({ kind: 'fs.root' });

    onCleanup(() => {
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
          <RailItem label="Desktop" active={section() === 'desktop'} onClick={() => navigate('desktop')} />
          <For each={panels()}>
            {(p) => (
              <RailItem
                label={p.section}
                active={section() === p.app_id}
                onClick={() => navigate(p.app_id)}
              />
            )}
          </For>
        </div>
        <div style={paneStyle}>
          <Show when={section() === 'desktop'}>
            <DesktopPane
              pack={pack()}
              onSelectPack={selectPack}
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
      font: tokens.type.textMd,
      'border-radius': `${tokens.radiusMd}`,
    }}
  >
    {props.label}
  </button>
);

const DesktopPane: Component<{
  pack: string;
  onSelectPack: (id: string) => void;
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
      <Section title="Theme">
        <div style={galleryStyle}>
          <For each={packs}>
            {(p) => (
              <PackCard pack={p} active={props.pack === p.id} onSelect={() => props.onSelectPack(p.id)} />
            )}
          </For>
        </div>
      </Section>

      <Section title="Palette (read-only)">
        <PaletteSwatches packId={props.pack} />
      </Section>

      <Section title="Custom wallpaper">
        <div style={{ display: 'flex', gap: '12px', 'align-items': 'flex-start' }}>
          <Thumbnail color={props.fallback} />
          <div style={{ flex: 1, display: 'flex', 'flex-direction': 'column', gap: '6px' }}>
            <div style={pathRowStyle}>
              <Show when={props.path} fallback={<span style={{ opacity: 0.55 }}>using the pack wallpaper — choose an image to override</span>}>
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
      'border-radius': `${tokens.radiusMd}`,
      'flex-shrink': 0,
    }}
  />
);

// PackCard previews one theme: its wallpaper (pulled over the asset.read
// channel via window.wash.fetchAsset — same path the desktop uses, so
// the preview is exactly what you'll get) plus a strip of accent swatches
// from the pack's scheme. Clicking selects it; the active pack is ringed.
const PackCard: Component<{ pack: Pack; active: boolean; onSelect: () => void }> = (props) => {
  const [url, setUrl] = createSignal<string>('');
  onMount(() => {
    let objURL = '';
    let cancelled = false;
    window.wash
      .fetchAsset(props.pack.wallpaper)
      .then((a) => {
        if (cancelled) return;
        objURL = URL.createObjectURL(new Blob([a.bytes], { type: a.mime || 'image/svg+xml' }));
        setUrl(objURL);
      })
      .catch(() => {/* preview falls back to the scheme's window color */});
    onCleanup(() => {
      cancelled = true;
      if (objURL) URL.revokeObjectURL(objURL);
    });
  });
  // A pack's vars are its colors; fall back to the tokens default (== Midnight)
  // for any a partial scheme omits.
  const swatch = (name: string, fallbackHex: string) => props.pack.scheme[name] || fallbackHex;
  return (
    <button
      type="button"
      onClick={props.onSelect}
      data-testid={`pack-card-${props.pack.id}`}
      data-active={props.active}
      title={props.pack.name}
      style={{
        display: 'flex',
        'flex-direction': 'column',
        gap: '6px',
        padding: '6px',
        background: props.active ? tokens.bgRowSelected : tokens.bgInset,
        border: `2px solid ${props.active ? tokens.accentBlue : tokens.borderMenu}`,
        'border-radius': `${tokens.radiusLg}`,
        cursor: 'pointer',
        width: '160px',
      }}
    >
      <div
        style={{
          width: '100%',
          height: '90px',
          background: url()
            ? `${swatch('--wash-bg-window', '#181828')} url("${url()}") center/cover no-repeat`
            : swatch('--wash-bg-window', '#181828'),
          'border-radius': `${tokens.radiusMd}`,
        }}
      />
      <div style={{ display: 'flex', 'align-items': 'center', 'justify-content': 'space-between', gap: '6px' }}>
        <span style={{ color: tokens.fg, font: tokens.type.textMd }}>{props.pack.name}</span>
        <div style={{ display: 'flex', gap: '3px' }}>
          <Swatch c={swatch('--wash-accent-blue', '#6090e0')} />
          <Swatch c={swatch('--wash-accent-green', '#5fbf85')} />
          <Swatch c={swatch('--wash-accent-violet', '#9a90e0')} />
        </div>
      </div>
    </button>
  );
};

const Swatch: Component<{ c: string }> = (props) => (
  <span
    style={{
      width: '12px',
      height: '12px',
      'border-radius': '50%',
      background: props.c,
      border: `1px solid ${tokens.borderMenu}`,
      display: 'inline-block',
    }}
  />
);

// PaletteSwatches lists the active pack's --wash-* color tokens as a
// read-only grid (swatch + name + value) so the palette can be reviewed
// and critiqued by name. Non-color scheme entries (radius, fonts) skip.
const PaletteSwatches: Component<{ packId: string }> = (props) => {
  const colors = () =>
    Object.entries(getPack(props.packId).scheme).filter(([, v]) => /^(#|rgb|color-mix)/.test(v));
  return (
    <div
      style={{
        display: 'grid',
        'grid-template-columns': 'repeat(auto-fill, minmax(220px, 1fr))',
        gap: '3px 16px',
      }}
    >
      <For each={colors()}>
        {([name, val]) => (
          <div
            data-testid={`palette-${name.replace('--wash-', '')}`}
            title={`${name}: ${val}`}
            style={{ display: 'flex', 'align-items': 'center', gap: '7px', overflow: 'hidden' }}
          >
            <span
              style={{
                width: '18px',
                height: '14px',
                background: val,
                border: `1px solid ${tokens.borderMenu}`,
                'border-radius': tokens.radiusSm,
                'flex-shrink': 0,
              }}
            />
            <span
              style={{
                color: tokens.fg,
                font: tokens.type.monoSm,
                overflow: 'hidden',
                'text-overflow': 'ellipsis',
                'white-space': 'nowrap',
              }}
            >
              {name.replace('--wash-', '')}
            </span>
            <span style={{ color: tokens.fgDim, font: tokens.type.monoSm, 'flex-shrink': 0 }}>
              {val}
            </span>
          </div>
        )}
      </For>
    </div>
  );
};

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

const galleryStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-wrap': 'wrap',
  gap: '12px',
};

const pathRowStyle: JSX.CSSProperties = {
  font: tokens.type.monoMd,
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
  'border-radius': `${tokens.radiusMd}`,
  padding: '4px 8px',
  font: tokens.type.monoMd,
  outline: 'none',
};

const checkboxStyle: JSX.CSSProperties = {
  display: 'inline-flex',
  'align-items': 'center',
  gap: '4px',
  font: tokens.type.textMd,
  cursor: 'pointer',
};

const statusStyle: JSX.CSSProperties = {
  position: 'absolute',
  right: '12px',
  bottom: '6px',
  font: tokens.type.textMd,
  opacity: 0.6,
  'pointer-events': 'none',
};

// ---- custom element ----

defineWashApp('wash-app-settings', (props) => <App {...props} />, {
  style: `display:block;position:relative;width:100%;height:100%;overflow:hidden;background:${tokens.bgWindow};color:${tokens.fg};font:${tokens.type.textMd};box-sizing:border-box`,
});
