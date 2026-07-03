<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getRegionTree,
    createRegion,
    updateRegion,
    deleteRegion,
    syncRegions,
    getCivilCodeDescription,
    addRegionByCivilCode,
    getGroupTree,
    createGroup,
    updateGroup,
    deleteGroup,
    listCatalogChannels,
    attachChannelsToRegion,
    detachChannelsFromRegion,
    attachChannelsToGroup,
    detachChannelsFromGroup,
  } from '$lib/api';
  import type { RegionTreeNode, GroupTreeNode, GBChannelBrief, ChannelKey } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import {
    MapPin,
    Network,
    Plus,
    Edit2,
    Trash2,
    ChevronRight,
    ChevronDown,
    RefreshCw,
    X,
    Search,
    Link2,
    Unlink,
    Folder,
  } from 'lucide-svelte';

  type ViewMode = 'region' | 'group';

  // 规范化的树节点 (区划/分组统一)
  interface CatNode {
    type: 'region' | 'group';
    id: number;
    deviceId: string;
    name: string;
    businessGroup?: string;
  }

  let viewMode = $state<ViewMode>('region');
  let loading = $state(true);
  let error = $state('');

  // 树: 顶层节点 + 每个节点的已加载子节点 (懒加载)
  let topNodes = $state<CatNode[]>([]);
  let childrenMap = $state<Record<number, CatNode[]>>({});
  let expandedNodes = $state<Set<number>>(new Set());

  // 选中节点 + 其下通道
  let selectedNode = $state<CatNode | null>(null);
  let nodeChannels = $state<GBChannelBrief[]>([]);
  let selectedChannelKeys = $state<Set<string>>(new Set());

  // 挂接对话框
  let showAttachDialog = $state(false);
  let attachSearch = $state('');
  let attachOnlyUnassigned = $state(true);
  let attachCandidates = $state<GBChannelBrief[]>([]);
  let attachSelected = $state<Set<string>>(new Set());

  // 节点表单
  let showNodeForm = $state(false);
  let nodeFormMode = $state<'create' | 'edit'>('create');
  let formDeviceId = $state('');
  let formName = $state('');
  let formParentDeviceId = $state('');
  let formBusinessGroup = $state('');
  let editingNode = $state<CatNode | null>(null);
  let civilPath = $state(''); // 标准库层级描述提示

  function chKey(c: { device_id: string; channel_id: string }): string {
    return `${c.device_id}__${c.channel_id}`;
  }

  function mapRegion(n: RegionTreeNode): CatNode {
    return { type: 'region', id: n.id ?? 0, deviceId: n.device_id, name: n.name };
  }
  function mapGroup(n: GroupTreeNode): CatNode {
    return { type: 'group', id: n.id ?? 0, deviceId: n.device_id, name: n.name, businessGroup: n.business_group };
  }

  async function fetchChildren(parentId: number): Promise<CatNode[]> {
    if (viewMode === 'region') {
      const nodes = await getRegionTree(parentId, false);
      return nodes.filter(n => n.type === 'region').map(mapRegion);
    }
    const nodes = await getGroupTree(parentId, false);
    return nodes.filter(n => n.type === 'group').map(mapGroup);
  }

  // 拉某节点下挂接的通道 (has_channel=true 取通道叶子)
  async function fetchNodeChannels(node: CatNode): Promise<GBChannelBrief[]> {
    if (viewMode === 'region') {
      const nodes = await getRegionTree(node.id, true);
      return nodes.filter(n => n.type === 'channel' && n.channel).map(n => n.channel!);
    }
    const nodes = await getGroupTree(node.id, true);
    return nodes.filter(n => n.type === 'channel' && n.channel).map(n => n.channel!);
  }

  async function loadTop() {
    loading = true;
    error = '';
    selectedNode = null;
    nodeChannels = [];
    childrenMap = {};
    expandedNodes = new Set();
    try {
      topNodes = await fetchChildren(0);
    } catch (e) {
      error = e instanceof Error ? e.message : String(t('common.error'));
    } finally {
      loading = false;
    }
  }

  async function toggleNode(node: CatNode) {
    const next = new Set(expandedNodes);
    if (next.has(node.id)) {
      next.delete(node.id);
      expandedNodes = next;
      return;
    }
    next.add(node.id);
    expandedNodes = next;
    if (!childrenMap[node.id]) {
      try {
        childrenMap = { ...childrenMap, [node.id]: await fetchChildren(node.id) };
      } catch (e) {
        showToast(t('common.error'), 'error');
      }
    }
  }

  async function selectNode(node: CatNode) {
    selectedNode = node;
    selectedChannelKeys = new Set();
    try {
      nodeChannels = await fetchNodeChannels(node);
    } catch (e) {
      nodeChannels = [];
      showToast(t('common.error'), 'error');
    }
  }

  function toggleChannelSelect(c: GBChannelBrief) {
    const next = new Set(selectedChannelKeys);
    const k = chKey(c);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    selectedChannelKeys = next;
  }

  function switchView(mode: ViewMode) {
    if (viewMode === mode) return;
    viewMode = mode;
    loadTop();
  }

  // ==================== 挂接 ====================

  async function openAttachDialog() {
    if (!selectedNode) return;
    attachSearch = '';
    attachOnlyUnassigned = true;
    attachSelected = new Set();
    showAttachDialog = true;
    await loadAttachCandidates();
  }

  async function loadAttachCandidates() {
    try {
      attachCandidates = await listCatalogChannels({
        q: attachSearch.trim() || undefined,
        unassignedRegion: viewMode === 'region' && attachOnlyUnassigned,
        unassignedGroup: viewMode === 'group' && attachOnlyUnassigned,
      });
    } catch (e) {
      attachCandidates = [];
      showToast(t('common.error'), 'error');
    }
  }

  function toggleAttachSelect(c: GBChannelBrief) {
    const next = new Set(attachSelected);
    const k = chKey(c);
    if (next.has(k)) next.delete(k);
    else next.add(k);
    attachSelected = next;
  }

  async function confirmAttach() {
    if (!selectedNode || attachSelected.size === 0) return;
    const keys: ChannelKey[] = attachCandidates
      .filter(c => attachSelected.has(chKey(c)))
      .map(c => ({ device_id: c.device_id, channel_id: c.channel_id }));
    try {
      if (viewMode === 'region') {
        await attachChannelsToRegion(selectedNode.deviceId, keys);
      } else {
        const bg = selectedNode.businessGroup || selectedNode.deviceId;
        await attachChannelsToGroup(selectedNode.deviceId, bg, keys);
      }
      showToast(t('gbCatalog.attachSuccess'), 'success');
      showAttachDialog = false;
      await selectNode(selectedNode);
    } catch (e) {
      showToast(t('gbCatalog.attachFailed'), 'error');
    }
  }

  async function detachSelected() {
    if (!selectedNode || selectedChannelKeys.size === 0) return;
    const keys: ChannelKey[] = nodeChannels
      .filter(c => selectedChannelKeys.has(chKey(c)))
      .map(c => ({ device_id: c.device_id, channel_id: c.channel_id }));
    try {
      if (viewMode === 'region') {
        await detachChannelsFromRegion(keys);
      } else {
        await detachChannelsFromGroup(keys);
      }
      showToast(t('gbCatalog.detachSuccess'), 'success');
      await selectNode(selectedNode);
    } catch (e) {
      showToast(t('gbCatalog.detachFailed'), 'error');
    }
  }

  // ==================== 节点 CRUD ====================

  function openCreateForm() {
    nodeFormMode = 'create';
    editingNode = null;
    formDeviceId = '';
    formName = '';
    civilPath = '';
    formParentDeviceId = selectedNode?.deviceId || '';
    // 分组视图: 业务分组默认取选中节点所属
    formBusinessGroup = viewMode === 'group' ? (selectedNode?.businessGroup || '') : '';
    showNodeForm = true;
  }

  function openEditForm(node: CatNode) {
    nodeFormMode = 'edit';
    editingNode = node;
    formDeviceId = node.deviceId;
    formName = node.name;
    civilPath = '';
    showNodeForm = true;
  }

  // 区划视图: 查标准库, 自动填充名称并显示层级
  async function lookupCivilCode() {
    const code = formDeviceId.trim();
    if (!code) return;
    try {
      const res = await getCivilCodeDescription(code);
      if (res.found) {
        if (res.name) formName = res.name;
        civilPath = res.description;
      } else {
        civilPath = '';
        showToast(t('gbCatalog.civilNotFound'), 'error');
      }
    } catch {
      showToast(t('common.error'), 'error');
    }
  }

  // 区划视图: 按标准库新增 (含全部上级)
  async function addByStandard() {
    const code = formDeviceId.trim();
    if (!code) {
      showToast(t('gbCatalog.fieldRequired'), 'error');
      return;
    }
    try {
      const res = await addRegionByCivilCode(code);
      showToast(t('gbCatalog.syncDone', { count: res.created }), 'success');
      showNodeForm = false;
      await refreshAfterMutation();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('gbCatalog.saveFailed'), 'error');
    }
  }

  async function submitNodeForm() {
    if (!formName.trim() || !formDeviceId.trim()) {
      showToast(t('gbCatalog.fieldRequired'), 'error');
      return;
    }
    try {
      if (nodeFormMode === 'edit' && editingNode) {
        if (viewMode === 'region') {
          await updateRegion(editingNode.id, { device_id: formDeviceId.trim(), name: formName.trim() });
        } else {
          await updateGroup(editingNode.id, { name: formName.trim() });
        }
      } else if (viewMode === 'region') {
        await createRegion({
          device_id: formDeviceId.trim(),
          name: formName.trim(),
          parent_device_id: formParentDeviceId.trim() || undefined,
        });
      } else {
        await createGroup({
          device_id: formDeviceId.trim(),
          name: formName.trim(),
          parent_device_id: formParentDeviceId.trim() || undefined,
          business_group: formBusinessGroup.trim() || undefined,
        });
      }
      showToast(t('gbCatalog.saveSuccess'), 'success');
      showNodeForm = false;
      await refreshAfterMutation();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('gbCatalog.saveFailed'), 'error');
    }
  }

  async function handleDeleteNode(node: CatNode) {
    if (!confirm(t('gbCatalog.deleteConfirm', { name: node.name }))) return;
    try {
      if (viewMode === 'region') await deleteRegion(node.id);
      else await deleteGroup(node.id);
      showToast(t('gbCatalog.deleteSuccess'), 'success');
      if (selectedNode?.id === node.id) {
        selectedNode = null;
        nodeChannels = [];
      }
      await refreshAfterMutation();
    } catch (e) {
      showToast(t('gbCatalog.deleteFailed'), 'error');
    }
  }

  async function handleSyncRegions() {
    try {
      const res = await syncRegions();
      showToast(t('gbCatalog.syncDone', { count: res.created }), 'success');
      await refreshAfterMutation();
    } catch (e) {
      showToast(t('gbCatalog.syncFailed'), 'error');
    }
  }

  // 变更后刷新已展开的层级
  async function refreshAfterMutation() {
    topNodes = await fetchChildren(0);
    const reloaded: Record<number, CatNode[]> = {};
    for (const idStr of Object.keys(childrenMap)) {
      const id = Number(idStr);
      if (expandedNodes.has(id)) {
        try {
          reloaded[id] = await fetchChildren(id);
        } catch {
          // 节点可能已删除, 跳过
        }
      }
    }
    childrenMap = reloaded;
  }

  let attachReloadTimer: ReturnType<typeof setTimeout> | null = null;
  function onAttachSearchInput() {
    if (attachReloadTimer) clearTimeout(attachReloadTimer);
    attachReloadTimer = setTimeout(loadAttachCandidates, 250);
  }

  onMount(loadTop);
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6">
    <!-- 页头 -->
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-bold th-text-primary flex items-center gap-2">
        <Network size={20} class="text-accent" />
        {t('gbCatalog.title')}
      </h1>
      <div class="flex items-center gap-2">
        <!-- 视图切换 -->
        <div class="flex items-center gap-1 p-1 rounded-lg th-bg-tertiary border th-border">
          <button
            class="px-3 py-1.5 text-xs font-medium rounded-md transition-colors flex items-center gap-1.5 {viewMode === 'region' ? 'bg-[var(--color-primary)] text-white' : 'th-text-secondary hover:th-text-primary'}"
            onclick={() => switchView('region')}
          >
            <MapPin size={13} />
            {t('gbCatalog.regionView')}
          </button>
          <button
            class="px-3 py-1.5 text-xs font-medium rounded-md transition-colors flex items-center gap-1.5 {viewMode === 'group' ? 'bg-[var(--color-primary)] text-white' : 'th-text-secondary hover:th-text-primary'}"
            onclick={() => switchView('group')}
          >
            <Network size={13} />
            {t('gbCatalog.groupView')}
          </button>
        </div>
        {#if viewMode === 'region'}
          <button onclick={handleSyncRegions} class="btn btn-ghost btn-sm" title={t('gbCatalog.syncRegions')}>
            <RefreshCw size={14} />
            {t('gbCatalog.syncRegions')}
          </button>
        {/if}
        <button onclick={openCreateForm} class="btn btn-primary btn-sm">
          <Plus size={14} />
          {t('gbCatalog.newNode')}
        </button>
      </div>
    </div>

    {#if loading}
      <div class="flex justify-center items-center h-64"><div class="spinner spinner-lg"></div></div>
    {:else if error}
      <div class="card p-8 text-center">
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadTop} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {:else}
      <div class="flex gap-4" style="height: calc(100vh - 160px);">
        <!-- 左: 目录树 -->
        <div class="w-72 flex-shrink-0 card border th-border overflow-hidden flex flex-col">
          <div class="p-3 border-b th-border">
            <h3 class="text-xs font-semibold th-text-secondary uppercase tracking-wider">
              {viewMode === 'region' ? t('gbCatalog.regionTree') : t('gbCatalog.groupTree')}
            </h3>
          </div>
          <div class="flex-1 overflow-y-auto p-2">
            {#if topNodes.length === 0}
              <div class="text-center py-8 th-text-muted">
                <Folder size={32} class="mx-auto mb-2 opacity-50" />
                <p class="text-xs">{t('gbCatalog.noNodes')}</p>
              </div>
            {:else}
              {#each topNodes as node}
                {@render TreeNode(node, 0)}
              {/each}
            {/if}
          </div>
        </div>

        <!-- 右: 通道列表 -->
        <div class="flex-1 flex flex-col overflow-hidden min-w-0 card border th-border">
          {#if selectedNode}
            <div class="flex items-center justify-between p-3 border-b th-border">
              <div class="flex items-center gap-2 min-w-0">
                <h2 class="text-base font-semibold th-text-primary truncate">{selectedNode.name}</h2>
                <span class="text-xs font-mono th-text-muted truncate">{selectedNode.deviceId}</span>
                <span class="text-xs th-text-secondary">· {nodeChannels.length} {t('gbCatalog.channels')}</span>
              </div>
              <div class="flex items-center gap-2 flex-shrink-0">
                <button onclick={openAttachDialog} class="btn btn-primary btn-sm">
                  <Link2 size={14} />
                  {t('gbCatalog.attachChannel')}
                </button>
                <button
                  onclick={detachSelected}
                  class="btn btn-ghost btn-sm text-red-500"
                  disabled={selectedChannelKeys.size === 0}
                >
                  <Unlink size={14} />
                  {t('gbCatalog.detach')} ({selectedChannelKeys.size})
                </button>
              </div>
            </div>

            <div class="flex-1 overflow-y-auto">
              {#if nodeChannels.length === 0}
                <div class="text-center py-16 th-text-muted">
                  <Link2 size={40} class="mx-auto mb-3 opacity-40" />
                  <p class="text-sm">{t('gbCatalog.noChannels')}</p>
                </div>
              {:else}
                <table class="w-full text-sm">
                  <thead class="th-bg-tertiary sticky top-0">
                    <tr class="th-text-secondary text-xs">
                      <th class="w-10 px-3 py-2"></th>
                      <th class="text-left px-3 py-2 font-medium">{t('gbCatalog.channelName')}</th>
                      <th class="text-left px-3 py-2 font-medium">{t('gbCatalog.channelId')}</th>
                      <th class="text-left px-3 py-2 font-medium">{t('gbCatalog.status')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each nodeChannels as ch}
                      {@const k = chKey(ch)}
                      <tr class="border-b th-border hover:th-bg-hover">
                        <td class="px-3 py-2">
                          <input
                            type="checkbox"
                            checked={selectedChannelKeys.has(k)}
                            onchange={() => toggleChannelSelect(ch)}
                          />
                        </td>
                        <td class="px-3 py-2 th-text-primary truncate">{ch.name || ch.channel_id}</td>
                        <td class="px-3 py-2 font-mono text-xs th-text-muted">{ch.channel_id}</td>
                        <td class="px-3 py-2">
                          <span class="flex items-center gap-1.5">
                            <span class="w-1.5 h-1.5 rounded-full {ch.status === 'ON' ? 'bg-green-500' : 'bg-gray-500'}"></span>
                            <span class="th-text-secondary text-xs">{ch.status === 'ON' ? t('gbCatalog.online') : t('gbCatalog.offline')}</span>
                          </span>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          {:else}
            <div class="flex-1 flex items-center justify-center th-text-muted">
              <div class="text-center">
                <Network size={48} class="mx-auto mb-3 opacity-50" />
                <p class="text-sm">{t('gbCatalog.selectNode')}</p>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </main>
</div>

<!-- 树节点模板 (懒加载) -->
{#snippet TreeNode(node: CatNode, depth: number)}
  {@const isExpanded = expandedNodes.has(node.id)}
  {@const isSelected = selectedNode?.id === node.id}
  {@const loadedChildren = childrenMap[node.id]}
  <div>
    <div
      class="group w-full flex items-center gap-1.5 px-2 py-1.5 rounded text-left transition-colors text-sm {isSelected ? 'bg-[var(--color-primary)] text-white' : 'hover:th-bg-hover th-text-secondary'}"
      style="padding-left: {depth * 16 + 6}px"
    >
      <span
        class="p-0.5 hover:bg-white/20 rounded cursor-pointer flex-shrink-0"
        onclick={() => toggleNode(node)}
        onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); toggleNode(node); } }}
        role="button"
        tabindex="0"
      >
        {#if isExpanded}<ChevronDown size={12} />{:else}<ChevronRight size={12} />{/if}
      </span>
      <button class="flex items-center gap-1.5 flex-1 min-w-0 text-left" onclick={() => selectNode(node)}>
        {#if viewMode === 'region'}
          <MapPin size={13} class="shrink-0 {isSelected ? 'text-white' : 'th-text-muted'}" />
        {:else}
          <Network size={13} class="shrink-0 {isSelected ? 'text-white' : 'th-text-muted'}" />
        {/if}
        <span class="truncate {isSelected ? 'font-medium' : ''}">{node.name}</span>
      </button>
      <span class="flex items-center gap-0.5 opacity-0 group-hover:opacity-100 transition-opacity flex-shrink-0">
        <button class="p-1 hover:bg-white/20 rounded" title={t('common.edit')} onclick={() => openEditForm(node)}>
          <Edit2 size={11} />
        </button>
        <button class="p-1 hover:bg-white/20 rounded text-red-400" title={t('common.delete')} onclick={() => handleDeleteNode(node)}>
          <Trash2 size={11} />
        </button>
      </span>
    </div>
    {#if isExpanded && loadedChildren}
      {#if loadedChildren.length === 0}
        <p class="text-xs th-text-muted py-1" style="padding-left: {depth * 16 + 30}px">{t('gbCatalog.noChildren')}</p>
      {:else}
        {#each loadedChildren as child}
          {@render TreeNode(child, depth + 1)}
        {/each}
      {/if}
    {/if}
  </div>
{/snippet}

<!-- 挂接通道对话框 -->
{#if showAttachDialog && selectedNode}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-2xl w-full p-6 max-h-[80vh] overflow-hidden flex flex-col">
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-lg font-semibold th-text-primary">
          {t('gbCatalog.attachTo', { name: selectedNode.name })}
        </h3>
        <button onclick={() => { showAttachDialog = false; }} class="btn btn-ghost btn-sm"><X size={16} /></button>
      </div>

      <div class="flex items-center gap-3 mb-3">
        <div class="relative flex-1">
          <Search size={14} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-muted" />
          <input
            type="search"
            class="input w-full pl-9"
            placeholder={t('gbCatalog.searchChannels')}
            bind:value={attachSearch}
            oninput={onAttachSearchInput}
          />
        </div>
        <label class="flex items-center gap-1.5 text-xs th-text-secondary whitespace-nowrap cursor-pointer">
          <input type="checkbox" bind:checked={attachOnlyUnassigned} onchange={loadAttachCandidates} />
          {t('gbCatalog.onlyUnassigned')}
        </label>
      </div>

      <div class="flex-1 overflow-y-auto border th-border rounded-lg">
        {#if attachCandidates.length === 0}
          <div class="text-center py-12 th-text-muted"><p>{t('gbCatalog.noChannels')}</p></div>
        {:else}
          <table class="w-full text-sm">
            <tbody>
              {#each attachCandidates as ch}
                {@const k = chKey(ch)}
                <tr class="border-b th-border hover:th-bg-hover cursor-pointer" onclick={() => toggleAttachSelect(ch)}>
                  <td class="w-10 px-3 py-2">
                    <input type="checkbox" checked={attachSelected.has(k)} onclick={(e) => e.stopPropagation()} onchange={() => toggleAttachSelect(ch)} />
                  </td>
                  <td class="px-3 py-2 th-text-primary truncate">{ch.name || ch.channel_id}</td>
                  <td class="px-3 py-2 font-mono text-xs th-text-muted">{ch.channel_id}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        {/if}
      </div>

      <div class="flex gap-3 justify-end mt-4">
        <button onclick={() => { showAttachDialog = false; }} class="btn btn-secondary">{t('common.cancel')}</button>
        <button onclick={confirmAttach} class="btn btn-primary" disabled={attachSelected.size === 0}>
          {t('gbCatalog.confirmAttach')} ({attachSelected.size})
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- 节点表单对话框 -->
{#if showNodeForm}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">
        {nodeFormMode === 'edit' ? t('gbCatalog.editNode') : t('gbCatalog.newNode')}
      </h3>
      <div class="mb-4">
        <label for="node-device-id" class="input-label">{t('gbCatalog.nodeCode')}</label>
        <div class="flex gap-2 mt-1">
          <input
            id="node-device-id"
            type="text"
            class="input font-mono flex-1"
            bind:value={formDeviceId}
            disabled={nodeFormMode === 'edit' && viewMode === 'group'}
            placeholder={viewMode === 'region' ? '4401' : '34020000002150000001'}
          />
          {#if viewMode === 'region' && nodeFormMode === 'create'}
            <button type="button" class="btn btn-secondary btn-sm whitespace-nowrap" onclick={lookupCivilCode}>
              {t('gbCatalog.lookupStandard')}
            </button>
          {/if}
        </div>
        {#if viewMode === 'group'}
          <p class="text-xs th-text-muted mt-1">{t('gbCatalog.groupCodeHint')}</p>
        {/if}
        {#if viewMode === 'region' && civilPath}
          <p class="text-xs text-green-600 mt-1">{civilPath}</p>
        {/if}
      </div>
      <div class="mb-4">
        <label for="node-name" class="input-label">{t('gbCatalog.nodeName')}</label>
        <input id="node-name" type="text" class="input mt-1" bind:value={formName} />
      </div>
      {#if nodeFormMode === 'create'}
        <div class="mb-4">
          <label for="node-parent" class="input-label">{t('gbCatalog.parentCode')}</label>
          <input id="node-parent" type="text" class="input mt-1 font-mono" bind:value={formParentDeviceId} placeholder={t('gbCatalog.parentCodeHint')} />
        </div>
        {#if viewMode === 'group'}
          <div class="mb-4">
            <label for="node-bg" class="input-label">{t('gbCatalog.businessGroup')}</label>
            <input id="node-bg" type="text" class="input mt-1 font-mono" bind:value={formBusinessGroup} placeholder={t('gbCatalog.businessGroupHint')} />
          </div>
        {/if}
      {/if}
      <div class="flex gap-3 justify-end items-center">
        {#if viewMode === 'region' && nodeFormMode === 'create'}
          <button onclick={addByStandard} class="btn btn-ghost btn-sm mr-auto" title={t('gbCatalog.addByStandardHint')}>
            {t('gbCatalog.addByStandard')}
          </button>
        {/if}
        <button onclick={() => { showNodeForm = false; }} class="btn btn-secondary">{t('common.cancel')}</button>
        <button onclick={submitNodeForm} class="btn btn-primary">{t('common.save')}</button>
      </div>
    </div>
  </div>
{/if}
