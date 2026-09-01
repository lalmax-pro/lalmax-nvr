/**
 * Camera API — CRUD, ONVIF discovery, PTZ, protocols, per-camera merge config
 */
import { apiRequest, getAuthHeader, getAuthToken, API_BASE } from './client';

// --- Types ---

export interface Camera {
  id: string;
  name: string;
  protocol: string;
  encoding?: string;
  rtsp_transport?: string;
  url: string;
  username?: string;
  has_password?: boolean;
  enabled: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  status?: string;
  error_type?: string | null;
  error_detail?: string | null;
  last_seen?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  profile_name?: string;
  stream_encoding?: string;
  audio_enabled?: boolean;
  source_type?: string;
  recording_paused?: boolean;
  recording_mode?: RecordingMode;
  archived?: boolean;
  created_at?: string; // RFC3339
  activation_state?: string;
  stable_id?: string;
  sub_stream_url?: string;
  sub_profile_token?: string;
  subnet_hints?: string[];
  adaptive?: { timelapse_interval?: string };
}

export type RecordingMode = 'continuous' | 'scheduled' | 'off' | 'event' | 'adaptive';

export interface RecordingScheduleRange {
  day_of_week: number; // 0=Sunday .. 6=Saturday
  start_time: string;  // "HH:MM"
  end_time: string;    // "HH:MM"
}

export interface CreateCameraRequest {
  name: string;
  protocol: string;
  encoding?: string;
  rtsp_transport?: string;
  url?: string;
  username?: string;
  password?: string;
  enabled?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  onvif_endpoint?: string;
  profile_token?: string;
  profile_name?: string;
  stream_encoding?: string;
  audio_enabled?: boolean;
  recording_mode?: RecordingMode;
  sub_stream_url?: string;
  sub_profile_token?: string;
  subnet_hints?: string[];
  adaptive?: { timelapse_interval?: string };
}

export interface UpdateCameraRequest {
  name?: string;
  url?: string;
  protocol?: string;
  encoding?: string;
  rtsp_transport?: string;
  username?: string;
  password?: string;
  enabled?: boolean;
  description?: string;
  location?: string;
  brand?: string;
  model?: string;
  serial_number?: string;
  retention_days?: number;
  onvif_endpoint?: string;
  profile_token?: string;
  profile_name?: string;
  stream_encoding?: string;
  audio_enabled?: boolean;
  recording_mode?: RecordingMode;
  sub_stream_url?: string;
  sub_profile_token?: string;
  subnet_hints?: string[];
  adaptive?: { timelapse_interval?: string };
}

export interface DiscoveredDevice {
  uuid: string;
  name: string;
  xaddrs: string[];
  scopes: string[];
  hardware: string;
  endpoint: string;
}

export interface DiscoveryError {
  category: 'NETWORK' | 'TIMEOUT' | 'NO_DEVICES' | 'PARSE_ERROR';
  message: string;
}

export interface DiscoveryResult {
  devices: DiscoveredDevice[];
  error: DiscoveryError | null;
}

export interface DeviceInfo {
  manufacturer: string;
  model: string;
  firmware: string;
  serial_number: string;
  hardware_id: string;
}

export interface DeviceProfile {
  token: string;
  name: string;
  encoding: string;
  width: number;
  height: number;
}

export interface ONVIFDeviceDetail {
  device_info: DeviceInfo;
  profiles: DeviceProfile[];
}

export interface PTZMoveRequest {
  mode: 'continuous' | 'absolute' | 'relative';
  pan: number;
  tilt: number;
  zoom: number;
}

export type PTZDirection = 'up' | 'down' | 'left' | 'right' | 'zoom_in' | 'zoom_out';

export function buildPTZContinuousMove(direction: PTZDirection, speed = 0.5): PTZMoveRequest {
  const s = Math.max(0.1, Math.min(1, speed));
  switch (direction) {
    case 'up':
      return { mode: 'continuous', pan: 0, tilt: s, zoom: 0 };
    case 'down':
      return { mode: 'continuous', pan: 0, tilt: -s, zoom: 0 };
    case 'left':
      return { mode: 'continuous', pan: -s, tilt: 0, zoom: 0 };
    case 'right':
      return { mode: 'continuous', pan: s, tilt: 0, zoom: 0 };
    case 'zoom_in':
      return { mode: 'continuous', pan: 0, tilt: 0, zoom: s };
    case 'zoom_out':
      return { mode: 'continuous', pan: 0, tilt: 0, zoom: -s };
  }
}

export interface PTZStatus {
  pan: number;
  tilt: number;
  zoom: number;
  moving: boolean;
}

export interface ProtocolCapabilities {
  hls: boolean;
  ptz: boolean;
  snapshot: boolean;
  discovery: boolean;
  auth: boolean;
}

export interface ProtocolInfo {
  id: string;
  label: string;
  encodings: string[];
  builtIn: boolean;
  addable?: boolean; // Whether this protocol can be manually added in the form
  capabilities: ProtocolCapabilities;
}

// Hardcoded fallback if API is unreachable
export const DEFAULT_PROTOCOLS: ProtocolInfo[] = [
  {
    id: 'rtsp',
    label: 'RTSP',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    addable: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: true },
  },
  {
    id: 'http',
    label: 'HTTP',
    encodings: ['jpeg'],
    builtIn: true,
    addable: true,
    capabilities: { hls: false, ptz: false, snapshot: true, discovery: false, auth: true },
  },
  {
    id: 'onvif',
    label: 'ONVIF',
    encodings: ['h264', 'h265', 'mjpeg'],
    builtIn: true,
    addable: true,
    capabilities: { hls: true, ptz: true, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'gb28181',
    label: 'GB28181',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: false, // Auto-registered via SIP, not manually added
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'xiaomi',
    label: 'Xiaomi',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: true, auth: true },
  },
  {
    id: 'rtmp-pull',
    label: 'RTMP Pull',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'http-flv-pull',
    label: 'HTTP-FLV Pull',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: true,
    capabilities: { hls: true, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'rtmp',
    label: 'RTMP Push',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: false, // Auto-registered when stream is pushed
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
  {
    id: 'srt',
    label: 'SRT',
    encodings: ['h264', 'h265'],
    builtIn: true,
    addable: false, // Auto-registered when stream is pushed
    capabilities: { hls: false, ptz: false, snapshot: false, discovery: false, auth: false },
  },
];

// Protocols that should NOT appear in the "Add Camera" form
// (they are auto-registered or use special mechanisms)
export const EXCLUDED_ADD_PROTOCOLS = ['gb28181'];

// --- Camera CRUD ---

export async function listCameras(signal?: AbortSignal, includeArchived = false): Promise<Camera[]> {
  const endpoint = includeArchived ? '/cameras?include_archived=true' : '/cameras';
  return apiRequest<Camera[]>(endpoint, { signal });
}

export async function createCamera(
  data: CreateCameraRequest,
  signal?: AbortSignal
): Promise<Camera> {
  return apiRequest<Camera>('/cameras', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

export async function getCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, { signal });
}

export async function updateCamera(
  id: string,
  data: UpdateCameraRequest,
  signal?: AbortSignal
): Promise<Camera> {
  return apiRequest<Camera>(`/cameras/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
    signal,
  });
}

export async function deleteCamera(id: string, signal?: AbortSignal): Promise<void> {
  return apiRequest<void>(`/cameras/${id}`, {
    method: 'DELETE',
    signal,
  });
}

export async function permanentlyDeleteCamera(id: string, signal?: AbortSignal): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/permanent`, {
    method: 'DELETE',
    signal,
  });
}

export interface CameraRecordingStats {
  recording_count: number;
  total_size: number;
}

export async function getCameraRecordingStats(id: string, signal?: AbortSignal): Promise<CameraRecordingStats> {
  return apiRequest<CameraRecordingStats>(`/cameras/${id}/stats`, { signal });
}

// getRecordingDays returns days (YYYY-MM-DD) in the given month (YYYY-MM) that have recordings.
export async function getRecordingDays(id: string, month: string, signal?: AbortSignal): Promise<string[]> {
  const res = await apiRequest<{ days: string[] }>(
    `/cameras/${id}/recordings/days?month=${encodeURIComponent(month)}`,
    { signal },
  );
  return res.days ?? [];
}

export async function enableCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return updateCamera(id, { enabled: true }, signal);
}

export async function disableCamera(id: string, signal?: AbortSignal): Promise<Camera> {
  return updateCamera(id, { enabled: false }, signal);
}

export async function startCamera(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/start`, {
    method: 'POST',
    signal,
  });
}

export async function stopCamera(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/stop`, {
    method: 'POST',
    signal,
  });
}

export async function activateCamera(
  id: string,
  username: string,
  password: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/activate`, {
    method: 'POST',
    body: JSON.stringify({ username, password }),
    signal,
  });
}

export async function rediscoverCamera(
  id: string,
  signal?: AbortSignal
): Promise<{ found: boolean }> {
  return apiRequest<{ found: boolean }>(`/cameras/${id}/rediscover`, {
    method: 'POST',
    signal,
  });
}

export async function pauseRecording(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/pause-recording`, {
    method: 'POST',
    signal,
  });
}

export async function resumeRecording(
  id: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${id}/resume-recording`, {
    method: 'POST',
    signal,
  });
}

export function getDashboardCameras(signal?: AbortSignal): Promise<Camera[]> {
  return apiRequest('/cameras', { signal });
}

// --- Test Connection ---

export interface TestConnectionRequest {
  protocol: string;
  url: string;
  username?: string;
  password?: string;
  encoding?: string;
  onvif_endpoint?: string;
}

export interface TestConnectionResult {
  success: boolean;
  message: string;
  latency_ms: number;
}

export async function testConnection(
  data: TestConnectionRequest,
  signal?: AbortSignal
): Promise<TestConnectionResult> {
  return apiRequest<TestConnectionResult>('/cameras/test-connection', {
    method: 'POST',
    body: JSON.stringify(data),
    signal,
  });
}

// --- Per-camera merge config ---

export interface MergeConfig {
  enabled?: boolean;
  check_interval?: string;
  window_size?: string;
  batch_limit?: number;
  min_segment_age?: string;
  min_segments_to_merge?: number;
}

export async function getMergeConfig(
  cameraId: string,
  signal?: AbortSignal
): Promise<MergeConfig | null> {
  try {
    return await apiRequest<MergeConfig>(`/cameras/${cameraId}/merge-config`, { signal });
  } catch {
    return null;
  }
}

export async function updateMergeConfig(
  cameraId: string,
  config: MergeConfig,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

export async function getRecordingSchedule(
  cameraId: string,
  signal?: AbortSignal
): Promise<RecordingScheduleRange[]> {
  const res = await apiRequest<{ ranges: RecordingScheduleRange[] }>(
    `/cameras/${cameraId}/recording-schedule`,
    { signal }
  );
  return res.ranges ?? [];
}

export async function setRecordingSchedule(
  cameraId: string,
  ranges: RecordingScheduleRange[],
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/recording-schedule`, {
    method: 'PUT',
    body: JSON.stringify({ ranges }),
    signal,
  });
}

export async function deleteCameraMergeConfig(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/merge-config`, {
    method: 'DELETE',
    signal,
  });
}

// --- PTZ ---

export async function ptzMove(
  cameraId: string,
  request: PTZMoveRequest,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${encodeURIComponent(cameraId)}/ptz/move`, {
    method: 'POST',
    body: JSON.stringify(request),
    signal,
  });
}

export async function ptzStop(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${encodeURIComponent(cameraId)}/ptz/stop`, {
    method: 'POST',
    signal,
  });
}

export async function getPTZStatus(
  cameraId: string,
  signal?: AbortSignal
): Promise<PTZStatus> {
  return apiRequest<PTZStatus>(`/cameras/${encodeURIComponent(cameraId)}/ptz/status`, { signal });
}

// --- ONVIF Discovery ---

export async function discoverONVIFDevices(
  timeout: number = 5,
  signal?: AbortSignal
): Promise<DiscoveryResult> {
  const result = await apiRequest<DiscoveryResult>('/onvif/discover', {
    method: 'POST',
    body: JSON.stringify({ timeout }),
    signal,
  });
  return {
    devices: result.devices || [],
    error: result.error || null,
  };
}

export async function getONVIFDeviceDetail(
  ip: string,
  signal?: AbortSignal
): Promise<ONVIFDeviceDetail> {
  return apiRequest<ONVIFDeviceDetail>(`/onvif/discover/${ip}`, { signal });
}

export async function probeONVIFDevice(
  host: string,
  port: number = 80,
  signal?: AbortSignal
): Promise<DiscoveredDevice | null> {
  const result = await apiRequest<{ device: DiscoveredDevice | null }>('/onvif/probe', {
    method: 'POST',
    body: JSON.stringify({ host, port }),
    signal,
  });
  return result.device;
}

// --- ONVIF Profiles ---

export interface ONVIFProfilesResponse {
  profiles: DeviceProfile[];
  capabilities?: DeviceCapabilitiesInfo;
}

/**
 * Get profiles from an ONVIF camera
 */
export async function getONVIFProfiles(
  cameraId: string,
  signal?: AbortSignal
): Promise<ONVIFProfilesResponse> {
  return apiRequest<ONVIFProfilesResponse>(`/cameras/${cameraId}/onvif/profiles`, { signal });
}

// --- Protocols ---

export async function listProtocols(signal?: AbortSignal): Promise<ProtocolInfo[]> {
  const response = await apiRequest<{ protocols: ProtocolInfo[] }>('/protocols', { signal });
  return response.protocols;
}

// Normalize legacy combined protocol names (rtsp_h264, etc.) to base protocol ID
export function normalizeProtocol(protocol: string): string {
  if (protocol === 'rtsp_h264' || protocol === 'rtsp_h265' || protocol === 'rtsp_mjpeg') return 'rtsp';
  if (protocol === 'http_jpeg') return 'http';
  return protocol;
}

// Build a lookup map from protocol list
export function buildProtocolsMap(protocols: ProtocolInfo[]): Map<string, ProtocolInfo> {
  const map = new Map<string, ProtocolInfo>();
  for (const p of protocols) {
    map.set(p.id, p);
  }
  return map;
}

// Get capabilities for a protocol, handling legacy protocol names
export function getProtocolCapabilities(
  protocol: string,
  protocolsMap: Map<string, ProtocolInfo>,
): ProtocolCapabilities {
  const baseId = normalizeProtocol(protocol);
  const info = protocolsMap.get(baseId);
  if (info) return info.capabilities;
  return { hls: false, ptz: false, snapshot: false, discovery: false, auth: false };
}

// --- Xiaomi Vendor Check ---

export interface VendorCheckResult {
  vendor: string;
  compatible: boolean;
  message?: string;
}

export async function checkVendor(did: string): Promise<VendorCheckResult> {
  return apiRequest<VendorCheckResult>(`/xiaomi/check-vendor?did=${encodeURIComponent(did)}`);
}

// --- Imaging ---

export interface ImagingSettings {
  brightness?: number;
  contrast?: number;
  saturation?: number;
  sharpness?: number;
  exposure?: {
    mode: string;
    exposure_time?: number;
    gain?: number;
  };
  white_balance?: {
    mode: string;
    color_temperature?: number;
  };
}

export interface ImagingOptionRange {
  min: number;
  max: number;
}

export interface ImagingOptions {
  brightness?: ImagingOptionRange;
  contrast?: ImagingOptionRange;
  saturation?: ImagingOptionRange;
  sharpness?: ImagingOptionRange;
  exposure_time?: ImagingOptionRange;
  gain?: ImagingOptionRange;
  color_temperature?: ImagingOptionRange;
}

export async function getImagingSettings(
  cameraId: string,
  signal?: AbortSignal
): Promise<ImagingSettings> {
  return apiRequest<ImagingSettings>(`/cameras/${cameraId}/imaging/settings`, { signal });
}

export async function setImagingSettings(
  cameraId: string,
  settings: Partial<ImagingSettings>,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/imaging/settings`, {
    method: 'PUT',
    body: JSON.stringify(settings),
    signal,
  });
}

export async function getImagingOptions(
  cameraId: string,
  signal?: AbortSignal
): Promise<ImagingOptions> {
  return apiRequest<ImagingOptions>(`/cameras/${cameraId}/imaging/options`, { signal });
}

// --- PTZ Presets ---

export interface PTZPreset {
  token: string;
  name: string;
}

export async function getPTZPresets(
  cameraId: string,
  signal?: AbortSignal
): Promise<PTZPreset[]> {
  const response = await apiRequest<{ presets: PTZPreset[] }>(`/cameras/${cameraId}/ptz/presets`, { signal });
  return response.presets ?? [];
}

export async function createPTZPreset(
  cameraId: string,
  name: string,
  signal?: AbortSignal
): Promise<PTZPreset> {
  const response = await apiRequest<{ token: string }>(`/cameras/${cameraId}/ptz/presets`, {
    method: 'POST',
    body: JSON.stringify({ name }),
    signal,
  });
  return { token: response.token, name };
}

export async function goToPTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}/goto`, {
    method: 'POST',
    signal,
  });
}

export async function deletePTZPreset(
  cameraId: string,
  token: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/ptz/presets/${encodeURIComponent(token)}`, {
    method: 'DELETE',
    signal,
  });
}

// --- Snapshot URI ---

export interface SnapshotUriResponse {
  uri: string;
}

export async function getSnapshotUri(
  cameraId: string,
  signal?: AbortSignal
): Promise<SnapshotUriResponse> {
  return apiRequest<SnapshotUriResponse>(`/cameras/${cameraId}/snapshot/uri`, { signal });
}

// --- Device Capabilities ---

export interface PTZCapabilitiesDetailed {
  supported: boolean;
  pan_tilt: boolean;
  zoom: boolean;
  presets: boolean;
  home: boolean;
}

export interface DeviceCapabilitiesInfo {
  ptz: boolean;
  ptz_detail: PTZCapabilitiesDetailed;
  imaging: boolean;
  events: boolean;
  snapshot: boolean;
  streaming: boolean;
  device_info?: {
    manufacturer?: string;
    model?: string;
    firmware?: string;
    serial_number?: string;
    hardware_id?: string;
  };
}

export async function getDeviceCapabilities(
  cameraId: string,
  signal?: AbortSignal
): Promise<DeviceCapabilitiesInfo> {
  return apiRequest<DeviceCapabilitiesInfo>(`/cameras/${cameraId}/onvif/capabilities`, { signal });
}

// --- Device Management ---

export interface NetworkIPv4 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  netmask?: string;
  gateway?: string;
}

export interface NetworkIPv6 {
  enabled: boolean;
  dhcp: boolean;
  address?: string;
  prefix?: number;
  gateway?: string;
}

export interface NetworkNTP {
  manual?: string[];
  dhcp: boolean;
}

export interface NetworkInterface {
  name: string;
  enabled: boolean;
  ipv4: NetworkIPv4;
  ipv6?: NetworkIPv6;
  dns?: string[];
  ntp?: NetworkNTP;
}

export interface ONVIFDeviceUser {
  username: string;
  password?: string;
  level: string; // "Administrator", "Operator", "User", "Anonymous"
}

export async function rebootDevice(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/reboot`, {
    method: 'POST',
    signal,
  });
}

export async function getNetworkInterfaces(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ interfaces: NetworkInterface[] }> {
  return apiRequest<{ interfaces: NetworkInterface[] }>(`/cameras/${cameraId}/onvif/network`, { signal });
}

export async function setNetworkInterfaces(
  cameraId: string,
  interfaces: NetworkInterface[],
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/network`, {
    method: 'PUT',
    body: JSON.stringify({ interfaces }),
    signal,
  });
}

export async function getDeviceUsers(
  cameraId: string,
  signal?: AbortSignal
): Promise<{ users: ONVIFDeviceUser[] }> {
  return apiRequest<{ users: ONVIFDeviceUser[] }>(`/cameras/${cameraId}/onvif/users`, { signal });
}

export async function createDeviceUsers(
  cameraId: string,
  users: ONVIFDeviceUser[],
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'POST',
    body: JSON.stringify({ users }),
    signal,
  });
}

export async function deleteDeviceUsers(
  cameraId: string,
  usernames: string[],
  signal?: AbortSignal
): Promise<{ status: string }> {
  return apiRequest<{ status: string }>(`/cameras/${cameraId}/onvif/users`, {
    method: 'DELETE',
    body: JSON.stringify({ usernames }),
    signal,
  });
}

// --- Per-camera timelapse config ---

export interface TimelapseConfig {
  enabled: boolean;
  interval: string;
  output_fps: number;
  video_codec: string;
  delete_original: boolean;
}

export async function getTimelapseConfig(
  cameraId: string,
  signal?: AbortSignal
): Promise<TimelapseConfig> {
  return apiRequest<TimelapseConfig>(`/cameras/${cameraId}/timelapse`, { signal });
}

export async function updateTimelapseConfig(
  cameraId: string,
  config: TimelapseConfig,
  signal?: AbortSignal
): Promise<any> {
  return apiRequest(`/cameras/${cameraId}/timelapse`, {
    method: 'PUT',
    body: JSON.stringify(config),
    signal,
  });
}

// --- Snapshot ---

export interface SnapshotInfo {
  camera_id: string;
  path: string;
  size: number;
  mod_time: string;
  url: string;
}

/**
 * Get snapshot URL for a camera (includes auth token for <img> tags)
 */
export function getSnapshotUrl(cameraId: string): string {
  const token = getAuthToken();
  const encodedId = encodeURIComponent(cameraId);
  const base = `${API_BASE}/snapshots/${encodedId}`;
  return token ? `${base}?token=${token}` : base;
}

/**
 * Get latest snapshot info
 */
export async function getSnapshotInfo(
  cameraId: string,
  signal?: AbortSignal
): Promise<SnapshotInfo> {
  return apiRequest<SnapshotInfo>(`/snapshots/${cameraId}/latest`, { signal });
}

/**
 * Take an immediate snapshot
 */
export async function takeSnapshot(
  cameraId: string,
  signal?: AbortSignal
): Promise<Blob> {
  const url = `${API_BASE}/snapshots/${cameraId}/take`;
  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, {
    method: 'POST',
    headers,
    signal,
  });

  if (!response.ok) {
    throw new Error(`Failed to take snapshot: ${response.status}`);
  }

  return response.blob();
}
