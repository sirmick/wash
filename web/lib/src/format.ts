// Shared display formatters used across apps. Per the house rule, only
// multi-consumer helpers live here.
//
// Byte/rate formatting is standardized on SI-style units (KB/MB/GB), one
// canonical precision ladder, so sizes read the same everywhere on the
// desktop. Apps previously hand-rolled diverging variants (compact `K/M/G`,
// IEC `KiB/MiB/GiB`); those are now this one function.

// fmtBytes renders a byte count as `B`/`KB`/`MB`/`GB`/`TB`/`PB`. Bytes are
// integer; KB/MB get one decimal; GB and up get two. A falsy or negative
// count renders `0 B`.
export function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  let v = n;
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024;
    i++;
  }
  const dp = i === 0 ? 0 : i <= 2 ? 1 : 2;
  return `${v.toFixed(dp)} ${u[i]}`;
}

// fmtRate renders a bytes-per-second value as `<size>/s` (e.g. `1.5 MB/s`).
export function fmtRate(bps: number): string {
  if (!bps || bps < 1) return '0 B/s';
  return `${fmtBytes(bps)}/s`;
}

// fmtUptime renders a duration in seconds as the largest two units
// (`45s`, `3m 12s`, `5h 4m`, `2d 7h`).
export function fmtUptime(sec: number): string {
  if (sec < 60) return `${sec}s`;
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  if (m < 60) return `${m}m ${s}s`;
  const h = Math.floor(m / 60);
  const mm = m % 60;
  if (h < 24) return `${h}h ${mm}m`;
  const d = Math.floor(h / 24);
  const hh = h % 24;
  return `${d}d ${hh}h`;
}

// fmtClockTime renders a microsecond timestamp as `HH:MM:SS.mmm` in local
// time (journal + syslogs share this exact format). A falsy timestamp yields
// a blank string of the same 12-column width so rows stay aligned.
export function fmtClockTime(tsMicros: number): string {
  if (!tsMicros) return '            ';
  const d = new Date(tsMicros / 1000);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  const ss = String(d.getSeconds()).padStart(2, '0');
  const ms = String(d.getMilliseconds()).padStart(3, '0');
  return `${hh}:${mm}:${ss}.${ms}`;
}
