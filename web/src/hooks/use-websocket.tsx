import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useCallback,
  type ReactNode,
} from "react";
import { getToken } from "@/lib/api";
import { useAuth } from "@/components/auth-provider";

export interface StreamEvent {
  topic: string;
  data: unknown;
  timestamp: string;
}

interface WebSocketContextType {
  events: StreamEvent[];
  connected: boolean;
  clearEvents: () => void;
}

const WebSocketContext = createContext<WebSocketContextType>({
  events: [],
  connected: false,
  clearEvents: () => {},
});

export function WebSocketProvider({ children }: { children: ReactNode }) {
  const { token: authToken } = useAuth();
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  const mountedRef = useRef(true);

  const connect = useCallback(() => {
    if (!mountedRef.current) return;

    // Close any existing connection before opening a new one
    if (wsRef.current) {
      wsRef.current.onclose = null; // prevent triggering reconnect from manual close
      wsRef.current.close();
      wsRef.current = null;
    }

    const token = getToken();
    if (!token) {
      // Not authenticated yet — don't attempt connection
      setConnected(false);
      return;
    }

    // Strip non-Latin1 characters to avoid WebSocket URL encoding issues
    const safeToken = token.replace(/[^\x00-\xFF]/g, "");
    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${protocol}//${window.location.host}/ws/stream?token=${encodeURIComponent(safeToken)}`;

    try {
      const ws = new WebSocket(url);
      wsRef.current = ws;

      ws.onopen = () => {
        if (mountedRef.current) setConnected(true);
      };

      ws.onclose = () => {
        if (mountedRef.current) {
          setConnected(false);
          reconnectTimer.current = setTimeout(connect, 3000);
        }
      };

      ws.onerror = () => {
        // onerror is always followed by onclose, so just close here
        ws.close();
      };

      ws.onmessage = (e) => {
        try {
          const event = JSON.parse(e.data) as StreamEvent;
          setEvents((prev) => [...prev.slice(-199), event]);
        } catch {
          // ignore malformed messages
        }
      };
    } catch {
      if (mountedRef.current) {
        setConnected(false);
        reconnectTimer.current = setTimeout(connect, 3000);
      }
    }
  }, []);

  useEffect(() => {
    mountedRef.current = true;
    connect();
    return () => {
      mountedRef.current = false;
      clearTimeout(reconnectTimer.current);
      if (wsRef.current) {
        wsRef.current.onclose = null;
        wsRef.current.close();
        wsRef.current = null;
      }
    };
  }, [connect, authToken]);

  const clearEvents = useCallback(() => setEvents([]), []);

  return (
    <WebSocketContext.Provider value={{ events, connected, clearEvents }}>
      {children}
    </WebSocketContext.Provider>
  );
}

export function useWebSocket() {
  return useContext(WebSocketContext);
}
