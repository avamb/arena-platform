/**
 * The orders search text (an e-mail, a phone, a name) of this tab, per
 * organization. It stays in memory, never in the page address: an address
 * lands in the browser history and in proxy logs. It survives a visit to an
 * order card, is lost on reload and is wiped when the person signs out, so
 * the next person at the same computer never sees it.
 */
const memory = new Map<string, string>();

export function rememberedOrderSearch(orgId: string): string {
  return memory.get(orgId) ?? "";
}

export function rememberOrderSearch(orgId: string, query: string): void {
  if (query.length === 0) memory.delete(orgId);
  else memory.set(orgId, query);
}

export function forgetOrderSearches(): void {
  memory.clear();
}
