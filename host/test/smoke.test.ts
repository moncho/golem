import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, events, input, waitFor } from './support.ts';

test('Pi runs the Router and persists a reply through its durable task scheduler', async () => {
  const f = await fixture(request => {
    const last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role === 'toolResult') return text();
    const event = events(request).at(-1)!;
    return call('reply', { event: event.event, text: 'Hello from Pi-backed Golem.' });
  });
  try {
    await f.app.admit(input('hello', 'hello'));
    await waitFor(async () => f.sent, values => values.length === 1);
    assert.equal(f.sent[0].text, 'Hello from Pi-backed Golem.');
    await f.restart();
    assert.equal(Object.values((await f.app.state()).deliveries)[0].status, 'sent');
    assert.equal(await f.app.admit(input('hello', 'hello')), 'duplicate');
  } finally { await f.close(); }
});
