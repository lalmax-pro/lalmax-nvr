import { apiRequest } from './client';

export interface StreamSessionStatus {
  session_id: string;
  protocol: string;
  remote?: string;
  bitrate_kbits?: number;
  read_bitrate_kbits?: number;
  write_bitrate_kbits?: number;
}

export interface StreamPlayURL {
  protocol: string;
  url: string;
  backend: string;
}

export interface StreamInfo {
  engine: string;
  stream_id: string;
  name?: string;
  app_name?: string;
  managed: boolean;
  management_type?: 'bound' | 'promoted' | 'camera';
  camera_id?: string;
  camera_name?: string;
  source_type: string;
  active: boolean;
  gb28181_playing?: boolean;
  publisher?: StreamSessionStatus;
  subscribers?: StreamSessionStatus[];
  video_codec?: string;
  audio_codec?: string;
  in_fps?: number;
  last_frame_time?: string;
  play_urls?: StreamPlayURL[];
  ingest_urls?: StreamPlayURL[];
  source_url?: string;
}

export interface StreamsResponse {
  streams: StreamInfo[];
  total?: number;
  limit?: number;
  offset?: number;
}

export interface ListStreamsParams {
  q?: string;
  managed?: boolean;
  limit?: number;
  offset?: number;
}

export interface StreamsListResult {
  streams: StreamInfo[];
  total: number;
  limit: number;
  offset: number;
}

export interface BindCameraRequest {
  camera_id: string;
}

export interface PromoteStreamRequest {
  name: string;
  description?: string;
  location?: string;
}

export interface CreateStreamRequest {
  stream_id: string;
  name?: string;
  input_mode?: 'push' | 'pull';
  source_url?: string;
}

export interface UpdateStreamRequest {
  name: string;
}

export async function createStream(data: CreateStreamRequest, signal?: AbortSignal): Promise<StreamInfo> {
  return apiRequest<StreamInfo>('/streams', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

export async function updateStream(streamId: string, data: UpdateStreamRequest, signal?: AbortSignal): Promise<StreamInfo> {
  return apiRequest<StreamInfo>(`/streams/${encodeURIComponent(streamId)}`, {
    method: 'PUT',
    body: JSON.stringify(data),
    signal,
  });
}

export interface StreamOperationResponse {
  stream_id: string;
  camera_id?: string;
  session_id?: string;
  source_type?: string;
  status: string;
}

export async function listStreams(
  params?: ListStreamsParams,
  signal?: AbortSignal,
): Promise<StreamsListResult> {
  const searchParams = new URLSearchParams();
  if (params?.q) searchParams.set('q', params.q);
  if (params?.managed !== undefined) searchParams.set('managed', String(params.managed));
  if (params?.limit !== undefined) searchParams.set('limit', String(params.limit));
  if (params?.offset !== undefined) searchParams.set('offset', String(params.offset));

  const query = searchParams.toString();
  const path = query ? `/streams?${query}` : '/streams';
  const response = await apiRequest<StreamsResponse>(path, { signal });

  return {
    streams: response.streams ?? [],
    total: response.total ?? response.streams?.length ?? 0,
    limit: response.limit ?? 20,
    offset: response.offset ?? 0,
  };
}

/** The lalmax stream ID backing a camera row. */
export function cameraStreamID(camera: { id: string; stream_id?: string }): string {
  return camera.stream_id || camera.id;
}

export function streamMediaURL(streamId: string, protocol: 'flv' | 'hls' | 'll-hls' | 'fmp4' | 'ws' | 'webrtc'): string {
  const id = encodeURIComponent(streamId);
  switch (protocol) {
    case 'flv':
      return `/api/streams/${id}/stream.flv`;
    case 'hls':
      return `/api/streams/${id}/stream/index.m3u8`;
    case 'll-hls':
      return `/api/streams/${id}/stream/index.m3u8?ll-hls=1`;
    case 'fmp4':
      return `/api/streams/${id}/stream.m4s`;
    case 'ws':
      return `/api/streams/${id}/stream/ws`;
    case 'webrtc':
      return `/api/streams/${id}/stream/webrtc`;
    default:
      return `/api/streams/${id}/stream.flv`;
  }
}

export async function getStream(streamId: string, signal?: AbortSignal): Promise<StreamInfo> {
  return apiRequest<StreamInfo>(`/streams/${encodeURIComponent(streamId)}`, { signal });
}

export async function bindCamera(streamId: string, cameraId: string, signal?: AbortSignal): Promise<StreamOperationResponse> {
  return apiRequest<StreamOperationResponse>(`/streams/${encodeURIComponent(streamId)}/bind-camera`, {
    method: 'POST',
    body: JSON.stringify({ camera_id: cameraId }),
    signal,
  });
}

export async function unbindCamera(streamId: string, signal?: AbortSignal): Promise<StreamOperationResponse> {
  return apiRequest<StreamOperationResponse>(`/streams/${encodeURIComponent(streamId)}/unbind-camera`, {
    method: 'POST',
    signal,
  });
}

export async function promoteStream(streamId: string, data: PromoteStreamRequest, signal?: AbortSignal): Promise<StreamOperationResponse> {
  return apiRequest<StreamOperationResponse>(`/streams/${encodeURIComponent(streamId)}/promote`, {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

export async function deleteStream(streamId: string, signal?: AbortSignal): Promise<StreamOperationResponse> {
  return apiRequest<StreamOperationResponse>(`/streams/${encodeURIComponent(streamId)}`, {
    method: 'DELETE',
    signal,
  });
}

export async function kickPublisher(streamId: string, signal?: AbortSignal): Promise<StreamOperationResponse> {
  return apiRequest<StreamOperationResponse>(`/streams/${encodeURIComponent(streamId)}/kick-publisher`, {
    method: 'POST',
    signal,
  });
}

export interface StreamMetricSample {
  ts: number;            // Unix seconds
  in_fps: number;        // publisher input frame rate
  bitrate_kbits: number; // publisher bitrate (audio+video combined)
  subscribers: number;   // number of active subscribers
}

export type StreamMetricsPeriod = '5m' | '15m' | '30m';

export interface StreamHistorySession {
  id: number;
  stream_id: string;
  app_name: string;
  protocol: string;
  remote_addr: string;
  session_id: string;
  started_at: string;
  ended_at?: string;
  duration_sec: number;
}

export interface StreamHistoryResponse {
  history: StreamHistorySession[];
  total: number;
  limit: number;
  offset: number;
}

export async function listStreamHistory(
  streamId: string,
  limit = 10,
  signal?: AbortSignal,
): Promise<StreamHistoryResponse> {
  const query = new URLSearchParams({
    stream_id: streamId,
    limit: String(limit),
    offset: '0',
  });
  const data = await apiRequest<StreamHistoryResponse>(`/streams/history?${query}`, { signal });
  return { ...data, history: data.history ?? [] };
}

export async function getStreamMetricsHistory(
  streamId: string,
  period: StreamMetricsPeriod = '15m',
  signal?: AbortSignal,
): Promise<StreamMetricSample[]> {
  const path = `/streams/${encodeURIComponent(streamId)}/metrics/history?period=${period}`;
  const data = await apiRequest<StreamMetricSample[]>(path, { signal });
  return data ?? [];
}
