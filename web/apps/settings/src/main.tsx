// wash-app-settings: singleton settings UI. v0 ships one pane —
// Desktop — that round-trips ~/.config/wash/desktop.json via the
// BE's settings.read / settings.write app_msg surface.
//
// Architecture: this app never talks to wash-session directly.
// settings.write rewrites desktop.json atomically; the session BE
// fswatches that file and re-ships desktop.config to its FE. Decoupled
// through disk by design (see [no premature service]).

import { For, Show, createEffect, createSignal, onCleanup, onMount } from 'solid-js';
import { render } from 'solid-js/web';
import type { Component, JSX } from 'solid-js';
import { FilePicker, tokens } from '@wash/ui';
import { Image as ImageIcon } from 'lucide-solid';

declare global {
  interface Window {
    wash: {
      sendAppMsg(instanceID: string, data: unknown): void;
    } & Record<string, unknown>;
  }
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

type Section = 'desktop';

const App: Component<{ instance: string; host: HTMLElement }> = (props) => {
  const [section, setSection] = createSignal<Section>('desktop');

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

    onCleanup(() => {
      props.host.removeEventListener('wash:msg', onMsg);
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
          <RailItem
            label="Desktop"
            active={section() === 'desktop'}
            onClick={() => setSection('desktop')}
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

// ---- custom element ----

class WashAppSettings extends HTMLElement {
  private cleanup?: () => void;

  connectedCallback() {
    this.style.cssText = [
      'display:block',
      'position:relative',
      'width:100%',
      'height:100%',
      'overflow:hidden',
      `background:${tokens.bgWindow}`,
      `color:${tokens.fg}`,
      `font:${tokens.fontSizeBase} ${tokens.fontSans}`,
      'box-sizing:border-box',
    ].join(';');
    const instance = this.getAttribute('data-wash-instance') ?? '';
    this.cleanup = render(() => <App instance={instance} host={this} />, this);
  }

  disconnectedCallback() {
    this.cleanup?.();
    this.cleanup = undefined;
  }
}

if (!customElements.get('wash-app-settings')) {
  customElements.define('wash-app-settings', WashAppSettings);
}
