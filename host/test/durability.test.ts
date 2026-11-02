import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, events, role, input, waitFor } from './support.ts';
import { Undeliverable } from '../src/golem.ts';

test('failed delivery survives reopen and preserves reply order within a thread', async () => {
  let firstAttempt = true; const sent: string[] = [];
  const f = await fixture(request => {
    const event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    const replies = request.messages.filter(message => message.role === 'toolResult' && message.toolName === 'reply').length;
    if (last?.role === 'toolResult' && replies >= 2) return text();
    return call('reply', { event: event.event, text: replies === 0 ? 'first' : 'second' });
  }, { send: async (_thread, message) => { if (firstAttempt) { firstAttempt = false; throw new Error('test outage'); } sent.push(message); } });
  try {
    await f.app.admit(input('delivery', 'two replies'));
    await waitFor(() => f.app.state(), state => Object.values(state.deliveries).length === 2 && Object.values(state.deliveries)[0].attempts === 1);
    await f.restart(); await waitFor(async () => sent, values => values.length === 2);
    assert.deepEqual(sent, ['first', 'second']);
    assert.equal(Object.values((await f.app.state()).deliveries).filter(item => item.status === 'sent').length, 2);
  } finally { await f.close(); }
});

test('a scheduled Pi task resumes after reopen and delivers once', async () => {
  const f = await fixture(request => {
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    const event = events(request).at(-1)!;
    return event.kind === 'timer' ? call('reply', { event: event.event, text: 'reminder' }) : call('schedule', { event: event.event, text: 'reminder', due: new Date(Date.now() + 500).toISOString() });
  });
  try {
    await f.app.admit(input('schedule', 'remind me'));
    await waitFor(() => f.app.state(), state => Object.keys(state.timers).length === 1);
    await f.restart(); await waitFor(async () => f.sent, values => values.length === 1);
    assert.equal(f.sent[0].text, 'reminder'); assert.equal(Object.values((await f.app.state()).timers)[0].active, false);
  } finally { await f.close(); }
});

test('the separate Watchdog can inspect and nudge stalled work', async () => {
  let audited = false;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (agent === 'watchdog') {
      if (last?.role === 'toolResult' && last.toolName === 'recall') { audited = true; return call('nudge_task', { event: event.event, work: event.work, text: 'Finish the stalled task.' }); }
      if (last?.role === 'toolResult') return text();
      return call('recall', { ref: `work:${event.work}` });
    }
    if (last?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'stalled', goal: 'finish' }) : call('reply', { event: event.event, text: 'recovered' });
    if (event.kind === 'nudge') return call('report', { status: 'done', text: 'recovered' });
    return text();
  }, { watchMs: 25, auditMs: 75 });
  try { await f.app.admit(input('audit', 'finish')); await waitFor(async () => f.sent, values => values.length === 1); assert.equal(audited, true); }
  finally { await f.close(); }
});

test('a reply that keeps failing is marked failed after its attempts and stops blocking later replies in the thread', async () => {
  const sent: string[] = [];
  const f = await fixture(request => {
    const event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    const replies = request.messages.filter(message => message.role === 'toolResult' && message.toolName === 'reply').length;
    if (last?.role === 'toolResult' && replies >= 2) return text();
    return call('reply', { event: event.event, text: replies === 0 ? 'first' : 'second' });
  }, { deliveryAttempts: 1, send: async (_thread, message) => { if (message === 'first') throw new Error('test outage'); sent.push(message); } });
  try {
    await f.app.admit(input('delivery', 'two replies'));
    const state = await waitFor(() => f.app.state(), state => Object.values(state.deliveries).some(item => item.status === 'failed') && sent.length === 1);
    assert.deepEqual(sent, ['second']);
    assert.deepEqual(Object.values(state.deliveries).map(item => item.status), ['failed', 'sent']);
  } finally { await f.close(); }
});

test('an undeliverable reply fails on its first attempt instead of retrying', async () => {
  let attempts = 0;
  const f = await fixture(request => {
    const event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    return call('reply', { event: event.event, text: 'gone' });
  }, { send: async () => { attempts++; throw new Undeliverable('channel is archived'); } });
  try {
    await f.app.admit(input('undeliverable', 'reply'));
    const state = await waitFor(() => f.app.state(), state => Object.values(state.deliveries).some(item => item.status === 'failed'));
    assert.equal(attempts, 1); assert.equal(Object.values(state.deliveries)[0].attempts, 1);
  } finally { await f.close(); }
});

test('a fired timer cannot schedule further timers', async () => {
  let rejected: boolean | undefined;
  const f = await fixture(request => {
    const last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') { if (last.toolName === 'schedule' && events(request).at(-1)!.kind === 'timer') rejected = last.isError; return text(); }
    const event = events(request).at(-1)!;
    return call('schedule', { event: event.event, text: 'again', due: new Date(Date.now() + 300).toISOString() });
  });
  try {
    await f.app.admit(input('chain', 'remind me'));
    await waitFor(async () => rejected, value => value !== undefined);
    assert.equal(rejected, true); assert.equal(Object.keys((await f.app.state()).timers).length, 1);
  } finally { await f.close(); }
});

test('finished work, notified commands, settled replies and spent timers are pruned after the retention period', async () => {
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') {
      if (agent === 'worker' && last.toolName === 'read_output') return call('report', { status: 'done', text: 'finished' });
      return text();
    }
    if (agent === 'router') {
      if (event.kind === 'explicit') return call('start_task', { event: event.event, title: 'short', goal: 'finish quickly' });
      if (event.kind === 'worker_report') return call('reply', { event: event.event, text: 'done' });
      return text();
    }
    if (event.kind === 'assignment') return call('start_command', { command: 'true' });
    if (event.kind === 'command_finished') return call('read_output', { job: JSON.parse(event.text).job });
    return text();
  }, { watchMs: 25, retainMs: 1500, maxEventKeys: 2 });
  try {
    await f.app.admit(input('prune', 'go'));
    await waitFor(async () => f.sent, values => values.length === 1);
    const before = await f.app.state();
    assert.equal(Object.keys(before.works).length, 1); assert.equal(Object.keys(before.commands).length, 1); assert.equal(Object.keys(before.deliveries).length, 1);
    const after = await waitFor(() => f.app.state(), state => !Object.keys(state.works).length && !Object.keys(state.deliveries).length);
    assert.deepEqual([Object.keys(after.commands).length, Object.keys(after.deliveries).length], [0, 0]);
    assert.ok(Object.keys(after.eventKeys).length <= 2);
    assert.equal(await f.app.admit(input('prune', 'go')), 'router', 'a pruned key is admitted again');
  } finally { await f.close(); }
});

test('the archive keeps only the newest finished tasks up to the configured limit', async () => {
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: event.text, goal: event.text }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'done', text: 'finished', outcome: 'resolved' });
    return text();
  }, { watchMs: 25, retainMs: 200, maxArchive: 1 });
  try {
    await f.app.admit(input('one', 'first goal'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.status === 'done'));
    await f.app.admit(input('two', 'second goal'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).filter(work => work.status === 'done').length === 2);
    await waitFor(() => f.app.state(), state => !Object.keys(state.works).length);
    const archived = await f.app.searchWork('');
    assert.deepEqual(archived.map(work => work.goal), ['second goal']);
  } finally { await f.close(); }
});
