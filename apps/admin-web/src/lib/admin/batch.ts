/**
 * A small runner for "do this for every item, a few at a time" (hall plan scans,
 * bulk venue verification). Pure: it knows nothing about HTTP or React, so the
 * screens test it with plain async functions.
 */

export interface BatchProgress {
  readonly total: number;
  readonly completed: number;
  readonly failed: number;
}

export interface BatchFailure<T> {
  readonly item: T;
  readonly error: unknown;
}

export interface BatchSuccess<T, R> {
  readonly item: T;
  readonly value: R;
}

export interface BatchResult<T, R> {
  readonly done: readonly BatchSuccess<T, R>[];
  readonly failed: readonly BatchFailure<T>[];
  /** True when shouldStop asked to stop before every item ran. */
  readonly stopped: boolean;
}

export interface BatchOptions {
  /** At most this many workers run at once (at least 1). */
  readonly concurrency: number;
  readonly onProgress?: (p: BatchProgress) => void;
  /** Checked before each item starts; true leaves the rest of the items unstarted. */
  readonly shouldStop?: () => boolean;
}

export async function runBatch<T, R>(
  items: readonly T[],
  worker: (item: T, index: number) => Promise<R>,
  options: BatchOptions,
): Promise<BatchResult<T, R>> {
  const total = items.length;
  const limit = Math.max(1, Math.min(Math.floor(options.concurrency) || 1, Math.max(1, total)));
  const done: BatchSuccess<T, R>[] = [];
  const failed: BatchFailure<T>[] = [];
  let next = 0;
  let completed = 0;
  let stopped = false;

  const report = (): void => {
    options.onProgress?.({ total, completed, failed: failed.length });
  };
  report();

  const lane = async (): Promise<void> => {
    for (;;) {
      if (options.shouldStop?.() === true) {
        stopped = true;
        return;
      }
      const index = next;
      if (index >= total) {
        return;
      }
      next += 1;
      const item = items[index] as T;
      try {
        const value = await worker(item, index);
        done.push({ item, value });
      } catch (error) {
        failed.push({ item, error });
      }
      completed += 1;
      report();
    }
  };

  await Promise.all(Array.from({ length: limit }, () => lane()));
  return { done, failed, stopped };
}
