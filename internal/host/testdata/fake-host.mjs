import { createInterface } from 'node:readline';
import { randomUUID } from 'node:crypto';

const pending = new Map();
const send = (m) => process.stdout.write(JSON.stringify(m) + '\n');
const rl = createInterface({ input: process.stdin });

const methods = {
  echo: (params) => new Promise((resolve) => setTimeout(() => resolve(params), params?.delayMs ?? 0)),
  fail: () => Promise.reject({ code: 'rejected', message: 'nope' }),
  callback: () => new Promise((resolve) => {
    const id = randomUUID();
    pending.set(id, resolve);
    send({ id, method: 'log', params: { level: 'info', message: 'hi' } });
  }),
  stray: async () => { process.stdout.write('stray output\n'); return null; },
  env: async () => Object.keys(process.env),
};

rl.on('line', async (line) => {
  let m;
  try { m = JSON.parse(line); } catch { return; }
  if (!m || typeof m.id !== 'string') return;
  if (typeof m.method !== 'string') {
    const resolve = pending.get(m.id);
    if (resolve) { pending.delete(m.id); resolve(m.result); }
    return;
  }
  if (m.method === 'bye') { process.stdout.write(JSON.stringify({ id: m.id, result: 'bye'.padEnd(8_000_000, 'x') }) + '\n', () => process.exit(0)); return; }
  const h = methods[m.method];
  if (!h) return send({ id: m.id, error: { code: 'invalid', message: 'Unknown method.' } });
  try { send({ id: m.id, result: (await h(m.params)) ?? null }); }
  catch (e) { send({ id: m.id, error: { code: e.code ?? 'internal', message: e.message ?? 'failed' } }); }
});
rl.on('close', () => process.exit(0));
