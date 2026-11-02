import type { TranscriptContext } from '@earendil-works/pi-ai';
import { fauxAssistantMessage, fauxToolCall } from '@earendil-works/pi-ai/providers/faux';
import type { Event } from './state.ts';

export async function until<T>(read: () => Promise<T>, accepts: (value: T) => boolean, timeout = 10000): Promise<T> {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const value = await read(); if (accepts(value)) return value;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  throw new Error('Demo condition did not complete.');
}
export function scriptedDecision(request: TranscriptContext) {
  const say = () => fauxAssistantMessage('Waiting for an event.');
  const call = (name: string, args: Parameters<typeof fauxToolCall>[1]) => fauxAssistantMessage(fauxToolCall(name, args), { stopReason: 'toolUse' });
  const system = JSON.stringify(request.messages.filter(message => message.role === 'system'));
  const last = request.messages.findLast(message => message.role !== 'system');
  const incoming = request.messages.filter(message => message.role === 'user').map(message => {
    const content = typeof message.content === 'string' ? message.content : message.content.filter(block => block.type === 'text').map(block => block.text).join('');
    return JSON.parse(content) as Event & { event: number };
  });
  const event = incoming.at(-1)!;
  if (system.includes('You are the Router')) {
    if (last?.role === 'toolResult') return say();
    if (event.kind === 'explicit') return call('start_task', { event: event.event, title: event.text, goal: event.text, ...(event.text.includes('remotely') ? { environment: 'remote' } : {}) });
    if (event.kind === 'worker_report') return call('reply', { event: event.event, text: JSON.parse(event.text).text });
    return say();
  }
  if (system.includes('You are the Watchdog')) return say();
  if (last?.role === 'toolResult') {
    const value = JSON.parse(last.content.filter(block => block.type === 'text').map(block => block.text).join(''));
    if (last.toolName === 'stop_command') return call('start_command', { command: "printf 'updated result'" });
    if (last.toolName === 'read_output') return call('report', { status: 'done', text: value.output, useful: true, evidence_job: value.job });
    return say();
  }
  if (event.kind === 'assignment') return call('start_command', { command: event.text.includes('independent') ? "printf 'independent result'" : "printf 'started\n'; sleep 60; printf 'obsolete result'", timeout_seconds: 90 });
  // Pi may steer several queued events together. Decisions consider all of them.
  const redirected = incoming.some(event => event.kind === 'explicit' && event.text === 'change plan');
  const stopped = request.messages.some(message => message.role === 'toolResult' && message.toolName === 'stop_command');
  if (redirected && !stopped) {
    const started = request.messages.find(message => message.role === 'toolResult' && message.toolName === 'start_command');
    if (!started || started.role !== 'toolResult') throw new Error('Missing prior command.');
    const result = JSON.parse(started.content.filter(block => block.type === 'text').map(block => block.text).join(''));
    return call('stop_command', { job: result.job });
  }
  if (redirected && stopped && request.messages.filter(message => message.role === 'toolResult' && message.toolName === 'start_command').length < 2) return call('start_command', { command: "printf 'updated result'" });
  if (event.kind === 'command_finished') {
    const job = incoming.filter(event => event.kind === 'command_finished').map(event => JSON.parse(event.text)).findLast(job => job.status === 'done');
    if (job) return call('read_output', { job: job.job });
  }
  return say();
}
