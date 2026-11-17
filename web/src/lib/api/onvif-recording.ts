/**
 * ONVIF Recording API — query and playback recordings from ONVIF device SD cards
 */
import { apiRequest } from './client';

// --- Types ---

export interface ONVIFRecordingSource {
  source_id: string;
  name: string;
  location: string;
  description: string;
}

export interface ONVIFRecordingTrack {
  token: string;
  track_type: string;
  description: string;
  segments?: ONVIFRecordingSegment[];
}

export interface ONVIFRecording {
  token: string;
  name: string;
  description: string;
  source: ONVIFRecordingSource;
  start_time: string;
  end_time: string;
  status: string;
  tracks?: ONVIFRecordingTrack[];
}

export interface ONVIFRecordingSegment {
  token: string;
  recording_token: string;
  start_time: string;
  end_time: string;
  file_path: string;
  duration: number;
  size: number;
  recording_bounds?: boolean;
}

export interface ONVIFRecordingsResponse {
  recordings: ONVIFRecording[];
}

export interface ONVIFRecordingSegmentsResponse {
  segments: ONVIFRecordingSegment[];
  fallback?: boolean;
  recordings?: ONVIFRecording[];
}

export interface ONVIFReplayResponse {
  uri: string;
  protocol: string;
}

export interface ONVIFRecordingSearchParams {
  start_time: string;
  end_time?: string;
  max_results?: number;
  signal?: AbortSignal;
}

// --- Functions ---

/**
 * Get recordings from an ONVIF device
 */
export async function getONVIFRecordings(
  cameraId: string,
  params?: {
    start_time?: string;
    end_time?: string;
    signal?: AbortSignal;
  }
): Promise<ONVIFRecordingsResponse> {
  const query = new URLSearchParams();
  if (params?.start_time) query.set('start_time', params.start_time);
  if (params?.end_time) query.set('end_time', params.end_time);

  const qs = query.toString();
  return apiRequest<ONVIFRecordingsResponse>(
    `/cameras/${cameraId}/onvif/recordings${qs ? `?${qs}` : ''}`,
    { signal: params?.signal }
  );
}

/**
 * Get detailed information for a specific recording
 */
export async function getONVIFRecordingInformation(
  cameraId: string,
  recordingToken: string,
  signal?: AbortSignal
): Promise<ONVIFRecording> {
  return apiRequest<ONVIFRecording>(
    `/cameras/${cameraId}/onvif/recordings/${encodeURIComponent(recordingToken)}`,
    { signal }
  );
}

/**
 * Search for recording segments on an ONVIF device
 */
export async function searchONVIFRecordings(
  cameraId: string,
  params: ONVIFRecordingSearchParams
): Promise<ONVIFRecordingSegmentsResponse> {
  const query = new URLSearchParams();
  query.set('start_time', params.start_time);
  if (params.end_time) query.set('end_time', params.end_time);
  if (params.max_results) query.set('max_results', String(params.max_results));

  return apiRequest<ONVIFRecordingSegmentsResponse>(
    `/cameras/${cameraId}/onvif/recordings/search?${query.toString()}`,
    { signal: params.signal }
  );
}

/**
 * Get replay URI for a recording
 */
export async function getONVIFReplayURI(
  cameraId: string,
  recordingToken: string,
  signal?: AbortSignal
): Promise<ONVIFReplayResponse> {
  return apiRequest<ONVIFReplayResponse>(
    `/cameras/${cameraId}/onvif/replay/${encodeURIComponent(recordingToken)}`,
    { signal }
  );
}
