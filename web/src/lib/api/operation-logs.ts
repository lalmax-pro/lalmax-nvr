import { apiRequest } from './client';

export interface OperationLog {
  id: number;
  user_id?: number;
  username?: string;
  actor_type: 'user' | 'system';
  action: string;
  resource: string;
  resource_id?: string;
  status: 'success' | 'failure';
  message?: string;
  metadata?: string;
  ip_address?: string;
  user_agent?: string;
  created_at: string;
}

export interface OperationLogsResponse {
  logs: OperationLog[];
  total: number;
  limit: number;
  offset: number;
}

export interface OperationLogsParams {
  username?: string;
  action?: string;
  resource?: string;
  status?: string;
  limit?: number;
  offset?: number;
}

export async function listOperationLogs(params: OperationLogsParams = {}): Promise<OperationLogsResponse> {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') query.set(key, String(value));
  }
  const suffix = query.toString() ? `?${query.toString()}` : '';
  return apiRequest<OperationLogsResponse>(`/operation-logs${suffix}`);
}
