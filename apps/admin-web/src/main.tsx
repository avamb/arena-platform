import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { AuthProvider } from "@/lib/auth/AuthProvider";
import { ScopeProvider } from "@/lib/auth/ScopeContext";
import { queryClient } from "@/lib/queryClient";
import { router } from "@/router";
import "@/styles.css";

const rootEl = document.getElementById("root");
if (rootEl === null) {
  throw new Error("Admin shell mount point #root not found in index.html");
}

createRoot(rootEl).render(
  <StrictMode>
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>
          <ScopeProvider>
            <RouterProvider router={router} />
          </ScopeProvider>
        </AuthProvider>
      </QueryClientProvider>
    </ErrorBoundary>
  </StrictMode>,
);
