// Desktop notifications a program in the terminal raises. There is no one
// standard; three escape sequences are in common use and each terminal
// speaks a different subset:
//
//   OSC 9   ; <body>                     iTerm2. ConEmu reuses OSC 9 for
//                                        numbered subcommands (9;4 is the
//                                        progress bar), which are NOT
//                                        notifications and are dropped here.
//   OSC 777 ; notify ; <title> ; <body>  urxvt, foot, Ghostty.
//   OSC 99  ; <metadata> ; <payload>     kitty: chunked by id, title and
//                                        body sent separately, optionally
//                                        base64.
//
// Claude Code's preferredNotifChannel picks one of iterm2 / kitty / ghostty,
// so all three are understood. Nothing is ever written back: kitty's `p=?`
// capability query goes unanswered.

export interface TermNotification {
  title: string;
  body: string;
}

// Bounds on what a stream can put in a toast, and on kitty's chunk buffer —
// a runaway or hostile program must not grow either without limit.
const MAX_TITLE = 256;
const MAX_BODY = 1024;
const MAX_PENDING = 8;

function make(title: string, body: string): TermNotification | null {
  title = title.trim().slice(0, MAX_TITLE);
  body = body.trim().slice(0, MAX_BODY);
  return title || body ? { title, body } : null;
}

// parseOsc9 handles OSC 9's payload. A leading number followed by `;` (or
// alone) is a ConEmu subcommand, not a message.
export function parseOsc9(data: string): TermNotification | null {
  if (/^\d+(;|$)/.test(data)) return null;
  return make('', data);
}

// parseOsc777 handles OSC 777's payload; only its `notify` verb is a
// notification. A `;` in the body is kept.
export function parseOsc777(data: string): TermNotification | null {
  const parts = data.split(';');
  if (parts[0] !== 'notify' || parts.length < 2) return null;
  return make(parts[1], parts.slice(2).join(';'));
}

function decodeBase64(s: string): string | null {
  try {
    const bin = atob(s.replace(/\s+/g, ''));
    return new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
  } catch {
    return null;
  }
}

// Osc99 assembles kitty notifications. One per terminal: chunks with d=0
// accumulate under their id until a chunk with d=1 (the default) completes
// it. Only the title and body payload types are used; icons, buttons,
// close and liveness requests are ignored.
export class Osc99 {
  private pending = new Map<string, { title: string; body: string }>();

  feed(data: string): TermNotification | null {
    const semi = data.indexOf(';');
    if (semi < 0) return null;
    const meta = new Map<string, string>();
    for (const kv of data.slice(0, semi).split(':')) {
      const eq = kv.indexOf('=');
      if (eq > 0) meta.set(kv.slice(0, eq), kv.slice(eq + 1));
    }
    const kind = meta.get('p') ?? 'title';
    if (kind !== 'title' && kind !== 'body') return null;
    let payload = data.slice(semi + 1);
    if (meta.get('e') === '1') {
      const decoded = decodeBase64(payload);
      if (decoded === null) return null;
      payload = decoded;
    }

    const id = meta.get('i') ?? '';
    let n = this.pending.get(id);
    if (!n) {
      if (this.pending.size >= MAX_PENDING) {
        const oldest = this.pending.keys().next().value;
        if (oldest !== undefined) this.pending.delete(oldest);
      }
      n = { title: '', body: '' };
      this.pending.set(id, n);
    }
    if (kind === 'title') n.title = (n.title + payload).slice(0, MAX_TITLE);
    else n.body = (n.body + payload).slice(0, MAX_BODY);

    if (meta.get('d') === '0') return null;
    this.pending.delete(id);
    // kitty shows a body-only notification with the body as its title.
    return n.title ? make(n.title, n.body) : make(n.body, '');
  }
}
