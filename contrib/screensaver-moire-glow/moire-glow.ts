// moire-glow — modern take on the classic Mac XOR-line moiré screensaver.
//
// Zero-dependency WebGL2. Same idea as the original — a bouncing figure that
// leaves a trail of copies of itself, with moiré where the copies bunch up —
// but drawn as antialiased light instead of 1-bit XOR:
//   * every trail copy is a polyline, every segment an instanced quad whose
//     fragment shader evaluates a capsule SDF (crisp core + soft halo);
//   * additive blend into a half-float target, so dense regions of the trail
//     build up into bright interference bands instead of cancelling;
//   * copies fade and shift hue with age, the head of the trail is brightest;
//   * bloom from a few tent-filtered taps of the target's mip chain, then
//     tonemap, vignette, dither.
// Motion is smooth: vertices fly with slowly turning velocities (no random
// jitter) and bounce off the edges, sampled at a fixed 60 Hz tick so the trail
// spacing is even.
//
//   import { defineMoireGlow } from './moire-glow';
//   defineMoireGlow();          // registers <wash-screensaver-moire-glow>
//   document.body.append(document.createElement('wash-screensaver-moire-glow'));

export type GlowShape = 'lines' | 'rects' | 'ovals' | 'polygon' | 'fan';
export type GlowPalette = 'aurora' | 'neon' | 'ice' | 'ember' | 'mono';
export const GLOW_SHAPES: GlowShape[] = ['lines', 'rects', 'ovals', 'polygon', 'fan'];
export const GLOW_PALETTES: GlowPalette[] = ['aurora', 'neon', 'ice', 'ember', 'mono'];

export interface MoireGlowOptions {
  /** 'auto' cycles shapes (default). */
  shape?: GlowShape | 'auto';
  palette?: GlowPalette;
  /** 0..1 (default 0.5). */
  brightness?: number;
  /** Trail length 0..1 → 12..220 copies (default 0.5). */
  trail?: number;
  /** 0..1 (default 0.4). */
  speed?: number;
  /** Bloom amount 0..1 (default 0.5). */
  glow?: number;
  /** Seconds per shape when cycling (default 35). */
  hold?: number;
  /** Max devicePixelRatio (default 2). */
  dprCap?: number;
}

const SEG = 8;                 // floats per segment instance: ax ay bx by r g b w
const MAX_SEGS = 40000;

const SEG_VS = /* glsl */ `#version 300 es
layout(location=0) in vec4 a_ab;
layout(location=1) in vec4 a_cw;
uniform vec2 u_res; uniform float u_hw; uniform float u_halo;
out vec2 v_p; out vec2 v_a; out vec2 v_b; out vec4 v_cw;
const vec2 C[6] = vec2[6](vec2(0,-1),vec2(1,-1),vec2(0,1),vec2(0,1),vec2(1,-1),vec2(1,1));
void main(){
  vec2 a = a_ab.xy, b = a_ab.zw;
  vec2 d = b - a; float L = length(d);
  vec2 t = L > 1e-4 ? d / L : vec2(1, 0), n = vec2(-t.y, t.x);
  float R = u_hw + u_halo * 3.0 + 1.0;
  vec2 c = C[gl_VertexID];
  vec2 p = a + t * (c.x * L + (c.x * 2.0 - 1.0) * R) + n * c.y * R;
  v_p = p; v_a = a; v_b = b; v_cw = a_cw;
  gl_Position = vec4(p / u_res * 2.0 - 1.0, 0.0, 1.0);
}`;

const SEG_FS = /* glsl */ `#version 300 es
precision highp float;
uniform float u_hw; uniform float u_halo;
in vec2 v_p; in vec2 v_a; in vec2 v_b; in vec4 v_cw; out vec4 o;
void main(){
  vec2 ab = v_b - v_a, ap = v_p - v_a;
  float h = dot(ap, ab) / max(dot(ab, ab), 1e-6);
  // half-open segment: no end cap, so joints of a polyline aren't double-lit
  if (h >= 1.0 && dot(ab, ab) > 1e-6) discard;
  float d = length(ap - ab * clamp(h, 0.0, 1.0));
  float core = 1.0 - smoothstep(u_hw - 0.7, u_hw + 0.7, d);
  float halo = exp(-d * d / (u_halo * u_halo)) * 0.22;
  o = vec4(v_cw.rgb * v_cw.a * (core + halo), 1.0);
}`;

const FS_VS = /* glsl */ `#version 300 es
void main(){
  vec2 v = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2));
  gl_Position = vec4(v*2.0-1.0, 0.0, 1.0);
}`;

// separable 13-tap gaussian (sigma ~3 texels) on a downsampled buffer
const BLUR_FS = /* glsl */ `#version 300 es
precision highp float;
uniform sampler2D u_src; uniform vec2 u_dir; uniform vec2 u_srcRes; uniform vec2 u_dstRes; uniform float u_lod;
out vec4 o;
void main(){
  vec2 uv = gl_FragCoord.xy / u_dstRes;
  vec2 st = u_dir / u_srcRes;
  float W[7] = float[7](0.1585, 0.1465, 0.1157, 0.0781, 0.0451, 0.0222, 0.0094);
  vec3 c = textureLod(u_src, uv, u_lod).rgb * W[0];
  for (int i = 1; i < 7; i++) {
    c += textureLod(u_src, uv + st * float(i), u_lod).rgb * W[i];
    c += textureLod(u_src, uv - st * float(i), u_lod).rgb * W[i];
  }
  o = vec4(c, 1.0);
}`;

const COMP_FS = /* glsl */ `#version 300 es
precision highp float;
uniform sampler2D u_scene; uniform sampler2D u_b1; uniform sampler2D u_b2; uniform vec2 u_res; uniform float u_time;
uniform float u_exposure; uniform float u_bloom;
out vec4 o;
void main(){
  vec2 uv = gl_FragCoord.xy / u_res;
  vec3 c = textureLod(u_scene, uv, 0.0).rgb;
  c += (texture(u_b1, uv).rgb * 0.9 + texture(u_b2, uv).rgb * 0.8) * u_bloom;
  // faint deep-blue floor so pure black isn't dead
  vec2 q = uv - 0.5;
  c += vec3(0.0006, 0.0008, 0.0018) * (1.0 - dot(q, q) * 2.0);
  c = 1.0 - exp(-c * u_exposure);
  c *= 1.0 - 0.55 * dot(q, q) * 1.5;
  c = pow(c, vec3(1.0 / 2.2));
  c += (fract(sin(dot(gl_FragCoord.xy + fract(u_time) * 71.0, vec2(12.9898, 78.233))) * 43758.5453) - 0.5) / 255.0;
  o = vec4(c, 1.0);
}`;

type V = { x: number; y: number; a: number; s: number; w: number; ph: number };
type Copy = { pts: Float32Array; tick: number };

function hsl(h: number, s: number, l: number): [number, number, number] {
  const f = (n: number) => {
    const k = (n + h * 12) % 12, a = s * Math.min(l, 1 - l);
    return l - a * Math.max(-1, Math.min(k - 3, 9 - k, 1));
  };
  return [f(0), f(8), f(4)];
}

export class MoireGlow {
  private gl: WebGL2RenderingContext;
  private o: Required<MoireGlowOptions>;
  private segProg: WebGLProgram; private compProg: WebGLProgram; private blurProg: WebGLProgram;
  private bl: { tex: WebGLTexture; fbo: WebGLFramebuffer; w: number; h: number }[] = [];
  private lastCopy: Float32Array | null = null;
  private U = new Map<WebGLProgram, Map<string, WebGLUniformLocation | null>>();
  private vao: WebGLVertexArrayObject; private emptyVao: WebGLVertexArrayObject;
  private buf: WebGLBuffer;
  private data = new Float32Array(MAX_SEGS * SEG);
  private fbo: WebGLFramebuffer | null = null; private tex: WebGLTexture | null = null;
  private hdr: boolean;
  private w = 0; private h = 0; private dpr = 1;
  private raf = 0; private running = false;
  private time = 0; private last = 0; private acc = 0; private tickN = 0; private since = 0;
  private mode: GlowShape = 'lines';
  private verts: V[] = [];
  private trail: Copy[] = [];
  private hue0 = Math.random();
  private ro: ResizeObserver;
  private onVis = () => { if (document.hidden) this.pause(); else if (this.running) this.resume(); };

  constructor(private canvas: HTMLCanvasElement, opts: MoireGlowOptions = {}) {
    const given = Object.fromEntries(Object.entries(opts).filter(([, v]) => v !== undefined && !(typeof v === 'number' && Number.isNaN(v))));
    this.o = { shape: 'auto', palette: 'aurora', brightness: 0.5, trail: 0.5, speed: 0.4, glow: 0.5, hold: 35, dprCap: 2, ...given } as Required<MoireGlowOptions>;
    const gl = canvas.getContext('webgl2', { antialias: false, alpha: false, depth: false, powerPreference: 'low-power' });
    if (!gl) throw new Error('moire-glow: WebGL2 unavailable');
    this.gl = gl;
    this.hdr = !!gl.getExtension('EXT_color_buffer_float');
    this.segProg = this.compile(SEG_VS, SEG_FS);
    this.compProg = this.compile(FS_VS, COMP_FS);
    this.blurProg = this.compile(FS_VS, BLUR_FS);
    this.emptyVao = gl.createVertexArray()!;
    this.vao = gl.createVertexArray()!;
    this.buf = gl.createBuffer()!;
    gl.bindVertexArray(this.vao);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
    gl.bufferData(gl.ARRAY_BUFFER, this.data.byteLength, gl.DYNAMIC_DRAW);
    for (let i = 0; i < 2; i++) {
      gl.enableVertexAttribArray(i);
      gl.vertexAttribPointer(i, 4, gl.FLOAT, false, SEG * 4, i * 16);
      gl.vertexAttribDivisor(i, 1);
    }
    gl.bindVertexArray(null);
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
  }

  start() {
    if (this.running) return this;
    this.running = true;
    document.addEventListener('visibilitychange', this.onVis);
    this.resize();
    this.reset(this.pick());
    this.resume();
    return this;
  }
  stop() { this.running = false; this.pause(); document.removeEventListener('visibilitychange', this.onVis); }
  destroy() { this.stop(); this.ro.disconnect(); this.gl.getExtension('WEBGL_lose_context')?.loseContext(); }

  get settings() {
    const o = this.o;
    return { shape: o.shape, palette: o.palette, brightness: o.brightness, trail: o.trail, speed: o.speed, glow: o.glow };
  }
  set(p: Partial<Pick<Required<MoireGlowOptions>, 'brightness' | 'trail' | 'speed' | 'glow' | 'palette' | 'hold'>>) {
    Object.assign(this.o, Object.fromEntries(Object.entries(p).filter(([, v]) => v !== undefined)));
  }
  setShape(s: GlowShape | 'auto') { this.o.shape = s; this.reset(this.pick()); }
  randomize() { this.hue0 = Math.random(); this.reset(this.pick()); }

  // ---- GL plumbing
  private compile(vs: string, fs: string) {
    const gl = this.gl;
    const mk = (t: number, src: string) => {
      const s = gl.createShader(t)!; gl.shaderSource(s, src); gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error('moire-glow shader: ' + gl.getShaderInfoLog(s));
      return s;
    };
    const p = gl.createProgram()!;
    gl.attachShader(p, mk(gl.VERTEX_SHADER, vs)); gl.attachShader(p, mk(gl.FRAGMENT_SHADER, fs));
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error('moire-glow link: ' + gl.getProgramInfoLog(p));
    this.U.set(p, new Map());
    return p;
  }
  private u(p: WebGLProgram, n: string) {
    const m = this.U.get(p)!;
    if (!m.has(n)) m.set(n, this.gl.getUniformLocation(p, n));
    return m.get(n)!;
  }
  private resize() {
    const gl = this.gl;
    this.dpr = Math.min(window.devicePixelRatio || 1, this.o.dprCap);
    const w = Math.max(1, Math.round(this.canvas.clientWidth * this.dpr));
    const h = Math.max(1, Math.round(this.canvas.clientHeight * this.dpr));
    if (w === this.w && h === this.h) return;
    const first = this.w === 0;
    // keep the figure where it was, proportionally
    if (!first) for (const v of this.verts) { v.x *= w / this.w; v.y *= h / this.h; }
    this.w = w; this.h = h;
    this.canvas.width = w; this.canvas.height = h;
    if (this.tex) gl.deleteTexture(this.tex);
    if (this.fbo) gl.deleteFramebuffer(this.fbo);
    this.tex = gl.createTexture()!;
    gl.bindTexture(gl.TEXTURE_2D, this.tex);
    if (this.hdr) gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA16F, w, h, 0, gl.RGBA, gl.HALF_FLOAT, null);
    else gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, w, h, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR_MIPMAP_LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.generateMipmap(gl.TEXTURE_2D);
    this.fbo = gl.createFramebuffer()!;
    gl.bindFramebuffer(gl.FRAMEBUFFER, this.fbo);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, this.tex, 0);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    // bloom targets: [A1, B1] at 1/4 res, [A2, B2] at 1/16 res
    for (const t of this.bl) { gl.deleteTexture(t.tex); gl.deleteFramebuffer(t.fbo); }
    this.bl = [4, 4, 16, 16].map(div => this.target(Math.max(1, Math.round(w / div)), Math.max(1, Math.round(h / div))));
    this.trail = []; this.lastCopy = null;
  }
  private target(w: number, h: number) {
    const gl = this.gl;
    const tex = gl.createTexture()!;
    gl.bindTexture(gl.TEXTURE_2D, tex);
    if (this.hdr) gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA16F, w, h, 0, gl.RGBA, gl.HALF_FLOAT, null);
    else gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA8, w, h, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    const fbo = gl.createFramebuffer()!;
    gl.bindFramebuffer(gl.FRAMEBUFFER, fbo);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, tex, 0);
    return { tex, fbo, w, h };
  }
  private blur(src: WebGLTexture, srcW: number, srcH: number, lod: number, a: number, b: number) {
    const gl = this.gl, P = this.blurProg, A = this.bl[a], B = this.bl[b];
    gl.useProgram(P);
    gl.uniform1i(this.u(P, 'u_src'), 0);
    gl.activeTexture(gl.TEXTURE0);
    // H pass: src (at lod) -> A ; V pass: A -> B
    gl.bindFramebuffer(gl.FRAMEBUFFER, A.fbo); gl.viewport(0, 0, A.w, A.h);
    gl.bindTexture(gl.TEXTURE_2D, src);
    gl.uniform2f(this.u(P, 'u_dir'), 1, 0);
    gl.uniform2f(this.u(P, 'u_srcRes'), A.w, A.h);
    gl.uniform2f(this.u(P, 'u_dstRes'), A.w, A.h);
    gl.uniform1f(this.u(P, 'u_lod'), lod);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    gl.bindFramebuffer(gl.FRAMEBUFFER, B.fbo);
    gl.bindTexture(gl.TEXTURE_2D, A.tex);
    gl.uniform2f(this.u(P, 'u_dir'), 0, 1);
    gl.uniform1f(this.u(P, 'u_lod'), 0);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
    void srcW; void srcH;
  }

  // ---- figure
  private pick(): GlowShape {
    if (this.o.shape !== 'auto') return this.o.shape;
    let m = this.mode;
    while (m === this.mode) m = GLOW_SHAPES[Math.floor(Math.random() * GLOW_SHAPES.length)];
    return m;
  }
  private reset(mode: GlowShape) {
    this.mode = mode;
    this.trail = []; this.lastCopy = null;
    this.since = 0;
    const n = mode === 'polygon' ? 3 + Math.floor(Math.random() * 3) : 2;
    const R = Math.random;
    this.verts = Array.from({ length: n }, () => ({
      x: (0.15 + 0.7 * R()) * this.w, y: (0.15 + 0.7 * R()) * this.h,
      a: R() * Math.PI * 2, s: 0.7 + 0.6 * R(), w: (R() - 0.5) * 1.2, ph: R() * 100,
    }));
  }
  private step(dt: number) {
    const W = this.w, H = this.h, k = this.dpr;
    const base = (40 + 380 * this.o.speed * this.o.speed) * k;
    for (const v of this.verts) {
      // velocity heading drifts smoothly — curving, never jittery
      v.a += dt * (v.w * 0.9 + 0.6 * Math.sin(this.time * 0.23 + v.ph) * Math.sin(this.time * 0.071 + v.ph * 1.7));
      let vx = Math.cos(v.a) * v.s * base, vy = Math.sin(v.a) * v.s * base;
      v.x += vx * dt; v.y += vy * dt;
      if (v.x < 0) { v.x = -v.x; vx = Math.abs(vx); v.a = Math.atan2(vy, vx); }
      if (v.x > W) { v.x = 2 * W - v.x; vx = -Math.abs(vx); v.a = Math.atan2(vy, vx); }
      if (v.y < 0) { v.y = -v.y; vy = Math.abs(vy); v.a = Math.atan2(vy, vx); }
      if (v.y > H) { v.y = 2 * H - v.y; vy = -Math.abs(vy); v.a = Math.atan2(vy, vx); }
    }
  }
  private outline(): Float32Array {
    const v = this.verts;
    if (this.mode === 'lines') return new Float32Array([v[0].x, v[0].y, v[1].x, v[1].y]);
    if (this.mode === 'polygon') {
      const a = new Float32Array((v.length + 1) * 2);
      v.forEach((p, i) => { a[2 * i] = p.x; a[2 * i + 1] = p.y; });
      a[2 * v.length] = v[0].x; a[2 * v.length + 1] = v[0].y;
      return a;
    }
    const x0 = Math.min(v[0].x, v[1].x), x1 = Math.max(v[0].x, v[1].x);
    const y0 = Math.min(v[0].y, v[1].y), y1 = Math.max(v[0].y, v[1].y);
    if (this.mode === 'rects') return new Float32Array([x0, y0, x1, y0, x1, y1, x0, y1, x0, y0]);
    // ovals
    const cx = (x0 + x1) / 2, cy = (y0 + y1) / 2, rx = (x1 - x0) / 2, ry = (y1 - y0) / 2;
    const n = Math.max(24, Math.min(96, Math.round((rx + ry) / (3 * this.dpr))));
    const a = new Float32Array((n + 1) * 2);
    for (let i = 0; i <= n; i++) { const t = (i / n) * Math.PI * 2; a[2 * i] = cx + rx * Math.cos(t); a[2 * i + 1] = cy + ry * Math.sin(t); }
    return a;
  }
  private gap() { return 5.5 * this.dpr; }
  private colorAt(age01: number): [number, number, number] {
    const t = this.time;
    switch (this.o.palette) {
      case 'neon': return hsl((this.hue0 + t * 0.03 - age01 * 0.35 + 10) % 1, 1.0, 0.55);
      case 'ice': return hsl(0.55 + 0.08 * age01, 0.75, 0.62 + 0.25 * (1 - age01));
      case 'ember': return hsl(0.02 + 0.09 * (1 - age01), 1.0, 0.5 + 0.12 * (1 - age01));
      case 'mono': return [1, 1, 1];
      default: return hsl((this.hue0 + 0.48 + 0.30 * age01 + 0.06 * Math.sin(t * 0.05)) % 1, 0.85, 0.58);
    }
  }

  // ---- loop
  private pause() { cancelAnimationFrame(this.raf); this.raf = 0; }
  private resume() {
    if (this.raf) return;
    this.last = performance.now();
    const TICK = 1 / 60;
    const loop = (now: number) => {
      this.raf = requestAnimationFrame(loop);
      const dt = Math.min((now - this.last) / 1000, 0.1);
      this.last = now;
      this.time += dt; this.since += dt;
      if (this.o.shape === 'auto' && this.since > this.o.hold) this.reset(this.pick());
      this.acc += dt;
      let n = 0;
      while (this.acc >= TICK && n++ < 8) {
        this.acc -= TICK; this.step(TICK); this.tickN++;
        if (this.mode !== 'fan') {
          const pts = this.outline(), lc = this.lastCopy;
          let moved = Infinity;
          if (lc && lc.length === pts.length) {
            moved = 0;
            for (let i = 0; i < pts.length; i++) moved = Math.max(moved, Math.abs(pts[i] - lc[i]));
          }
          // a new copy every ~gap px of travel: distinct lines, even spacing
          if (moved >= this.gap()) { this.trail.push({ pts, tick: this.tickN }); this.lastCopy = pts; }
        }
      }
      const max = Math.round(10 + 150 * this.o.trail);
      if (this.trail.length > max) this.trail.splice(0, this.trail.length - max);
      this.draw();
    };
    this.raf = requestAnimationFrame(loop);
  }

  private build(): number {
    const d = this.data; let n = 0;
    const push = (ax: number, ay: number, bx: number, by: number, c: [number, number, number], w: number) => {
      if (n >= MAX_SEGS) return;
      const i = n++ * SEG;
      d[i] = ax; d[i + 1] = this.h - ay; d[i + 2] = bx; d[i + 3] = this.h - by;
      d[i + 4] = c[0]; d[i + 5] = c[1]; d[i + 6] = c[2]; d[i + 7] = w;
    };
    if (this.mode === 'fan') {
      // rays from each hub to evenly spaced points round the border; the
      // rasterized fan of thin lines is what produces the moiré
      const W = this.w, H = this.h;
      const gap = (5 + 13 * (1 - this.o.trail)) * this.dpr;
      const per = 2 * (W + H);
      const nr = Math.min(Math.floor(per / gap), (MAX_SEGS / 2) | 0);
      this.verts.forEach((v, hi) => {
        const off = (this.time * 6 * this.dpr * (hi ? -1 : 1)) % gap;
        for (let i = 0; i < nr; i++) {
          let s = (i * gap + off + per) % per, x: number, y: number;
          if (s < W) { x = s; y = 0; } else if ((s -= W) < H) { x = W; y = s; }
          else if ((s -= H) < W) { x = W - s; y = H; } else { s -= W; x = 0; y = H - s; }
          const c = this.colorAt((hi * 0.5 + i / nr * 0.5) % 1);
          push(v.x, v.y, x, y, c, 0.15);
        }
      });
      return n;
    }
    const N = this.trail.length;
    for (let k = 0; k < N; k++) {
      const age = (N - 1 - k) / Math.max(N - 1, 1);         // 0 = newest
      const fade = Math.pow(1 - age, 1.6);
      const w = (0.06 + 0.70 * fade) * (k === N - 1 ? 2.4 : 1);
      const c = this.colorAt(age);
      const p = this.trail[k].pts;
      for (let i = 0; i + 3 < p.length; i += 2) push(p[i], p[i + 1], p[i + 2], p[i + 3], c, w);
    }
    return n;
  }

  private draw() {
    const gl = this.gl;
    if (!this.fbo) return;
    const n = this.build();
    gl.bindFramebuffer(gl.FRAMEBUFFER, this.fbo);
    gl.viewport(0, 0, this.w, this.h);
    gl.clearColor(0, 0, 0, 1); gl.clear(gl.COLOR_BUFFER_BIT);
    if (n) {
      gl.useProgram(this.segProg);
      gl.uniform2f(this.u(this.segProg, 'u_res'), this.w, this.h);
      gl.uniform1f(this.u(this.segProg, 'u_hw'), 0.55 * this.dpr);
      gl.uniform1f(this.u(this.segProg, 'u_halo'), 2.2 * this.dpr);
      gl.bindBuffer(gl.ARRAY_BUFFER, this.buf);
      gl.bufferSubData(gl.ARRAY_BUFFER, 0, this.data, 0, n * SEG);
      gl.enable(gl.BLEND); gl.blendFunc(gl.ONE, gl.ONE);
      gl.bindVertexArray(this.vao);
      gl.drawArraysInstanced(gl.TRIANGLES, 0, 6, n);
      gl.disable(gl.BLEND);
    }
    gl.bindVertexArray(this.emptyVao);
    gl.bindTexture(gl.TEXTURE_2D, this.tex);
    gl.generateMipmap(gl.TEXTURE_2D);
    this.blur(this.tex!, this.w, this.h, 2.0, 0, 1);              // ~12 px glow
    this.blur(this.tex!, this.w, this.h, 4.0, 2, 3);              // ~48 px glow
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.viewport(0, 0, this.w, this.h);
    gl.useProgram(this.compProg);
    gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D, this.tex);
    gl.activeTexture(gl.TEXTURE1); gl.bindTexture(gl.TEXTURE_2D, this.bl[1].tex);
    gl.activeTexture(gl.TEXTURE2); gl.bindTexture(gl.TEXTURE_2D, this.bl[3].tex);
    gl.activeTexture(gl.TEXTURE0);
    gl.uniform1i(this.u(this.compProg, 'u_scene'), 0);
    gl.uniform1i(this.u(this.compProg, 'u_b1'), 1);
    gl.uniform1i(this.u(this.compProg, 'u_b2'), 2);
    gl.uniform2f(this.u(this.compProg, 'u_res'), this.w, this.h);
    gl.uniform1f(this.u(this.compProg, 'u_time'), this.time);
    gl.uniform1f(this.u(this.compProg, 'u_exposure'), 0.9 * Math.pow(4, this.o.brightness - 0.5));
    gl.uniform1f(this.u(this.compProg, 'u_bloom'), 1.4 * this.o.glow);
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
const STORE_KEY = 'wash.screensaver.moire-glow';
const LABEL: Record<string, string> = {
  lines: 'Lines', rects: 'Rectangles', ovals: 'Ovals', polygon: 'Polygon', fan: 'Fan',
  aurora: 'Aurora', neon: 'Neon', ice: 'Ice', ember: 'Ember', mono: 'Mono',
};

/**
 * Registers <wash-screensaver-moire-glow>. Fullscreen fixed overlay by
 * default (`inline` for block layout). Attributes: shape, palette,
 * brightness, trail, speed, glow (0..1), hold (s).
 * Space toggles the control panel; settings persist in localStorage.
 */
export function defineMoireGlow(tag = 'wash-screensaver-moire-glow') {
  if (customElements.get(tag)) return;
  customElements.define(tag, class extends HTMLElement {
    private m: MoireGlow | null = null;
    private onKey = (e: KeyboardEvent) => {
      if (e.code !== 'Space' || e.repeat) return;
      e.preventDefault();
      this.classList.toggle('ui');
    };
    connectedCallback() {
      const root = this.shadowRoot ?? this.attachShadow({ mode: 'open' });
      const sliders: [string, string][] = [['brightness', 'Brightness'], ['glow', 'Glow'], ['trail', 'Trail'], ['speed', 'Speed']];
      root.innerHTML = `<style>${PANEL_CSS}</style><canvas></canvas>
        <div class="panel">
          <label class="row"><span>Shape</span><select data-s="shape">
            <option value="auto">Cycle</option>${GLOW_SHAPES.map(s => `<option value="${s}">${LABEL[s]}</option>`).join('')}
          </select><span></span></label>
          <label class="row"><span>Palette</span><select data-s="palette">
            ${GLOW_PALETTES.map(s => `<option value="${s}">${LABEL[s]}</option>`).join('')}
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
        this.m = new MoireGlow(canvas, {
          shape: (attr('shape') ?? saved.shape) as MoireGlowOptions['shape'],
          palette: (attr('palette') ?? saved.palette) as MoireGlowOptions['palette'],
          brightness: num('brightness') ?? saved.brightness,
          glow: num('glow') ?? saved.glow,
          trail: num('trail') ?? saved.trail,
          speed: num('speed') ?? saved.speed,
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
      const selS = sel('shape'), selP = sel('palette');
      selS.value = m.settings.shape; selP.value = m.settings.palette;
      selS.addEventListener('change', () => { m.setShape(selS.value as GlowShape | 'auto'); save(); });
      selP.addEventListener('change', () => { m.set({ palette: selP.value as GlowPalette }); save(); });
      noSpace(selS); noSpace(selP);
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
