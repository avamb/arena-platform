/**
 * F-007: the pure rules of the "Connect Telegram" block (ConnectTelegram.tsx).
 */

/** True once the link's own end has passed (an unreadable end counts as passed). */
export function connectExpired(expiresAt: string, now: number): boolean {
  const end = Date.parse(expiresAt);
  return !Number.isFinite(end) || end <= now;
}

/** Whole minutes the link still works, at least 1 while it works, 0 after. */
export function connectTimeLeft(expiresAt: string, now: number): number {
  if (connectExpired(expiresAt, now)) return 0;
  return Math.max(1, Math.ceil((Date.parse(expiresAt) - now) / 60_000));
}

/** The server's base64 PNG as a Blob, null when it is not base64. */
export function pngBlobOf(base64: string): Blob | null {
  try {
    const raw = atob(base64);
    const bytes = new Uint8Array(raw.length);
    for (let i = 0; i < raw.length; i++) bytes[i] = raw.charCodeAt(i);
    return new Blob([bytes], { type: "image/png" });
  } catch {
    return null;
  }
}

/** How long a link works (hbot.TelegramConnectCodeTTL): its end minus this is when the server made it. */
export const CONNECT_LINK_TTL_MS = 15 * 60_000;

/**
 * True when one of the accounts was linked after the link on screen was made,
 * both on the server's clock (whole seconds): that link was used. A person
 * whose Telegram was linked before keeps the old time and is not "done" yet.
 */
export function connectedSinceLink(accounts: ReadonlyArray<{ linked_at: string }>, linkExpiresAt: string): boolean {
  const end = Date.parse(linkExpiresAt);
  if (!Number.isFinite(end)) return false;
  const made = Math.floor((end - CONNECT_LINK_TTL_MS) / 1000);
  return accounts.some((a) => {
    const at = Date.parse(a.linked_at);
    return Number.isFinite(at) && Math.floor(at / 1000) >= made;
  });
}
