import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Toaster } from "sonner";
import { AuthProvider } from "@/components/auth-provider";
import { WebSocketProvider } from "@/hooks/use-websocket";
import App from "./App";
import "./index.css";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      refetchInterval: 5000,
      // Don't retry on 401 (bad token) — retry other transient errors 3 times.
      retry: (failureCount, error) => {
        if (error instanceof Error && "status" in error && (error as { status: number }).status === 401) return false;
        return failureCount < 3;
      },
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 10000),
      staleTime: 2000,
    },
    mutations: {
      // Retry network errors on mutations (e.g. port-forward drops mid-request).
      retry: (failureCount, error) => {
        const isNetwork =
          error instanceof TypeError ||
          (error instanceof Error && /network|failed to fetch|load failed/i.test(error.message));
        return isNetwork && failureCount < 2;
      },
      retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 5000),
    },
  },
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <AuthProvider>
          <WebSocketProvider>
            <App />
            <Toaster
              theme="dark"
              position="bottom-right"
              toastOptions={{
                style: {
                  background: "hsl(217 33% 17%)",
                  border: "1px solid hsl(215 19% 22%)",
                  color: "hsl(214 32% 91%)",
                },
              }}
            />
          </WebSocketProvider>
        </AuthProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>
);
