// Shell-side diagnostic log. Goes to console.info (mirrored to the
// wash router's t:log channel by main.tsx) AND, when available, to
// window.__washDiagLog — a sink installed by the outer demo page
// (wash-vm/web/src/tinyemu-bridge.ts) that forwards via the demo
// server's WS bus. Without the sink the line is still visible in the
// browser console; with it, the line lands in
// /tmp/wash-demo-server.log so the bug can be diagnosed without
// devtools open.

type DiagLog = (source: string, msg: string) => void;

export function wlog(msg: string): void {
  console.info(`[wash-diag] ${msg}`);
  const sink = (window as unknown as { __washDiagLog?: DiagLog }).__washDiagLog;
  try {
    sink?.('wash-diag', msg);
  } catch {
    /* ignore */
  }
}
