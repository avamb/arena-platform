/**
 * Is a newer event center deployed than the one this window runs?
 *
 * The app's code is loaded once, when the window opens. An installed app
 * (PWA) or a tab left open for days keeps running the build it started
 * with: signing out and back in does not reload the page, so a deploy goes
 * unseen (owner, 2026-11-08). Every build writes its id into
 * /version.json (vite.config.ts) and into the bundle (VITE_BUILD_ID); the
 * window compares the two and offers a reload. It never reloads by itself,
 * so a half-typed form is not lost.
 */

/** The id of the build this window runs ("dev" outside a production build). */
export const RUNNING_BUILD: string = String(import.meta.env.VITE_BUILD_ID ?? "dev");

/** How often an open window asks, and the least time between two asks. */
export const CHECK_EVERY_MS = 5 * 60 * 1000;
export const MIN_GAP_MS = 60 * 1000;

/** The build id /version.json names, or null when it cannot be read. */
export function parseVersionFile(body: unknown): string | null {
  if (typeof body !== "object" || body === null) return null;
  const build = (body as { build?: unknown }).build;
  return typeof build === "string" && /^[A-Za-z0-9._-]{1,64}$/.test(build) ? build : null;
}

/** True when the server names another build than the running one. */
export function isNewBuild(running: string, deployed: string | null): boolean {
  return deployed !== null && running !== "dev" && deployed !== running;
}

/** Reads the deployed build id; null on any failure (offline, 404, bad JSON). */
export async function fetchDeployedBuild(
  fetchImpl: typeof fetch = fetch,
  now: () => number = Date.now,
): Promise<string | null> {
  try {
    const res = await fetchImpl(`/version.json?t=${now()}`, { cache: "no-store", credentials: "omit" });
    if (!res.ok) return null;
    return parseVersionFile(await res.json());
  } catch {
    return null;
  }
}
