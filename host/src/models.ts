import { createModels, createProvider, envApiKeyAuth, InMemoryCredentialStore, type MutableModels } from '@earendil-works/pi-ai';
import { openAICompletionsApi } from '@earendil-works/pi-ai/api/openai-completions.lazy';
import { openaiProvider } from '@earendil-works/pi-ai/providers/openai';
import { anthropicProvider } from '@earendil-works/pi-ai/providers/anthropic';
import { deepseekProvider } from '@earendil-works/pi-ai/providers/deepseek';
import { fauxProvider, type FauxResponseStep } from '@earendil-works/pi-ai/providers/faux';
import type { ModelRef } from '@earendil-works/pi-durable';
import { scriptedDecision } from './demo.ts';

export type ModelParams = { provider: string; modelId: string; routerModelId?: string; baseUrl?: string; apiKey?: string };

export async function configuredModels(params: ModelParams): Promise<{ models: MutableModels; model: ModelRef; routerModel: ModelRef; responses?: FauxResponseStep }> {
  const credentials = new InMemoryCredentialStore();
  // Keys come only from params.apiKey via the credential store; ambient environment variables never configure a model.
  const models = createModels({ credentials, identity: { attribution: null }, authContext: { env: async () => undefined, fileExists: async () => false } });
  const { provider, modelId, baseUrl } = params;
  if (provider === 'faux') {
    // Scripted decisions for the demo and protocol tests: no network or API key.
    const faux = fauxProvider({ tokensPerSecond: 0 }); models.setProvider(faux.provider);
    faux.setResponses(Array.from({ length: 1000 }, () => scriptedDecision));
    const model = { provider: 'faux', modelId: 'faux-1' };
    return { models, model, routerModel: model, responses: scriptedDecision };
  }
  const native = { openai: openaiProvider, anthropic: anthropicProvider, deepseek: deepseekProvider };
  if (Object.hasOwn(native, provider)) models.setProvider(native[provider as keyof typeof native]());
  else if (provider === 'compatible') {
    if (!baseUrl) throw new Error('Compatible providers require GOLEM_BASE_URL.');
    // Pi owns the protocol implementation, model catalog and request lifecycle.
    const ids = [...new Set([modelId, params.routerModelId].filter((id): id is string => !!id))];
    models.setProvider(createProvider({ id: provider, baseUrl,
      auth: { apiKey: envApiKeyAuth('Compatible API key', []) },
      models: ids.map(id => ({ id, name: id, provider, baseUrl, api: 'openai-completions' as const, reasoning: false, input: ['text' as const], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 128000, maxTokens: 8192 })),
      api: openAICompletionsApi(),
    }));
  } else throw new Error('Unknown provider. Choose openai, anthropic, deepseek or compatible.');
  if (params.apiKey) { const key = params.apiKey; await credentials.modify(provider, async () => ({ type: 'api_key', key })); }
  const model = { provider, modelId }, routerModel = { provider, modelId: params.routerModelId || modelId };
  if (!models.getModel(provider, model.modelId) || !models.getModel(provider, routerModel.modelId)) throw new Error('Configured model is not in the selected Pi provider catalog.');
  return { models, model, routerModel };
}
