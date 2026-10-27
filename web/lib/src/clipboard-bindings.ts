// The clipboard bindings, written once (docs/CLIPBOARD.md). The menus'
// shortcut hints and the sidebar Clipboard pane's list both read this, so
// what wash says it does and what the pane documents can't drift apart.
// Cmd replaces Ctrl on macOS, and Option replaces Alt.

const isMac =
  typeof navigator !== 'undefined' &&
  /Mac|iPhone|iPad/.test((navigator as { userAgentData?: { platform?: string } }).userAgentData?.platform ?? navigator.platform ?? '');

const mod = isMac ? 'Cmd' : 'Ctrl';
const alt = isMac ? 'Option' : 'Alt';

/** The menu items that carry a shortcut hint. */
export type ClipboardAction = 'cut' | 'copy' | 'paste' | 'paste-selection' | 'select-all';

const HINTS: Record<ClipboardAction, string> = {
  cut: `${mod}+X`,
  copy: `${mod}+C`,
  paste: `${mod}+V`,
  'paste-selection': 'Middle-click',
  'select-all': `${mod}+A`,
};

/** shortcutHint is the key shown beside a menu item. */
export function shortcutHint(action: ClipboardAction): string {
  return HINTS[action];
}

export interface ClipboardBinding {
  keys: string;
  does: string;
}

/** Everywhere: terminals, text fields, the editor, apps. */
export const CLIPBOARD_BINDINGS: ClipboardBinding[] = [
  { keys: `${mod}+C / ${mod}+X / ${mod}+V`, does: 'copy, cut, paste' },
  { keys: `Middle-click, ${alt}+right-click, left+right`, does: 'paste the selection (what you last highlighted)' },
  { keys: 'Right-click', does: 'wash menu' },
  { keys: 'Shift+right-click', does: 'browser menu' },
];

/** In a terminal, on top of the above. */
export const TERMINAL_BINDINGS: ClipboardBinding[] = isMac
  ? [
      { keys: 'Cmd+C', does: 'copy; Ctrl+C interrupts' },
      { keys: 'Cmd+V', does: 'paste; an image pastes as a file path' },
    ]
  : [
      { keys: 'Ctrl+C', does: 'copy if something is selected, else interrupt' },
      { keys: 'Ctrl+Shift+C', does: 'copy' },
      { keys: 'Ctrl+V', does: 'paste; an image pastes as a file path' },
      { keys: 'Ctrl+Shift+V', does: 'send a literal ^V (vim block select)' },
    ];
