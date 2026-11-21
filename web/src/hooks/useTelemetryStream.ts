import { useEffect, useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { fetchEngineState } from '../lib/api';
import type { EngineTelemetrySnapshot } from '../types/telemetry';

export type StreamConnectionState = 'CONNECTING' | 'LIVE' | 'RECONNECTING' | 'DISCONNECTED';

export const ENGINE_STATE_QUERY_KEY = ['engine-telemetry'] as const;

export function useTelemetryStream() {
  const queryClient = useQueryClient();
  const [connectionState, setConnectionState] = useState<StreamConnectionState>('CONNECTING');
  const [streamError, setStreamError] = useState<string | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimerRef = useRef<number | null>(null);
  const attemptRef = useRef<number>(0);

  const query = useQuery<EngineTelemetrySnapshot>({
    queryKey: ENGINE_STATE_QUERY_KEY,
    queryFn: fetchEngineState,
    refetchInterval: connectionState === 'LIVE' ? false : 1000,
    retry: 1,
  });

  useEffect(() => {
    let unmounted = false;

    const scheduleReconnect = () => {
      if (unmounted) return;
      const attempt = attemptRef.current;
      // Exponential backoff: 400ms, 800ms, 1600ms, 3200ms, capped at 8000ms
      const delayMs = Math.min(400 * Math.pow(2, attempt), 8000);
      attemptRef.current = attempt + 1;
      reconnectTimerRef.current = window.setTimeout(connectWebSocket, delayMs);
    };

    const connectWebSocket = () => {
      if (unmounted) return;
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl =
        import.meta.env.VITE_WS_URL || `${protocol}//${window.location.host}/ws`;

      try {
        const ws = new WebSocket(wsUrl);
        wsRef.current = ws;

        ws.onopen = () => {
          if (unmounted) return;
          attemptRef.current = 0;
          setConnectionState('LIVE');
          setStreamError(null);
        };

        ws.onmessage = (event) => {
          if (unmounted) return;
          try {
            const payload = JSON.parse(event.data) as EngineTelemetrySnapshot;
            queryClient.setQueryData(ENGINE_STATE_QUERY_KEY, payload);
            setConnectionState('LIVE');
            setStreamError(null);
          } catch {
            // Ignore malformed telemetry frame
          }
        };

        ws.onerror = () => {
          if (unmounted) return;
          setConnectionState('RECONNECTING');
        };

        ws.onclose = () => {
          if (unmounted) return;
          setConnectionState('DISCONNECTED');
          setStreamError(
            `DAEMON IPC STREAM DISCONNECTED — RETRYING WITH EXPONENTIAL BACKOFF (ATTEMPT #${attemptRef.current + 1})`
          );
          scheduleReconnect();
        };
      } catch {
        setConnectionState('DISCONNECTED');
        scheduleReconnect();
      }
    };

    connectWebSocket();

    return () => {
      unmounted = true;
      if (reconnectTimerRef.current !== null) {
        window.clearTimeout(reconnectTimerRef.current);
      }
      if (wsRef.current) {
        wsRef.current.close();
      }
    };
  }, [queryClient]);

  return {
    telemetry: query.data ?? null,
    isLoading: query.isLoading,
    connectionState,
    daemonError:
      streamError || (query.error instanceof Error ? query.error.message : null),
  };
}
