// Tab labels for wash-term strips.
//
// A shell's default OSC title is "user@host: /some/long/path", and a tab
// has room for a couple of dozen characters. The first cut head-truncated
// at 12, which turned every tab into "mick@ai: ~/…" — the one part of the
// title that is identical across tabs was the only part that survived.
//
// So: strip the user@host prefix (the status bar already says who and
// where), and when a path still does not fit, keep its LAST segments —
// the directory you are in distinguishes tabs; the root it hangs off does
// not. A title with no path in it is cut from the end instead, since a
// program name leads ("vim main.tsx …"). Pure, unit-tested; the tooltip
// carries the untruncated title.

export const TAB_LABEL_MAX = 24;

// A leading "user@host:" (with or without a space after the colon —
// bash's default title has one, many PROMPT_COMMANDs do not).
const USER_AT_HOST = /^[^\s@:/]+@[^\s:/]+:\s*/;

export function shortShellName(p: string): string {
  const i = p.lastIndexOf('/');
  return i >= 0 ? p.slice(i + 1) : p;
}

// fullTabLabel is the untruncated label: the OSC title, else the shell's
// basename. What the tooltip shows.
export function fullTabLabel(title: string | undefined, shell: string): string {
  return (title ?? '').trim() || shortShellName(shell);
}

// tabLabelFor is what the strip shows for a title/shell pair.
export function tabLabelFor(title: string | undefined, shell: string, max = TAB_LABEL_MAX): string {
  let s = fullTabLabel(title, shell);
  const stripped = s.replace(USER_AT_HOST, '');
  if (stripped !== '') s = stripped;
  if (s.length <= max) return s;
  if (!s.includes('/')) return s.slice(0, Math.max(1, max - 1)) + '…';
  return tailOfPath(s, max);
}

// tailOfPath keeps as many trailing path segments as fit behind "…/". A
// single segment that is itself too long is cut from its front, so the
// end of the name — the part most likely to differ — stays.
function tailOfPath(path: string, max: number): string {
  const parts = path.replace(/\/+$/, '').split('/');
  let out = parts[parts.length - 1] ?? '';
  for (let i = parts.length - 2; i >= 0; i--) {
    const cand = parts[i] + '/' + out;
    if (cand.length + 2 > max) break;
    out = cand;
  }
  if (out.length + 2 > max) return '…' + out.slice(-(max - 1));
  return '…/' + out;
}
