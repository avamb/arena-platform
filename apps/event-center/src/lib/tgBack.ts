/**
 * The Telegram back button inside the mini app entered through /tg: it walks
 * the history of the app and disappears on the first page (Telegram's own
 * cross then closes the window). Pure over a history and a binder, so a test
 * runs it without a DOM.
 */
export interface HistoryLike {
  canGoBack: () => boolean;
  back: () => void;
  subscribe: (cb: () => void) => () => void;
}

export interface BackDeps {
  /** True once the person entered through /tg. */
  active: () => boolean;
  /** Shows the button and runs the handler on a press; answers the function that takes the button away. */
  bind: (handler: () => void) => () => void;
}

/** Keeps the button in step with the history. Answers the function that stops it. */
export function followHistory(history: HistoryLike, deps: BackDeps): () => void {
  let unbind: (() => void) | null = null;
  const sync = () => {
    const want = deps.active() && history.canGoBack();
    if (want && unbind === null) unbind = deps.bind(() => history.back());
    else if (!want && unbind !== null) {
      unbind();
      unbind = null;
    }
  };
  sync();
  const off = history.subscribe(sync);
  return () => {
    off();
    if (unbind !== null) unbind();
    unbind = null;
  };
}
