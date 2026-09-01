/**
 * Settings API — cleanup, webdav, merge, feature flags
 */
import { apiRequest } from './client';

// --- Types ---

export interface CleanupConfig {
  retention_days: number;
  disk_threshold_percent: number;
  check_interval: string;
}

export interface WebDAVConfig {
  enabled: boolean;
  path_prefix: string;
  read_write: boolean;
}

export interface WebRTCConfig {
  enabled: boolean;
  max_viewers: number;
  idle_timeout: string;
}

export interface FLVStreamingConfig {
  enabled: boolean;
  max_viewers: number;
  idle_timeout: string;
  gop_cache_size: number;
}

export interface HLSStreamingConfig {
  low_latency: boolean;
}

export interface RTMPConfig {
  enabled: boolean;
  port: number;
  stream_keys?: Record<string, string>; // stream_key → camera_id
}

export interface SRTStreamConfig {
  stream_id: string;
  camera_id: string;
  mode: string;       // "listener" or "caller"
  address: string;
  passphrase: string;
}

export interface SRTConfig {
  enabled: boolean;
  port: number;
  streams?: SRTStreamConfig[];
}

export interface WHIPConfig {
  enabled: boolean;
  url?: string;
  ice_mux_port?: number;
}

export interface StreamingConfig {
  default_protocol: string; // webrtc | flv | ws-flv | hls | ll-hls
  auto_stop_no_view_sec?: number; // seconds to wait before stopping stream when no viewers (default 300)
  webrtc: WebRTCConfig;
  flv: FLVStreamingConfig;
  hls: HLSStreamingConfig;
  rtmp?: RTMPConfig;
  srt?: SRTConfig;
  whip?: WHIPConfig;
}

export interface SettingsConfig {
  cleanup: CleanupConfig;
  webdav: WebDAVConfig;
  streaming?: StreamingConfig;
}

export interface MergeStatus {
  enabled: boolean;
  last_run_time: string;
  segments_merged: number;
  files_created: number;
  error_count: number;
}

export interface MergePending {
  enabled: boolean;
  pending: Record<string, number>;
}

export interface FeatureFlags {
  protocols: Record<string, boolean>;
}

// --- Settings ---

export async function getSettings(signal?: AbortSignal): Promise<SettingsConfig> {
  return apiRequest<SettingsConfig>('/settings', { signal });
}

export async function updateSettings(
  settings: SettingsConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings', {
    method: 'PUT',
    body: JSON.stringify(settings),
    signal,
  });
}

// --- Global merge settings ---

export async function getMergeSettings(signal?: AbortSignal): Promise<MergeConfig> {
  return apiRequest<MergeConfig>('/settings/merge', { signal });
}

export async function updateMergeSettings(
  config: MergeConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/merge', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// MergeConfig type — re-exported from cameras module for convenience
export type { MergeConfig } from './cameras';

// --- Merge status ---

export async function getMergeStatus(signal?: AbortSignal): Promise<MergeStatus> {
  return apiRequest<MergeStatus>('/merge/status', { signal });
}

export async function getMergePending(signal?: AbortSignal): Promise<MergePending> {
  return apiRequest<MergePending>('/merge/pending', { signal });
}

// --- Feature flags ---

export async function getFeatures(signal?: AbortSignal): Promise<FeatureFlags> {
  return apiRequest<FeatureFlags>('/features', { signal });
}

export async function updateFeatures(
  features: FeatureFlags,
  signal?: AbortSignal
): Promise<void> {
  await apiRequest('/features', {
    method: 'PUT',
    body: JSON.stringify(features),
    signal,
  });
}

// --- Streaming settings ---

export async function getStreamingSettings(signal?: AbortSignal): Promise<StreamingConfig> {
  return apiRequest<StreamingConfig>('/settings/streaming', { signal });
}

export async function updateStreamingSettings(
  config: StreamingConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/streaming', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

export interface AutoDiscoverSettings {
  enabled: boolean;
  listen_for_hello: boolean;
  scan_interval: string;
  default_username?: string;
  has_password?: boolean;
  network_interface?: string;
}

export async function getAutoDiscoverSettings(signal?: AbortSignal): Promise<AutoDiscoverSettings> {
  return apiRequest<AutoDiscoverSettings>('/settings/auto-discover', { signal });
}

export async function updateAutoDiscoverSettings(
  config: Partial<AutoDiscoverSettings> & { default_password?: string },
  signal?: AbortSignal
): Promise<AutoDiscoverSettings> {
  return apiRequest<AutoDiscoverSettings>('/settings/auto-discover', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- GB28181 settings ---

export interface GB28181Config {
  enabled: boolean;
  host: string;
  port: number;
  id: string;
  password: string;
  media_ip: string;
  media_port?: number;
  standard_version: '2016' | '2022';
}

export async function getGB28181Settings(signal?: AbortSignal): Promise<GB28181Config> {
  return apiRequest<GB28181Config>('/settings/gb28181', { signal });
}

export async function updateGB28181Settings(
  config: GB28181Config,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/gb28181', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- Config management ---

export async function reloadConfig(signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/config/reload', {
    method: 'POST',
    signal,
  });
}

export async function checkConfigChange(signal?: AbortSignal): Promise<{ changed: boolean }> {
  return apiRequest<{ changed: boolean }>('/config/check', { signal });
}

export async function regenerateLalmaxConfig(signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/lalmax/regenerate', {
    method: 'POST',
    signal,
  });
}

// --- HLS settings ---

export interface HLSConfig {
  enabled?: boolean;
  on_demand?: boolean;
  idle_timeout?: string;
  segment_count: number;
  lal_fragment_duration_ms: number;
  lal_fragment_num: number;
  lal_cleanup_mode: number;
  lal_use_memory: boolean;
  lalmax_segment_duration: number;
  lalmax_part_duration: number;
}

export async function getHLSSettings(signal?: AbortSignal): Promise<HLSConfig> {
  return apiRequest<HLSConfig>('/settings/hls', { signal });
}

export async function updateHLSSettings(
  config: HLSConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>('/settings/hls', {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}
