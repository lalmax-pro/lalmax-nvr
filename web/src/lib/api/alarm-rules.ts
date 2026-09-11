import { apiRequest } from './client';

export interface AlarmRule {
  id: number;
  name: string;
  enabled: boolean;
  camera_id: string;
  source: string;
  event_type: string;
  severity: string;
  action: 'record' | 'webhook' | 'goto_preset';
  action_target: string;
  created_at: string;
}

export async function listAlarmRules(): Promise<AlarmRule[]> {
  const res = await apiRequest<{ rules: AlarmRule[] }>('/events/rules');
  return res.rules || [];
}

export async function createAlarmRule(rule: Partial<AlarmRule>): Promise<AlarmRule> {
  return apiRequest<AlarmRule>('/events/rules', {
    method: 'POST',
    body: JSON.stringify(rule),
  });
}

export async function deleteAlarmRule(id: number): Promise<void> {
  await apiRequest(`/events/rules/${id}`, { method: 'DELETE' });
}
