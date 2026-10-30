/**
 * The password again before a money action (owner decision 2026-10-14,
 * spec 37 section 4.6). POST /v1/auth/step-up returns a token for five
 * minutes; every money request of the web carries it in X-Step-Up-Token,
 * and arena-api refuses a money request from the web without it (403
 * auth.step_up_required).
 *
 * The token is made for ONE organization and for this sign-in: the server
 * refuses it in another organization and after a sign-out. So the store
 * keeps the organization next to the token and gives it out only for that
 * organization.
 *
 * The token is NOT kept in the address or in localStorage. It is kept in
 * memory and mirrored in the tab's sessionStorage (M-11): the dialog promises
 * that the confirmation lasts 5 minutes, and a full page load (opening the
 * next order by its address, a reload) used to wipe the memory copy and ask
 * for the password again. sessionStorage dies with the tab, is read only for
 * the same organization and until the token's end, and the refresh token of
 * the sign-in already sits in the same storage, so this adds no reach to
 * anyone who could read it. Sign-out forgets both copies.
 */
import { ApiError, call } from "@/lib/api/http";
import { API } from "@/lib/api/routes";
import type { AuthStepUpResponse } from "@/lib/api/types";
import type { MessageKey } from "@/lib/i18n/en";

/** The part of Web Storage the store uses (so it can be tested without a browser). */
export interface StepUpStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

const STORAGE_KEY = "arena.events.stepup";

/** The tab's sessionStorage, or null when the browser refuses it. */
function tabStorage(): StepUpStorage | null {
  try {
    return typeof sessionStorage === "undefined" ? null : sessionStorage;
  } catch {
    return null;
  }
}

export const STEP_UP_HEADER = "X-Step-Up-Token";

/** A token this close to its end is asked for again: the request may be slow. */
const MARGIN_MS = 30_000;

export class StepUpStore {
  private token: string | null = null;
  private orgId: string | null = null;
  private expiresAt = 0;

  constructor(
    private readonly now: () => number = Date.now,
    private readonly storage: StepUpStorage | null = null,
  ) {}

  /** The token of orgId while it still covers a request, else null. */
  current(orgId: string): string | null {
    if (this.token === null) this.restore();
    if (this.token === null || this.orgId !== orgId || this.expiresAt - this.now() <= MARGIN_MS) return null;
    return this.token;
  }

  set(token: string, orgId: string, expiresAt: number): void {
    this.token = token;
    this.orgId = orgId;
    this.expiresAt = expiresAt;
    try {
      this.storage?.setItem(STORAGE_KEY, JSON.stringify({ token, orgId, expiresAt }));
    } catch {
      // Storage full or blocked: the memory copy still works until a reload.
    }
  }

  clear(): void {
    this.token = null;
    this.orgId = null;
    this.expiresAt = 0;
    try {
      this.storage?.removeItem(STORAGE_KEY);
    } catch {
      // Nothing to forget in a storage that cannot be reached.
    }
  }

  /** After a page load the memory is empty: take back the tab's copy while it is still valid. */
  private restore(): void {
    if (this.storage === null) return;
    try {
      const raw = this.storage.getItem(STORAGE_KEY);
      if (raw === null) return;
      const v = JSON.parse(raw) as { token?: unknown; orgId?: unknown; expiresAt?: unknown };
      if (typeof v.token === "string" && typeof v.orgId === "string" && typeof v.expiresAt === "number" && v.expiresAt - this.now() > MARGIN_MS) {
        this.token = v.token;
        this.orgId = v.orgId;
        this.expiresAt = v.expiresAt;
      } else {
        this.storage.removeItem(STORAGE_KEY);
      }
    } catch {
      // A damaged copy is no confirmation: the password is asked again.
    }
  }
}

export const stepUp = new StepUpStore(Date.now, tabStorage());

/** Checks the password for orgId and keeps the token. Throws ApiError on a refusal. */
export async function confirmPassword(password: string, orgId: string, store: StepUpStore = stepUp): Promise<void> {
  const res = await call<AuthStepUpResponse>({ route: API.stepUp, body: { password, org_id: orgId } });
  store.set(res.step_up_token, orgId, Date.parse(res.expires_at));
}

/** The person closed the password question: nothing was sent. */
export class StepUpCancelled extends Error {
  constructor() {
    super("The password was not confirmed.");
    this.name = "StepUpCancelled";
  }
}

/**
 * Runs a money request of organization orgId with the step-up header. `ask`
 * opens the password question and resolves true once the password is
 * confirmed (false: the person cancelled). When the server still says
 * auth.step_up_required (the token ran out on the way), it asks once more.
 */
export async function withStepUp<T>(
  orgId: string,
  run: (headers: Record<string, string>) => Promise<T>,
  ask: (orgId: string) => Promise<boolean>,
  store: StepUpStore = stepUp,
): Promise<T> {
  for (let attempt = 0; attempt < 2; attempt++) {
    let token = store.current(orgId);
    if (token === null) {
      if (!(await ask(orgId))) throw new StepUpCancelled();
      token = store.current(orgId);
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

/**
 * The one open password question. A second ask() while the question is on
 * screen does not open a second one and does not drop the first caller:
 * it waits for the same answer. Every caller is told the answer once.
 */
export class StepUpQuestion {
  private waiters: Array<(ok: boolean) => void> = [];
  private openOrg: string | null = null;

  constructor(private readonly onChange: (orgId: string | null, intro?: MessageKey) => void) {}

  /** The organization the open question is for, null when none is open. */
  get org(): string | null {
    return this.openOrg;
  }

  /** `intro` replaces the dialog's text when the action moves no money (the first asker of an open question wins). */
  ask(orgId: string, intro?: MessageKey): Promise<boolean> {
    return new Promise<boolean>((resolve) => {
      this.waiters.push(resolve);
      if (this.openOrg === null) {
        this.openOrg = orgId;
        this.onChange(orgId, intro);
      }
    });
  }

  /** The person answered: confirmed (true) or cancelled (false). */
  answer(ok: boolean): void {
    const waiters = this.waiters;
    this.waiters = [];
    this.openOrg = null;
    this.onChange(null);
    for (const resolve of waiters) resolve(ok);
  }
}

/**
 * Where Tab moves the focus inside a dialog with `count` focusable elements
 * when the focus is on element `current` (-1: outside them). It never
 * leaves the dialog: past the last it goes to the first, before the first
 * to the last.
 */
export function trapFocusIndex(count: number, current: number, backwards: boolean): number {
  if (count <= 0) return -1;
  if (current < 0) return backwards ? count - 1 : 0;
  if (backwards) return current === 0 ? count - 1 : current - 1;
  return current === count - 1 ? 0 : current + 1;
}

/** Errors of POST /v1/auth/step-up. Each one says what to do next. */
export function stepUpErrorKey(err: unknown): MessageKey {
  if (err instanceof ApiError) {
    if (err.status === 0) return "common.error.network";
    if (err.code === "auth.step_up_wrong_password") return "stepUp.error.wrong";
    if (err.code === "auth.rate_limited" || err.status === 429) return "stepUp.error.tooMany";
    if (err.code === "validation.password_required") return "stepUp.error.empty";
    if (err.code === "auth.session_ended" || err.status === 401) return "stepUp.error.sessionEnded";
  }
  return "common.error.generic";
}
