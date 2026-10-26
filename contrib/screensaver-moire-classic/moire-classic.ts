// moire-classic — classic Mac-style XOR line screensaver for wash.
//
// Zero-dependency Canvas2D. Shapes are rasterized by hand (Bresenham /
// midpoint) into a low-res Uint32 framebuffer with XOR, then blitted with
// nearest-neighbour scaling. The moiré is the real thing: it comes from the
// aliasing of hard 1-px lines packed close together, exactly as on a 1-bit Mac.
//
// Trail modes keep a ring of the last N shapes. Each tick XOR-draws the new
// shape and XOR-draws the oldest again, which erases it — no clears, no
// redraws, and where shapes overlap the pixels cancel the way QuickDraw's
// patXor did.
//
//   import { defineMoireClassic } from './moire-classic';
//   defineMoireClassic();       // registers <wash-screensaver-moire-classic>
//   document.body.append(document.createElement('wash-screensaver-moire-classic'));

export type ClassicShape = 'lines' | 'rects' | 'ovals' | 'polygon' | 'fan';
export type ClassicColor = 'mono' | 'rainbow' | 'green' | 'amber';
export const CLASSIC_SHAPES: ClassicShape[] = ['lines', 'rects', 'ovals', 'polygon', 'fan'];
export const CLASSIC_COLORS: ClassicColor[] = ['mono', 'rainbow', 'green', 'amber'];

export interface MoireClassicOptions {
  /** 'auto' cycles shapes (default). */
  shape?: ClassicShape | 'auto';
  color?: ClassicColor;
  /** 0..1 (default 0.5). */
  brightness?: number;
  /** Trail length 0..1 → 8..160 shapes (default 0.5). */
  trail?: number;
  /** 0..1 → ticks per second (default 0.5). */
  speed?: number;
  /** Screen pixels per framebuffer pixel, 1..4 (default 2 — the chunky look). */
  pixel?: number;
  /** Seconds per shape when cycling (default 30). */
  hold?: number;
}

type Pt = { x: number; y: number; vx: number; vy: number };

// ImageData's Uint32 view is little-endian ABGR.
const rgb = (r: number, g: number, b: number) => ((b & 255) << 16) | ((g & 255) << 8) | (r & 255);
function hue(h: number): number {
  const f = (n: number) => {
    const k = (n + h * 6) % 6;
    return 255 * (1 - Math.max(0, Math.min(k, 4 - k, 1)));
  };
  return rgb(f(5), f(3), f(1));
}

export class MoireClassic {
  private ctx: CanvasRenderingContext2D;
  private o: Required<MoireClassicOptions>;
  private W = 0; private H = 0;
  private buf = new Uint32Array(0);
  private img: ImageData | null = null;
  private out = new Uint32Array(0);
  private raf = 0; private running = false;
  private last = 0; private acc = 0; private since = 0;
  private mode: ClassicShape = 'lines';
  private pts: Pt[] = [];
  private ring: { kind: ClassicShape; c: number[]; col: number }[] = [];
  private hueT = Math.random();
  private ro: ResizeObserver;
  private onVis = () => { if (document.hidden) this.pause(); else if (this.running) this.resume(); };

  constructor(private canvas: HTMLCanvasElement, opts: MoireClassicOptions = {}) {
    const given = Object.fromEntries(Object.entries(opts).filter(([, v]) => v !== undefined && !(typeof v === 'number' && Number.isNaN(v))));
    this.o = { shape: 'auto', color: 'mono', brightness: 0.5, trail: 0.5, speed: 0.5, pixel: 2, hold: 30, ...given } as Required<MoireClassicOptions>;
    const ctx = canvas.getContext('2d', { alpha: false });
    if (!ctx) throw new Error('moire-classic: no 2D context');
    this.ctx = ctx;
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
  }

  start() {
    if (this.running) return this;
    this.running = true;
    document.addEventListener('visibilitychange', this.onVis);
    this.resize(true);
    this.resume();
    return this;
  }
  stop() { this.running = false; this.pause(); document.removeEventListener('visibilitychange', this.onVis); }
  destroy() { this.stop(); this.ro.disconnect(); }

  get settings() {
    const o = this.o;
    return { shape: o.shape, color: o.color, brightness: o.brightness, trail: o.trail, speed: o.speed, pixel: o.pixel };
  }
  set(p: Partial<Pick<Required<MoireClassicOptions>, 'brightness' | 'trail' | 'speed' | 'color' | 'pixel' | 'hold'>>) {
    const oldPixel = this.o.pixel, oldColor = this.o.color;
    Object.assign(this.o, Object.fromEntries(Object.entries(p).filter(([, v]) => v !== undefined)));
    if (this.o.pixel !== oldPixel) this.resize(true);
    else if (this.o.color !== oldColor) this.reset(this.mode);   // recolour cleanly
    this.applyBrightness();
  }
  setShape(s: ClassicShape | 'auto') { this.o.shape = s; this.reset(this.pick()); }
  randomize() { this.reset(this.pick()); }

  // ---- setup
  private pick(): ClassicShape {
    if (this.o.shape !== 'auto') return this.o.shape;
    let m = this.mode;
    while (m === this.mode) m = CLASSIC_SHAPES[Math.floor(Math.random() * CLASSIC_SHAPES.length)];
    return m;
  }
  private resize(force = false) {
    const px = Math.max(1, Math.min(4, Math.round(this.o.pixel)));
    const dpr = window.devicePixelRatio || 1;
    const W = Math.max(8, Math.floor(this.canvas.clientWidth * dpr / px));
    const H = Math.max(8, Math.floor(this.canvas.clientHeight * dpr / px));
    if (!force && W === this.W && H === this.H) return;
    this.W = W; this.H = H;
    this.canvas.width = W; this.canvas.height = H;
    this.buf = new Uint32Array(W * H);
    this.img = this.ctx.createImageData(W, H);
    this.out = new Uint32Array(this.img.data.buffer);
    this.applyBrightness();
    this.reset(this.ring.length || force ? this.pick() : this.mode);
  }
  private applyBrightness() {
    this.canvas.style.filter = `brightness(${(0.35 + 1.3 * this.o.brightness).toFixed(3)})`;
  }
  private reset(mode: ClassicShape) {
    this.mode = mode;
    this.buf.fill(0);
    this.ring = [];
    this.since = 0;
    const n = mode === 'polygon' ? 4 : mode === 'fan' ? 2 : 2;
    const R = Math.random;
    const sp = () => (0.6 + R() * 1.6) * (R() < 0.5 ? -1 : 1);
    this.pts = Array.from({ length: n }, () => ({ x: R() * this.W, y: R() * this.H, vx: sp(), vy: sp() }));
  }

  // ---- loop
  private pause() { cancelAnimationFrame(this.raf); this.raf = 0; }
  private resume() {
    if (this.raf) return;
    this.last = performance.now();
    const loop = (now: number) => {
      this.raf = requestAnimationFrame(loop);
      const dt = Math.min((now - this.last) / 1000, 0.1);
      this.last = now;
      this.since += dt;
      if (this.o.shape === 'auto' && this.since > this.o.hold) this.reset(this.pick());
      if (this.mode === 'fan') this.fanFrame(dt);
      else {
        const tps = 12 + 220 * this.o.speed * this.o.speed;
        this.acc += dt * tps;
        let n = Math.min(Math.floor(this.acc), 24);
        this.acc -= Math.floor(this.acc);
        while (n-- > 0) this.tick();
      }
      this.blit();
    };
    this.raf = requestAnimationFrame(loop);
  }

  private color(): number {
    switch (this.o.color) {
      case 'rainbow': this.hueT = (this.hueT + 0.0035) % 1; return hue(this.hueT);
      case 'green': return rgb(70, 255, 110);
      case 'amber': return rgb(255, 176, 40);
      default: return rgb(255, 255, 255);
    }
  }
  private move(scale = 1) {
    const W = this.W - 1, H = this.H - 1;
    for (const p of this.pts) {
      p.x += p.vx * scale; p.y += p.vy * scale;
      if (p.x < 0) { p.x = -p.x; p.vx = Math.abs(p.vx); }
      if (p.x > W) { p.x = 2 * W - p.x; p.vx = -Math.abs(p.vx); }
      if (p.y < 0) { p.y = -p.y; p.vy = Math.abs(p.vy); }
      if (p.y > H) { p.y = 2 * H - p.y; p.vy = -Math.abs(p.vy); }
      // occasional nudge so the figure doesn't settle into a fixed orbit
      if (Math.random() < 0.004) { p.vx += (Math.random() - 0.5) * 0.6; p.vy += (Math.random() - 0.5) * 0.6; }
      const s = Math.hypot(p.vx, p.vy);
      if (s > 3) { p.vx *= 3 / s; p.vy *= 3 / s; }
      if (s < 0.5) { p.vx *= 0.5 / s; p.vy *= 0.5 / s; }
    }
  }
  private tick() {
    this.move();
    const c = this.pts.flatMap(p => [Math.round(p.x), Math.round(p.y)]);
    const sh = { kind: this.mode, c, col: this.color() };
    this.draw(sh);
    this.ring.push(sh);
    const max = Math.round(8 + 152 * this.o.trail);
    while (this.ring.length > max) this.draw(this.ring.shift()!);   // XOR again = erase
  }
  private draw(s: { kind: ClassicShape; c: number[]; col: number }) {
    const [x0, y0, x1, y1] = s.c;
    if (s.kind === 'lines') this.line(x0, y0, x1, y1, s.col);
    else if (s.kind === 'rects') this.rect(x0, y0, x1, y1, s.col);
    else if (s.kind === 'ovals') this.oval(x0, y0, x1, y1, s.col);
    else if (s.kind === 'polygon') {
      // edges share endpoints; drop each edge's last pixel so vertices aren't XORed twice
      const c = s.c, n = c.length / 2;
      for (let i = 0; i < n; i++) {
        const j = (i + 1) % n;
        this.line(c[2 * i], c[2 * i + 1], c[2 * j], c[2 * j + 1], s.col, true);
      }
    }
  }
  // fan: lines from two moving hubs to every k-th border pixel, redrawn each frame
  private fanFrame(dt: number) {
    this.move(dt * (20 + 90 * this.o.speed));
    this.buf.fill(0);
    const W = this.W - 1, H = this.H - 1;
    const k = Math.max(2, Math.round(14 - 10 * this.o.trail));
    const col = this.color();
    for (const p of this.pts) {
      const cx = Math.round(p.x), cy = Math.round(p.y);
      for (let x = 0; x < W; x += k) { this.line(cx, cy, x, 0, col, true); this.line(cx, cy, W - x, H, col, true); }
      for (let y = 0; y < H; y += k) { this.line(cx, cy, W, y, col, true); this.line(cx, cy, 0, H - y, col, true); }
    }
  }

  // ---- XOR rasterizers
  private plot(x: number, y: number, c: number) {
    if (x < 0 || y < 0 || x >= this.W || y >= this.H) return;
    this.buf[y * this.W + x] ^= c;
  }
  private line(x0: number, y0: number, x1: number, y1: number, c: number, skipLast = false) {
    const dx = Math.abs(x1 - x0), dy = -Math.abs(y1 - y0);
    const sx = x0 < x1 ? 1 : -1, sy = y0 < y1 ? 1 : -1;
    let err = dx + dy;
    for (;;) {
      const end = x0 === x1 && y0 === y1;
      if (end && skipLast) break;
      this.plot(x0, y0, c);
      if (end) break;
      const e2 = 2 * err;
      if (e2 >= dy) { err += dy; x0 += sx; }
      if (e2 <= dx) { err += dx; y0 += sy; }
    }
  }
  private rect(ax: number, ay: number, bx: number, by: number, c: number) {
    const x0 = Math.min(ax, bx), x1 = Math.max(ax, bx), y0 = Math.min(ay, by), y1 = Math.max(ay, by);
    for (let x = x0; x <= x1; x++) { this.plot(x, y0, c); if (y1 !== y0) this.plot(x, y1, c); }
    for (let y = y0 + 1; y < y1; y++) { this.plot(x0, y, c); if (x1 !== x0) this.plot(x1, y, c); }
  }
  private oval(ax: number, ay: number, bx: number, by: number, c: number) {
    // midpoint ellipse inscribed in the bounding box, each pixel plotted once
    const x0 = Math.min(ax, bx), x1 = Math.max(ax, bx), y0 = Math.min(ay, by), y1 = Math.max(ay, by);
    const a = (x1 - x0) / 2, b = (y1 - y0) / 2, cx = (x0 + x1) / 2, cy = (y0 + y1) / 2;
    if (a < 1 || b < 1) { this.rect(x0, y0, x1, y1, c); return; }
    const seen = new Set<number>();
    const p4 = (x: number, y: number) => {
      for (const [sx, sy] of [[1, 1], [-1, 1], [1, -1], [-1, -1]]) {
        const px = Math.round(cx + sx * x), py = Math.round(cy + sy * y);
        const k = py * this.W + px;
        if (!seen.has(k)) { seen.add(k); this.plot(px, py, c); }
      }
    };
    let x = 0, y = b;
    const a2 = a * a, b2 = b * b;
    let d1 = b2 - a2 * b + 0.25 * a2, dx = 2 * b2 * x, dy = 2 * a2 * y;
    while (dx < dy) {
      p4(x, y);
      if (d1 < 0) { x++; dx += 2 * b2; d1 += dx + b2; }
      else { x++; y--; dx += 2 * b2; dy -= 2 * a2; d1 += dx - dy + b2; }
    }
    let d2 = b2 * (x + 0.5) ** 2 + a2 * (y - 1) ** 2 - a2 * b2;
    while (y >= 0) {
      p4(x, y);
      if (d2 > 0) { y--; dy -= 2 * a2; d2 += a2 - dy; }
      else { y--; x++; dx += 2 * b2; dy -= 2 * a2; d2 += dx - dy + a2; }
    }
  }

  private blit() {
    const b = this.buf, o = this.out;
    for (let i = 0; i < b.length; i++) o[i] = b[i] | 0xff000000;
    this.ctx.putImageData(this.img!, 0, 0);
  }
}

// -------------------------------------------------------- custom element ----

const PANEL_CSS = `
  :host{display:block;position:fixed;inset:0;z-index:2147483000;background:#000;cursor:none}
  :host([inline]){position:relative;inset:auto;z-index:auto;cursor:auto}
  :host(.ui){cursor:auto}
  canvas{display:block;width:100%;height:100%;image-rendering:pixelated;image-rendering:crisp-edges}
  .panel{position:absolute;left:20px;bottom:20px;width:240px;padding:14px 16px 12px;border-radius:12px;
    background:rgba(10,12,22,.7);backdrop-filter:blur(14px) saturate(1.2);-webkit-backdrop-filter:blur(14px);
    border:1px solid rgba(255,255,255,.08);box-shadow:0 8px 40px rgba(0,0,0,.5);
    font:12px/1.2 ui-sans-serif,system-ui,-apple-system,sans-serif;color:rgba(220,226,240,.82);
    opacity:0;transform:translateY(8px);pointer-events:none;transition:opacity .25s,transform .25s}
  :host(.ui) .panel{opacity:1;transform:none;pointer-events:auto}
  .row{display:grid;grid-template-columns:72px 1fr 30px;align-items:center;gap:8px;margin:7px 0}
  .row span:last-child{text-align:right;font-variant-numeric:tabular-nums;opacity:.6}
  input[type=range]{-webkit-appearance:none;appearance:none;width:100%;height:16px;background:transparent;margin:0}
  input[type=range]::-webkit-slider-runnable-track{height:3px;border-radius:2px;background:rgba(255,255,255,.18)}
  input[type=range]::-moz-range-track{height:3px;border-radius:2px;background:rgba(255,255,255,.18)}
  input[type=range]::-webkit-slider-thumb{-webkit-appearance:none;width:12px;height:12px;margin-top:-4.5px;border-radius:50%;background:#e8ecf6;border:0}
  input[type=range]::-moz-range-thumb{width:12px;height:12px;border-radius:50%;background:#e8ecf6;border:0}
  select{width:100%;padding:3px 4px;border-radius:6px;border:1px solid rgba(255,255,255,.12);
    background:rgba(255,255,255,.06);color:inherit;font:inherit;cursor:pointer}
  select option{background:#11131d;color:#dde2ee}
  button{margin-top:6px;width:100%;padding:7px 0;border-radius:7px;border:1px solid rgba(255,255,255,.12);
    background:rgba(255,255,255,.06);color:inherit;font:inherit;cursor:pointer;letter-spacing:.02em}
  button:hover{background:rgba(255,255,255,.12)}
  .hint{margin-top:9px;text-align:center;opacity:.4;font-size:11px}
  .toast{position:absolute;left:50%;bottom:24px;transform:translateX(-50%);font:12px system-ui,sans-serif;
    color:rgba(220,226,240,.5);letter-spacing:.04em;transition:opacity 1.2s;pointer-events:none}
`;
const STORE_KEY = 'wash.screensaver.moire-classic';
const LABEL: Record<string, string> = {
  lines: 'Lines', rects: 'Rectangles', ovals: 'Ovals', polygon: 'Polygon', fan: 'Fan',
  mono: 'Mono', rainbow: 'Rainbow', green: 'Green phosphor', amber: 'Amber phosphor',
};

/**
 * Registers <wash-screensaver-moire-classic>. Fullscreen fixed overlay by
 * default (`inline` attribute for block layout). Attributes: shape, color,
 * brightness, trail, speed (0..1), pixel (1..4), hold (s).
 * Space toggles the control panel; settings persist in localStorage.
 */
export function defineMoireClassic(tag = 'wash-screensaver-moire-classic') {
  if (customElements.get(tag)) return;
  customElements.define(tag, class extends HTMLElement {
    private m: MoireClassic | null = null;
    private onKey = (e: KeyboardEvent) => {
      if (e.code !== 'Space' || e.repeat) return;
      e.preventDefault();
      this.classList.toggle('ui');
    };
    connectedCallback() {
      const root = this.shadowRoot ?? this.attachShadow({ mode: 'open' });
      const sliders: [string, string][] = [['brightness', 'Brightness'], ['trail', 'Trail'], ['speed', 'Speed']];
      root.innerHTML = `<style>${PANEL_CSS}</style><canvas></canvas>
        <div class="panel">
          <label class="row"><span>Shape</span><select data-s="shape">
            <option value="auto">Cycle</option>${CLASSIC_SHAPES.map(s => `<option value="${s}">${LABEL[s]}</option>`).join('')}
          </select><span></span></label>
          <label class="row"><span>Color</span><select data-s="color">
            ${CLASSIC_COLORS.map(s => `<option value="${s}">${LABEL[s]}</option>`).join('')}
          </select><span></span></label>
          <label class="row"><span>Pixels</span><select data-s="pixel">
            <option value="1">Fine (1×)</option><option value="2">Classic (2×)</option><option value="3">Chunky (3×)</option><option value="4">Huge (4×)</option>
          </select><span></span></label>
          ${sliders.map(([k, l]) => `<label class="row"><span>${l}</span><input type="range" min="0" max="100" data-k="${k}"><span data-v="${k}"></span></label>`).join('')}
          <button type="button">Re-randomize</button>
          <div class="hint">space to hide</div>
        </div>
        <div class="toast">press space for controls</div>`;
      const canvas = root.querySelector('canvas')!;
      const attr = (a: string) => this.getAttribute(a) ?? undefined;
      const num = (a: string) => { const v = this.getAttribute(a); return v == null ? undefined : Number(v); };
      let saved: Record<string, any> = {};
      try { saved = JSON.parse(localStorage.getItem(STORE_KEY) || '{}'); } catch { /* storage blocked */ }
      try {
        this.m = new MoireClassic(canvas, {
          shape: (attr('shape') ?? saved.shape) as MoireClassicOptions['shape'],
          color: (attr('color') ?? saved.color) as MoireClassicOptions['color'],
          brightness: num('brightness') ?? saved.brightness,
          trail: num('trail') ?? saved.trail,
          speed: num('speed') ?? saved.speed,
          pixel: num('pixel') ?? saved.pixel,
          hold: num('hold'),
        });
        this.m.start();
      } catch (e) {
        console.warn(e);
        return;
      }
      const m = this.m;
      const save = () => { try { localStorage.setItem(STORE_KEY, JSON.stringify(m.settings)); } catch { /* ignore */ } };
      const noSpace = (el: HTMLElement) => el.addEventListener('keydown', e => { if (e.code === 'Space') e.preventDefault(); });
      for (const [k] of sliders) {
        const input = root.querySelector<HTMLInputElement>(`input[data-k="${k}"]`)!;
        const out = root.querySelector<HTMLElement>(`[data-v="${k}"]`)!;
        input.value = String(Math.round((m.settings as unknown as Record<string, number>)[k] * 100));
        out.textContent = input.value;
        input.addEventListener('input', () => { out.textContent = input.value; m.set({ [k]: Number(input.value) / 100 }); save(); });
        noSpace(input);
      }
      const sel = (k: string) => root.querySelector<HTMLSelectElement>(`select[data-s="${k}"]`)!;
      const selS = sel('shape'), selC = sel('color'), selP = sel('pixel');
      selS.value = m.settings.shape; selC.value = m.settings.color; selP.value = String(m.settings.pixel);
      selS.addEventListener('change', () => { m.setShape(selS.value as ClassicShape | 'auto'); save(); });
      selC.addEventListener('change', () => { m.set({ color: selC.value as ClassicColor }); save(); });
      selP.addEventListener('change', () => { m.set({ pixel: Number(selP.value) }); save(); });
      for (const s of [selS, selC, selP]) noSpace(s);
      const btn = root.querySelector('button')!;
      btn.addEventListener('click', () => { m.randomize(); btn.blur(); });
      noSpace(btn);
      window.addEventListener('keydown', this.onKey, true);
      const toast = root.querySelector<HTMLElement>('.toast')!;
      setTimeout(() => { toast.style.opacity = '0'; }, 2500);
    }
    disconnectedCallback() {
      window.removeEventListener('keydown', this.onKey, true);
      this.m?.destroy(); this.m = null;
    }
  });
}
