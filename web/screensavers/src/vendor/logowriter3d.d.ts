// Types for the vendored logowriter3d.js (README "Embedding" in its repo).
export interface Named {
  readonly id: string;
  readonly name: string;
}
export const PROGRAMS: readonly Named[];
export const LOOKS: readonly Named[];
export const CAMS: readonly Named[];
export interface LogoWriter3DOptions {
  programs?: 'all' | string[];
  order?: 'shuffle' | 'sequence';
  look?: string;
  cam?: string;
  speed?: number;
  hud?: boolean;
  keys?: boolean;
  editor?: boolean;
  maxSegments?: number;
  fps?: number;
}
export interface LogoWriter3DHandle {
  dispose(): void;
  pause(): void;
  resume(): void;
  next(): void;
  set(o: Partial<LogoWriter3DOptions>): void;
}
export function mount(container: HTMLElement, opts?: LogoWriter3DOptions): LogoWriter3DHandle;
