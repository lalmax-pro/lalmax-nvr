/**
 * AI Detection Settings — localStorage-backed persistence
 *
 * Stores per-browser AI detection preferences (enable, confidence, frame skip).
 * These are client-side only — no backend API calls.
 */

// ─── Types ────────────────────────────────────────────────────────────────────

export interface AiDetectionSettings {
  /** Whether AI detection is active in live view */
  enabled: boolean;
  /** Confidence threshold for filtering detections (0.1–0.9, default 0.5) */
  confidenceThreshold: number;
  /** Detect every N frames (1–10, default 3) */
  frameSkip: number;
}

// ─── Constants ────────────────────────────────────────────────────────────────

const STORAGE_KEY = 'nvr_ai_settings';

const DEFAULTS: AiDetectionSettings = {
  enabled: false,
  confidenceThreshold: 0.5,
  frameSkip: 3,
};

// ─── Persistence ──────────────────────────────────────────────────────────────

/**
 * Load AI detection settings from localStorage.
 * Returns defaults if nothing stored or on parse error.
 */
export function getAiSettings(): AiDetectionSettings {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return { ...DEFAULTS };
    const parsed = JSON.parse(raw);
    return {
      enabled: typeof parsed.enabled === 'boolean' ? parsed.enabled : DEFAULTS.enabled,
      confidenceThreshold: clampConfidence(parsed.confidenceThreshold),
      frameSkip: clampFrameSkip(parsed.frameSkip),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

/**
 * Save AI detection settings to localStorage.
 */
export function saveAiSettings(settings: AiDetectionSettings): void {
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      enabled: settings.enabled,
      confidenceThreshold: clampConfidence(settings.confidenceThreshold),
      frameSkip: clampFrameSkip(settings.frameSkip),
    }));
  } catch (e) {
    console.error('Failed to save AI settings:', e);
  }
}

// ─── Validation helpers ───────────────────────────────────────────────────────

function clampConfidence(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.confidenceThreshold;
  return Math.round(Math.min(0.9, Math.max(0.1, value)) * 10) / 10;
}

function clampFrameSkip(value: number): number {
  if (typeof value !== 'number' || isNaN(value)) return DEFAULTS.frameSkip;
  return Math.min(10, Math.max(1, Math.round(value)));
}

// ─── Backend detection ────────────────────────────────────────────────────────

/**
 * Detect the best available AI inference backend.
 * Returns 'WebGPU' if available, otherwise 'WASM SIMD'.
 */
export function detectAiBackend(): string {
  try {
    if (typeof navigator !== 'undefined' && (navigator as any).gpu !== undefined) {
      return 'WebGPU';
    }
  } catch {
    // navigator not available
  }
  return 'WASM SIMD';
}

// ─── Backend API ─────────────────────────────────────────────────────────────

import { apiRequest, getAuthToken } from './client';

/** AI engine status response from GET /api/ai/status. */
export interface AiStatusResponse {
  available: boolean;
  backend: 'webhook' | 'disabled';
  reason: string;
}

/** Detection event from SSE stream. */
export interface AiDetectionEvent {
  camera_id: string;
  pts: number;
  timestamp?: number;
  image_url?: string;
  detections: AiDetection[];
}

/** Single detection result. */
export interface AiDetection {
  label: string;
  confidence: number;
  box: [number, number, number, number]; // [x, y, w, h] normalized
  track_id?: number; // Object tracking ID (from ByteTrack/supervision)
  zone_id?: string;  // Zone identifier for region-based detection
}

/** Get AI engine status. */
export async function getAiStatus(): Promise<AiStatusResponse> {
  return apiRequest<AiStatusResponse>('/ai/status');
}

/** Subscribe to AI detection events via SSE. Returns cleanup function. */
export function subscribeAiEvents(
  onEvent: (event: AiDetectionEvent) => void,
  onError?: (error: Event) => void,
): () => void {
  const token = getAuthToken();
  const authParam = token ? `?token=${encodeURIComponent(token)}` : '';
  const eventSource = new EventSource(`/api/ai/events${authParam}`);

  eventSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data) as AiDetectionEvent;
      onEvent(data);
    } catch (e) {
      console.warn('Failed to parse AI event:', e);
    }
  };

  eventSource.onerror = (error) => {
    if (onError) onError(error);
  };

  return () => {
    eventSource.close();
  };
}

// ─── AI Settings API ──────────────────────────────────────────────────────

/** AI backend configuration from GET /api/settings/ai. */
export interface AiBackendConfig {
  enabled: boolean;
  backend: 'webhook' | 'disabled';
  frame_skip_rate: number;
  confidence_threshold: number;
  inference_timeout_ms: number;
  webhook: {
    enabled: boolean;
  } | null;
  multimodal: MultimodalConfig | null;
}

export interface MultimodalProviderConfig {
  provider: string;
  apiKey: string;
  endpoint: string;
  model: string;
  visionModel: string;
  maxTokens: number;
  temperature: number;
  timeout: number;
}

export interface MultimodalConfig {
  enabled: boolean;
  provider: string;
  providers: Record<string, MultimodalProviderConfig>;
  analysisPrompt: string;
  analysisInterval: string;
  saveResults: boolean;
  maxResults: number;
}

export interface MultimodalStatus {
  active_provider: string;
  providers: Record<string, boolean>;
}

export interface MultimodalAnalysisEvent {
  camera_id: string;
  timestamp: number;
  analysis: string;
  labels: string[];
  confidence: number;
  image_url?: string;
  trigger_detections?: AiDetection[];
  metadata?: Record<string, string>;
}

/** Get AI backend configuration. */
export async function getAiBackendConfig(): Promise<AiBackendConfig> {
  return apiRequest<AiBackendConfig>('/settings/ai');
}

/** Update AI backend configuration. */
export async function updateAiBackendConfig(config: Partial<AiBackendConfig>): Promise<{ status: string }> {
  return apiRequest('/settings/ai', {
    method: 'PUT',
    body: JSON.stringify(config),
  });
}

export interface AiDetectionHistoryResponse {
  detections: AiDetectionEvent[];
  total: number;
}

export interface MultimodalHistoryResponse {
  analyses: MultimodalAnalysisEvent[];
  total: number;
}

/** List persisted AI detection history. */
export async function listAiDetections(params: { camera_id?: string; label?: string; limit?: number; offset?: number } = {}): Promise<AiDetectionHistoryResponse> {
  const query = new URLSearchParams();
  if (params.camera_id) query.set('camera_id', params.camera_id);
  if (params.label) query.set('label', params.label);
  if (params.limit) query.set('limit', String(params.limit));
  if (params.offset) query.set('offset', String(params.offset));
  const suffix = query.toString() ? `?${query}` : '';
  return apiRequest<AiDetectionHistoryResponse>(`/ai/detections${suffix}`);
}

/** List persisted multimodal analysis history. */
export async function listAiAnalyses(params: { camera_id?: string; limit?: number; offset?: number } = {}): Promise<MultimodalHistoryResponse> {
  const query = new URLSearchParams();
  if (params.camera_id) query.set('camera_id', params.camera_id);
  if (params.limit) query.set('limit', String(params.limit));
  if (params.offset) query.set('offset', String(params.offset));
  const suffix = query.toString() ? `?${query}` : '';
  return apiRequest<MultimodalHistoryResponse>(`/ai/analyses${suffix}`);
}

/** Get multimodal analyzer status. */
export async function getMultimodalStatus(): Promise<MultimodalStatus> {
  return apiRequest<MultimodalStatus>('/ai/multimodal/status');
}

/** Subscribe to multimodal analysis events via SSE. Returns cleanup function. */
export function subscribeMultimodalEvents(
  onEvent: (event: MultimodalAnalysisEvent) => void,
  onError?: (error: Event) => void,
): () => void {
  const token = getAuthToken();
  const authParam = token ? `?token=${encodeURIComponent(token)}` : '';
  const eventSource = new EventSource(`/api/ai/multimodal/events${authParam}`);

  eventSource.onmessage = (event) => {
    try {
      const data = JSON.parse(event.data) as MultimodalAnalysisEvent;
      onEvent(data);
    } catch (e) {
      console.warn('Failed to parse multimodal event:', e);
    }
  };

  eventSource.onerror = (error) => {
    if (onError) onError(error);
  };

  return () => {
    eventSource.close();
  };
}
