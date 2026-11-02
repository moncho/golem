import { LiveDoc, UsageDoc, type Harness } from '@earendil-works/pi-durable';
import { BACKGROUND_CONTEXT } from '@earendil-works/chord/context';
import { StateDoc, conversationId } from './state.ts';

export async function status(harness: Harness) {
  const state = await harness.snapshot(StateDoc, BACKGROUND_CONTEXT);
  const cost = async (conversation: number) => {
    const doc = await harness.snapshot(UsageDoc, conversationId(conversation), BACKGROUND_CONTEXT);
    return Object.values(doc?.models ?? {}).reduce((sum, usage) => sum + (usage.cost?.total ?? 0), 0);
  };
  const failures: { conversation: number; reason: string }[] = [];
  if (state) for (const id of [state.root, state.watchdog, ...Object.values(state.works).map(work => work.conversation)]) {
    const live = await harness.snapshot(LiveDoc, conversationId(id), BACKGROUND_CONTEXT);
    if (live?.runFailure) failures.push({ conversation: id, reason: live.runFailure.reason });
  }
  return state ? { tasks: await Promise.all(Object.values(state.works).map(async work => ({ id: work.id, title: work.title, status: work.status, environment: work.environment, threads: work.threads, outcome: work.outcome ?? (work.status === 'done' ? 'resolved' : 'open'), summary: work.summary, nextActor: work.nextActor, nextAction: work.nextAction, checkAt: work.checkAt, endedAt: work.endedAt, waitingAt: work.waitingAt, cost: await cost(work.conversation) }))),
    commands: Object.values(state.commands).map(job => ({ id: job.id, work: job.work, status: job.status, stopRequested: job.stop })),
    queuedReplies: Object.values(state.deliveries).filter(item => item.status === 'queued').length,
    failedReplies: Object.values(state.deliveries).filter(item => item.status === 'failed').map(item => ({ id: item.id, thread: item.thread, attempts: item.attempts })),
    timers: Object.values(state.timers).filter(timer => timer.active), failures } : { tasks: [], commands: [], queuedReplies: 0, failedReplies: [], timers: [], failures };
}
