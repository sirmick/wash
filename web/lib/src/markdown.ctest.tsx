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
