<script lang="ts">
  import { onDestroy, onMount, setContext } from 'svelte';
  import {
    getDashboardCameras,
    getCredentials,
    listProtocols,
    DEFAULT_PROTOCOLS,
    buildProtocolsMap,
    normalizeProtocol,
    getProtocolCapabilities,
    apiRequest,
    subscribeNvrEvents,
    streamMediaURL,
    listStreams,
    playGB28181Stream,
  } from '$lib/api';  import type { NvrEvent } from '$lib/api';
  import type { Camera, ProtocolInfo, StreamInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { Loader2, AlertCircle, Video, VideoOff, X, Settings, ImageOff, CircleCheck, CirclePause, WifiOff, LayoutGrid, Plus, Search, Play } from 'lucide-svelte';
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
  let allStreams = $state<StreamInfo[]>([]);
  let refreshingStreams = false;
  // Status polling must not tear down a healthy player on one transient
  // inactive response. Two consecutive polls (20s) are required to mark an
  // already-online stream offline.
  const offlinePollCounts = new Map<string, number>();
  let streamStatusTimer: number | undefined;
  let dashboardDisposed = false;
  onDestroy(() => {
    dashboardDisposed = true;

    if (streamStatusTimer !== undefined) {
      window.clearInterval(streamStatusTimer);
    }

  });

  interface WallSource {
    id: string;
    name: string;
    kind: 'camera' | 'stream';
    camera?: Camera;
    stream?: StreamInfo;
  }

  function buildWallSources(cameras: Camera[], streams: StreamInfo[]): WallSource[] {
    const covered = new Set<string>();
    const cameraIds = new Set(cameras.map((cam) => cam.id));
    for (const cam of cameras) {
      covered.add(cam.id);
      if (cam.stream_id) covered.add(cam.stream_id);
    }
    const items: WallSource[] = cameras.map((cam) => ({
      id: cam.id,
      name: cam.name || cam.id,
      kind: 'camera',
      camera: cam,
    }));
    const seen = new Set<string>();
    for (const stream of streams) {
      if (!stream.stream_id || covered.has(stream.stream_id) || seen.has(stream.stream_id)) continue;
      if (stream.camera_id && cameraIds.has(stream.camera_id)) continue;
      seen.add(stream.stream_id);
      items.push({
        id: stream.stream_id,
        name: stream.name || stream.camera_name || stream.stream_id,
        kind: 'stream',
        stream,
      });
    }
    return items;
  }

  let wallSources = $derived(buildWallSources(allCameras, allStreams));
  let sourceById = $derived(new Map(wallSources.map((source) => [source.id, source])));
  let streamById = $derived(new Map(
    wallSources.filter((source) => source.kind === 'stream' && source.stream).map((source) => [source.id, source.stream!]),
  ));
  let configOpen = $state(false);
  let applyingLayout = $state(false);
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

  type GBPlayState = 'starting' | 'ready' | 'error';

  let gbPlayStates = $state<Record<string, GBPlayState>>({});
  let gbPlayErrors = $state<Record<string, string>>({});

  // The GB28181 signaling request is independent of WebRTC playback.
  // WebRTCPlayer itself handles WHEP startup and reconnection; do not block
  // its creation on sampled FPS/Publisher statistics from EnsurePlay.
  const gbPlayInFlight = new Set<string>();

  function isGB28181WallCamera(camera: Camera): boolean {
    return normalizeProtocol(camera.protocol || '') === 'gb28181';
  }

  function getGB28181Ids(camera: Camera): {
    deviceId: string;
    channelId: string;
  } | null {
    if (!isGB28181WallCamera(camera)) return null;

    const parts = camera.id.split(':');
    if (parts.length !== 2 || !parts[0] || !parts[1]) return null;

    return {
      deviceId: parts[0],
      channelId: parts[1],
    };
  }

  async function ensureDashboardGBPlay(camera: Camera): Promise<void> {
    const ids = getGB28181Ids(camera);
    if (!ids || dashboardDisposed) return;

    const cameraId = camera.id;
    if (gbPlayInFlight.has(cameraId)) return;
    if (!slotAssignments.includes(cameraId)) return;

    gbPlayInFlight.add(cameraId);
    gbPlayStates[cameraId] = 'starting';
    delete gbPlayErrors[cameraId];

    try {
      // EnsurePlay creates/reuses a SIP/RTP session. Its "starting" result
      // does not mean WHEP must wait: WebRTCPlayer probes the real endpoint
      // and retries on initial 404/connection failures by itself.
      await playGB28181Stream({
        device_id: ids.deviceId,
        channel_id: ids.channelId,
        stream_id: streamIdFor(cameraId),
        ensure: true,
      });

      if (!dashboardDisposed && slotAssignments.includes(cameraId)) {
        gbPlayStates[cameraId] = 'ready';
      }
    } catch (err) {
      if (!dashboardDisposed && slotAssignments.includes(cameraId)) {
        // Do not unmount an already-working WHEP player because of a SIP or
        // media-stat API error. Display a non-blocking retry indication.
        gbPlayStates[cameraId] = 'error';
        gbPlayErrors[cameraId] = err instanceof Error ? err.message : String(err);
      }
    } finally {
      gbPlayInFlight.delete(cameraId);
    }
  }

  function retryDashboardGBPlay(camera: Camera): void {
    if (gbPlayInFlight.has(camera.id)) return;
    delete gbPlayErrors[camera.id];
    gbPlayStates[camera.id] = 'starting';
    void ensureDashboardGBPlay(camera);
  }

  // One EnsurePlay per assigned camera on Dashboard mount/assignment.
  // Status polling must NOT repeatedly restart the underlying SIP/RTP stream.
  $effect(() => {
    const ids = assignedCameraIds;
    const cameras = cameraById;
    for (const cameraId of ids) {
      const camera = cameras.get(cameraId);
      if (!camera || !isGB28181WallCamera(camera)) continue;
      if (!gbPlayStates[cameraId] && !gbPlayInFlight.has(cameraId)) {
        void ensureDashboardGBPlay(camera);
      }
    }
  });

  // Snapshot state
  let snapshotUrls = $state<Record<string, string>>({});
  let snapshotLoading = $state<Record<string, boolean>>({});
  let snapshotTransientErrors = $state<Record<string, boolean>>({});
  let alarmFlash = $state<Record<string, number>>({});
  let tourOn = $state(false);
  let tourIndex = $state(0);
  let instantReplayId = $state<string | null>(null);

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

  async function applyLayoutConfig() {
    applyingLayout = true;
    try {
      const nextAssignments = [...pendingSlotAssignments];
      await loadCameraPlayURLs(getAssignedIds(nextAssignments));
      gridLayout = pendingGridLayout;
      slotAssignments = nextAssignments;
      saveLayout();
      configOpen = false;
      editingSlotIndex = null;
    } finally {
      applyingLayout = false;
    }
  }

  function wallSourceLabel(source: WallSource): string {
    if (source.kind === 'camera') return source.camera?.protocol || '';
    switch (source.stream?.source_type) {
      case 'camera':
        return t('streams.sourceCamera');
      case 'voip':
        return t('streams.sourceVoIP');
      case 'iptv':
        return t('streams.sourceIPTV');
      case 'emby':
        return t('streams.sourceEmby');
      case 'rtmp_push':
        return t('streams.sourceRTMPPush');
      case 'srt_push':
        return t('streams.sourceSRTPush');
      case 'whip_push':
        return t('streams.sourceWHIPPush');
      case 'relay_pull':
        return t('streams.sourceRelayPull');
      case 'push':
        return t('streams.sourcePush');
      default:
        return t('streams.sourceStream');
    }
  }

  function filteredConfigSources(): WallSource[] {
    const q = configSearchQuery.trim().toLowerCase();
    if (!q) return wallSources;
    return wallSources.filter((source) =>
      source.name.toLowerCase().includes(q) ||
      source.id.toLowerCase().includes(q) ||
      wallSourceLabel(source).toLowerCase().includes(q)
    );
  }

  async function loadWallStreams(): Promise<StreamInfo[]> {
    const pageSize = 100;
    const first = await listStreams({ limit: pageSize, offset: 0 });
    const total = Math.min(first.total, 500);
    const offsets = Array.from(
      { length: Math.ceil((total - pageSize) / pageSize) },
      (_, index) => (index + 1) * pageSize,
    );
    const remaining = await Promise.all(offsets.map((offset) => listStreams({ limit: pageSize, offset })));
    return [...first.streams, ...remaining.flatMap((page) => page.streams)];
  }

  async function refreshWallStreams() {
    if (refreshingStreams) return;
    refreshingStreams = true;
    try {
      const refreshed = await loadWallStreams();
      const currentById = new Map(allStreams.map((stream) => [stream.stream_id, stream]));
      const refreshedIDs = new Set(refreshed.map((stream) => stream.stream_id));
      const listChanged = refreshedIDs.size !== currentById.size ||
        [...refreshedIDs].some((id) => !currentById.has(id));
      const nextStreams = refreshed.map((incoming) => {
        const current = currentById.get(incoming.stream_id);
        if (!current) return incoming;

        let nextActive = current.active;
        if (incoming.active) {
          offlinePollCounts.delete(incoming.stream_id);
          nextActive = true;
        } else {
          const offlinePolls = (offlinePollCounts.get(incoming.stream_id) ?? 0) + 1;
          offlinePollCounts.set(incoming.stream_id, offlinePolls);
          // Preserve the last known online state for one transient poll.
          nextActive = current.active && offlinePolls < 2;
        }

        // Keep each existing row and its playback URLs stable. Replacing every
        // object here invalidates the dashboard's derived wall sources on
        // every poll and can make player components reconnect unnecessarily.
        if (current.active !== nextActive) current.active = nextActive;
        return current;
      });

      // Only replace the collection when streams were actually added/removed.
      // For a normal status poll, changing `active` above is the only reactive
      // update, so live players with an unchanged status remain untouched.
      if (listChanged) allStreams = nextStreams;
    } catch (e) {
      console.warn('Failed to refresh stream status for the dashboard:', e);
    } finally {
      refreshingStreams = false;
    }
  }

  function streamForCamera(camera: Camera): StreamInfo | undefined {
    return allStreams.find((stream) =>
      stream.stream_id === camera.stream_id ||
      stream.stream_id === camera.id ||
      stream.camera_id === camera.id
    );
  }

  function wallSourceStatus(source: WallSource): { label: string; badgeClass: string } {
    const stream = source.stream || (source.camera ? streamForCamera(source.camera) : undefined);
    if (!stream) return { label: t('health.status.unknown'), badgeClass: 'badge-neutral' };
    return stream.active
      ? { label: t('dashboard.healthOnline'), badgeClass: 'badge-success' }
      : { label: t('dashboard.streamOffline'), badgeClass: 'badge-warning' };
  }

  interface CameraProtocolDetail {
    Protocol: string;
    Available: boolean;
    Reason: string;
    PlayURL?: string;
    Backend?: string;
  }

  async function loadCameraPlayURLs(ids: string[], knownCameraIds?: ReadonlySet<string>) {
    const cameraIds = ids.filter((id) => knownCameraIds ? knownCameraIds.has(id) : cameraById.has(id));
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

  // Camera-scoped routes still work, but prefer the bound stream when known.
  function streamIdFor(cameraId: string): string {
    const cam = allCameras.find((c) => c.id === cameraId);
    return cam?.stream_id || cameraId;
  }

  function getStreamUrl(cameraId: string): string {
    const protocol = defaultProtocol === 'll-hls' ? 'll-hls' : 'hls';
    const fallback = protocol === 'll-hls'
      ? streamMediaURL(streamIdFor(cameraId), 'll-hls')
      : streamMediaURL(streamIdFor(cameraId), 'hls');
    return getCameraPlayURL(cameraId, protocol, fallback);
  }

  function getCameraPlayURL(cameraId: string, protocol: string, fallback = ''): string {
    const cameraUrls = streamPlayURLs[cameraId] || {};
    return cameraUrls[protocol] || fallback;
  }

  function getStreamPlayURL(stream: StreamInfo, protocol: string, fallback: string): string {
    return stream.play_urls?.find((play) => play.protocol === protocol)?.url || fallback;
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

  function getStreamStatusBadge(stream: StreamInfo | undefined): { class: string; icon: any; text: string } {
    if (!stream) return { class: 'badge-neutral', icon: CirclePause, text: t('health.status.unknown') };
    if (stream.active) return { class: 'badge-success', icon: CircleCheck, text: t('dashboard.healthOnline') };
    return { class: 'badge-warning', icon: WifiOff, text: t('dashboard.streamOffline') };
  }

  function isHlsSupported(camera: Camera): boolean {
    return getProtocolCapabilities(camera.protocol, protocolsMap).hls;
  }

  type CameraMode = 'wasm' | 'fmp4' | 'webrtc' | 'flv' | 'ws-flv' | 'hls' | 'snapshot' | 'unsupported';

  function videoMode(): CameraMode {
    if (defaultProtocol === 'wasm') return 'wasm';
    if (defaultProtocol === 'fmp4') return 'fmp4';
    if (defaultProtocol === 'webrtc') return 'webrtc';
    if (defaultProtocol === 'flv') return 'flv';
    if (defaultProtocol === 'ws-flv') return 'ws-flv';
    return 'hls';
  }

  function getCameraMode(camera: Camera): CameraMode {
    if (!isHlsSupported(camera)) {
      if (snapshotMgr.isUnsupported(camera.id)) return 'unsupported';
      return 'snapshot';
    }
    return videoMode();
  }

  function assignedUsesVideo(id: string): boolean {
    const cam = cameraById.get(id);
    if (cam) return isHlsSupported(cam);
    return streamById.has(id);
  }

  function streamPageURL(stream: StreamInfo, mode: CameraMode): string {
    const streamId = stream.stream_id;
    if (mode === 'flv') return getStreamPlayURL(stream, 'flv', streamMediaURL(streamId, 'flv'));
    if (mode === 'ws-flv') {
      return getStreamPlayURL(stream, 'ws-flv', '');
    }
    if (mode === 'wasm') return getStreamPlayURL(stream, 'wasm', streamMediaURL(streamId, 'ws'));
    if (mode === 'webrtc') return getStreamPlayURL(stream, 'webrtc', streamMediaURL(streamId, 'webrtc'));
    if (mode === 'fmp4') return getStreamPlayURL(stream, 'fmp4', streamMediaURL(streamId, 'fmp4'));
    const protocol = defaultProtocol === 'll-hls' ? 'll-hls' : 'hls';
    const fallback = protocol === 'll-hls'
      ? streamMediaURL(streamId, 'll-hls')
      : streamMediaURL(streamId, 'hls');
    return getStreamPlayURL(stream, protocol, fallback);
  }

  // Preload WasmPlayer when any assigned cell would use 'wasm' mode
  $effect(() => {
    if (defaultProtocol === 'wasm' && assignedCameraIds.some(assignedUsesVideo)) {
      loadWasmPlayer();
    }
  });

  // Preload FMP4Player when any assigned cell would use 'fmp4' mode
  $effect(() => {
    if (defaultProtocol === 'fmp4' && assignedCameraIds.some(assignedUsesVideo)) {
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
  function handleStreamClick(streamId: string) {
    if (expandedCameraId === streamId) {
      shrinkToGrid();
      return;
    }
    expandToHls(streamId);
  }
  function handleCellDblClick(camera: Camera) {
    if (expandedCameraId === camera.id) {
      shrinkToGrid();
    }
  }


  function closePtz() {
    ptzOpenIndex = -1;
  }

  function toggleTour() {
    if (tourOn) {
      tourOn = false;
      tourIndex = 0;
      shrinkToGrid();
      return;
    }
    if (assignedCameraIds.length < 2) {
      showToast(t('dashboard.tourNeedsTwoSources'), 'info');
      return;
    }
    tourIndex = 0;
    expandedCameraId = assignedCameraIds[0];
    tourOn = true;
  }

  $effect(() => {
    if (!tourOn) return;
    const ids = assignedCameraIds;
    if (ids.length < 2) {
      tourOn = false;
      shrinkToGrid();
      return;
    }
    const timer = window.setInterval(() => {
      tourIndex = (tourIndex + 1) % ids.length;
      expandedCameraId = ids[tourIndex];
    }, 8000);
    return () => window.clearInterval(timer);
  });

  function startInstantReplay(cameraId: string) {
    instantReplayId = cameraId;
  }

  function instantReplayUrl(cameraId: string): string {
    const end = new Date();
    const start = new Date(end.getTime() - 30 * 1000);
    return `/api/cameras/${encodeURIComponent(cameraId)}/playback/playlist.m3u8?start=${encodeURIComponent(start.toISOString())}&end=${encodeURIComponent(end.toISOString())}`;
  }


  // --- Lifecycle ---

  onMount(async () => {
    let cameras: Camera[] = [];
    let streams: StreamInfo[] = [];
    let cameraErr = '';
    let streamErr = '';
    const saved = loadSavedLayout();
    const camerasPromise = getDashboardCameras();
    // Protocol URL discovery is non-critical and can be slow for large walls.
    // It must not hold up first render or the camera-scoped WebRTC proxy.
    void camerasPromise.then((cameraList) => loadCameraPlayURLs(
      getAssignedIds(saved.slots),
      new Set(cameraList.filter((camera) => camera.enabled !== false).map((camera) => camera.id)),
    )).catch((reason) => {
      console.warn('Failed to load camera playback URLs:', reason);
    });
    const [cameraResult, streamResult, protocolResult, settingsResult] = await Promise.allSettled([
      camerasPromise,
      loadWallStreams(),
      listProtocols(),
      getStreamingSettings(),
    ]);
    if (cameraResult.status === 'fulfilled') {
      cameras = cameraResult.value.filter(c => c.enabled !== false);
    } else {
      cameraErr = cameraResult.reason instanceof Error ? cameraResult.reason.message : String(cameraResult.reason);
    }
    if (streamResult.status === 'fulfilled') {
      streams = streamResult.value;
    } else {
      streamErr = streamResult.reason instanceof Error ? streamResult.reason.message : String(streamResult.reason);
      console.warn('Failed to load streams for the dashboard:', streamResult.reason);
    }
    if (protocolResult.status === 'fulfilled' && protocolResult.value?.length) {
      protocolsMap = buildProtocolsMap(protocolResult.value);
    } else if (protocolResult.status === 'rejected') {
      console.warn('Failed to load protocol capabilities:', protocolResult.reason);
    }
    if (settingsResult.status === 'fulfilled' && settingsResult.value.default_protocol) {
      defaultProtocol = settingsResult.value.default_protocol;
    } else if (settingsResult.status === 'rejected') {
      console.warn('Failed to load streaming settings:', settingsResult.reason);
    }
    if (cameraErr && streamErr) {
      error = cameraErr;
    } else if (cameraErr) {
      console.warn('Failed to load cameras for the dashboard:', cameraErr);
    }
    allCameras = cameras;
    allStreams = streams;
    const availableIds = new Set(buildWallSources(cameras, streams).map((source) => source.id));
    const cameraIds = new Set(cameras.map((cam) => cam.id));
    gridLayout = saved.layout;
    slotAssignments = saved.slots.map((id) => {
      if (!id) return null;
      if (availableIds.has(id)) return id;
      if (streamErr && !cameraIds.has(id)) return id;
      return null;
    });
    pendingGridLayout = gridLayout;
    pendingSlotAssignments = [...slotAssignments];
    // WS-FLV requires a server-provided play URL. Other modes have stable route fallbacks.
    loading = false;
    streamStatusTimer = window.setInterval(() => {
      void refreshWallStreams();
    }, 10000);
    if (dashboardDisposed) window.clearInterval(streamStatusTimer);
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    // Page Visibility API: pause players when tab hidden, resume when visible
    const visibilityHandler = () => {
      tabVisible = !document.hidden;
    };
    document.addEventListener('visibilitychange', visibilityHandler);

    const stopEvents = subscribeNvrEvents({}, (ev: NvrEvent) => {
      if (!ev.camera_id) return;
      alarmFlash = { ...alarmFlash, [ev.camera_id]: Date.now() };
      window.setTimeout(() => {
        if (alarmFlash[ev.camera_id]) {
          const next = { ...alarmFlash };
          delete next[ev.camera_id];
          alarmFlash = next;
        }
      }, 4000);
    });

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
      stopEvents();
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
          class="btn btn-sm {tourOn ? 'btn-primary' : 'btn-ghost'}"
          onclick={toggleTour}
          title={t('dashboard.tour')}
        >
          {t('dashboard.tour')}
        </button>
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
            {@const assigned = cameraId ? sourceById.get(cameraId) : null}
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
                {@const sourceStatus = wallSourceStatus(assigned)}
                <p class="text-sm th-text-primary truncate font-medium">{assigned.name}</p>
                <div class="flex items-center gap-1.5 min-w-0">
                  <p class="text-[10px] th-text-muted font-mono truncate">{assigned.id}</p>
                  <span class="badge {sourceStatus.badgeClass} text-[9px] px-1 py-0.5 shrink-0">{sourceStatus.label}</span>
                </div>
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
          {#if wallSources.length === 0}
            <p class="text-sm th-text-muted text-center py-6">{t('dashboard.noCamerasAvailable')}</p>
          {:else if filteredConfigSources().length === 0}
            <p class="text-sm th-text-muted text-center py-6">{t('dashboard.noSearchResults')}</p>
          {:else}
            {#each filteredConfigSources() as source}
              {@const sourceStatus = wallSourceStatus(source)}
              <button
                class="w-full flex items-center gap-2 px-2 py-2 rounded-md hover:bg-[var(--bg-tertiary)] transition-colors text-left"
                onclick={() => {
                  const target = editingSlotIndex ?? pendingSlotAssignments.findIndex(id => !id);
                  const slotIndex = target >= 0 ? target : 0;
                  assignCameraToSlot(slotIndex, source.id);
                  editingSlotIndex = slotIndex;
                }}
              >
                <LayoutGrid size={14} class="th-text-muted shrink-0" />
                <span class="text-sm th-text-primary truncate">{source.name}</span>
                <span class="ml-auto flex shrink-0 flex-col items-end gap-1">
                  <span class="badge {sourceStatus.badgeClass} text-[9px] px-1 py-0.5">{sourceStatus.label}</span>
                  <span class="text-xs th-text-muted">{wallSourceLabel(source)}</span>
                </span>
              </button>
            {/each}
          {/if}
        </div>

        <div class="flex justify-end gap-2">
          <button class="btn btn-ghost text-sm px-3 py-1.5" onclick={() => { configOpen = false; editingSlotIndex = null; }}>
            {t('common.dismiss')}
          </button>
          <button class="btn btn-primary text-sm px-3 py-1.5" onclick={applyLayoutConfig} disabled={applyingLayout}>
            {applyingLayout ? t('common.loading') : t('dashboard.apply')}
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
    {:else if wallSources.length === 0}
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
        {#each slotAssignments as cameraId, slotIndex (`${slotIndex}:${cameraId ?? ''}`)}
          {@const camera = cameraId ? cameraById.get(cameraId) : null}
          {@const stream = cameraId && !camera ? streamById.get(cameraId) : undefined}
          {#if camera}
            {@const cameraStream = streamForCamera(camera)}
            {@const status = getStreamStatusBadge(cameraStream)}
            {@const mode = getCameraMode(camera)}
            {@const StatusIcon = status.icon}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="relative bg-black rounded-lg overflow-hidden group camera-grid-cell {getCellClass(cameraId, slotIndex)}"
              class:cell-expanded={expandedCameraId === camera.id}
              class:cell-alarm={!!alarmFlash[camera.id]}
              style="min-height: {getCellMinHeight(gridLayout)};"
              role="button"
              tabindex="0"
              aria-label="{camera.name || camera.id} — {status.text}"
              onclick={() => handleCellClick(camera)}
              onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleCellClick(camera); } }}
              ondblclick={() => handleCellDblClick(camera)}
            >
            {#if cameraStream && !cameraStream.active}
              <div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/85 px-4 text-center">
                <WifiOff size={26} class="text-amber-400" />
                <p class="text-sm font-medium text-white">{t('dashboard.streamOfflineWaiting')}</p>
                <p class="text-xs text-white/60">{t('dashboard.streamOfflineAutoRestore')}</p>
              </div>
            {:else if mode === 'snapshot'}
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
              <!-- Same immediate WHEP playback path as the single-camera page.
                   SIP EnsurePlay runs in parallel and never gates the player. -->
              <WebRTCPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                expanded={expandedCameraId === camera.id}
                {tabVisible}
              />
              {#if isGB28181WallCamera(camera) && gbPlayStates[camera.id] === 'error'}
                <div class="absolute top-2 right-2 z-20 max-w-[80%] rounded bg-red-950/85 px-2 py-1.5 text-xs text-white/90 shadow-lg">
                  <span class="text-red-300">GB28181 点播请求失败</span>
                  <button
                    type="button"
                    class="ml-2 rounded bg-blue-600 px-2 py-0.5 text-white"
                    onclick={(event) => {
                      event.stopPropagation();
                      retryDashboardGBPlay(camera);
                    }}
                  >重试</button>
                </div>
              {/if}

            {:else if mode === 'flv' || mode === 'ws-flv'}
              <FlvPlayer
                cameraId={camera.id}
                cameraName={camera.name || camera.id}
                streamUrl={getCameraPlayURL(camera.id, mode, mode === 'flv' ? streamMediaURL(streamIdFor(camera.id), 'flv') : '')}
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
                    wsPath={getCameraPlayURL(camera.id, 'wasm', streamMediaURL(streamIdFor(camera.id), 'ws'))}
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
                  streamUrl={getCameraPlayURL(camera.id, 'fmp4', streamMediaURL(streamIdFor(camera.id), 'fmp4'))}
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

            {#if instantReplayId === camera.id}
              <div class="absolute inset-0 z-20 bg-black flex flex-col">
                <div class="flex items-center justify-between px-2 py-1 th-bg-secondary text-xs">
                  <span>{t('dashboard.instantReplay')}</span>
                  <button class="btn btn-ghost btn-sm" onclick={(e) => { e.stopPropagation(); instantReplayId = null; }}>{t('common.close')}</button>
                </div>
                <video class="w-full flex-1 object-contain bg-black" controls autoplay src={instantReplayUrl(camera.id)}>
                  <track kind="captions" />
                </video>
              </div>
            {/if}

            <button
              class="absolute bottom-2 right-2 z-10 btn btn-ghost btn-sm bg-black/50 text-white opacity-0 group-hover:opacity-100"
              onclick={(e) => { e.stopPropagation(); startInstantReplay(camera.id); }}
              title={t('dashboard.instantReplay')}
            >
              <Play size={12} />
            </button>

            <!-- Streaming protocol badge -->
            {#if mode !== 'unsupported'}
              {@const protocolLabel = mode === 'wasm' ? 'WebCodecs' : mode === 'fmp4' ? 'fMP4' : mode === 'webrtc' ? 'WebRTC' : mode === 'flv' ? 'HTTP-FLV' : mode === 'ws-flv' ? 'WS-FLV' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'LL-HLS' : 'HLS') : 'JPEG'}
              {@const protocolColor = mode === 'wasm' ? 'bg-cyan-500/60' : mode === 'fmp4' ? 'bg-teal-500/60' : mode === 'webrtc' ? 'bg-green-500/60' : mode === 'flv' ? 'bg-orange-500/60' : mode === 'ws-flv' ? 'bg-amber-500/60' : mode === 'hls' ? (defaultProtocol === 'll-hls' ? 'bg-purple-500/60' : 'bg-blue-500/60') : 'bg-gray-500/60'}
              <span class="absolute top-2 right-2 z-10 {protocolColor} text-white text-[10px] font-medium px-2 py-0.5 rounded-full pointer-events-none select-none">
                {protocolLabel}
              </span>
            {/if}

            <!-- Health indicator dot + status -->
            <span
              class="absolute top-2 left-2 z-10 flex items-center gap-1 bg-black/60 text-white text-[10px] font-medium px-1.5 py-0.5 rounded-full select-none"
              title={status.text}
            >
              <StatusIcon size={10} />
              {status.text}
            </span>

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
          {:else if stream}
            {@const mode = videoMode()}
            {@const title = stream.name || stream.stream_id}
            {@const mediaURL = streamPageURL(stream, mode)}
            <!-- svelte-ignore a11y_click_events_have_key_events -->
            <!-- svelte-ignore a11y_no_static_element_interactions -->
            <div
              class="relative bg-black rounded-lg overflow-hidden group camera-grid-cell {getCellClass(cameraId, slotIndex)}"
              class:cell-expanded={expandedCameraId === stream.stream_id}
              style="min-height: {getCellMinHeight(gridLayout)};"
              role="button"
              tabindex="0"
              aria-label={title}
              onclick={() => handleStreamClick(stream.stream_id)}
              onkeydown={(e: KeyboardEvent) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleStreamClick(stream.stream_id); } }}
              ondblclick={() => { if (expandedCameraId === stream.stream_id) shrinkToGrid(); }}
            >
              {#if !stream.active}
                <div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/85 px-4 text-center">
                  <WifiOff size={26} class="text-amber-400" />
                  <p class="text-sm font-medium text-white">{t('dashboard.streamOfflineWaiting')}</p>
                  <p class="text-xs text-white/60">{t('dashboard.streamOfflineAutoRestore')}</p>
                </div>
              {:else if mode === 'hls'}
                <VideoPlayer
                  cameraId={stream.stream_id}
                  cameraName={title}
                  streamUrl={mediaURL}
                  cameraProtocol="stream"
                  protocol={defaultProtocol}
                  expanded={expandedCameraId === stream.stream_id}
                  {tabVisible}
                />
              {:else if mode === 'webrtc'}
                <WebRTCPlayer
                  cameraId={stream.stream_id}
                  cameraName={title}
                  whepUrl={mediaURL}
                  expanded={expandedCameraId === stream.stream_id}
                  {tabVisible}
                />
              {:else if mode === 'flv' || mode === 'ws-flv'}
                <FlvPlayer
                  cameraId={stream.stream_id}
                  cameraName={title}
                  streamUrl={mediaURL}
                  protocol={mode === 'ws-flv' ? 'ws-flv' : 'flv'}
                  expanded={expandedCameraId === stream.stream_id}
                  {tabVisible}
                />
              {:else if mode === 'wasm'}
                {#if WasmPlayerComponent}
                  {@const WasmPlayer = WasmPlayerComponent}
                  <WasmPlayer
                    cameraId={stream.stream_id}
                    cameraName={title}
                    wsPath={mediaURL}
                    expanded={expandedCameraId === stream.stream_id}
                    {tabVisible}
                  />
                {:else if wasmPlayerLoading}
                  <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                    <span class="text-white/50 text-xs">{t('dashboard.loadingWasmPlayer')}</span>
                  </div>
                {:else}
                  <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                    <span class="text-white/50 text-xs">{t('dashboard.wasmPlayerLoadError')}</span>
                  </div>
                {/if}
              {:else if mode === 'fmp4'}
                {#if FMP4PlayerComponent}
                  {@const FMP4Player = FMP4PlayerComponent}
                  <FMP4Player
                    cameraId={stream.stream_id}
                    cameraName={title}
                    streamUrl={mediaURL}
                    expanded={expandedCameraId === stream.stream_id}
                    {tabVisible}
                  />
                {:else if fmp4PlayerLoading}
                  <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                    <span class="text-white/50 text-xs">{t('common.loading')}</span>
                  </div>
                {:else}
                  <div class="absolute inset-0 flex items-center justify-center bg-black/80">
                    <span class="text-white/50 text-xs">{t('common.error')}</span>
                  </div>
                {/if}
              {/if}
              <div class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2 pointer-events-none">
                <div class="flex items-center gap-2">
                  <span class="badge {stream.active ? 'badge-success' : 'badge-neutral'} text-[10px] px-1.5 py-0.5">
                    {stream.active ? t('dashboard.live') : t('dashboard.healthOffline')}
                  </span>
                  <span class="text-white text-sm font-medium truncate">{title}</span>
                </div>
              </div>
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

  .cell-alarm {
    box-shadow: inset 0 0 0 3px var(--color-danger);
    animation: alarm-pulse 0.8s ease-in-out infinite;
  }

  @keyframes alarm-pulse {
    0%, 100% { box-shadow: inset 0 0 0 3px var(--color-danger); }
    50% { box-shadow: inset 0 0 0 3px transparent; }
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
