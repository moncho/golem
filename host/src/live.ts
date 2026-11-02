// Opt-in live acceptance test. Sends only the temporary fixture and uses the configured provider.
import assert from 'node:assert/strict';
import { mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { configuredModels } from './models.ts';
import { Golem } from './golem.ts';
import { commandEnvironment } from './executor.ts';
import { StdioExecutor } from './stdio.ts';
import { until } from './demo.ts';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';

async function live() {
  // The process environment only; no configuration file is read.
  const env = process.env;
  const provider = env.GOLEM_MODEL ? env.GOLEM_PROVIDER || (env.GOLEM_BASE_URL ? 'compatible' : 'openai') : 'openai';
  const models = await configuredModels({ provider, modelId: env.GOLEM_MODEL || 'gpt-4.1-mini', routerModelId: env.GOLEM_ROUTER_MODEL || undefined,
    baseUrl: env.GOLEM_BASE_URL || undefined, apiKey: env.GOLEM_API_KEY || env[`${provider.toUpperCase()}_API_KEY`] || undefined });
  const directory = await mkdtemp(join(tmpdir(), 'golem-pi-live-'));
  await writeFile(join(directory, 'calc.py'), 'def add(a, b):\n    return a - b\n');
  await writeFile(join(directory, 'test_calc.py'), 'import unittest\nfrom calc import add\nclass TestAdd(unittest.TestCase):\n    def test_add(self):\n        self.assertEqual(add(2, 3), 5)\n        self.assertEqual(add(-2, 3), 1)\n');
  const endpoint = new StdioExecutor(directory, join(directory, 'execution'));
  let app: Golem | undefined;
  let failure = 'The acceptance conditions were not met.';
  const sent: string[] = [];
  const input = (key: string, text: string) => ({ key, text, thread: { id: 'console:live', source: 'console' as const, channel: '', replyTo: '' }, sender: 'owner', explicit: true });
  const options = { stateDir: join(directory, 'controller'), ...models, environments: { local: endpoint }, pollMs: 100,
    send: async (_thread: unknown, message: string) => { sent.push(message); console.log(`Router: ${message}`); } };
  try {
    await endpoint.ready; app = await Golem.open(options);
    console.log(`Live test: ${models.model.provider}/${models.model.modelId}, temporary code fixture, real commands.`);
    await app.admit(input('start', "Delegate one Worker to fix the add function in calc.py and verify with python3 -m unittest -v test_calc. First, the Worker must start exactly this preparation command: printf 'ready\\n'; sleep 90. After starting it, wait for my followup before inspecting or changing any files. Keep the task active. Avoid milestone replies."));
    const running = await until(async () => {
      const entries = (await app!.root.entries({ order: 'descending' }, 6, undefined, BACKGROUND_CONTEXT)).items;
      for (const entry of entries) for (const message of entry.model ?? []) {
        if (message.role === 'assistant' && message.stopReason === 'error') {
          failure = /credits|billing|quota/i.test(message.errorMessage ?? '') ? 'The provider rejected the request because of billing or credits.' : 'The provider rejected the model request. Check provider access and model configuration.';
          throw new Error('Provider rejected the live request.');
        }
      }
      return app!.state();
    }, state => Object.values(state.commands).some(job => job.status === 'running'), 90000);
    const original = Object.values(running.commands).find(job => job.status === 'running')!;
    await app.close(); app = undefined;
    assert.equal((await endpoint.poll(original.id)).status, 'running');
    app = await Golem.open(options);
    console.log('Live test: Pi reopened; directing the Worker to cancel preparation and finish the code fix.');
    assert.equal(await app.admit(input('redirect', 'Change plan now: stop the running preparation command, inspect calc.py, fix the add function, run python3 -m unittest -v test_calc, and report the verified result. Do not change test_calc.py.')), 'subscribed');
    const done = await until(() => app!.state(), state => Object.values(state.works).some(work => work.status === 'done') && Object.values(state.deliveries).some(delivery => delivery.status === 'sent'), 180000);
    assert.equal(done.commands[original.id].status, 'cancelled');
    const verify = spawnSync('python3', ['-m', 'unittest', '-v', 'test_calc'], { cwd: directory, env: commandEnvironment(), encoding: 'utf8', timeout: 10000 });
    assert.equal(verify.status, 0, 'Independent fixture verification failed.');
    assert.ok(sent.length > 0);
    console.log('PASS: a live model resumed through Pi, stopped its command on followup, repaired the fixture, verified it, and returned the result through the Router.');
  } catch (error) {
    console.error(failure);
    throw error;
  } finally { await app?.close(); await endpoint.close(); await rm(directory, { recursive: true, force: true }); }
}
live().catch(() => { console.error('Live acceptance failed. Check model configuration and provider access; no live result has been certified.'); process.exitCode = 1; });
