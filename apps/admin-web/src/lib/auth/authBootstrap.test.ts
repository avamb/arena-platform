import { describe, it, expect, vi } from "vitest";
import { runBootstrap, type BootstrapCallbacks } from "./authBootstrap";

function makeCallbacks(overrides: Partial<BootstrapCallbacks> = {}): {
  callbacks: BootstrapCallbacks;
  mocks: { [K in keyof BootstrapCallbacks]: ReturnType<typeof vi.fn> };
} {
  const mocks = {
    getRefreshToken: vi.fn<[], string | null>().mockReturnValue("token"),
    refresh: vi.fn<[], Promise<void>>().mockResolvedValue(undefined),
    loadMe: vi.fn<[], Promise<void>>().mockResolvedValue(undefined),
    setUnauthenticated: vi.fn<[], void>(),
    isCancelled: vi.fn<[], boolean>().mockReturnValue(false),
  };
  const callbacks: BootstrapCallbacks = {
    ...mocks,
    ...overrides,
  } as unknown as BootstrapCallbacks;
  return { callbacks, mocks };
}

describe("runBootstrap", () => {
  it("path 1: no refresh token → setUnauthenticated() called when not cancelled", async () => {
    const { callbacks, mocks } = makeCallbacks();
    mocks.getRefreshToken.mockReturnValue(null);
    mocks.isCancelled.mockReturnValue(false);

    await runBootstrap(callbacks);

    expect(mocks.setUnauthenticated).toHaveBeenCalledOnce();
    expect(mocks.refresh).not.toHaveBeenCalled();
    expect(mocks.loadMe).not.toHaveBeenCalled();
  });

  it("path 1 (cancelled): no refresh token + cancelled → setUnauthenticated() NOT called", async () => {
    const { callbacks, mocks } = makeCallbacks();
    mocks.getRefreshToken.mockReturnValue(null);
    mocks.isCancelled.mockReturnValue(true);

    await runBootstrap(callbacks);

    expect(mocks.setUnauthenticated).not.toHaveBeenCalled();
    expect(mocks.loadMe).not.toHaveBeenCalled();
  });

  it("path 2: refresh() throws → setUnauthenticated() called when not cancelled", async () => {
    const { callbacks, mocks } = makeCallbacks();
    mocks.refresh.mockRejectedValue(new Error("network error"));
    mocks.isCancelled.mockReturnValue(false);

    await runBootstrap(callbacks);

    expect(mocks.setUnauthenticated).toHaveBeenCalledOnce();
    expect(mocks.loadMe).not.toHaveBeenCalled();
  });

  it("path 2 (cancelled): refresh() throws + cancelled → setUnauthenticated() NOT called", async () => {
    const { callbacks, mocks } = makeCallbacks();
    mocks.refresh.mockRejectedValue(new Error("network error"));
    mocks.isCancelled.mockReturnValue(true);

    await runBootstrap(callbacks);

    expect(mocks.setUnauthenticated).not.toHaveBeenCalled();
    expect(mocks.loadMe).not.toHaveBeenCalled();
  });

  it("path 3 (AB-29 key fix): refresh() succeeds + isCancelled() true → loadMe() still called", async () => {
    const { callbacks, mocks } = makeCallbacks();
    // isCancelled returns true (simulating React 18 StrictMode cleanup)
    mocks.isCancelled.mockReturnValue(true);

    await runBootstrap(callbacks);

    // loadMe must be called even though the effect was "cancelled"
    expect(mocks.loadMe).toHaveBeenCalledOnce();
    // setUnauthenticated must NOT be called on the success path
    expect(mocks.setUnauthenticated).not.toHaveBeenCalled();
  });

  it("path 3 (happy path): refresh() succeeds + not cancelled → loadMe() called, setUnauthenticated not called", async () => {
    const { callbacks, mocks } = makeCallbacks();
    mocks.isCancelled.mockReturnValue(false);

    await runBootstrap(callbacks);

    expect(mocks.refresh).toHaveBeenCalledOnce();
    expect(mocks.loadMe).toHaveBeenCalledOnce();
    expect(mocks.setUnauthenticated).not.toHaveBeenCalled();
  });

  it("path 4: cancelled before getRefreshToken check → setUnauthenticated() NOT called", async () => {
    // isCancelled() is true even before any async work; the function checks
    // it only after determining there is no token or after a refresh failure.
    // To exercise the "cancelled at the very first gate" scenario we return
    // null from getRefreshToken (so we reach the isCancelled gate) but have
    // isCancelled return true → setUnauthenticated must be skipped.
    const { callbacks, mocks } = makeCallbacks();
    mocks.getRefreshToken.mockReturnValue(null);
    mocks.isCancelled.mockReturnValue(true);

    await runBootstrap(callbacks);

    expect(mocks.setUnauthenticated).not.toHaveBeenCalled();
  });
});
