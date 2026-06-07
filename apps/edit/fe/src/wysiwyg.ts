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

export interface WysiwygHandle {
  editor: Editor;
  getMarkdown(): string;
  setMarkdown(md: string): void;
  destroy(): void;
  focus(): void;
  markClean(): void;
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
  font: ${tokens.fontSizeBase} ${tokens.fontSans};
  line-height: 1.55;
  caret-color: ${tokens.fg};
}
.wash-wysiwyg:focus { outline: none; }
.wash-wysiwyg > * { margin: 0 0 0.6em 0; }
.wash-wysiwyg > *:last-child { margin-bottom: 0; }
.wash-wysiwyg h1 { font-size: 1.9em; font-weight: 600; margin: 0.6em 0 0.4em; color: #e9d5ff; }
.wash-wysiwyg h2 { font-size: 1.5em; font-weight: 600; margin: 0.8em 0 0.35em; color: #ddd6fe; }
.wash-wysiwyg h3 { font-size: 1.25em; font-weight: 600; margin: 0.7em 0 0.3em; color: #ddd6fe; }
.wash-wysiwyg h4, .wash-wysiwyg h5, .wash-wysiwyg h6 { font-weight: 600; margin: 0.6em 0 0.3em; color: ${tokens.fg}; }
.wash-wysiwyg p { margin: 0 0 0.6em; }
.wash-wysiwyg strong { font-weight: 700; color: #fafafa; }
.wash-wysiwyg em { font-style: italic; }
.wash-wysiwyg s { color: ${tokens.fgDim}; }
.wash-wysiwyg a { color: #60a5fa; text-decoration: underline; cursor: pointer; }
.wash-wysiwyg code {
  font-family: ${tokens.fontMono};
  font-size: 0.92em;
  background: #1a1a2a;
  border: 1px solid ${tokens.borderMenu};
  border-radius: ${tokens.radiusSm}px;
  padding: 0 4px;
  color: #fde68a;
}
.wash-wysiwyg pre {
  font-family: ${tokens.fontMono};
  font-size: 0.92em;
  background: ${tokens.bgInset};
  border: 1px solid ${tokens.borderMenu};
  border-radius: ${tokens.radiusMd}px;
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
  border-radius: ${tokens.radiusSm}px;
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
.wash-wysiwyg img { max-width: 100%; height: auto; border-radius: ${tokens.radiusSm}px; }
.wash-wysiwyg .ProseMirror-selectednode {
  outline: 2px solid ${tokens.borderFocus};
  outline-offset: 2px;
}
.wash-wysiwyg ::selection { background: ${tokens.bgRowSelected}; }
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
  };
}

// isMarkdownPath returns true when the on-disk path ends in a
// known markdown extension. Used by the auto-WYSIWYG default on open.
export function isMarkdownPath(p: string): boolean {
  const lower = p.toLowerCase();
  return lower.endsWith('.md') || lower.endsWith('.markdown');
}
