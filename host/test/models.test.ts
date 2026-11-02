import test from 'node:test';
import assert from 'node:assert/strict';
import { configuredModels } from '../src/models.ts';
import { scriptedDecision } from '../src/demo.ts';

test('provider setup uses pinned Pi adapters and accepts a custom compatible endpoint', async () => {
  const native = await configuredModels({ provider: 'openai', modelId: 'gpt-4.1-mini', apiKey: 'test-only' });
  assert.equal(native.models.getModel('openai', 'gpt-4.1-mini')?.api, 'openai-responses');
  assert.deepEqual(native.routerModel, { provider: 'openai', modelId: 'gpt-4.1-mini' });
  const compatible = await configuredModels({ provider: 'compatible', modelId: 'custom', routerModelId: 'small', baseUrl: 'http://models.test/v1', apiKey: 'test-only' });
  assert.equal(compatible.models.getModel('compatible', 'custom')?.baseUrl, 'http://models.test/v1');
  assert.deepEqual(compatible.routerModel, { provider: 'compatible', modelId: 'small' });
  await assert.rejects(configuredModels({ provider: 'compatible', modelId: 'custom' }), /^Error: Compatible providers require GOLEM_BASE_URL\.$/);
  await assert.rejects(configuredModels({ provider: 'openai', modelId: 'not-a-catalog-model' }), /^Error: Configured model is not in the selected Pi provider catalog\.$/);
  await assert.rejects(configuredModels({ provider: 'other', modelId: 'x' }), /^Error: Unknown provider\. Choose openai, anthropic, deepseek or compatible\.$/);
});

test('faux returns scripted responses', async () => {
  const faux = await configuredModels({ provider: 'faux', modelId: 'faux-1' });
  assert.deepEqual(faux.model, { provider: 'faux', modelId: 'faux-1' });
  assert.deepEqual(faux.routerModel, faux.model);
  assert.ok(faux.models.getModel('faux', 'faux-1'));
  assert.equal(faux.responses, scriptedDecision);
});

test('the model key comes only from the parameters, never from the environment', async () => {
  const saved = { GOLEM_API_KEY: process.env.GOLEM_API_KEY, OPENAI_API_KEY: process.env.OPENAI_API_KEY };
  process.env.GOLEM_API_KEY = 'ambient-key'; process.env.OPENAI_API_KEY = 'ambient-key';
  try {
    const compatible = await configuredModels({ provider: 'compatible', modelId: 'custom', baseUrl: 'http://models.test/v1' });
    assert.equal(await compatible.models.getAuth('compatible'), undefined);
    const native = await configuredModels({ provider: 'openai', modelId: 'gpt-4.1-mini' });
    assert.equal(await native.models.getAuth('openai'), undefined);
    const given = await configuredModels({ provider: 'compatible', modelId: 'custom', baseUrl: 'http://models.test/v1', apiKey: 'init-key' });
    assert.equal((await given.models.getAuth('compatible'))?.auth.apiKey, 'init-key');
  } finally {
    for (const [key, value] of Object.entries(saved)) { if (value === undefined) delete process.env[key]; else process.env[key] = value; }
  }
});
