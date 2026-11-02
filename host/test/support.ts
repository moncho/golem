import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createModels, type TranscriptContext, type JsonObject } from '@earendil-works/pi-ai';
import { fauxProvider, fauxAssistantMessage, fauxToolCall, type FauxResponseStep } from '@earendil-works/pi-ai/providers/faux';
import { Golem, type Options } from '../src/golem.ts';
import { Unavailable, type Executor } from '../src/executor.ts';
import { StdioExecutor } from '../src/stdio.ts';
import { Peer, ProtocolError, type Handler } from '../src/protocol.ts';
import type { Event } from '../src/state.ts';

export const text = (value = 'Waiting for the next event.') => fauxAssistantMessage(value);
export const call = (name: string, args: JsonObject) => fauxAssistantMessage(fauxToolCall(name, args), { stopReason: 'toolUse' });
export function events(request: TranscriptContext): (Event & { event: number })[] {
  return request.messages.filter(message => message.role === 'user').flatMap(message => {
    const contents = typeof message.content === 'string' ? message.content : message.content.filter(block => block.type === 'text').map(block => block.text).join('\n');
    try { const value = JSON.parse(contents); return typeof value.event === 'number' ? [value] : []; } catch { return []; }
  });
}
export function role(request: TranscriptContext): string {
  const serialized = JSON.stringify(request.messages.filter(message => message.role === 'system'));
  if (serialized.includes('You are a Worker')) return 'worker';
  if (serialized.includes('You are the Watchdog')) return 'watchdog';
  return 'router';
}
export async function waitFor<T>(read: () => Promise<T>, predicate: (value: T) => boolean, ms = 8000): Promise<T> {
  const until = Date.now() + ms;
  while (Date.now() < until) { const value = await read(); if (predicate(value)) return value; await new Promise(resolve => setTimeout(resolve, 15)); }
  throw new Error('Condition did not become true before the deadline.');
}
export async function fixture(response: FauxResponseStep, options: Partial<Options> = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'golem-pi-test-'));
  const executor = new StdioExecutor(dir, join(dir, 'execution')); await executor.ready;
  const models = createModels(), faux = fauxProvider({ tokensPerSecond: 0 }); models.setProvider(faux.provider);
  faux.setResponses(Array.from({ length: 150 }, () => response));
  const sent: { thread: string; text: string; id: string }[] = [];
  const settings: Options = { stateDir: join(dir, 'controller'), models, model: { provider: 'faux', modelId: 'faux-1' }, environments: { local: executor },
    send: async (thread, text, id) => { sent.push({ thread: thread.id, text, id }); }, pollMs: 25, ...options };
  let app = await Golem.open(settings);
  return { get app() { return app; }, dir, executor, settings, sent, faux,
    async restart() { await app.close(); app = await Golem.open(settings); return app; },
    async close() { await app.close(); await executor.close(); await rm(dir, { recursive: true, force: true }); },
  };
}
export const input = (key: string, text: string, topic = 'main') => ({ key, text, thread: { id: `console:${topic}`, source: 'console' as const, channel: '', replyTo: '' }, sender: 'owner', explicit: true });

// Drives `node src/host.ts serve` over its stdio the way the Go program does.
export class TestPeer {
  readonly child: ChildProcessWithoutNullStreams;
  readonly logs: { level: string; message: string }[] = [];
  private exit: Promise<number | null>;
  stderr = '';
  private peer: Peer;
  constructor() {
    this.child = spawn(process.execPath, ['src/host.ts', 'serve'], { stdio: 'pipe' });
    this.peer = new Peer(this.child.stdout, this.child.stdin);
    this.child.stderr.setEncoding('utf8'); this.child.stderr.on('data', (chunk: string) => { this.stderr += chunk; });
    this.exit = new Promise(resolve => this.child.once('exit', code => resolve(code)));
    this.handle('log', async params => { this.logs.push(params); return null; });
    void this.peer.run();
  }
  handle(method: string, handler: Handler) { this.peer.handle(method, handler); }
  call(method: string, params: unknown = {}): Promise<any> { return this.peer.call(method, params); }
  // Answers exec.* from local executors, reporting an unavailable endpoint the way the Go side does.
  serveExecutors(executors: Record<string, Executor>) {
    const run = async <T>(environment: string, work: (executor: Executor) => Promise<T>) => {
      try { return await work(executors[environment]); } catch (error) { if (error instanceof Unavailable) throw new ProtocolError('unavailable', error.message); throw error; }
    };
    this.handle('exec.start', async p => run(p.environment, e => e.start(p.spec)));
    this.handle('exec.poll', async p => run(p.environment, e => e.poll(p.id)));
    this.handle('exec.stop', async p => run(p.environment, async e => { await e.stop(p.id); return null; }));
    this.handle('exec.output', async p => run(p.environment, e => e.output(p.id, p.offset, p.limit)));
  }
  // Bounded so a host that never exits fails the test and still reaches kill() in finally.
  async exited(ms = 10000) {
    let timer!: NodeJS.Timeout;
    const late = new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(`Host did not exit within ${ms} ms.`)), ms); });
    try { return await Promise.race([this.exit, late]); } finally { clearTimeout(timer); }
  }
  async end() { this.child.stdin.end(); return this.exited(); }
  kill() { if (this.child.exitCode === null && this.child.signalCode === null) this.child.kill('SIGKILL'); }
}
export const init = (state: string, extra: Record<string, unknown> = {}) => ({ state, model: { provider: 'faux', modelId: 'faux-1' }, proactive: 'off',
  channels: [], users: [], bots: [], environments: ['local'], principles: false, memory: false, pollMs: 25, ...extra });
