// moire — interference-pattern screensaver for wash.
//
// Zero-dependency WebGL2, one fullscreen fragment shader.
//
//   import { defineMoire } from './moire';
//   defineMoire();                              // registers <wash-screensaver-moire>
//   document.body.append(document.createElement('wash-screensaver-moire'));
//
// Moiré needs a NON-linear combination of fine periodic layers — a plain sum of
// two gratings averages to flat grey; the low-frequency beat only appears when
// the layers multiply (overlaid transparencies) or screen (overlaid light).
// So each pattern produces up to three line/dot coverage layers, and the
// palette decides how they combine:
//   neon  — coloured light lines, screen-blended on black
//   gel   — coloured filter films, multiplied over a dim light
//   ink   — black lines on dim paper light (the classic)
//   glow  — white light lines on black
// Each layer is analytically antialiased with fwidth() and fades to its mean
// coverage once its period approaches a pixel, so the moiré you see is the
// designed one, not raster aliasing.
//
// Patterns cross-fade every `hold` seconds, re-seeding parameters each time.

export type MoirePattern = 'rings' | 'gratings' | 'spirals' | 'halftone';
export type MoirePalette = 'neon' | 'gel' | 'ink' | 'glow';
export const MOIRE_PATTERNS: MoirePattern[] = ['rings', 'gratings', 'spirals', 'halftone'];
export const MOIRE_PALETTES: MoirePalette[] = ['neon', 'gel', 'ink', 'glow'];

export interface MoireOptions {
  /** 'auto' cycles through all patterns (default). */
  pattern?: MoirePattern | 'auto';
  palette?: MoirePalette;
  /** 0..1, default 0.5. */
  brightness?: number;
  /** Line density 0..1 (coarse → fine), default 0.5. */
  density?: number;
  /** Animation speed 0..1, default 0.4. */
  speed?: number;
  /** Seconds each pattern holds before cross-fading (default 40). */
  hold?: number;
  /** Max devicePixelRatio (default 2 — fine lines want real pixels). */
  dprCap?: number;
  /** Frame cap; 0 = every rAF. */
  fps?: number;
}

const VS = /* glsl */ `#version 300 es
void main(){
  vec2 v = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2));
  gl_Position = vec4(v*2.0-1.0, 0.0, 1.0);
}`;

const FS = /* glsl */ `#version 300 es
precision highp float;
uniform vec2 u_res; uniform float u_dpr; uniform float u_time;
uniform float u_period; uniform float u_bright; uniform float u_mix;
uniform int u_modeA, u_modeB, u_pal;
uniform float u_seedA, u_seedB;
out vec4 o;

const float TAU = 6.2831853;
const float LW = 0.30; // line duty (fraction of period)

float hs(float s, float k){ return fract(sin(s*127.1 + k*311.7) * 43758.5453); }
mat2 rot(float a){ float c = cos(a), s = sin(a); return mat2(c, -s, s, c); }

// coverage of a line grating at phase ph, with explicit phase-per-pixel w
float lineCovW(float ph, float w){
  float d = abs(fract(ph) - 0.5);
  float hw = LW * 0.5;
  float c = 1.0 - smoothstep(hw - w, hw + w, d);
  return mix(LW, c, 1.0 - smoothstep(0.28, 0.55, w));
}
float lineCov(float ph){ return lineCovW(ph, fwidth(ph)); }

// hex dot lattice coverage
float dotCov(vec2 q, float r){
  vec2 s = vec2(1.0, 1.7320508);
  vec2 a = mod(q, s) - s*0.5;
  vec2 b = mod(q - s*0.5, s) - s*0.5;
  float d = min(length(a), length(b));
  float w = fwidth(q.x) * 1.2;
  float c = 1.0 - smoothstep(r - w, r + w, d);
  float mean = 3.14159 * r * r * 2.0 / 1.7320508;
  return mix(mean, c, 1.0 - smoothstep(0.22, 0.45, w));
}

// returns per-layer coverage (xyz) — third layer weight in .w
vec4 pattern(int mode, float sd, vec2 p, float t){
  float P = u_period;
  vec2 half_ = u_res / u_dpr * 0.5;
  vec3 cov = vec3(0.0);
  float w3 = 1.0;
  if (mode == 0) {                     // drifting concentric rings
    for (int i = 0; i < 3; i++) {
      float fi = float(i);
      vec2 c = vec2(sin(t*(0.05 + 0.06*hs(sd, fi)) + TAU*hs(sd, fi+3.0)),
                    cos(t*(0.04 + 0.06*hs(sd, fi+6.0)) + TAU*hs(sd, fi+9.0))) * half_ * vec2(0.85, 0.75);
      float ph = length(p - c) / (P * (1.0 + 0.07*fi*hs(sd, 12.0)));
      cov[i] = lineCov(ph);
    }
    w3 = step(0.65, hs(sd, 13.0));
  } else if (mode == 1) {              // near-parallel gratings, slowly warped
    float base = TAU * hs(sd, 1.0);
    for (int i = 0; i < 3; i++) {
      float fi = float(i);
      float a = base + (fi - 1.0) * (0.06 + 0.12*hs(sd, fi+2.0)) + sin(t*0.03 + fi*2.0) * 0.03;
      vec2 d = vec2(cos(a), sin(a)), n = vec2(-d.y, d.x);
      float warp = (6.0 + 14.0*hs(sd, fi+5.0)) * sin(dot(p, n) / (160.0 + 260.0*hs(sd, fi+7.0)) + t*(0.08 + 0.12*hs(sd, fi+8.0)));
      float ph = (dot(p, d) + warp) / (P * (1.0 + 0.05*fi*hs(sd, 16.0))) + t * 0.12 * (fi - 1.0);
      cov[i] = lineCov(ph);
    }
    w3 = step(0.5, hs(sd, 14.0));
  } else if (mode == 2) {              // counter-rotating spirals over rings
    float mn = min(half_.x, half_.y);
    for (int i = 0; i < 3; i++) {
      float fi = float(i);
      vec2 c = vec2(sin(t*0.031 + fi*2.1 + TAU*hs(sd, fi)), cos(t*0.027 + fi*1.7)) * mn * 0.32;
      vec2 q = p - c;
      float r = length(q);
      float m = floor(1.0 + 4.0*hs(sd, fi+4.0)) * (i == 1 ? -1.0 : 1.0) * (i == 2 ? 0.0 : 1.0);
      float k = r / (P * (1.0 + 0.03*fi));
      float ph = k + atan(q.y, q.x) * m / TAU + t * 0.08 * (fi - 1.0);
      float ph2 = k + atan(-q.y, -q.x) * m / TAU;      // same field, branch cut on the other side
      cov[i] = lineCovW(ph, min(fwidth(ph), fwidth(ph2)));
    }
    w3 = 0.7;
  } else {                             // two (or three) rotated halftone screens → rosettes
    float base = TAU * hs(sd, 2.0);
    for (int i = 0; i < 3; i++) {
      float fi = float(i);
      float a = base + fi * (0.04 + 0.09*hs(sd, fi+3.0)) + t * 0.006 * (fi - 1.0);
      float sc = P * 1.7 * (1.0 + 0.02*fi*sin(t*0.05 + fi));
      cov[i] = dotCov(rot(a) * p / sc, 0.30);
    }
    w3 = step(0.55, hs(sd, 15.0));
  }
  return vec4(cov, w3);
}

vec3 tint(float sd, int i){
  // cyan / magenta / amber, rotated per seed
  vec3 T[3] = vec3[3](vec3(0.10, 0.80, 1.00), vec3(1.00, 0.18, 0.70), vec3(1.00, 0.68, 0.16));
  int k = int(floor(hs(sd, 21.0) * 3.0));
  return T[(i + k) % 3];
}

vec3 shade(int mode, float sd, vec2 p, float t){
  vec4 L4 = pattern(mode, sd, p, t);
  vec3 cov = L4.xyz;
  vec3 w = vec3(1.0, 1.0, L4.w);
  // soft wandering light so the field isn't uniformly flat
  vec2 half_ = u_res / u_dpr * 0.5;
  vec2 lc = vec2(sin(t*0.021 + sd*9.0), cos(t*0.017 + sd*5.0)) * half_ * 0.5;
  float mn = min(half_.x, half_.y);
  float L = 0.30 + 0.70 * exp(-dot(p - lc, p - lc) / (mn*mn*1.4));
  vec3 c;
  if (u_pal == 0) {          // neon: screen-blended coloured light lines
    vec3 inv = vec3(1.0);
    for (int i = 0; i < 3; i++) inv *= 1.0 - cov[i] * w[i] * tint(sd, i) * 0.9;
    c = (1.0 - inv) * L * 1.7;
  } else if (u_pal == 1) {   // gel: coloured filter films over light
    vec3 tr = vec3(1.0);
    for (int i = 0; i < 3; i++) tr *= 1.0 - cov[i] * w[i] * (1.0 - tint(sd, i)) * 0.92;
    c = tr * L * vec3(0.95, 0.93, 0.9);
  } else if (u_pal == 2) {   // ink: black lines on paper light
    float tr = 1.0;
    for (int i = 0; i < 3; i++) tr *= 1.0 - cov[i] * w[i] * 0.93;
    c = vec3(tr) * L * vec3(1.0, 0.95, 0.86);
  } else {                   // glow: white light lines
    float inv = 1.0;
    for (int i = 0; i < 3; i++) inv *= 1.0 - cov[i] * w[i] * 0.8;
    c = vec3(1.0 - inv) * L * vec3(0.82, 0.9, 1.0) * 1.3;
  }
  return c;
}

void main(){
  vec2 p = (gl_FragCoord.xy - u_res * 0.5) / u_dpr;   // css px from centre
  vec3 c = shade(u_modeA, u_seedA, p, u_time);
  if (u_mix > 0.0) c = mix(c, shade(u_modeB, u_seedB, p, u_time), smoothstep(0.0, 1.0, u_mix));
  c *= u_bright;
  vec2 uv = gl_FragCoord.xy / u_res - 0.5;
  c *= 1.0 - 0.6 * dot(uv, uv) * 1.6;
  c = pow(max(c, 0.0), vec3(1.1));
  c += (fract(sin(dot(gl_FragCoord.xy + fract(u_time)*61.0, vec2(12.9898, 78.233))) * 43758.5453) - 0.5) / 255.0;
  o = vec4(c, 1.0);
}`;

export class Moire {
  private gl: WebGL2RenderingContext;
  private prog: WebGLProgram;
  private vao: WebGLVertexArrayObject;
  private U = new Map<string, WebGLUniformLocation | null>();
  private o: Required<MoireOptions>;
  private w = 0; private h = 0; private dpr = 1;
  private raf = 0; private running = false;
  private t = 0; private last = 0; private lastDraw = 0;
  private a = { mode: 0, seed: Math.random() * 100 };
  private b = { mode: 0, seed: 0 };
  private fade = -1;          // <0 idle, else 0..1 progress
  private sinceSwitch = 0;
  private ro: ResizeObserver;
  private onVis = () => { if (document.hidden) this.pause(); else if (this.running) this.resume(); };

  constructor(private canvas: HTMLCanvasElement, opts: MoireOptions = {}) {
    const given = Object.fromEntries(Object.entries(opts).filter(([, v]) => v !== undefined && !(typeof v === 'number' && Number.isNaN(v))));
    this.o = { pattern: 'auto', palette: 'neon', brightness: 0.5, density: 0.5, speed: 0.4, hold: 40, dprCap: 2, fps: 0, ...given } as Required<MoireOptions>;
    const gl = canvas.getContext('webgl2', { antialias: false, alpha: false, depth: false, powerPreference: 'low-power' });
    if (!gl) throw new Error('moire: WebGL2 unavailable');
    this.gl = gl;
    const sh = (type: number, src: string) => {
      const s = gl.createShader(type)!;
      gl.shaderSource(s, src); gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error('moire shader: ' + gl.getShaderInfoLog(s));
      return s;
    };
    this.prog = gl.createProgram()!;
    gl.attachShader(this.prog, sh(gl.VERTEX_SHADER, VS));
    gl.attachShader(this.prog, sh(gl.FRAGMENT_SHADER, FS));
    gl.linkProgram(this.prog);
    if (!gl.getProgramParameter(this.prog, gl.LINK_STATUS)) throw new Error('moire link: ' + gl.getProgramInfoLog(this.prog));
    this.vao = gl.createVertexArray()!;
    this.a.mode = this.pickMode(-1);
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
  }

  start() {
    if (this.running) return this;
    this.running = true;
    document.addEventListener('visibilitychange', this.onVis);
    this.resize(); this.resume();
    return this;
  }
  stop() { this.running = false; this.pause(); document.removeEventListener('visibilitychange', this.onVis); }
  destroy() { this.stop(); this.ro.disconnect(); this.gl.getExtension('WEBGL_lose_context')?.loseContext(); }

  get settings() {
    const o = this.o;
    return { pattern: o.pattern, palette: o.palette, brightness: o.brightness, density: o.density, speed: o.speed };
  }
  set(p: Partial<Pick<Required<MoireOptions>, 'brightness' | 'density' | 'speed' | 'hold' | 'palette'>>) {
    Object.assign(this.o, Object.fromEntries(Object.entries(p).filter(([, v]) => v !== undefined)));
    if (!this.running) this.draw();
  }
  /** Pin a pattern ('auto' cycles). Cross-fades to it. */
  setPattern(p: MoirePattern | 'auto') { this.o.pattern = p; this.next(); }
  /** Cross-fade to fresh parameters now. */
  randomize() { this.next(); }

  private pickMode(cur: number) {
    if (this.o.pattern !== 'auto') return MOIRE_PATTERNS.indexOf(this.o.pattern);
    let m = cur;
    while (m === cur) m = Math.floor(Math.random() * MOIRE_PATTERNS.length);
    return m;
  }
  private next() {
    if (this.fade >= 0) { this.a = { ...this.b }; }   // finish an in-flight fade instantly
    this.b = { mode: this.pickMode(this.a.mode), seed: Math.random() * 100 };
    this.fade = 0; this.sinceSwitch = 0;
  }

  private pause() { cancelAnimationFrame(this.raf); this.raf = 0; }
  private resume() {
    if (this.raf) return;
    this.last = performance.now();
    const loop = (now: number) => {
      this.raf = requestAnimationFrame(loop);
      if (this.o.fps > 0 && now - this.lastDraw < 1000 / this.o.fps - 1) return;
      this.lastDraw = now;
      const dt = Math.min((now - this.last) / 1000, 0.1);
      this.last = now;
      this.t += dt * (0.15 + 2.2 * this.o.speed * this.o.speed);
      this.sinceSwitch += dt;
      if (this.fade >= 0) {
        this.fade += dt / 5;
        if (this.fade >= 1) { this.a = { ...this.b }; this.fade = -1; }
      } else if (this.sinceSwitch > this.o.hold) this.next();
      this.draw();
    };
    this.raf = requestAnimationFrame(loop);
  }
  private resize() {
    this.dpr = Math.min(window.devicePixelRatio || 1, this.o.dprCap);
    const w = Math.max(1, Math.round(this.canvas.clientWidth * this.dpr));
    const h = Math.max(1, Math.round(this.canvas.clientHeight * this.dpr));
    if (w === this.w && h === this.h) return;
    this.w = this.canvas.width = w; this.h = this.canvas.height = h;
    if (!this.running) this.draw();
  }
  private u(n: string) {
    if (!this.U.has(n)) this.U.set(n, this.gl.getUniformLocation(this.prog, n));
    return this.U.get(n)!;
  }
  private draw() {
    const gl = this.gl, o = this.o;
    gl.viewport(0, 0, this.w, this.h);
    gl.useProgram(this.prog);
    gl.uniform2f(this.u('u_res'), this.w, this.h);
    gl.uniform1f(this.u('u_dpr'), this.dpr);
    gl.uniform1f(this.u('u_time'), this.t);
    gl.uniform1f(this.u('u_period'), 11 - 7 * o.density);          // css px per line
    gl.uniform1f(this.u('u_bright'), 0.55 * Math.pow(3, o.brightness - 0.5));
    gl.uniform1f(this.u('u_mix'), Math.max(this.fade, 0));
    gl.uniform1i(this.u('u_modeA'), this.a.mode);
    gl.uniform1i(this.u('u_modeB'), this.fade >= 0 ? this.b.mode : this.a.mode);
    gl.uniform1f(this.u('u_seedA'), this.a.seed);
    gl.uniform1f(this.u('u_seedB'), this.b.seed);
    gl.uniform1i(this.u('u_pal'), Math.max(0, MOIRE_PALETTES.indexOf(o.palette)));
    gl.bindVertexArray(this.vao);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }
}

// -------------------------------------------------------- custom element ----

const PANEL_CSS = `
  :host{display:block;position:fixed;inset:0;z-index:2147483000;background:#000;cursor:none}
  :host([inline]){position:relative;inset:auto;z-index:auto;cursor:auto}
  :host(.ui){cursor:auto}
  canvas{display:block;width:100%;height:100%}
  .panel{position:absolute;left:20px;bottom:20px;width:240px;padding:14px 16px 12px;border-radius:12px;
    background:rgba(10,12,22,.6);backdrop-filter:blur(14px) saturate(1.2);-webkit-backdrop-filter:blur(14px);
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
const STORE_KEY = 'wash.screensaver.moire';

/**
 * Registers <wash-screensaver-moire>. Fullscreen fixed overlay by default
 * (`inline` attribute for block layout). Attributes: pattern, palette,
 * brightness, density, speed (0..1), hold (s), fps.
 * Space toggles the control panel; settings persist in localStorage.
 */
export function defineMoire(tag = 'wash-screensaver-moire') {
  if (customElements.get(tag)) return;
  customElements.define(tag, class extends HTMLElement {
    private m: Moire | null = null;
    private onKey = (e: KeyboardEvent) => {
      if (e.code !== 'Space' || e.repeat) return;
      e.preventDefault();
      this.classList.toggle('ui');
    };
    connectedCallback() {
      const root = this.shadowRoot ?? this.attachShadow({ mode: 'open' });
      const sliders: [string, string][] = [['brightness', 'Brightness'], ['density', 'Density'], ['speed', 'Speed']];
      const cap = (s: string) => s[0].toUpperCase() + s.slice(1);
      root.innerHTML = `<style>${PANEL_CSS}</style><canvas></canvas>
        <div class="panel">
          <label class="row"><span>Pattern</span><select data-s="pattern">
            <option value="auto">Cycle</option>${MOIRE_PATTERNS.map(p => `<option value="${p}">${cap(p)}</option>`).join('')}
          </select><span></span></label>
          <label class="row"><span>Palette</span><select data-s="palette">
            ${MOIRE_PALETTES.map(p => `<option value="${p}">${cap(p)}</option>`).join('')}
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
        this.m = new Moire(canvas, {
          pattern: (attr('pattern') ?? saved.pattern) as MoireOptions['pattern'],
          palette: (attr('palette') ?? saved.palette) as MoireOptions['palette'],
          brightness: num('brightness') ?? saved.brightness,
          density: num('density') ?? saved.density,
          speed: num('speed') ?? saved.speed,
          hold: num('hold'), fps: num('fps'),
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
      const selP = root.querySelector<HTMLSelectElement>('select[data-s="pattern"]')!;
      const selC = root.querySelector<HTMLSelectElement>('select[data-s="palette"]')!;
      selP.value = m.settings.pattern; selC.value = m.settings.palette;
      selP.addEventListener('change', () => { m.setPattern(selP.value as MoirePattern | 'auto'); save(); });
      selC.addEventListener('change', () => { m.set({ palette: selC.value as MoirePalette }); save(); });
      noSpace(selP); noSpace(selC);
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
