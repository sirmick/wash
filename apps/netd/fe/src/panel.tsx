// Network settings panel — picks the networking renderer + opens the
// dedicated network app. It selects the engine and shows what's running;
// all actual config editing (and the privileged apply) lives in
// com.wash.net (docs/NET.md §2.7) — the "Open Network…" button launches
// it. Changing the backend persists the choice to network.json (via the
// host's settings store) and restarts netd + the net window.
//
// This bundle (panel.js) ships embedded in the wash-netd binary and is
// loaded on demand by the settings host. Extracted from the settings
// monolith's NetworkPane; the persisted choice + restart/launch now flow
// through the panel port instead of settings-internal calls.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { JSX } from 'solid-js';
import { Section, Row, ServiceBadge, SmallBtn, defineSettingsPanel, tokens } from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';

const NET_APP = 'com.wash.net'; // the dedicated network window the panel opens

// Backend choices; value persisted to network.json and read by
// com.wash.netd at startup (docs/NET.md §2.7).
const NET_BACKENDS: { value: string; label: string }[] = [
  { value: 'auto', label: 'Automatic (detect)' },
  { value: 'nm', label: 'NetworkManager (coexist)' },
  { value: 'networkd', label: 'systemd-networkd (take over)' },
];

const Panel = (props: SettingsPanelProps) => {
  const port = props.port;
  const [mode, setMode] = createSignal('auto'); // persisted renderer choice
  const [active, setActive] = createSignal(''); // what netd reports running
  const [available, setAvailable] = createSignal<string[]>([]);
  const [uci, setUci] = createSignal(''); // live config rendered as UCI (read-only)

  onMount(() => {
    const offCfg = port.readConfig('network', (v) => setMode((v as { backend?: string }).backend || 'auto'));
    const offMsg = port.onMessage((p) => {
      if (p.kind !== 'state') return;
      const s = ((p as { state?: { backend?: string; available?: string[]; uci?: string } }).state) || {};
      setActive(s.backend ?? '');
      setAvailable(Array.isArray(s.available) ? s.available : []);
      // publish() stamps uci on every state; omitempty drops it when empty, so an
      // absent field means "no managed config" (reflect it, don't keep stale text).
      setUci(s.uci ?? '');
    });
    port.send({ kind: 'subscribe' });
    onCleanup(() => {
      port.send({ kind: 'unsubscribe' });
      offCfg();
      offMsg();
    });
  });

  // A backend is selectable if it's auto, the current choice, or detected
  // available; others grey out (e.g. systemd-networkd on a non-systemd box).
  const selectable = (v: string) => v === 'auto' || v === mode() || available().includes(v);

  // Persist the choice, then restart netd AND the net window so both pick
  // up the new backend at process start — no hot reload (docs/NET.md §2.7).
  const choose = (backend: string) => {
    setMode(backend);
    port.writeConfig('network', { backend });
    port.restart(); // com.wash.netd (own app)
    port.restart(NET_APP);
  };

  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="Networking backend">
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: '12px' }}>
          <Row label="Manage mode">
            <select
              data-testid="net-backend"
              value={mode()}
              onChange={(e) => choose(e.currentTarget.value)}
              style={textInputStyle}
            >
              <For each={NET_BACKENDS}>
                {(b) => (
                  <option value={b.value} disabled={!selectable(b.value)}>
                    {b.label}
                    {!selectable(b.value) ? ' — unavailable' : ''}
                  </option>
                )}
              </For>
            </select>
          </Row>
          <Row label="Currently running">
            <Show
              when={active() && active() !== 'fake'}
              fallback={<ServiceBadge tone="off" label={active() === 'fake' ? 'no backend selected' : 'starting…'} />}
            >
              <ServiceBadge tone="on" label={active()} />
            </Show>
          </Row>
          <div style={{ opacity: 0.7, font: `${tokens.fontSizeMd} ${tokens.fontSans}`, 'max-width': '440px', 'line-height': 1.5 }}>
            Choosing a backend restarts the network service; any open Network window reloads.
            <strong> Automatic</strong> keeps NetworkManager when it's already managing the box (coexist),
            and uses systemd-networkd when wash owns it.
          </div>
        </div>
      </Section>
      <Section title="Current configuration (UCI)">
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: '8px' }}>
          <div style={{ opacity: 0.7, font: `${tokens.fontSizeMd} ${tokens.fontSans}`, 'max-width': '440px', 'line-height': 1.5 }}>
            <Show
              when={active() === 'nm'}
              fallback={<>What wash is enforcing on this box, rendered to UCI (read-only). Edit it in the Network window.</>}
            >
              wash-managed interfaces only — NetworkManager owns the rest. wash's view rendered to UCI (read-only).
            </Show>
          </div>
          <Show
            when={uci().trim()}
            fallback={
              <div style={{ opacity: 0.5, font: `${tokens.fontSizeMd} ${tokens.fontMono}`, padding: '8px 0' }}>
                {active() && active() !== 'fake' ? '(no managed configuration)' : 'loading…'}
              </div>
            }
          >
            <pre data-testid="net-uci" style={uciPreStyle}>{uci()}</pre>
            <div>
              <SmallBtn onClick={() => navigator.clipboard?.writeText(uci())} data-testid="net-uci-copy">
                Copy
              </SmallBtn>
            </div>
          </Show>
        </div>
      </Section>
      <Section title="Network settings">
        <div>
          <SmallBtn onClick={() => port.launch(NET_APP)} data-testid="net-open">
            Open Network…
          </SmallBtn>
        </div>
      </Section>
    </div>
  );
};

const uciPreStyle: JSX.CSSProperties = {
  margin: 0,
  'max-height': '320px',
  overflow: 'auto',
  background: tokens.bgMenu,
  color: tokens.fg,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}px`,
  padding: '10px 12px',
  font: `${tokens.fontSizeBase} ${tokens.fontMono}`,
  'line-height': 1.45,
  'white-space': 'pre',
  'user-select': 'text',
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

defineSettingsPanel('wash-settings-panel-network', Panel);
