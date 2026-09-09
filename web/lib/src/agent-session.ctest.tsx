import { test, expect, beforeEach, afterEach } from 'vitest';
import { render, cleanup } from '@solidjs/testing-library';
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

test('an OS text file is attached inline as a fenced block; a binary is named as skipped', async () => {
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
  const note = container.querySelector('[data-testid="agent-drop-note"]');
  expect(note?.textContent).toMatch(/Not attached .*shot\.png/);
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
