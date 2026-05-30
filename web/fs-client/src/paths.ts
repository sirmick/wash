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
