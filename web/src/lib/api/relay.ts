import { apiRequest } from './client';

export interface RelayTask {
  id: string;
  stream_id: string;
  target_url: string;
  status: 'running' | 'stopped' | 'error';
  error_msg?: string;
  created_at: string;
  started_at?: string;
  stopped_at?: string;
}

export interface RelayTaskStats {
  task_id: string;
  stream_id: string;
  target_url: string;
  status: 'running' | 'stopped' | 'error';
  started_at?: string;
  stopped_at?: string;
  video_fps: number;
  video_bitrate: number;
  audio_fps: number;
  audio_bitrate: number;
  total_bytes: number;
  duration: number;
}

export interface StatSample {
  timestamp: string;
  video_fps: number;
  video_bitrate: number;
  audio_fps: number;
  audio_bitrate: number;
  total_bytes: number;
}

export interface StatsHistory {
  task_id: string;
  samples: StatSample[];
}

export interface CreateRelayTaskRequest {
  stream_id: string;
  target_url: string;
}

export async function listRelayTasks(signal?: AbortSignal): Promise<RelayTask[]> {
  return apiRequest<RelayTask[]>('/relay/tasks', { signal });
}

export async function getRelayTask(taskId: string, signal?: AbortSignal): Promise<RelayTask> {
  return apiRequest<RelayTask>(`/relay/tasks/${encodeURIComponent(taskId)}`, { signal });
}

export async function createRelayTask(req: CreateRelayTaskRequest): Promise<RelayTask> {
  return apiRequest<RelayTask>('/relay/tasks', {
    method: 'POST',
    body: JSON.stringify(req),
  });
}

export async function deleteRelayTask(taskId: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/relay/tasks/${encodeURIComponent(taskId)}`, {
    method: 'DELETE',
  });
}

export async function startRelayTask(taskId: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/relay/tasks/${encodeURIComponent(taskId)}/start`, {
    method: 'POST',
  });
}

export async function stopRelayTask(taskId: string): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/relay/tasks/${encodeURIComponent(taskId)}/stop`, {
    method: 'POST',
  });
}

export async function getRelayTaskStats(taskId: string, signal?: AbortSignal): Promise<RelayTaskStats> {
  return apiRequest<RelayTaskStats>(`/relay/tasks/${encodeURIComponent(taskId)}/stats`, { signal });
}

export async function getRelayTaskStatsHistory(
  taskId: string, 
  durationMinutes: number = 60,
  signal?: AbortSignal
): Promise<StatsHistory> {
  return apiRequest<StatsHistory>(
    `/relay/tasks/${encodeURIComponent(taskId)}/stats/history?duration=${durationMinutes}`,
    { signal }
  );
}