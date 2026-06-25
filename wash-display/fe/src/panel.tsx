// Display settings panel — live status of the wash-display compositor
// (running / which wayland-N / native-window count) + a restart. Host-
// rendered by the settings app over the service's display.state pushes.
//
// This bundle (panel.js) is embedded as raw bytes in the wash-display
// C++ binary (CMake, base64-free) and loaded on demand by the settings
// host. The compositor already emits display.state on subscribe and on
// window create/destroy (wire_conn.cpp emit_display_state). No
// "not installed" fallback is needed: when the wash-display package is
// absent the registry has no panel descriptor, so settings shows no
// Display section at all (docs/SETTINGS.md).

import { Show, createSignal, onCleanup, onMount } from 'solid-js';
import { Row, Section, Select, ServiceBadge, SmallBtn, defineSettingsPanel, tokens } from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';
import { RotateCcw } from 'lucide-solid';

interface DisplayState {
  running: boolean;
  wayland_display: string;
  window_count: number;
  dpi: number;
  display_width: number;
  display_height: number;
  display_scale: number;
}

const DEFAULT_DPI = 96;
type ScaleMode = 'auto' | '1' | '2';
const DPI_OPTIONS: [string, string][] = [
  ['72', '72 dpi'],
  ['96', '96 dpi'],
  ['120', '120 dpi'],
  ['144', '144 dpi'],
  ['168', '168 dpi'],
  ['192', '192 dpi'],
];
const SCALE_OPTIONS: [ScaleMode, string][] = [
  ['auto', 'Automatic'],
  ['1', '1x'],
  ['2', '2x HiDPI'],
];

function parseDpi(v: unknown): number {
  const n = typeof v === 'number' ? v : Number(v);
  if (!Number.isFinite(n) || n <= 0) return DEFAULT_DPI;
  return Math.max(72, Math.min(240, Math.round(n)));
}

function parseScaleMode(v: unknown): ScaleMode {
  return v === '1' || v === '2' || v === 'auto' ? v : 'auto';
}

function shellDisplayScaleMode(): ScaleMode {
  const wash = (window as unknown as { wash?: { displayScaleMode?: () => ScaleMode } }).wash;
  return parseScaleMode(wash?.displayScaleMode?.());
}

function setShellDisplayScaleMode(mode: ScaleMode): void {
  const wash = (window as unknown as {
    wash?: { setDisplayScaleMode?: (mode: ScaleMode) => ScaleMode };
  }).wash;
  wash?.setDisplayScaleMode?.(mode);
}

const Panel = (props: SettingsPanelProps) => {
  const port = props.port;
  const [state, setState] = createSignal<DisplayState | null>(null);
  const [dpi, setDpi] = createSignal(String(DEFAULT_DPI));
  const [scaleMode, setScaleMode] = createSignal<ScaleMode>(shellDisplayScaleMode());
  const [config, setConfig] = createSignal<Record<string, unknown>>({});

  const writeConfig = (patch: Record<string, unknown>) => {
    const next = { ...config(), ...patch };
    setConfig(next);
    port.writeConfig('display', next);
  };

  const applyDpi = (next: number, persist: boolean) => {
    const value = parseDpi(next);
    setDpi(String(value));
    port.send({ kind: 'display.set_dpi', dpi: value });
    if (persist) writeConfig({ dpi: value });
  };

  const applyScaleMode = (next: ScaleMode, persist: boolean) => {
    const value = parseScaleMode(next);
    setScaleMode(value);
    setShellDisplayScaleMode(value);
    if (persist) writeConfig({ scale_mode: value });
  };

  onMount(() => {
    const offMsg = port.onMessage((p) => {
      if (p.kind === 'display.state' || p.kind === 'display_ready') {
        const nextDpi = parseDpi((p as { dpi?: number }).dpi ?? DEFAULT_DPI);
        setDpi(String(nextDpi));
        setState({
          running: (p as { running?: boolean }).running ?? true,
          wayland_display: (p as { wayland_display?: string }).wayland_display ?? '',
          window_count: (p as { window_count?: number }).window_count ?? 0,
          dpi: nextDpi,
          display_width: (p as { display_width?: number }).display_width ?? 0,
          display_height: (p as { display_height?: number }).display_height ?? 0,
          display_scale: (p as { display_scale?: number }).display_scale ?? 1,
        });
      }
    });
    const offCfg = port.readConfig('display', (value) => {
      setConfig({ ...(value || {}), ...config() });
      if (value && Object.prototype.hasOwnProperty.call(value, 'dpi')) {
        applyDpi(parseDpi(value.dpi), false);
      }
      if (value && Object.prototype.hasOwnProperty.call(value, 'scale_mode')) {
        applyScaleMode(parseScaleMode(value.scale_mode), false);
      } else {
        setScaleMode(shellDisplayScaleMode());
      }
    });
    port.send({ kind: 'subscribe' });
    onCleanup(() => {
      port.send({ kind: 'unsubscribe' });
      offMsg();
      offCfg();
    });
  });

  const s = () => state();

  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="X / Wayland compositor">
        <Show when={s()} fallback={<div style={{ opacity: 0.6 }}>Checking…</div>}>
          <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
            <Row label="Status">
              <ServiceBadge tone={s()!.running ? 'on' : 'off'} label={s()!.running ? 'running' : 'stopped'} />
            </Row>
            <Show when={s()!.wayland_display}>
              <Row label="Wayland display">
                <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>{s()!.wayland_display}</span>
              </Row>
            </Show>
            <Row label="Native windows">
              <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>{s()!.window_count}</span>
            </Row>
            <Row label="Virtual output">
              <span style={{ font: `${tokens.fontSizeMd} ${tokens.fontMono}` }}>
                {s()!.display_width || 0}x{s()!.display_height || 0} @ {s()!.display_scale || 1}x
              </span>
            </Row>
            <Row label="DPI">
              <Select
                value={dpi()}
                options={DPI_OPTIONS}
                data-testid="display-dpi"
                onChange={(v) => applyDpi(parseDpi(v), true)}
              />
            </Row>
            <Row label="Scale">
              <Select
                value={scaleMode()}
                options={SCALE_OPTIONS}
                data-testid="display-scale-mode"
                onChange={(v) => applyScaleMode(parseScaleMode(v), true)}
              />
            </Row>
            <div>
              <SmallBtn onClick={() => port.restart()} data-testid="display-restart">
                <RotateCcw size={14} /> Restart compositor
              </SmallBtn>
            </div>
          </div>
        </Show>
      </Section>
    </div>
  );
};

defineSettingsPanel('wash-settings-panel-display', Panel);
