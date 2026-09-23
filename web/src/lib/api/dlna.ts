import { apiRequest } from './client';

export interface DLNAConfig {
  enabled: boolean;
  friendly_name: string;
  uuid?: string;
  advertise_url?: string;
  interface?: string;
  allowed_cidrs?: string[];
  include_live: boolean;
  include_recordings: boolean;
  max_browse_count: number;
  max_media_viewers: number;
}

export function getDLNASettings(signal?: AbortSignal) {
  return apiRequest<DLNAConfig>('/settings').then((settings: any) => settings.dlna as DLNAConfig);
}

export function updateDLNASettings(config: Partial<DLNAConfig>, signal?: AbortSignal) {
  return apiRequest<{ status: string }>('/settings', {
    method: 'PUT',
    body: JSON.stringify({ dlna: config }),
    signal,
  });
}
