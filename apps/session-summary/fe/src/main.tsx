import { createSignal } from 'solid-js';
import type { Component } from 'solid-js';
import { Button, createAppBus, defineWashApp, tokens, type WashAppProps } from '@wash/ui';

type SummaryMessage =
  | { kind: 'summary.started'; total: number }
  | { kind: 'summary.progress'; done: number; total: number; title: string }
  | { kind: 'summary.complete'; text: string; windows: number }
  | { kind: 'summary.failed'; code: string; msg: string }
  | { kind: 'summary.cancelled' };

const MAX_WINDOWS = 64;
const MAX_CONTENT = 64 * 1024;

function contentText(value: unknown): { text: string; truncated: boolean } {
  const seen = new WeakSet<object>();
  let text: string;
  try {
    text = JSON.stringify(value, (_key, item) => {
      if (typeof item === 'object' && item !== null) {
        if (seen.has(item)) return '[Circular]';
        seen.add(item);
      }
      return item;
    }, 2) ?? '';
  } catch (err) {
    text = `[Content could not be encoded: ${err instanceof Error ? err.message : String(err)}]`;
  }
  if (text.length <= MAX_CONTENT) return { text, truncated: false };
  return { text: `${text.slice(0, MAX_CONTENT)}\n[truncated]`, truncated: true };
}

const App: Component<WashAppProps> = (props) => {
  const [busy, setBusy] = createSignal(false);
  const [status, setStatus] = createSignal('Ready');
  const [output, setOutput] = createSignal('Click “Summarize session” to inspect the current window contexts. Nothing is sent until you click.');

  const bus = createAppBus<SummaryMessage>(props, { onMsg: (msg) => {
    switch (msg.kind) {
      case 'summary.started':
        setStatus(`Summarizing ${msg.total} window${msg.total === 1 ? '' : 's'}…`);
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

  const start = () => {
    const contexts = window.wash.windowContexts({ excludeInstance: props.instance }).slice(0, MAX_WINDOWS);
    const windows = contexts.map((ctx) => {
      const encoded = contentText(ctx.content);
      return {
        app_id: ctx.appID,
        instance_id: ctx.instanceID,
        origin: ctx.origin,
        title: ctx.title,
        state: ctx.state,
        focused: ctx.focused,
        content_source: ctx.contentSource,
        content: encoded.text,
        truncated: encoded.truncated,
      };
    });
    setBusy(true);
    setStatus(`Capturing ${windows.length} window${windows.length === 1 ? '' : 's'}…`);
    setOutput('');
    bus.send({ kind: 'summary.start', windows });
  };

  const cancel = () => {
    setStatus('Cancelling…');
    bus.send({ kind: 'summary.cancel' });
  };

  return (
    <main style={{ display: 'flex', 'flex-direction': 'column', gap: '12px', height: '100%', padding: '16px', 'box-sizing': 'border-box', color: tokens.fg, background: tokens.bgWindow }}>
      <div style={{ display: 'flex', gap: '8px', 'align-items': 'center' }}>
        <Button onClick={busy() ? cancel : start}>{busy() ? 'Cancel' : 'Summarize session'}</Button>
        <span style={{ color: tokens.fgMuted, font: tokens.type.textSm }}>{status()}</span>
      </div>
      <textarea
        readOnly
        aria-label="Session summary"
        value={output()}
        style={{ flex: '1', width: '100%', resize: 'none', 'box-sizing': 'border-box', padding: '12px', color: tokens.fg, background: tokens.bgInset, border: `1px solid ${tokens.borderMenu}`, 'border-radius': tokens.radiusSm, font: tokens.type.textMd, 'line-height': '1.5' }}
      />
    </main>
  );
};

defineWashApp('wash-app-session-summary', App, { style: 'display:block;width:100%;height:100%;' });
