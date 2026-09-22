import { apiRequest, getAuthToken } from './client';

export interface ServiceLogEntry {
  seq: number;
  time: string;
  level: string;
  msg: string;
  attrs?: Record<string, unknown>;
}

export interface ServiceLogsResponse {
  logs: ServiceLogEntry[];
  count: number;
  capacity: number;
  buffered: number;
  truncated: boolean;
  source?: 'file' | 'memory' | string;
  path?: string;
}

export interface ServiceLogsParams {
  level?: string;
  q?: string;
  limit?: number;
  after_seq?: number;
  since?: string;
}

export async function listServiceLogs(params: ServiceLogsParams = {}): Promise<ServiceLogsResponse> {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') query.set(key, String(value));
  }
  const suffix = query.toString() ? `?${query.toString()}` : '';
  return apiRequest<ServiceLogsResponse>(`/service-logs${suffix}`);
}

export function serviceLogsStreamUrl(params: { level?: string; q?: string } = {}): string {
  const query = new URLSearchParams();
  if (params.level) query.set('level', params.level);
  if (params.q) query.set('q', params.q);
  const token = getAuthToken();
  if (token) query.set('token', token);
  const qs = query.toString();
  return qs ? `/api/service-logs/stream?${qs}` : '/api/service-logs/stream';
}

export function subscribeServiceLogs(
  params: { level?: string; q?: string } = {},
  onLog: (entry: ServiceLogEntry) => void,
): () => void {
  let es: EventSource | null = null;
  let stopped = false;
  let retry = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

  const connect = () => {
    if (stopped) return;
    es = new EventSource(serviceLogsStreamUrl(params));
    es.addEventListener('log', (e: Event) => {
      retry = 0;
      try {
        onLog(JSON.parse((e as MessageEvent).data) as ServiceLogEntry);
      } catch {
        // ignore malformed payloads
      }
    });
    es.onerror = () => {
      if (stopped || !es) return;
      if (es.readyState !== EventSource.CLOSED) return;
      es.close();
      es = null;
      const delay = Math.min(1000 * 2 ** retry, 15000);
      retry += 1;
      reconnectTimer = setTimeout(connect, delay);
    };
  };

  connect();
  return () => {
    stopped = true;
    if (reconnectTimer) clearTimeout(reconnectTimer);
    es?.close();
    es = null;
  };
}
