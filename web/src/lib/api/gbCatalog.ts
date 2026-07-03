/**
 * GB28181 国标目录 — 行政区划 / 业务分组 与通道挂接
 */
import { apiRequest } from './client';

// ==================== Types ====================

export interface GBChannelBrief {
  device_id: string;
  channel_id: string;
  name: string;
  status: string;
  civil_code: string;
  parent_id: string;
  business_group_id: string;
}

/** 区划树节点 (type=region) 或挂接的通道叶子 (type=channel) */
export interface RegionTreeNode {
  type: 'region' | 'channel';
  id?: number;
  device_id: string;
  name: string;
  parent_id?: number;
  is_leaf: boolean;
  channel_id?: string;
  status?: string;
  channel?: GBChannelBrief;
}

/** 分组树节点 (type=group) 或挂接的通道叶子 (type=channel) */
export interface GroupTreeNode {
  type: 'group' | 'channel';
  id?: number;
  device_id: string;
  name: string;
  parent_id?: number;
  business_group?: string;
  is_leaf: boolean;
  channel_id?: string;
  status?: string;
  channel?: GBChannelBrief;
}

export interface ChannelKey {
  device_id: string;
  channel_id: string;
}

// ==================== 行政区划 ====================

export async function getRegionTree(parent?: number, hasChannel = false): Promise<RegionTreeNode[]> {
  const qs = new URLSearchParams();
  if (parent != null) qs.set('parent', String(parent));
  if (hasChannel) qs.set('has_channel', 'true');
  const res = await apiRequest<{ nodes: RegionTreeNode[] }>(`/gb28181/regions/tree?${qs.toString()}`);
  return res.nodes || [];
}

export async function createRegion(req: {
  device_id: string;
  name: string;
  parent_device_id?: string;
}): Promise<{ status: string }> {
  return apiRequest('/gb28181/regions', { method: 'POST', body: JSON.stringify(req) });
}

export async function updateRegion(
  id: number,
  req: { device_id?: string; name?: string }
): Promise<{ status: string }> {
  return apiRequest(`/gb28181/regions/${id}`, { method: 'PUT', body: JSON.stringify(req) });
}

export async function deleteRegion(id: number): Promise<{ status: string }> {
  return apiRequest(`/gb28181/regions/${id}`, { method: 'DELETE' });
}

export async function syncRegions(): Promise<{ created: number; status: string }> {
  return apiRequest('/gb28181/regions/sync', { method: 'POST' });
}

export interface CivilCode {
  code: string;
  name: string;
  parent_code: string;
}

/** 从内置 GB/T 2260 标准库取某父级下的候选区划 (parent 为空取省级) */
export async function getCivilCodeCandidates(parent?: string): Promise<CivilCode[]> {
  const qs = new URLSearchParams();
  if (parent) qs.set('parent', parent);
  const res = await apiRequest<{ candidates: CivilCode[] }>(`/gb28181/regions/candidates?${qs.toString()}`);
  return res.candidates || [];
}

/** 查询某编码的层级描述，如 广东省/广州市/天河区 */
export async function getCivilCodeDescription(
  civilCode: string
): Promise<{ civil_code: string; name: string; description: string; found: boolean }> {
  return apiRequest(`/gb28181/regions/description?civil_code=${encodeURIComponent(civilCode)}`);
}

/** 按标准编码新增区划，自动补全全部上级节点 */
export async function addRegionByCivilCode(civilCode: string): Promise<{ created: number; status: string }> {
  return apiRequest('/gb28181/regions/by-civil-code', {
    method: 'POST',
    body: JSON.stringify({ civil_code: civilCode }),
  });
}

// ==================== 业务分组 / 虚拟组织 ====================

export async function getGroupTree(parent?: number, hasChannel = false): Promise<GroupTreeNode[]> {
  const qs = new URLSearchParams();
  if (parent != null) qs.set('parent', String(parent));
  if (hasChannel) qs.set('has_channel', 'true');
  const res = await apiRequest<{ nodes: GroupTreeNode[] }>(`/gb28181/groups/tree?${qs.toString()}`);
  return res.nodes || [];
}

export async function createGroup(req: {
  device_id: string;
  name: string;
  parent_device_id?: string;
  business_group?: string;
  civil_code?: string;
}): Promise<{ status: string }> {
  return apiRequest('/gb28181/groups', { method: 'POST', body: JSON.stringify(req) });
}

export async function updateGroup(id: number, req: { name?: string }): Promise<{ status: string }> {
  return apiRequest(`/gb28181/groups/${id}`, { method: 'PUT', body: JSON.stringify(req) });
}

export async function deleteGroup(id: number): Promise<{ status: string }> {
  return apiRequest(`/gb28181/groups/${id}`, { method: 'DELETE' });
}

// ==================== 通道列表 & 挂接 ====================

export async function listCatalogChannels(opts: {
  q?: string;
  unassignedRegion?: boolean;
  unassignedGroup?: boolean;
} = {}): Promise<GBChannelBrief[]> {
  const qs = new URLSearchParams();
  if (opts.q) qs.set('q', opts.q);
  if (opts.unassignedRegion) qs.set('unassigned_region', 'true');
  if (opts.unassignedGroup) qs.set('unassigned_group', 'true');
  const res = await apiRequest<{ channels: GBChannelBrief[] }>(`/gb28181/catalog/channels?${qs.toString()}`);
  return res.channels || [];
}

export async function attachChannelsToRegion(civilCode: string, channels: ChannelKey[]): Promise<{ status: string }> {
  return apiRequest('/gb28181/channels/region', {
    method: 'POST',
    body: JSON.stringify({ civil_code: civilCode, channels }),
  });
}

export async function detachChannelsFromRegion(channels: ChannelKey[]): Promise<{ status: string }> {
  return apiRequest('/gb28181/channels/region', {
    method: 'DELETE',
    body: JSON.stringify({ channels }),
  });
}

export async function attachChannelsToGroup(
  parentId: string,
  businessGroup: string,
  channels: ChannelKey[]
): Promise<{ status: string }> {
  return apiRequest('/gb28181/channels/group', {
    method: 'POST',
    body: JSON.stringify({ parent_id: parentId, business_group: businessGroup, channels }),
  });
}

export async function detachChannelsFromGroup(channels: ChannelKey[]): Promise<{ status: string }> {
  return apiRequest('/gb28181/channels/group', {
    method: 'DELETE',
    body: JSON.stringify({ channels }),
  });
}
