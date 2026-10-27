# Clipboard and selection

wash has two slots, each with one meaning, the way X11 does:

- **The clipboard** holds what you deliberately copied: Ctrl+C, Ctrl+X, a
  menu's Copy or Cut, a Copy button. Ctrl+V and a menu's Paste paste it.
- **The selection** holds what you last highlighted, anywhere in wash.
  Middle-click pastes it, and so do Alt+right-click and left+right pressed
  together. Highlighting never touches the clipboard.

Both live in the router, so one session shares them across every tab and
every box attached to it. This replaces a model where highlighting copied to
the clipboard, so a highlight wiped what you had copied, and paste guessed
between two clipboards.

## Bindings

The same everywhere: terminals, text fields, the editor, apps. Cmd stands in
for Ctrl on macOS. One table in `web/lib/src/clipboard-bindings.ts` drives the
menus' shortcut hints and the sidebar pane's list, so the two can't drift.

| Gesture | Does |
|---|---|
| Ctrl+C / Ctrl+X / Ctrl+V | copy, cut, paste the clipboard |
| Right-click | wash menu: Cut, Copy, Paste, Paste selection, Select all, plus the app's items |
| Middle-click | paste the selection |
| Alt+right-click (Option on macOS) | paste the selection |
| Left+right pressed together | paste the selection |
| Shift+right-click | the browser's own menu (spelling, Inspect), in every browser |

In a terminal:

| Keys | Does |
|---|---|
| Ctrl+C | copies when there is a selection (and clears it), else interrupts |
| Ctrl+Shift+C | copy |
| Ctrl+V | paste. An image pastes as the path of a file holding it |
| Ctrl+Shift+V | sends a literal `^V` (vim visual block, bash quoted-insert) |

Why each one is there:

- **Ctrl+V is the browser's native paste**, never a programmatic read. A real
  key press is the one thing every browser accepts as consent, so it works on
  plain HTTP and needs no permission prompt. xterm.js turned it into `^V` and
  cancelled the key, so it never pasted (fixed; Ctrl+Shift+V keeps `^V`).
- **Paste selection has no key.** The selection is a mouse idea. Alt+right-
  click covers a Mac trackpad, middle-click and the chord cover mice, and the
  menu item covers everyone else.
- **Shift+right-click is the browser's** because Firefox forces it to be
  anyway; making it so everywhere means no browser disagrees. Alt+right-click
  on a Linux desktop that grabs it for window moves (Xfce, older KDE) loses
  one of three paste-selection routes, not the only route to a menu.
- **The paste-selection gestures act only where text can go**: a terminal,
  a text field, a rich editor. Elsewhere the buttons keep their own meaning
  (a middle-click closes an editor tab, an app's right-click menu opens at
  once). On those surfaces a plain right-click waits 60ms before its menu
  opens, in case it is the start of a chord.
- **wash-display** passes every gesture to the guest app unchanged, except
  the left+right chord, which reaches the guest as a middle-click
  (`web/shell/src/mouse-chord.ts`).

## The clipboard and this computer's clipboard

Each box has its own system clipboard, and wash's is shared by all of them.
The rule that keeps them in step is **last change wins**, decided per box:

- Each box remembers the system clipboard text it last saw (`systemSeen`, in
  `localStorage`, shared by the tabs of one browser): what wash last wrote
  there, or what a paste last read from it.
- A paste carries the system clipboard, `S`. If `S` differs from
  `systemSeen`, something outside wash copied it since: adopt it (the wash
  clipboard becomes `S`) and paste it. If not, the wash clipboard is at
  least as new: paste the wash clipboard instead.
- A copy in wash writes both, and updates `systemSeen` when the system write
  succeeded.
- A clipboard change from elsewhere (another box, an app's BE, a guest app)
  is written to this box's system clipboard while the page has focus, where
  the browser allows it (Chrome, HTTPS), and on the next focus otherwise.

So copying on box A and pasting on box B pastes A's copy, while copying in a
desktop app on box B and pasting into wash there pastes that.

`resolvePaste` in `web/lib/src/clipboard.ts` is the rule; the shell's paste
listener applies it to text fields and the editor, the terminal applies it to
its own pastes, and menu Paste applies it to a programmatic read.

## Images

A paste carrying an image and no text, into a terminal, saves the image to
`$XDG_RUNTIME_DIR/wash/paste/` on the router's host and pastes its path.
Claude Code takes a path as an image attachment, and any CLI gets a usable
file. The last 20 files are kept. The image also becomes the wash clipboard,
so the pane shows it.

## When the browser won't

Keyboard and mouse work over plain HTTP: native copy and paste events need no
API. What can't work there is anything not driven by a key press or click:
a menu's Paste reading the system clipboard, or a guest app's copy reaching
it. The break glass is the sidebar's Clipboard pane:

- **Copy to this computer** writes the wash clipboard out; the click supplies
  the gesture the browser wants.
- **Click, then Ctrl+V** brings this computer's clipboard in.

Menu Paste with no readable system clipboard pastes the wash clipboard.

## The Clipboard pane

Sidebar, quiet: it never opens itself.

1. **Clipboard**: preview (text, image thumbnail, or "3 files · cut"), where
   it came from and when, Copy to this computer, the Ctrl+V import box,
   Clear.
2. **Selection**: preview, where it came from, Copy to clipboard, Clear.
3. **Status**: one line, only when something is wrong (HTTP, a blocked
   permission).
4. **Shortcuts**: the bindings table, folded.

No history: a clipboard history would keep copied passwords.

## Wire

`clipboard.set` / `get` / `data` / `changed` on the shell channel carry
`slot`: empty for the clipboard, `"selection"` for the selection. They also
carry `source` (where it came from: the focused window's title, or the app's
name for a BE's set), `at` (stamped by the router), and `data` for non-text
content (`changed` announces its `size` instead of pushing the bytes). App
BEs reach only the clipboard slot, over the event channel as before, and are
not told about selection changes. `clipboard.save_image` asks the router to
write image bytes under `$XDG_RUNTIME_DIR/wash/paste/`; `clipboard.saved`
replies with the path.

## Not yet

- wash-display does not bridge the guest PRIMARY selection to the wash
  selection; guests share PRIMARY among themselves, and the wash clipboard
  feeds both their CLIPBOARD and PRIMARY as before.
- Image paste into a remote host's terminal (docs/REMOTE.md) is not wired:
  the file would land on the wrong machine.
