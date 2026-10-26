// harbor-night — night harbor city screensaver for wash.
//
// Zero-dependency WebGL2. Drive a canvas: new HarborNight(canvas).start();
// the shell mounts it through entries/harbor.ts.
//
// Pipeline (all into one HDR offscreen target, then a composite pass):
//   1. sky      fullscreen: gradient + city light dome, stars, moon, fbm clouds
//   2. planes   instanced light sprites (CPU-simulated nav/strobe/beacon/landing)
//   3. city     per building group: silhouette quad, then its window lights
//               (window on/off state is evaluated in the vertex shader from a
//               hash of (seed, floor(t/period)) — no per-frame CPU work)
//   4. composite  above horizon: scene. Below: mirrored scene displaced by a
//               perspective-correct, LOD-faded wave gradient, supersampled
//               (6 taps) → shimmering vertical light columns. Then tonemap,
//               vignette, dither.
// Anything added to the light pass (fireworks later) gets reflected for free.

export interface HarborNightOptions {
  /** Animation speed multiplier (default 1). */
  speed?: number;
  /** Deterministic city layout seed (default 7). */
  seed?: number;
  /** Max devicePixelRatio used for the backbuffer (default 1.5). */
  dprCap?: number;
  /** Frame cap; 0 = every rAF (default 0). 30 halves GPU cost. */
  fps?: number;
  /** Overall brightness, 0..1 (default 0.5). */
  brightness?: number;
  /** Cloud cover, 0..1 (default 0.4). */
  clouds?: number;
  /** Harbour fog / haze, 0..1 (default 0). */
  fog?: number;
  /** Bridge style; 'auto' picks from the seed (default 'auto'). */
  bridge?: BridgeType | 'auto';
  /** Which edge the bridge runs off; 'auto' picks from the seed (default 'auto'). */
  bridgeSide?: BridgeSide | 'auto';
}

export type BridgeType = 'cable' | 'suspension' | 'arch' | 'none';
export type BridgeSide = 'left' | 'right';
export const BRIDGE_TYPES: BridgeType[] = ['cable', 'suspension', 'arch', 'none'];

const HZ = 0.36; // horizon height, fraction of screen height from bottom

// ---------------------------------------------------------------- GLSL ----

const COMMON = /* glsl */ `#version 300 es
precision highp float;
precision highp int;
uint pcg(uint v){ uint s=v*747796405u+2891336453u; uint w=((s>>((s>>28u)+4u))^s)*277803737u; return (w>>22u)^w; }
float h1(uint v){ return float(pcg(v))*(1.0/4294967295.0); }
float h2(uvec2 v){ return h1(v.x*1597334673u ^ pcg(v.y)); }
`;

const SKY_FN = /* glsl */ `
uniform float u_hz;
uniform float u_cityX;
vec3 skyBase(vec2 p){
  float h = max(p.y - u_hz, 0.0);
  float t = clamp(h / (1.0 - u_hz), 0.0, 1.0);
  vec3 zen = vec3(0.0004, 0.0006, 0.0020);
  vec3 mid = vec3(0.0020, 0.0030, 0.0075);
  vec3 hor = vec3(0.0100, 0.0095, 0.0175);
  vec3 c = mix(hor, mid, smoothstep(0.0, 0.33, t));
  c = mix(c, zen, smoothstep(0.25, 1.0, t));
  // light dome over downtown: modern LED city — neutral-violet with a warm core
  float dx = (p.x - u_cityX) / 0.55;
  float dome = exp(-h * 8.0) * (0.35 + exp(-dx*dx));
  c += mix(vec3(0.010,0.008,0.016), vec3(0.020,0.012,0.009), exp(-h*20.0)) * dome;
  return c;
}
`;

const FS_TRI_VS = /* glsl */ `${COMMON}
void main(){
  vec2 v = vec2(float((gl_VertexID<<1)&2), float(gl_VertexID&2));
  gl_Position = vec4(v*2.0-1.0, 0.0, 1.0);
}`;

const SKY_FS = /* glsl */ `${COMMON}${SKY_FN}
uniform vec2 u_res; uniform float u_time; uniform float u_dpr;
uniform vec3 u_moon; // x, y, r in scene units
uniform float u_cloud; // cover 0..1
out vec4 o;
float vnoise(vec2 p){
  vec2 i=floor(p), f=fract(p); f=f*f*(3.0-2.0*f);
  uvec2 u=uvec2(ivec2(i)+32768);
  float a=h2(u), b=h2(u+uvec2(1,0)), c=h2(u+uvec2(0,1)), d=h2(u+uvec2(1,1));
  return mix(mix(a,b,f.x), mix(c,d,f.x), f.y);
}
float fbm(vec2 p){ float s=0.0,a=0.5; for(int i=0;i<5;i++){ s+=a*vnoise(p); p=p*2.03+vec2(17.1,9.3); a*=0.5; } return s; }
void main(){
  vec2 p = gl_FragCoord.xy / u_res.y;
  float h = p.y - u_hz;
  vec3 c = skyBase(p);
  float t = u_time;

  // clouds (computed first: they occlude stars + moon)
  vec2 q = vec2(p.x*1.1 + t*0.0045, p.y*3.4);
  q += 0.35*vec2(fbm(q*0.7 + t*0.002), fbm(q*0.7 + 4.0));
  float cl = fbm(q);
  float lo = mix(0.78, 0.26, u_cloud);
  float dens = smoothstep(lo, lo + 0.34, cl) * smoothstep(0.02, 0.18, h) * mix(0.5, 1.0, u_cloud);

  // stars: one candidate per ~3 css px cell, heavy-tailed brightness,
  // washed out near the horizon by light pollution
  float cell = 3.0*u_dpr;
  vec2 ci = floor(gl_FragCoord.xy / cell);
  uvec2 cu = uvec2(ci + 4096.0);
  float r = h2(cu);
  float star = 0.0;
  if (r > 0.9962) {
    vec2 sc = (ci + 0.5 + (vec2(h2(cu+7u), h2(cu+13u))-0.5)*0.7) * cell;
    float d = length(gl_FragCoord.xy - sc) / u_dpr;
    float b = pow(h2(cu+29u), 6.0) * 1.3 + 0.03;
    float tw = 0.75 + 0.25*sin(t*(0.7+2.3*h2(cu+3u)) + r*600.0);
    star = b * tw * exp(-d*d*1.4);
  }
  float md = length(p - u_moon.xy);
  star *= smoothstep(0.10, 0.45, h) * (1.0 - smoothstep(0.0, 0.4, dens*2.2)) * smoothstep(0.05, 0.25, md);
  c += vec3(0.85, 0.9, 1.0) * star;

  // moon: waxing gibbous, faint maria, halo
  vec2 mq = (p - u_moon.xy) / u_moon.z;
  float mr = length(mq);
  if (mr < 1.0) {
    vec3 n = vec3(mq, sqrt(1.0 - mr*mr));
    float lit = smoothstep(-0.12, 0.18, dot(n, normalize(vec3(-0.75, 0.2, 0.55))));
    float mar = 0.62 + 0.38*smoothstep(0.35, 0.65, fbm(mq*1.8 + 3.0));
    float limb = 0.75 + 0.25*n.z;
    float edge = 1.0 - smoothstep(0.96, 1.0, mr);
    c = mix(c, vec3(1.0, 0.96, 0.88) * 0.95 * mar * limb * lit + c*0.6, edge);
  }
  float mh = max(mr - 1.0, 0.0) * u_moon.z;
  c += vec3(0.55, 0.6, 0.75) * (0.06*exp(-mh*40.0) + 0.015*exp(-mh*6.0));

  // cloud colour: underside lit by the city dome, edges silvered near the moon
  float dx = (p.x - u_cityX) / 0.6;
  vec3 cc = vec3(0.011, 0.011, 0.016) * (0.8 + 0.9 * u_cloud) * (1.0 + 2.2*exp(-h*6.0)*(0.4+exp(-dx*dx)));
  cc += vec3(0.12, 0.13, 0.17) * exp(-md*7.0) * (0.4 + 0.6*smoothstep(0.5, 0.75, cl));
  c = mix(c, cc, dens);
  o = vec4(c, 1.0);
}`;

const RECT_VS = /* glsl */ `${COMMON}
layout(location=0) in vec4 a_r;   // xL xR ybotL ytopL (scene units) — vertical-edged trapezoid
layout(location=1) in vec4 a_s;   // ybotR ytopR darkness base-glow
uniform vec2 u_res;
out vec2 v_p; out vec4 v_s;
const vec2 C[6] = vec2[6](vec2(0,0),vec2(1,0),vec2(0,1),vec2(0,1),vec2(1,0),vec2(1,1));
void main(){
  vec2 c = C[gl_VertexID];
  float yb = mix(a_r.z, a_s.x, c.x), yt = mix(a_r.w, a_s.y, c.x);
  vec2 p = vec2(mix(a_r.x, a_r.y, c.x), mix(yb, yt, c.y));
  v_p = p; v_s = vec4(a_s.z, a_s.w, 0.0, 0.0);
  gl_Position = vec4(p * u_res.y / u_res * 2.0 - 1.0, 0.0, 1.0);
}`;

const RECT_FS = /* glsl */ `${COMMON}${SKY_FN}
in vec2 v_p; in vec4 v_s; out vec4 o;
void main(){
  vec3 c = skyBase(vec2(v_p.x, u_hz + 0.03)) * v_s.x;
  float yb = v_p.y - u_hz;
  c += vec3(0.012, 0.009, 0.007) * exp(-yb * 70.0) * v_s.y; // street glow on the base
  o = vec4(c, 1.0);
}`;

const LIGHT_VS = /* glsl */ `${COMMON}
layout(location=0) in vec4 a_rect; // cx cy w h (scene units)
layout(location=1) in vec4 a_p;    // seed kind intensity extra
layout(location=2) in vec4 a_col;  // rgb pOn
uniform vec2 u_res; uniform float u_time; uniform float u_dpr;
out vec2 v_l; out vec2 v_hs; out float v_gr; out float v_ga; out vec3 v_c;
const vec2 C[6] = vec2[6](vec2(-1,-1),vec2(1,-1),vec2(-1,1),vec2(-1,1),vec2(1,-1),vec2(1,1));
// on/off with a short cross-fade between consecutive hashed states
float toggle(uint s, float period, float pOn){
  float t = u_time / period + h1(s ^ 0x5bd1e995u) * 97.0;
  float k = floor(t), f = fract(t);
  float a = step(h1(s*31u + uint(k)), pOn);
  float b = step(h1(s*31u + uint(k) - 1u), pOn);
  return mix(b, a, smoothstep(0.0, 0.012, f));
}
vec3 hsv(float h, float s, float v){ vec3 k=clamp(abs(fract(h+vec3(0,2,1)/3.0)*6.0-3.0)-1.0,0.0,1.0); return v*mix(vec3(1),k,s); }
void main(){
  uint seed = uint(a_p.x); int kind = int(a_p.y + 0.5);
  float I = a_p.z, ex = a_p.w, pOn = a_col.a, t = u_time;
  vec3 col = a_col.rgb;
  float s = 1.0, ga = 0.07, gm = 2.0;
  if (kind == 0) {                       // residential, warm, independent
    s = toggle(seed, 35.0 + 70.0*h1(seed*7u), pOn);
  } else if (kind == 1) {                // office: whole floors + stragglers
    float fl = toggle(uint(ex), 110.0, 0.18) * step(h1(seed ^ 0x9e3779b9u), 0.88);
    s = max(fl, toggle(seed, 75.0, pOn * 0.5));
  } else if (kind == 2) {                // TV flicker
    float n = sin(t*7.1 + ex*40.0)*0.5 + sin(t*3.3 + ex*13.0)*0.3 + sin(t*13.7)*0.2;
    s = toggle(seed, 60.0, 0.65) * (0.6 + 0.4*n);
    col = mix(col, vec3(0.55, 0.72, 1.0), 0.65);
  } else if (kind == 3) {                // steady
    s = 1.0;
  } else if (kind == 4) {                // LED crown / edge strip, slow hue drift + chase
    col = hsv(fract(t*0.012 + ex), 0.65, 1.0);
    s = 0.55 + 0.45*sin(t*0.9 - a_rect.y*400.0 - a_rect.x*120.0);
    ga = 0.22; gm = 3.0;
  } else if (kind == 5) {                // red aviation beacon
    float ph = fract(t/2.2 + ex);
    s = smoothstep(0.0, 0.08, ph) * (1.0 - smoothstep(0.30, 0.55, ph));
    ga = 0.30; gm = 3.5;
  } else if (kind == 6) {                // white strobe
    s = step(fract(t/1.7 + ex), 0.035);
    ga = 0.7; gm = 7.0;
  } else if (kind == 7) {                // CPU-driven (planes, traffic): extra = glow mult
    ga = 0.35; gm = ex;
  } else if (kind == 8) {                // bridge cable LEDs, travelling wave
    s = 0.30 + 0.70*pow(0.5 + 0.5*sin(a_rect.x*70.0 - a_rect.y*90.0 - t*0.55 + ex), 3.0);
    ga = 0.18;
  }
  float e = s * I;
  vec2 hs = max(a_rect.zw * u_res.y * 0.5, vec2(0.45 * u_dpr));
  float gr = max(hs.x, hs.y) * gm + 1.2 * u_dpr;
  vec2 lp = C[gl_VertexID] * (hs + 2.2 * gr);
  v_l = lp; v_hs = hs; v_gr = gr; v_ga = ga; v_c = col * e;
  vec2 pos = a_rect.xy * u_res.y + lp;
  gl_Position = e < 0.003 ? vec4(2.0, 2.0, 2.0, 1.0) : vec4(pos / u_res * 2.0 - 1.0, 0.0, 1.0);
}`;

const LIGHT_FS = /* glsl */ `${COMMON}
in vec2 v_l; in vec2 v_hs; in float v_gr; in float v_ga; in vec3 v_c; out vec4 o;
void main(){
  float d = length(max(abs(v_l) - v_hs, 0.0));
  float core = 1.0 - smoothstep(0.0, 1.0, d);
  float g = exp(-d*d / (v_gr*v_gr));
  o = vec4(v_c * (core + v_ga * g), 1.0);
}`;

const COMP_FS = /* glsl */ `${COMMON}
uniform sampler2D u_scene;
uniform vec2 u_res; uniform float u_time; uniform float u_hz; uniform float u_exposure; uniform float u_fog;
out vec4 o;
// gradient of a sum of dispersive directional waves on the water plane,
// each term faded out once it would alias at this screen row
vec2 waveGrad(vec2 w, float t, float pxPhase){
  vec2 g = vec2(0.0); float norm = 0.0;
  for (int i = 0; i < 7; i++) {
    float fi = float(i);
    float a = fi * 2.39996 + 0.6;
    float ang = 1.5708 + sin(a) * 0.9;
    vec2 D = vec2(cos(ang) * 1.4, sin(ang));
    float f = 4.5 * pow(1.42, fi);
    float sp = sqrt(f) * 0.9;
    float lod = 1.0 - smoothstep(0.7, 2.2, f * pxPhase);
    float ph = dot(D, w) * f + t * sp + fi * 1.7;
    g += D * cos(ph) * lod;
    norm += lod;
  }
  return g / max(sqrt(norm) * 1.4, 1.0);
}
void main(){
  vec2 uv = gl_FragCoord.xy / u_res;
  float aspect = u_res.x / u_res.y;
  vec3 c;
  if (uv.y >= u_hz) {
    c = textureLod(u_scene, uv, 0.0).rgb;
  } else {
    float d0 = u_hz - uv.y;
    vec3 acc = vec3(0.0);
    const int N = 6;
    for (int k = 0; k < N; k++) {
      float jit = (float(k) + 0.5) / float(N) - 0.5;
      float d = max(d0 + jit * 1.3 / u_res.y, 1e-4);
      float z = 1.0 / (d + 0.015);
      vec2 w = vec2((uv.x - 0.5) * aspect * z, z) * 0.9;
      float pxPhase = 0.9 / ((d + 0.015) * (d + 0.015) * u_res.y); // phase/px per unit freq
      vec2 g = waveGrad(w, u_time * 0.9, pxPhase);
      float ax = 0.0006 + d * 0.010;
      float ay = 0.004 + d * 0.11;
      vec2 r = vec2(uv.x + g.x * ax, u_hz + d + g.y * ay);
      r.y = max(r.y, u_hz + 0.6 / u_res.y);
      acc += textureLod(u_scene, r, 0.0).rgb;
    }
    vec3 refl = acc / float(N);
    float fres = mix(0.62, 0.24, smoothstep(0.0, 0.22, d0));
    c = refl * fres + vec3(0.0002, 0.0005, 0.0009);
    // sparkle: brief glints riding the bright reflection columns
    float lum = dot(refl, vec3(0.3, 0.5, 0.2));
    uvec2 gc = uvec2(gl_FragCoord.xy / 2.0) + uvec2(0u, uint(u_time * 9.0) * 7919u);
    c += refl * step(0.992, h2(gc)) * 2.5 * smoothstep(0.02, 0.15, lum) * smoothstep(0.01, 0.12, d0);
  }
  if (u_fog > 0.001) {
    // haze hugs the waterline; lights bloom into it (blurred scene from the mip chain)
    float dy = abs(uv.y - u_hz);
    float band = exp(-dy * mix(22.0, 4.5, u_fog));
    float f = clamp(u_fog * (0.18 + 0.82 * band), 0.0, 0.92);
    float n = 0.85 + 0.3 * sin(uv.x * 9.0 + u_time * 0.05) * sin(uv.x * 23.0 - u_time * 0.031 + uv.y * 30.0);
    vec2 su = vec2(uv.x, uv.y < u_hz ? 2.0 * u_hz - uv.y : uv.y);
    vec3 scat = textureLod(u_scene, su, 4.0).rgb * 0.6 + textureLod(u_scene, su, 6.0).rgb * 0.8;
    vec3 fogc = vec3(0.010, 0.009, 0.013) + scat * 0.9;
    c = mix(c, fogc, f * n);
  }
  c = 1.0 - exp(-c * u_exposure);                    // tonemap
  vec2 vq = uv - 0.5;
  c *= 1.0 - 0.55 * dot(vq, vq) * 1.6;               // vignette
  c = pow(c, vec3(1.0 / 2.2));
  c += (h2(uvec2(gl_FragCoord.xy) + uint(u_time * 60.0) * 3u) - 0.5) / 255.0; // dither + grain
  o = vec4(c, 1.0);
}`;

// ----------------------------------------------------------- city gen ----

type Rng = () => number;
function mulberry32(a: number): Rng {
  return () => {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const L_STRIDE = 12; // floats per light instance
const R_STRIDE = 8;  // floats per rect instance

interface Group { r0: number; rn: number; l0: number; ln: number }
interface City {
  rects: Float32Array; lights: Float32Array; groups: Group[];
  cityX: number; moon: [number, number, number];
  roadGroup: number; // draw index where the waterfront road traffic slots in (behind the near row)
  bridge: { x0: number; x1: number; deckY: (u: number) => number; mirror: boolean; type: BridgeType; side: BridgeSide };
  shore: [number, number]; // x-range of the waterfront road
}

const WARM: [number, number, number] = [1.0, 0.70, 0.40];
const NEUTRAL: [number, number, number] = [1.0, 0.86, 0.66];
const COOL: [number, number, number] = [0.78, 0.88, 1.0];

function genCity(aspect: number, seed: number, bType: BridgeType | 'auto' = 'auto', bSide: BridgeSide | 'auto' = 'auto'): City {
  const R = mulberry32(seed);
  const BR = mulberry32(seed ^ 0x51ed27);
  const type: BridgeType = bType !== 'auto' ? bType : BR() < 0.08 ? 'none' : (['cable', 'suspension', 'arch'] as const)[Math.floor(BR() * 3)];
  const side: BridgeSide = bSide !== 'auto' ? bSide : BR() < 0.5 ? 'left' : 'right';
  const rnd = (a: number, b: number) => a + (b - a) * R();
  const gauss = () => { let s = 0; for (let i = 0; i < 4; i++) s += R(); return (s - 2) / 0.577; };
  const rects: number[] = [];
  const lights: number[] = [];
  const groups: Group[] = [];
  let sid = 1;
  let cur: Group | null = null;
  const begin = () => { cur = { r0: rects.length / R_STRIDE, rn: 0, l0: lights.length / L_STRIDE, ln: 0 }; };
  const end = () => {
    const g = cur!;
    g.rn = rects.length / R_STRIDE - g.r0; g.ln = lights.length / L_STRIDE - g.l0;
    if (g.rn || g.ln) groups.push(g);
    cur = null;
  };
  const rect = (x0: number, y0: number, x1: number, y1: number, dark: number, glow = 1) =>
    rects.push(x0, x1, y0, y1, y0, y1, dark, glow);
  const quad = (xL: number, xR: number, ybL: number, ytL: number, ybR: number, ytR: number, dark: number, glow = 1) =>
    rects.push(xL, xR, ybL, ytL, ybR, ytR, dark, glow);
  const light = (x: number, y: number, w: number, h: number, kind: number, I: number,
    col: readonly number[], pOn = 1, extra = 0, seedv = sid++) =>
    lights.push(x, y, w, h, seedv, kind, I, extra, col[0], col[1], col[2], pOn);
  const tint = (c: readonly number[], v: number) => [
    c[0] * (1 + rnd(-v, v)), c[1] * (1 + rnd(-v, v)), c[2] * (1 + rnd(-v, v))];

  const cityX = aspect * 0.56;
  const city2 = aspect * 0.80;
  const bridgeX1 = type === 'none' ? -0.01 : aspect * ({ cable: 0.22, suspension: 0.27, arch: 0.21 } as const)[type];
  const deckH = type === 'suspension' ? 0.030 : type === 'arch' ? 0.016 : 0.024;
  const deckY = (u: number) => {
    const rise = 1 - smooth(0.80, 1.0, u);
    const crown = type === 'arch' ? 0.0015 : 0.004;
    return HZ + 0.004 + deckH * rise + crown * Math.sin(Math.PI * Math.min(u / 0.8, 1)) * rise;
  };

  // window grid on a box
  type WinOpt = { fh: number; cs: number; I: number; type: 0 | 1 | 2; dens: number };
  const windows = (x0: number, x1: number, y0: number, y1: number, o: WinOpt) => {
    const nF = Math.floor((y1 - y0 - o.fh * 0.6) / o.fh);
    const nC = Math.max(1, Math.floor((x1 - x0) / o.cs) - 1);
    const cs = (x1 - x0) / (nC + 1);
    const base = o.type === 1 ? COOL : o.type === 0 ? (R() < 0.7 ? WARM : NEUTRAL) : NEUTRAL;
    const fseed0 = sid * 7 + 1;
    for (let f = 0; f < nF; f++) {
      const y = y0 + o.fh * (f + 0.75);
      for (let c = 0; c < nC; c++) {
        const x = x0 + cs * (c + 1);
        let kind = o.type === 1 ? 1 : 0;
        if (o.type === 0 && R() < 0.035) kind = 2;
        light(x, y, cs * 0.42, o.fh * 0.40, kind, o.I * rnd(0.6, 1.25), tint(base, 0.08),
          o.dens * rnd(0.6, 1.3), kind === 1 ? fseed0 + f : R() * 10);
      }
    }
  };

  // One-point perspective, eye at the waterline, vanishing point on the horizon
  // at screen centre. A box of depth D shows the side facing the VP; that face
  // recedes toward the VP (vertical edges stay vertical, top edge slopes down).
  const vpx = aspect * 0.40, PD = 0.32;
  const kz = (z: number) => PD / (PD + z);
  const proj = (x: number, y: number, z: number): [number, number] => [vpx + (x - vpx) * kz(z), HZ + (y - HZ) * kz(z)];
  const box = (x0: number, x1: number, y0: number, y1: number, D: number, dark: number, glow = 1) => {
    const xn = (x0 + x1) / 2 < vpx ? x1 : x0;
    const [xf, ybf] = proj(xn, y0, D);
    const [, ytf] = proj(xn, y1, D);
    if (Math.abs(xf - xn) > 0.0002) {
      // right face (left of VP) catches the moon a little; left face is in shadow
      const sd = xn === x1 ? dark * 0.82 : dark * 0.62;
      if (xf > xn) quad(xn, xf, y0, y1, ybf, ytf, sd, glow);
      else quad(xf, xn, ybf, ytf, y0, y1, sd, glow);
    }
    rect(x0, y0, x1, y1, dark, glow);
  };
  const sideWindows = (x0: number, x1: number, y0: number, y1: number, D: number, o: WinOpt) => {
    const xn = (x0 + x1) / 2 < vpx ? x1 : x0;
    const off = Math.abs(xn - vpx);
    if (off * (1 - kz(D)) < 0.0018) return; // face too thin to read
    const nF = Math.floor((y1 - y0 - o.fh * 0.6) / o.fh);
    const nS = Math.max(1, Math.floor(D / o.cs) - 1);
    const base = o.type === 1 ? COOL : (R() < 0.7 ? WARM : NEUTRAL);
    const fseed0 = sid * 7 + 3;
    for (let j = 0; j < nS; j++) {
      const z = D * (j + 1) / (nS + 1);
      const k = kz(z);
      const ws = off * PD / ((PD + z) ** 2) * (D / (nS + 1)) * 0.45;
      for (let f = 0; f < nF; f++) {
        const [x, y] = proj(xn, y0 + o.fh * (f + 0.75), z);
        const kind = o.type === 1 ? 1 : 0;
        light(x, y, ws, o.fh * 0.40 * k, kind, o.I * 0.8 * rnd(0.6, 1.2), tint(base, 0.08),
          o.dens * rnd(0.6, 1.3), kind === 1 ? fseed0 + f : R() * 10);
      }
    }
  };

  // ---- far layer: continuous low skyline, swelling toward downtown
  begin();
  for (let x = -0.01; x < aspect + 0.01;) {
    const w = rnd(0.008, 0.026);
    const g1 = Math.exp(-(((x - cityX) / (aspect * 0.16)) ** 2));
    const g2 = Math.exp(-(((x - city2) / (aspect * 0.07)) ** 2));
    const h = 0.006 + 0.022 * R() + 0.07 * g1 * R() + 0.035 * g2 * R();
    rect(x, HZ, x + w, HZ + h, 0.80, 0.6);
    windows(x, x + w, HZ, HZ + h, { fh: 0.0026, cs: 0.0022, I: 0.22, type: R() < 0.4 ? 1 : 0, dens: rnd(0.08, 0.28) });
    x += w + (R() < 0.15 ? rnd(0.002, 0.01) : 0);
  }
  end();

  // ---- mid layer: towers, each its own group so overlaps occlude correctly
  type Tower = { x: number; w: number; h: number };
  const towers: Tower[] = [];
  const nT = Math.round(34 * aspect / 1.78);
  for (let i = 0; i < nT; i++) {
    const main = R() < 0.72;
    const x = main ? cityX + gauss() * aspect * 0.13 : city2 + gauss() * aspect * 0.06;
    if (x < bridgeX1 + 0.04 || x > aspect - 0.02) { i--; continue; }
    const g = Math.exp(-(((x - (main ? cityX : city2)) / (aspect * (main ? 0.12 : 0.06))) ** 2));
    const h = 0.035 + (main ? 0.27 : 0.13) * g * Math.pow(R(), 0.8) + rnd(0, 0.025);
    towers.push({ x, w: rnd(0.012, 0.032) * (h > 0.15 ? 1.15 : 1), h });
  }
  towers.sort((a, b) => b.h - a.h + (R() - 0.5) * 0.04);
  const byHeight = [...towers].sort((a, b) => b.h - a.h);
  const ledSet = new Set(byHeight.slice(1, 5).filter(() => R() < 0.8));
  const edgeSet = new Set(byHeight.slice(3, 10).filter(() => R() < 0.4));
  const beaconSet = new Set(byHeight.slice(0, 10));
  for (const t of towers) {
    begin();
    const x0 = t.x - t.w / 2, x1 = t.x + t.w / 2, top = HZ + t.h;
    const type: 0 | 1 = R() < 0.6 ? 1 : 0;
    const fh = rnd(0.0036, 0.0050), cs = fh * rnd(0.75, 1.15);
    const dens = R() < 0.15 ? 0.02 : rnd(0.07, 0.30);
    const tiers = t.h > 0.16 && R() < 0.55 ? 3 : t.h > 0.1 && R() < 0.4 ? 2 : 1;
    let yb = HZ, w = t.w;
    let tx0 = x0, tx1 = x1;
    for (let k = 0; k < tiers; k++) {
      const yt = k === tiers - 1 ? top : HZ + t.h * (tiers === 2 ? 0.7 : k === 0 ? 0.6 : 0.85);
      tx0 = t.x - w / 2; tx1 = t.x + w / 2;
      const D = w * rnd(0.7, 1.2);
      const wo: WinOpt = { fh, cs, I: 0.55, type, dens };
      box(tx0, tx1, yb, yt, D, 0.48);
      windows(tx0, tx1, yb + (k === 0 ? fh * 1.4 : 0), yt, wo);
      sideWindows(tx0, tx1, yb + (k === 0 ? fh * 1.4 : 0), yt, D, wo);
      yb = yt; w *= rnd(0.62, 0.8);
    }
    // lobby
    light(t.x, HZ + fh * 0.6, t.w * 0.7, fh * 0.5, 3, 0.35, NEUTRAL);
    const tw = tx1 - tx0;
    if (ledSet.has(t)) {
      const n = Math.max(3, Math.floor(tw / 0.0018));
      for (let i = 0; i <= n; i++) light(tx0 + tw * i / n, top - 0.0012, 0.0010, 0.0010, 4, 0.6, [1, 1, 1], 1, (t.x * 3.7) % 1);
    }
    if (edgeSet.has(t)) {
      for (let y = HZ + 0.004; y < top - 0.002; y += 0.0032) {
        light(x0 + 0.0006, y, 0.0008, 0.0016, 4, 0.35, [1, 1, 1], 1, 0.5);
        light(x1 - 0.0006, y, 0.0008, 0.0016, 4, 0.35, [1, 1, 1], 1, 0.5);
      }
    }
    if (beaconSet.has(t)) {
      if (t === byHeight[0] || (t === byHeight[1] && R() < 0.6)) {
        const sh = rnd(0.02, 0.04);
        rect(t.x - 0.0006, top, t.x + 0.0006, top + sh, 0.48);
        light(t.x, top + sh, 0.0011, 0.0011, 5, 1.5, [1, 0.08, 0.04], 1, 0);
        light(t.x, top + sh * 0.5, 0.0009, 0.0009, 5, 0.9, [1, 0.08, 0.04], 1, 0.0);
        if (t === byHeight[0]) light(t.x, top + 0.001, 0.0014, 0.0014, 6, 5.0, [0.9, 0.95, 1.0], 1, 0.37);
      } else {
        light(tx0 + 0.001, top + 0.0008, 0.0009, 0.0009, 5, 1.1, [1, 0.08, 0.04], 1, 0);
        light(tx1 - 0.001, top + 0.0008, 0.0009, 0.0009, 5, 1.1, [1, 0.08, 0.04], 1, 0);
      }
    }
    end();
  }

  // ---- near layer: waterfront, shore wall, promenade lights
  const roadGroup = groups.length;
  const nearB: [number, number, number][] = [];
  for (let x = bridgeX1 + 0.012; x < aspect;) {
    const w = rnd(0.018, 0.07);
    if (R() < 0.25) { x += w * 0.6; continue; }
    nearB.push([x, w, rnd(0.008, 0.045)]);
    x += w + rnd(0.003, 0.02);
  }
  // paint outermost first so faces receding toward the VP tuck behind inner neighbours
  nearB.sort((a, b) => Math.abs(b[0] + b[1] / 2 - vpx) - Math.abs(a[0] + a[1] / 2 - vpx));
  for (const [x, w, h] of nearB) {
    begin();
    const D = rnd(0.02, 0.05);
    const wo: WinOpt = { fh: 0.0062, cs: 0.0068, I: 0.6, type: R() < 0.25 ? 1 : 0, dens: rnd(0.10, 0.35) };
    box(x, x + w, HZ, HZ + h, D, 0.30, 1.5);
    windows(x, x + w, HZ + 0.0045, HZ + h, wo);
    sideWindows(x, x + w, HZ + 0.0045, HZ + h, D, wo);
    end();
  }
  begin();
  rect(-0.01, HZ - 0.001, aspect + 0.01, HZ + 0.0040, 0.20, 2);
  for (let x = bridgeX1 + 0.006; x < aspect + 0.01; x += rnd(0.008, 0.016)) {
    if (R() < 0.18) { x += rnd(0.01, 0.05); continue; }
    light(x, HZ + 0.0048, 0.0009, 0.0009, 3, rnd(0.35, 0.7), R() < 0.6 ? NEUTRAL : WARM);
  }
  end();

  // ---- bridge: runs off the (pre-mirror) left edge and lands at bridgeX1
  const bx0 = -0.04;
  const bx = (u: number) => bx0 + u * (bridgeX1 - bx0);
  const RED = [1, 0.08, 0.04];
  // lights spaced evenly along a polyline-ish curve given by f(u) -> [x, y]
  const string = (f: (u: number) => [number, number], u0: number, u1: number, gap: number, emit: (x: number, y: number, i: number) => void) => {
    let [px, py] = f(u0), acc = gap, i = 0;
    const steps = 400;
    for (let k = 1; k <= steps; k++) {
      const [x, y] = f(u0 + (u1 - u0) * k / steps);
      acc += Math.hypot(x - px, y - py); px = x; py = y;
      if (acc >= gap) { acc = 0; emit(x, y, i++); }
    }
  };
  if (type !== 'none') {
    begin();
    if (type === 'cable') {
      const pH = HZ + 0.125;
      for (const u of [0.30, 0.66]) {
        const x = bx(u);
        rect(x - 0.0015, HZ, x + 0.0015, pH, 0.45);
        light(x, pH + 0.0008, 0.0010, 0.0010, 5, 1.3, RED, 1, 0.5);
        for (let k = 1; k <= 8; k++) {
          for (const sgn of [-1, 1]) {
            const ue = u + sgn * k * 0.034;
            if (ue < 0 || ue > 0.98) continue;
            const ex = bx(ue), ey = deckY(ue) + 0.002, ty = pH - 0.004 - k * 0.0035;
            string(a => [x + (ex - x) * a, ty + (ey - ty) * a], 0, 1, 0.0045,
              (lx, ly) => light(lx, ly, 0.0006, 0.0006, 8, 0.22, [0.75, 0.9, 1.0], 1, k * 0.4));
          }
        }
      }
    } else if (type === 'suspension') {
      // two floodlit towers, parabolic main cable strung with "necklace" lights, faint suspenders
      const tA = 0.24, tB = 0.70, tY = HZ + 0.15, anchor = 0.93;
      const cable = (u: number): number => {
        const d = deckY(u) + 0.004;
        if (u < tA) { const a = u / tA; return d + (tY - d) * (0.25 + 0.75 * a * a); }   // side span (anchor off-screen)
        if (u <= tB) { const m = (tA + tB) / 2, a = (u - m) / ((tB - tA) / 2); return d + (tY - d) * a * a; }
        const a = Math.max(0, 1 - (u - tB) / (anchor - tB)); return d + (tY - d) * a * a;
      };
      for (const u of [tA, tB]) {
        const x = bx(u);
        rect(x - 0.0021, HZ, x + 0.0021, tY + 0.004, 0.75);
        rect(x - 0.0030, tY - 0.012, x + 0.0030, tY - 0.009, 0.75); // portal strut
        for (let y = HZ + 0.006; y < tY; y += 0.012) light(x, y, 0.0026, 0.0018, 3, 0.10, WARM); // floodlight wash
        light(x, tY + 0.0048, 0.0011, 0.0011, 5, 1.3, RED, 1, 0.2);
        light(x, tY - 0.03, 0.0009, 0.0009, 5, 0.8, RED, 1, 0.2);
      }
      string(u => [bx(u), cable(u)], 0, anchor, 0.0042,
        (x, y) => light(x, y, 0.0008, 0.0008, 3, 0.55, NEUTRAL));
      for (let u = 0.012; u < anchor; u += 0.018) {
        const x = bx(u), y0 = deckY(u) + 0.003, y1 = cable(u);
        for (let y = y0 + 0.004; y < y1 - 0.002; y += 0.004) light(x, y, 0.0004, 0.0004, 3, 0.06, [0.8, 0.85, 1.0]);
      }
    } else {
      // through-arch: twin LED ribbons, dim hangers, stone abutments
      const a0 = 0.10, a1 = 0.84, aH = 0.085;
      const arch = (u: number, off: number) => {
        const t = (u - a0) / (a1 - a0);
        return deckY(u) - 0.006 + (aH + off) * Math.pow(Math.max(Math.sin(Math.PI * t), 0), 0.85);
      };
      for (const u of [a0, a1]) rect(bx(u) - 0.006, HZ, bx(u) + 0.006, deckY(u) - 0.001, 0.5);
      string(u => [bx(u), arch(u, 0)], a0, a1, 0.0034,
        (x, y) => light(x, y, 0.0008, 0.0008, 8, 0.45, [0.85, 0.92, 1.0], 1, 0.0));
      string(u => [bx(u), arch(u, -0.009)], a0 + 0.02, a1 - 0.02, 0.0040,
        (x, y) => light(x, y, 0.0006, 0.0006, 8, 0.25, [0.85, 0.92, 1.0], 1, 1.3));
      for (let u = a0 + 0.04; u < a1 - 0.03; u += 0.028) {
        const x = bx(u), y0 = deckY(u) + 0.003, y1 = arch(u, -0.009);
        for (let y = y0 + 0.004; y < y1 - 0.002; y += 0.0045) light(x, y, 0.0004, 0.0004, 3, 0.07, [0.8, 0.85, 1.0]);
      }
      const top = bx((a0 + a1) / 2);
      light(top, arch((a0 + a1) / 2, 0) + 0.002, 0.0010, 0.0010, 5, 1.1, RED, 1, 0.7);
    }
    for (let u = 0; u <= 1.0; u += 0.0145) {
      const x = bx(u), y = deckY(u);
      light(x, y + 0.0016, 0.0009, 0.0009, 3, 0.9, NEUTRAL);
      light(x, y - 0.0004, 0.0008, 0.0008, 3, 0.5, WARM);
    }
    end();
  }

  // mirror the whole layout (city included) so the bridge lands on the right
  const mirror = side === 'right';
  if (mirror) {
    for (let i = 0; i < rects.length; i += R_STRIDE) {
      const [xL, xR, ybL, ytL, ybR, ytR] = rects.slice(i, i + 6);
      rects[i] = aspect - xR; rects[i + 1] = aspect - xL;
      rects[i + 2] = ybR; rects[i + 3] = ytR; rects[i + 4] = ybL; rects[i + 5] = ytL;
    }
    for (let i = 0; i < lights.length; i += L_STRIDE) lights[i] = aspect - lights[i];
  }
  const shoreA = Math.max(bridgeX1, 0) - 0.01, shoreB = aspect + 0.01;

  return {
    rects: new Float32Array(rects), lights: new Float32Array(lights), groups, roadGroup,
    cityX: mirror ? aspect - cityX : cityX,
    moon: [aspect * (0.62 + 0.30 * R()), 0.72 + 0.16 * R(), 0.026],
    bridge: { x0: bx0, x1: bridgeX1, deckY, mirror, type, side },
    shore: mirror ? [aspect - shoreB, aspect - shoreA] : [shoreA, shoreB],
  };
}

function smooth(a: number, b: number, x: number) {
  const t = Math.min(Math.max((x - a) / (b - a), 0), 1);
  return t * t * (3 - 2 * t);
}

// ------------------------------------------------------------ dynamics ----

interface Plane { x: number; y: number; vx: number; vy: number; span: number; land: boolean; ph: number }
interface Car { u: number; v: number; lane: number }
interface RoadCar { x: number; v: number; I: number }
const ROAD_Y = HZ + 0.0066; // just above the shore wall + promenade lamps

// --------------------------------------------------------------- engine ----

export class HarborNight {
  private gl: WebGL2RenderingContext;
  private opts: Required<HarborNightOptions>;
  private progs!: { sky: WebGLProgram; rect: WebGLProgram; light: WebGLProgram; comp: WebGLProgram };
  private U = new Map<WebGLProgram, Map<string, WebGLUniformLocation | null>>();
  private emptyVao!: WebGLVertexArrayObject;
  private rectVao!: WebGLVertexArrayObject; private rectBuf!: WebGLBuffer;
  private lightVao!: WebGLVertexArrayObject; private lightBuf!: WebGLBuffer;
  private dynVao!: WebGLVertexArrayObject; private dynBuf!: WebGLBuffer;
  private dyn = new Float32Array(L_STRIDE * 512);
  private fbo: WebGLFramebuffer | null = null; private tex: WebGLTexture | null = null;
  private hdr = false;
  private city: City | null = null;
  private w = 0; private h = 0; private dpr = 1;
  private raf = 0; private running = false;
  private t = 0; private last = 0; private lastDraw = 0;
  private planes: Plane[] = []; private nextPlane = 2;
  private cars: Car[] = [];
  private road: RoadCar[] = [];
  private nPlane = 0; private nRoad = 0;
  private rng = mulberry32(12345);
  private ro: ResizeObserver;
  private onVis = () => { if (document.hidden) this.pause(); else if (this.running) this.resume(); };

  constructor(private canvas: HTMLCanvasElement, opts: HarborNightOptions = {}) {
    const given = Object.fromEntries(Object.entries(opts).filter(([, v]) => v !== undefined && !(typeof v === 'number' && Number.isNaN(v))));
    this.opts = { speed: 1, seed: 7, dprCap: 1.5, fps: 0, brightness: 0.5, clouds: 0.4, fog: 0, bridge: 'auto', bridgeSide: 'auto', ...given } as Required<HarborNightOptions>;
    const gl = canvas.getContext('webgl2', { antialias: false, alpha: false, depth: false, powerPreference: 'low-power' });
    if (!gl) throw new Error('harbor-night: WebGL2 unavailable');
    this.gl = gl;
    this.hdr = !!gl.getExtension('EXT_color_buffer_float');
    this.init();
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
    for (let i = 0; i < 12; i++) this.cars.push({ u: this.rng(), v: 0.045 + this.rng() * 0.03, lane: i % 2 });
    // seed the sky so it isn't empty on start
    this.spawnPlane(0.15 + this.rng() * 0.5);
  }

  start() {
    if (this.running) return;
    this.running = true;
    document.addEventListener('visibilitychange', this.onVis);
    this.resize();
    this.resume();
    return this;
  }
  stop() { this.running = false; this.pause(); document.removeEventListener('visibilitychange', this.onVis); }
  destroy() {
    this.stop(); this.ro.disconnect();
    this.gl.getExtension('WEBGL_lose_context')?.loseContext();
  }

  private pause() { cancelAnimationFrame(this.raf); this.raf = 0; }
  private resume() {
    if (this.raf) return;
    this.last = performance.now();
    const loop = (now: number) => {
      this.raf = requestAnimationFrame(loop);
      const minDt = this.opts.fps > 0 ? 1000 / this.opts.fps - 1 : 0;
      if (now - this.lastDraw < minDt) return;
      this.lastDraw = now;
      const dt = Math.min((now - this.last) / 1000, 0.1) * this.opts.speed;
      this.last = now;
      this.t += dt;
      this.step(dt);
      this.draw();
    };
    this.raf = requestAnimationFrame(loop);
  }

  // ---- GL setup
  private compile(vs: string, fs: string) {
    const gl = this.gl;
    const mk = (type: number, src: string) => {
      const s = gl.createShader(type)!;
      gl.shaderSource(s, src); gl.compileShader(s);
      if (!gl.getShaderParameter(s, gl.COMPILE_STATUS)) throw new Error('harbor-night shader: ' + gl.getShaderInfoLog(s));
      return s;
    };
    const p = gl.createProgram()!;
    gl.attachShader(p, mk(gl.VERTEX_SHADER, vs));
    gl.attachShader(p, mk(gl.FRAGMENT_SHADER, fs));
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) throw new Error('harbor-night link: ' + gl.getProgramInfoLog(p));
    this.U.set(p, new Map());
    return p;
  }
  private u(p: WebGLProgram, name: string) {
    const m = this.U.get(p)!;
    if (!m.has(name)) m.set(name, this.gl.getUniformLocation(p, name));
    return m.get(name)!;
  }
  private init() {
    const gl = this.gl;
    this.progs = {
      sky: this.compile(FS_TRI_VS, SKY_FS),
      rect: this.compile(RECT_VS, RECT_FS),
      light: this.compile(LIGHT_VS, LIGHT_FS),
      comp: this.compile(FS_TRI_VS, COMP_FS),
    };
    this.emptyVao = gl.createVertexArray()!;
    const inst = (buf: WebGLBuffer, stride: number, sizes: number[]) => {
      const vao = gl.createVertexArray()!;
      gl.bindVertexArray(vao); gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      let off = 0;
      sizes.forEach((n, i) => {
        gl.enableVertexAttribArray(i);
        gl.vertexAttribPointer(i, n, gl.FLOAT, false, stride * 4, off * 4);
        gl.vertexAttribDivisor(i, 1);
        off += n;
      });
      gl.bindVertexArray(null);
      return vao;
    };
    this.rectBuf = gl.createBuffer()!; this.rectVao = inst(this.rectBuf, R_STRIDE, [4, 4]);
    this.lightBuf = gl.createBuffer()!; this.lightVao = inst(this.lightBuf, L_STRIDE, [4, 4, 4]);
    this.dynBuf = gl.createBuffer()!; this.dynVao = inst(this.dynBuf, L_STRIDE, [4, 4, 4]);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.dynBuf);
    gl.bufferData(gl.ARRAY_BUFFER, this.dyn.byteLength, gl.DYNAMIC_DRAW);
  }

  private resize() {
    const gl = this.gl;
    this.dpr = Math.min(window.devicePixelRatio || 1, this.opts.dprCap);
    const w = Math.max(1, Math.round(this.canvas.clientWidth * this.dpr));
    const h = Math.max(1, Math.round(this.canvas.clientHeight * this.dpr));
    if (w === this.w && h === this.h && this.city) return;
    const aspectChanged = !this.city || Math.abs(w / h - this.w / this.h) > 1e-3;
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
    gl.generateMipmap(gl.TEXTURE_2D);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    this.fbo = gl.createFramebuffer()!;
    gl.bindFramebuffer(gl.FRAMEBUFFER, this.fbo);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, this.tex, 0);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    if (aspectChanged) this.regen();
    if (!this.running) this.draw();
  }
  private regen() {
    const gl = this.gl;
    this.city = genCity(this.w / this.h, this.opts.seed, this.opts.bridge, this.opts.bridgeSide);
    this.spawnRoad();
    gl.bindBuffer(gl.ARRAY_BUFFER, this.rectBuf);
    gl.bufferData(gl.ARRAY_BUFFER, this.city.rects, gl.STATIC_DRAW);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.lightBuf);
    gl.bufferData(gl.ARRAY_BUFFER, this.city.lights, gl.STATIC_DRAW);
  }

  // ---- live controls
  get settings() {
    const o = this.opts;
    return { brightness: o.brightness, clouds: o.clouds, fog: o.fog, seed: o.seed, bridge: o.bridge, bridgeSide: o.bridgeSide };
  }
  /** Bridge style/side; 'auto' derives it from the seed. Rebuilds the city. */
  setBridge(type: BridgeType | 'auto' = this.opts.bridge, side: BridgeSide | 'auto' = this.opts.bridgeSide) {
    this.opts.bridge = type; this.opts.bridgeSide = side;
    if (this.w) this.regen();
    if (!this.running) this.draw();
  }
  set(p: Partial<Pick<Required<HarborNightOptions>, 'brightness' | 'clouds' | 'fog' | 'speed'>>) {
    for (const [k, v] of Object.entries(p)) if (typeof v === 'number' && Number.isFinite(v)) (this.opts as any)[k] = v;
    if (!this.running) this.draw();
  }
  /** New city layout + moon position. Returns the seed used. */
  randomize(seed = (Math.random() * 2 ** 31) | 0) {
    this.opts.seed = seed;
    if (this.w) this.regen();
    this.planes = []; this.nextPlane = 3;
    if (!this.running) this.draw();
    return seed;
  }

  private spawnRoad() {
    const R = this.rng, [x0, x1] = this.city!.shore;
    const n = Math.round(30 * (x1 - x0) / 1.4);
    this.road = [];
    for (let i = 0; i < n; i++) {
      const dir = i % 2 ? 1 : -1;
      this.road.push({ x: x0 + R() * (x1 - x0), v: dir * (0.006 + R() * 0.008), I: 0.7 + R() * 0.6 });
    }
  }

  // ---- simulation (planes + traffic only; everything else is shader-time)
  private spawnPlane(x01?: number) {
    const R = this.rng, aspect = this.w && this.h ? this.w / this.h : 1.78;
    const land = R() < 0.4;
    const dir = land ? 1 : R() < 0.5 ? 1 : -1;
    const span = land ? 0.0035 + R() * 0.0015 : 0.0022 + R() * 0.0015;
    const speed = land ? 0.010 + R() * 0.006 : 0.012 + R() * 0.012;
    const y = land ? HZ + 0.20 + R() * 0.16 : HZ + 0.28 + R() * 0.32;
    const x = x01 !== undefined ? x01 * aspect : dir > 0 ? -0.05 : aspect + 0.05;
    this.planes.push({ x, y, vx: dir * speed, vy: land ? -0.0011 - R() * 0.0008 : (R() - 0.5) * 0.0006, span, land, ph: R() });
  }
  private step(dt: number) {
    const aspect = this.w / this.h;
    this.nextPlane -= dt;
    if (this.nextPlane <= 0 && this.planes.length < 2) { this.spawnPlane(); this.nextPlane = 20 + this.rng() * 40; }
    for (const p of this.planes) { p.x += p.vx * dt; p.y += p.vy * dt; }
    this.planes = this.planes.filter(p => p.x > -0.08 && p.x < aspect + 0.08 && p.y > HZ + 0.05);
    for (const c of this.cars) { c.u += (c.lane ? -1 : 1) * c.v * dt * 0.25; c.u = ((c.u % 1) + 1) % 1; }
    if (this.city) {
      const [x0, x1] = this.city.shore, span = x1 - x0;
      for (const c of this.road) { c.x += c.v * dt; if (c.x > x1) c.x -= span; else if (c.x < x0) c.x += span; }
    }
  }
  private buildDynamic(): void {
    const d = this.dyn; let n = 0; const t = this.t;
    const push = (x: number, y: number, s: number, I: number, col: readonly number[], glow: number) => {
      if (n >= 512 || I <= 0) return;
      d.set([x, y, s, s, 0, 7, I, glow, col[0], col[1], col[2], 1], n++ * L_STRIDE);
    };
    for (const p of this.planes) {
      const dir = Math.sign(p.vx), hs = p.span / 2;
      const strobe = ((t / 1.25 + p.ph) % 1);
      const sOn = strobe < 0.03 || (strobe > 0.10 && strobe < 0.13) ? 1 : 0;
      const bcn = Math.max(0, Math.sin(((t + p.ph * 3) % 1.1) / 1.1 * Math.PI)) ** 6;
      push(p.x - hs, p.y, 0.0005, 0.22, [1, 0.12, 0.08], 1.2);
      push(p.x + hs, p.y, 0.0005, 0.22, [0.15, 1, 0.35], 1.2);
      push(p.x - dir * hs * 0.85, p.y + 0.0004, 0.0005, 0.14, [1, 1, 1], 1.0);
      push(p.x, p.y + 0.0005, 0.0005, 0.45 * bcn, [1, 0.1, 0.06], 1.6);
      push(p.x - hs, p.y, 0.0006, 1.1 * sOn, [0.92, 0.96, 1], 2.0);
      push(p.x + hs, p.y, 0.0006, 1.1 * sOn, [0.92, 0.96, 1], 2.0);
      if (p.land) push(p.x + dir * hs * 0.5, p.y - 0.0003, 0.0006, 0.5, [1, 0.95, 0.85], 2.0);
    }
    if (this.city) {
      const b = this.city.bridge;
      for (const c of this.cars) {
        if (c.u > 0.985) continue;
        if (b.type === 'none') break;
        const xl = b.x0 + c.u * (b.x1 - b.x0), x = b.mirror ? this.w / this.h - xl : xl;
        const y = b.deckY(c.u) + 0.0007 + c.lane * 0.0005;
        push(x, y, 0.0008, c.lane ? 0.9 : 1.3, c.lane ? [1, 0.12, 0.06] : [1, 0.9, 0.7], 3);
      }
    }
    this.nPlane = n;
    // waterfront road: dim headlight leading, taillight trailing, soft glow
    for (const c of this.road) {
      const d = Math.sign(c.v);
      push(c.x + d * 0.0024, ROAD_Y, 0.0007, 0.30 * c.I, [1, 0.92, 0.8], 3.5);
      push(c.x - d * 0.0024, ROAD_Y - 0.0001, 0.0006, 0.22 * c.I, [1, 0.10, 0.05], 3.0);
    }
    this.nRoad = n - this.nPlane;
  }

  // ---- render
  private draw() {
    const gl = this.gl, P = this.progs, c = this.city;
    if (!c || !this.fbo) return;
    const res: [number, number] = [this.w, this.h];
    gl.bindFramebuffer(gl.FRAMEBUFFER, this.fbo);
    gl.viewport(0, 0, this.w, this.h);
    gl.disable(gl.BLEND);

    // sky
    gl.useProgram(P.sky);
    gl.uniform2f(this.u(P.sky, 'u_res'), ...res);
    gl.uniform1f(this.u(P.sky, 'u_time'), this.t);
    gl.uniform1f(this.u(P.sky, 'u_dpr'), this.dpr);
    gl.uniform1f(this.u(P.sky, 'u_hz'), HZ);
    gl.uniform1f(this.u(P.sky, 'u_cityX'), c.cityX);
    gl.uniform3f(this.u(P.sky, 'u_moon'), ...c.moon);
    gl.uniform1f(this.u(P.sky, 'u_cloud'), this.opts.clouds);
    gl.bindVertexArray(this.emptyVao);
    gl.drawArrays(gl.TRIANGLES, 0, 3);

    // light program uniforms (shared by planes + windows)
    gl.useProgram(P.light);
    gl.uniform2f(this.u(P.light, 'u_res'), ...res);
    gl.uniform1f(this.u(P.light, 'u_time'), this.t);
    gl.uniform1f(this.u(P.light, 'u_dpr'), this.dpr);
    gl.useProgram(P.rect);
    gl.uniform2f(this.u(P.rect, 'u_res'), ...res);
    gl.uniform1f(this.u(P.rect, 'u_hz'), HZ);
    gl.uniform1f(this.u(P.rect, 'u_cityX'), c.cityX);

    // planes (behind towers)
    this.buildDynamic();
    gl.useProgram(P.light);
    gl.enable(gl.BLEND); gl.blendFunc(gl.ONE, gl.ONE);
    gl.bindBuffer(gl.ARRAY_BUFFER, this.dynBuf);
    gl.bufferSubData(gl.ARRAY_BUFFER, 0, this.dyn, 0, (this.nPlane + this.nRoad) * L_STRIDE);
    gl.bindVertexArray(this.dynVao);
    this.rebase(this.dynVao, this.dynBuf, L_STRIDE, [4, 4, 4], 0);
    if (this.nPlane) gl.drawArraysInstanced(gl.TRIANGLES, 0, 6, this.nPlane);

    // city, back to front, silhouettes then their lights
    c.groups.forEach((g, gi) => {
      if (gi === c.roadGroup && this.nRoad) {
        gl.enable(gl.BLEND);
        gl.useProgram(P.light);
        gl.bindVertexArray(this.dynVao);
        this.rebase(this.dynVao, this.dynBuf, L_STRIDE, [4, 4, 4], this.nPlane);
        gl.drawArraysInstanced(gl.TRIANGLES, 0, 6, this.nRoad);
      }
      if (g.rn) {
        gl.disable(gl.BLEND);
        gl.useProgram(P.rect);
        gl.bindVertexArray(this.rectVao);
        this.rebase(this.rectVao, this.rectBuf, R_STRIDE, [4, 4], g.r0);
        gl.drawArraysInstanced(gl.TRIANGLES, 0, 6, g.rn);
      }
      if (g.ln) {
        gl.enable(gl.BLEND);
        gl.useProgram(P.light);
        gl.bindVertexArray(this.lightVao);
        this.rebase(this.lightVao, this.lightBuf, L_STRIDE, [4, 4, 4], g.l0);
        gl.drawArraysInstanced(gl.TRIANGLES, 0, 6, g.ln);
      }
    });
    gl.disable(gl.BLEND);

    // composite to screen
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.useProgram(P.comp);
    gl.activeTexture(gl.TEXTURE0); gl.bindTexture(gl.TEXTURE_2D, this.tex);
    if (this.opts.fog > 0.001) gl.generateMipmap(gl.TEXTURE_2D);
    gl.uniform1i(this.u(P.comp, 'u_scene'), 0);
    gl.uniform2f(this.u(P.comp, 'u_res'), ...res);
    gl.uniform1f(this.u(P.comp, 'u_time'), this.t);
    gl.uniform1f(this.u(P.comp, 'u_hz'), HZ);
    gl.uniform1f(this.u(P.comp, 'u_exposure'), 0.72 * Math.pow(4, this.opts.brightness - 0.5));
    gl.uniform1f(this.u(P.comp, 'u_fog'), this.opts.fog);
    gl.bindVertexArray(this.emptyVao);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  }
  // WebGL2 has no baseInstance; re-point the instanced attributes at the group start.
  private rebase(_vao: WebGLVertexArrayObject, buf: WebGLBuffer, stride: number, sizes: number[], first: number) {
    const gl = this.gl;
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    let off = 0;
    sizes.forEach((n, i) => {
      gl.vertexAttribPointer(i, n, gl.FLOAT, false, stride * 4, (first * stride + off) * 4);
      off += n;
    });
  }
}
