/**
 * Asks the platform's check about EVERY date of a draft (task 6-1b).
 *
 * The import takes one date per request, so a draft with three dates costs
 * three checks. The scheduler (validation.ts) keeps the pace: one run at a
 * time, after a pause in typing. This runner asks the dates of a run one after
 * another and remembers the answer to each bundle it has seen, so a change to
 * ONE date asks about that date only, not about all of them again.
 *
 * The payload of a run is the JSON text of the array of bundles; it is also
 * the identity the scheduler compares.
 */
import type { CheckAnswer } from "@/lib/wizard/validation";

export interface MultiAnswer {
  /** One answer per date, in the order of the bundles. */
  answers: CheckAnswer[];
}

/** The most answers kept for bundles that are no longer in the draft. */
const SPARE = 8;

export function createCheckRunner(post: (bundle: unknown) => Promise<CheckAnswer>): (payload: string) => Promise<MultiAnswer> {
  const cache = new Map<string, CheckAnswer>();
  return async (payload) => {
    const bundles = JSON.parse(payload) as unknown[];
    const keys = bundles.map((b) => JSON.stringify(b));
    const answers: CheckAnswer[] = [];
    for (let i = 0; i < bundles.length; i += 1) {
      let answer = cache.get(keys[i]);
      if (answer === undefined) {
        answer = await post(bundles[i]);
        cache.set(keys[i], answer);
      }
      answers.push(answer);
    }
    // Forget old answers: the draft changes all the time, the cache must not grow.
    const keep = new Set(keys);
    let spare = SPARE;
    for (const k of [...cache.keys()].reverse()) {
      if (keep.has(k)) continue;
      if (spare > 0) spare -= 1;
      else cache.delete(k);
    }
    return { answers };
  };
}
