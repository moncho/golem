import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, role, events, input, waitFor } from './support.ts';
import { StateDoc } from '../src/state.ts';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';
import { Rejected } from '../src/golem.ts';

test('dispatch finds archived work and resumes its original conversation across restart', async () => {
  let resumed = false, sawCandidate = false;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') {
      if (agent === 'router' && last.toolName === 'start_task') {
        const result = JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join(''));
        if (result.action === 'review_existing_work') {
          sawCandidate = true;
          return call('resume_task', { event: event.event, work: result.candidates[0].id, text: 'Continue the supplier investigation' });
        }
      }
      return text();
    }
    if (agent === 'router' && event.kind === 'explicit') return call('start_task', { event: event.event, title: 'Supplier investigation', goal: 'Investigate supplier missing fabrication file' });
    if (agent === 'worker') {
      if (event.kind === 'instructions') resumed = true;
      return call('report', { status: 'done', text: 'Supplier file verified', outcome: 'resolved' });
    }
    return text();
  }, { watchMs: 25, retainMs: 200 });
  try {
    await f.app.admit(input('original', 'Investigate supplier'));
    const state = await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.status === 'done'));
    const original = Object.values(state.works)[0];
    await waitFor(() => f.app.state(), state => !state.works[original.id]);
    await f.restart();
    const found = await f.app.searchWork('fabrication');
    assert.equal(found[0].id, original.id); assert.equal(found[0].conversation, original.conversation);
    await f.app.admit(input('later', 'The supplier replied', 'email'));
    await waitFor(async () => resumed, value => value);
    assert.equal(sawCandidate, true);
    assert.equal((await f.app.inspectWork(original.id)).work.conversation, original.conversation);
    assert.equal((await f.app.searchWork('fabrication')).length, 1);
  } finally { await f.close(); }
});

test('an unresolved outcome survives restart and its due check wakes the same Worker', async () => {
  let followups = 0;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') return text();
    if (agent === 'router' && event.kind === 'explicit') return call('start_task', { event: event.event, title: 'Supplier', goal: 'Wait for supplier and verify receipt' });
    if (agent === 'worker' && event.kind === 'assignment') return call('report', { status: 'waiting', text: 'Request sent; receipt still pending.', outcome: 'check_later', next_actor: 'supplier', next_action: 'Inspect whether the supplier acknowledged the file.', check_at: new Date(Date.now() + 700).toISOString() });
    if (agent === 'worker' && event.kind === 'followup') { followups++; return call('report', { status: 'done', text: 'Receipt verified.', outcome: 'resolved' }); }
    return text();
  }, { watchMs: 25, retainMs: 100 });
  try {
    await f.app.admit(input('waiting', 'Send and check later'));
    const waiting = await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.outcome === 'check_later'));
    const work = Object.values(waiting.works)[0]; assert.equal(work.status, 'waiting'); assert.equal(work.nextActor, 'supplier');
    await f.restart();
    await waitFor(() => f.app.state(), state => state.works[work.id]?.outcome === 'resolved');
    assert.equal(followups, 1);
    assert.equal((await f.app.inspectWork(work.id)).work.conversation, work.conversation);
  } finally { await f.close(); }
});

test('a newer user instruction invalidates the pending automatic followup', async () => {
  let followups = 0, redirected = false;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'waiting', goal: 'wait' }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'waiting', text: 'Awaiting response', outcome: 'check_later', next_actor: 'supplier', next_action: 'Check response', check_at: new Date(Date.now() + 250).toISOString() });
    if (event.kind === 'followup') followups++;
    if (event.kind === 'explicit') { redirected = true; return call('report', { status: 'waiting', text: 'Waiting for your decision', outcome: 'needs_decision', next_actor: 'user', next_action: 'Choose whether to proceed' }); }
    return text();
  });
  try {
    await f.app.admit(input('wait', 'wait'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => !!work.checkAt));
    await f.app.admit(input('redirect', 'Do not check; wait for my decision'));
    await waitFor(async () => redirected, value => value);
    await new Promise(resolve => setTimeout(resolve, 400));
    assert.equal(followups, 0);
    assert.equal(Object.values((await f.app.state()).works)[0].outcome, 'needs_decision');
  } finally { await f.close(); }
});

const toolResult = (request: Parameters<typeof role>[0], name: string) => {
  const last = request.messages.findLast(message => message.role !== 'system');
  if (last?.role !== 'toolResult' || last.toolName !== name) return undefined;
  return JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join(''));
};

test('an identical active goal on the same thread reuses the Worker even when new_reason is given', async () => {
  const results: { work?: string; action?: string }[] = [];
  const f = await fixture(request => {
    const event = events(request).at(-1)!, result = toolResult(request, 'start_task');
    if (result) results.push(result);
    if (role(request) === 'router' && results.length < 2) return call('start_task', { event: event.event, title: 'rebuild', goal: 'rebuild the search index', new_reason: 'retrying after a transient error' });
    return text();
  });
  try {
    await f.app.admit(input('twice', 'rebuild the index'));
    await waitFor(async () => results.length, count => count === 2);
    assert.equal(Object.keys((await f.app.state()).works).length, 1);
    assert.equal(results[1].work, results[0].work);
  } finally { await f.close(); }
});

test('a finished task sharing one word with a new goal on the same thread does not block dispatch', async () => {
  const results: { work?: string; action?: string }[] = [];
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, result = toolResult(request, 'start_task');
    if (result) { results.push(result); return text(); }
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router' && event.kind === 'explicit') return call('start_task', { event: event.event, title: event.text, goal: event.text });
    if (agent === 'worker' && event.kind === 'assignment') return call('report', { status: 'done', text: 'finished', outcome: 'resolved' });
    return text();
  });
  try {
    await f.app.admit(input('tests', 'Run the tests for the scheduler'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.status === 'done'));
    assert.equal(await f.app.admit(input('lint', 'Run the linter')), 'router');
    await waitFor(async () => results.length, count => count === 2);
    assert.equal(results[1].action, undefined);
    assert.equal(Object.keys((await f.app.state()).works).length, 2);
  } finally { await f.close(); }
});

test('a pending check survives an explicit message the Worker answers without a new report', async () => {
  let followups = 0;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'wait', goal: 'wait for the supplier' }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'waiting', text: 'Request sent.', outcome: 'check_later', next_actor: 'supplier', next_action: 'Check the acknowledgement', check_at: new Date(Date.now() + 500).toISOString() });
    if (event.kind === 'followup') followups++;
    return text('Noted.');
  });
  try {
    await f.app.admit(input('wait', 'wait'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => !!work.checkAt));
    assert.equal(await f.app.admit(input('thanks', 'thanks, sounds good')), 'subscribed');
    await waitFor(async () => followups, count => count === 1, 4000);
  } finally { await f.close(); }
});

test('resuming unresolved work clears its stale next actor and action', async () => {
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'supplier', goal: 'wait for the supplier' }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'waiting', text: 'Request sent.', outcome: 'check_later', next_actor: 'supplier', next_action: 'Inspect whether the supplier acknowledged', check_at: new Date(Date.now() + 3_600_000).toISOString() });
    return text();
  });
  try {
    await f.app.admit(input('wait', 'wait'));
    const waiting = await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.outcome === 'check_later'));
    const work = Object.values(waiting.works)[0];
    await f.app.resumeWork(work.id, 'the supplier replied', 'console:main', 'resume-1');
    const after = (await f.app.inspectWork(work.id)).work;
    assert.equal(after.outcome, 'open');
    assert.equal(after.checkAt, undefined);
    assert.equal(after.nextActor, undefined);
    assert.equal(after.nextAction, undefined);
  } finally { await f.close(); }
});

test('the Worker system prompt carries an excerpt of its task, not the full report text', async () => {
  const marker = 'x'.repeat(3000);
  let fullTextInPrompt = false, answered = false;
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (agent === 'worker' && JSON.stringify(request.messages.filter(message => message.role === 'system')).includes(marker)) fullTextInPrompt = true;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'long', goal: 'produce a long report' }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'progress', text: marker });
    if (event.kind === 'explicit') answered = true;
    return text();
  });
  try {
    await f.app.admit(input('go', 'go'));
    await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.summary === marker));
    assert.equal(await f.app.admit(input('ping', 'how is it going?')), 'subscribed');
    await waitFor(async () => answered, value => value);
    assert.equal(fullTextInPrompt, false);
  } finally { await f.close(); }
});

test('history after returns only newer entries in ascending order', async () => {
  const f = await fixture(() => text());
  try {
    await f.app.admit(input('first', 'First message', 'paged'));
    await f.app.admit(input('second', 'Second message', 'paged'));
    await f.app.admit(input('third', 'Third message', 'paged'));
    const empty = await f.app.history('console:unused');
    assert.deepEqual(empty.items, []);
    assert.ok(empty.after !== undefined && empty.after > 0, 'the newest page returns the newest scanned entry as the after cursor');
    assert.deepEqual((await f.app.history('console:unused', { after: empty.after })).items, []);
    const newest = await f.app.history('console:paged');
    const all = newest.items;
    assert.deepEqual(all.map(item => item.text), ['First message', 'Second message', 'Third message']);
    assert.ok(newest.after !== undefined && newest.after >= all[2].id);
    const newer = await f.app.history('console:paged', { after: all[0].id });
    assert.deepEqual(newer.items.map(item => item.text), ['Second message', 'Third message']);
    assert.deepEqual((await f.app.history('console:paged', { before: all[2].id })).items.map(item => item.text), ['First message', 'Second message']);
    await assert.rejects(f.app.history('console:paged', { after: -1 }), Rejected);
  } finally { await f.close(); }
});

test('a Worker is named after its title, with the task number for uniqueness', async () => {
  const titles = ['Review PR 1234', 'Fix "add()" / the tests!', '!!!', 'A very long title that keeps going well past the point where an identifier stays readable'];
  let next = 0;
  const f = await fixture(request => {
    const event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (role(request) !== 'router' || last?.role === 'toolResult' || event.kind !== 'explicit') return text();
    const title = titles[next++ % titles.length];
    return call('start_task', { event: event.event, title, goal: `goal ${title}`, new_reason: 'independent' });
  });
  try {
    for (let i = 0; i < titles.length; i++) {
      await f.app.admit(input(`k${i}`, `request ${i}`, `t${i}`));
      await waitFor(() => f.app.state(), state => Object.keys(state.works).length === i + 1);
    }
    const ids = Object.values((await f.app.state()).works).map(work => work.id).sort();
    assert.equal(ids.length, 4);
    assert.ok(ids.some(id => /^review-pr-1234-\d+$/.test(id)), ids.join(', '));
    assert.ok(ids.some(id => /^fix-add-the-tests-\d+$/.test(id)), ids.join(', '));
    assert.ok(ids.some(id => /^task-\d+$/.test(id)), 'an unslugable title falls back to task-N: ' + ids.join(', '));
    const long = ids.find(id => id.startsWith('a-very-long-title'))!;
    assert.ok(long && long.length <= 48, `long titles are cut: ${long}`);
    assert.ok(ids.every(id => /^[a-z0-9-]+-\d+$/.test(id)), 'ids are lowercase slugs: ' + ids.join(', '));
  } finally { await f.close(); }
});

test('a waiting Worker records since when, and the mark clears when it is woken', async () => {
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!;
    if (request.messages.findLast(message => message.role !== 'system')?.role === 'toolResult') return text();
    if (agent === 'router') return event.kind === 'explicit' ? call('start_task', { event: event.event, title: 'Wait for the user', goal: 'wait' }) : text();
    if (event.kind === 'assignment') return call('report', { status: 'waiting', text: 'Need a decision', outcome: 'needs_decision', next_actor: 'user', next_action: 'Decide' });
    return text();
  });
  try {
    await f.app.admit(input('w', 'wait'));
    const waiting = await waitFor(() => f.app.state(), state => Object.values(state.works).some(work => work.status === 'waiting'));
    const work = Object.values(waiting.works)[0];
    assert.equal(typeof work.waitingAt, 'number');
    assert.ok(Date.now() - work.waitingAt! < 10_000, 'waitingAt is recent');
    const status = await f.app.harness.snapshot(StateDoc, BACKGROUND_CONTEXT);
    assert.equal(status!.works[work.id].waitingAt, work.waitingAt);
    assert.equal(await f.app.admit(input('wake', 'here is my decision')), 'subscribed');
    await waitFor(() => f.app.state(), state => state.works[work.id].waitingAt === undefined);
    const inspected = await f.app.inspectWork(work.id);
    assert.equal(typeof inspected.usage.input, 'number'); assert.equal(typeof inspected.usage.output, 'number'); assert.equal(typeof inspected.usage.cost, 'number');
  } finally { await f.close(); }
});
