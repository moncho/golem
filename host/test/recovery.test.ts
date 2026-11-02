import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, events, role, input, waitFor } from './support.ts';
import { scriptedDecision } from '../src/demo.ts';
import { Unavailable } from '../src/executor.ts';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';

test('lost start acknowledgement and offline cancellation recover without repeating the command', async () => {
  const f = await fixture(scriptedDecision);
  let connected = false, started = '', starts = 0;
  f.settings.environments.local = {
    async start(spec, signal) {
      if (started && !connected) throw new Unavailable();
      starts++; const result = await f.executor.start(spec, signal);
      if (!started) { started = spec.id; throw new Unavailable(); }
      return result;
    },
    async stop(id, signal) { if (!connected) throw new Unavailable(); await f.executor.stop(id, signal); },
    async poll(id, signal) { if (!connected) throw new Unavailable(); return f.executor.poll(id, signal); },
    output: (...args) => f.executor.output(...args),
  };
  try {
    await f.app.admit(input('lost', 'long task'));
    await waitFor(async () => started, value => !!value);
    assert.equal((await f.app.state()).commands[started].status, 'queued');
    await f.restart();
    assert.equal((await f.executor.poll(started)).status, 'running');
    assert.equal(await f.app.admit(input('redirect-offline', 'change plan')), 'subscribed');
    await waitFor(() => f.app.state(), state => state.commands[started].stop);
    connected = true;
    await waitFor(async () => f.sent, messages => messages.some(message => message.text === 'updated result'));
    assert.equal((await f.app.state()).commands[started].status, 'cancelled');
    assert.equal(starts, 2, 'the original command must not be started twice');
  } finally { await f.close(); }
});

test('every subscribed Worker receives a followup and the Router receives no duplicate', async () => {
  const seen = new Set<string>();
  const f = await fixture(request => {
    const event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (role(request) === 'router') {
      if (event.text === 'declare side thread') return text();
      const starts = request.messages.filter(message => message.role === 'toolResult' && message.toolName === 'start_task').length;
      if (starts >= 2) return text();
      return call('start_task', { event: event.event, title: `task ${starts}`, goal: `goal ${starts}`, new_reason: 'The user requested two independent outcomes.' });
    }
    if (last?.role === 'toolResult') return text();
    if (event.kind === 'assignment') return call('subscribe', { thread: 'console:side', enabled: true });
    if (event.text === 'follow both') {
      seen.add(events(request).find(event => event.kind === 'assignment')!.work);
      return text();
    }
    return text();
  });
  try {
    await f.app.admit(input('side', 'declare side thread', 'side'));
    await f.app.admit(input('two', 'start two independent goals'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).filter(work => work.threads.includes('console:side')).length === 2);
    assert.equal(await f.app.admit(input('both', 'follow both', 'side')), 'subscribed');
    await waitFor(async () => seen.size, value => value === 2);
    const state = await f.app.state();
    const history = await f.app.root.entries({ order: 'descending' }, 100, undefined, BACKGROUND_CONTEXT);
    const routed = history.items.find(entry => entry.kind === 'golem.event' && (entry.data as { key?: string })?.key === 'both')!;
    assert.deepEqual((routed.data as { targets: number[] }).targets.sort(), Object.values(state.works).map(work => work.conversation).sort());
    assert.equal(history.items.filter(entry => entry.kind === 'golem.input' && (entry.data as { event?: number })?.event === routed.id).length, 0);
  } finally { await f.close(); }
});

test('an explicit stop_task cancels the running command at the executor and the record converges', async () => {
  let work = '';
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') return text();
    if (agent === 'router') {
      if (event.text === 'go') return call('start_task', { event: event.event, title: 'long', goal: 'run long' });
      if (event.text === 'cancel it') return call('stop_task', { event: event.event, work });
      return text();
    }
    if (agent === 'worker' && event.kind === 'assignment') { work = event.work; return call('start_command', { command: 'sleep 60', timeout_seconds: 90 }); }
    return text();
  });
  try {
    await f.app.admit(input('go', 'go'));
    const running = await waitFor(() => f.app.state(), state => Object.values(state.commands).some(job => job.status === 'running'));
    const job = Object.values(running.commands)[0].id;
    assert.equal(await f.app.admit(input('cancel', 'cancel it', 'other')), 'router');
    await waitFor(() => f.executor.poll(job), result => result.status === 'cancelled');
    const state = await waitFor(() => f.app.state(), state => state.commands[job].notified);
    assert.equal(state.commands[job].status, 'cancelled'); assert.equal(state.works[work].status, 'cancelled');
  } finally { await f.close(); }
});

test('stop_task rejects work that already finished', async () => {
  let work = '', rejected: boolean | undefined;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') { if (last.toolName === 'stop_task') rejected = last.isError; return text(); }
    if (agent === 'router') {
      if (event.text === 'go') return call('start_task', { event: event.event, title: 'quick', goal: 'finish' });
      if (event.text === 'cancel it') return call('stop_task', { event: event.event, work });
      return text();
    }
    if (agent === 'worker' && event.kind === 'assignment') { work = event.work; return call('report', { status: 'done', text: 'finished' }); }
    return text();
  });
  try {
    await f.app.admit(input('go', 'go'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(item => item.status === 'done'));
    await f.app.admit(input('cancel', 'cancel it', 'other'));
    await waitFor(async () => rejected, value => value !== undefined);
    assert.equal(rejected, true); assert.equal((await f.app.state()).works[work].status, 'done');
  } finally { await f.close(); }
});

test('read_output advances next_offset by bytes consumed, not by the decoded text length', async () => {
  let next: number | undefined;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') {
      if (last.toolName === 'read_output') next = JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join('')).next_offset;
      return text();
    }
    if (agent === 'router' && event.kind === 'explicit') return call('start_task', { event: event.event, title: 'bytes', goal: 'print' });
    if (agent === 'worker' && event.kind === 'assignment') return call('start_command', { command: "printf 'éé'" });
    if (agent === 'worker' && event.kind === 'command_finished') return call('read_output', { job: JSON.parse(event.text).job, limit: 1 });
    return text();
  });
  try {
    await f.app.admit(input('bytes', 'print'));
    await waitFor(async () => next, value => value !== undefined);
    assert.equal(next, 1);
  } finally { await f.close(); }
});
