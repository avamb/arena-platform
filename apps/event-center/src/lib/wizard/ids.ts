/** A fresh key for a draft, a date, a category or a price change. Never sent to the server as a name. */
export function makeId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
  }
}
