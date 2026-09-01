<script lang="ts">
  import { onMount, setContext } from 'svelte';
  import { getDashboardCameras, getCredentials, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities, getHealthCameras, apiRequest } from '$lib/api';
  import type { Camera, ProtocolInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { Loader2, AlertCircle, Video, VideoOff, X, Settings, ImageOff, CircleCheck, CirclePause, CircleAlert, RefreshCw, WifiOff, LayoutGrid, Plus, Search } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import FlvPlayer from '../components/FlvPlayer.svelte';
  // WasmPlayer is lazy-loaded to keep main bundle small (~180 KB WebCodecs/AI deps)
  import { getStreamingSettings } from '$lib/api/settings';
  import { formatDate } from '$lib/format';
  import { createSnapshotManager } from '$lib/snapshot';
  import { createReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';

  type GridLayout = 1 | 4 | 6 | 9 | 16;
  const LAYOUT_OPTIONS: GridLayout[] = [1, 4, 6, 9, 16];
  const GRID_COLS_CLASS: Record<GridLayout, string> = {
    1: 'grid-cols-1',
    4: 'grid-cols-2',
    6: 'grid-cols-3',
    9: 'grid-cols-3',
    16: 'grid-cols-4',
  };
  const GRID_EXPAND_CLASS: Record<GridLayout, string> = {
    1: 'col-span-1 row-span-1',
    4: 'col-span-2 row-span-2',
    6: 'col-span-3 row-span-3',
    9: 'col-span-3 row-span-3',
    16: 'col-span-4 row-span-4',
  };

  let loading = $state(true);
  let error = $state('');
  let expandedCameraId = $state<string | null>(null);

  // Page Visibility — pause/resume all players when tab hidden/visible
  let tabVisible = $state(true);

  let ptzOpenIndex = $state(-1);

  let allCameras = $state<Camera[]>([]);
  let configOpen = $state(false);
  let gridLayout = $state<GridLayout>(4);
  let slotAssignments = $state<(string | null)[]>([null, null, null, null]);
  let pendingGridLayout = $state<GridLayout>(4);
  let pendingSlotAssignments = $state<(string | null)[]>([null, null, null, null]);
  let editingSlotIndex = $state<number | null>(null);
  let configSearchQuery = $state('');

  let cameraById = $derived(new Map(allCameras.map(c => [c.id, c])));
  let assignedCameraIds = $derived(
    [...new Set(slotAssignments.filter((id): id is string => !!id))]
  );
  let hasAnyAssignment = $derived(slotAssignments.some(id => !!id));

  // Snapshot state
  let snapshotUrls = $state<Record<string, string>>({});
  let snapshotLoading = $state<Record<string, boolean>>({});
  let snapshotTransientErrors = $state<Record<string, boolean>>({});
  let healthStatuses = $state<Record<string, string>>({});

  function healthDotColor(status: string): string {
    switch (status) {
      case 'healthy':
      case 'recording':
        return 'var(--color-success)';
      case 'reconnecting':
        return 'var(--color-warning)';
      case 'error':
      case 'offline':
        return 'var(--color-danger)';
      default:
        return 'var(--color-text-tertiary)';
    }
  }

  function healthDotLabel(status: string): string {
    switch (status) {
      case 'healthy':
      case 'recording':
        return t('dashboard.healthOnline');
      case 'reconnecting':
        return t('health.status.reconnecting');
      case 'error':
      case 'offline':
        return t('dashboard.healthOffline');
      default:
        return t('health.status.unknown');
    }
  }
  let streamPlayURLs = $state<Record<string, Record<string, string>>>({});

  // Snapshot manager — handles fetch, interval, and cleanup lifecycle
  const snapshotMgr = createSnapshotManager({
    intervalMs: 3000,
    getCredentials,
    onUrlUpdate: (id, url) => { snapshotUrls[id] = url; },
    onUrlRevoke: (id) => {
      if (snapshotUrls[id]) { URL.revokeObjectURL(snapshotUrls[id]); delete snapshotUrls[id]; }
    },
    onLoadingChange: (id, val) => { snapshotLoading[id] = val; },
    onErrorChange: (id, val) => {
      if (val) { snapshotTransientErrors[id] = true; } else { delete snapshotTransientErrors[id]; }
    },
    onUnsupported: (id) => { /* tracked internally by manager */ },
  });

  // Protocol capabilities for capability-based checks
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));
  const STORAGE_KEY = 'dashboard-layout-v2';
  const LEGACY_STORAGE_KEY = 'dashboard-selected-cameras';

  // Default streaming protocol from settings
  let defaultProtocol = $state<string>('flv');

  // Lazy-loaded WasmPlayer component (only loads when 'wasm' protocol is selected)
  let WasmPlayerComponent = $state<any>(null);
  let wasmPlayerLoading = $state(false);
  let wasmPlayerError = $state('');

  // Lazy-loaded FMP4Player component (only loads when 'fmp4' protocol is selected)
  let FMP4PlayerComponent = $state<any>(null);
  let fmp4PlayerLoading = $state(false);

  async function loadWasmPlayer() {
    if (WasmPlayerComponent || wasmPlayerLoading) return;
    wasmPlayerLoading = true;
    wasmPlayerError = '';
    try {
      const mod = await import('../components/WasmPlayer.svelte');
      WasmPlayerComponent = mod.default;
    } catch (e) {
      console.error('Failed to load WasmPlayer:', e);
      wasmPlayerError = String(e);
      showToast(t('dashboard.wasmPlayerFailed'), 'error');
    } finally {
      wasmPlayerLoading = false;
    }
  }

  async function loadFMP4Player() {
    if (FMP4PlayerComponent || fmp4PlayerLoading) return;
    fmp4PlayerLoading = true;
    try {
      const mod = await import('../components/FMP4Player.svelte');
      FMP4PlayerComponent = mod.default;
    } catch (e) {
      console.error('Failed to load FMP4Player:', e);
    } finally {
      fmp4PlayerLoading = false;
    }
  }

  // Reconnection coordinator — limits concurrent reconnects, global exponential backoff,
  // and backend pressure detection (HTTP 503 triggers 10s global cooldown)
  const reconnectCoordinator = createReconnectCoordinator();
  setContext('reconnect-coordinator', reconnectCoordinator);

  function normalizeLayout(value: unknown): GridLayout {
    if (value === 1 || value === 4 || value === 9 || value === 16) return value;
    return 4;
  }

  function normalizeSlots(layout: GridLayout, slots: unknown): (string | null)[] {
    const result = Array<null>(layout).fill(null);
    if (!Array.isArray(slots)) return result;
    for (let i = 0; i < layout; i++) {
      const id = slots[i];
      result[i] = typeof id === 'string' && id ? id : null;
    }
    return result;
  }

  function loadLegacyCameraIds(): string[] {
    try {
      const raw = localStorage.getItem(LEGACY_STORAGE_KEY);
      if (raw) {
        const ids: string[] = JSON.parse(raw);
        if (Array.isArray(ids)) return ids.filter(id => typeof id === 'string');
      }
    } catch (e) { console.warn('Failed to load legacy camera IDs:', e); }
    return [];
  }

  function loadSavedLayout(): { layout: GridLayout; slots: (string | null)[] } {
    try {
      const raw = localStorage.getItem(STORAGE_KEY);
      if (raw) {
        const data = JSON.parse(raw);
        const layout = normalizeLayout(data?.layout);
        return { layout, slots: normalizeSlots(layout, data?.slots) };
      }
    } catch (e) { console.warn('Failed to load saved layout:', e); }

    const legacy = loadLegacyCameraIds();
    if (legacy.length > 0) {
      const layout: GridLayout = 4;
      const slots = normalizeSlots(layout, legacy);
      return { layout, slots };
    }

    return { layout: 4, slots: normalizeSlots(4, []) };
  }

  function saveLayout() {
    localStorage.setItem(STORAGE_KEY, JSON.stringify({
      layout: gridLayout,
      slots: slotAssignments,
    }));
  }

  function resizeSlots(slots: (string | null)[], layout: GridLayout): (string | null)[] {
    const next = normalizeSlots(layout, slots);
    return next;
  }

  function getAssignedIds(slots: (string | null)[]): string[] {
    return [...new Set(slots.filter((id): id is string => !!id))];
  }

  function setGridLayout(layout: GridLayout, persist = true) {
    gridLayout = layout;
    slotAssignments = resizeSlots(slotAssignments, layout);
    if (persist) {
      saveLayout();
      void loadCameraPlayURLs(getAssignedIds(slotAssignments));
    }
  }

  function openConfig(slotIndex: number | null = null) {
    editingSlotIndex = slotIndex;
    pendingGridLayout = gridLayout;
    pendingSlotAssignments = [...slotAssignments];
    configSearchQuery = '';
    configOpen = true;
  }

  function setPendingLayout(layout: GridLayout) {
    pendingGridLayout = layout;
    pendingSlotAssignments = resizeSlots(pendingSlotAssignments, layout);
  }

  function assignCameraToSlot(slotIndex: number, cameraId: string | null) {
    const next = [...pendingSlotAssignments];
    next[slotIndex] = cameraId;
    pendingSlotAssignments = next;
  }

  function applyLayoutConfig() {
    gridLayout = pendingGridLayout;
    slotAssignments = [...pendingSlotAssignments];
    saveLayout();
    void loadCameraPlayURLs(getAssignedIds(slotAssignments));
    configOpen = false;
    editingSlotIndex = null;
  }

  function filteredConfigCameras(): Camera[] {
    const q = configSearchQuery.trim().toLowerCase();
    if (!q) return allCameras;
    return allCameras.filter(cam =>
      (cam.name || '').toLowerCase().includes(q) ||
      cam.id.toLowerCase().includes(q) ||
      (cam.protocol || '').toLowerCase().includes(q)
    );
  }

  interface CameraProtocolDetail {
    Protocol: string;
    Available: boolean;
    Reason: string;
    PlayURL?: string;
    Backend?: string;
  }

  async function loadCameraPlayURLs(cameraIds: string[]) {
    const results = await Promise.all(cameraIds.map(async (cameraId) => {
      try {
        const response = await apiRequest<{ protocols: CameraProtocolDetail[] }>(`/cameras/${cameraId}/protocols?quality=sub`);
        const urls: Record<string, string> = {};
        for (const protocol of response.protocols) {
          if (protocol.Available && protocol.PlayURL) {
            urls[protocol.Protocol] = protocol.PlayURL;
          }
        }
        return [cameraId, urls] as const;
      } catch (e) {
        console.warn(`Failed to load play URLs for ${cameraId}:`, e);
        return [cameraId, {}] as const;
      }
    }));

    const next: Record<string, Record<string, string>> = {};
    for (const [cameraId, urls] of results) {
      next[cameraId] = urls;
    }
    streamPlayURLs = next;
  }

  function getStreamUrl(cameraId: string): string {
    const baseUrl = `/api/cameras/${cameraId}/stream/index.m3u8`;
    if (defaultProtocol === 'll-hls') {
      return `${baseUrl}?ll-hls=1`;
    }
    return baseUrl;
  }

  function getProtocolPlayURL(cameraId: string, protocol: 'flv' | 'ws-flv'): string {
    const cameraUrls = streamPlayURLs[cameraId] || {};
    if (cameraUrls[protocol]) return cameraUrls[protocol];
    if (protocol === 'flv') return `/api/cameras/${cameraId}/stream.flv`;
    return '';
  }

  function getGridClass(layout: GridLayout): string {
    return GRID_COLS_CLASS[layout];
  }

  function getCellClass(cameraId: string | null, slotIndex: number): string {
    if (!cameraId) return '';
    if (expandedCameraId) {
      return cameraId === expandedCameraId
        ? GRID_EXPAND_CLASS[gridLayout]
        : 'hidden';
    }
    // 6-screen: first slot spans 2x2
    if (gridLayout === 6 && slotIndex === 0) {
      return 'col-span-2 row-span-2';
    }
    return '';
  }

  function getCellMinHeight(layout: GridLayout): string {
    const rows = layout === 6 ? 3 : Math.sqrt(layout);
    return `calc((100vh - 168px) / ${rows})`;
  }

  function getStatusBadge(camera: Camera): { class: string; label: string; icon: any; text: string } {
    const status = camera.status?.toLowerCase() || '';
    if (status === 'recording' || status === 'active') {
      return { class: 'badge-success', label: '●', icon: CircleCheck, text: t('cameras.statusRecording') };
    }
    if (status === 'error' || status === 'failed') {
      return { class: 'badge-error', label: '●', icon: CircleAlert, text: t('cameras.statusError') };
    }
    if (status === 'reconnecting') {
      return { class: 'badge-warning', label: '●', icon: RefreshCw, text: t('cameras.statusReconnecting') };
    }
    if (status === 'offline') {
      return { class: 'badge-warning', label: '●', icon: WifiOff, text: t('cameras.statusOffline') || 'Offline' };
    }
    return { class: 'badge-neutral', label: '●', icon: CirclePause, text: t('cameras.statusStopped') };
  }

  function isHlsSupported(camera: Camera): boolean {
    return getProtocolCapabilities(camera.protocol, protocolsMap).hls;
  }

  type CameraMode = 'wasm' | 'fmp4' | 'webrtc' | 'flv' | 'ws-flv' | 'hls' | 'snapshot' | 'unsupported';

  function getCameraMode(camera: Camera): CameraMode {
    if (!isHlsSupported(camera)) {
      if (snapshotMgr.isUnsupported(camera.id)) return 'unsupported';
      return 'snapshot';
    }
    if (defaultProtocol === 'wasm') return 'wasm';
    if (defaultProtocol === 'fmp4') return 'fmp4';
    if (defaultProtocol === 'webrtc') return 'webrtc';
    if (defaultProtocol === 'flv') return 'flv';
    if (defaultProtocol === 'ws-flv') return 'ws-flv';
    // hls, ll-hls, or default
    return 'hls';
  }

  // Preload WasmPlayer when any assigned camera would use 'wasm' mode
  $effect(() => {
    if (defaultProtocol === 'wasm' && assignedCameraIds.some(id => {
      const cam = cameraById.get(id);
      return cam && isHlsSupported(cam);
    })) {
      loadWasmPlayer();
    }
  });

  // Preload FMP4Player when any assigned camera would use 'fmp4' mode
  $effect(() => {
    if (defaultProtocol === 'fmp4' && assignedCameraIds.some(id => {
      const cam = cameraById.get(id);
      return cam && isHlsSupported(cam);
    })) {
      loadFMP4Player();
    }
  });

  // --- Expand / shrink ---

  function expandToHls(cameraId: string) {
    expandedCameraId = cameraId;
  }

  function shrinkToGrid() {
    expandedCameraId = null;
  }

  function handleFullscreenChange() {
    if (!document.fullscreenElement) {
      shrinkToGrid();
    }
  }
  function handleCellClick(camera: Camera) {
    if (expandedCameraId === camera.id) {
      shrinkToGrid();
      return;
    }
    if (isHlsSupported(camera)) {
      expandToHls(camera.id);
    }
  }
  function handleCellDblClick(camera: Camera) {
    if (expandedCameraId === camera.id) {
      shrinkToGrid();
    }
  }


  function closePtz() {
    ptzOpenIndex = -1;
  }


  // --- Lifecycle ---

  onMount(async () => {
    try {
      const fetched = await getDashboardCameras();
      allCameras = fetched.filter(c => c.enabled !== false);
      const saved = loadSavedLayout();
      const availableIds = new Set(allCameras.map(c => c.id));
      gridLayout = saved.layout;
      slotAssignments = saved.slots.map(id => (id && availableIds.has(id) ? id : null));
      pendingGridLayout = gridLayout;
      pendingSlotAssignments = [...slotAssignments];
    } catch (e) {
      error = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
    await loadCameraPlayURLs(getAssignedIds(slotAssignments));
    // Fetch camera health status (public, no auth)
    try {
      const healthData = await getHealthCameras();
      const statuses: Record<string, string> = {};
      for (const [id, detail] of Object.entries(healthData)) {
        statuses[id] = detail.latest_status;
      }
      healthStatuses = statuses;
    } catch (e) {
      console.warn('Failed to load camera health status:', e);
    }
    // Load protocol capabilities
    try {
      const list = await listProtocols();
      if (list && list.length > 0) {
        protocolsMap = buildProtocolsMap(list);
      }
    } catch (e) {
      console.warn('Failed to load protocol capabilities:', e);
    }
    // Load default streaming protocol from settings
    try {
      const config = await getStreamingSettings();
      if (config.default_protocol) {
        defaultProtocol = config.default_protocol;
      }
    } catch (e) {
      console.warn('Failed to load streaming settings:', e);
    }
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    // Page Visibility API: pause players when tab hidden, resume when visible
    const visibilityHandler = () => {
      tabVisible = !document.hidden;
    };
    document.addEventListener('visibilitychange', visibilityHandler);

    // Intercept fetch to detect backend pressure (HTTP 503 → global cooldown)
    const originalFetch = window.fetch;
    window.fetch = async function (...args: Parameters<typeof fetch>): Promise<Response> {
      const response = await originalFetch.apply(this, args);
      if (response.status === 503) {
        reconnectCoordinator.reportBackendPressure();
      }
      return response;
    };


    return () => {
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
      document.removeEventListener('visibilitychange', visibilityHandler);
      window.fetch = originalFetch;
      reconnectCoordinator.dispose();
    };
  });

  let prevVisibleIds: Set<string> = new Set();

  // React to assigned camera changes — init/teardown snapshot cameras
  $effect(() => {
    const ids = assignedCameraIds;
    const _loading = loading;
    if (_loading) return;

    const visibleIds = new Set(ids);

    for (const id of prevVisibleIds) {
      if (!visibleIds.has(id)) {
        snapshotMgr.stopRefresh(id);
      }
    }

    for (const id of ids) {
      if (prevVisibleIds.has(id)) continue;
      const cam = cameraById.get(id);
      if (!cam) continue;
      if (getCameraMode(cam) === 'snapshot') {
        snapshotMgr.startRefresh(id);
      }
    }

    prevVisibleIds = visibleIds;

    return () => {
      snapshotMgr.stopAll();
    };
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="mx-auto px-3 sm:px-4 lg:px-6 py-4 sm:py-6" style="max-width: 100%;">

    <!-- Header -->
    <div class="flex flex-wrap items-center justify-between gap-3 mb-4 sm:mb-6">
      <h1 class="text-lg sm:text-xl font-bold th-text-primary flex items-center gap-2">
        <Video size={20} class="text-accent" />
        {t('dashboard.title')}
      </h1>
      <div class="flex items-center gap-2">
        <div class="flex items-center gap-1 p-1 rounded-lg th-bg-tertiary border th-border" role="group" aria-label={t('dashboard.gridLayout')}>
          {#each LAYOUT_OPTIONS as layout}
            <button
              class="layout-btn px-2.5 py-1.5 text-xs font-medium rounded-md transition-colors {gridLayout === layout ? 'layout-btn-active' : 'th-text-secondary hover:th-text-primary'}"
              onclick={() => setGridLayout(layout)}
              title={t(`dashboard.layout${layout}`)}
            >
              {layout}
            </button>
          {/each}
        </div>
        <button
          class="btn btn-ghost p-2"
          onclick={() => configOpen ? (configOpen = false) : openConfig(null)}
          title={t('dashboard.configure')}
        >
          <Settings size={18} />
        </button>
      </div>
    </div>

    <!-- Layout configuration panel -->
    {#if configOpen}
      <div class="card p-4 mb-4">
        <div class="flex flex-wrap items-center justify-between gap-3 mb-4">
          <div>
            <h3 class="text-sm font-semibold th-text-primary">{t('dashboard.configureLayout')}</h3>
            <p class="text-xs th-text-secondary mt-1">{t('dashboard.configureLayoutHint')}</p>
          </div>
          <div class="flex items-center gap-1 p-1 rounded-lg th-bg-tertiary border th-border">
            {#each LAYOUT_OPTIONS as layout}
              <button
                class="layout-btn px-2.5 py-1.5 text-xs font-medium rounded-md transition-colors {pendingGridLayout === layout ? 'layout-btn-active' : 'th-text-secondary hover:th-text-primary'}"
                onclick={() => setPendingLayout(layout)}
              >
                {layout}
              </button>
            {/each}
          </div>
        </div>

        <div class="grid gap-2 mb-4 {GRID_COLS_CLASS[pendingGridLayout]}">
          {#each pendingSlotAssignments as cameraId, slotIndex}
            {@const assigned = cameraId ? cameraById.get(cameraId) : null}
            <button
              type="button"
              class="slot-config rounded-lg border th-border p-2 min-h-[72px] text-left w-full {editingSlotIndex === slotIndex ? 'slot-config-active' : ''} {pendingGridLayout === 6 && slotIndex === 0 ? 'col-span-2 row-span-2' : ''}"
              onclick={() => { editingSlotIndex = slotIndex; }}
            >
              <div class="flex items-center justify-between gap-2 mb-1.5">
                <span class="text-[10px] font-medium th-text-muted uppercase tracking-wide">
                  {t('dashboard.slot', { index: slotIndex + 1 })}
                </span>
                {#if cameraId}
                  <span
                    role="button"
                    tabindex="0"
                    class="text-[10px] th-text-muted hover:th-color-danger"
                    onclick={(e) => { e.stopPropagation(); assignCameraToSlot(slotIndex, null); }}
                    onkeydown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); assignCameraToSlot(slotIndex, null); } }}
                  >
                    {t('dashboard.clearSlot')}
                  </span>
                {/if}
              </div>
              {#if assigned}
                <p class="text-sm th-text-primary truncate font-medium">{assigned.name || assigned.id}</p>
                <p class="text-[10px] th-text-muted font-mono truncate">{assigned.id}</p>
              {:else}
                <p class="text-xs th-text-muted">{t('dashboard.emptySlot')}</p>
              {/if}
            </button>
          {/each}
        </div>

        <div class="relative mb-3">
          <Search size={14} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-muted" />
          <input
            type="search"
            class="input w-full pl-9 text-sm"
            placeholder={t('dashboard.searchCameras')}
            bind:value={configSearchQuery}
          />
        </div>

        <div class="space-y-1 max-h-52 overflow-y-auto mb-4 border th-border rounded-lg p-1">
          {#if allCameras.length === 0}
            <p class="text-sm th-text-muted text-center py-6">{t('dashboard.noCamerasAvailable')}</p>
          {:else if filteredConfigCameras().length === 0}
            <p class="text-sm th-text-muted text-center py-6">{t('dashboard.noSearchResults')}</p>
          {:else}
            {#each filteredConfigCameras() as camera}
              <button
                class="w-full flex items-center gap-2 px-2 py-2 rounded-md hover:bg-[var(--bg-tertiary)] transition-colors text-left"
                onclick={() => {
                  const target = editingSlotIndex ?? pendingSlotAssignments.findIndex(id => !id);
                  const slotIndex = target >= 0 ? target : 0;
                  assignCameraToSlot(slotIndex, camera.id);
                  editingSlotIndex = slotIndex;
                }}
              >
                <LayoutGrid size={14} class="th-text-muted shrink-0" />
                <span class="text-sm th-text-primary truncate">{camera.name || camera.id}</span>
                <span class="text-xs th-text-muted ml-auto shrink-0">{camera.protocol}</span>
              </button>
            {/each}
          {/if}
        </div>

        <div class="flex justify-end gap-2">
          <button class="btn btn-ghost text-sm px-3 py-1.5" onclick={() => { configOpen = false; editingSlotIndex = null; }}>
            {t('common.dismiss')}
          </button>
          <button class="btn btn-primary text-sm px-3 py-1.5" onclick={applyLayoutConfig}>
            {t('dashboard.apply')}
          </button>
        </div>
      </div>
    {/if}

    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="flex flex-col items-center gap-3">
          <div class="spinner spinner-lg"></div>
          <span class="text-sm th-text-secondary">{t('common.loading')}</span>
        </div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
      </div>
    {:else if allCameras.length === 0}
      <div class="card p-8 sm:p-12 text-center">
        <div class="th-text-muted mb-4 flex justify-center"><VideoOff size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('dashboard.noCameras')}</h3>
        <p class="th-text-secondary text-sm">{t('dashboard.noCamerasHint')}</p>
      </div>
    {:else}
      {#if !hasAnyAssignment}
        <div class="card px-4 py-3 mb-3 flex flex-wrap items-center justify-between gap-3">
          <p class="text-sm th-text-secondary">{t('dashboard.noAssignmentsHint')}</p>
          <button class="btn btn-primary text-sm px-3 py-1.5" onclick={() => openConfig(0)}>
            {t('dashboard.configureNow')}
          </button>
        </div>
      {/if}

      <div
        class="grid gap-2 sm:gap-3 {getGridClass(gridLayout)}"
        onexpand={(e: CustomEvent) => expandToHls(e.detail.cameraId)}
        onshrink={(e: CustomEvent) => shrinkToGrid()}
      >
        {#each slotAssignments as cameraId, slotIndex}
          {@const camera = cameraId ? cameraById.get(cameraId) : null}
          {#if camera}
            {@const status = getStatusBadge(camera)}
            {@const mode = getCameraMode(camera)}
            {@const StatusIcon = status.icon}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="relative bg-black rounded-lg overflow-hidden group camera-grid-cell {getCellClass(cameraId, slotIndex)}"
              class:cell-expanded={expandedCameraId === camera.id}
              style="min-height: {getCellMinHeight(gridLayout)};"
              role="button"
              tabindex="0"
              aria-label="{camera.name || camera.id} — {status.text}"
              onclick={() => handleCellClick(camera)}
              onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleCellClick(camera); } }}
              ondblclick={() => handleCellDblClick(camera)}
            >
            {#if mode === 'snapshot'}
              <!-- Snapshot thumbnail mode (HTTP_JPEG cameras) -->
              {#if snapshotLoading[camera.id] && !snapshotUrls[camera.id]}
                <!-- Initial loading -->
                <div class="absolute inset-0 flex items-center justify-center bg-black/40">
                  <div class="flex flex-col items-center gap-2">
                    <Loader2 size={24} class="text-white animate-spin" />
                    <span class="text-white/70 text-xs">{t('common.loading')}</span>
                  </div>
                </div>
              {:else if snapshotUrls[camera.id]}
                <!-- Snapshot image -->
                <img
                  src={snapshotUrls[camera.id]}
                  alt={camera.name || camera.id}
                  class="w-full h-full object-contain"
                />
                <!-- Transient error overlay (keeps last good image visible) -->
                {#if snapshotTransientErrors[camera.id]}
                  <div class="absolute inset-0 bg-black/30 flex items-center justify-center pointer-events-none">
                    <span class="text-white/50 text-xs">{t('dashboard.snapshotError')}</span>
                  </div>
                {/if}
              {:else if snapshotTransientErrors[camera.id]}
                <!-- Error with no previous image -->
                <div class="absolute inset-0 flex items-center justify-center">
                  <div class="flex flex-col items-center gap-2">
                    <ImageOff size={24} class="text-white/40" />
                    <span class="text-white/50 text-xs">{t('dashboard.snapshotError')}</span>
                  </div>
                </div>
              {/if}

              <!-- Camera name + status overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge {status.class} text-[10px] px-1.5 py-0.5 flex items-center gap-1">

                    <StatusIcon size={10} />
                    {status.text}
                  </span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>

            {:else if mode === 'hls'}
              <VideoPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                streamUrl={getStreamUrl(camera.id)}
                cameraProtocol={camera.protocol}
                protocol={defaultProtocol}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
              />

            {:else if mode === 'webrtc'}
              <WebRTCPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
              />

            {:else if mode === 'flv' || mode === 'ws-flv'}
              <FlvPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                streamUrl={getProtocolPlayURL(camera.id, mode === 'ws-flv' ? 'ws-flv' : 'flv')}
                protocol={mode === 'ws-flv' ? 'ws-flv' : 'flv'}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
              />
            {:else if mode === 'wasm'}
              {#if WasmPlayerComponent}
                {@const WasmPlayer = WasmPlayerComponent}
                <WasmPlayer
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  expanded={expandedCameraId === camera.id}
                  tabVisible={tabVisible}
                />
              {:else if wasmPlayerLoading}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">{t('dashboard.loadingWasmPlayer')}</span>
                  </div>
                </div>
              {:else}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <AlertCircle size={20} class="text-red-400/60" />
                    <span class="text-white/50 text-xs">{t('dashboard.wasmPlayerLoadError')}</span>
                    <button class="text-xs text-white/40 underline" onclick={loadWasmPlayer}>{t('live.retry') || 'Retry'}</button>
                  </div>
                </div>
              {/if}

            {:else if mode === 'fmp4'}
              {#if FMP4PlayerComponent}
                {@const FMP4Player = FMP4PlayerComponent}
                <FMP4Player
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  expanded={expandedCameraId === camera.id}
                  tabVisible={tabVisible}
                />
              {:else if fmp4PlayerLoading}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">Loading fMP4 player...</span>
                  </div>
                </div>
              {:else}
                <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                  <div class="flex flex-col items-center gap-2">
                    <AlertCircle size={20} class="text-red-400/60" />
                    <span class="text-white/50 text-xs">Failed to load fMP4 player</span>
                    <button class="text-xs text-white/40 underline" onclick={loadFMP4Player}>Retry</button>
                  </div>
                </div>
              {/if}

            {:else}
              <!-- Unsupported protocol (no snapshot, no HLS) -->
              <div class="absolute inset-0 flex items-center justify-center">
                <div class="flex flex-col items-center gap-2 text-center px-4">
                  <VideoOff size={24} class="text-white/40" />
                  <span class="text-white/50 text-xs">{t('live.notSupported')}</span>
                  <span class="text-white/30 text-[10px] font-mono">{camera.protocol}</span>
                </div>
              </div>
              <!-- Camera name overlay -->
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="badge badge-neutral text-[10px] px-1.5 py-0.5 flex items-center gap-1">
                    <CirclePause size={10} />
                    {t('live.notSupported')}
                  </span>
                  <span class="text-white text-sm font-medium truncate">{camera.name || camera.id}</span>
                </div>
              </div>
            {/if}

            <!-- Streaming protocol badge -->
            {#if mode !== 'unsupported'}
              {@const protocolLabel = mode === 'wasm' ? 'WebCodecs' : mode === 'fmp4' ? 'fMP4' : mode === 'webrtc' ? 'WebRTC' : mode === 'flv' ? 'HTTP-FLV' : mode === 'ws-flv' ? 'WS-FLV' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'LL-HLS' : 'HLS') : 'JPEG'}
              {@const protocolColor = mode === 'wasm' ? 'bg-cyan-500/60' : mode === 'fmp4' ? 'bg-teal-500/60' : mode === 'webrtc' ? 'bg-green-500/60' : mode === 'flv' ? 'bg-orange-500/60' : mode === 'ws-flv' ? 'bg-amber-500/60' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'bg-purple-500/60' : 'bg-blue-500/60') : 'bg-gray-500/60'}
              <span class="absolute top-2 right-2 z-10 {protocolColor} text-white text-[10px] font-medium px-2 py-0.5 rounded-full pointer-events-none select-none">
                {protocolLabel}
              </span>
            {/if}

            <!-- Health indicator dot + status -->
            {#if healthStatuses[camera.id] !== undefined}
              {@const hStatus = healthStatuses[camera.id]}
              <span
                class="absolute top-2 left-2 z-10 flex items-center gap-1 bg-black/60 text-white text-[10px] font-medium px-1.5 py-0.5 rounded-full select-none"
                title={healthDotLabel(hStatus)}
              >
                <span class="w-2 h-2 rounded-full flex-shrink-0" style="background-color: {healthDotColor(hStatus)}"></span>
                {healthDotLabel(hStatus)}
              </span>
            {/if}

            <!-- PTZ Overlay for PTZ-capable cameras -->
            {#if ptzOpenIndex === slotIndex && getProtocolCapabilities(camera.protocol, protocolsMap).ptz}
              <div
                class="absolute top-2 left-2 z-10"
                onclick={(e: MouseEvent) => { e.stopPropagation(); }}
              >
                <div class="relative">
                  <button
                    class="absolute -top-1.5 -right-1.5 z-20 p-0.5 rounded-full bg-black/70 text-white/80 hover:text-white hover:bg-black/90 transition-all"
                    onclick={(e: MouseEvent) => { e.stopPropagation(); closePtz(); }}
                    aria-label={t('common.close')}
                  >
                    <X size={12} />
                  </button>
                  <PtzControl cameraId={camera.id} enabled={true} />
                </div>
              </div>
            {/if}
            </div>
          {:else}
            <!-- Empty slot placeholder -->
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <button
              type="button"
              class="empty-slot rounded-lg border-2 border-dashed th-border flex flex-col items-center justify-center gap-2 transition-colors hover:border-[var(--color-primary)] hover:bg-[var(--bg-tertiary)]"
              style="min-height: {getCellMinHeight(gridLayout)};"
              onclick={() => openConfig(slotIndex)}
              aria-label={t('dashboard.assignSlot', { index: slotIndex + 1 })}
            >
              <Plus size={20} class="th-text-muted" />
              <span class="text-xs th-text-muted">{t('dashboard.assignSlot', { index: slotIndex + 1 })}</span>
            </button>
          {/if}
        {/each}
      </div>
    {/if}
  </main>
</div>

<style>
  /* Grid cell expand/shrink transitions */
  .camera-grid-cell {
    transition: opacity var(--duration-normal) var(--ease-out),
                transform var(--duration-normal) var(--ease-out);
  }

  /* Subtle hover lift on grid cells */
  .camera-grid-cell:not(.hidden):hover {
    opacity: 0.92;
  }

  /* Fade-in + scale-up when a cell expands */
  .cell-expanded {
    animation: cell-expand var(--duration-normal) var(--ease-out);
  }

  @keyframes cell-expand {
    from {
      opacity: 0.3;
      transform: scale(0.96);
    }
    to {
      opacity: 1;
      transform: scale(1);
    }
  }

  .layout-btn-active {
    background: var(--color-primary);
    color: white;
  }

  .slot-config-active {
    border-color: var(--color-primary);
    box-shadow: 0 0 0 1px var(--color-primary);
  }

  .empty-slot {
    background: var(--bg-secondary);
  }

  /* Responsive grid adjustments */
  @media (max-width: 767px) {
    :global(.grid-cols-2),
    :global(.grid-cols-3),
    :global(.grid-cols-4) {
      grid-template-columns: 1fr !important;
    }
    
    :global(.col-span-2),
    :global(.col-span-3),
    :global(.col-span-4) {
      grid-column: span 1 !important;
    }
    
    :global(.row-span-2),
    :global(.row-span-3),
    :global(.row-span-4) {
      grid-row: span 1 !important;
    }
  }

  @media (min-width: 768px) and (max-width: 1023px) {
    :global(.grid-cols-3) {
      grid-template-columns: repeat(2, 1fr) !important;
    }
    
    :global(.grid-cols-4) {
      grid-template-columns: repeat(2, 1fr) !important;
    }
    
    :global(.col-span-3),
    :global(.col-span-4) {
      grid-column: span 2 !important;
    }
    
    :global(.row-span-3),
    :global(.row-span-4) {
      grid-row: span 2 !important;
    }
  }
</style>
