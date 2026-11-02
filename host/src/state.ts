import { defineDoc, type ConversationId, type EntryId, type TaskId } from '@earendil-works/pi-durable';
import { randomUUID } from 'node:crypto';

export type Role = 'router' | 'worker' | 'watchdog';
// engaged marks a thread Golem was addressed in or replied to; later replies
// there count as requests without a mention.
export type Thread = { id: string; source: 'console' | 'slack'; channel: string; replyTo: string; engaged?: boolean };
export type Event = {
  key: string; thread: string; kind: string; text: string; sender: string;
  work: string; publish: boolean; explicit: boolean; targets: number[]; at: number;
};
export type Work = {
  id: string; title: string; goal: string; conversation: number; origin: string;
  threads: string[]; environment: string; directory: string;
  status: 'active' | 'waiting' | 'done' | 'cancelled'; speculative: boolean;
  progressAt: number; auditedAt: number; endedAt?: number;
  summary?: string;
  outcome?: 'open' | 'waiting_reply' | 'needs_decision' | 'check_later' | 'resolved';
  nextActor?: string; nextAction?: string; checkAt?: number; followupVersion?: number;
  // waitingAt is set while the Worker waits on someone else and cleared when it is woken.
  waitingAt?: number;
  abortPending?: boolean;
};
export type CommandSpec = { id: string; command: string; directory: string; timeout_seconds: number };
export type CommandResult = {
  spec: CommandSpec; status: string; exit_code: number; note?: string; truncated?: boolean;
};
export type Command = {
  id: string; work: string; environment: string; spec: CommandSpec;
  status: string; stop: boolean; notified: boolean; result: CommandResult | null; createdAt: number;
};
export type Delivery = { id: string; sequence: number; thread: string; text: string; status: 'queued' | 'sent' | 'failed'; attempts: number; at: number };
export type Timer = { id: string; thread: string; text: string; due: number; every: number; active: boolean };
export type State = {
  namespace: string; nextDelivery: number; root: number; watchdog: number; maintenance: number;
  threads: Record<string, Thread>; works: Record<string, Work>;
  commands: Record<string, Command>; deliveries: Record<string, Delivery>;
  timers: Record<string, Timer>; eventKeys: Record<string, number>; recentEvents: number[];
};
export const StateDoc = defineDoc<State>({
  kind: 'golem.state', version: 1, scope: 'session',
  checkpointWhen: (_value, _ops, info) => info.deltasSinceBase >= 100,
  initial: () => ({ namespace: randomUUID(), nextDelivery: 0, root: 0, watchdog: 0, maintenance: 0, threads: {}, works: {}, commands: {}, deliveries: {}, timers: {}, eventKeys: {}, recentEvents: [] }),
});
export const RoleDoc = defineDoc<{ role: Role; work: string }>({
  kind: 'golem.role', version: 1, scope: 'conversation', history: 'latest', fork: 'initial',
  checkpointWhen: () => true, initial: () => ({ role: 'router', work: '' }),
});
// Finished task metadata moves here when hot execution state is pruned.
// Conversations remain in Pi; Herma remains the shared knowledge store.
export const ArchiveDoc = defineDoc<{ works: Record<string, Work> }>({
  kind: 'golem.archive', version: 1, scope: 'session', initial: () => ({ works: {} }), checkpointWhen: (_value, _ops, info) => info.deltasSinceBase >= 100,
});
// Retired by Pi when the tool task finishes. It makes Golem mutations replay-safe
// even if the process stops after our commit but before Pi stores the tool result.
export const EffectDoc = defineDoc<{ result: string | null }>({
  kind: 'golem.effect', version: 1, scope: 'task', checkpointWhen: () => true,
  initial: () => ({ result: null }),
});
export const conversationId = (id: number) => id as ConversationId;
export const entryId = (id: number) => id as EntryId;
export const taskId = (id: number) => id as TaskId;
export const active = (work: Work) => work.status === 'active' || work.status === 'waiting';
export const finished = (status: string) => ['done', 'failed', 'cancelled', 'interrupted'].includes(status);
export const excerpt = (text: string, limit = 1200) => text.length > limit ? `${text.slice(0, limit)}… [use recall for the full event]` : text;
