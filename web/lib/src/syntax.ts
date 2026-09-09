// Syntax highlighting for transcript code blocks (docs/Review-findings.md
// P2 → agent: "syntax highlighting in fences").
//
// Framework-free: the decisions — which language a fence info string
// means, and which role each run of characters plays — live here under
// node:test, and the Solid half in markdown.tsx only paints roles as
// colours. Nothing here produces HTML: the output is a list of text runs,
// each with a role, so the renderer keeps building elements the way the
// rest of the Markdown does (no innerHTML, no sanitiser to get wrong).
//
// Parsers are the same Lezer grammars wash-edit already vendors through
// CodeMirror, used directly: @lezer/highlight walks the tree and names
// roles, and no editor machinery comes along. Languages without a Lezer
// grammar (shell, most notably) render plain — a wrong highlight is
// worse than none.

import { highlightTree, tagHighlighter, tags as t } from '@lezer/highlight';
import type { Parser } from '@lezer/common';
import { parser as javascript } from '@lezer/javascript';
import { parser as python } from '@lezer/python';
import { parser as go } from '@lezer/go';
import { parser as rust } from '@lezer/rust';
import { parser as json } from '@lezer/json';
import { parser as yaml } from '@lezer/yaml';
import { parser as css } from '@lezer/css';
import { parser as html } from '@lezer/html';
import { parser as cpp } from '@lezer/cpp';
import { parser as java } from '@lezer/java';

/** The roles a run of code can have. The renderer maps each to a colour;
 *  the diff roles are what a unified diff's lines get. */
export type SyntaxRole =
  | 'keyword' | 'string' | 'comment' | 'number' | 'type' | 'function' | 'property' | 'operator' | 'meta'
  | 'add' | 'del' | 'hunk';

export interface SyntaxSpan {
  text: string;
  role?: SyntaxRole;
}

/** MAX_HIGHLIGHT_BYTES bounds what gets parsed. Past it a block renders
 *  plain: a pasted log is not code, and a parse is not free. */
export const MAX_HIGHLIGHT_BYTES = 64 * 1024;

// Fence info strings people and models actually write, normalised to the
// grammar that reads them. Anything not listed renders plain.
const LANGS: Record<string, () => Parser> = {
  javascript: () => javascript,
  js: () => javascript,
  mjs: () => javascript,
  cjs: () => javascript,
  jsx: () => javascript.configure({ dialect: 'jsx' }),
  typescript: () => javascript.configure({ dialect: 'ts' }),
  ts: () => javascript.configure({ dialect: 'ts' }),
  tsx: () => javascript.configure({ dialect: 'ts jsx' }),
  python: () => python,
  py: () => python,
  go: () => go,
  golang: () => go,
  rust: () => rust,
  rs: () => rust,
  json: () => json,
  jsonc: () => json,
  yaml: () => yaml,
  yml: () => yaml,
  css: () => css,
  html: () => html,
  htm: () => html,
  xml: () => html,
  svg: () => html,
  c: () => cpp,
  h: () => cpp,
  cpp: () => cpp,
  cc: () => cpp,
  cxx: () => cpp,
  hpp: () => cpp,
  'c++': () => cpp,
  java: () => java,
};

/** syntaxLang normalises a fence info string ("TypeScript", "c++",
 *  "shell-session") to the key highlightSpans understands, or '' when
 *  there is no grammar for it. `diff` and `patch` are their own thing. */
export function syntaxLang(info: string): string {
  const k = (info ?? '').trim().toLowerCase().split(/[\s{]/, 1)[0];
  if (k === 'diff' || k === 'patch') return 'diff';
  return k in LANGS ? k : '';
}

// Role per Lezer tag. The list is deliberately short: a transcript block
// wants keywords, strings, comments and a little structure, not an
// editor theme's forty distinctions.
const roles = tagHighlighter([
  { tag: [t.keyword, t.modifier, t.controlKeyword, t.operatorKeyword, t.definitionKeyword, t.moduleKeyword, t.self, t.null], class: 'keyword' },
  { tag: [t.string, t.special(t.string), t.regexp, t.character, t.docString], class: 'string' },
  { tag: [t.comment, t.lineComment, t.blockComment], class: 'comment' },
  { tag: [t.number, t.integer, t.float, t.bool, t.atom, t.literal], class: 'number' },
  { tag: [t.typeName, t.className, t.namespace, t.tagName], class: 'type' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.definition(t.function(t.variableName)), t.macroName], class: 'function' },
  { tag: [t.propertyName, t.attributeName, t.definition(t.propertyName)], class: 'property' },
  { tag: [t.operator, t.punctuation, t.bracket], class: 'operator' },
  { tag: [t.meta, t.processingInstruction, t.annotation, t.labelName], class: 'meta' },
]);

/** highlightSpans splits code into runs with roles for `lang` (a key
 *  syntaxLang returned). Unknown language, or a block over the cap, is
 *  one plain run. The runs concatenate back to exactly `code`. */
export function highlightSpans(code: string, lang: string): SyntaxSpan[] {
  if (lang === 'diff') return diffSpans(code);
  const make = LANGS[lang];
  if (!make || code === '' || code.length > MAX_HIGHLIGHT_BYTES) return [{ text: code }];
  let parser: Parser;
  try {
    parser = make();
  } catch {
    return [{ text: code }];
  }
  const tree = parser.parse(code);
  const out: SyntaxSpan[] = [];
  let pos = 0;
  highlightTree(tree, roles, (from, to, classes) => {
    if (from > pos) out.push({ text: code.slice(pos, from) });
    out.push({ text: code.slice(from, to), role: classes.split(' ')[0] as SyntaxRole });
    pos = to;
  });
  if (pos < code.length) out.push({ text: code.slice(pos) });
  return out;
}

/** diffSpans colours a unified diff line by line: added, removed, hunk
 *  headers, and the file headers as meta. Each run is one line including
 *  its newline, so the runs concatenate back to the input. */
export function diffSpans(text: string): SyntaxSpan[] {
  const out: SyntaxSpan[] = [];
  const lines = text.split('\n');
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] + (i < lines.length - 1 ? '\n' : '');
    if (line === '') continue;
    let role: SyntaxRole | undefined;
    if (line.startsWith('+++') || line.startsWith('---')) role = 'meta';
    else if (line.startsWith('@@')) role = 'hunk';
    else if (line.startsWith('+')) role = 'add';
    else if (line.startsWith('-')) role = 'del';
    else if (line.startsWith('…')) role = 'meta';
    out.push(role ? { text: line, role } : { text: line });
  }
  return out;
}

/** diffFiles lists the paths a unified diff touches, from its +++ headers
 *  (else ---), stripped of the a/ b/ prefixes. */
export function diffFiles(text: string): string[] {
  const out: string[] = [];
  for (const line of text.split('\n')) {
    const m = /^\+\+\+ (?:b\/)?(.+)$/.exec(line) ?? /^--- (?:a\/)?(.+)$/.exec(line);
    if (!m || m[1] === '/dev/null') continue;
    const p = m[1].startsWith('/') ? m[1] : '/' + m[1];
    if (!out.includes(p)) out.push(p);
  }
  return out;
}
