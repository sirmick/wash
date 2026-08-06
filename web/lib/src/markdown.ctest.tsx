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
