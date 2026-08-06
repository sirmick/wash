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
