// Runs `golem executor --stdio` as a child process. Test and acceptance tooling,
// not the runtime path: the Go program owns execution when Golem serves.
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { resolve } from 'node:path';
import { randomUUID } from 'node:crypto';
import type { CommandSpec, CommandResult } from './state.ts';
import { Unavailable, commandEnvironment, type Chunk, type Executor } from './executor.ts';

export class StdioExecutor implements Executor {
  private child: ChildProcessWithoutNullStreams;
  private pending = new Map<string, { resolve: (value: unknown) => void; reject: (err: Error) => void }>();
  private ended = false;
  private exit: Promise<void>;
  readonly ready: Promise<void>;
  constructor(workspace: string, state: string, binary = resolve('..', 'bin', 'golem')) {
    this.child = spawn(binary, ['executor', '--stdio', '--workspace', resolve(workspace), '--state', resolve(state)], { env: commandEnvironment(), stdio: 'pipe' });
    let readyResolve!: () => void, readyReject!: (err: Error) => void;
    this.ready = new Promise((yes, no) => { readyResolve = yes; readyReject = no; });
    const fail = () => { this.ended = true; readyReject(new Error('Execution endpoint could not start. Run make build and check that the state directory is not already in use.')); for (const p of this.pending.values()) p.reject(new Unavailable()); this.pending.clear(); };
    let buffer = '';
    this.child.stdout.setEncoding('utf8');
    this.child.stdout.on('data', (chunk: string) => {
      buffer += chunk;
      let newline: number;
      while ((newline = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
        try {
          const message = JSON.parse(line);
          if (message.ready) { readyResolve(); continue; }
          const p = this.pending.get(message.id); if (!p) continue; this.pending.delete(message.id);
          if (message.error) p.reject(message.error.code === 'unavailable' ? new Unavailable() : new Error(message.error.message));
          else p.resolve(message.result);
        } catch { fail(); this.child.kill('SIGTERM'); }
      }
    });
    // Endpoint diagnostics never enter model context or channel messages.
    this.child.stderr.on('data', () => {});
    this.child.once('error', fail);
    this.exit = new Promise(resolveExit => this.child.once('close', () => { fail(); resolveExit(); }));
  }
  private async call<T>(op: string, args: object, signal?: AbortSignal): Promise<T> {
    await this.ready;
    if (this.ended || signal?.aborted) throw new Unavailable();
    const id = randomUUID();
    return new Promise<T>((resolveCall, rejectCall) => {
      const timer = setTimeout(() => finish(new Unavailable()), 15_000);
      const abort = () => finish(new Unavailable());
      const finish = (error?: Error, result?: unknown) => {
        clearTimeout(timer); signal?.removeEventListener('abort', abort); this.pending.delete(id);
        if (error) rejectCall(error); else resolveCall(result as T);
      };
      this.pending.set(id, { resolve: result => finish(undefined, result), reject: finish });
      signal?.addEventListener('abort', abort, { once: true });
      this.child.stdin.write(`${JSON.stringify({ id, op, ...args })}\n`, err => { if (err) finish(new Unavailable()); });
    });
  }
  start(spec: CommandSpec, signal?: AbortSignal) { return this.call<CommandResult>('start', { spec }, signal); }
  poll(job: string, signal?: AbortSignal) { return this.call<CommandResult>('poll', { job }, signal); }
  async stop(job: string, signal?: AbortSignal) { await this.call('stop', { job }, signal); }
  output(job: string, offset: number, limit: number, signal?: AbortSignal) { return this.call<Chunk>('output', { job, offset, limit }, signal); }
  async close() { this.child.stdin.end(); await this.exit; }
}
