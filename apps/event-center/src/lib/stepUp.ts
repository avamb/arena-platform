/**
 * The password again before a money action (owner decision 2026-10-14,
 * spec 37 section 4.6). POST /v1/auth/step-up returns a token for five
 * minutes; every money request of the web carries it in X-Step-Up-Token,
 * and arena-api refuses a money request from the web without it (403
 * auth.step_up_required).
 *
 * The token lives in memory only: never in storage, never in the address.
 * Sign-out forgets it. Refunds arrive in phase R5; this is the mechanism
 * they call (`withStepUp`).
 */
import { ApiError, call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { AuthStepUpResponse } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

export const STEP_UP_HEADER = "X-Step-Up-Token";

/** A token this close to its end is asked for again: the request may be slow. */
const MARGIN_MS = 30_000;

export class StepUpStore {
  private token: string | null = null;
  private expiresAt = 0;

  constructor(private readonly now: () => number = Date.now) {}

  /** The token while it still covers a request, else null. */
  current(): string | null {
    if (this.token === null || this.expiresAt - this.now() <= MARGIN_MS) return null;
    return this.token;
  }

  set(token: string, expiresAt: number): void {
    this.token = token;
    this.expiresAt = expiresAt;
  }

  clear(): void {
    this.token = null;
    this.expiresAt = 0;
  }
}

export const stepUp = new StepUpStore();

/** Checks the password and keeps the token. Throws ApiError on a refusal. */
export async function confirmPassword(password: string, store: StepUpStore = stepUp): Promise<void> {
  const res = await call<AuthStepUpResponse>({ route: API.stepUp, body: { password } });
  store.set(res.step_up_token, Date.parse(res.expires_at));
}

/** The person closed the password question: nothing was sent. */
export class StepUpCancelled extends Error {
  constructor() {
    super("The password was not confirmed.");
    this.name = "StepUpCancelled";
  }
}

/**
 * Runs a money request with the step-up header. `ask` opens the password
 * question and resolves true once the password is confirmed (false: the
 * person cancelled). When the server still says auth.step_up_required (the
 * token ran out on the way), it asks once more.
 */
export async function withStepUp<T>(
  run: (headers: Record<string, string>) => Promise<T>,
  ask: () => Promise<boolean>,
  store: StepUpStore = stepUp,
): Promise<T> {
  for (let attempt = 0; attempt < 2; attempt++) {
    let token = store.current();
    if (token === null) {
      if (!(await ask())) throw new StepUpCancelled();
      token = store.current();
      if (token === null) throw new StepUpCancelled();
    }
    try {
      return await run({ [STEP_UP_HEADER]: token });
    } catch (err) {
      if (err instanceof ApiError && err.code === "auth.step_up_required" && attempt === 0) {
        store.clear();
        continue;
      }
      throw err;
    }
  }
  throw new StepUpCancelled();
}

/** Errors of POST /v1/auth/step-up. Each one says what to do next. */
export function stepUpErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.code === "auth.step_up_wrong_password") return "stepUp.error.wrong";
    if (err.code === "auth.rate_limited" || err.status === 429) return "stepUp.error.tooMany";
    if (err.code === "validation.password_required") return "stepUp.error.empty";
  }
  return "common.error.generic";
}
