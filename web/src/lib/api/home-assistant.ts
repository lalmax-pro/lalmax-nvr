import { apiRequest } from './client';

export interface HomeAssistantPublication {
  stream_id: string;
  name?: string;
  app_name: string;
}

export interface HomeAssistantSettings {
  instance_id: string;
  enabled: boolean;
  rtsp_base_url: string;
  streams: HomeAssistantPublication[];
}

export function getHomeAssistantSettings(signal?: AbortSignal) {
  return apiRequest<HomeAssistantSettings>('/settings/home-assistant', { signal });
}

export function saveHomeAssistantSettings(settings: HomeAssistantSettings, signal?: AbortSignal) {
  const { enabled, rtsp_base_url, streams } = settings;
  return apiRequest<HomeAssistantSettings>('/settings/home-assistant', {
    method: 'PUT', body: JSON.stringify({ enabled, rtsp_base_url, streams }), signal,
  });
}

export function publishHomeAssistantStream(id: string, enabled: boolean) {
  return apiRequest<HomeAssistantSettings>(`/integrations/home-assistant/streams/${encodeURIComponent(id)}`, {
    method: 'PUT', body: JSON.stringify({ enabled }),
  });
}
