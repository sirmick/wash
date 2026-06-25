// WYSIWYG markdown editor for wash-edit. TipTap (ProseMirror) under
// the hood; markdown round-trip via tiptap-markdown. One create call
// per .md tab — handles stay alive while their tab is open so each
// tab keeps its own undo stack, the same per-tab discipline the
// CodeMirror side enforces with captured EditorState.
//
// The on-disk file is the source of truth; the editor parses MD on
// open and re-serializes on save. tiptap-markdown is set to
// html: true so raw HTML blocks survive round-trip. Constructs we
// don't model (footnotes, math, mermaid, …) get normalized away on
// save — documented in main.tsx.

import { Editor, Extension } from '@tiptap/core';
import { Plugin, PluginKey } from '@tiptap/pm/state';
import { Decoration, DecorationSet } from '@tiptap/pm/view';
import type { Node as PMNode } from '@tiptap/pm/model';
import StarterKit from '@tiptap/starter-kit';
import Image from '@tiptap/extension-image';
import { Table } from '@tiptap/extension-table';
import TableRow from '@tiptap/extension-table-row';
import TableHeader from '@tiptap/extension-table-header';
import TableCell from '@tiptap/extension-table-cell';
import TaskList from '@tiptap/extension-task-list';
import TaskItem from '@tiptap/extension-task-item';
import { Markdown } from 'tiptap-markdown';
import { tokens } from '@wash/ui';

export interface WysiwygOpts {
  parent: HTMLElement;
  content: string;
  onChange?: (md: string) => void;
  onDirtyChange?: (dirty: boolean) => void;
}

// Find/replace state as reported to the find bar: total match count
// plus the 0-based index of the current match (-1 when none).
export interface WysiwygSearchState {
  count: number;
  current: number;
}

export interface WysiwygHandle {
  editor: Editor;
  getMarkdown(): string;
  setMarkdown(md: string): void;
  destroy(): void;
  focus(): void;
  markClean(): void;
  search: {
    set(query: string): WysiwygSearchState;
    next(): WysiwygSearchState;
    prev(): WysiwygSearchState;
    replace(repl: string): WysiwygSearchState;
    replaceAll(repl: string): WysiwygSearchState;
    clear(): void;
    state(): WysiwygSearchState;
  };
}

// Tab-style keyboard binding: in TipTap a plain Tab key would move
// browser focus. We map Tab to "indent the current list item" inside
// lists and let the default key handler ignore it otherwise (so
// Tab outside a list still moves focus — matches Notion / Bear).
const ListTab = Extension.create({
  name: 'listTab',
  addKeyboardShortcuts() {
    return {
      Tab: () => {
        if (this.editor.isActive('listItem')) return this.editor.commands.sinkListItem('listItem');
        if (this.editor.isActive('taskItem')) return this.editor.commands.sinkListItem('taskItem');
        return false;
      },
      'Shift-Tab': () => {
        if (this.editor.isActive('listItem')) return this.editor.commands.liftListItem('listItem');
        if (this.editor.isActive('taskItem')) return this.editor.commands.liftListItem('taskItem');
        return false;
      },
    };
  },
});

// ---- find / replace ----
//
// ProseMirror has no built-in document search, so the WYSIWYG side
// carries its own tiny plugin: plain-text case-insensitive matching
// with inline decorations for every hit plus a distinct class on the
// current one. The find bar in main.tsx drives it exclusively through
// the WysiwygHandle.search API — the plugin never owns UI.

interface SearchMatch {
  from: number;
  to: number;
}

interface SearchPluginState {
  query: string;
  matches: SearchMatch[];
  current: number; // index into matches, -1 when none
}

const searchKey = new PluginKey<SearchPluginState>('washSearch');

// findMatches scans every textblock, flattening its inline text nodes
// into one string so a match can span mark boundaries (e.g. "foo**bar**"
// matches "foobar"). Non-text inline nodes (images, hard breaks)
// contribute a NUL sentinel so matches can't silently swallow them.
function findMatches(doc: PMNode, query: string): SearchMatch[] {
  const out: SearchMatch[] = [];
  if (!query) return out;
  const q = query.toLowerCase();
  doc.descendants((node, pos) => {
    if (!node.isTextblock) return true;
    let text = '';
    const segs: { start: number; end: number; pos: number }[] = [];
    node.content.forEach((child, offset) => {
      if (child.isText && child.text) {
        segs.push({ start: text.length, end: text.length + child.text.length, pos: pos + 1 + offset });
        text += child.text;
      } else {
        text += '\u0000';
      }
    });
    const posAt = (off: number): number | null => {
      for (const s of segs) if (off >= s.start && off < s.end) return s.pos + (off - s.start);
      return null;
    };
    const lower = text.toLowerCase();
    let i = lower.indexOf(q);
    while (i >= 0) {
      const from = posAt(i);
      const last = posAt(i + q.length - 1);
      if (from != null && last != null) out.push({ from, to: last + 1 });
      i = lower.indexOf(q, i + q.length);
    }
    return false; // children already consumed via the flat scan above
  });
  return out;
}

// The dispatcher (handle.search below) computes the desired query +
// current index and ships them via meta; apply() recomputes matches
// against the post-transaction doc. Plain doc edits with an active
// query also rescan so highlights track typing — the scan is a linear
// string pass, cheap at editor-document sizes.
const SearchExt = Extension.create({
  name: 'washSearch',
  addProseMirrorPlugins() {
    return [
      new Plugin<SearchPluginState>({
        key: searchKey,
        state: {
          init: () => ({ query: '', matches: [], current: -1 }),
          apply(tr, prev): SearchPluginState {
            const meta = tr.getMeta(searchKey) as { query: string; current: number } | undefined;
            if (meta) {
              const matches = findMatches(tr.doc, meta.query);
              const current = matches.length ? Math.min(Math.max(meta.current, 0), matches.length - 1) : -1;
              return { query: meta.query, matches, current };
            }
            if (tr.docChanged && prev.query) {
              const matches = findMatches(tr.doc, prev.query);
              const current = matches.length ? Math.min(Math.max(prev.current, 0), matches.length - 1) : -1;
              return { query: prev.query, matches, current };
            }
            return prev;
          },
        },
        props: {
          decorations(state) {
            const s = searchKey.getState(state);
            if (!s || !s.matches.length) return DecorationSet.empty;
            return DecorationSet.create(state.doc, s.matches.map((m, i) =>
              Decoration.inline(m.from, m.to, {
                class: i === s.current ? 'wash-find-match wash-find-current' : 'wash-find-match',
              })));
          },
        },
      }),
    ];
  },
});

let stylesInjected = false;
function injectStyles() {
  if (stylesInjected) return;
  stylesInjected = true;
  const style = document.createElement('style');
  style.id = '__wash_wysiwyg_css__';
  // Scope every rule under .wash-wysiwyg so this stylesheet can't
  // leak into the rest of wash-edit's chrome.
  style.textContent = `
.wash-wysiwyg {
  height: 100%;
  outline: none;
  padding: 16px 24px;
  overflow: auto;
  color: ${tokens.fg};
  background: ${tokens.bgWindow};
  font: ${tokens.type.textMd};
  line-height: 1.55;
  caret-color: ${tokens.fg};
}
.wash-wysiwyg:focus { outline: none; }
.wash-wysiwyg > * { margin: 0 0 0.6em 0; }
.wash-wysiwyg > *:last-child { margin-bottom: 0; }
.wash-wysiwyg h1 { font-size: 1.9em; font-weight: 600; margin: 0.6em 0 0.4em; color: ${tokens.accentViolet}; }
.wash-wysiwyg h2 { font-size: 1.5em; font-weight: 600; margin: 0.8em 0 0.35em; color: ${tokens.accentViolet}; }
.wash-wysiwyg h3 { font-size: 1.25em; font-weight: 600; margin: 0.7em 0 0.3em; color: ${tokens.accentViolet}; }
.wash-wysiwyg h4, .wash-wysiwyg h5, .wash-wysiwyg h6 { font-weight: 600; margin: 0.6em 0 0.3em; color: ${tokens.fg}; }
.wash-wysiwyg p { margin: 0 0 0.6em; }
.wash-wysiwyg strong { font-weight: 700; color: ${tokens.fg}; }
.wash-wysiwyg em { font-style: italic; }
.wash-wysiwyg s { color: ${tokens.fgDim}; }
.wash-wysiwyg a { color: ${tokens.accentBlue}; text-decoration: underline; cursor: pointer; }
.wash-wysiwyg code {
  font-family: ${tokens.fontMono};
  font-size: 0.92em;
  background: ${tokens.bgInset};
  border: 1px solid ${tokens.borderMenu};
  border-radius: ${tokens.radiusSm};
  padding: 0 4px;
  color: ${tokens.accentAmber};
}
.wash-wysiwyg pre {
  font-family: ${tokens.fontMono};
  font-size: 0.92em;
  background: ${tokens.bgInset};
  border: 1px solid ${tokens.borderMenu};
  border-radius: ${tokens.radiusMd};
  padding: 10px 12px;
  overflow-x: auto;
  color: ${tokens.fg};
}
.wash-wysiwyg pre code {
  background: transparent;
  border: none;
  padding: 0;
  color: inherit;
}
.wash-wysiwyg blockquote {
  margin: 0 0 0.6em;
  padding: 0.2em 0 0.2em 12px;
  border-left: 3px solid ${tokens.borderMenu};
  color: ${tokens.fgDim};
}
.wash-wysiwyg ul, .wash-wysiwyg ol { margin: 0 0 0.6em; padding-left: 1.6em; }
.wash-wysiwyg ul.tight p, .wash-wysiwyg ol.tight p { margin: 0; }
.wash-wysiwyg li { margin: 0.15em 0; }
.wash-wysiwyg hr {
  border: none;
  border-top: 1px solid ${tokens.borderMenu};
  margin: 1em 0;
}
.wash-wysiwyg ul[data-type="taskList"] { list-style: none; padding-left: 0.4em; }
.wash-wysiwyg ul[data-type="taskList"] li { display: flex; align-items: flex-start; gap: 8px; }
.wash-wysiwyg ul[data-type="taskList"] li > label { flex-shrink: 0; margin-top: 4px; }
.wash-wysiwyg ul[data-type="taskList"] li > div { flex: 1; }
.wash-wysiwyg ul[data-type="taskList"] li > div > p { margin: 0; }
.wash-wysiwyg ul[data-type="taskList"] li[data-checked="true"] > div { color: ${tokens.fgDim}; text-decoration: line-through; }
.wash-wysiwyg ul[data-type="taskList"] input[type="checkbox"] {
  appearance: none; -webkit-appearance: none;
  width: 14px; height: 14px;
  background: ${tokens.bgWindow};
  border: 1px solid ${tokens.borderMenu};
  border-radius: ${tokens.radiusSm};
  cursor: pointer;
  background-repeat: no-repeat;
  background-position: center;
  background-size: 11px 11px;
}
.wash-wysiwyg ul[data-type="taskList"] input[type="checkbox"]:checked {
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 24 24' fill='none' stroke='%23eeeeeed9' stroke-width='3' stroke-linecap='round' stroke-linejoin='round'%3E%3Cpath d='M20 6 9 17l-5-5'/%3E%3C/svg%3E");
}
.wash-wysiwyg table {
  border-collapse: collapse;
  margin: 0 0 0.6em;
  width: 100%;
  table-layout: auto;
}
.wash-wysiwyg th, .wash-wysiwyg td {
  border: 1px solid ${tokens.borderMenu};
  padding: 4px 8px;
  text-align: left;
  vertical-align: top;
  min-width: 60px;
}
.wash-wysiwyg th { background: ${tokens.bgMenu}; font-weight: 600; }
.wash-wysiwyg img { max-width: 100%; height: auto; border-radius: ${tokens.radiusSm}; }
.wash-wysiwyg .ProseMirror-selectednode {
  outline: 2px solid ${tokens.borderFocus};
  outline-offset: 2px;
}
.wash-wysiwyg ::selection { background: ${tokens.bgRowSelected}; }
/* Find/replace match highlights — same palette as the CM source
   view's .cm-searchMatch so the two modes read identically. */
.wash-wysiwyg .wash-find-match {
  background: rgba(180, 180, 80, 0.25);
  outline: 1px solid rgba(180, 180, 80, 0.5);
}
.wash-wysiwyg .wash-find-current {
  background: rgba(180, 180, 80, 0.5);
}
`;
  document.head.appendChild(style);
}

// tiptap-markdown attaches itself to editor.storage at runtime but
// doesn't augment the @tiptap/core Storage type, so we narrow here.
const getMd = (e: Editor): string =>
  (e.storage as { markdown?: { getMarkdown(): string } }).markdown?.getMarkdown() ?? '';

export function createWysiwyg(opts: WysiwygOpts): WysiwygHandle {
  injectStyles();
  let dirty = false;
  // Initial content goes in via setContent below — not the
  // constructor's `content` prop — because tiptap-markdown's parser
  // hooks onto editor.commands.setContent and the constructor path
  // bypasses that override on TipTap v3 (initial content is parsed
  // as HTML/text, leaving the doc empty for markdown sources).
  const editor = new Editor({
    element: opts.parent,
    extensions: [
      StarterKit.configure({
        // We theme the prose ourselves; the keyboard list-tab binding
        // we wire below covers the common ones, so we leave the kit's
        // built-in keymap in place.
        codeBlock: { HTMLAttributes: { class: 'wash-codeblock' } },
        link: { openOnClick: false, autolink: true, linkOnPaste: true },
      }),
      Image.configure({ inline: false, allowBase64: true }),
      Table.configure({ resizable: false, HTMLAttributes: { class: 'wash-table' } }),
      TableRow,
      TableHeader,
      TableCell,
      TaskList,
      TaskItem.configure({ nested: true }),
      ListTab,
      SearchExt,
      Markdown.configure({
        html: true,
        tightLists: true,
        tightListClass: 'tight',
        bulletListMarker: '-',
        linkify: true,
        breaks: false,
        transformPastedText: true,
        transformCopiedText: false,
      }),
    ],
    editorProps: {
      attributes: {
        class: 'wash-wysiwyg',
        'data-testid': 'edit-wf-body',
        spellcheck: 'true',
      },
    },
    onUpdate: ({ editor }) => {
      if (!dirty) {
        dirty = true;
        opts.onDirtyChange?.(true);
      }
      opts.onChange?.(getMd(editor as Editor));
    },
  });
  // Seed with the initial markdown via setContent so tiptap-markdown's
  // parser is used. emitUpdate:false keeps the dirty marker off.
  if (opts.content) {
    editor.commands.setContent(opts.content, { emitUpdate: false });
  }

  // ---- search API ----
  // All methods guard on isDestroyed because the find bar holds the
  // handle across teardown races (wysiwyg→source toggle destroys the
  // editor before Solid unmounts the bar).
  const searchState = (): WysiwygSearchState => {
    if (editor.isDestroyed) return { count: 0, current: -1 };
    const s = searchKey.getState(editor.state);
    return { count: s?.matches.length ?? 0, current: s?.current ?? -1 };
  };
  // Scroll the current match into view inside the .wash-wysiwyg
  // scroll container. Decorations don't move the selection, so we
  // walk to the match's DOM node directly.
  const scrollToCurrent = () => {
    const s = searchKey.getState(editor.state);
    if (!s || s.current < 0) return;
    const m = s.matches[s.current];
    const dom = editor.view.domAtPos(m.from);
    const el = dom.node instanceof HTMLElement ? dom.node : dom.node.parentElement;
    el?.scrollIntoView({ block: 'nearest' });
  };
  const dispatchSearch = (query: string, current: number): WysiwygSearchState => {
    if (editor.isDestroyed) return { count: 0, current: -1 };
    editor.view.dispatch(editor.state.tr.setMeta(searchKey, { query, current }));
    scrollToCurrent();
    return searchState();
  };
  const search: WysiwygHandle['search'] = {
    // set() re-anchors to the first match at/after the caret so a
    // fresh Ctrl+F finds "the next occurrence from here", like CM.
    set: (query: string) => {
      if (editor.isDestroyed) return { count: 0, current: -1 };
      const caret = editor.state.selection.from;
      const matches = findMatches(editor.state.doc, query);
      let cur = matches.findIndex((m) => m.from >= caret);
      if (cur < 0) cur = matches.length ? 0 : -1;
      return dispatchSearch(query, cur);
    },
    next: () => {
      const s = searchState();
      if (!s.count || editor.isDestroyed) return s;
      const q = searchKey.getState(editor.state)!.query;
      return dispatchSearch(q, (s.current + 1) % s.count);
    },
    prev: () => {
      const s = searchState();
      if (!s.count || editor.isDestroyed) return s;
      const q = searchKey.getState(editor.state)!.query;
      return dispatchSearch(q, (s.current - 1 + s.count) % s.count);
    },
    // replace() swaps the current match; the plugin's docChanged
    // rescan keeps the same index, which now points at what was the
    // following match — so repeated Replace walks the document.
    replace: (repl: string) => {
      if (editor.isDestroyed) return { count: 0, current: -1 };
      const s = searchKey.getState(editor.state);
      if (!s || s.current < 0) return searchState();
      const m = s.matches[s.current];
      editor.view.dispatch(editor.state.tr.insertText(repl, m.from, m.to));
      scrollToCurrent();
      return searchState();
    },
    // replaceAll() applies right-to-left in one transaction so earlier
    // replacements don't shift later match positions (and it's a
    // single undo step).
    replaceAll: (repl: string) => {
      if (editor.isDestroyed) return { count: 0, current: -1 };
      const s = searchKey.getState(editor.state);
      if (!s || !s.matches.length) return searchState();
      const tr = editor.state.tr;
      for (let i = s.matches.length - 1; i >= 0; i--) {
        tr.insertText(repl, s.matches[i].from, s.matches[i].to);
      }
      editor.view.dispatch(tr);
      return searchState();
    },
    clear: () => {
      if (editor.isDestroyed) return;
      const s = searchKey.getState(editor.state);
      if (s?.query) dispatchSearch('', -1);
    },
    state: searchState,
  };

  return {
    editor,
    getMarkdown: () => getMd(editor),
    setMarkdown: (md: string) => {
      // emitUpdate=false: we're loading from disk, not editing.
      editor.commands.setContent(md, { emitUpdate: false });
      if (dirty) {
        dirty = false;
        opts.onDirtyChange?.(false);
      }
    },
    destroy: () => editor.destroy(),
    focus: () => { editor.commands.focus(); },
    markClean: () => {
      if (dirty) {
        dirty = false;
        opts.onDirtyChange?.(false);
      }
    },
    search,
  };
}

// isMarkdownPath returns true when the on-disk path ends in a
// known markdown extension. Used by the auto-WYSIWYG default on open.
export function isMarkdownPath(p: string): boolean {
  const lower = p.toLowerCase();
  return lower.endsWith('.md') || lower.endsWith('.markdown');
}
