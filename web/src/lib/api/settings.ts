/**
 * Settings API — cleanup, webdav, merge, feature flags
 */
import { apiRequest } from './client';
import type { DLNAConfig } from './dlna';

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
  dlna?: DLNAConfig;
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
): Promise<{ status: string; restart_error?: string }> {
  return apiRequest<{ status: string; restart_error?: string }>('/settings/streaming', {
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
  record_calls?: boolean;
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

// --- VoIP settings ---
export interface VoIPUser {
  username: string;
  password?: string;
  has_password?: boolean;
  record_calls?: boolean;
}
export interface VoIPConfig {
  enabled: boolean;
  supported?: boolean;
  manual_answer: boolean;
  ring_timeout_ms: number;
  pbx_server: string;
  pbx_transport: 'udp' | 'tcp' | 'tls';
  pbx_domain: string;
  pbx_tls_server_name: string;
  pbx_tls_ca_file: string;
  pbx_username: string;
  pbx_password?: string;
  pbx_has_password?: boolean;
  pbx_register_expires: number;
  sip_listen_addr: string;
  sip_tcp_listen_addr: string;
  sip_tls_listen_addr: string;
  sip_dtls_listen_addr: string;
  sip_ws_listen_addr: string;
  sip_wss_listen_addr: string;
  sip_tls_cert_file: string;
  sip_tls_key_file: string;
  sip_ip: string;
  media_ip: string;
  media_port_min: number;
  media_port_max: number;
  realm: string;
  auth_enable: boolean;
  users: VoIPUser[];
  rtp_timeout_ms: number;
  ack_timeout_ms: number;
  srtp_enable: boolean;
  srtp_mandatory: boolean;
  bundle_enable: boolean;
}
export function defaultVoIPConfig(): VoIPConfig {
  return {
    enabled: false, manual_answer: false, ring_timeout_ms: 30000,
    pbx_server: '', pbx_transport: 'udp', pbx_domain: '', pbx_tls_server_name: '', pbx_tls_ca_file: '',
    pbx_username: '', pbx_password: '', pbx_register_expires: 3600,
    sip_listen_addr: '0.0.0.0:5070', sip_tcp_listen_addr: '',
    sip_tls_listen_addr: '', sip_dtls_listen_addr: '', sip_ws_listen_addr: '', sip_wss_listen_addr: '',
    sip_tls_cert_file: '', sip_tls_key_file: '', sip_ip: '', media_ip: '',
    media_port_min: 41000, media_port_max: 42000, realm: 'lalmax-nvr',
    auth_enable: false, users: [], rtp_timeout_ms: 15000, ack_timeout_ms: 32000,
    srtp_enable: false, srtp_mandatory: false, bundle_enable: false,
  };
}
export async function getVoIPSettings(signal?: AbortSignal): Promise<VoIPConfig> {
  return apiRequest<VoIPConfig>('/settings/voip', { signal });
}
export async function updateVoIPSettings(config: VoIPConfig, signal?: AbortSignal): Promise<{ status: string }> {
  const { supported, pbx_has_password, users, ...settings } = config;
  return apiRequest<{ status: string }>('/settings/voip', {
    method: 'PUT',
    body: JSON.stringify({ ...settings, users: users.map(({ username, password, record_calls }) => ({ username, password, record_calls })) }),
    signal,
  });
}

export interface VoIPStatus {
 enabled:boolean;
 incoming_calls_need_answer:boolean;
 upstream_registration:{configured:boolean;server?:string;transport?:string;username?:string;state:string;expires_at?:string;last_error?:string};
 endpoints:{user:string;contact:string;user_agent:string;expires_at:string;remote_addr:string;transport:string}[];
 calls:{call_id:string;stream_id:string;from_user:string;to_user:string;state:string;direction?:string;incoming_pending?:boolean;failure_reason?:string;browser_ready?:boolean;talk_available?:boolean;dtmf_available?:boolean;held:boolean;started_at:string;duration_seconds:number;remote_addr:string;transport:string;audio_codec?:string;video_codec?:string}[];
}
export interface VoIPCallHistoryEntry {
 call_id:string;direction:string;from_user:string;to_user:string;outcome:string;started_at:string;answered_at?:string;ended_at:string;
 duration_seconds:number;failure_reason?:string;remote_addr?:string;transport?:string;audio_codec?:string;video_codec?:string;stream_id?:string;
}
export interface VoIPCallHistory {
 items:VoIPCallHistoryEntry[];total:number;limit:number;offset:number;
}
export function getVoIPCallHistory(limit=20,offset=0,signal?:AbortSignal):Promise<VoIPCallHistory> {
 return apiRequest(`/voip/calls/history?limit=${limit}&offset=${offset}`,{signal});
}
export function getVoIPStatus(signal?:AbortSignal):Promise<VoIPStatus> {return apiRequest('/voip/status',{signal});}
export function hangupVoIPCall(id:string):Promise<{status:string}> {return apiRequest(`/voip/calls/${encodeURIComponent(id)}/hangup`,{method:'POST'});}
export function answerVoIPCall(id:string):Promise<{status:string}> {return apiRequest(`/voip/calls/${encodeURIComponent(id)}/answer`,{method:'POST'});}
export function rejectVoIPCall(id:string):Promise<{status:string}> {return apiRequest(`/voip/calls/${encodeURIComponent(id)}/reject`,{method:'POST'});}
export type VoIPMediaSecurity = '' | 'rtp' | 'sdes' | 'dtls';
export function dialVoIPCall(user:string,security:VoIPMediaSecurity,sdp:string,signal?:AbortSignal):Promise<{call_id:string;state:string;talk_token:string}> {
 return apiRequest('/voip/calls',{method:'POST',body:JSON.stringify({user,security,sdp}),signal});
}
export function attachVoIPTalk(id:string,talk_token:string,sdp:string,signal?:AbortSignal):Promise<{type:'answer';sdp:string}> {
 return apiRequest(`/voip/calls/${encodeURIComponent(id)}/talk`,{method:'POST',body:JSON.stringify({talk_token,sdp}),signal});
}
export function claimVoIPTalk(id:string):Promise<{talk_token:string}> {
 return apiRequest(`/voip/calls/${encodeURIComponent(id)}/talk-token`,{method:'POST'});
}
export function detachVoIPTalk(id:string,talk_token:string):Promise<{status:string}> {
 return apiRequest(`/voip/calls/${encodeURIComponent(id)}/talk/detach`,{method:'POST',body:JSON.stringify({talk_token})});
}
export function sendVoIPDTMF(id:string,digit:string):Promise<{status:string}> {
 return apiRequest(`/voip/calls/${encodeURIComponent(id)}/dtmf`,{method:'POST',body:JSON.stringify({digit})});
}
export function keepVoIPTalk(id:string,talk_token:string):Promise<{status:string}> {
 return apiRequest(`/voip/calls/${encodeURIComponent(id)}/keepalive`,{method:'POST',body:JSON.stringify({talk_token})});
}
