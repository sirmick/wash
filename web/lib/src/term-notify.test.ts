import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { Osc99, parseOsc777, parseOsc9 } from './term-notify.ts';

const b64 = (s: string) => Buffer.from(s, 'utf-8').toString('base64');

test('OSC 9 is a body-only notification', () => {
  assert.deepEqual(parseOsc9('Build finished'), { title: '', body: 'Build finished' });
  assert.deepEqual(parseOsc9('3 tests failed'), { title: '', body: '3 tests failed' });
});

test('ConEmu OSC 9 subcommands are not notifications', () => {
  assert.equal(parseOsc9('4;1;50'), null); // progress
  assert.equal(parseOsc9('4;0'), null);
  assert.equal(parseOsc9('1;500'), null);
  assert.equal(parseOsc9('12'), null);
  assert.equal(parseOsc9('   '), null);
});

test('OSC 777 notify carries a title and a body, keeping ; in the body', () => {
  assert.deepEqual(parseOsc777('notify;Claude;Waiting for input'), { title: 'Claude', body: 'Waiting for input' });
  assert.deepEqual(parseOsc777('notify;T;a;b'), { title: 'T', body: 'a;b' });
  assert.deepEqual(parseOsc777('notify;Only title'), { title: 'Only title', body: '' });
});

test('OSC 777 verbs other than notify are ignored', () => {
  assert.equal(parseOsc777('precmd'), null);
  assert.equal(parseOsc777('notify'), null);
  assert.equal(parseOsc777('notify;;'), null);
});

test('OSC 99: a bare payload is a title, shown alone', () => {
  assert.deepEqual(new Osc99().feed(';Hello'), { title: 'Hello', body: '' });
});

test('OSC 99: title and body arrive as chunks under one id', () => {
  const k = new Osc99();
  assert.equal(k.feed('i=1:d=0;Claude'), null);
  assert.deepEqual(k.feed('i=1:p=body;Waiting for input'), { title: 'Claude', body: 'Waiting for input' });
});

test('OSC 99: chunks under different ids do not mix', () => {
  const k = new Osc99();
  assert.equal(k.feed('i=a:d=0;A'), null);
  assert.equal(k.feed('i=b:d=0;B'), null);
  assert.deepEqual(k.feed('i=a:p=body;from a'), { title: 'A', body: 'from a' });
  assert.deepEqual(k.feed('i=b:p=body;from b'), { title: 'B', body: 'from b' });
});

test('OSC 99: base64 payloads decode; a body alone becomes the title', () => {
  const k = new Osc99();
  assert.deepEqual(k.feed(`e=1:p=body;${b64('naïve ✓')}`), { title: 'naïve ✓', body: '' });
  assert.equal(k.feed('e=1;!!!'), null);
});

test('OSC 99: queries, icons and close requests raise nothing', () => {
  const k = new Osc99();
  assert.equal(k.feed('i=1:p=?;'), null);
  assert.equal(k.feed('i=1:p=icon;abc'), null);
  assert.equal(k.feed('i=1:p=close;'), null);
  assert.equal(k.feed('no separator'), null);
});

test('OSC 99: the pending buffer is bounded', () => {
  const k = new Osc99();
  for (let i = 0; i < 100; i++) k.feed(`i=${i}:d=0;t${i}`);
  // The oldest ids were evicted; a recent one still completes.
  assert.deepEqual(k.feed('i=99:p=body;b'), { title: 't99', body: 'b' });
  assert.deepEqual(k.feed('i=0:p=body;b'), { title: 'b', body: '' });
});

test('long text is clamped', () => {
  const n = parseOsc777(`notify;${'t'.repeat(1000)};${'b'.repeat(5000)}`);
  assert.equal(n?.title.length, 256);
  assert.equal(n?.body.length, 1024);
});
