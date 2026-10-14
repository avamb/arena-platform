import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { ApiError } from "@/lib/api/http";
import { AuthProvider } from "@/lib/auth";
import { StepUpProvider } from "@/components/StepUpDialog";
import { I18nProvider } from "@/lib/i18n";
import { router } from "@/router";
import "@/styles.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 30_000,
      gcTime: 5 * 60_000,
      // A 4xx answer will not change on a retry: show it at once.
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
      refetchOnWindowFocus: false,
    },
    mutations: { retry: 0 },
  },
});

// The installable app (PWA): the worker caches the app shell only, never
// an API answer (public/sw.js). Dev builds skip it: Vite serves source.
if (import.meta.env.PROD && "serviceWorker" in navigator) {
  window.addEventListener("load", () => {
    navigator.serviceWorker.register("/sw.js").catch(() => undefined);
  });
}

const rootEl = document.getElementById("root");
if (rootEl === null) throw new Error("#root is missing in index.html");

createRoot(rootEl).render(
  <StrictMode>
    <I18nProvider>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <StepUpProvider>
            <RouterProvider router={router} />
          </StepUpProvider>
        </AuthProvider>
      </QueryClientProvider>
    </I18nProvider>
  </StrictMode>,
);
