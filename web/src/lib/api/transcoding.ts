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
  run_policy: 'always' | 'manual'; generation: number;
}
export interface TranscodeVariant {
  id: string; job_id: string; source_id: string; state: string; error_code?: string; retry_at?: string;
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
export function probeSavedTranscodeProfile(id: string, signal?: AbortSignal) {
  return apiRequest<CapabilityReport>(`/transcoding/profiles/${encodeURIComponent(id)}/probe`, { method: 'POST', signal });
}
export function saveTranscodeJob(job: TranscodeJob, signal?: AbortSignal) {
  const path = job.generation ? `/transcoding/jobs/${encodeURIComponent(job.id)}` : '/transcoding/jobs';
  return apiRequest<TranscodeJob>(path, { method: job.generation ? 'PATCH' : 'POST', body: JSON.stringify(job), signal });
}
export function transcodeJobAction(job: TranscodeJob, action: 'start' | 'stop' | 'delete', signal?: AbortSignal) {
  const suffix = action === 'delete' ? '' : `/${action}`;
  return apiRequest<unknown>(`/transcoding/jobs/${encodeURIComponent(job.id)}${suffix}?generation=${job.generation}`, { method: action === 'delete' ? 'DELETE' : 'POST', signal });
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
