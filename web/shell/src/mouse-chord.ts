// ChordFilter turns left+right pressed together into a middle-click, for
// wash-display's guests (docs/CLIPBOARD.md): the shell's own chord handling
// (clipboard-gestures.ts) pastes the wash selection, but a guest app gets
// its input raw, so there the chord has to become the button it stands for.
//
// It is X11's Emulate3Buttons: a left or right press is held back for up to
// CHORD_MS in case the other joins it. A release, a second button, or
// motion past a few pixels lets the held press through as it was.

export type ButtonName = 'left' | 'middle' | 'right';
export type ButtonEdge = { btn: ButtonName; state: 'down' | 'up' };

export const CHORD_MS = 60;

export class ChordFilter {
  private pending: ButtonName | null = null;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private chord = false;
  private readonly held = new Set<ButtonName>();

  private readonly emit: (e: ButtonEdge) => void;
  private readonly ms: number;
  private readonly setT: (fn: () => void, ms: number) => ReturnType<typeof setTimeout>;
  private readonly clearT: (t: ReturnType<typeof setTimeout>) => void;

  constructor(
    emit: (e: ButtonEdge) => void,
    ms = CHORD_MS,
    setT: (fn: () => void, ms: number) => ReturnType<typeof setTimeout> = setTimeout,
    clearT: (t: ReturnType<typeof setTimeout>) => void = clearTimeout,
  ) {
    this.emit = emit;
    this.ms = ms;
    this.setT = setT;
    this.clearT = clearT;
  }

  down(btn: ButtonName): void {
    this.held.add(btn);
    if (this.chord) return;
    if (this.pending && this.pending !== btn && btn !== 'middle') {
      // The other of left/right, inside the window: one middle-click.
      this.cancel();
      this.chord = true;
      this.emit({ btn: 'middle', state: 'down' });
      return;
    }
    this.flush();
    if (btn !== 'middle' && this.held.size === 1) {
      this.pending = btn;
      this.timer = this.setT(() => this.flush(), this.ms);
      return;
    }
    this.emit({ btn, state: 'down' });
  }

  up(btn: ButtonName): void {
    this.held.delete(btn);
    if (this.chord) {
      // The middle-click ends when the last of the two lets go.
      if (!this.held.has('left') && !this.held.has('right')) {
        this.chord = false;
        this.emit({ btn: 'middle', state: 'up' });
      }
      return;
    }
    if (this.pending === btn) this.flush();
    this.emit({ btn, state: 'up' });
  }

  /** forget: btn was released into something else (a window move the
   *  shell took over) — drop it without telling the guest. */
  forget(btn: ButtonName): void {
    this.held.delete(btn);
    if (this.pending === btn) this.cancel();
    if (this.chord && !this.held.has('left') && !this.held.has('right')) {
      this.chord = false;
      this.emit({ btn: 'middle', state: 'up' });
    }
  }

  /** moved: the pointer travelled far enough that a held press is a drag. */
  moved(): void {
    this.flush();
  }

  private flush(): void {
    if (!this.pending) return;
    const btn = this.pending;
    this.cancel();
    this.emit({ btn, state: 'down' });
  }

  private cancel(): void {
    if (this.timer) this.clearT(this.timer);
    this.timer = null;
    this.pending = null;
  }
}
