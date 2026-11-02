import test from 'node:test';
import assert from 'node:assert/strict';
import { getCurrentTools } from '@earendil-works/pi-ai';
import { fixture, call, text, events, role, input, waitFor } from './support.ts';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';

test('admission filters channels and senders before the Router; observe cannot publish or spawn', async () => {
  const denied: boolean[] = [];
  const f = await fixture(request => {
    const last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') { denied.push(last.isError); return text(); }
    const event = events(request).at(-1)!;
    return event.text === 'spawn' ? call('start_task', { event: event.event, title: 'ambient', goal: 'ambient' }) : call('reply', { event: event.event, text: 'unsolicited' });
  }, { channels: ['C1'], users: ['U1'], bots: ['B1'], proactive: 'observe' });
  try {
    const base = { ...input('a', 'publish'), thread: { id: 'slack:C1:1', source: 'slack' as const, channel: 'C1', replyTo: '1' }, sender: 'U1', explicit: false };
    assert.equal(await f.app.admit({ ...base, sender: 'stranger' }), 'ignored');
    assert.equal(await f.app.admit({ ...base, thread: { ...base.thread, channel: 'C2' } }), 'ignored');
    assert.equal(Object.keys((await f.app.state()).eventKeys).length, 0);
    assert.equal(await f.app.admit({ ...base, bot: true, sender: 'B1', explicit: true }), 'router');
    await waitFor(async () => denied, values => values.length === 1);
    await f.app.admit({ ...base, key: 'b', text: 'spawn' });
    await waitFor(async () => denied, values => values.length === 2);
    assert.deepEqual(denied, [true, true]); assert.equal(f.sent.length, 0); assert.equal(Object.keys((await f.app.state()).works).length, 0);
  } finally { await f.close(); }
});

test('role tools are isolated and speculative publication requires successful command evidence', async () => {
  let rejectedUnverified = false;
  const f = await fixture(request => {
    const agent = role(request), last = request.messages.findLast(message => message.role !== 'system'), event = events(request).at(-1)!;
    const tools = getCurrentTools(request.messages).map(tool => tool.name);
    if (agent === 'router') {
      assert.ok(!tools.includes('start_command'));
      if (last?.role === 'toolResult') return text();
      if (event.kind === 'candidate') return call('start_task', { event: event.event, title: 'verify', goal: 'verify the candidate' });
      return call('reply', { event: event.event, text: 'verified result' });
    }
    assert.ok(!tools.includes('reply')); assert.ok(!tools.includes('start_task'));
    if (last?.role === 'toolResult') {
      if (last.isError) { rejectedUnverified = true; return call('start_command', { command: "printf 'verified'" }); }
      if (last.toolName === 'read_output') {
        const value = JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join(''));
        return call('report', { status: 'done', text: value.output, useful: true, evidence_job: value.job });
      }
      return text();
    }
    if (event.kind === 'assignment') return call('report', { status: 'done', text: 'unverified', useful: true });
    if (event.kind === 'command_finished') return call('read_output', { job: JSON.parse(event.text).job });
    return text();
  }, { proactive: 'act' });
  try {
    await f.app.admit({ ...input('candidate', 'maybe useful'), explicit: false });
    await waitFor(async () => f.sent, values => values.length === 1);
    assert.equal(rejectedUnverified, true); assert.equal(f.sent[0].text, 'verified result');
    assert.equal(Object.values((await f.app.state()).works)[0].speculative, true);
  } finally { await f.close(); }
});

test('a reply in a Slack thread Golem already took part in is a request without a mention', async () => {
  const f = await fixture(() => text(), { channels: ['C1'], users: ['U1'], bots: ['B1'] });
  try {
    const slack = (key: string, root: string, explicit: boolean, sender = 'U1', bot = false) => ({
      key: `slack:C1:${key}`, text: 'hello', sender, bot, explicit,
      thread: { id: `slack:C1:${root}`, source: 'slack' as const, channel: 'C1', replyTo: root },
    });
    // A mention opens the thread.
    assert.equal(await f.app.admit(slack('10', '10', true)), 'router');
    // A later reply in that thread, with no mention, is still a request.
    assert.equal(await f.app.admit(slack('12', '10', false)), 'router');
    // A reply in a thread Golem never joined stays ambient and is ignored under proactive=off.
    assert.equal(await f.app.admit(slack('21', '20', false)), 'ignored');
    // A bot never gains request authority, even in Golem's thread.
    assert.equal(await f.app.admit(slack('13', '10', false, 'B1', true)), 'ignored');
    const entries = (await f.app.root.entries({ order: 'descending' }, 10, undefined, BACKGROUND_CONTEXT)).items
      .filter(entry => entry.kind === 'golem.event').map(entry => entry.data as { key: string; kind: string; explicit: boolean });
    const reply = entries.find(entry => entry.key === 'slack:C1:12')!;
    assert.equal(reply.kind, 'explicit'); assert.equal(reply.explicit, true);
  } finally { await f.close(); }
});

test('the Slack stop button cancels the Worker on that thread, for allowed people only', async () => {
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'Long job', goal: 'sleep' }) : text();
    if (event.kind === 'assignment') return call('start_command', { command: 'sleep 30', timeout_seconds: 60 });
    return text();
  }, { channels: ['C1'], users: ['U1'] });
  try {
    const thread = { id: 'slack:C1:10', source: 'slack' as const, channel: 'C1', replyTo: '10' };
    await f.app.admit({ key: 'slack:C1:10', text: 'go', sender: 'U1', explicit: true, thread });
    const running = await waitFor(() => f.app.state(), state => Object.values(state.commands).some(job => job.status === 'running'));
    const work = Object.values(running.works)[0];
    assert.equal(await f.app.stopThread('slack:C1:10', 'C1', 'stranger'), 0);
    assert.equal(await f.app.stopThread('slack:C9:10', 'C9', 'U1'), 0);
    assert.equal((await f.app.state()).works[work.id].status, 'active');
    assert.equal(await f.app.stopThread('slack:C1:10', 'C1', 'U1'), 1);
    const state = await waitFor(() => f.app.state(), state => Object.values(state.commands).every(job => job.status === 'cancelled'));
    assert.equal(state.works[work.id].status, 'cancelled');
    assert.equal(await f.app.stopThread('slack:C1:10', 'C1', 'U1'), 0);
  } finally { await f.close(); }
});

test('without a channel allowlist, allowed people are heard in any conversation the bot is in', async () => {
  const f = await fixture(() => text(), { users: ['U1'] });
  try {
    const slack = (channel: string, sender: string) => ({ key: `slack:${channel}:1:${sender}`, text: 'hi', sender, explicit: true,
      thread: { id: `slack:${channel}:1`, source: 'slack' as const, channel, replyTo: '1' } });
    assert.equal(await f.app.admit(slack('C1', 'U1')), 'router');
    assert.equal(await f.app.admit(slack('D9', 'U1')), 'router');
    assert.equal(await f.app.admit(slack('C1', 'stranger')), 'ignored');
    assert.equal(await f.app.stopThread('slack:C1:1', 'C1', 'stranger'), 0);
  } finally { await f.close(); }
});
