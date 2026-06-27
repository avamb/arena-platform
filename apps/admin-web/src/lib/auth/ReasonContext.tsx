/**
 * <ReasonProvider /> -- cross-tenant audit-reason context (SAUI-04).
 *
 * Mounts under <AuthProvider /> so it can register an interactive
 * resolver with the API client (see lib/api/reason.ts). The resolver:
 *
 *   1. If an active reason is cached for this tab, returns it immediately
 *      (no UI churn).
 *   2. Otherwise, enqueues a prompt request, shows the modal, and resolves
 *      when the operator submits a non-empty reason. The operator may
 *      cancel; the resolver rejects in that case so the API client surfaces
 *      a `superadmin.reason_required` ApiError instead of firing the
 *      request without a header.
 *
 * The provider also exposes the active reason + a `clearReason()` action
 * to the in-shell badge (`<ActiveReasonBadge />`) so operators can force
 * a re-prompt before the next request.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import {
  clearActiveReason as clearActiveReasonStore,
  getActiveReason as getActiveReasonStore,
  setActiveReason as setActiveReasonStore,
  setReasonResolver,
  subscribeReason,
  type ReasonResolver,
} from "@/lib/api/reason";

/**
 * One pending prompt request. The API client awaits `promise`; the modal
 * resolves it via `resolve()` (submit) or `reject()` (cancel).
 */
interface PendingPrompt {
  readonly path: string;
  readonly resolve: (reason: string) => void;
  readonly reject: (cause: Error) => void;
}

export interface ReasonContextValue {
  readonly activeReason: string | null;
  /** Currently open prompt, if any. Drives the modal render. */
  readonly pendingPrompt: PendingPrompt | null;
  /** Manually open the prompt (e.g. via the badge's "Change" action). */
  openPrompt(): void;
  /** Drop the current reason and force a re-prompt on next request. */
  clearReason(): void;
  /** Submit a reason for the current pending prompt. */
  submitPrompt(reason: string): void;
  /** Dismiss the current pending prompt (operator cancelled). */
  cancelPrompt(): void;
}

const ReasonContext = createContext<ReasonContextValue | null>(null);

interface ReasonProviderProps {
  readonly children: ReactNode;
}

export function ReasonProvider({ children }: ReasonProviderProps) {
  const [activeReason, setActiveReasonState] = useState<string | null>(() =>
    getActiveReasonStore(),
  );
  const [pendingPrompt, setPendingPrompt] = useState<PendingPrompt | null>(null);
  // Pending queue: while a modal is open, additional API calls funnel
  // through and wait on the same single prompt. We coalesce by chaining
  // resolves/rejects.
  const queueRef = useRef<PendingPrompt[]>([]);

  // Mirror the store -> React state so badges re-render when the API
  // client (or another tab event) updates the reason.
  useEffect(() => {
    const unsub = subscribeReason((next) => {
      setActiveReasonState(next);
    });
    return unsub;
  }, []);

  // Drain queued waiters with a single value once a reason is captured.
  const flushQueue = useCallback((reason: string) => {
    const waiters = queueRef.current;
    queueRef.current = [];
    for (const w of waiters) {
      w.resolve(reason);
    }
  }, []);

  const failQueue = useCallback((cause: Error) => {
    const waiters = queueRef.current;
    queueRef.current = [];
    for (const w of waiters) {
      w.reject(cause);
    }
  }, []);

  // Register the resolver the API client calls before sending a
  // cross-tenant request. Cleanup ensures we never leave a stale closure
  // referencing an unmounted React tree.
  useEffect(() => {
    const resolver: ReasonResolver = (path) => {
      const existing = getActiveReasonStore();
      if (existing !== null) {
        return Promise.resolve(existing);
      }
      return new Promise<string>((resolve, reject) => {
        const entry: PendingPrompt = { path, resolve, reject };
        // If a prompt is already open, queue silently; the modal submit
        // will flush every queued waiter at once.
        if (queueRef.current.length === 0 && pendingPromptRef.current === null) {
          pendingPromptRef.current = entry;
          setPendingPrompt(entry);
        }
        queueRef.current.push(entry);
      });
    };
    setReasonResolver(resolver);
    return () => {
      setReasonResolver(null);
    };
    // pendingPromptRef is a ref; safe to omit from deps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // We need a ref-mirror of pendingPrompt so the resolver closure (which
  // captures setReasonResolver's argument once) can check the latest
  // value without re-registering on every state change.
  const pendingPromptRef = useRef<PendingPrompt | null>(null);
  useEffect(() => {
    pendingPromptRef.current = pendingPrompt;
  }, [pendingPrompt]);

  const submitPrompt = useCallback(
    (reason: string) => {
      const trimmed = reason.trim();
      if (trimmed === "") {
        // Defensive: the modal enforces this in markup, but we also
        // refuse here so an automated caller cannot bypass.
        return;
      }
      setActiveReasonStore(trimmed);
      setActiveReasonState(trimmed);
      flushQueue(trimmed);
      pendingPromptRef.current = null;
      setPendingPrompt(null);
    },
    [flushQueue],
  );

  const cancelPrompt = useCallback(() => {
    const cause = new Error("Operator cancelled the audit-reason prompt");
    failQueue(cause);
    pendingPromptRef.current = null;
    setPendingPrompt(null);
  }, [failQueue]);

  const clearReason = useCallback(() => {
    clearActiveReasonStore();
    setActiveReasonState(null);
  }, []);

  const openPrompt = useCallback(() => {
    // Operator-initiated re-prompt. We do NOT clear the existing reason
    // here -- the modal's submit replaces it on success; cancel keeps the
    // old reason intact.
    if (pendingPromptRef.current !== null) {
      return;
    }
    const entry: PendingPrompt = {
      path: "(manual)",
      resolve: () => {
        // No API caller is waiting; just close.
      },
      reject: () => {
        // Operator cancelled; no waiters to fail.
      },
    };
    pendingPromptRef.current = entry;
    setPendingPrompt(entry);
  }, []);

  const value = useMemo<ReasonContextValue>(
    () => ({
      activeReason,
      pendingPrompt,
      openPrompt,
      clearReason,
      submitPrompt,
      cancelPrompt,
    }),
    [
      activeReason,
      pendingPrompt,
      openPrompt,
      clearReason,
      submitPrompt,
      cancelPrompt,
    ],
  );

  return <ReasonContext.Provider value={value}>{children}</ReasonContext.Provider>;
}

export function useReason(): ReasonContextValue {
  const ctx = useContext(ReasonContext);
  if (ctx === null) {
    throw new Error("useReason must be used inside <ReasonProvider>");
  }
  return ctx;
}
