// The Pi application behind the Go program: `serve` speaks JSON Lines on stdio; `status` reads state once.
import { parseArgs, format } from 'node:util';
import { resolve, join } from 'node:path';
import { access } from 'node:fs/promises';
import { createModels } from '@earendil-works/pi-ai';
import { Harness, createRegistry } from '@earendil-works/pi-durable';
import { openNodeSqliteStorage } from '@earendil-works/pi-durable/storage/sqlite/node';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';
import { Golem, Rejected, Undeliverable, type Options } from './golem.ts';
import { PeerExecutor } from './executor.ts';
import { configuredModels, type ModelParams } from './models.ts';
import { Peer, ProtocolError, type Handler } from './protocol.ts';
import { status } from './status.ts';

type Init = { state: string; model: ModelParams; proactive: 'off' | 'observe' | 'act'; channels: string[]; users: string[]; bots: string[];
  environments: string[]; principles: boolean; memory: boolean } & Partial<Record<typeof tuning[number], number>>;
const tuning = ['maxWorkers', 'pollMs', 'watchMs', 'auditMs', 'deliveryAttempts', 'retainMs', 'maxEventKeys', 'maxArchive'] as const;
// Calls that wait on Go's own bounded work (a Slack post up to 20 s, a principles
// refresh of up to two 10 s Herma runs) must not time out before Go does: a send
// retried after a late success would post the reply twice.
const slowCallMs = 30_000;
const strings = (value: unknown) => Array.isArray(value) && value.every(item => typeof item === 'string');

async function open(peer: Peer, params: Init): Promise<Golem> {
  if (!params || typeof params.state !== 'string' || !params.state || !params.model || typeof params.model.provider !== 'string' || typeof params.model.modelId !== 'string'
    || !['off', 'observe', 'act'].includes(params.proactive) || ![params.channels, params.users, params.bots, params.environments].every(strings)) throw new ProtocolError('invalid', 'init parameters are incomplete.');
  const models = await configuredModels(params.model).catch(error => { throw new ProtocolError('invalid', (error as Error).message); });
  const options: Options = { stateDir: resolve(params.state), models: models.models, model: models.model, routerModel: models.routerModel,
    proactive: params.proactive, channels: params.channels, users: params.users, bots: params.bots,
    environments: Object.fromEntries(params.environments.map(name => [name, new PeerExecutor(peer, name)])),
    send: async (thread, text, id) => {
      try { await peer.call('send', { thread, text, id }, slowCallMs); } catch (error) {
        if (error instanceof ProtocolError && error.code === 'undeliverable') throw new Undeliverable(error.message);
        throw new Error('Channel delivery failed.');
      }
    },
    behavior: params.principles ? () => peer.call('principles', {}, slowCallMs) : undefined,
    memory: params.memory ? { recall: query => peer.call('memory.recall', { query }, slowCallMs), get: id => peer.call('memory.get', { id }, slowCallMs),
      propose: (title, body, source, requestId) => peer.call('memory.propose', { title, body, source, requestId }, slowCallMs) } : undefined };
  for (const key of tuning) if (typeof params[key] === 'number') options[key] = params[key];
  options.onEvent = (name, fields, level = 'info') => { peer.call('log', { level, message: name, fields }).catch(() => {}); };
  try { return await Golem.open(options); } catch (error) {
    console.error('Golem: Pi state could not be opened:', error);
    throw new ProtocolError('invalid', 'Pi state could not be opened.');
  }
}

async function serve() {
  const peer = new Peer(process.stdin, process.stdout);
  let app: Golem | undefined, opening = false, closing: Promise<void> | undefined;
  const close = () => closing ??= (async () => { await app?.close(); })();
  const exit = () => process.stdout.write('', () => process.exit(0));
  const method = (name: string, run: (app: Golem, params: any) => Promise<unknown>) => peer.handle(name, async params => {
    if (!app) throw new ProtocolError('invalid', 'init must be the first message.');
    try { return await run(app, params ?? {}); } catch (error) { throw error instanceof Rejected ? new ProtocolError('rejected', error.message) : error; }
  });
  const init: Handler = async params => {
    if (app || opening) throw new ProtocolError('invalid', 'init must be the first message.');
    opening = true;
    try { app = await open(peer, params); } finally { opening = false; }
    // stdout carries the protocol; diagnostics travel to the Go program's log.
    console.error = (...args: unknown[]) => { peer.call('log', { level: 'error', message: format(...args) }).catch(() => {}); };
    return { ok: true };
  };
  peer.handle('init', init);
  method('admit', (app, params) => app.admit(params));
  method('status', app => status(app.harness));
  method('search', (app, { query }) => app.searchWork(String(query ?? '')));
  method('inspect', (app, { work }) => app.inspectWork(String(work ?? '')));
  method('history', (app, { thread, before, after }) => app.history(String(thread ?? ''), { before, after }));
  method('stop', async (app, { thread, channel, sender }) => ({ stopped: await app.stopThread(String(thread ?? ''), String(channel ?? ''), String(sender ?? '')) }));
  method('resume', (app, { work, text, thread, key }) => app.resumeWork(String(work ?? ''), String(text ?? ''), String(thread ?? ''), String(key ?? '')));
  // The answer is written before exit; exit waits for it to drain.
  method('close', async () => { await close(); setImmediate(exit); return { ok: true }; });
  await peer.run();
  await close(); exit();
}

async function statusMode(state: string) {
  const path = join(resolve(state), 'golem.sqlite');
  try { await access(path); } catch { console.error('No Pi state exists at the selected directory.'); process.exitCode = 1; return; }
  try {
    const harness = await Harness.open(await openNodeSqliteStorage(path), { models: createModels(), registry: createRegistry(), readOnly: true }, BACKGROUND_CONTEXT);
    try { console.log(JSON.stringify(await status(harness), null, 2)); } finally { await harness.close(BACKGROUND_CONTEXT); }
  } catch { console.error('Pi state could not be read.'); process.exitCode = 1; }
}

const { values, positionals } = parseArgs({ allowPositionals: true, options: { state: { type: 'string' } } });
if (positionals[0] === 'serve' && positionals.length === 1) await serve();
else if (positionals[0] === 'status' && positionals.length === 1 && values.state) await statusMode(values.state);
else { console.error('Usage: host serve | host status --state DIR'); process.exitCode = 2; }
