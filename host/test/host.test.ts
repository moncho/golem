import test from 'node:test';
import assert from 'node:assert/strict';
import { fixture, text, waitFor } from './support.ts';

test('output chunks report the bytes consumed so offsets stay exact across split multibyte characters', async () => {
  const f = await fixture(() => text());
  try {
    await f.executor.start({ id: 'bytes', command: "printf 'éé'", directory: '.', timeout_seconds: 5 });
    await waitFor(() => f.executor.poll('bytes'), job => job.status === 'done');
    const first = await f.executor.output('bytes', 0, 3);
    assert.equal(first.bytes, 3); assert.equal(first.output, 'é�');
    const rest = await f.executor.output('bytes', first.bytes, 100);
    assert.equal(rest.bytes, 1); assert.equal(rest.output, '�');
  } finally { await f.close(); }
});
