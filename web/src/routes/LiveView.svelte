<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getCamera, listProtocols, DEFAULT_PROTOCOLS, buildProtocolsMap, normalizeProtocol, getProtocolCapabilities, getDeviceCapabilities, playGB28181Stream, getONVIFProfiles } from '$lib/api';
  import type { Camera, ProtocolInfo, DeviceCapabilitiesInfo } from '$lib/api';
  import { ArrowLeft, Maximize, Minimize, AlertCircle, RefreshCw, ChevronDown, ChevronRight, Image, Move, Activity, Mic, MicOff, Info, Settings, Video, Copy } from 'lucide-svelte';
  import PtzControl from '../components/PtzControl.svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import FlvPlayer from '../components/FlvPlayer.svelte';
  import ProtocolSwitcher from '../components/ProtocolSwitcher.svelte';
  import type { StreamingProtocol } from '../components/ProtocolSwitcher.svelte';
  import SnapshotButton from '../components/SnapshotButton.svelte';
  import ImagingPanel from '$lib/components/ImagingPanel.svelte';
  import PresetManager from '$lib/components/PresetManager.svelte';
  import ONVIFEvents from '$lib/components/ONVIFEvents.svelte';
  import DeviceCapabilities from '$lib/components/DeviceCapabilities.svelte';
  import TalkButton from '../components/TalkButton.svelte';
  import XiaomiTalkButton from '../components/XiaomiTalkButton.svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';

  let { cameraId = '' }: { cameraId?: string } = $props();

  let camera = $state<Camera | null>(null);
  let loading = $state(true);
  let error = $state('');
  let isFullscreen = $state(false);
  let playerContainer: HTMLDivElement | undefined = $state();
  let protocolsMap = $state<Map<string, ProtocolInfo>>(buildProtocolsMap(DEFAULT_PROTOCOLS));
  let streamingProtocol = $state<StreamingProtocol>('hls');
  let switchingProtocol = $state(false);
  let streamPlayURLs = $state<Record<string, string>>({});
  let availableProtocols = $state<string[]>([]);

  // Lazy-loaded WasmPlayer component
  let WasmPlayerComponent = $state<any>(null);
  let wasmPlayerLoading = $state(false);
  let wasmPlayerError = $state('');

  // Lazy-loaded FMP4Player component
  let FMP4PlayerComponent = $state<any>(null);
  let fmp4PlayerLoading = $state(false);

  // Right panel tab state
  type RightPanelTab = 'control' | 'device' | 'events';
  let activeRightTab = $state<RightPanelTab>('control');

  // ONVIF capabilities
  let deviceCaps = $state<DeviceCapabilitiesInfo | null>(null);
  let capsLoading = $state(false);

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
      showToast(t('live.wasmPlayerFailed'), 'error');
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

  function isHlsSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).hls;
  }

  function isPtzSupported(cam: Camera): boolean {
    return getProtocolCapabilities(cam.protocol, protocolsMap).ptz;
  }

  function isOnvifCamera(cam: Camera): boolean {
    return normalizeProtocol(cam.protocol) === 'onvif';
  }

  function isGB28181Camera(cam: Camera): boolean {
    return normalizeProtocol(cam.protocol) === 'gb28181';
  }

  function isXiaomiCamera(cam: Camera): boolean {
    return normalizeProtocol(cam.protocol) === 'xiaomi';
  }

  async function loadCapabilities() {
    if (!camera || !isOnvifCamera(camera)) {
      deviceCaps = null;
      return;
    }
    capsLoading = true;
    try {
      deviceCaps = await getDeviceCapabilities(camera.id);
    } catch (e) {
      console.warn('Failed to load device capabilities:', e);
      deviceCaps = null;
    } finally {
      capsLoading = false;
    }
  }

  async function loadCamera() {
    loading = true;
    error = '';
    try {
      camera = await getCamera(cameraId);
    } catch (e) {
      // If camera not found and ID looks like GB28181 (contains ':'), try to start play first
      if (cameraId.includes(':')) {
        try {
          const parts = cameraId.split(':');
          if (parts.length === 2) {
            await playGB28181Stream({ device_id: parts[0], channel_id: parts[1] });
            // Retry loading camera after play starts
            camera = await getCamera(cameraId);
            return;
          }
        } catch (playErr) {
          console.warn('Failed to start GB28181 play:', playErr);
        }
      }
      error = e instanceof Error ? e.message : t('live.failedLoadCamera');
      camera = null;
    } finally {
      loading = false;
    }
  }

  function goBack() {
    window.location.hash = '#/devices';
  }

  function toggleFullscreen() {
    if (!playerContainer) return;
    try {
      if (!document.fullscreenElement) {
        playerContainer.requestFullscreen();
        isFullscreen = true;
      } else {
        document.exitFullscreen();
        isFullscreen = false;
      }
    } catch (e) { console.warn('Fullscreen not supported:', e); }
  }

  function handleFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  function handleProtocolChange(protocol: StreamingProtocol) {
    switchingProtocol = true;
    streamingProtocol = protocol;
    setTimeout(() => { switchingProtocol = false; }, 100);
  }

  function handleWasmFallback() {
    // Find the best available protocol instead of hardcoded HLS
    const fallbackOrder: StreamingProtocol[] = ['fmp4', 'webrtc', 'flv', 'ws-flv', 'hls', 'll-hls'];
    const fallback = fallbackOrder.find(p => availableProtocols.includes(p));
    if (fallback) {
      showToast(t('live.wasm.fallbackToHls') || `WebCodecs unavailable, switching to ${fallback.toUpperCase()}`, 'warning');
      handleProtocolChange(fallback);
    } else {
      showToast('WebCodecs unavailable and no other protocols available', 'error');
    }
  }

  interface CameraProtocolDetail {
    Protocol: string;
    Available: boolean;
    Reason: string;
    PlayURL?: string;
    Backend?: string;
  }

  function handleProtocolsLoaded(protocols: CameraProtocolDetail[]) {
    const next: Record<string, string> = {};
    const available: string[] = [];
    for (const protocol of protocols) {
      if (protocol.Available && protocol.PlayURL) {
        next[protocol.Protocol] = protocol.PlayURL;
      }
      if (protocol.Available) {
        available.push(protocol.Protocol);
      }
    }
    streamPlayURLs = next;
    availableProtocols = available;
  }

  function getStreamPlayURL(protocol: StreamingProtocol): string {
    if (streamPlayURLs[protocol]) return streamPlayURLs[protocol];
    if (protocol === 'flv') return `/api/cameras/${cameraId}/stream.flv`;
    if (protocol === 'hls') return `/api/cameras/${cameraId}/stream/index.m3u8`;
    if (protocol === 'll-hls') return `/api/cameras/${cameraId}/stream/index.m3u8?ll-hls=1`;
    return '';
  }

  // Check if PTZ should be shown
  let showPtz = $derived(camera && (isGB28181Camera(camera) || (isOnvifCamera(camera) && (!deviceCaps || deviceCaps.ptz))));
  let showTalk = $derived(camera && isGB28181Camera(camera));
  let showXiaomiTalk = $derived(camera && isXiaomiCamera(camera));

  $effect(() => {
    if (streamingProtocol === 'wasm') {
      loadWasmPlayer();
    }
  });

  $effect(() => {
    if (streamingProtocol === 'fmp4') {
      loadFMP4Player();
    }
  });

  $effect(() => {
    if (camera && isOnvifCamera(camera)) {
      loadCapabilities();
    }
  });

  onMount(() => {
    if (!cameraId) {
      error = t('live.cameraIdRequired');
      loading = false;
      return;
    }

    loadCamera();
    document.addEventListener('fullscreenchange', handleFullscreenChange);
    listProtocols().then(list => {
      if (list && list.length > 0) protocolsMap = buildProtocolsMap(list);
    }).catch((e) => { console.warn('Failed to load protocols:', e); });
  });

  onDestroy(() => {
    document.removeEventListener('fullscreenchange', handleFullscreenChange);
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-[1600px] mx-auto px-4 sm:px-6 lg:px-8 py-6">
    <!-- Loading state -->
    {#if loading}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>
    {:else if error}
      <div class="card p-8 text-center">
        <div class="th-color-danger mb-4 flex justify-center"><AlertCircle size={48} /></div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <div class="flex justify-center gap-3">
          <button onclick={loadCamera} class="btn btn-primary btn-sm flex items-center gap-1">
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('detail.back')}
          </button>
        </div>
      </div>
    {:else if camera}
      <!-- Header -->
      <div class="flex items-center gap-3 mb-4">
        <button onclick={goBack} class="btn btn-ghost btn-sm flex items-center gap-1">
          <ArrowLeft size={16} />
          {t('nav.cameras')}
        </button>
        <h2 class="text-xl font-bold th-text-primary truncate">
          {camera.name || camera.id}
        </h2>
        <span class="badge badge-neutral">{protocolsMap.get(camera.protocol)?.label || camera.protocol}</span>

        {#if isOnvifCamera(camera) && camera.profile_token}
          <span class="badge badge-outline text-xs max-w-[150px] truncate" title={camera.profile_name || camera.profile_token}>
            {camera.profile_name || camera.profile_token}
          </span>
        {/if}

        {#if isOnvifCamera(camera)}
          <SnapshotButton cameraId={camera.id} />
        {/if}

        {#if isHlsSupported(camera)}
          <div class="flex-1"></div>
          <ProtocolSwitcher
            cameraId={camera.id}
            cameraEncoding={camera.encoding || camera.stream_encoding || ''}
            selected={streamingProtocol}
            onchange={handleProtocolChange}
            onprotocolsloaded={handleProtocolsLoaded}
          />
          {#if streamPlayURLs['rtsp']}
            <button
              class="btn btn-ghost btn-sm flex items-center gap-1"
              title={t('cameras.copyRtsp')}
              onclick={async () => {
                try {
                  await navigator.clipboard.writeText(streamPlayURLs['rtsp']);
                  showToast(t('cameras.rtspCopied'), 'success');
                } catch {
                  showToast(t('cameras.rtspCopyFailed'), 'error');
                }
              }}
            >
              <Copy size={16} />
              RTSP
            </button>
          {/if}
          <button onclick={toggleFullscreen} class="btn btn-ghost btn-sm flex items-center gap-1">
            {#if isFullscreen}
              <Minimize size={16} />
            {:else}
              <Maximize size={16} />
            {/if}
          </button>
        {/if}
      </div>

      {#if isHlsSupported(camera)}
        <!-- Main content: Player + Controls -->
        <div class="live-layout flex gap-4" style="height: calc(100vh - 180px);">
          <!-- Left: Player -->
          <div class="live-player flex-1 min-w-0">
            <div
              class="card border th-border overflow-hidden h-full"
              bind:this={playerContainer}
            >
              {#if switchingProtocol}
                <div class="relative w-full h-full bg-black flex items-center justify-center">
                  <div class="flex items-center gap-2">
                    <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                    <span class="text-white/50 text-xs">{t('live.protocol.switching')}</span>
                  </div>
                </div>
              {:else if streamingProtocol === 'wasm'}
                {#if WasmPlayerComponent}
                  {@const WasmPlayer = WasmPlayerComponent}
                  <WasmPlayer
                    cameraId={camera.id}
                    cameraName={camera.name || camera.id}
                    expanded={true}
                    onFallbackNeeded={handleWasmFallback}
                  />
                {:else if wasmPlayerLoading}
                  <div class="relative w-full h-full bg-black flex items-center justify-center">
                    <div class="flex flex-col items-center gap-2">
                      <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                      <span class="text-white/50 text-xs">{t('live.loadingWasmPlayer')}</span>
                    </div>
                  </div>
                {:else}
                  <div class="relative w-full h-full bg-black flex items-center justify-center">
                    <div class="flex flex-col items-center gap-2">
                      <AlertCircle size={20} class="text-red-400/60" />
                      <span class="text-white/50 text-xs">{t('live.wasmPlayerLoadError')}</span>
                      <button class="text-xs text-white/40 underline" onclick={loadWasmPlayer}>{t('live.retry') || 'Retry'}</button>
                    </div>
                  </div>
                {/if}
              {:else if streamingProtocol === 'fmp4'}
                {#if FMP4PlayerComponent}
                  {@const FMP4Player = FMP4PlayerComponent}
                  <FMP4Player
                    cameraId={camera.id}
                    cameraName={camera.name || camera.id}
                    expanded={true}
                    onFallbackNeeded={() => handleProtocolChange('hls')}
                  />
                {:else if fmp4PlayerLoading}
                  <div class="relative w-full h-full bg-black flex items-center justify-center">
                    <div class="flex flex-col items-center gap-2">
                      <div class="w-4 h-4 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
                      <span class="text-white/50 text-xs">Loading fMP4 player...</span>
                    </div>
                  </div>
                {:else}
                  <div class="relative w-full h-full bg-black flex items-center justify-center">
                    <div class="flex flex-col items-center gap-2">
                      <AlertCircle size={20} class="text-red-400/60" />
                      <span class="text-white/50 text-xs">Failed to load fMP4 player</span>
                      <button class="text-xs text-white/40 underline" onclick={loadFMP4Player}>Retry</button>
                    </div>
                  </div>
                {/if}
              {:else if streamingProtocol === 'webrtc'}
                <WebRTCPlayer
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  expanded={true}
                />
              {:else if streamingProtocol === 'flv' || streamingProtocol === 'ws-flv'}
                <FlvPlayer
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  streamUrl={getStreamPlayURL(streamingProtocol)}
                  protocol={streamingProtocol === 'ws-flv' ? 'ws-flv' : 'flv'}
                  expanded={true}
                />
              {:else}
                <VideoPlayer
                  cameraId={camera.id}
                  cameraName={camera.name || camera.id}
                  streamUrl={getStreamPlayURL(streamingProtocol)}
                  cameraProtocol={camera.protocol}
                  protocol={streamingProtocol}
                  expanded={true}
                />
              {/if}
            </div>
          </div>

          <!-- Right: Controls Panel -->
          {#if showPtz || showTalk || showXiaomiTalk || isOnvifCamera(camera)}
            <div class="live-controls w-80 flex-shrink-0 flex flex-col overflow-y-auto">
              <!-- Tab Navigation -->
              <div class="flex border-b th-border mb-2">
                <button
                  class="flex-1 px-3 py-2 text-xs font-medium transition-colors {activeRightTab === 'control' ? 'th-text-primary border-b-2 border-primary' : 'th-text-secondary hover:th-text-primary'}"
                  onclick={() => activeRightTab = 'control'}
                >
                  <div class="flex items-center justify-center gap-1.5">
                    <Video size={14} />
                    <span>控制</span>
                  </div>
                </button>
                {#if isOnvifCamera(camera)}
                  <button
                    class="flex-1 px-3 py-2 text-xs font-medium transition-colors {activeRightTab === 'device' ? 'th-text-primary border-b-2 border-primary' : 'th-text-secondary hover:th-text-primary'}"
                    onclick={() => activeRightTab = 'device'}
                  >
                    <div class="flex items-center justify-center gap-1.5">
                      <Settings size={14} />
                      <span>设备</span>
                    </div>
                  </button>
                  <button
                    class="flex-1 px-3 py-2 text-xs font-medium transition-colors {activeRightTab === 'events' ? 'th-text-primary border-b-2 border-primary' : 'th-text-secondary hover:th-text-primary'}"
                    onclick={() => activeRightTab = 'events'}
                  >
                    <div class="flex items-center justify-center gap-1.5">
                      <Activity size={14} />
                      <span>事件</span>
                    </div>
                  </button>
                {/if}
              </div>

              <!-- Tab Content -->
              <div class="flex-1 overflow-y-auto px-1">
                <!-- Control Tab -->
                {#if activeRightTab === 'control'}
                  <div class="space-y-3">
                    <!-- Talk Button (GB28181 only) -->
                    {#if showTalk}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Mic size={16} />
                          {t('live.talk.title') || '语音对讲'}
                        </h3>
                        <TalkButton
                          deviceId={camera.id.split(':')[0] || camera.id}
                          channelId={camera.id.split(':')[1] || '0'}
                        />
                      </div>
                    {/if}

                    <!-- Talk Button (Xiaomi) -->
                    {#if showXiaomiTalk}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Mic size={16} />
                          {t('live.talk.title') || '语音对讲'}
                        </h3>
                        <XiaomiTalkButton cameraId={camera.id} />
                      </div>
                    {/if}

                    <!-- PTZ Control -->
                    {#if showPtz}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Move size={16} />
                          {t('live.ptz.title') || '云台控制'}
                        </h3>
                        <PtzControl {cameraId} enabled={true} compact={true} />
                      </div>
                    {/if}

                    <!-- Preset Manager (in control tab for quick access) -->
                    {#if isOnvifCamera(camera) && deviceCaps?.ptz}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Move size={16} />
                          {t('onvif.presets.title')}
                        </h3>
                        <PresetManager cameraId={camera.id} />
                      </div>
                    {/if}
                  </div>
                {/if}

                <!-- Device Tab -->
                {#if activeRightTab === 'device' && isOnvifCamera(camera)}
                  <div class="space-y-3">
                    <!-- Device Capabilities -->
                    <div class="card border th-border p-4">
                      <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                        <Info size={16} />
                        设备信息
                      </h3>
                      <DeviceCapabilities cameraId={camera.id} />
                    </div>

                    <!-- Imaging Panel -->
                    {#if deviceCaps?.imaging}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Image size={16} />
                          {t('onvif.imaging.title')}
                        </h3>
                        <ImagingPanel cameraId={camera.id} />
                      </div>
                    {/if}
                  </div>
                {/if}

                <!-- Events Tab -->
                {#if activeRightTab === 'events' && isOnvifCamera(camera)}
                  <div class="space-y-3">
                    {#if deviceCaps?.events}
                      <div class="card border th-border p-4">
                        <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                          <Activity size={16} />
                          {t('onvif.events.title')}
                        </h3>
                        <ONVIFEvents cameraId={camera.id} maxEvents={50} />
                      </div>
                    {:else}
                      <div class="card border th-border p-6 text-center">
                        <Activity size={24} class="mx-auto mb-2 th-text-tertiary" />
                        <p class="text-sm th-text-secondary">此设备不支持事件订阅</p>
                      </div>
                    {/if}
                  </div>
                {/if}
              </div>
            </div>
          {/if}
        </div>
      {:else}
        <!-- Unsupported protocol -->
        <div class="card p-12 text-center">
          <div class="th-text-muted mb-4 flex justify-center"><AlertCircle size={48} /></div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('live.notSupported')}</h3>
          <p class="th-text-secondary text-sm mb-4">
            {t('live.notSupportedDesc')}
            <span class="font-mono th-text-primary">{camera.protocol}</span>.
          </p>
          <button onclick={goBack} class="btn btn-secondary btn-sm">
            {t('live.backToCameras')}
          </button>
        </div>
      {/if}
    {/if}
  </main>
</div>

<style>
  /* Responsive layout */
  .live-layout {
    display: flex;
    gap: 1rem;
  }

  .live-player {
    flex: 1;
    min-width: 0;
  }

  .live-controls {
    width: 320px;
    flex-shrink: 0;
  }

  /* Tablet: narrower controls */
  @media (min-width: 768px) and (max-width: 1023px) {
    .live-controls {
      width: 256px;
    }
  }

  /* Mobile: stack layout */
  @media (max-width: 767px) {
    .live-layout {
      flex-direction: column;
      height: auto !important;
    }
    
    .live-controls {
      width: 100%;
    }
  }
</style>
