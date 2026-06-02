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
import { Row, Section, ServiceBadge, SmallBtn, defineSettingsPanel, tokens } from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';
import { RotateCcw } from 'lucide-solid';

interface DisplayState {
  running: boolean;
  wayland_display: string;
  window_count: number;
}

const Panel = (props: SettingsPanelProps) => {
  const port = props.port;
  const [state, setState] = createSignal<DisplayState | null>(null);

  onMount(() => {
    const off = port.onMessage((p) => {
      if (p.kind === 'display.state' || p.kind === 'display_ready') {
        setState({
          running: (p as { running?: boolean }).running ?? true,
          wayland_display: (p as { wayland_display?: string }).wayland_display ?? '',
          window_count: (p as { window_count?: number }).window_count ?? 0,
        });
      }
    });
    port.send({ kind: 'subscribe' });
    onCleanup(() => {
      port.send({ kind: 'unsubscribe' });
      off();
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
