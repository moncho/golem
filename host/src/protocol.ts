import { randomUUID } from 'node:crypto';
import type { Readable, Writable } from 'node:stream';

export class ProtocolError extends Error {
  code: string;
  constructor(code: string, message: string) { super(message); this.code = code; }
}

export type Handler = (params: any) => Promise<unknown>;

type Pending = { resolve: (value: any) => void; reject: (error: Error) => void; timer: NodeJS.Timeout };

export class Peer {
  private handlers = new Map<string, Handler>();
  private pending = new Map<string, Pending>();
  private closed = false;

  private input: Readable;
  private output: Writable;

  constructor(input: Readable, output: Writable) { this.input = input; this.output = output; }

  handle(method: string, handler: Handler) { this.handlers.set(method, handler); }

  call(method: string, params: unknown, timeoutMs = 15_000): Promise<any> {
    if (this.closed) return Promise.reject(new ProtocolError('closed', 'Peer closed.'));
    const id = randomUUID();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.pending.delete(id);
        reject(new ProtocolError('timeout', `Call to ${method} timed out.`));
      }, timeoutMs);
      this.pending.set(id, { resolve, reject, timer });
      try { this.send({ id, method, params }); } catch (error) {
        clearTimeout(timer);
        this.pending.delete(id);
        reject(error as Error);
      }
    });
  }

  run(): Promise<void> {
    return new Promise((resolve) => {
      let buffer = '';
      this.input.setEncoding('utf8');
      this.input.on('data', (chunk: string) => {
        buffer += chunk;
        let at;
        while ((at = buffer.indexOf('\n')) >= 0) {
          const line = buffer.slice(0, at);
          buffer = buffer.slice(at + 1);
          if (line.trim()) this.receive(line);
        }
      });
      const end = () => {
        if (this.closed) return;
        if (buffer.trim()) { const last = buffer; buffer = ''; this.receive(last); }
        this.closed = true;
        for (const [id, p] of this.pending) {
          clearTimeout(p.timer);
          p.reject(new ProtocolError('closed', 'Peer closed.'));
          this.pending.delete(id);
        }
        resolve();
      };
      const fail = (error: Error) => { console.error('Stream failed:', error); end(); };
      this.input.on('end', end);
      this.input.on('close', end);
      this.input.on('error', fail);
      this.output.on('error', fail);
    });
  }

  private send(message: unknown) { this.output.write(JSON.stringify(message) + '\n'); }

  private receive(line: string) {
    let message: any;
    try { message = JSON.parse(line); } catch { console.error('Ignored a line that is not JSON.'); return; }
    if (!message || typeof message !== 'object' || typeof message.id !== 'string') return;
    if (typeof message.method === 'string') { void this.serve(message.id, message.method, message.params); return; }
    const p = this.pending.get(message.id);
    if (!p) return;
    this.pending.delete(message.id);
    clearTimeout(p.timer);
    if (message.error) p.reject(new ProtocolError(String(message.error.code ?? 'internal'), String(message.error.message ?? '')));
    else p.resolve(message.result);
  }

  private async serve(id: string, method: string, params: unknown) {
    const handler = this.handlers.get(method);
    try {
      if (!handler) throw new ProtocolError('invalid', 'Unknown method.');
      this.send({ id, result: (await handler(params)) ?? null });
    } catch (error) {
      if (error instanceof ProtocolError) this.send({ id, error: { code: error.code, message: error.message } });
      else {
        console.error(`Handler ${method} failed:`, error);
        this.send({ id, error: { code: 'internal', message: 'Request could not be completed.' } });
      }
    }
  }
}
