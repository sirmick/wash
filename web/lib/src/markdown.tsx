// A small, safe Markdown renderer for agent output (docs/AGENT_APP.md §9).
//
// Agents answer in Markdown, so a transcript that renders it as
// pre-wrapped text shows `###` and backticks as literal noise.
//
// The important decision here is what this DOESN'T do: it never produces
// an HTML string, and nothing ever reaches innerHTML. Every node is built
// as a Solid element, so agent output — which is model-generated text
// wash does not control — has no path to executing anything, and there is
// no sanitiser to get subtly wrong. That is worth more than completeness:
// this is a deliberately partial Markdown, covering what models actually
// emit in a chat reply.
//
// Supported: ATX headings, fenced code blocks, blockquotes, unordered and
// ordered lists, paragraphs, and inline code / bold / italic / links.
// Everything unrecognised renders as its own literal text, which is the
// correct failure for a renderer whose input is untrusted.

import { For, Show } from 'solid-js';
import type { Component, JSX } from 'solid-js';
import { tokens } from './tokens';

type Block =
  | { t: 'p'; lines: string[] }
  | { t: 'h'; level: number; text: string }
  | { t: 'code'; lang: string; lines: string[] }
  | { t: 'quote'; lines: string[] }
  | { t: 'list'; ordered: boolean; items: string[] };

const H_RE = /^(#{1,6})\s+(.*)$/;
const FENCE_RE = /^\s*```(\w*)\s*$/;
const UL_RE = /^\s*[-*+]\s+(.*)$/;
const OL_RE = /^\s*\d+[.)]\s+(.*)$/;
const QUOTE_RE = /^\s*>\s?(.*)$/;

/** parseBlocks splits text into block-level chunks. */
export function parseBlocks(src: string): Block[] {
  const lines = src.split('\n');
  const out: Block[] = [];
  let i = 0;

  const flushPara = (buf: string[]) => {
    if (buf.length) out.push({ t: 'p', lines: buf.slice() });
    buf.length = 0;
  };

  const para: string[] = [];
  while (i < lines.length) {
    const line = lines[i];

    const fence = FENCE_RE.exec(line);
    if (fence) {
      flushPara(para);
      const body: string[] = [];
      i++;
      // An unterminated fence runs to the end — which is exactly what a
      // half-streamed code block looks like, and it must still render.
      while (i < lines.length && !FENCE_RE.test(lines[i])) body.push(lines[i++]);
      i++; // consume the closing fence if present
      out.push({ t: 'code', lang: fence[1] ?? '', lines: body });
      continue;
    }

    const h = H_RE.exec(line);
    if (h) {
      flushPara(para);
      out.push({ t: 'h', level: h[1].length, text: h[2] });
      i++;
      continue;
    }

    if (QUOTE_RE.test(line)) {
      flushPara(para);
      const body: string[] = [];
      while (i < lines.length) {
        const m = QUOTE_RE.exec(lines[i]);
        if (!m) break;
        body.push(m[1]);
        i++;
      }
      out.push({ t: 'quote', lines: body });
      continue;
    }

    const isUL = UL_RE.test(line);
    const isOL = OL_RE.test(line);
    if (isUL || isOL) {
      flushPara(para);
      const items: string[] = [];
      const re = isUL ? UL_RE : OL_RE;
      while (i < lines.length) {
        const m = re.exec(lines[i]);
        if (!m) break;
        items.push(m[1]);
        i++;
      }
      out.push({ t: 'list', ordered: isOL, items });
      continue;
    }

    if (line.trim() === '') {
      flushPara(para);
      i++;
      continue;
    }

    para.push(line);
    i++;
  }
  flushPara(para);
  return out;
}

type Span =
  | { t: 'text'; s: string }
  | { t: 'code'; s: string }
  | { t: 'strong'; s: string }
  | { t: 'em'; s: string }
  | { t: 'link'; s: string; href: string };

// Inline scanner. Code spans win over emphasis, because `*` inside
// backticks is a literal asterisk and getting that backwards mangles
// every shell glob an agent prints.
const INLINE_RE = /(`[^`]+`)|(\[[^\]]+\]\([^)\s]+\))|(\*\*[^*]+\*\*)|(\*[^*]+\*)|(_[^_]+_)/;

export function parseInline(src: string): Span[] {
  const out: Span[] = [];
  let rest = src;
  for (;;) {
    const m = INLINE_RE.exec(rest);
    if (!m || m.index === undefined) break;
    if (m.index > 0) out.push({ t: 'text', s: rest.slice(0, m.index) });
    const tok = m[0];
    if (tok.startsWith('`')) {
      out.push({ t: 'code', s: tok.slice(1, -1) });
    } else if (tok.startsWith('[')) {
      const close = tok.indexOf(']');
      out.push({ t: 'link', s: tok.slice(1, close), href: tok.slice(close + 2, -1) });
    } else if (tok.startsWith('**')) {
      out.push({ t: 'strong', s: tok.slice(2, -2) });
    } else {
      out.push({ t: 'em', s: tok.slice(1, -1) });
    }
    rest = rest.slice(m.index + tok.length);
  }
  if (rest) out.push({ t: 'text', s: rest });
  return out;
}

const codeStyle: JSX.CSSProperties = {
  font: tokens.type.monoSm,
  background: tokens.bgInset,
  border: `1px solid ${tokens.borderMenu}`,
  'border-radius': tokens.radiusSm,
  padding: '1px 4px',
};

const Inline: Component<{ text: string }> = (p) => (
  <For each={parseInline(p.text)}>
    {(s) => (
      <Show when={s.t !== 'text'} fallback={<>{(s as { s: string }).s}</>}>
        <Show when={s.t === 'code'}>
          <code style={codeStyle}>{s.s}</code>
        </Show>
        <Show when={s.t === 'strong'}>
          <strong style={{ 'font-weight': '600' }}>{s.s}</strong>
        </Show>
        <Show when={s.t === 'em'}>
          <em>{s.s}</em>
        </Show>
        <Show when={s.t === 'link'}>
          {/* Rendered as text plus its target rather than an anchor: the
              href comes from model output, and a transcript is not a place
              to hand it a click. */}
          <span style={{ color: tokens.accentBlue }}>{s.s}</span>
          <span style={{ color: tokens.fgDim, font: tokens.type.monoSm }}>
            {' '}
            ({(s as { href: string }).href})
          </span>
        </Show>
      </Show>
    )}
  </For>
);

export interface MarkdownProps {
  text: string;
}

/** Renders Markdown as Solid elements. Never touches innerHTML. */
export const Markdown: Component<MarkdownProps> = (props) => (
  <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceMd}px` }}>
    <For each={parseBlocks(props.text)}>
      {(b) => (
        <>
          <Show when={b.t === 'p'}>
            <div style={{ 'white-space': 'pre-wrap', 'overflow-wrap': 'anywhere' }}>
              <Inline text={(b as { lines: string[] }).lines.join('\n')} />
            </div>
          </Show>

          <Show when={b.t === 'h'}>
            <div
              style={{
                font: (b as { level: number }).level <= 2 ? tokens.type.titleSm : tokens.type.textMd,
                'font-weight': '600',
                color: tokens.fg,
                'margin-top': `${tokens.spaceXs}px`,
              }}
            >
              <Inline text={(b as { text: string }).text} />
            </div>
          </Show>

          <Show when={b.t === 'code'}>
            {/* Its own scroll container: a long command must not make the
                whole transcript scroll sideways. */}
            <pre
              style={{
                margin: 0,
                background: tokens.bgInset,
                border: `1px solid ${tokens.borderMenu}`,
                'border-radius': tokens.radiusMd,
                padding: `${tokens.spaceMd}px`,
                font: tokens.type.monoSm,
                color: tokens.fg,
                'overflow-x': 'auto',
              }}
            >
              {(b as { lines: string[] }).lines.join('\n')}
            </pre>
          </Show>

          <Show when={b.t === 'quote'}>
            <div
              style={{
                'border-left': `2px solid ${tokens.borderMenu}`,
                'padding-left': `${tokens.spaceMd}px`,
                color: tokens.fgMuted,
                'white-space': 'pre-wrap',
              }}
            >
              <Inline text={(b as { lines: string[] }).lines.join('\n')} />
            </div>
          </Show>

          <Show when={b.t === 'list'}>
            <div style={{ display: 'flex', 'flex-direction': 'column', gap: `${tokens.spaceXs}px` }}>
              <For each={(b as { items: string[] }).items}>
                {(item, idx) => (
                  <div style={{ display: 'flex', gap: `${tokens.spaceMd}px` }}>
                    <span style={{ color: tokens.fgDim, flex: 'none', 'min-width': '1.2em' }}>
                      {(b as { ordered: boolean }).ordered ? `${idx() + 1}.` : '•'}
                    </span>
                    <span style={{ 'overflow-wrap': 'anywhere' }}>
                      <Inline text={item} />
                    </span>
                  </div>
                )}
              </For>
            </div>
          </Show>
        </>
      )}
    </For>
  </div>
);
