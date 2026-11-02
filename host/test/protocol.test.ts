import test from 'node:test';
import assert from 'node:assert/strict';
import { PassThrough } from 'node:stream';
import { Peer, ProtocolError } from '../src/protocol.ts';

function setup() {
  const toPeer = new PassThrough(), fromPeer = new PassThrough();
  const peer = new Peer(toPeer, fromPeer);
  const lines: any[] = [];
  const waiters: Array<() => void> = [];
  let buf = '';
  fromPeer.on('data', (chunk) => {
    buf += chunk;
    let i;
    while ((i = buf.indexOf('\n')) >= 0) { lines.push(JSON.parse(buf.slice(0, i))); buf = buf.slice(i + 1); }
    for (const w of waiters.splice(0)) w();
  });
  const next = async (n: number) => {
    while (lines.length < n) await new Promise<void>((r) => waiters.push(r));
    return lines;
  };
  const send = (m: unknown) => toPeer.write(JSON.stringify(m) + '\n');
  return { peer, toPeer, out: fromPeer, next, send };
}

test('responses may arrive out of order and resolve the matching call', async () => {
  const { peer, next, send } = setup();
  const run = peer.run();
  const a = peer.call('a', { n: 1 }), b = peer.call('b', { n: 2 });
  const [ra, rb] = await next(2);
  assert.equal(ra.method, 'a');
  assert.deepEqual(rb.params, { n: 2 });
  send({ id: rb.id, result: 'second' });
  send({ id: ra.id, result: 'first' });
  assert.equal(await a, 'first');
  assert.equal(await b, 'second');
  void run;
});

test('a handler error answers its code and message; unknown throws answer internal', async () => {
  const { peer, next, send } = setup();
  peer.handle('known', async () => { throw new ProtocolError('nope', 'No.'); });
  peer.handle('unknown', async () => { throw new Error('secret detail'); });
  peer.handle('ok', async (p) => ({ echo: p }));
  void peer.run();
  const err = console.error;
  console.error = () => {};
  try {
    send({ id: '1', method: 'known', params: {} });
    send({ id: '2', method: 'unknown', params: {} });
    send({ id: '3', method: 'ok', params: 7 });
    send({ id: '4', method: 'missing', params: {} });
    const out = await next(4);
    const by = Object.fromEntries(out.map((m) => [m.id, m]));
    assert.deepEqual(by['1'].error, { code: 'nope', message: 'No.' });
    assert.deepEqual(by['2'].error, { code: 'internal', message: 'Request could not be completed.' });
    assert.deepEqual(by['3'].result, { echo: 7 });
    assert.deepEqual(by['4'].error, { code: 'invalid', message: 'Unknown method.' });
  } finally { console.error = err; }
});

test('run resolves on end of input and rejects pending calls with closed', async () => {
  const { peer, toPeer } = setup();
  const run = peer.run();
  const call = peer.call('x', {});
  const rejected = assert.rejects(call, (e: any) => e instanceof ProtocolError && e.code === 'closed' && e.message === 'Peer closed.');
  toPeer.end();
  await run;
  await rejected;
});

test('a non-JSON line is ignored', async () => {
  const { peer, toPeer, next, send } = setup();
  peer.handle('ok', async () => 'fine');
  void peer.run();
  const err = console.error;
  console.error = () => {};
  try {
    toPeer.write('not json\n{"nothing":1}\n');
    send({ id: '1', method: 'ok', params: {} });
    const out = await next(1);
    assert.deepEqual(out[0], { id: '1', result: 'fine' });
  } finally { console.error = err; }
});

test('a call times out with code timeout', async () => {
  const { peer } = setup();
  void peer.run();
  await assert.rejects(peer.call('slow', {}, 20), (e: any) => e instanceof ProtocolError && e.code === 'timeout');
});

test('an error on output or input ends the peer instead of throwing', async () => {
  const quiet = console.error;
  console.error = () => {};
  try {
    const a = setup();
    const runA = a.peer.run();
    const call = a.peer.call('x', {});
    const rejected = assert.rejects(call, (e: any) => e.code === 'closed');
    a.out.emit('error', new Error('EPIPE'));
    await runA;
    await rejected;
    const b = setup();
    const runB = b.peer.run();
    b.toPeer.destroy(new Error('read failed'));
    await runB;
  } finally { console.error = quiet; }
});

test('a failed send rejects the call and leaves no pending entry', async () => {
  const toPeer = new PassThrough();
  const broken: any = new PassThrough();
  broken.write = () => { throw new Error('write failed'); };
  const peer = new Peer(toPeer, broken);
  void peer.run();
  await assert.rejects(peer.call('x', {}, 60_000), /write failed/);
  assert.equal((peer as any).pending.size, 0);
});

test('a final line without a trailing newline is delivered at end of input', async () => {
  const { peer, toPeer, next } = setup();
  const run = peer.run();
  const call = peer.call('x', {});
  const [req] = await next(1);
  toPeer.write(JSON.stringify({ id: req.id, result: 'last' }));
  toPeer.end();
  assert.equal(await call, 'last');
  await run;
});
