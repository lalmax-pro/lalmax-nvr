import { apiRequest } from './client';

export type TranscodeBackend = 'ffmpeg' | 'gstreamer';
export interface TranscodeProfile {
  id: string; name: string; revision: number; backend: TranscodeBackend;
  video: { codec: string; encoder: string; max_width: number; max_height: number; fps: number; bitrate_kbps: number; gop_frames: number };
  audio: { codec: string; sample_rate: number; channels: number; bitrate_kbps: number };
}
export interface TranscodeJob {
  id: string; source: { engine_id: string; stream_id: string; app_name: string };
  profile_id: string; profile_revision: number; enabled: boolean;
  run_policy: 'always' | 'manual' | 'on_demand'; generation: number; stopped_by_user?: boolean;
}
export interface TranscodeVariant {
  id: string; job_id: string; source_id: string; state: string; error_code?: string; retry_at?: string;
}
// Read-only catalog for a single logical source. The server verifies the job
// belongs to this source before issuing any actual playback target.
export function listStreamTranscodeVariants(streamID: string, signal?: AbortSignal) {
  return apiRequest<{ variants: TranscodeVariant[] }>(
    `/streams/${encodeURIComponent(streamID)}/variants`, { signal },
  );
}

// Always use the authenticated same-origin FLV proxy; physical output IDs,
// engine credentials and token-bearing RTSP URLs never reach the browser.
export function transcodePreviewFLVURL(streamID: string, jobID: string): string {
  return `/api/streams/${encodeURIComponent(streamID)}/variants/${encodeURIComponent(jobID)}/stream.flv`;
}

export function listTranscodeProfiles(signal?: AbortSignal) {
  return apiRequest<{ profiles: TranscodeProfile[] }>('/transcoding/profiles', { signal });
}
export function listTranscodeJobs(signal?: AbortSignal) {
  return apiRequest<{ jobs: TranscodeJob[]; variants: TranscodeVariant[]; runtime_available: boolean }>('/transcoding/jobs', { signal });
}
export function createTranscodeProfile(profile: TranscodeProfile, signal?: AbortSignal) {
  return apiRequest<TranscodeProfile>('/transcoding/profiles', { method: 'POST', body: JSON.stringify(profile), signal });
}
export function updateTranscodeProfile(profile: TranscodeProfile, signal?: AbortSignal) {
  if (!profile.revision) throw new Error('Profile revision is required for updates');
  return apiRequest<TranscodeProfile>(`/transcoding/profiles/${encodeURIComponent(profile.id)}`, {
    method: 'PATCH', body: JSON.stringify(profile), signal,
  });
}

export function deleteTranscodeProfile(profile: TranscodeProfile, signal?: AbortSignal) {
  if (!profile.revision) throw new Error('Profile revision is required for deletion');
  return apiRequest<void>(
    `/transcoding/profiles/${encodeURIComponent(profile.id)}?revision=${profile.revision}`,
    { method: 'DELETE', signal },
  );
}

export function probeSavedTranscodeProfile(id: string, signal?: AbortSignal) {
  return apiRequest<CapabilityReport>(`/transcoding/profiles/${encodeURIComponent(id)}/probe`, { method: 'POST', signal });
}
export function saveTranscodeJob(job: TranscodeJob, signal?: AbortSignal) {
  const path = job.generation ? `/transcoding/jobs/${encodeURIComponent(job.id)}` : '/transcoding/jobs';
  return apiRequest<TranscodeJob>(path, { method: job.generation ? 'PATCH' : 'POST', body: JSON.stringify(job), signal });
}
export function transcodeJobAction(job: TranscodeJob, action: 'start' | 'stop' | 'resume' | 'delete', signal?: AbortSignal) {
  const suffix = action === 'delete' ? '' : `/${action}`;
  return apiRequest<unknown>(`/transcoding/jobs/${encodeURIComponent(job.id)}${suffix}?generation=${job.generation}`, { method: action === 'delete' ? 'DELETE' : 'POST', signal });
}
export interface TranscodeDemandLease {
  token: string; job_id: string; generation: number; expires_at: string;
}
export function acquireTranscodeDemandLease(jobID: string, generation: number, waitMs = 20000) {
  if (!Number.isSafeInteger(waitMs) || waitMs < 1 || waitMs > 20000) throw new Error('waitMs must be 1–20000');
  return apiRequest<{lease: TranscodeDemandLease; runtime: {state: string}}>(
    `/transcoding/jobs/${encodeURIComponent(jobID)}/leases?generation=${generation}&wait_ms=${waitMs}`,
    {method:'POST'},
  );
}
export function renewTranscodeDemandLease(token: string) {
  return apiRequest<TranscodeDemandLease>(
    `/transcoding/leases/renew`, {method:'POST', body:JSON.stringify({token})},
  );
}
export function releaseTranscodeDemandLease(token: string) {
  return apiRequest<void>(
    `/transcoding/leases/release`, {method:'POST', body:JSON.stringify({token})},
  );
}

export interface CapabilityReport {
  backend: TranscodeBackend;
  profile_id: string;
  fingerprint: string;
  checked_at: string;
  can_enable: boolean;
  stale: boolean;
  checks: { name: string; status: 'passed' | 'failed' | 'not_tested' | 'unsupported'; detail: string }[];
}
export function getTranscodeCapabilities(signal?: AbortSignal) {
  return apiRequest<{ enabled: boolean; reports: CapabilityReport[] }>('/transcoding/capabilities', { signal });
}
export function probeTranscodeCapabilities(backend: TranscodeBackend, signal?: AbortSignal) {
  return apiRequest<CapabilityReport>('/transcoding/capabilities/probe', {
    method: 'POST', body: JSON.stringify({ backend }), signal,
  });
}

export function derivedRecordingAction(job: TranscodeJob, action: 'start' | 'stop', signal?: AbortSignal) {
  return apiRequest<void>(
    `/transcoding/jobs/${encodeURIComponent(job.id)}/record/${action}?generation=${job.generation}`,
    { method: 'POST', signal },
  );
}
