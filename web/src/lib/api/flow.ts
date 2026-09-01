/**
 * Camera flow API — source → engine → recording/viewers
 */
import { apiRequest } from './client';

export interface CameraFlow {
  camera_id: string;
  name: string;
  status: string;
  protocol: string;
  encoding: string;
  error?: string;
  source: {
    active: boolean;
    url_host?: string;
    transport?: string;
  };
  engine?: {
    active: boolean;
    video_codec?: string;
    audio_codec?: string;
    fps: number;
    last_frame_age_s: number;
    publisher_protocol?: string;
  };
  recording: {
    status: string;
    paused: boolean;
    merge_pending: number;
  };
  substream: { active: boolean };
  viewers_by_protocol: Record<string, number>;
}

export async function getCameraFlow(cameraId: string, signal?: AbortSignal): Promise<CameraFlow> {
  return apiRequest<CameraFlow>(`/cameras/${encodeURIComponent(cameraId)}/flow`, { signal });
}

export async function getFlowStreams(signal?: AbortSignal): Promise<CameraFlow[]> {
  const res = await apiRequest<{ cameras: CameraFlow[] }>('/flow/streams', { signal });
  return res.cameras || [];
}
