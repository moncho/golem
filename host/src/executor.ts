import type { CommandSpec, CommandResult } from './state.ts';
import { ProtocolError, type Peer } from './protocol.ts';

export class Unavailable extends Error { constructor() { super('Execution environment unavailable.'); } }
// bytes counts log bytes consumed; the next offset is offset + bytes even when a split multibyte character was replaced.
export type Chunk = { output: string; bytes: number };
export interface Executor {
  start(spec: CommandSpec, signal?: AbortSignal): Promise<CommandResult>;
  poll(id: string, signal?: AbortSignal): Promise<CommandResult>;
  stop(id: string, signal?: AbortSignal): Promise<void>;
  output(id: string, offset: number, limit: number, signal?: AbortSignal): Promise<Chunk>;
}
export function commandEnvironment(): NodeJS.ProcessEnv {
  return Object.fromEntries(['PATH', 'HOME', 'USER', 'SHELL', 'TMPDIR', 'LANG', 'LC_ALL', 'TERM']
    .filter(key => process.env[key] !== undefined).map(key => [key, process.env[key]]));
}
// Runs commands through the Go program, which owns the execution endpoints, over the host protocol.
export class PeerExecutor implements Executor {
  private peer: Peer;
  private environment: string;
  constructor(peer: Peer, environment: string) { this.peer = peer; this.environment = environment; }
  private async call<T>(method: string, args: object, signal?: AbortSignal): Promise<T> {
    if (signal?.aborted) throw new Unavailable();
    let abort!: () => void;
    const aborted = new Promise<never>((_, reject) => { abort = () => reject(new Unavailable()); signal?.addEventListener('abort', abort, { once: true }); });
    try { return await Promise.race([this.peer.call(method, { environment: this.environment, ...args }), aborted]); } catch (error) {
      if (!(error instanceof ProtocolError)) throw error;
      // A lost or timed-out call leaves the effect unknown; the stable job ID makes the retry safe.
      if (['unavailable', 'timeout', 'closed'].includes(error.code)) throw new Unavailable();
      if (error.code === 'expired') throw new Error('Output no longer retained.');
      throw new Error(error.message);
    } finally { signal?.removeEventListener('abort', abort); }
  }
  start(spec: CommandSpec, signal?: AbortSignal) { return this.call<CommandResult>('exec.start', { spec }, signal); }
  poll(id: string, signal?: AbortSignal) { return this.call<CommandResult>('exec.poll', { id }, signal); }
  async stop(id: string, signal?: AbortSignal) { await this.call('exec.stop', { id }, signal); }
  output(id: string, offset: number, limit: number, signal?: AbortSignal) { return this.call<Chunk>('exec.output', { id, offset, limit }, signal); }
}
