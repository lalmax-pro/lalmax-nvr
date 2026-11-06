/**
 * Xiaomi API — cloud auth, device listing, sync
 */
import { apiRequest } from './client';

// --- Types ---

export interface XiaomiDevice {
  did: string;
  name: string;
  model: string;
  localip: string;
  isOnline: boolean;
}

export interface XiaomiDevicesResponse {
  devices: XiaomiDevice[];
  authenticated: boolean;
  message?: string;
}

export interface XiaomiStatusResponse {
  authenticated: boolean;
  region: string;
}

export interface XiaomiAccountInfo {
  id: string;
  region: string;
  authenticated: boolean;
}

export async function xiaomiAccounts(signal?: AbortSignal): Promise<{ accounts: XiaomiAccountInfo[] }> {
  return apiRequest('/xiaomi/accounts', { signal });
}

export interface XiaomiAuthResponse {
  user_id?: string;
  status?: string;
  captcha?: string;       // base64-encoded image
  verify_phone?: string;  // masked phone number
  verify_email?: string;  // masked email
  session_id?: string;    // for captcha/verify continuation
}

// --- Functions ---

export async function xiaomiAuth(
  username: string,
  password: string,
  region?: string,
  signal?: AbortSignal,
  account = 'default'
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/auth', {
    method: 'POST',
    body: JSON.stringify({ username, password, region: region || 'cn', account }),
    signal,
  });
}

export async function xiaomiDevices(signal?: AbortSignal, account = 'default'): Promise<XiaomiDevicesResponse> {
  return apiRequest<XiaomiDevicesResponse>(`/xiaomi/devices?account=${encodeURIComponent(account)}`, { signal });
}

export async function xiaomiStatus(signal?: AbortSignal): Promise<XiaomiStatusResponse> {
  return apiRequest('/xiaomi/status', { signal });
}

export async function xiaomiLogout(signal?: AbortSignal, account = 'default'): Promise<{ status: string }> {
  return apiRequest(`/xiaomi/logout?account=${encodeURIComponent(account)}`, { method: 'POST', signal });
}

export async function xiaomiCaptcha(
  sessionId: string,
  captchaCode: string,
  signal?: AbortSignal,
  account = 'default'
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/captcha', {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId, captcha_code: captchaCode, account }),
    signal,
  });
}

export async function xiaomiVerify(
  sessionId: string,
  ticket: string,
  signal?: AbortSignal,
  account = 'default'
): Promise<XiaomiAuthResponse> {
  return apiRequest('/xiaomi/verify', {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId, ticket, account }),
    signal,
  });
}

export async function xiaomiSync(signal?: AbortSignal, account = 'default'): Promise<{ synced: number; total: number }> {
  return apiRequest(`/xiaomi/sync?account=${encodeURIComponent(account)}`, { method: 'POST', signal });
}
