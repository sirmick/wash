// Remote settings panel — lists the live remote sessions and mounted folders
// the com.wash.remote supervisor is holding, and lets you tear either down
// gracefully (disconnect cancels the ssh process + detaches the host; unmount
// escalates FUSE flush → lazy detach → abort). All the heavy lifting already
// lives in the supervisor; this panel just subscribes to its state and sends
// the disconnect/unmount verbs it already handles. "Open Connect…" launches the
// dedicated com.wash.connect window for adding/launching hosts — mirroring the
// Network panel's "Open Network…" (docs/SETTINGS.md, docs/REMOTE.md).
//
// This bundle (panel.js) ships embedded in the wash-remote binary and is
// loaded on demand by the settings host. wash-remote has no window of its own.

import { For, Show, createSignal, onCleanup, onMount } from 'solid-js';
import type { JSX } from 'solid-js';
import { Section, ServiceBadge, SmallBtn, defineSettingsPanel, tokens } from '@wash/ui';
import type { SettingsPanelProps } from '@wash/ui';

const CONNECT_APP = 'com.wash.connect'; // the windowed app the panel opens

// ----- wire types (mirror apps/remote/be HostState / MountState) -----

type HostStatus = 'starting' | 'up' | 'reconnecting' | 'down';

interface HostState {
  host: string;
  origin: string;
  status: HostStatus;
  error?: string;
  code?: string;
}

type MountStatus = 'mounting' | 'mounted' | 'error';

interface MountState {
  host: string;
  remote_root: string;
  mount_point: string;
  status: MountStatus;
  error?: string;
}

interface RemoteState {
  hosts?: HostState[];
  mounts?: MountState[];
}

// hostBadge maps a connection status to a status pill (tone + label).
function hostBadge(s: HostStatus): { tone: 'on' | 'off' | 'busy' | 'absent'; label: string } {
  switch (s) {
    case 'up': return { tone: 'on', label: 'connected' };
    case 'starting': return { tone: 'busy', label: 'connecting…' };
    case 'reconnecting': return { tone: 'busy', label: 'reconnecting…' };
    case 'down': return { tone: 'off', label: 'disconnected' };
  }
}

// mountBadge maps a mount status to a status pill.
function mountBadge(mt: MountState): { tone: 'on' | 'off' | 'busy' | 'absent'; label: string } {
  switch (mt.status) {
    case 'mounted': return { tone: 'on', label: 'mounted' };
    case 'mounting': return { tone: 'busy', label: 'mounting…' };
    case 'error': return { tone: 'absent', label: mt.error || 'mount failed' };
  }
}

const Panel = (props: SettingsPanelProps) => {
  const port = props.port;
  const [hosts, setHosts] = createSignal<HostState[]>([]);
  const [mounts, setMounts] = createSignal<MountState[]>([]);

  onMount(() => {
    // Subscribe straight to the supervisor's StateService (relayed through the
    // settings host). It replies with {kind:"state", state:{hosts, mounts, …}}
    // — the same snapshot the connect window sees, before its BE re-brands it.
    const offMsg = port.onMessage((p) => {
      if (p.kind !== 'state') return;
      const s = ((p as { state?: RemoteState }).state) || {};
      setHosts(Array.isArray(s.hosts) ? s.hosts : []);
      setMounts(Array.isArray(s.mounts) ? s.mounts : []);
    });
    port.send({ kind: 'subscribe' });
    onCleanup(() => {
      port.send({ kind: 'unsubscribe' });
      offMsg();
    });
  });

  // Graceful teardown — the supervisor cancels the ssh ctx (disconnect) and
  // runs the escalating FUSE unmount (unmount); both also drop the entry from
  // the published state, so the row vanishes on the next push.
  const disconnect = (host: string) => port.send({ kind: 'disconnect', host });
  const unmount = (mountPoint: string) => port.send({ kind: 'unmount', mount_point: mountPoint });

  return (
    <div style={{ display: 'flex', 'flex-direction': 'column', gap: '20px', padding: '4px 4px' }}>
      <Section title="Remote sessions">
        <Show
          when={hosts().length > 0}
          fallback={<div style={emptyStyle}>No active connections.</div>}
        >
          <div style={listStyle} data-testid="remote-sessions">
            <For each={hosts()}>
              {(h) => {
                const b = () => hostBadge(h.status);
                return (
                  <div style={rowStyle} data-testid={`remote-session-${h.origin}`} data-status={h.status}>
                    <span style={nameStyle} title={h.host}>{h.host}</span>
                    <ServiceBadge tone={b().tone} label={b().label} />
                    <SmallBtn onClick={() => disconnect(h.host)} data-testid="remote-disconnect">
                      Disconnect
                    </SmallBtn>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
      </Section>

      <Section title="Mounted folders">
        <Show
          when={mounts().length > 0}
          fallback={<div style={emptyStyle}>No remote folders mounted.</div>}
        >
          <div style={listStyle} data-testid="remote-mounts">
            <For each={mounts()}>
              {(mt) => {
                const b = () => mountBadge(mt);
                return (
                  <div style={rowStyle} data-testid="remote-mount" data-status={mt.status} data-mount={mt.mount_point}>
                    <span style={nameStyle} title={mt.mount_point}>
                      {mt.remote_root}
                      <span style={hostSuffixStyle}>{mt.host}</span>
                    </span>
                    <ServiceBadge tone={b().tone} label={b().label} />
                    <SmallBtn onClick={() => unmount(mt.mount_point)} data-testid="remote-unmount">
                      Unmount
                    </SmallBtn>
                  </div>
                );
              }}
            </For>
          </div>
        </Show>
      </Section>

      <Section title="Connections">
        <div style={{ display: 'flex', 'flex-direction': 'column', gap: '8px' }}>
          <div style={{ opacity: 0.7, font: tokens.type.textMd, 'max-width': '440px', 'line-height': 1.5 }}>
            Add hosts, connect, and launch remote apps in the Connect window.
          </div>
          <div>
            <SmallBtn onClick={() => port.launch(CONNECT_APP)} data-testid="remote-open-connect">
              Open Connect…
            </SmallBtn>
          </div>
        </div>
      </Section>
    </div>
  );
};

const listStyle: JSX.CSSProperties = {
  display: 'flex',
  'flex-direction': 'column',
  gap: '8px',
};

const rowStyle: JSX.CSSProperties = {
  display: 'flex',
  'align-items': 'center',
  gap: '10px',
  background: tokens.bgMenu,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': `${tokens.radiusMd}`,
  padding: '7px 10px',
};

const nameStyle: JSX.CSSProperties = {
  flex: 1,
  'min-width': 0,
  font: tokens.type.monoMd,
  color: tokens.fg,
  overflow: 'hidden',
  'text-overflow': 'ellipsis',
  'white-space': 'nowrap',
};

// hostSuffixStyle is the dim host tag shown after a mount's remote root.
const hostSuffixStyle: JSX.CSSProperties = {
  'margin-left': '8px',
  color: tokens.fgMuted,
  font: tokens.type.textSm,
};

const emptyStyle: JSX.CSSProperties = {
  opacity: 0.5,
  font: tokens.type.textMd,
  padding: '6px 0',
};

defineSettingsPanel('wash-settings-panel-remote', Panel);
