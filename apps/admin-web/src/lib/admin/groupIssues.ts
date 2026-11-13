/**
 * Collapses equal server issue texts: each distinct text appears once, in the order
 * of its first appearance, with " (N times)" added when it occurs more than once.
 */
export function groupIssues(texts: readonly string[]): string[] {
  const counts = new Map<string, number>();
  for (const t of texts) counts.set(t, (counts.get(t) ?? 0) + 1);
  return [...counts].map(([text, n]) => (n > 1 ? `${text} (${n} times)` : text));
}
