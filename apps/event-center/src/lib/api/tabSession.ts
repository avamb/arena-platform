/**
 * The sign-in session of one browser tab, coordinated with the other tabs
 * of the SAME session (review finding M3, 2026-10-13).
 *
 * The problem. The refresh token lives in sessionStorage, and the browser
 * copies sessionStorage into a duplicated tab. arena-api rotates the
 * refresh token on every refresh and treats a second use of an old token as
 * theft: it revokes ALL the person's sessions (auth.session_compromised),
 * admin-web included. Two tabs that refresh with one token trip it.
 *
 * The rule. Exactly one tab of a session — the LEADER — ever sends the
 * refresh token. Leadership is a Web Lock named after the session key, so
 * the browser hands it to the next tab when the leader closes. Every other
 * tab is a FOLLOWER: it asks the leader for an access token over a
 * BroadcastChannel, and it copies every rotated refresh token into its own
 * sessionStorage, so it holds the latest one when it becomes the leader.
 *
 * A session key is minted at sign-in and travels with the copied
 * sessionStorage. A tab signed in separately (another key) never takes
 * another session's tokens.
 *
 * Without Web Locks or BroadcastChannel (old browsers) the tab acts alone,
 * which is the previous behaviour.
 *
 * Failures: only a refusal from the server (401/400 on refresh) signs the
 * person out, in every tab of the session. A network error keeps the
 * session (review finding L7).
 */

export interface Tokens {
  accessToken: string;
  refreshToken: string;
  /** RFC 3339. */
  expiresAt: string;
  userId: string;
}

export interface Stored {
  key: string;
  refreshToken: string;
  userId: string;
}

export interface SessionStore {
  get(): Stored | null;
  set(value: Stored | null): void;
}

export type Message =
  | { type: "rotated"; key: string; refreshToken: string; accessToken: string; expiresAt: number; userId: string }
  /** `stale`: the access token the asking tab holds and the API refused. */
  | { type: "need-token"; key: string; id: string; stale?: string }
  | { type: "token"; key: string; id: string; accessToken: string; expiresAt: number; userId: string }
  | { type: "token-failed"; key: string; id: string; status: number; code: string }
  | { type: "signed-out"; key: string };

export interface Bus {
  post(msg: Message): void;
  onMessage(fn: (msg: Message) => void): () => void;
}

/** Resolves with a release function once the lock is held, or null when `ifAvailable` and it is taken. */
export interface Locks {
  acquire(name: string, opts: { ifAvailable: boolean; signal?: AbortSignal }): Promise<(() => void) | null>;
}

/** An error the refresh call throws: `status` 0 is a network failure. */
export interface RefreshError extends Error {
  status: number;
  code: string;
}

export interface Deps {
  store: SessionStore;
  bus: Bus | null;
  locks: Locks | null;
  refresh: (refreshToken: string) => Promise<Tokens>;
  makeError: (status: number, code: string, message: string) => RefreshError;
  now?: () => number;
  newId?: () => string;
  /** How long a follower waits for the leader. */
  askTimeoutMs?: number;
}

/** An access token this close to expiry is refreshed before use. */
export const EXPIRY_MARGIN_MS = 30_000;

export class TabSession {
  private access: { token: string; expiresAt: number; userId: string } | null = null;
  private leader = false;
  /** The claim in progress or done, so a second start() (React StrictMode) reuses it. */
  private claiming: { key: string; done: Promise<void> } | null = null;
  private release: (() => void) | null = null;
  private queued: AbortController | null = null;
  private inflight: Promise<void> | null = null;
  private waiters = new Map<string, { resolve: () => void; reject: (e: Error) => void; timer: ReturnType<typeof setTimeout> }>();
  private listeners = new Set<() => void>();
  private readonly now: () => number;
  private readonly newId: () => string;
  private readonly askTimeoutMs: number;

  constructor(private readonly deps: Deps) {
    this.now = deps.now ?? (() => Date.now());
    this.newId = deps.newId ?? (() => crypto.randomUUID());
    this.askTimeoutMs = deps.askTimeoutMs ?? 10_000;
    deps.bus?.onMessage((m) => this.onMessage(m));
  }

  // ── reading ─────────────────────────────────────────────────────────

  hasSession(): boolean {
    return this.access !== null || this.deps.store.get() !== null;
  }

  getAccessToken(): string | null {
    return this.access?.token ?? null;
  }

  getUserId(): string | null {
    return this.access?.userId ?? this.deps.store.get()?.userId ?? null;
  }

  getRefreshToken(): string | null {
    return this.deps.store.get()?.refreshToken ?? null;
  }

  isLeader(): boolean {
    return this.leader;
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  // ── lifecycle ───────────────────────────────────────────────────────

  /** Claims leadership of a session restored from sessionStorage. */
  async start(): Promise<void> {
    const stored = this.deps.store.get();
    if (stored !== null) await this.claim(stored.key);
  }

  /** After a sign-in: a new session with a new key, led by this tab. */
  async signedIn(tokens: Tokens): Promise<void> {
    this.dropLeadership();
    const key = this.newId();
    this.deps.store.set({ key, refreshToken: tokens.refreshToken, userId: tokens.userId });
    this.setAccess(tokens.accessToken, Date.parse(tokens.expiresAt), tokens.userId);
    await this.claim(key);
    this.notify();
  }

  /** Signs this tab out and tells the other tabs of the session. */
  signOut(broadcast = true): void {
    const key = this.deps.store.get()?.key;
    this.clearLocal();
    if (broadcast && key !== undefined) this.deps.bus?.post({ type: "signed-out", key });
  }

  // ── tokens ──────────────────────────────────────────────────────────

  /** Makes sure the tab holds an access token that is not about to expire. */
  async ensureFresh(): Promise<void> {
    if (this.access !== null && this.access.expiresAt - this.now() > EXPIRY_MARGIN_MS) return;
    await this.refresh();
  }

  /** Gets a new access token: the leader rotates, a follower asks. */
  refresh(): Promise<void> {
    if (this.inflight !== null) return this.inflight;
    const run = this.leader || this.deps.bus === null || this.deps.locks === null ? this.rotate() : this.ask();
    this.inflight = run.finally(() => {
      this.inflight = null;
    });
    return this.inflight;
  }

  private async rotate(): Promise<void> {
    const stored = this.deps.store.get();
    if (stored === null) throw this.deps.makeError(401, "auth.no_refresh_token", "You are signed out.");
    let tokens: Tokens;
    try {
      tokens = await this.deps.refresh(stored.refreshToken);
    } catch (err) {
      const status = (err as Partial<RefreshError>).status ?? 0;
      if (status === 400 || status === 401) this.signOut(true);
      throw err;
    }
    // Another sign-in may have replaced the session meanwhile.
    if (this.deps.store.get()?.key !== stored.key) return;
    const expiresAt = Date.parse(tokens.expiresAt);
    this.deps.store.set({ key: stored.key, refreshToken: tokens.refreshToken, userId: tokens.userId });
    this.setAccess(tokens.accessToken, expiresAt, tokens.userId);
    this.deps.bus?.post({
      type: "rotated",
      key: stored.key,
      refreshToken: tokens.refreshToken,
      accessToken: tokens.accessToken,
      expiresAt,
      userId: tokens.userId,
    });
    this.notify();
  }

  private ask(): Promise<void> {
    const stored = this.deps.store.get();
    if (stored === null) return Promise.reject(this.deps.makeError(401, "auth.no_refresh_token", "You are signed out."));
    const id = this.newId();
    return new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.waiters.delete(id);
        // The session stays: the leader tab may be busy. The person can retry.
        reject(this.deps.makeError(0, "network.timeout", "Another tab of this session did not answer."));
      }, this.askTimeoutMs);
      this.waiters.set(id, { resolve, reject, timer });
      this.deps.bus?.post({ type: "need-token", key: stored.key, id, stale: this.access?.token });
    });
  }

  // ── messages from the other tabs ────────────────────────────────────

  private onMessage(m: Message): void {
    const mine = this.deps.store.get()?.key;
    if (mine === undefined || m.key !== mine) return;
    switch (m.type) {
      case "rotated":
        this.deps.store.set({ key: m.key, refreshToken: m.refreshToken, userId: m.userId });
        this.setAccess(m.accessToken, m.expiresAt, m.userId);
        this.notify();
        return;
      case "need-token":
        if (!this.leader) return;
        // The asking tab was refused with the token this tab holds: rotate.
        (m.stale !== undefined && m.stale === this.access?.token ? this.refresh() : this.ensureFresh()).then(
          () => {
            const a = this.access;
            if (a !== null) {
              this.deps.bus?.post({ type: "token", key: m.key, id: m.id, accessToken: a.token, expiresAt: a.expiresAt, userId: a.userId });
            }
          },
          (err: unknown) => {
            const e = err as Partial<RefreshError>;
            this.deps.bus?.post({ type: "token-failed", key: m.key, id: m.id, status: e.status ?? 0, code: e.code ?? "network.failure" });
          },
        );
        return;
      case "token": {
        const w = this.waiters.get(m.id);
        if (w === undefined) return;
        this.waiters.delete(m.id);
        clearTimeout(w.timer);
        this.setAccess(m.accessToken, m.expiresAt, m.userId);
        this.notify();
        w.resolve();
        return;
      }
      case "token-failed": {
        const w = this.waiters.get(m.id);
        if (w === undefined) return;
        this.waiters.delete(m.id);
        clearTimeout(w.timer);
        w.reject(this.deps.makeError(m.status, m.code, "The session could not be refreshed."));
        return;
      }
      case "signed-out":
        this.clearLocal();
        return;
    }
  }

  // ── internals ───────────────────────────────────────────────────────

  private lockName(key: string): string {
    return `arena.events.session-leader.${key}`;
  }

  private claim(key: string): Promise<void> {
    if (this.claiming?.key === key) return this.claiming.done;
    const done = this.claimLock(key);
    this.claiming = { key, done };
    return done;
  }

  private async claimLock(key: string): Promise<void> {
    const locks = this.deps.locks;
    if (locks === null || this.deps.bus === null) {
      this.leader = true;
      return;
    }
    const name = this.lockName(key);
    const now = await locks.acquire(name, { ifAvailable: true });
    if (now !== null) {
      this.becomeLeader(key, now);
      return;
    }
    // A tab of this session already leads. Wait in line for its lock.
    this.leader = false;
    const ctl = new AbortController();
    this.queued = ctl;
    locks.acquire(name, { ifAvailable: false, signal: ctl.signal }).then(
      (rel) => {
        if (rel !== null) this.becomeLeader(key, rel);
      },
      () => {
        // Aborted at sign-out or by a new sign-in.
      },
    );
  }

  private becomeLeader(key: string, rel: () => void): void {
    if (this.deps.store.get()?.key !== key) {
      rel();
      return;
    }
    this.leader = true;
    this.release = rel;
    this.queued = null;
  }

  private dropLeadership(): void {
    this.claiming = null;
    this.queued?.abort();
    this.queued = null;
    this.release?.();
    this.release = null;
    this.leader = false;
  }

  private setAccess(token: string, expiresAt: number, userId: string): void {
    this.access = { token, expiresAt, userId };
  }

  private clearLocal(): void {
    this.access = null;
    this.deps.store.set(null);
    this.dropLeadership();
    for (const [, w] of this.waiters) {
      clearTimeout(w.timer);
      w.reject(this.deps.makeError(401, "auth.signed_out", "You are signed out."));
    }
    this.waiters.clear();
    this.notify();
  }

  private notify(): void {
    for (const l of this.listeners) l();
  }
}

// ── browser adapters ────────────────────────────────────────────────────

const STORE_KEY = "arena.events.session";

/** sessionStorage of this tab (copied by the browser into a duplicated tab). */
export function browserStore(): SessionStore {
  return {
    get() {
      try {
        const raw = sessionStorage.getItem(STORE_KEY);
        if (raw === null) return null;
        const v = JSON.parse(raw) as Partial<Stored>;
        if (typeof v.key !== "string" || typeof v.refreshToken !== "string" || typeof v.userId !== "string") return null;
        return { key: v.key, refreshToken: v.refreshToken, userId: v.userId };
      } catch {
        return null;
      }
    },
    set(value) {
      try {
        if (value === null) sessionStorage.removeItem(STORE_KEY);
        else sessionStorage.setItem(STORE_KEY, JSON.stringify(value));
      } catch {
        // Without storage a reload signs the person out. Nothing else breaks.
      }
    },
  };
}

export function browserBus(): Bus | null {
  if (typeof BroadcastChannel === "undefined") return null;
  const ch = new BroadcastChannel("arena.events.session");
  // Node (unit tests) keeps a process alive for an open channel.
  (ch as unknown as { unref?: () => void }).unref?.();
  return {
    post: (msg) => ch.postMessage(msg),
    onMessage: (fn) => {
      const h = (e: MessageEvent) => fn(e.data as Message);
      ch.addEventListener("message", h);
      return () => ch.removeEventListener("message", h);
    },
  };
}

export function browserLocks(): Locks | null {
  if (typeof navigator === "undefined" || navigator.locks === undefined) return null;
  const lm = navigator.locks;
  return {
    acquire(name, opts) {
      return new Promise((resolve, reject) => {
        lm.request(name, { ifAvailable: opts.ifAvailable, signal: opts.signal }, (lock) => {
          if (lock === null) {
            resolve(null);
            return undefined;
          }
          // Hold the lock until the release function is called.
          return new Promise<void>((done) => resolve(() => done()));
        }).catch(reject);
      });
    },
  };
}
