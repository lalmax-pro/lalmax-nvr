import { apiRequest } from './client';

export interface IPTVImportJob {
  id: string;
  name: string;
  playlist_url?: string;
  status: string;
  error?: string;
  total_items: number;
  created_at: string;
  updated_at: string;
}

export interface IPTVImportItem {
  id: string;
  job_id: string;
  row_no: number;
  external_id: string;
  name: string;
  group_name?: string;
  logo_url?: string;
  channel_no?: string;
  status: string;
  error?: string;
  http_status?: number;
  video_codec?: string;
  audio_codec?: string;
  encrypted?: boolean;
  drm?: boolean;
  playable?: boolean;
  recordable?: boolean;
  checked_at?: string;
}

export interface IPTVSource {
  id: string;
  name: string;
  playlist_url?: string;
  enabled: boolean;
  refresh_interval_sec: number;
  last_error?: string;
}

export interface IPTVChannel {
  id: string;
  source_id: string;
  external_id: string;
  stream_id: string;
  channel_no?: string;
  name: string;
  group_name?: string;
  logo_url?: string;
  enabled: boolean;
  favorite: boolean;
  probe_status: string;
  probe_error?: string;
  video_codec?: string;
  audio_codec?: string;
  playable: boolean;
  recordable: boolean;
  publish_enabled: boolean;
}

export interface IPTVPlaybackDetails {
  url: string;
}

export async function createIPTVImport(data: {
  name?: string;
  playlist_url?: string;
  playlist_text?: string;
  headers?: Record<string, string>;
}): Promise<IPTVImportJob> {
  return apiRequest<IPTVImportJob>('/iptv/imports', {
    method: 'POST',
    body: JSON.stringify(data),
  });
}

export async function getIPTVImport(id: string): Promise<IPTVImportJob> {
  return apiRequest<IPTVImportJob>(`/iptv/imports/${encodeURIComponent(id)}`);
}

export async function listIPTVImportItems(id: string, params?: { status?: string; q?: string }): Promise<{ items: IPTVImportItem[]; total: number }> {
  const search = new URLSearchParams();
  if (params?.status) search.set('status', params.status);
  if (params?.q) search.set('q', params.q);
  const query = search.toString();
  return apiRequest(`/iptv/imports/${encodeURIComponent(id)}/items${query ? `?${query}` : ''}`);
}

export async function probeIPTVImportItem(jobId: string, itemId: string): Promise<IPTVImportItem> {
  return apiRequest(`/iptv/imports/${encodeURIComponent(jobId)}/items/${encodeURIComponent(itemId)}/probe`, {
    method: 'POST',
  });
}

export async function commitIPTVImport(jobId: string, itemIds: string[]): Promise<{ source: IPTVSource; channel_count: number }> {
  return apiRequest(`/iptv/imports/${encodeURIComponent(jobId)}/commit`, {
    method: 'POST',
    body: JSON.stringify({ item_ids: itemIds }),
  });
}

export async function listIPTVSources(): Promise<IPTVSource[]> {
  const res = await apiRequest<{ sources: IPTVSource[] }>('/iptv/sources');
  return res.sources ?? [];
}

export async function deleteIPTVSource(id: string): Promise<void> {
  await apiRequest(`/iptv/sources/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

export async function listIPTVGroups(): Promise<string[]> {
  const res = await apiRequest<{ groups: string[] }>('/iptv/groups');
  return res.groups ?? [];
}

export async function listIPTVChannels(params?: { source_id?: string; group?: string; q?: string; favorite?: boolean }): Promise<IPTVChannel[]> {
  const search = new URLSearchParams();
  if (params?.source_id) search.set('source_id', params.source_id);
  if (params?.group) search.set('group', params.group);
  if (params?.q) search.set('q', params.q);
  if (params?.favorite !== undefined) search.set('favorite', String(params.favorite));
  const query = search.toString();
  const res = await apiRequest<{ channels: IPTVChannel[] }>(`/iptv/channels${query ? `?${query}` : ''}`);
  return res.channels ?? [];
}

export async function getIPTVChannelPlayback(id: string): Promise<IPTVPlaybackDetails> {
  return apiRequest(`/iptv/channels/${encodeURIComponent(id)}/playback`);
}

export async function updateIPTVChannel(id: string, data: { name?: string; enabled?: boolean; favorite?: boolean; publish_enabled?: boolean }): Promise<IPTVChannel> {
  return apiRequest(`/iptv/channels/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
}

export async function deleteIPTVChannel(id: string): Promise<void> {
  await apiRequest(`/iptv/channels/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
