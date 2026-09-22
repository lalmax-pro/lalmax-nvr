/**
 * Recording plans — stream-keyed recording policy.
 * A stream is recorded only when an enabled plan says so.
 */
import { apiRequest } from './client';
import type { RecordingMode, RecordingScheduleRange } from './cameras';

export interface RecordingPlan {
  id: string;
  stream_id: string;
  name: string;
  mode: RecordingMode;
  enabled: boolean;
  windows?: RecordingScheduleRange[];
  created_at?: string;
  updated_at?: string;
}

export interface RecordingPlanRequest {
  stream_id?: string;
  name?: string;
  mode?: RecordingMode;
  enabled?: boolean;
  windows?: RecordingScheduleRange[];
}

export async function listRecordingPlans(signal?: AbortSignal): Promise<RecordingPlan[]> {
  const res = await apiRequest<{ plans: RecordingPlan[] }>('/recording-plans', { signal });
  return res.plans ?? [];
}

export async function getRecordingPlan(id: string, signal?: AbortSignal): Promise<RecordingPlan> {
  return apiRequest<RecordingPlan>(`/recording-plans/${id}`, { signal });
}

export async function createRecordingPlan(
  body: RecordingPlanRequest,
  signal?: AbortSignal,
): Promise<RecordingPlan> {
  return apiRequest<RecordingPlan>('/recording-plans', {
    method: 'POST',
    body: JSON.stringify(body),
    signal,
  });
}

export async function updateRecordingPlan(
  id: string,
  body: RecordingPlanRequest,
  signal?: AbortSignal,
): Promise<RecordingPlan> {
  return apiRequest<RecordingPlan>(`/recording-plans/${id}`, {
    method: 'PUT',
    body: JSON.stringify(body),
    signal,
  });
}

export async function deleteRecordingPlan(
  id: string,
  signal?: AbortSignal,
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/recording-plans/${id}`, {
    method: 'DELETE',
    signal,
  });
}
