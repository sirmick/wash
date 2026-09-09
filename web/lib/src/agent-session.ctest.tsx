import { test, expect, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@solidjs/testing-library';
import { AgentSession, type AgentEvent } from './agent-session.tsx';

beforeEach(() => {
  HTMLElement.prototype.scrollTo = () => {};
});

afterEach(cleanup);

test('agent thoughts render markdown like assistant messages', () => {
  const events: AgentEvent[] = [{
    seq: 1,
    kind: 'thought',
    text: '## Next step\n\n- **inspect** the parser',
    at_ms: 0,
  }];

  const { container } = render(() => <AgentSession events={() => events} />);

  expect(container.textContent).toContain('Next step');
  expect(container.textContent).not.toContain('## Next step');
  expect(container.querySelector('strong')?.textContent).toBe('inspect');
});

test('human prompts stay literal markdown text', () => {
  const events: AgentEvent[] = [{
    seq: 1,
    kind: 'user',
    text: 'Please keep **this** literal',
    at_ms: 0,
  }];

  const { container } = render(() => <AgentSession events={() => events} />);

  expect(container.textContent).toContain('Please keep **this** literal');
  expect(container.querySelector('strong')).toBeNull();
});

// Messenger semantics mid-turn (docs/Review-findings.md, "Concurrent
// prompt mid-turn"): the composer does NOT disable while the agent is
// replying — what you send is queued by agentd and sent when the turn
// ends — and the status line says how many are waiting.
test('the composer stays open mid-turn and the status line counts the queue', async () => {
  const sent: string[] = [];
  const { container } = render(() => (
    <AgentSession
      events={() => []}
      status={() => ({ state: 'working', agent: 'codex', queued: 2 })}
      onSend={(t) => sent.push(t)}
    />
  ));
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;
  expect(composer.disabled).toBe(false);
  expect(composer.placeholder).toMatch(/sent when this turn ends/);

  const queued = container.querySelector('[data-testid="agent-queued"]');
  expect(queued?.textContent).toBe('2 queued');

  // Sending while working still sends — the BE decides to queue it.
  composer.value = 'follow-up while it talks';
  composer.dispatchEvent(new Event('input', { bubbles: true }));
  composer.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  expect(sent).toEqual(['follow-up while it talks']);
  expect(composer.value).toBe('');
});

test('an idle session shows no queue and the plain placeholder', () => {
  const { container } = render(() => (
    <AgentSession events={() => []} status={() => ({ state: 'done', agent: 'codex' })} onSend={() => {}} />
  ));
  expect(container.querySelector('[data-testid="agent-queued"]')).toBeNull();
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;
  expect(composer.placeholder).toMatch(/drop a file from wash-fm/);
});

// Drops onto the composer (docs/Review-findings.md P2: the placeholder
// promised "drop a file from wash-fm…" and nothing handled it). The
// decisions are in agent-compose-drop.ts under node:test; this is the
// wiring — a real drop event on the real textarea.
function dropEvent(dt: { types: string[]; data?: Record<string, string>; files?: File[] }): Event {
  const ev = new Event('drop', { bubbles: true, cancelable: true });
  Object.defineProperty(ev, 'dataTransfer', {
    value: {
      types: dt.types,
      getData: (f: string) => dt.data?.[f] ?? '',
      files: dt.files ?? [],
      dropEffect: 'none',
    },
  });
  return ev;
}
// jsdom has no DataTransfer, so a paste is built the same way a drop is.
function pasteEvent(files: File[]): Event {
  const ev = new Event('paste', { bubbles: true, cancelable: true });
  Object.defineProperty(ev, 'clipboardData', {
    value: { types: ['Files'], getData: () => '', files },
  });
  return ev;
}
const settle = () => new Promise((r) => setTimeout(r, 20));

test('a wash-fm drag inserts @path references at the caret', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;
  composer.value = 'please review';
  composer.dispatchEvent(new Event('input', { bubbles: true }));
  composer.setSelectionRange(13, 13);

  const ev = dropEvent({
    types: ['application/x-wash-paths'],
    data: { 'application/x-wash-paths': JSON.stringify(['/home/u/wash/main.go', '/home/u/My Docs/notes.md']) },
  });
  composer.dispatchEvent(ev);
  await settle();

  expect(ev.defaultPrevented).toBe(true);
  expect(composer.value).toBe('please review @/home/u/wash/main.go @"/home/u/My Docs/notes.md"');
  expect(container.querySelector('[data-testid="agent-drop-note"]')).toBeNull();
});

test('an OS text file is attached inline as a fenced block; an image becomes a chip', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;

  const ev = dropEvent({
    types: ['Files'],
    files: [
      new File(['package main\n'], 'main.go', { type: '' }),
      new File([new Uint8Array([0x89, 0x50])], 'shot.png', { type: 'image/png' }),
    ],
  });
  composer.dispatchEvent(ev);
  await settle();

  expect(composer.value).toBe('main.go:\n```go\npackage main\n```');
  // An image is the thing an agent can actually use, so it rides along as
  // an attachment rather than being named as "not attached".
  const chip = container.querySelector('[data-testid="agent-attachment"]');
  expect(chip?.getAttribute('data-kind')).toBe('image');
  expect(chip?.textContent).toContain('shot.png');
  expect(container.querySelector('[data-testid="agent-drop-note"]')).toBeNull();
});

test('a binary that is neither text nor an image is named as skipped', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;

  const ev = dropEvent({
    types: ['Files'],
    files: [new File([new Uint8Array([0, 1, 2])], 'a.bin', { type: 'application/octet-stream' })],
  });
  composer.dispatchEvent(ev);
  await settle();

  expect(container.querySelector('[data-testid="agent-drop-note"]')?.textContent).toMatch(/Not attached .*a\.bin/);
});

test('a drag the composer does not understand is left to the browser', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const composer = container.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;
  const ev = dropEvent({ types: ['text/plain'], data: { 'text/plain': 'hello' } });
  composer.dispatchEvent(ev);
  await settle();
  expect(ev.defaultPrevented).toBe(false);
  expect(composer.value).toBe('');
});

// docs/Review-findings.md P2 → agent: "diffs the agent made are not
// viewable" — the tool row was a one-liner and the Agent app passed no
// onOpenTool, so nothing about an edit could be seen or opened.
test('a tool row shows the diff the call made, foldable, and opens its path', () => {
  const events: AgentEvent[] = [{
    seq: 1,
    kind: 'tool',
    tool_kind: 'edit',
    title: 'Edit notes.md',
    status: 'completed',
    path: '/w/proj/notes.md',
    diff: '--- a/w/proj/notes.md\n+++ b/w/proj/notes.md\n@@ -1,2 +1,2 @@\n a\n-b\n+c\n',
    at_ms: 0,
  }];
  const opened: string[] = [];

  const { container } = render(() => (
    <AgentSession events={() => events} onOpenTool={(e) => opened.push(e.path ?? '')} />
  ));

  const diff = container.querySelector('[data-testid="agent-tool-diff"]');
  expect(diff?.textContent).toContain('-b');
  expect(diff?.textContent).toContain('+c');
  expect(container.querySelector('[data-testid="agent-tool-path"]')?.textContent).toBe('notes.md');

  (container.querySelector('[data-testid="agent-tool-diff-toggle"]') as HTMLButtonElement).click();
  expect(container.querySelector('[data-testid="agent-tool-diff"]')).toBeNull();

  // The row opens the file; folding the diff must not have.
  expect(opened).toEqual([]);
  (container.querySelector('[data-testid="agent-tool-row"]') as HTMLElement).click();
  expect(opened).toEqual(['/w/proj/notes.md']);
});

test('a tool row with no diff renders no diff box', () => {
  const events: AgentEvent[] = [
    { seq: 1, kind: 'tool', tool_kind: 'read', title: 'Read main.go', status: 'completed', at_ms: 0 },
  ];
  const { container } = render(() => <AgentSession events={() => events} />);
  expect(container.querySelector('[data-testid="agent-tool-diff"]')).toBeNull();
  expect(container.querySelector('[data-testid="agent-tool-diff-toggle"]')).toBeNull();
});

// docs/Review-findings.md P2 → agent: "Esc-to-cancel, Ctrl+Enter, ↑ prompt
// history".
const composerOf = (c: HTMLElement) => c.querySelector('[data-testid="agent-composer"]') as HTMLTextAreaElement;

test('Ctrl+Enter sends, Enter still sends, Shift+Enter does not', () => {
  const sent: string[] = [];
  const { container } = render(() => <AgentSession events={() => []} onSend={(t) => sent.push(t)} />);
  const box = composerOf(container);

  fireEvent.input(box, { target: { value: 'one' } });
  fireEvent.keyDown(box, { key: 'Enter', ctrlKey: true });
  expect(sent).toEqual(['one']);

  fireEvent.input(box, { target: { value: 'two' } });
  fireEvent.keyDown(box, { key: 'Enter', shiftKey: true });
  expect(sent).toEqual(['one']);

  fireEvent.keyDown(box, { key: 'Enter' });
  expect(sent).toEqual(['one', 'two']);
});

test('the up arrow walks back through this session\'s own prompts', () => {
  const events: AgentEvent[] = [
    { seq: 1, kind: 'user', text: 'first', at_ms: 0 },
    { seq: 2, kind: 'message', text: 'ok', at_ms: 0 },
    { seq: 3, kind: 'user', text: 'second', at_ms: 0 },
  ];
  const { container } = render(() => <AgentSession events={() => events} onSend={() => {}} />);
  const box = composerOf(container);

  fireEvent.keyDown(box, { key: 'ArrowUp' });
  expect(box.value).toBe('second');
  fireEvent.keyDown(box, { key: 'ArrowUp' });
  expect(box.value).toBe('first');
  // The oldest is a floor, not a wrap-around.
  fireEvent.keyDown(box, { key: 'ArrowUp' });
  expect(box.value).toBe('first');

  fireEvent.keyDown(box, { key: 'ArrowDown' });
  expect(box.value).toBe('second');
  fireEvent.keyDown(box, { key: 'ArrowDown' });
  expect(box.value).toBe('');

  // A non-empty draft keeps the arrow as an arrow.
  fireEvent.input(box, { target: { value: 'typing' } });
  fireEvent.keyDown(box, { key: 'ArrowUp' });
  expect(box.value).toBe('typing');
});

test('a prompt is recallable before agentd has echoed it back', () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const box = composerOf(container);
  fireEvent.input(box, { target: { value: 'not yet echoed' } });
  fireEvent.keyDown(box, { key: 'Enter' });
  expect(box.value).toBe('');
  fireEvent.keyDown(box, { key: 'ArrowUp' });
  expect(box.value).toBe('not yet echoed');
});

test('Stop is offered while a question is pending, and Esc is the same verb', () => {
  const asks = [{ id: 'a1', tool: 'Bash', subject: 'rm -rf /', age_ms: 0 }];
  let cancels = 0;
  const { container } = render(() => (
    <AgentSession
      events={() => []}
      asks={() => asks}
      status={() => ({ state: 'needs-input' })}
      onSend={() => {}}
      onCancel={() => { cancels++; }}
    />
  ));

  // Previously the Stop button hid itself the moment an ask appeared.
  const stop = container.querySelector('[data-testid="agent-stop"]') as HTMLButtonElement;
  expect(stop).not.toBeNull();
  stop.click();
  expect(cancels).toBe(1);

  fireEvent.keyDown(composerOf(container), { key: 'Escape' });
  expect(cancels).toBe(2);
});

test('Esc does nothing when there is no turn to stop', () => {
  let cancels = 0;
  const { container } = render(() => (
    <AgentSession events={() => []} status={() => ({ state: 'done' })} onSend={() => {}} onCancel={() => { cancels++; }} />
  ));
  expect(container.querySelector('[data-testid="agent-stop"]')).toBeNull();
  fireEvent.keyDown(composerOf(container), { key: 'Escape' });
  expect(cancels).toBe(0);
});

// docs/Review-findings.md P2 → agent: "attach files/images or paste an
// image". The composer sent text and nothing else.
test('Attach… adds a file chip that goes out as a block and is cleared on send', async () => {
  const sent: [string, unknown][] = [];
  const { container } = render(() => (
    <AgentSession
      events={() => []}
      onSend={(t, b) => sent.push([t, b])}
      onPickFiles={() => Promise.resolve(['/w/proj/notes.txt'])}
    />
  ));

  (container.querySelector('[data-testid="agent-attach"]') as HTMLButtonElement).click();
  await settle();
  const chip = container.querySelector('[data-testid="agent-attachment"]');
  expect(chip?.getAttribute('data-kind')).toBe('file');
  expect(chip?.textContent).toContain('notes.txt');

  const composer = composerOf(container);
  fireEvent.input(composer, { target: { value: 'look at this' } });
  fireEvent.keyDown(composer, { key: 'Enter' });

  expect(sent).toEqual([['look at this', [{ type: 'file', path: '/w/proj/notes.txt', name: 'notes.txt' }]]]);
  // The attachment belonged to that message; it must not ride along on
  // the next one.
  expect(container.querySelector('[data-testid="agent-attachment"]')).toBeNull();
});

test('a host with no file client is offered no Attach button', () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  expect(container.querySelector('[data-testid="agent-attach"]')).toBeNull();
});

test('an attachment alone is a sendable message', async () => {
  const sent: [string, unknown][] = [];
  const { container } = render(() => (
    <AgentSession events={() => []} onSend={(t, b) => sent.push([t, b])} onPickFiles={() => Promise.resolve(['/w/a.txt'])} />
  ));
  (container.querySelector('[data-testid="agent-attach"]') as HTMLButtonElement).click();
  await settle();
  fireEvent.keyDown(composerOf(container), { key: 'Enter' });
  expect(sent).toHaveLength(1);
  expect(sent[0][0]).toBe('');
});

test('an oversized pasted image is refused with a reason, not silently', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const composer = composerOf(container);
  const big = new File([new Uint8Array(3 * 1024 * 1024)], 'huge.png', { type: 'image/png' });
  composer.dispatchEvent(pasteEvent([big]));
  await settle();

  expect(container.querySelector('[data-testid="agent-attachment"]')).toBeNull();
  expect(container.querySelector('[data-testid="agent-drop-note"]')?.textContent).toMatch(/huge\.png is \d+ KB, over/);
});

test('a pasted image becomes a chip and goes out as an image block', async () => {
  const sent: [string, unknown][] = [];
  const { container } = render(() => <AgentSession events={() => []} onSend={(t, b) => sent.push([t, b])} />);
  const composer = composerOf(container);

  const png = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], 'shot.png', { type: 'image/png' });
  const ev = pasteEvent([png]);
  composer.dispatchEvent(ev);
  await settle();

  // A text paste must still be the browser's; only an image is taken.
  expect(ev.defaultPrevented).toBe(true);
  const chip = container.querySelector('[data-testid="agent-attachment"]');
  expect(chip?.getAttribute('data-kind')).toBe('image');
  expect(chip?.querySelector('img')?.getAttribute('src')).toMatch(/^data:image\/png;base64,/);

  fireEvent.input(composer, { target: { value: 'what is this?' } });
  fireEvent.keyDown(composer, { key: 'Enter' });
  const blocks = sent[0][1] as { type: string; mime: string; data: string }[];
  expect(blocks).toHaveLength(1);
  expect(blocks[0].type).toBe('image');
  expect(blocks[0].mime).toBe('image/png');
  expect(blocks[0].data.length).toBeGreaterThan(0);
});

test('a plain text paste is left to the browser', async () => {
  const { container } = render(() => <AgentSession events={() => []} onSend={() => {}} />);
  const ev = pasteEvent([]);
  composerOf(container).dispatchEvent(ev);
  await settle();
  expect(ev.defaultPrevented).toBe(false);
  expect(container.querySelector('[data-testid="agent-attachment"]')).toBeNull();
});

// docs/Review-findings.md P2 → agent: "fs/terminal confined to the session
// cwd with no override". The extra folders are shown NAMED, because the
// whole hazard of widening a session is forgetting that you did.
test('extra roots are named in the status bar and can be taken back', () => {
  const removed: string[] = [];
  const { container } = render(() => (
    <AgentSession
      events={() => []}
      status={() => ({ dir: 'app', roots: ['/w/lib', '/w/schema'] })}
      onSend={() => {}}
      onRemoveRoot={(p) => removed.push(p)}
    />
  ));

  const chips = Array.from(container.querySelectorAll('[data-testid="agent-root"]'));
  expect(chips.map((c) => c.getAttribute('data-path'))).toEqual(['/w/lib', '/w/schema']);
  expect(chips[0].textContent).toContain('lib');

  (chips[1].querySelector('[data-testid="agent-root-remove"]') as HTMLButtonElement).click();
  expect(removed).toEqual(['/w/schema']);
});

test('a session confined to its cwd shows no root chips', () => {
  const { container } = render(() => (
    <AgentSession events={() => []} status={() => ({ dir: 'app' })} onSend={() => {}} />
  ));
  expect(container.querySelector('[data-testid="agent-root"]')).toBeNull();
});
