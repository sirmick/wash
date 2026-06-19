/// <reference types="vite/client" />

declare global {
  interface Window {
    /** Boot-timeline logger, defined in index.html's first inline script.
     *  console.logs `performance.now()` (ms since page load) + a phase label,
     *  so cold-start timing is visible on the deployed demo where the dbg WS
     *  bus is dev-only (silent). No-op-safe: call as `window.__washBootMark?.()`. */
    __washBootMark?: (phase: string) => void;
  }
}

export {};
