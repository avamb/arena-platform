import { QueryClient } from "@tanstack/react-query";

/**
 * Single shared TanStack Query client for the admin shell.
 *
 * Defaults are tuned for an operational tool: do not retry mutations
 * silently, retry server reads twice with backoff, never serve stale
 * data longer than 30s on idle screens, and refetch on window focus so
 * an operator returning to the tab sees fresh state.
 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      retry: 2,
      refetchOnWindowFocus: true,
      refetchOnReconnect: true,
    },
    mutations: {
      retry: 0,
    },
  },
});
