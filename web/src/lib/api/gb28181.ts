import { apiRequest } from './client';

export interface GB28181Channel {
  channel_id: string;
  name?: string;
  is_playing?: boolean;
  stream_id?: string;
  stream_number_list?: string;
  encode_type?: string;
}

export interface GB28181Device {
  device_id: string;
  gb_version?: '2016' | '2022' | 'unknown';
  name?: string;
  manufacturer?: string;
  model?: string;
  firmware?: string;
  is_online: boolean;
  address: string;
  last_keepalive_at?: string;
  last_register_at?: string;
  channels: GB28181Channel[];
}

export interface GB28181DevicesResponse {
  devices: GB28181Device[];
}

export interface GB28181PlayRequest {
  device_id: string;
  channel_id: string;
  stream_id?: string;
  ensure?: boolean;
}

export interface GB28181PlayResponse {
  ssrc: string;
  stream_id: string;
  state?: 'ready' | 'starting';
}

// Platform types
export interface GB28181Platform {
  id: number;
  name: string;
  enable: boolean;
  server_gb_id: string;
  server_ip: string;
  server_port: number;
  device_gb_id: string;
  device_ip: string;
  device_port: number;
  transport: string;
  status?: boolean;
}

export interface GB28181PlatformsResponse {
  platforms: GB28181Platform[];
}

export interface AddPlatformRequest {
  name: string;
  enable: boolean;
  server_gb_id: string;
  server_gb_domain?: string;
  server_ip: string;
  server_port: number;
  device_gb_id?: string;
  device_gb_domain?: string;
  device_ip?: string;
  device_port?: number;
  username?: string;
  password?: string;
  transport?: string;
  character_set?: string;
  expires?: number;
  keep_timeout?: number;
  max_timeout_count?: number;
}

// Alarm types
export interface GB28181Alarm {
  id: number;
  device_id: string;
  channel_id: string;
  alarm_type: string;
  alarm_time: string;
  priority: number;
  method: string;
  description: string;
  created_at: string;
}

export interface GB28181AlarmsResponse {
  alarms: GB28181Alarm[];
  total: number;
}

// Download types
export interface GB28181Download {
  id: number;
  device_id: string;
  channel_id: string;
  file_path: string;
  start_time: string;
  end_time: string;
  file_size: number;
  status: string;
  created_at: string;
}

export interface GB28181DownloadsResponse {
  downloads: GB28181Download[];
  total: number;
}

// Device API
export async function listGB28181Devices(signal?: AbortSignal): Promise<GB28181DevicesResponse> {
  return apiRequest<GB28181DevicesResponse>('/gb28181/devices', { signal });
}

export async function playGB28181Stream(req: GB28181PlayRequest, signal?: AbortSignal): Promise<GB28181PlayResponse> {
  return apiRequest<GB28181PlayResponse>('/gb28181/play', {
    method: 'POST',
    body: JSON.stringify(req),
    signal,
  });
}

export async function stopGB28181Stream(deviceId: string, channelId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/gb28181/stop', {
    method: 'POST',
    body: JSON.stringify({ device_id: deviceId, channel_id: channelId }),
    signal,
  });
}

function gb28181ChannelPath(deviceId: string, channelId: string): string {
  return `/gb28181/devices/${encodeURIComponent(deviceId)}/channels/${encodeURIComponent(channelId)}`;
}

export interface GB28181PTZControlRequest {
  direction: string;
  speed?: number;
}

export async function controlGB28181PTZ(
  deviceId: string,
  channelId: string,
  req: GB28181PTZControlRequest,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`${gb28181ChannelPath(deviceId, channelId)}/ptz`, {
    method: 'POST',
    body: JSON.stringify(req),
    signal,
  });
}

export async function controlGB28181Recording(
  deviceId: string,
  channelId: string,
  command: 'record' | 'stop',
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`${gb28181ChannelPath(deviceId, channelId)}/record`, {
    method: 'POST',
    body: JSON.stringify({ command }),
    signal,
  });
}

// Platform API
export async function listGB28181Platforms(signal?: AbortSignal): Promise<GB28181PlatformsResponse> {
  return apiRequest<GB28181PlatformsResponse>('/gb28181/platforms', { signal });
}

export async function addGB28181Platform(req: AddPlatformRequest, signal?: AbortSignal): Promise<{ id: number; status: string }> {
  return apiRequest<{ id: number; status: string }>('/gb28181/platforms', {
    method: 'POST',
    body: JSON.stringify(req),
    signal,
  });
}

export async function deleteGB28181Platform(id: number, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/gb28181/platforms?id=${id}`, {
    method: 'DELETE',
    signal,
  });
}

// Broadcast API
export async function startBroadcast(deviceId: string, channelId: string, signal?: AbortSignal): Promise<{ session_id: string; port: number; ssrc: string }> {
  return apiRequest<{ session_id: string; port: number; ssrc: string }>('/gb28181/broadcast/start', {
    method: 'POST',
    body: JSON.stringify({ device_id: deviceId, channel_id: channelId }),
    signal,
  });
}

export async function stopBroadcast(deviceId: string, channelId: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/gb28181/broadcast/stop', {
    method: 'POST',
    body: JSON.stringify({ device_id: deviceId, channel_id: channelId }),
    signal,
  });
}

// Alarm API
export async function listGB28181Alarms(deviceId?: string, limit = 50, offset = 0, signal?: AbortSignal): Promise<GB28181AlarmsResponse> {
  const params = new URLSearchParams();
  if (deviceId) params.set('device_id', deviceId);
  params.set('limit', String(limit));
  params.set('offset', String(offset));
  return apiRequest<GB28181AlarmsResponse>(`/gb28181/alarms?${params}`, { signal });
}

// Download API
export async function stopDownload(deviceId: string, channelId: string, downloadId: number, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/gb28181/download/stop', {
    method: 'POST',
    body: JSON.stringify({ device_id: deviceId, channel_id: channelId, download_id: downloadId }),
    signal,
  });
}

export async function listGB28181Downloads(deviceId?: string, channelId?: string, limit = 50, offset = 0, signal?: AbortSignal): Promise<GB28181DownloadsResponse> {
  const params = new URLSearchParams();
  if (deviceId) params.set('device_id', deviceId);
  if (channelId) params.set('channel_id', channelId);
  params.set('limit', String(limit));
  params.set('offset', String(offset));
  return apiRequest<GB28181DownloadsResponse>(`/gb28181/downloads?${params}`, { signal });
}

// --- Device Recording Query ---

export interface DeviceRecordItem {
  start_time: string;
  end_time: string;
  date?: string;
}

export interface RecordTimeSegment {
  start: number; // Unix timestamp
  end: number;   // Unix timestamp
}

export interface RecordDayData {
  date: string;  // "2006-01-02"
  items: RecordTimeSegment[];
}

export interface DeviceRecordResponse {
  day_total: number;
  time_num: number;
  data: RecordDayData[];
}

// Transform backend response to flat record list
export function transformRecords(response: DeviceRecordResponse): DeviceRecordItem[] {
  const records: DeviceRecordItem[] = [];
  for (const day of response.data || []) {
    for (const item of day.items || []) {
      records.push({
        start_time: new Date(item.start * 1000).toISOString(),
        end_time: new Date(item.end * 1000).toISOString(),
        date: day.date,
      });
    }
  }
  return records;
}

// Get timeline data grouped by day
export function getTimelineData(response: DeviceRecordResponse): Array<{date: string, segments: Array<{start: number, end: number, startTime: string, endTime: string}>}> {
  const result: Array<{date: string, segments: Array<{start: number, end: number, startTime: string, endTime: string}>}> = [];
  for (const day of response.data || []) {
    const segments = (day.items || []).map(item => ({
      start: item.start,
      end: item.end,
      startTime: new Date(item.start * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
      endTime: new Date(item.end * 1000).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' }),
    }));
    result.push({ date: day.date, segments });
  }
  return result;
}

export interface QueryDeviceRecordRequest {
  device_id: string;
  channel_id: string;
  start_time: string; // RFC3339 or "2006-01-02T15:04:05"
  end_time: string;
}

export async function queryDeviceRecords(data: QueryDeviceRecordRequest): Promise<DeviceRecordResponse> {
  return apiRequest('/gb28181/record_info', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export interface PlaybackRequest {
  device_id: string;
  channel_id: string;
  stream_id?: string;
  start_time: string;
  end_time: string;
}

export interface PlayURL {
  protocol: string;
  url: string;
}

export interface PlaybackResponse {
  ssrc: string;
  stream_id: string;
  url?: string;      // backward compat
  urls?: PlayURL[];   // multi-protocol support
  message?: string;
}

export async function startDevicePlayback(data: PlaybackRequest): Promise<PlaybackResponse> {
  return apiRequest('/gb28181/playback', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

// Playback speed control
export interface PlaySpeedRequest {
  device_id: string;
  channel_id: string;
  speed: 0.5 | 1 | 2 | 4;
}

// Playback seek control
export interface PlaySeekRequest {
  device_id: string;
  channel_id: string;
  seek_time: number; // seconds from start
}

export async function setPlaybackSpeed(data: PlaySpeedRequest): Promise<{ status: string }> {
  return apiRequest('/gb28181/playback/speed', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function seekPlayback(data: PlaySeekRequest): Promise<{ status: string }> {
  return apiRequest('/gb28181/playback/seek', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export interface PlayPauseRequest {
  device_id: string;
  channel_id: string;
}

export async function pausePlayback(data: PlayPauseRequest): Promise<{ status: string }> {
  return apiRequest('/gb28181/playback/pause', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function resumePlayback(data: PlayPauseRequest): Promise<{ status: string }> {
  return apiRequest('/gb28181/playback/resume', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

// Download API
export interface DownloadRequest {
  device_id: string;
  channel_id: string;
  start_time: string;
  end_time: string;
}

export interface DownloadResponse {
  download_id: number;
  file_path: string;
  status: string;
}

export interface BatchDownloadRequest {
  device_id: string;
  channel_id: string;
  segments: Array<{
    start_time: string;
    end_time: string;
  }>;
}

export interface BatchDownloadResponse {
  downloads: DownloadResponse[];
  errors: string[];
  total: number;
}

export async function startDownload(data: DownloadRequest): Promise<DownloadResponse> {
  return apiRequest('/gb28181/download/start', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function batchDownload(data: BatchDownloadRequest): Promise<BatchDownloadResponse> {
  return apiRequest('/gb28181/download/batch', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

// Platform Events API
export interface PlatformEvent {
  id: number;
  platform_id: number;
  platform_name: string;
  event_type: string;
  server_ip: string;
  server_port: number;
  channel_id: string;
  stream_id: string;
  details: string;
  created_at: string;
}

export interface PlatformEventsResponse {
  events: PlatformEvent[];
  total: number;
}

export interface PlatformStatus {
  platform_id: number;
  platform_name: string;
  server_ip: string;
  server_port: number;
  enable: boolean;
  is_online: boolean;
  last_event_type: string;
  last_event_time: string;
}

export interface PlatformStatusResponse {
  platforms: PlatformStatus[];
}

export async function listPlatformEvents(platformId?: number, eventType?: string, limit = 50, offset = 0): Promise<PlatformEventsResponse> {
  const params = new URLSearchParams();
  if (platformId) params.set('platform_id', String(platformId));
  if (eventType) params.set('event_type', eventType);
  params.set('limit', String(limit));
  params.set('offset', String(offset));
  return apiRequest(`/gb28181/platform/events?${params}`);
}

export async function getPlatformStatus(): Promise<PlatformStatusResponse> {
  return apiRequest('/gb28181/platform/status');
}
