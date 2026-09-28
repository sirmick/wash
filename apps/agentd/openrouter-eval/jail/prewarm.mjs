// Run at image build: one OpenCode session through to a prompt, with a dummy
// key, so everything OpenCode fetches on first use (the models.dev catalogue,
// its plugin and provider packages) is fetched now, while the build has a
// network, and a run needs nothing but OpenRouter. The prompt itself fails
// (the key is not real); that is expected.
import { spawn } from 'node:child_process';

const oc = spawn('opencode', ['acp'], { env: { ...process.env, OPENROUTER_API_KEY: 'sk-or-v1-prewarm' }, stdio: ['pipe', 'pipe', 'inherit'] });
let buf = '';
const waiting = new Map();
oc.stdout.on('data', d => {
  buf += d;
  let i;
  while ((i = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, i);
    buf = buf.slice(i + 1);
    try {
      const m = JSON.parse(line);
      if (m.id !== undefined && waiting.has(m.id)) { waiting.get(m.id)(m); waiting.delete(m.id); }
    } catch { /* not a reply */ }
  }
});
let next = 1;
const call = (method, params) => new Promise(resolve => {
  const id = next++;
  waiting.set(id, resolve);
  oc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
});

const timer = setTimeout(() => { console.error('prewarm: timed out'); oc.kill(); process.exit(0); }, 120_000);
await call('initialize', { protocolVersion: 1, clientCapabilities: {} });
const s = await call('session/new', { cwd: process.cwd(), mcpServers: [] });
const sessionId = s.result?.sessionId;
const model = (s.result?.configOptions ?? []).find(o => o.category === 'model');
console.log('prewarm: session', sessionId, 'models offered', model?.options?.length ?? 0);
if (model) await call('session/set_config_option', { sessionId, configId: model.id, value: 'openrouter/z-ai/glm-5.3-flash' });
const p = await call('session/prompt', { sessionId, prompt: [{ type: 'text', text: 'hi' }] });
console.log('prewarm: prompt ended', JSON.stringify(p.error ?? p.result).slice(0, 200));
clearTimeout(timer);
oc.kill();
