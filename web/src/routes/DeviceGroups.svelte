<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listDeviceGroups,
    getDeviceGroup,
    getDeviceGroupTree,
    createDeviceGroup,
    updateDeviceGroup,
    deleteDeviceGroup,
    listGroupChannels,
    addGroupChannel,
    removeGroupChannel,
    listGB28181Devices,
    listCameras,
    getSnapshotUrl,
    getStream,
    getStreamingSettings,
    streamMediaURL,
  } from '$lib/api';
  import type {
    DeviceGroup,
    DeviceGroupTreeNode,
    DeviceGroupChannelDetail,
    GB28181Device,
    Camera,
    StreamInfo,
  } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import FlvPlayer from '../components/FlvPlayer.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import TalkButton from '../components/TalkButton.svelte';
  import {
    FolderTree,
    Plus,
    Edit2,
    Trash2,
    ChevronRight,
    ChevronDown,
    Video,
    X,
    Search,
    Users,
    Mic,
    Move,
  } from 'lucide-svelte';

  type GridLayout = 1 | 4 | 6 | 9 | 16;
  type GroupPlaybackProtocol = 'webrtc' | 'flv' | 'ws-flv' | 'hls' | 'll-hls';
  const LAYOUT_OPTIONS: GridLayout[] = [1, 4, 6, 9, 16];
  const GROUP_PLAYBACK_PROTOCOLS: GroupPlaybackProtocol[] = ['webrtc', 'flv', 'ws-flv', 'hls', 'll-hls'];
  const GRID_COLS_CLASS: Record<GridLayout, string> = {
    1: 'grid-cols-1',
    4: 'grid-cols-2',
    6: 'grid-cols-3',
    9: 'grid-cols-3',
    16: 'grid-cols-4',
  };

  let groups = $state<DeviceGroup[]>([]);
  let groupTree = $state<DeviceGroupTreeNode[]>([]);
  let loading = $state(true);
  let error = $state('');

  // Selected group
  let selectedGroupId = $state<number | null>(null);
  let selectedGroup = $state<DeviceGroup | null>(null);
  let groupChannels = $state<DeviceGroupChannelDetail[]>([]);
  let channelStats = $state({ total: 0, online: 0 });

  // Form state
  let showCreateForm = $state(false);
  let showEditForm = $state(false);
  let editingGroup = $state<DeviceGroup | null>(null);
  let newGroupName = $state('');
  let newGroupParentId = $state(0);

  // Add channel dialog
  let showAddChannelDialog = $state(false);
  let gb28181Devices = $state<GB28181Device[]>([]);
  let cameras = $state<Camera[]>([]);
  let searchQuery = $state('');
  let activeTab = $state<'gb28181' | 'cameras'>('gb28181');

  // Expanded tree nodes
  let expandedNodes = $state<Set<number>>(new Set());

  // Grid layout
  let gridLayout = $state<GridLayout>(4);
  let defaultProtocol = $state<string>('flv');

  // Playing slots: slotIndex → { channel, streamId, streamUrl, protocol }
  let playingSlots = $state<Record<number, { channel: DeviceGroupChannelDetail; streamId: string; streamUrl: string; protocol: GroupPlaybackProtocol }>>({});

  // Selected channel for detail panel
  let selectedSlotIndex = $state<number | null>(null);
  let selectedChannel = $state<DeviceGroupChannelDetail | null>(null);
  let showDevicePanel = $state(false);

  function getGridClass(layout: GridLayout): string {
    return GRID_COLS_CLASS[layout];
  }

  function getStreamId(channel: DeviceGroupChannelDetail): string {
    if (channel.device_id === channel.channel_id) {
      return channel.device_id;
    }
    return `${channel.device_id}_${channel.channel_id}`;
  }

  function getPlayURL(stream: StreamInfo, protocol: string): string {
    const found = stream.play_urls?.find(p => p.protocol === protocol);
    return found?.url || '';
  }

  function getProxyPlayURL(streamId: string, protocol: GroupPlaybackProtocol): string {
    if (protocol === 'webrtc') return '';
    if (protocol === 'hls') return streamMediaURL(streamId, 'hls');
    if (protocol === 'll-hls') return streamMediaURL(streamId, 'll-hls');
    if (protocol === 'flv') return streamMediaURL(streamId, 'flv');
    return '';
  }

  function getGroupPlayerURL(stream: StreamInfo, protocol: GroupPlaybackProtocol): string {
    return getPlayURL(stream, protocol) || getProxyPlayURL(stream.stream_id, protocol);
  }

  function choosePlaybackProtocol(stream: StreamInfo): GroupPlaybackProtocol {
    // Check if default protocol is available
    if (defaultProtocol === 'webrtc') return 'webrtc';
    if (defaultProtocol === 'wasm' || defaultProtocol === 'fmp4') {
      // These are not in GroupPlaybackProtocol, fall through to check stream URLs
    }
    if (
      GROUP_PLAYBACK_PROTOCOLS.includes(defaultProtocol as GroupPlaybackProtocol) &&
      (stream.play_urls?.some(p => p.protocol === defaultProtocol) || defaultProtocol === 'hls' || defaultProtocol === 'll-hls' || defaultProtocol === 'flv' || defaultProtocol === 'webrtc')
    ) {
      return defaultProtocol as GroupPlaybackProtocol;
    }

    // Fallback: check available protocols
    const available = new Set(stream.play_urls?.map(p => p.protocol) || []);
    for (const protocol of GROUP_PLAYBACK_PROTOCOLS) {
      if (available.has(protocol) || protocol === 'hls' || protocol === 'flv' || protocol === 'webrtc') {
        return protocol;
      }
    }
    return 'flv';
  }

  async function loadGroups() {
    loading = true;
    error = '';
    try {
      const [groupsData, treeData] = await Promise.all([
        listDeviceGroups(),
        getDeviceGroupTree(),
      ]);
      groups = groupsData;
      groupTree = treeData;
    } catch (e) {
      error = e instanceof Error ? e.message : String(t('common.error'));
    } finally {
      loading = false;
    }
  }

  async function loadGroupChannels(groupId: number) {
    try {
      const [channels, groupInfo] = await Promise.all([
        listGroupChannels(groupId),
        getDeviceGroup(groupId),
      ]);
      groupChannels = channels || [];
      channelStats = {
        total: groupInfo?.channel_total ?? 0,
        online: groupInfo?.channel_online ?? 0,
      };
    } catch (e) {
      console.error('Failed to load group channels:', e);
      showToast(t('common.error'), 'error');
    }
  }

  async function loadGB28181Devices() {
    try {
      const [gbRes, camRes] = await Promise.all([
        listGB28181Devices(),
        listCameras(),
      ]);
      gb28181Devices = gbRes.devices || [];
      cameras = (camRes || []).filter(c => c.protocol !== 'gb28181');
    } catch (e) {
      console.warn('Failed to load devices:', e);
    }
  }

  function selectGroup(group: DeviceGroup) {
    selectedGroupId = group.id;
    selectedGroup = group;
    selectedChannel = null;
    showDevicePanel = false;
    selectedSlotIndex = null;
    loadGroupChannels(group.id);
  }

  function toggleNode(nodeId: number) {
    const next = new Set(expandedNodes);
    if (next.has(nodeId)) {
      next.delete(nodeId);
    } else {
      next.add(nodeId);
    }
    expandedNodes = next;
  }

  function handleSlotClick(slotIndex: number) {
    const channel = groupChannels[slotIndex];
    if (!channel) return;

    const streamId = getStreamId(channel);

    // If this slot is already playing, just select it for the panel
    if (playingSlots[slotIndex] && playingSlots[slotIndex].streamId === streamId) {
      selectedSlotIndex = slotIndex;
      selectedChannel = channel;
      showDevicePanel = true;
      return;
    }

    // Play this channel in the slot
    void playChannel(slotIndex, channel);
  }

  async function playChannel(slotIndex: number, channel: DeviceGroupChannelDetail) {
    const streamId = getStreamId(channel);
    let stream: StreamInfo;

    try {
      stream = await getStream(streamId);
    } catch (e) {
      console.warn('Failed to load stream play URLs:', e);
      showToast(t('streams.loadFailed'), 'error');
      return;
    }

    const protocol = choosePlaybackProtocol(stream);
    
    // WebRTC doesn't need a stream URL - the WebRTCPlayer component handles connection directly
    if (protocol === 'webrtc') {
      playingSlots = {
        ...playingSlots,
        [slotIndex]: {
          channel,
          streamId,
          streamUrl: '', // WebRTC component doesn't need URL
          protocol,
        },
      };
      selectedSlotIndex = slotIndex;
      selectedChannel = channel;
      showDevicePanel = true;
      return;
    }

    const streamUrl = getGroupPlayerURL(stream, protocol);
    if (!streamUrl) {
      showToast(t('streams.streamInactive'), 'error');
      return;
    }

    // Set playing state
    playingSlots = {
      ...playingSlots,
      [slotIndex]: {
        channel,
        streamId,
        streamUrl,
        protocol,
      },
    };

    // Select this slot
    selectedSlotIndex = slotIndex;
    selectedChannel = channel;
    showDevicePanel = true;
  }

  function stopSlot(slotIndex: number) {
    const { [slotIndex]: _, ...rest } = playingSlots;
    playingSlots = rest;
    if (selectedSlotIndex === slotIndex) {
      selectedSlotIndex = null;
      selectedChannel = null;
      showDevicePanel = false;
    }
  }

  function getCellClass(slotIndex: number): string {
    let cls = '';
    if (selectedSlotIndex === slotIndex) {
      cls = 'ring-2 ring-[var(--color-primary)]';
    }
    // 6-screen: first slot spans 2x2
    if (gridLayout === 6 && slotIndex === 0) {
      cls += ' col-span-2 row-span-2';
    }
    return cls;
  }

  // Create group
  function openCreateForm(parentId: number = 0) {
    newGroupName = '';
    newGroupParentId = parentId;
    showCreateForm = true;
  }

  async function handleCreateGroup() {
    if (!newGroupName.trim()) {
      showToast(t('groups.nameRequired'), 'error');
      return;
    }
    try {
      await createDeviceGroup({
        name: newGroupName.trim(),
        parent_id: newGroupParentId,
      });
      showToast(t('groups.createSuccess'), 'success');
      showCreateForm = false;
      await loadGroups();
    } catch (e) {
      showToast(t('groups.createFailed'), 'error');
    }
  }

  // Edit group
  function openEditForm(group: DeviceGroup) {
    editingGroup = group;
    newGroupName = group.name;
    showEditForm = true;
  }

  async function handleUpdateGroup() {
    if (!editingGroup || !newGroupName.trim()) {
      showToast(t('groups.nameRequired'), 'error');
      return;
    }
    try {
      await updateDeviceGroup(editingGroup.id, {
        name: newGroupName.trim(),
      });
      showToast(t('groups.updateSuccess'), 'success');
      showEditForm = false;
      editingGroup = null;
      await loadGroups();
      if (selectedGroupId === editingGroup?.id) {
        selectedGroup = { ...selectedGroup!, name: newGroupName.trim() };
      }
    } catch (e) {
      showToast(t('groups.updateFailed'), 'error');
    }
  }

  // Delete group
  async function handleDeleteGroup(group: DeviceGroup) {
    if (!confirm(t('groups.deleteConfirm', { name: group.name }))) {
      return;
    }
    try {
      await deleteDeviceGroup(group.id);
      showToast(t('groups.deleteSuccess'), 'success');
      if (selectedGroupId === group.id) {
        selectedGroupId = null;
        selectedGroup = null;
        groupChannels = [];
        playingSlots = {};
      }
      await loadGroups();
    } catch (e) {
      showToast(t('groups.deleteFailed'), 'error');
    }
  }

  // Add channel to group
  function openAddChannelDialog() {
    if (!selectedGroupId) return;
    searchQuery = '';
    showAddChannelDialog = true;
    loadGB28181Devices();
  }

  async function handleAddChannel(deviceId: string, channelId: string) {
    if (!selectedGroupId) return;
    try {
      await addGroupChannel(selectedGroupId, deviceId, channelId);
      showToast(t('groups.channelAdded'), 'success');
      await loadGroupChannels(selectedGroupId);
    } catch (e) {
      showToast(t('groups.channelAddFailed'), 'error');
    }
  }

  async function handleRemoveChannel(channel: DeviceGroupChannelDetail) {
    if (!selectedGroupId) return;
    try {
      await removeGroupChannel(selectedGroupId, channel.device_id, channel.channel_id);
      showToast(t('groups.channelRemoved'), 'success');
      // Stop playing if this channel was playing
      const streamId = getStreamId(channel);
      for (const [idx, slot] of Object.entries(playingSlots)) {
        if (slot.streamId === streamId) {
          stopSlot(parseInt(idx));
          break;
        }
      }
      await loadGroupChannels(selectedGroupId);
    } catch (e) {
      showToast(t('groups.channelRemoveFailed'), 'error');
    }
  }

  // Filter devices based on search
  let filteredDevices = $derived(() => {
    const q = searchQuery.trim().toLowerCase();
    if (activeTab === 'gb28181') {
      if (!q) return gb28181Devices;
      return gb28181Devices.filter(
        d =>
          d.device_id.toLowerCase().includes(q) ||
          d.name.toLowerCase().includes(q)
      );
    }
    if (!q) return cameras;
    return cameras.filter(
      c =>
        c.id.toLowerCase().includes(q) ||
        (c.name || '').toLowerCase().includes(q)
    );
  });

  // Check if channel is already in group
  function isChannelInGroup(deviceId: string, channelId: string): boolean {
    return groupChannels.some(c => c.device_id === deviceId && c.channel_id === channelId);
  }

  onMount(() => {
    loadGroups();
    getStreamingSettings()
      .then(config => {
        if (config.default_protocol) {
          defaultProtocol = config.default_protocol;
        }
      })
      .catch(e => {
        console.warn('Failed to load streaming settings:', e);
      });
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6">
    <!-- Page Header -->
    <div class="flex items-center justify-between mb-4">
      <h1 class="text-xl font-bold th-text-primary flex items-center gap-2">
        <FolderTree size={20} class="text-accent" />
        {t('groups.title')}
      </h1>
      <div class="flex items-center gap-2">
        <!-- Grid Layout Options -->
        <div class="flex items-center gap-1 p-1 rounded-lg th-bg-tertiary border th-border">
          {#each LAYOUT_OPTIONS as layout}
            <button
              class="px-2.5 py-1.5 text-xs font-medium rounded-md transition-colors {gridLayout === layout ? 'bg-[var(--color-primary)] text-white' : 'th-text-secondary hover:th-text-primary'}"
              onclick={() => gridLayout = layout}
              title="{layout} 画面"
            >
              {layout}
            </button>
          {/each}
        </div>
        <button onclick={() => openCreateForm(0)} class="btn btn-primary btn-sm">
          <Plus size={14} />
          {t('groups.createGroup')}
        </button>
      </div>
    </div>

    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadGroups} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {:else}
      <div class="flex gap-4" style="height: calc(100vh - 160px);">
        <!-- Left: Group Tree -->
        <div class="w-64 flex-shrink-0 card border th-border overflow-hidden flex flex-col">
          <div class="p-3 border-b th-border">
            <h3 class="text-xs font-semibold th-text-secondary uppercase tracking-wider">{t('groups.groupTree')}</h3>
          </div>
          <div class="flex-1 overflow-y-auto p-2">
            {#if groupTree.length === 0}
              <div class="text-center py-8 th-text-muted">
                <FolderTree size={32} class="mx-auto mb-2 opacity-50" />
                <p class="text-xs">{t('groups.noGroups')}</p>
              </div>
            {:else}
              {#each groupTree as node}
                {@render TreeNode(node, 0)}
              {/each}
            {/if}
          </div>
        </div>

        <!-- Center: Video Grid -->
        <div class="flex-1 flex flex-col overflow-hidden min-w-0">
          {#if selectedGroup}
            <!-- Group Header -->
            <div class="flex items-center justify-between mb-3">
              <div class="flex items-center gap-3">
                <h2 class="text-lg font-semibold th-text-primary">{selectedGroup.name}</h2>
                <span class="text-sm th-text-secondary">
                  {channelStats.total} {t('groups.channels')} · {channelStats.online} {t('groups.online')}
                </span>
              </div>
              <div class="flex items-center gap-2">
                <button onclick={openAddChannelDialog} class="btn btn-primary btn-sm">
                  <Plus size={14} />
                  {t('groups.addChannel')}
                </button>
                <button onclick={() => openEditForm(selectedGroup)} class="btn btn-ghost btn-sm">
                  <Edit2 size={14} />
                </button>
                <button onclick={() => handleDeleteGroup(selectedGroup)} class="btn btn-ghost btn-sm text-red-500">
                  <Trash2 size={14} />
                </button>
              </div>
            </div>

            <!-- Video Grid (wvp style: fixed grid, click to play) -->
            <div class="flex-1 overflow-hidden relative">
              <div class="grid gap-1 h-full {getGridClass(gridLayout)}">
                {#each Array(gridLayout) as _, slotIndex}
                  {@const channel = groupChannels[slotIndex]}
                  {@const slot = playingSlots[slotIndex]}
                  {@const isPlaying = !!slot && (slot.protocol === 'webrtc' || !!slot.streamUrl)}
                  {@const isSelected = selectedSlotIndex === slotIndex}
                  <!-- svelte-ignore a11y_no_static_element_interactions -->
                  <div
                    class="relative bg-black rounded overflow-hidden cursor-pointer {getCellClass(slotIndex)}"
                    role="button"
                    tabindex="0"
                    onclick={() => handleSlotClick(slotIndex)}
                    onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleSlotClick(slotIndex); } }}
                  >
                    {#if isPlaying}
                      <!-- Video Player -->
                      {#if slot.protocol === 'webrtc'}
                        <WebRTCPlayer
                          cameraId={slot.streamId}
                          cameraName={slot.channel.channel_name || slot.channel.channel_id}
                          whepUrl={streamMediaURL(slot.streamId, 'webrtc')}
                          expanded={gridLayout === 1}
                          tabVisible={true}
                        />
                      {:else if slot.protocol === 'flv' || slot.protocol === 'ws-flv'}
                        <FlvPlayer
                          cameraId={slot.streamId}
                          cameraName={slot.channel.channel_name || slot.channel.channel_id}
                          streamUrl={slot.streamUrl}
                          protocol={slot.protocol}
                          expanded={gridLayout === 1}
                          tabVisible={true}
                        />
                      {:else}
                        <VideoPlayer
                          cameraId={slot.streamId}
                          cameraName={slot.channel.channel_name || slot.channel.channel_id}
                          streamUrl={slot.streamUrl}
                          cameraProtocol={slot.protocol}
                          protocol={slot.protocol}
                          expanded={gridLayout === 1}
                          tabVisible={true}
                        />
                      {/if}
                    {:else if channel}
                      <!-- Snapshot thumbnail -->
                      {@const streamId = getStreamId(channel)}
                      <img
                        src="{getSnapshotUrl(streamId)}&_t={Date.now()}"
                        alt={channel.channel_name || channel.channel_id}
                        class="w-full h-full object-cover"
                        loading="lazy"
                        onerror={(e) => { (e.target as HTMLImageElement).style.display = 'none'; }}
                      />
                      <!-- Play overlay -->
                      <div class="absolute inset-0 flex items-center justify-center bg-black/30 opacity-0 hover:opacity-100 transition-opacity">
                        <div class="w-12 h-12 rounded-full bg-white/80 flex items-center justify-center">
                          <Video size={24} class="text-gray-800" />
                        </div>
                      </div>
                      <!-- Status indicator -->
                      <div class="absolute top-2 left-2 z-10">
                        <span class="w-2 h-2 rounded-full {channel.is_online ? 'bg-green-500' : 'bg-gray-500'}"></span>
                      </div>
                      <!-- Bottom overlay -->
                      <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-2 py-1.5">
                        <div class="flex items-center justify-between">
                          <span class="text-white text-xs font-medium truncate">
                            {channel.channel_name || channel.device_name || channel.channel_id}
                          </span>
                        </div>
                      </div>
                    {:else}
                      <!-- Empty slot -->
                      <div class="w-full h-full flex items-center justify-center">
                        <div class="text-center">
                          <Video size={20} class="mx-auto mb-1 text-gray-600" />
                          <p class="text-[10px] text-gray-500">空位 {slotIndex + 1}</p>
                        </div>
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>

              <!-- Right: Device Panel (overlay, doesn't compress grid) -->
              {#if showDevicePanel && selectedChannel}
                <div class="absolute top-0 right-0 bottom-0 w-72 z-20 card border th-border overflow-hidden flex flex-col shadow-xl">
                  <!-- Panel Header -->
                  <div class="flex items-center justify-between p-3 border-b th-border">
                    <h3 class="text-sm font-semibold th-text-primary truncate">
                      {selectedChannel.channel_name || selectedChannel.device_name || selectedChannel.channel_id}
                    </h3>
                    <button class="btn btn-ghost btn-sm p-1" onclick={() => { showDevicePanel = false; selectedChannel = null; selectedSlotIndex = null; }}>
                      <X size={14} />
                    </button>
                  </div>

                  <!-- Panel Content - Scrollable -->
                  <div class="flex-1 overflow-y-auto">
                    <!-- Device Info Section -->
                    <div class="p-3 border-b th-border">
                      <div class="space-y-2">
                        <div class="flex justify-between text-xs">
                          <span class="th-text-tertiary">设备ID</span>
                          <span class="font-mono th-text-primary truncate ml-2">{selectedChannel.device_id}</span>
                        </div>
                        <div class="flex justify-between text-xs">
                          <span class="th-text-tertiary">设备名称</span>
                          <span class="th-text-primary truncate ml-2">{selectedChannel.device_name || '-'}</span>
                        </div>
                        <div class="flex justify-between text-xs">
                          <span class="th-text-tertiary">通道ID</span>
                          <span class="font-mono th-text-primary truncate ml-2">{selectedChannel.channel_id}</span>
                        </div>
                        <div class="flex justify-between text-xs">
                          <span class="th-text-tertiary">通道名称</span>
                          <span class="th-text-primary truncate ml-2">{selectedChannel.channel_name || '-'}</span>
                        </div>
                        <div class="flex justify-between text-xs">
                          <span class="th-text-tertiary">状态</span>
                          <span class="flex items-center gap-1.5">
                            <span class="w-1.5 h-1.5 rounded-full {selectedChannel.is_online ? 'bg-green-500' : 'bg-gray-500'}"></span>
                            <span class="th-text-primary">{selectedChannel.is_online ? '在线' : '离线'}</span>
                          </span>
                        </div>
                      </div>
                    </div>

                    <!-- PTZ Section -->
                    {#if selectedChannel.is_online}
                      <div class="p-3 border-b th-border">
                        <div class="flex items-center gap-2 mb-3">
                          <Move size={14} class="th-text-tertiary" />
                          <span class="text-xs font-semibold th-text-secondary uppercase tracking-wider">PTZ 控制</span>
                        </div>
                        <PtzControl
                          cameraId={getStreamId(selectedChannel)}
                          enabled={true}
                          compact={true}
                        />
                      </div>
                    {/if}

                    <!-- Talk Section -->
                    <div class="p-3">
                      <div class="flex items-center gap-2 mb-3">
                        <Mic size={14} class="th-text-tertiary" />
                        <span class="text-xs font-semibold th-text-secondary uppercase tracking-wider">语音对讲</span>
                      </div>
                      <div class="flex justify-center">
                        <TalkButton
                          deviceId={selectedChannel.device_id}
                          channelId={selectedChannel.channel_id}
                        />
                      </div>
                    </div>

                    <!-- Actions -->
                    <div class="p-3 border-t th-border">
                      <button
                        class="btn btn-danger btn-sm w-full"
                        onclick={() => handleRemoveChannel(selectedChannel)}
                      >
                        <Trash2 size={14} />
                        {t('groups.removeChannel')}
                      </button>
                    </div>
                  </div>
                </div>
              {/if}
            </div>
          {:else}
            <div class="flex-1 flex items-center justify-center th-text-muted card border th-border rounded-lg">
              <div class="text-center">
                <Users size={48} class="mx-auto mb-3 opacity-50" />
                <p class="text-sm">{t('groups.selectGroup')}</p>
                <p class="text-xs mt-1 th-text-tertiary">{t('groups.selectGroupHint')}</p>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </main>
</div>

<!-- Tree Node Template -->
{#snippet TreeNode(node: DeviceGroupTreeNode, depth: number)}
  {@const isExpanded = expandedNodes.has(node.id)}
  {@const isSelected = selectedGroupId === node.id}
  {@const hasChildren = node.children && node.children.length > 0}
  <div>
    <button
      class="w-full flex items-center gap-2 px-2.5 py-1.5 rounded text-left transition-colors text-sm {isSelected ? 'bg-[var(--color-primary)] text-white' : 'hover:th-bg-hover th-text-secondary'}"
      style="padding-left: {depth * 16 + 10}px"
      onclick={() => selectGroup(node)}
    >
      {#if hasChildren}
        <span
          class="p-0.5 hover:bg-white/20 rounded cursor-pointer"
          onclick={(e) => { e.stopPropagation(); toggleNode(node.id); }}
          onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.stopPropagation(); toggleNode(node.id); } }}
          role="button"
          tabindex="0"
        >
          {#if isExpanded}
            <ChevronDown size={12} />
          {:else}
            <ChevronRight size={12} />
          {/if}
        </span>
      {:else}
        <span class="w-4"></span>
      {/if}
      <FolderTree size={14} class="shrink-0 {isSelected ? 'text-white' : 'th-text-muted'}" />
      <span class="truncate {isSelected ? 'font-medium' : ''}">{node.name}</span>
    </button>
    {#if isExpanded && hasChildren}
      {#each node.children as child}
        {@render TreeNode(child, depth + 1)}
      {/each}
    {/if}
  </div>
{/snippet}

<!-- Create Group Dialog -->
{#if showCreateForm}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('groups.createGroup')}</h3>
      <div class="mb-4">
        <label for="create-group-name" class="input-label">{t('groups.name')}</label>
        <input
          id="create-group-name"
          type="text"
          class="input mt-1"
          bind:value={newGroupName}
          placeholder={t('groups.namePlaceholder')}
        />
      </div>
      <div class="mb-6">
        <label for="create-group-parent" class="input-label">{t('groups.parentGroup')}</label>
        <select id="create-group-parent" class="input mt-1" bind:value={newGroupParentId}>
          <option value={0}>{t('groups.rootGroup')}</option>
          {#each groups as group}
            <option value={group.id}>{group.name}</option>
          {/each}
        </select>
      </div>
      <div class="flex gap-3 justify-end">
        <button onclick={() => { showCreateForm = false; }} class="btn btn-secondary">
          {t('common.cancel')}
        </button>
        <button onclick={handleCreateGroup} class="btn btn-primary">
          {t('common.create')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Edit Group Dialog -->
{#if showEditForm && editingGroup}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('groups.editGroup')}</h3>
      <div class="mb-6">
        <label for="edit-group-name" class="input-label">{t('groups.name')}</label>
        <input
          id="edit-group-name"
          type="text"
          class="input mt-1"
          bind:value={newGroupName}
          placeholder={t('groups.namePlaceholder')}
        />
      </div>
      <div class="flex gap-3 justify-end">
        <button onclick={() => { showEditForm = false; editingGroup = null; }} class="btn btn-secondary">
          {t('common.cancel')}
        </button>
        <button onclick={handleUpdateGroup} class="btn btn-primary">
          {t('common.save')}
        </button>
      </div>
    </div>
  </div>
{/if}

<!-- Add Channel Dialog -->
{#if showAddChannelDialog && selectedGroupId}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-2xl w-full p-6 max-h-[80vh] overflow-hidden flex flex-col">
      <div class="flex items-center justify-between mb-4">
        <h3 class="text-lg font-semibold th-text-primary">{t('groups.addChannel')}</h3>
        <button onclick={() => { showAddChannelDialog = false; }} class="btn btn-ghost btn-sm">
          <X size={16} />
        </button>
      </div>

      <!-- Tabs -->
      <div class="flex gap-1 p-1 rounded-lg th-bg-tertiary border th-border mb-4">
        <button
          class="flex-1 px-3 py-2 text-sm font-medium rounded-md transition-colors {activeTab === 'gb28181' ? 'bg-[var(--color-primary)] text-white' : 'th-text-secondary hover:th-text-primary'}"
          onclick={() => { activeTab = 'gb28181'; searchQuery = ''; }}
        >
          GB28181 ({gb28181Devices.length})
        </button>
        <button
          class="flex-1 px-3 py-2 text-sm font-medium rounded-md transition-colors {activeTab === 'cameras' ? 'bg-[var(--color-primary)] text-white' : 'th-text-secondary hover:th-text-primary'}"
          onclick={() => { activeTab = 'cameras'; searchQuery = ''; }}
        >
          {t('nav.cameras')} ({cameras.length})
        </button>
      </div>

      <!-- Search -->
      <div class="relative mb-4">
        <Search size={14} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-muted" />
        <input
          type="search"
          class="input w-full pl-9"
          placeholder={activeTab === 'gb28181' ? t('groups.searchDevices') : t('cameras.search')}
          bind:value={searchQuery}
        />
      </div>

      <!-- Device List -->
      <div class="flex-1 overflow-y-auto space-y-2">
        {#if filteredDevices().length === 0}
          <div class="text-center py-8 th-text-muted">
            <p>{t('groups.noDevices')}</p>
          </div>
        {:else if activeTab === 'gb28181'}
          {#each filteredDevices() as device}
            {@const d = device as GB28181Device}
            <div class="card border th-border p-3">
              <div class="flex items-center justify-between mb-2">
                <div>
                  <p class="font-medium th-text-primary">{d.name || d.device_id}</p>
                  <p class="text-xs font-mono th-text-muted">{d.device_id}</p>
                </div>
                <span class="badge {d.is_online ? 'badge-success' : 'badge-neutral'}">
                  {d.is_online ? t('groups.online') : t('groups.offline')}
                </span>
              </div>
              {#if d.channels && d.channels.length > 0}
                <div class="flex flex-wrap gap-1 mt-2">
                  {#each d.channels as channel}
                    {@const isInGroup = isChannelInGroup(d.device_id, channel.channel_id)}
                    <button
                      class="badge {isInGroup ? 'badge-success' : 'badge-outline'} cursor-pointer"
                      disabled={isInGroup}
                      onclick={() => handleAddChannel(d.device_id, channel.channel_id)}
                    >
                      {channel.name || channel.channel_id}
                      {#if isInGroup}
                        ({t('groups.added')})
                      {/if}
                    </button>
                  {/each}
                </div>
              {/if}
            </div>
          {/each}
        {:else}
          {#each filteredDevices() as camera}
            {@const c = camera as Camera}
            {@const isInGroup = isChannelInGroup(c.id, c.id)}
            <div class="card border th-border p-3">
              <div class="flex items-center justify-between">
                <div>
                  <p class="font-medium th-text-primary">{c.name || c.id}</p>
                  <p class="text-xs font-mono th-text-muted">{c.id} · {c.protocol}</p>
                </div>
                <button
                  class="btn btn-sm {isInGroup ? 'btn-ghost' : 'btn-primary'}"
                  disabled={isInGroup}
                  onclick={() => handleAddChannel(c.id, c.id)}
                >
                  {isInGroup ? t('groups.added') : t('groups.addChannel')}
                </button>
              </div>
            </div>
          {/each}
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  /* Responsive grid adjustments */
  @media (max-width: 767px) {
    :global(.grid-cols-2),
    :global(.grid-cols-3),
    :global(.grid-cols-4) {
      grid-template-columns: 1fr !important;
    }
  }

  @media (min-width: 768px) and (max-width: 1023px) {
    :global(.grid-cols-3) {
      grid-template-columns: repeat(2, 1fr) !important;
    }
    :global(.grid-cols-4) {
      grid-template-columns: repeat(2, 1fr) !important;
    }
  }
</style>
