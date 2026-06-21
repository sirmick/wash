// Pure path + format helpers for wash-fm. No DOM, no Solid — extracted
// verbatim from the App() monolith so they're unit-testable with
// node:test (see paths.test.ts) and reusable. Behaviour is identical to
// the in-closure originals.

export function joinPath(parent: string, name: string): string {
  if (parent.endsWith('/')) return parent + name;
  return parent + '/' + name;
}

export function parentPath(p: string): string {
  if (!p || p === '/') return '/';
  const i = p.lastIndexOf('/');
  if (i <= 0) return '/';
  return p.slice(0, i);
}

export function baseName(p: string): string {
  const i = p.lastIndexOf('/');
  return i < 0 ? p : p.slice(i + 1);
}

// extName returns the lowercase extension of a file NAME (no leading dot),
// or "" when there's none. Pass a basename, not a path: a dotfile with no
// extension ("/.config") and a dot only in a parent dir must both yield "".
// i must be > 0 so a leading-dot dotfile ("/.bashrc") is extensionless.
export function extName(name: string): string {
  const i = name.lastIndexOf('.');
  return i > 0 ? name.slice(i + 1).toLowerCase() : '';
}

// ancestorChain returns the path's ancestors from the root down to p
// inclusive: "/a/b/c" → ["/", "/a", "/a/b", "/a/b/c"]. "/" (or any
// path with no segments) → ["/"]. fm's expandPath walks this chain
// expanding+listing each dir; keeping the accumulation pure lets the
// segment math be unit-tested while the side-effecting walk stays in App.
export function ancestorChain(p: string): string[] {
  const parts = p.split('/').filter(Boolean);
  const chain = ['/'];
  let acc = '/';
  for (const part of parts) {
    acc = acc === '/' ? '/' + part : acc + '/' + part;
    chain.push(acc);
  }
  return chain;
}

export function humanSize(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// formatDate renders a unix seconds timestamp in the compact
// ls-style: "Dec 15 14:32" for this year, "Dec 15  2024" for
// older entries. Returns "" for 0/missing values so the row
// stays clean.
export function formatDate(unix: number): string {
  if (!unix) return '';
  const d = new Date(unix * 1000);
  const now = new Date();
  const month = MONTHS[d.getMonth()];
  const day = String(d.getDate()).padStart(2, ' ');
  if (d.getFullYear() === now.getFullYear()) {
    const hh = String(d.getHours()).padStart(2, '0');
    const mm = String(d.getMinutes()).padStart(2, '0');
    return `${month} ${day} ${hh}:${mm}`;
  }
  return `${month} ${day}  ${d.getFullYear()}`;
}

export function octalPerm(mode: number): string {
  return '0' + (mode & 0o777).toString(8);
}
