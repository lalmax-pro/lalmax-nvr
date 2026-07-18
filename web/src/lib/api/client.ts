/**
 * Base HTTP client — auth, generic fetch wrappers
 */

// Auth credentials storage (sessionStorage — cleared on browser close)
const AUTH_KEY = 'nvr_auth';

export interface AuthCredentials {
  username: string;
  password: string;
}

export interface LoginResponse {
  status: string;
}

export interface ApiError {
  error: string;
  code?: string;
}

// API error with machine-readable code for i18n mapping
export class ApiRequestError extends Error {
  constructor(message: string, public code?: string) {
    super(message);
    this.name = 'ApiRequestError';
  }
}

export interface HealthCheck {
  status: 'ok' | 'warning' | 'error';
  message?: string;
}

export interface HealthResponse {
  status: 'ok' | 'degraded' | 'unhealthy';
  checks: Record<string, HealthCheck>;
  uptime: string;
  start_time?: string; // RFC3339
  setup_required?: boolean;
}

export interface SystemStats {
  cpu: {
    total: number;
    idle: number;
  };
  memory: {
    total: number;
    available: number;
    process_rss: number;
  };
  network: {
    bytes_sent: number;
    bytes_recv: number;
  };
  system: {
    os: string;
    arch: string;
    cpu_cores: number;
  };
  uptime: string;
  timestamp: number;
}

// Store credentials in sessionStorage (cleared on browser close)
export function storeCredentials(username: string, password: string): void {
  const encoded = btoa(`${username}:${password}`);
  sessionStorage.setItem(AUTH_KEY, encoded);
}

// Get credentials from sessionStorage
export function getCredentials(): AuthCredentials | null {
  const encoded = sessionStorage.getItem(AUTH_KEY);
  if (!encoded) return null;

  try {
    const decoded = atob(encoded);
    const [username, password] = decoded.split(':');
    return { username, password };
  } catch (e) { console.warn('Failed to decode credentials:', e);
return null;
}
}

// Clear credentials
export function clearCredentials(): void {
  sessionStorage.removeItem(AUTH_KEY);
}

// Check if user is authenticated
export function isAuthenticated(): boolean {
  return getCredentials() !== null;
}

// Get Basic Auth header value
export function getAuthHeader(): string | null {
  const creds = getCredentials();
  if (!creds) return null;

  const encoded = btoa(`${creds.username}:${creds.password}`);
  return `Basic ${encoded}`;
}

// Get base64 token for query parameter auth (e.g. <img src="?token=...">)
export function getAuthToken(): string | null {
  const creds = getCredentials();
  if (!creds) return null;

  return btoa(`${creds.username}:${creds.password}`);
}

// API base URL (relative path for embedded static files)
export const API_BASE = '/api';

// Generic API request function
export async function apiRequest<T>(
  endpoint: string,
  options: RequestInit = {}
): Promise<T> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {
    'Content-Type': 'application/json',
    ...options.headers,
  };

  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, {
    ...options,
    headers,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Unknown error' }));
    const apiErr = errorData as ApiError;
    throw new ApiRequestError(apiErr.error || `HTTP ${response.status}`, apiErr.code);
  }

  return response.json();
}

// Generic API request for blob responses (e.g. file downloads)
export async function apiRequestBlob(
  endpoint: string,
  options: RequestInit = {}
): Promise<Blob> {
  const url = `${API_BASE}${endpoint}`;

  const headers: HeadersInit = {};
  const authHeader = getAuthHeader();
  if (authHeader) {
    headers['Authorization'] = authHeader;
  }

  const response = await fetch(url, { ...options, headers });
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.blob();
}

// Login endpoint
export async function login(
  username: string,
  password: string,
  signal?: AbortSignal
): Promise<LoginResponse> {
  const authHeader = `Basic ${btoa(`${username}:${password}`)}`;

  const response = await fetch('/api/auth/login', {
    method: 'POST',
    headers: {
      'Authorization': authHeader,
    },
    signal,
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Invalid credentials' }));
    // Check for setup required (no password configured)
    if ((errorData as ApiError).code === 'SETUP_REQUIRED') {
      throw new Error('setup_required');
    }
    throw new Error((errorData as ApiError).error || 'Invalid credentials');
  }

  const data = await response.json();

  // Store credentials on success
  storeCredentials(username, password);

  return data as LoginResponse;
}

// Logout
export function logout(): void {
  clearCredentials();
  window.location.hash = '#/login';
}

// Health check (no auth required)
export async function healthCheck(signal?: AbortSignal): Promise<HealthResponse> {
  const response = await fetch('/api/health', { signal });
  return response.json();
}

// System stats endpoint
export async function getSystemStats(signal?: AbortSignal): Promise<SystemStats> {
  return apiRequest<SystemStats>('/stats/system', { signal });
}

// Network interface
export interface NetworkInterface {
  name: string;
  mtu: number;
  hardware_addr: string;
  addresses: string[];
  is_up: boolean;
  is_loopback: boolean;
  speed: string;
}

// Get local network interfaces
export async function getLocalNetworkInterfaces(signal?: AbortSignal): Promise<{ interfaces: NetworkInterface[] }> {
  return apiRequest<{ interfaces: NetworkInterface[] }>('/network', { signal });
}

// System metrics history sample
export interface SystemMetricSample {
  ts: number;         // Unix seconds
  cpu: number;        // 0–100
  mem: number;        // 0–100
  net_up: number;     // bytes/sec
  net_dn: number;     // bytes/sec
  goroutines: number;
}

// Get system resource history (period: "1h" | "6h" | "24h")
export async function getSystemMetricsHistory(period: string, signal?: AbortSignal): Promise<SystemMetricSample[]> {
  return apiRequest<SystemMetricSample[]>(`/stats/system/history?period=${encodeURIComponent(period)}`, { signal });
}

// Hourly recording activity
export interface HourlyStats {
  hour: string;       // RFC3339, e.g. "2024-01-01T14:00:00Z"
  recordings: number;
  total_size: number;
}

// Get hourly recording stats (hours: 24, 48, 168)
export async function getHourlyStats(hours: number, signal?: AbortSignal): Promise<HourlyStats[]> {
  return apiRequest<HourlyStats[]>(`/stats/hourly?hours=${hours}`, { signal });
}

// Camera uptime / stability stat
export interface CameraUptimeStat {
  camera_id: string;
  camera_name: string;
  connection_losses: number;
  connection_restores: number;
  total_events: number;
}

// Get camera stability stats (days: 7, 14, 30)
export async function getCameraUptimeStats(days: number, signal?: AbortSignal): Promise<CameraUptimeStat[]> {
  return apiRequest<CameraUptimeStat[]>(`/stats/camera-uptime?days=${days}`, { signal });
}

// Setup response
export interface SetupResponse {
  status: string;
  token: string;
  data_dir?: string;
  config_path?: string;
  restart_required?: boolean;
}

// First-time setup endpoint (no auth required)
export async function setupApi(
  username: string,
  password: string,
  language?: string,
  dataDir?: string,
): Promise<SetupResponse> {
  const body: Record<string, string> = { username, password };
  if (language) body.language = language;
  if (dataDir) body.data_dir = dataDir;

  const response = await fetch('/api/setup', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });

  if (!response.ok) {
    const errorData = await response.json().catch(() => ({ error: 'Setup failed' }));
    throw new Error((errorData as ApiError).error || `HTTP ${response.status}`);
  }

  return response.json();
}

export interface SetupDirectoryEntry {
  name: string;
  path: string;
}

export interface SetupDirectoriesResponse {
  path: string;
  parent: string;
  entries: SetupDirectoryEntry[];
}

export async function browseSetupDirectories(path?: string): Promise<SetupDirectoriesResponse> {
  const query = path ? `?path=${encodeURIComponent(path)}` : '';
  return apiRequest<SetupDirectoriesResponse>(`/setup/directories${query}`);
}
