import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { join } from 'node:path';
import { ProtocolError } from '../src/protocol.ts';
import { TestPeer, fixture, init, input, text, waitFor } from './support.ts';

test('init with the faux provider runs the scripted scenario through the pipe', async () => {
  const f = await fixture(() => text()), host = new TestPeer();
  const sent: string[] = []; let principles = 0;
  host.serveExecutors({ local: f.executor });
  host.handle('send', async p => { sent.push(p.text); return null; });
  host.handle('principles', async () => { principles++; return 'Be brief.'; });
  try {
    assert.deepEqual(await host.call('init', init(join(f.dir, 'host'), { principles: true })), { ok: true });
    assert.equal(await host.call('admit', input('independent', 'Do the independent task', 'other')), 'router');
    await waitFor(async () => sent, values => values.includes('independent result'), 15000);
    const status = await waitFor(() => host.call('status'), value => value.tasks.length === 1 && value.tasks[0].status === 'done');
    assert.equal(typeof status.tasks[0].endedAt, 'number', 'a finished task reports when it ended');
    assert.equal(status.commands[0].status, 'done');
    assert.ok(principles > 0);
    assert.deepEqual(await host.call('close'), { ok: true });
    assert.equal(await host.exited(), 0);
  } finally { host.kill(); await f.close(); }
});

test('host answers invalid before init and on a second init', async () => {
  const f = await fixture(() => text()), host = new TestPeer(), state = join(f.dir, 'host');
  try {
    await assert.rejects(host.call('admit', input('early', 'Too early')), { code: 'invalid', message: 'init must be the first message.' });
    await assert.rejects(host.call('init', init(state, { model: { provider: 'other', modelId: 'x' } })), { code: 'invalid', message: 'Unknown provider. Choose openai, anthropic, deepseek or compatible.' });
    assert.deepEqual(await host.call('init', init(state)), { ok: true });
    await assert.rejects(host.call('init', init(state)), { code: 'invalid', message: 'init must be the first message.' });
    assert.deepEqual((await host.call('status')).tasks, []);
    assert.equal(host.stderr.includes('Golem'), false);
  } finally { host.kill(); await f.close(); }
});

test('rejected checks pass through with their message; unexpected failures are internal', async () => {
  const f = await fixture(() => text()), host = new TestPeer();
  host.handle('exec.start', async () => { throw new Error('Endpoint exploded.'); });
  host.handle('send', async () => null);
  try {
    await host.call('init', init(join(f.dir, 'host')));
    await assert.rejects(host.call('resume', { work: 'missing', text: 'Continue', thread: 'console:main', key: 'k1' }), { code: 'rejected', message: 'Unknown task or thread.' });
    await assert.rejects(host.call('inspect', { work: 'missing' }), { code: 'rejected', message: 'Unknown task.' });
    await assert.rejects(host.call('history', { thread: 'console:main', after: -1 }), { code: 'rejected', message: 'Invalid history position.' });
    await assert.rejects(host.call('admit', { ...input('', 'No key'), key: '' }), { code: 'internal', message: 'Request could not be completed.' });
    await waitFor(async () => host.logs, logs => logs.some(log => log.level === 'error' && log.message.includes('Handler admit failed')));
    await host.call('admit', input('independent', 'Do the independent task', 'other'));
    const status = await waitFor(() => host.call('status'), value => value.commands.length === 1 && value.commands[0].status === 'interrupted', 15000);
    assert.equal(status.tasks[0].status, 'active');
    assert.equal(await host.end(), 0);
  } finally { host.kill(); await f.close(); }
});

test('an undeliverable send marks the reply failed without retrying', async () => {
  const f = await fixture(() => text()), host = new TestPeer();
  let sends = 0;
  host.serveExecutors({ local: f.executor });
  host.handle('send', async () => { sends++; throw new ProtocolError('undeliverable', 'Channel archived.'); });
  try {
    await host.call('init', init(join(f.dir, 'host')));
    await host.call('admit', input('independent', 'Do the independent task', 'other'));
    const status = await waitFor(() => host.call('status'), value => value.failedReplies.length === 1, 15000);
    assert.equal(status.failedReplies[0].attempts, 1);
    assert.equal(sends, 1);
  } finally { host.kill(); await f.close(); }
});

test('stdin end closes the host with exit code 0', async () => {
  const f = await fixture(() => text()), host = new TestPeer();
  try {
    await host.call('init', init(join(f.dir, 'host')));
    assert.equal(await host.end(), 0);
  } finally { host.kill(); await f.close(); }
});

test('status mode prints the status JSON for an existing state and fails for a missing one', async () => {
  const f = await fixture(() => text());
  try {
    const child = spawnSync(process.execPath, ['src/host.ts', 'status', '--state', join(f.dir, 'controller')], { encoding: 'utf8' });
    assert.equal(child.status, 0, child.stderr);
    assert.deepEqual(JSON.parse(child.stdout), { tasks: [], commands: [], queuedReplies: 0, failedReplies: [], timers: [], failures: [] });
    const missing = spawnSync(process.execPath, ['src/host.ts', 'status', '--state', join(f.dir, 'missing')], { encoding: 'utf8' });
    assert.equal(missing.status, 1);
    assert.equal(missing.stderr.trim(), 'No Pi state exists at the selected directory.');
  } finally { await f.close(); }
});
