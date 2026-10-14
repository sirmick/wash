import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { parseOsc52 } from './term-osc52.ts';

const b64 = (s: string) => Buffer.from(s, 'utf-8').toString('base64');

test('a clipboard write decodes to its text', () => {
  assert.equal(parseOsc52(`c;${b64('hello world')}`), 'hello world');
});

test('the targets are not distinguished, and may be empty', () => {
  for (const t of ['', 'p', 's', '0', 'cp']) {
    assert.equal(parseOsc52(`${t};${b64('x')}`), 'x');
  }
});

test('UTF-8 and newlines survive', () => {
  const text = 'naïve — ✓\nline two\n';
  assert.equal(parseOsc52(`c;${b64(text)}`), text);
});

test('a query never reads the clipboard back', () => {
  assert.equal(parseOsc52('c;?'), null);
});

test('a clear, a missing separator, and bad base64 copy nothing', () => {
  assert.equal(parseOsc52('c;'), null);
  assert.equal(parseOsc52(b64('x')), null);
  assert.equal(parseOsc52('c;!!!not base64'), null);
});

test('bytes that are not UTF-8 copy nothing', () => {
  assert.equal(parseOsc52(`c;${Buffer.from([0xff, 0xfe]).toString('base64')}`), null);
});
