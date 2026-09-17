import { createSignal, For, Show } from 'solid-js';
import type { Component } from 'solid-js';
import { Button, createAppBus, defineWashApp, tokens, type WashAppProps } from '@wash/ui';

type SummaryMessage =
  | { kind: 'summary.started'; total: number }
  | { kind: 'summary.progress'; done: number; total: number; title: string }
  | { kind: 'summary.complete'; text: string; windows: number }
  | { kind: 'summary.failed'; code: string; msg: string }
  | { kind: 'summary.cancelled' };

const MAX_WINDOWS = 64;

/** What each window gave: shown beside the briefing so the reader knows
 *  whether a line came from the terminal's own screen or a saved state. */
interface Observed {
  title: string;
  appID: string;
  source: WashObservation['source'] | 'error';
}

const App: Component<WashAppProps> = (props) => {
  const [busy, setBusy] = createSignal(false);
  const [status, setStatus] = createSignal('Ready');
  const [observed, setObserved] = createSignal<Observed[]>([]);
  const [output, setOutput] = createSignal('Click “Summarize session” to observe the other open windows and brief them. Nothing is sent until you click.');

  const bus = createAppBus<SummaryMessage>(props, { onMsg: (msg) => {
    switch (msg.kind) {
      case 'summary.started':
        setStatus(`Briefing ${msg.total} window${msg.total === 1 ? '' : 's'}…`);
        return;
      case 'summary.progress':
        setStatus(`${msg.done}/${msg.total} · ${msg.title}`);
        return;
      case 'summary.complete':
        setBusy(false);
        setStatus(`Complete · ${msg.windows} window${msg.windows === 1 ? '' : 's'}`);
        setOutput(msg.text);
        return;
      case 'summary.failed':
        setBusy(false);
        setStatus(`Error · ${msg.code}`);
        setOutput(msg.msg);
        return;
      case 'summary.cancelled':
        setBusy(false);
        setStatus('Cancelled');
        return;
    }
  }});

  // Observe every other window through its own router (docs/COMMANDER.md
  // §4): the router decides what may be read and redacts it; this window
  // only forwards what came back to its backend for briefing.
  const start = async () => {
    const wins = window.wash.windows().filter((w) => w.instanceID !== props.instance).slice(0, MAX_WINDOWS);
    setBusy(true);
    setStatus(`Observing ${wins.length} window${wins.length === 1 ? '' : 's'}…`);
    setOutput('');
    setObserved([]);
    const looks = await Promise.all(wins.map(async (w) => {
      try {
        return { w, o: await window.wash.observe(w.origin || undefined, w.instanceID) };
      } catch {
        return { w, o: null };
      }
    }));
    setObserved(looks.map(({ w, o }) => ({ title: w.title, appID: w.appID, source: o ? o.source : 'error' })));
    const windows = looks
      .filter(({ o }) => o && o.source !== 'none' && o.content)
      .map(({ w, o }) => ({
        app_id: w.appID,
        instance_id: w.instanceID,
        origin: w.origin,
        title: w.title,
        state: w.state,
        focused: w.focused,
        source: o!.source,
        content_type: o!.content_type ?? 'text/plain',
        content: o!.content ?? '',
        truncated: o!.truncated ?? false,
      }));
    if (windows.length === 0) {
      setBusy(false);
      setStatus('Nothing to brief');
      setOutput(wins.length === 0
        ? 'There are no other windows open.'
        : 'None of the open windows can be observed: their apps say none, or they have shown nothing yet.');
      return;
    }
    bus.send({ kind: 'summary.start', windows });
  };

  const cancel = () => {
    setStatus('Cancelling…');
    bus.send({ kind: 'summary.cancel' });
  };

  return (
    <main style={{ display: 'flex', 'flex-direction': 'column', gap: '12px', height: '100%', padding: '16px', 'box-sizing': 'border-box', color: tokens.fg, background: tokens.bgWindow }}>
      <div style={{ display: 'flex', gap: '8px', 'align-items': 'center' }}>
        <Button data-testid="summary-run" onClick={busy() ? cancel : () => { void start(); }}>{busy() ? 'Cancel' : 'Summarize session'}</Button>
        <span data-testid="summary-status" style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>{status()}</span>
      </div>
      <Show when={observed().length > 0}>
        <div data-testid="summary-sources" style={{ display: 'flex', 'flex-wrap': 'wrap', gap: '6px 12px', color: tokens.fgMuted, font: tokens.type.textSm }}>
          <For each={observed()}>{(o) => (
            <span title={o.appID}>{o.title || o.appID} <span style={{ opacity: 0.7 }}>· {o.source}</span></span>
          )}</For>
        </div>
      </Show>
      <textarea
        readOnly
        data-testid="summary-output"
        aria-label="Session summary"
        value={output()}
        style={{ flex: '1', width: '100%', resize: 'none', 'box-sizing': 'border-box', padding: '12px', color: tokens.fg, background: tokens.bgInset, border: `1px solid ${tokens.borderMenu}`, 'border-radius': tokens.radiusSm, font: tokens.type.textMd, 'line-height': '1.5' }}
      />
    </main>
  );
};

defineWashApp('wash-app-session-summary', App, { style: 'display:block;width:100%;height:100%;' });
