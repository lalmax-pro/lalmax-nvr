/**
 * Events API — unified NVR event center
 */
import { apiRequest, getAuthToken } from './client';

export type EventSource = 'health' | 'recorder' | 'ai' | 'mqtt';
export type EventSeverity = 'info' | 'warning' | 'critical';
export type EventStatus = 'open' | 'acknowledged';

export interface NvrEvent {
  id: number;
  camera_id: string;
  source: EventSource | string;
  type: string;
  severity: EventSeverity | string;
  status: EventStatus | string;
  message: string;
  metadata: string;
  recording_id?: string;
  snapshot_path?: string;
  started_at: string;
  ended_at?: string;
  acknowledged_at?: string;
  created_at: string;
}

export interface EventsResponse {
  events: NvrEvent[];
  total: number;
  limit: number;
  offset: number;
}

export interface EventsParams {
  camera_id?: string;
  source?: string;
  type?: string;
  status?: string;
  since?: string;
  until?: string;
  limit?: number;
  offset?: number;
  signal?: AbortSignal;
}

export async function listEvents(params: EventsParams = {}): Promise<EventsResponse> {
  const query = new URLSearchParams();
  if (params.camera_id) query.set('camera_id', params.camera_id);
  if (params.source) query.set('source', params.source);
  if (params.type) query.set('type', params.type);
  if (params.status) query.set('status', params.status);
  if (params.since) query.set('since', params.since);
  if (params.until) query.set('until', params.until);
  if (params.limit !== undefined) query.set('limit', String(params.limit));
  if (params.offset !== undefined) query.set('offset', String(params.offset));

  const qs = query.toString();
  return apiRequest<EventsResponse>(qs ? `/events?${qs}` : '/events', { signal: params.signal });
}

export async function getEvent(id: number, signal?: AbortSignal): Promise<NvrEvent> {
  return apiRequest<NvrEvent>(`/events/${id}`, { signal });
}

export async function acknowledgeEvent(id: number, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/events/${id}/ack`, {
    method: 'POST',
    signal,
  });
}

export async function deleteEvent(id: number, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/events/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export function eventsStreamUrl(params: { camera_id?: string; source?: string } = {}): string {
  const query = new URLSearchParams();
  if (params.camera_id) query.set('camera_id', params.camera_id);
  if (params.source) query.set('source', params.source);
  const token = getAuthToken();
  if (token) query.set('token', token);
  const qs = query.toString();
  return qs ? `/api/events/stream?${qs}` : '/api/events/stream';
}
