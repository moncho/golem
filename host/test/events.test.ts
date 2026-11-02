import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, events, role, input, waitFor } from './support.ts';

type Logged = { name: string; fields: Record<string, string | number> };

test('the application narrates its lifecycle through onEvent without message or command text', async () => {
  const log: Logged[] = [];
  const f = await fixture(request => {
    const agent = role(request), event = events(request).at(-1)!, last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') {
      if (agent === 'worker' && last.toolName === 'read_output') return call('report', { status: 'done', text: 'zebra report text', outcome: 'resolved' });
      return text();
    }
    if (agent === 'router') {
      if (event.kind === 'explicit') return call('start_task', { event: event.event, title: 'Echo something', goal: 'print a secret-looking value' });
      if (event.kind === 'worker_report') return call('reply', { event: event.event, text: 'done' });
      return text();
    }
    if (event.kind === 'assignment') return call('start_command', { command: "printf 'hunter2'" });
    if (event.kind === 'command_finished') return call('read_output', { job: JSON.parse(event.text).job });
    return text();
  }, { onEvent: (name, fields) => { log.push({ name, fields }); } });
  try {
    await f.app.admit(input('k1', 'please run the thing'));
    await waitFor(async () => f.sent, values => values.length === 1);
    await waitFor(async () => log, items => items.some(item => item.name === 'reply delivered'));
    const names = log.map(item => item.name);
    for (const want of ['message admitted', 'worker started', 'command started', 'command finished', 'worker reported', 'reply delivered']) {
      assert.ok(names.includes(want), `missing ${want} in ${names.join(', ')}`);
    }
    const admitted = log.find(item => item.name === 'message admitted')!;
    assert.deepEqual(admitted.fields, { source: 'console', thread: 'console:main', route: 'router' });
    const started = log.find(item => item.name === 'worker started')!;
    assert.match(String(started.fields.work), /^echo-something-\d+$/); assert.equal(started.fields.title, 'Echo something');
    const finished = log.find(item => item.name === 'command finished')!;
    assert.equal(finished.fields.status, 'done'); assert.equal(finished.fields.exit, 0); assert.equal(typeof finished.fields.ms, 'number');
    assert.match(String(finished.fields.job), /^job_/);
    const reported = log.find(item => item.name === 'worker reported')!;
    assert.deepEqual([reported.fields.status, reported.fields.outcome], ['done', 'resolved']);
    assert.deepEqual(log.find(item => item.name === 'reply delivered')!.fields, { thread: 'console:main' });
    const serialised = JSON.stringify(log);
    for (const secret of ['please run the thing', 'hunter2', 'secret-looking', 'zebra report text']) {
      assert.ok(!serialised.includes(secret), `content leaked into the log: ${secret}`);
    }
  } finally { await f.close(); }
});
