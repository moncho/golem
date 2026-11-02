import type { Work } from './state.ts';
const words = (text: string) => [...new Set(text.toLowerCase().match(/[\p{L}\p{N}_-]+/gu) ?? [])];
export function findWork(works: Work[], query: string, thread = '') {
  const terms = words(query).filter(word => word.length > 2 && !['the', 'and', 'for', 'task', 'this', 'with'].includes(word));
  return works.map(work => {
    const text = `${work.title} ${work.goal} ${work.summary ?? ''}`.toLowerCase(), tokens = words(text);
    const matches = terms.filter(term => tokens.includes(term)).length;
    const exact = work.id === query || work.goal.toLowerCase() === query.toLowerCase();
    const linked = !!thread && work.threads.includes(thread);
    return { work, score: (exact ? 100 : 0) + matches * 3 + (linked ? 2 : 0), matches, ratio: terms.length ? matches / terms.length : 0, exact, linked };
  }).filter(item => !query.trim() || item.exact || item.linked || item.matches > 0)
    .sort((a, b) => b.score - a.score || b.work.progressAt - a.work.progressAt).slice(0, 20);
}
