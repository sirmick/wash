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
