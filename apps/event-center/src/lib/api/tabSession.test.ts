/**
 * Review finding M3: a duplicated tab copies sessionStorage, and two tabs
 * refreshing with one refresh token make arena-api revoke every session of
 * the person. These tests run several TabSessions against a fake server that
 * rotates the token and flags a reused one, exactly like arena-api.
 */
import { describe, expect, it } from "vitest";
import { TabSession, type Bus, type Locks, type Message, type SessionStore, type Stored, type Tokens } from "@/lib/api/tabSession";

class RefreshFailure extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(code);
  }
}

/** arena-api's refresh: rotates, and a second use of a token revokes everything. */
class FakeServer {
  current: string;
  used = new Set<string>();
  compromised = false;
  calls = 0;
  offline = false;
  private n = 0;

  constructor(first = "r0") {
    this.current = first;
  }

  refresh = async (token: string): Promise<Tokens> => {
    this.calls++;
    await Promise.resolve();
    if (this.offline) throw new RefreshFailure(0, "network.failure");
    if (this.compromised) throw new RefreshFailure(401, "auth.session_compromised");
    if (token !== this.current) {
      if (this.used.has(token)) this.compromised = true;
      throw new RefreshFailure(401, this.compromised ? "auth.session_compromised" : "auth.invalid_refresh_token");
    }
    this.used.add(token);
    this.n++;
    this.current = `r${this.n}`;
    return { accessToken: `a${this.n}`, refreshToken: this.current, expiresAt: new Date(Date.now() + 900_000).toISOString(), userId: "u1" };
  };
}

/** BroadcastChannel: a message reaches every OTHER member, asynchronously. */
class FakeHub {
  private members = new Set<(m: Message) => void>();
  bus(): Bus {
    let self: ((m: Message) => void) | null = null;
    return {
      post: (msg) => {
        const copy = structuredClone(msg);
        for (const fn of this.members) if (fn !== self) queueMicrotask(() => fn(copy));
      },
      onMessage: (fn) => {
        self = fn;
        this.members.add(fn);
        return () => this.members.delete(fn);
      },
    };
  }
}

/** Web Locks: one holder per name, the rest wait in order. */
class FakeLocks implements Locks {
  private held = new Set<string>();
  private queues = new Map<string, Array<{ grant: () => void; signal?: AbortSignal }>>();

  acquire(name: string, opts: { ifAvailable: boolean; signal?: AbortSignal }): Promise<(() => void) | null> {
    if (!this.held.has(name)) {
      this.held.add(name);
      return Promise.resolve(this.releaser(name));
    }
    if (opts.ifAvailable) return Promise.resolve(null);
    return new Promise((resolve, reject) => {
      const q = this.queues.get(name) ?? [];
      const entry = { grant: () => resolve(this.releaser(name)), signal: opts.signal };
      q.push(entry);
      this.queues.set(name, q);
      opts.signal?.addEventListener("abort", () => {
        const i = q.indexOf(entry);
        if (i >= 0) q.splice(i, 1);
        reject(new Error("AbortError"));
      });
    });
  }

  private releaser(name: string): () => void {
    let done = false;
    return () => {
      if (done) return;
      done = true;
      const next = this.queues.get(name)?.shift();
      if (next !== undefined) next.grant();
      else this.held.delete(name);
    };
  }
}

function memoryStore(initial: Stored | null = null): SessionStore & { value: Stored | null } {
  const s = {
    value: initial,
    get: () => (s.value === null ? null : { ...s.value }),
    set: (v: Stored | null) => {
      s.value = v === null ? null : { ...v };
    },
  };
  return s;
}

function tab(server: FakeServer, hub: FakeHub | null, locks: Locks | null, store = memoryStore(), ids = { n: 0 }) {
  return {
    store,
    session: new TabSession({
      store,
      bus: hub?.bus() ?? null,
      locks,
      refresh: server.refresh,
      makeError: (status, code, message) => Object.assign(new Error(message), { status, code }),
      newId: () => `id-${++ids.n}-${Math.random().toString(36).slice(2)}`,
      askTimeoutMs: 1_000,
    }),
  };
}

const settle = () => new Promise((r) => setTimeout(r, 0));

/** Signs tab A in with the server's first token. */
async function signIn(server: FakeServer, t: ReturnType<typeof tab>): Promise<void> {
  await t.session.signedIn({ accessToken: "a0", refreshToken: server.current, expiresAt: new Date(Date.now() + 900_000).toISOString(), userId: "u1" });
}

describe("TabSession across tabs", () => {
  it("a duplicated tab never reuses the rotated refresh token", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    expect(a.session.isLeader()).toBe(true);

    // "Duplicate tab": the browser copies sessionStorage, key included.
    const b = tab(server, hub, locks, memoryStore(a.store.get()));
    await b.session.start();
    expect(b.session.isLeader()).toBe(false);

    // Both tabs refresh, again and again, at the same time.
    for (let i = 0; i < 5; i++) {
      await Promise.all([a.session.refresh(), b.session.refresh(), b.session.refresh()]);
    }
    await settle();
    expect(server.compromised).toBe(false);
    expect(server.calls).toBe(5);
    expect(b.store.get()?.refreshToken).toBe(server.current);
    expect(a.store.get()?.refreshToken).toBe(server.current);
    expect(b.session.getAccessToken()).toBe(a.session.getAccessToken());
  });

  it("the duplicate takes over with the latest token when the leader closes", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const b = tab(server, hub, locks, memoryStore(a.store.get()));
    await b.session.start();
    await a.session.refresh();
    await settle();

    // The leader's page goes away: its lock is released, its storage is gone.
    a.session.signOut(false);
    await settle();
    expect(b.session.isLeader()).toBe(true);

    await b.session.refresh();
    expect(server.compromised).toBe(false);
    expect(b.store.get()?.refreshToken).toBe(server.current);
  });

  it("a follower whose access token was refused makes the leader rotate", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const b = tab(server, hub, locks, memoryStore(a.store.get()));
    await b.session.start();
    await b.session.refresh(); // gets a0 from the leader without a rotation
    expect(server.calls).toBe(0);
    expect(b.session.getAccessToken()).toBe("a0");

    await b.session.refresh(); // the API refused a0: the leader must rotate
    expect(server.calls).toBe(1);
    expect(b.session.getAccessToken()).toBe("a1");
  });

  it("a separate sign-in in another tab stays independent", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const other = new FakeServer("x0");
    const c = tab(other, hub, locks);
    await c.session.signedIn({ accessToken: "xa", refreshToken: "x0", expiresAt: new Date(Date.now() + 900_000).toISOString(), userId: "u2" });
    expect(c.session.isLeader()).toBe(true);

    await a.session.refresh();
    await settle();
    expect(c.store.get()?.refreshToken).toBe("x0");
    a.session.signOut(true);
    await settle();
    expect(c.session.hasSession()).toBe(true);
  });

  it("a refusal signs out every tab of the session", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const b = tab(server, hub, locks, memoryStore(a.store.get()));
    await b.session.start();
    await b.session.refresh(); // b now holds the leader's access token

    // The API refuses that token, the leader rotates, and the server says no.
    server.compromised = true;
    await expect(b.session.refresh()).rejects.toMatchObject({ status: 401 });
    await settle();
    expect(a.session.hasSession()).toBe(false);
    expect(b.session.hasSession()).toBe(false);
  });

  it("starting twice (React StrictMode) keeps the lead", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const reloaded = tab(server, hub, locks, memoryStore(a.store.get()));
    a.session.signOut(false); // the page that signed in is gone
    await Promise.all([reloaded.session.start(), reloaded.session.start()]);
    expect(reloaded.session.isLeader()).toBe(true);
  });

  it("a network error keeps the session (review finding L7)", async () => {
    const server = new FakeServer();
    const a = tab(server, null, null);
    await signIn(server, a);
    server.offline = true;
    await expect(a.session.refresh()).rejects.toMatchObject({ status: 0 });
    expect(a.session.hasSession()).toBe(true);
    expect(a.store.get()?.refreshToken).toBe("r0");

    server.offline = false;
    await a.session.refresh();
    expect(a.store.get()?.refreshToken).toBe("r1");
  });

  it("without Web Locks or BroadcastChannel the tab rotates by itself", async () => {
    const server = new FakeServer();
    const a = tab(server, null, null);
    await signIn(server, a);
    await Promise.all([a.session.refresh(), a.session.refresh()]);
    expect(server.calls).toBe(1);
    expect(a.store.get()?.refreshToken).toBe("r1");
  });
});

/** A bus that can stop listening, like a tab that is busy or closing. */
function deafBus(hub: FakeHub): { bus: Bus; deaf: { on: boolean } } {
  const inner = hub.bus();
  const deaf = { on: false };
  return {
    deaf,
    bus: {
      post: (msg) => inner.post(msg),
      onMessage: (fn) => inner.onMessage((m) => {
        if (!deaf.on) fn(m);
      }),
    },
  };
}

function tabWith(server: FakeServer, bus: Bus | null, locks: Locks | null, store = memoryStore(), askTimeoutMs = 10_000) {
  return {
    store,
    session: new TabSession({
      store,
      bus,
      locks,
      refresh: server.refresh,
      makeError: (status, code, message) => Object.assign(new Error(message), { status, code }),
      newId: () => `id-${Math.random().toString(36).slice(2)}`,
      askTimeoutMs,
    }),
  };
}

describe("TabSession review follow-ups", () => {
  it("R-1: a browser that refuses the lock still signs in, the tab acts alone", async () => {
    const server = new FakeServer();
    const refusing: Locks = { acquire: () => Promise.reject(new Error("SecurityError")) };
    const a = tabWith(server, new FakeHub().bus(), refusing);
    await signIn(server, a);
    expect(a.session.isLeader()).toBe(true);
    await a.session.refresh();
    expect(server.calls).toBe(1);
    expect(a.store.get()?.refreshToken).toBe("r1");

    const reloaded = tabWith(server, new FakeHub().bus(), refusing, memoryStore(a.store.get()));
    a.session.signOut(false);
    await expect(reloaded.session.start()).resolves.toBeUndefined();
    await reloaded.session.ensureFresh();
    expect(reloaded.store.get()?.refreshToken).toBe("r2");
  });

  it("R-3: a duplicate that missed a rotation gets the latest refresh token with its answer", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const a = tab(server, hub, locks);
    await signIn(server, a);
    const copied = memoryStore(a.store.get()); // sessionStorage copied with r0
    await a.session.refresh(); // rotated to r1 before the duplicate listens
    const b = tab(server, hub, locks, copied);
    expect(b.store.get()?.refreshToken).toBe("r0");
    await b.session.start();
    await b.session.ensureFresh();
    expect(b.store.get()?.refreshToken).toBe("r1");

    a.session.signOut(false);
    await settle();
    await b.session.refresh();
    expect(server.compromised).toBe(false);
  });

  it("R-4: a tab that becomes the leader answers its own question at once", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const da = deafBus(hub);
    const a = tabWith(server, da.bus, locks);
    await signIn(server, a);
    const b = tabWith(server, hub.bus(), locks, memoryStore(a.store.get()));
    await b.session.start();

    da.deaf.on = true; // the leader is closing and answers nothing
    const asked = b.session.refresh();
    await settle();
    a.session.signOut(false); // its lock goes to b
    const started = Date.now();
    await asked;
    expect(Date.now() - started).toBeLessThan(1_000);
    expect(b.session.isLeader()).toBe(true);
    expect(server.calls).toBe(1);
    expect(server.compromised).toBe(false);
  });

  it("R-4: a follower asks the new leader again when the old one closed", async () => {
    const server = new FakeServer();
    const hub = new FakeHub();
    const locks = new FakeLocks();
    const da = deafBus(hub);
    const a = tabWith(server, da.bus, locks);
    await signIn(server, a);
    const b = tabWith(server, hub.bus(), locks, memoryStore(a.store.get()));
    await b.session.start();
    const c = tabWith(server, hub.bus(), locks, memoryStore(a.store.get()));
    await c.session.start();

    da.deaf.on = true;
    const asked = c.session.refresh();
    await settle();
    a.session.signOut(false); // b is first in line for the lock
    const started = Date.now();
    await asked;
    expect(Date.now() - started).toBeLessThan(1_000);
    expect(b.session.isLeader()).toBe(true);
    expect(c.session.isLeader()).toBe(false);
    expect(c.session.getAccessToken()).toBe(b.session.getAccessToken());
    expect(server.compromised).toBe(false);
  });
});
