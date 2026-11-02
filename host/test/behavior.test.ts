import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, text, input, waitFor } from './support.ts';
import { getCurrentSystemPrompt } from '@earendil-works/pi-ai';

test('Pi receives revised Herma behavior on the next request without restarting', async () => {
  let principle = 'Use short replies.'; const prompts: string[] = [];
  const f = await fixture(request => { prompts.push(getCurrentSystemPrompt(request.messages)); return text(); }, { behavior: async () => principle });
  try {
    await f.app.admit(input('first-principle', 'hello'));
    await waitFor(async () => prompts, values => values.some(value => value.includes(principle)));
    principle = 'Explain decisions with examples.';
    await f.app.admit(input('second-principle', 'hello again'));
    await waitFor(async () => prompts, values => values.some(value => value.includes(principle)));
    assert.ok(prompts.at(-1)!.includes('only role allowed to communicate'));
  } finally { await f.close(); }
});
