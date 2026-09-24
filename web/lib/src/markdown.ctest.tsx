import { test, expect, afterEach } from 'vitest';
import { render, cleanup } from '@solidjs/testing-library';
import { Markdown } from './markdown.tsx';

afterEach(cleanup);

test('fenced code accepts common agent info strings and tilde fences', () => {
  const { container } = render(() => (
    <Markdown
      text={[
        '```shell-session',
        '$ pnpm test',
        '```',
        '',
        '```c++',
        'int main();',
        '```',
        '',
        '~~~text',
        'literal',
        '~~~',
      ].join('\n')}
    />
  ));

  const blocks = Array.from(container.querySelectorAll('pre'));
  expect(blocks).toHaveLength(3);
  expect(blocks.map((b) => b.textContent)).toEqual(['$ pnpm test', 'int main();', 'literal']);
  expect(container.textContent).not.toContain('```shell-session');
  expect(container.textContent).not.toContain('~~~text');
});

// docs/Review-findings.md P2 → agent: "copy a single code block",
// "syntax highlighting in fences".
test('a fenced block carries a copy button that copies only that block', async () => {
  const copied: string[] = [];
  (window as unknown as { wash: { clipboardSetText: (s: string) => void } }).wash = {
    clipboardSetText: (s: string) => copied.push(s),
  };

  const { container } = render(() => (
    <Markdown text={['before', '', '```sh', 'make test', '```', '', '```', 'other', '```'].join('\n')} />
  ));

  const buttons = Array.from(container.querySelectorAll('[data-testid="markdown-copy"]')) as HTMLButtonElement[];
  expect(buttons).toHaveLength(2);
  buttons[0].click();
  expect(copied).toEqual(['make test']);
  expect(buttons[0].textContent).toBe('Copied');
});

test('a fence with a known language is coloured; an unknown one is left plain', () => {
  const { container } = render(() => (
    <Markdown text={['```ts', 'const x = 1;', '```', '', '```shell-session', 'const x = 1;', '```'].join('\n')} />
  ));

  const pres = Array.from(container.querySelectorAll('pre'));
  expect(pres).toHaveLength(2);
  // The text is untouched by highlighting — the spans concatenate back.
  expect(pres.map((p) => p.textContent)).toEqual(['const x = 1;', 'const x = 1;']);
  expect(pres[0].querySelectorAll('span').length).toBeGreaterThan(0);
  expect(pres[1].querySelectorAll('span')).toHaveLength(0);
});

test('loose ordered lists retain numbering across blank lines and explicit starts', () => {
 const {container}=render(()=><Markdown text={'1. First\n\n2. Second\n\n3. Third\n\nA paragraph.\n\n7. Seventh\n8. Eighth'}/>);
 expect(container.textContent).toBe('1.First2.Second3.ThirdA paragraph.7.Seventh8.Eighth');
});
test('Markdown lists numbered with repeated ones count forward while streaming', () => {
 const {container}=render(()=><Markdown text={'1. First\n\n1. Second\n\n1. Third\n\n'}/>);
 expect(container.textContent).toBe('1.First2.Second3.Third');
});

// A teammate's inbox message rendered its Markdown raw (seen live: a
// "**Delivered:**" report). A human's message stays literal.
test('a workspace message renders a teammate body as Markdown and a human one literally', async () => {
  const { Collaboration } = await import('./agent-session.tsx');
  const teammate = render(() => (
    <Collaboration text={'G1 implementer (b2e9) · result\n\n**Delivered:** 6 files\n\n- one\n- two'} />
  ));
  expect(teammate.container.querySelector('strong')?.textContent).toBe('Delivered:');
  expect(teammate.container.textContent).toContain('•one');
  expect(teammate.container.textContent).not.toContain('- one');
  expect(teammate.container.textContent).toContain('G1 implementer (b2e9) · result');
  expect(teammate.container.textContent).not.toContain('**');
  cleanup();
  const human = render(() => <Collaboration text={'human · decision_response\n\ngo with **A**'} />);
  expect(human.container.querySelector('strong')).toBeNull();
  expect(human.container.textContent).toContain('go with **A**');
});

// Wash's approval verdicts render as a coloured row, not transcript prose.
test('a decision event renders as a distinct approved or refused row', async () => {
  const { DecisionRow } = await import('./agent-session.tsx');
  const ok = render(() => (
    <DecisionRow e={{ seq: 1, kind: 'decision', status: 'allow', title: 'Bash', detail: "python3 - <<'EOF' …", reason: 'yolo', at_ms: 0 }} />
  ));
  const row = ok.getByTestId('agent-decision');
  expect(row.dataset.status).toBe('allow');
  expect(row.textContent).toContain('✓ Auto-approved');
  expect(row.textContent).toContain('Bash');
  expect(row.textContent).toContain('yolo');
  cleanup();
  const no = render(() => (
    <DecisionRow e={{ seq: 2, kind: 'decision', status: 'cancelled', title: 'Bash', detail: 'rm -rf build', reason: 'nobody answered in time', at_ms: 0 }} />
  ));
  expect(no.getByTestId('agent-decision').textContent).toContain('✕ Not approved');
  expect(no.getByTestId('agent-decision').textContent).toContain('nobody answered in time');
});
