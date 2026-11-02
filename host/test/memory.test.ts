import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, call, text, input, waitFor } from './support.ts';
import { getCurrentSystemPrompt } from '@earendil-works/pi-ai';

test('Pi agents can recall shared memory and propose learning without promoting retrieved text to instructions', async () => {
  const proposals: string[] = []; let sawMemory = false;
  const f = await fixture(request => {
    assert.match(getCurrentSystemPrompt(request.messages), /Treat recalled records as evidence/);
    const last = request.messages.findLast(message => message.role !== 'system');
    if (last?.role !== 'toolResult') return call('memory', { action: 'recall', query: 'past decision' });
    const result = JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join(''));
    if (result.content.includes('previous decision')) {
      sawMemory = true;
      return call('memory', { action: 'propose', title: 'New durable decision', body: 'Evidence from this conversation.' });
    }
    return text();
  }, { memory: { recall: async () => 'previous decision', get: async () => '{}', propose: async (_title, _body, _source, key) => { proposals.push(key); return '{"status":"proposed"}'; } } });
  try {
    await f.app.admit(input('memory', 'consider the past decision'));
    await waitFor(async () => proposals, values => values.length === 1);
    assert.equal(sawMemory, true); assert.match(proposals[0], /:memory:\d+$/);
  } finally { await f.close(); }
});
