// OSC 52 — a program in the terminal setting the clipboard.
//
// Full-screen TUIs that turn on mouse tracking (Claude Code, tmux, vim with
// mouse=a) own the mouse: a drag selects in THEIR model, not xterm's, and
// they copy by emitting `ESC ] 52 ; <targets> ; <base64> ST`. Without a
// handler that text reaches no clipboard at all.
//
// Only writes are honoured. A `?` payload asks the terminal to report the
// clipboard back into the stream — that would let anything printed to the
// terminal (a `cat`ed file, a remote host over ssh) read the clipboard, so
// it is ignored, as is an empty payload (some terminals treat that as
// "clear"; wash's clipboard has no such verb). The targets (c, p, s, 0-7)
// are not distinguished: wash has one clipboard.

// parseOsc52 returns the text an OSC 52 payload (everything after `52;`)
// asks to copy, or null when it is a query, a clear, or malformed.
export function parseOsc52(data: string): string | null {
  const semi = data.indexOf(';');
  if (semi < 0) return null;
  const b64 = data.slice(semi + 1).replace(/\s+/g, '');
  if (b64 === '' || b64 === '?') return null;
  let bin: string;
  try {
    bin = atob(b64);
  } catch {
    return null;
  }
  const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0));
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(bytes);
  } catch {
    return null;
  }
}
