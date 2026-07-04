<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { getStream, unbindCamera, promoteStream, deleteStream, kickPublisher, deleteCamera, getStreamMetricsHistory, getStreamingSettings } from '$lib/api';
  import type { StreamInfo, StreamMetricSample, StreamMetricsPeriod } from '$lib/api';
  import { loadChart, createStreamMetricChart, updateStreamMetricChart } from '$lib/charts';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import {
    ArrowLeft,
    RefreshCw,
    AlertCircle,
    Play,
    Pause,
    Radio,
    Users,
    Video,
    AudioLines,
    Activity,
    BarChart3,
    Link,
    Unlink,
    ArrowUpCircle,
    Trash2,
    UserMinus,
    ExternalLink,
    Clock,
    Wifi,
    WifiOff,
  } from 'lucide-svelte';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import WebRTCPlayer from '../components/WebRTCPlayer.svelte';
  import FlvPlayer from '../components/FlvPlayer.svelte';

  let { streamId = '' }: { streamId?: string } = $props();

  let stream = $state<StreamInfo | null>(null);
  let loading = $state(true);
  let error = $state('');
  let refreshTimer: number | undefined;

  // Tabs
  type DetailTab = 'detail' | 'metrics' | 'urls' | 'actions';
  let activeTab = $state<DetailTab>('detail');

  // Live metrics tab
  let metricsPeriod = $state<StreamMetricsPeriod>('15m');
  let metricSamples = $state<StreamMetricSample[]>([]);
  let metricsLoading = $state(false);
  let metricsTimer: number | undefined;
  let ChartJs: any = null;
  let fpsChart: any = null;
  let bitrateChart: any = null;
  let subsChart: any = null;

  // Player state
  // Empty until resolved from the configured default protocol (Settings page)
  // or the first available protocol — avoids rendering the wrong player first.
  let selectedProtocol = $state<string>('');
  // Default playback protocol from Settings; applied until the user picks one.
  let configuredDefaultProtocol = $state<string>('wasm');
  let userPickedProtocol = $state(false);
  const webPlayableProtocols = new Set(['hls', 'll-hls', 'flv', 'ws-flv', 'webrtc', 'fmp4', 'wasm']);

  // Dialog states
  let showPromoteDialog = $state(false);
  let promoteName = $state('');
  let promoteDescription = $state('');
  let promoteLocation = $state('');
  let operating = $state(false);

  // Lazy-loaded players
  let WasmPlayerComponent = $state<any>(null);
  let wasmPlayerLoading = $state(false);
  let FMP4PlayerComponent = $state<any>(null);
  let fmp4PlayerLoading = $state(false);

  async function loadWasmPlayer() {
    if (WasmPlayerComponent || wasmPlayerLoading) return;
    wasmPlayerLoading = true;
    try {
      const mod = await import('../components/WasmPlayer.svelte');
      WasmPlayerComponent = mod.default;
    } catch (e) {
      console.error('Failed to load WasmPlayer:', e);
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

  function updateStreamState(next: StreamInfo) {
    if (!stream || stream.stream_id !== next.stream_id) {
      stream = next;
      return;
    }

    const current = stream as StreamInfo & Record<string, unknown>;
    const incoming = next as StreamInfo & Record<string, unknown>;

    // Preserve play_urls reference if contents are identical to avoid
    // triggering unnecessary player re-renders via the {#key} block.
    const currentPlayUrls = current.play_urls;
    const incomingPlayUrls = incoming.play_urls;
    delete current.play_urls;
    delete incoming.play_urls;

    for (const key of Object.keys(current)) {
      if (!(key in incoming)) {
        delete current[key];
      }
    }
    Object.assign(current, incoming);

    // Restore play_urls: keep old reference if JSON content is the same.
    if (currentPlayUrls && incomingPlayUrls &&
        JSON.stringify(currentPlayUrls) === JSON.stringify(incomingPlayUrls)) {
      current.play_urls = currentPlayUrls;
    } else {
      current.play_urls = incomingPlayUrls;
    }
  }

  async function loadStream() {
    loading = stream === null;
    error = '';
    try {
      const next = await getStream(streamId);
      updateStreamState(next);
    } catch (e) {
      error = e instanceof Error ? e.message : t('streams.loadFailed');
    } finally {
      loading = false;
    }
  }

  function goBack() {
    window.location.hash = '#/streams';
  }

  // ── Live metrics tab ────────────────────────────────────────────────────

  async function loadMetrics() {
    if (!stream) return;
    metricsLoading = metricSamples.length === 0;
    try {
      metricSamples = await getStreamMetricsHistory(stream.stream_id, metricsPeriod);
    } catch (e) {
      console.error('[StreamDetail] load metrics failed', e);
    } finally {
      metricsLoading = false;
    }
    await renderMetricCharts();
  }

  async function renderMetricCharts() {
    // Skip while empty so we never create a chart on a hidden (zero-size) canvas.
    if (metricSamples.length === 0) return;
    if (!ChartJs) ChartJs = await loadChart();

    const fpsData = metricSamples.map(s => ({ ts: s.ts, value: +s.in_fps.toFixed(1) }));
    const brData  = metricSamples.map(s => ({ ts: s.ts, value: s.bitrate_kbits }));
    const subData = metricSamples.map(s => ({ ts: s.ts, value: s.subscribers }));

    if (fpsChart && bitrateChart && subsChart) {
      updateStreamMetricChart(fpsChart, fpsData);
      updateStreamMetricChart(bitrateChart, brData);
      updateStreamMetricChart(subsChart, subData);
      return;
    }

    const fpsCtx = document.getElementById('streamFpsChart') as HTMLCanvasElement | null;
    const brCtx  = document.getElementById('streamBitrateChart') as HTMLCanvasElement | null;
    const subCtx = document.getElementById('streamSubsChart') as HTMLCanvasElement | null;
    if (fpsCtx) fpsChart     = createStreamMetricChart(ChartJs, fpsCtx, fpsData, t('streams.metricFps'), 'fps', 'rgba(56, 189, 248, 0.85)');
    if (brCtx)  bitrateChart = createStreamMetricChart(ChartJs, brCtx,  brData,  t('streams.metricBitrate'), 'Kbps', 'rgba(139, 92, 246, 0.85)');
    if (subCtx) subsChart    = createStreamMetricChart(ChartJs, subCtx, subData, t('streams.metricSubscribers'), '', 'rgba(16, 185, 129, 0.85)');
  }

  function destroyMetricCharts() {
    if (fpsChart)     { fpsChart.destroy();     fpsChart = null; }
    if (bitrateChart) { bitrateChart.destroy(); bitrateChart = null; }
    if (subsChart)    { subsChart.destroy();    subsChart = null; }
  }

  function startMetricsPolling() {
    stopMetricsPolling();
    metricsTimer = window.setInterval(() => { void loadMetrics(); }, 5000);
  }

  function stopMetricsPolling() {
    if (metricsTimer) { window.clearInterval(metricsTimer); metricsTimer = undefined; }
  }

  async function switchTab(tab: DetailTab) {
    if (tab === activeTab) return;
    if (activeTab === 'metrics') {
      stopMetricsPolling();
      destroyMetricCharts();
    }
    activeTab = tab;
    if (tab === 'metrics') {
      await tick(); // wait for canvases to mount
      await loadMetrics();
      startMetricsPolling();
    }
  }

  async function changeMetricsPeriod(p: StreamMetricsPeriod) {
    if (p === metricsPeriod) return;
    metricsPeriod = p;
    destroyMetricCharts(); // recreate with new window
    await loadMetrics();
  }

  function getPlayURL(protocol: string): string {
    if (!stream?.play_urls) return '';
    const found = stream.play_urls.find(p => p.protocol === protocol);
    return found?.url || '';
  }

  function getPlayerURL(protocol: string): string {
    if (!stream) return '';
    const streamID = encodeURIComponent(stream.stream_id);
    if (protocol === 'flv') return `/api/cameras/${streamID}/stream.flv`;
    if (protocol === 'hls') return `/api/cameras/${streamID}/stream/index.m3u8`;
    if (protocol === 'll-hls') return `/api/cameras/${streamID}/stream/index.m3u8?ll-hls=1`;
    if (protocol === 'fmp4') return `/api/cameras/${streamID}/stream.m4s`;
    return getPlayURL(protocol);
  }

  // Protocols that imply a browser-decodable H.264/H.265 video transport.
  // WebCodecs ("wasm") plays these over the generic WebSocket endpoint.
  const videoTransports = ['hls', 'll-hls', 'flv', 'ws-flv', 'webrtc', 'fmp4'];

  function getAvailableProtocols(): string[] {
    if (!stream?.play_urls) return [];
    const backend = stream.play_urls
      .map(p => p.protocol)
      .filter(protocol => webPlayableProtocols.has(protocol));
    // Offer WebCodecs as the preferred option whenever the stream exposes a
    // decodable video transport — it needs no backend play_url of its own.
    if (backend.some(p => videoTransports.includes(p)) && !backend.includes('wasm')) {
      return ['wasm', ...backend];
    }
    return backend;
  }

  function protocolLabel(protocol: string): string {
    switch (protocol) {
      case 'hls': return 'HLS';
      case 'll-hls': return 'LL-HLS';
      case 'flv': return 'FLV';
      case 'ws-flv': return 'WS-FLV';
      case 'webrtc': return 'WebRTC';
      case 'fmp4': return 'fMP4';
      case 'wasm': return 'WebCodecs';
      case 'rtmp': return 'RTMP';
      case 'rtsp': return 'RTSP';
      default: return protocol.toUpperCase();
    }
  }

  function sourceTypeLabel(type: string): string {
    switch (type) {
      case 'camera': return t('streams.sourceCamera');
      case 'gb28181': return t('streams.sourceGB28181');
      case 'rtmp_push': return t('streams.sourceRTMPPush');
      case 'srt_push': return t('streams.sourceSRTPush');
      case 'whip_push': return t('streams.sourceWHIPPush');
      case 'relay_pull': return t('streams.sourceRelayPull');
      default: return t('streams.sourceStream');
    }
  }

  // Track whether the stream was ever seen as active to prevent the player
  // from flickering when polling briefly returns active=false (common with
  // GB28181 streams where the lal engine may not report the pub session).
  let everActive = $state(false);
  $effect(() => {
    if (stream && (stream.active || stream.gb28181_playing)) {
      everActive = true;
    }
  });
  let canPlay = $derived(
    !!stream && everActive && getAvailableProtocols().length > 0
  );

  function formatTime(value?: string): string {
    if (!value) return t('streams.unknown');
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return value;
    return date.toLocaleString();
  }

  function formatDuration(startedAt?: string): string {
    if (!startedAt) return '—';
    const start = new Date(startedAt);
    if (Number.isNaN(start.getTime())) return '—';
    const now = new Date();
    const diff = Math.floor((now.getTime() - start.getTime()) / 1000);
    const hours = Math.floor(diff / 3600);
    const minutes = Math.floor((diff % 3600) / 60);
    const seconds = diff % 60;
    if (hours > 0) return `${hours}h ${minutes}m ${seconds}s`;
    if (minutes > 0) return `${minutes}m ${seconds}s`;
    return `${seconds}s`;
  }

  function formatBitrate(kbits?: number): string {
    if (!kbits || kbits <= 0) return '—';
    if (kbits >= 1000) return `${(kbits / 1000).toFixed(2)} Mbps`;
    return `${kbits.toFixed(0)} Kbps`;
  }

  async function handleUnbind() {
    if (!stream) return;
    if (!confirm(t('streams.confirmUnbind'))) return;
    operating = true;
    try {
      await unbindCamera(stream.stream_id);
      showToast(t('streams.unbindSuccess'), 'success');
      await loadStream();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.unbindFailed'), 'error');
    } finally {
      operating = false;
    }
  }

  async function handleDemote() {
    if (!stream) return;
    if (!confirm(t('streams.confirmDemote'))) return;
    operating = true;
    try {
      await deleteCamera(stream.camera_id || stream.stream_id);
      showToast(t('streams.demoteSuccess'), 'success');
      await loadStream();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.demoteFailed'), 'error');
    } finally {
      operating = false;
    }
  }

  async function handlePromote() {
    if (!stream || !promoteName) return;
    operating = true;
    try {
      await promoteStream(stream.stream_id, {
        name: promoteName,
        description: promoteDescription,
        location: promoteLocation,
      });
      showToast(t('streams.promoteSuccess'), 'success');
      showPromoteDialog = false;
      promoteName = '';
      promoteDescription = '';
      promoteLocation = '';
      await loadStream();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.promoteFailed'), 'error');
    } finally {
      operating = false;
    }
  }

  async function handleDelete() {
    if (!stream) return;
    if (!confirm(t('streams.confirmDelete'))) return;
    operating = true;
    try {
      await deleteStream(stream.stream_id);
      showToast(t('streams.deleteSuccess'), 'success');
      goBack();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.deleteFailed'), 'error');
    } finally {
      operating = false;
    }
  }

  async function handleKickPublisher() {
    if (!stream) return;
    if (!confirm(t('streams.confirmKick'))) return;
    operating = true;
    try {
      await kickPublisher(stream.stream_id);
      showToast(t('streams.kickSuccess'), 'success');
      await loadStream();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.kickFailed'), 'error');
    } finally {
      operating = false;
    }
  }

  function closePromoteDialog() {
    showPromoteDialog = false;
  }

  function handleDialogOverlayKeydown(event: KeyboardEvent, close: () => void) {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault();
      close();
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
    }
  }

  function handleDialogKeydown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
    }
  }

  function stopDialogClick(event: MouseEvent) {
    event.stopPropagation();
  }

  $effect(() => {
    const available = getAvailableProtocols();
    if (available.length === 0) return;
    // Until the user explicitly picks a protocol, prefer the configured default
    // (Settings → default playback protocol) when it is available here.
    if (!userPickedProtocol && available.includes(configuredDefaultProtocol)) {
      if (selectedProtocol !== configuredDefaultProtocol) {
        selectedProtocol = configuredDefaultProtocol;
      }
      return;
    }
    if (!available.includes(selectedProtocol)) {
      selectedProtocol = available[0];
    }
  });

  $effect(() => {
    if (!stream) return;
    const available = getAvailableProtocols();
    console.info('[StreamDetail] player selection', {
      streamId: stream.stream_id,
      selectedProtocol,
      selectedURL: getPlayerURL(selectedProtocol),
      availableProtocols: available,
      playURLs: stream.play_urls,
    });
  });

  $effect(() => {
    if (selectedProtocol === 'wasm') loadWasmPlayer();
    if (selectedProtocol === 'fmp4') loadFMP4Player();
  });

  onMount(() => {
    if (!streamId) {
      error = t('streams.streamIdRequired');
      loading = false;
      return;
    }
    loadStream();
    // Resolve the configured default playback protocol (Settings page).
    getStreamingSettings()
      .then(config => {
        if (config.default_protocol) configuredDefaultProtocol = config.default_protocol;
      })
      .catch(() => { /* keep fallback default */ });
    refreshTimer = window.setInterval(() => {
      void loadStream();
    }, 5000);
  });

  onDestroy(() => {
    if (refreshTimer) {
      window.clearInterval(refreshTimer);
    }
    stopMetricsPolling();
    destroyMetricCharts();
  });
</script>

<div class="stream-detail-page">
  <main class="stream-detail-shell">
    <!-- Loading state -->
    {#if loading && !stream}
      <div class="loading-card">
        <div class="spinner"></div>
        <p>{t('common.loading')}</p>
      </div>
    {:else if error}
      <div class="error-card">
        <AlertCircle size={48} />
        <div>
          <h3>{t('common.error')}</h3>
          <p>{error}</p>
        </div>
        <div class="error-actions">
          <button class="btn btn-primary btn-sm" onclick={loadStream}>
            <RefreshCw size={14} />
            {t('common.retry')}
          </button>
          <button class="btn btn-secondary btn-sm" onclick={goBack}>
            {t('streams.backToList')}
          </button>
        </div>
      </div>
    {:else if stream}
      {@const encodedStreamId = encodeURIComponent(stream.stream_id)}
      <!-- Header -->
      <header class="detail-header glass">
        <div class="header-left">
          <button class="btn btn-ghost btn-sm" onclick={goBack}>
            <ArrowLeft size={16} />
            {t('streams.title')}
          </button>
          <div class="header-info">
            <h1>{stream.camera_name || stream.stream_id}</h1>
            <div class="header-meta">
              <span class="stream-id-badge">{stream.stream_id}</span>
              <span class:status-active={stream.active} class:status-inactive={!stream.active} class="status-badge">
                {#if stream.active}
                  <Radio size={12} />
                  {t('streams.active')}
                {:else}
                  <Pause size={12} />
                  {t('streams.idle')}
                {/if}
              </span>
              <span class="source-badge">{sourceTypeLabel(stream.source_type)}</span>
              {#if stream.managed}
                <span class="managed-badge">
                  <Link size={12} />
                  {t('streams.managed')}
                </span>
              {/if}
            </div>
          </div>
        </div>
        <div class="header-actions">
          <button class="btn btn-ghost btn-sm" onclick={loadStream} disabled={loading}>
            <RefreshCw size={14} style={loading ? 'animation: spin 1s linear infinite;' : undefined} />
            {t('common.refresh')}
          </button>
        </div>
      </header>

      <!-- Main content -->
      <div class="detail-body">
        <!-- Player section (always visible) -->
        <section class="player-section glass">
          <div class="section-header">
            <h2>
              <Play size={18} />
              {t('streams.player')}
            </h2>
            {#if stream.play_urls?.length}
              <div class="protocol-selector">
                {#each getAvailableProtocols() as protocol}
                  <button
                    class="protocol-btn"
                    class:active={selectedProtocol === protocol}
                    onclick={() => {
                      console.info('[StreamDetail] protocol click', {
                        streamId: stream.stream_id,
                        protocol,
                        targetURL: getPlayerURL(protocol),
                      });
                      userPickedProtocol = true;
                      selectedProtocol = protocol;
                    }}
                  >
                    {protocolLabel(protocol)}
                  </button>
                {/each}
              </div>
            {/if}
          </div>

          {#if canPlay}
              <div class="player-container">
                {#if !selectedProtocol}
                  <!-- Resolving the configured default protocol -->
                  <div class="player-loading"><div class="spinner"></div></div>
                {:else}
                {#key selectedProtocol + ':' + getPlayerURL(selectedProtocol)}
                  {#if selectedProtocol === 'webrtc'}
                    <WebRTCPlayer
                      cameraId={encodedStreamId}
                      cameraName={stream.camera_name || stream.stream_id}
                      expanded={true}
                    />
                  {:else if selectedProtocol === 'flv' || selectedProtocol === 'ws-flv'}
                    <FlvPlayer
                      cameraId={encodedStreamId}
                      cameraName={stream.camera_name || stream.stream_id}
                      streamUrl={getPlayerURL(selectedProtocol)}
                      protocol={selectedProtocol === 'ws-flv' ? 'ws-flv' : 'flv'}
                      expanded={true}
                    />
                  {:else if selectedProtocol === 'wasm' && WasmPlayerComponent}
                    {@const WasmPlayer = WasmPlayerComponent}
                    <WasmPlayer
                      cameraId={encodedStreamId}
                      cameraName={stream.camera_name || stream.stream_id}
                      expanded={true}
                      onFallbackNeeded={() => {
                        const fallback = getAvailableProtocols().find(p => p !== 'wasm');
                        if (fallback) selectedProtocol = fallback;
                      }}
                    />
                  {:else if selectedProtocol === 'wasm'}
                    <!-- WasmPlayer chunk still loading — avoid falling through to HLS -->
                    <div class="player-loading">
                      <div class="spinner"></div>
                    </div>
                  {:else if selectedProtocol === 'fmp4' && FMP4PlayerComponent}
                    {@const FMP4Player = FMP4PlayerComponent}
                    <FMP4Player
                      cameraId={encodedStreamId}
                      cameraName={stream.camera_name || stream.stream_id}
                      expanded={true}
                    />
                  {:else}
                    <VideoPlayer
                      cameraId={encodedStreamId}
                      cameraName={stream.camera_name || stream.stream_id}
                      streamUrl={getPlayerURL(selectedProtocol) || getPlayerURL('hls')}
                      cameraProtocol={selectedProtocol}
                      protocol={selectedProtocol}
                      expanded={true}
                    />
                  {/if}
                {/key}
                {/if}
              </div>
          {:else}
            <div class="player-inactive">
              <WifiOff size={48} />
              <p>{t('streams.streamInactive')}</p>
              <p class="hint">{t('streams.streamInactiveHint')}</p>
            </div>
          {/if}
        </section>

        <!-- Tab navigation -->
        <nav class="detail-tabs">
          <button class="detail-tab" class:active={activeTab === 'detail'} onclick={() => switchTab('detail')}>
            <Activity size={15} />
            {t('streams.tabDetail')}
          </button>
          <button class="detail-tab" class:active={activeTab === 'metrics'} onclick={() => switchTab('metrics')}>
            <BarChart3 size={15} />
            {t('streams.tabMetrics')}
          </button>
          <button class="detail-tab" class:active={activeTab === 'urls'} onclick={() => switchTab('urls')}>
            <ExternalLink size={15} />
            {t('streams.tabUrls')}
          </button>
          <button class="detail-tab" class:active={activeTab === 'actions'} onclick={() => switchTab('actions')}>
            <Trash2 size={15} />
            {t('streams.tabActions')}
          </button>
        </nav>

        {#if activeTab === 'detail'}
        <!-- Stream info section -->
        <section class="info-section glass">
          <div class="section-header">
            <h2>
              <Activity size={18} />
              {t('streams.streamInfo')}
            </h2>
          </div>

          <div class="info-grid">
            <div class="info-item">
              <Video size={16} />
              <div>
                <span class="info-label">{t('streams.videoCodec')}</span>
                <strong>{stream.video_codec || '—'}</strong>
              </div>
            </div>
            <div class="info-item">
              <AudioLines size={16} />
              <div>
                <span class="info-label">{t('streams.audioCodec')}</span>
                <strong>{stream.audio_codec || '—'}</strong>
              </div>
            </div>
            <div class="info-item">
              <Activity size={16} />
              <div>
                <span class="info-label">{t('streams.inputFPS')}</span>
                <strong>{stream.in_fps ? stream.in_fps.toFixed(1) : '—'}</strong>
              </div>
            </div>
            <div class="info-item">
              <Activity size={16} />
              <div>
                <span class="info-label">{t('streams.bitrate')}</span>
                <strong>{formatBitrate(stream.publisher?.bitrate_kbits || stream.publisher?.read_bitrate_kbits)}</strong>
              </div>
            </div>
            <div class="info-item">
              <Users size={16} />
              <div>
                <span class="info-label">{t('streams.viewers')}</span>
                <strong>{stream.subscribers?.length || 0}</strong>
              </div>
            </div>
            <div class="info-item">
              <Clock size={16} />
              <div>
                <span class="info-label">{t('streams.lastFrame')}</span>
                <strong>{formatTime(stream.last_frame_time)}</strong>
              </div>
            </div>
            <div class="info-item">
              <Wifi size={16} />
              <div>
                <span class="info-label">{t('streams.engine')}</span>
                <strong>{stream.engine}</strong>
              </div>
            </div>
          </div>

          <!-- Publisher info -->
          {#if stream.publisher}
            <div class="publisher-section">
              <h3>
                <Radio size={16} />
                {t('streams.publisher')}
              </h3>
              <div class="publisher-info">
                <div class="info-item">
                  <span class="info-label">{t('streams.protocol')}</span>
                  <strong>{stream.publisher.protocol}</strong>
                </div>
                <div class="info-item">
                  <span class="info-label">{t('streams.sessionId')}</span>
                  <code>{stream.publisher.session_id}</code>
                </div>
                {#if stream.publisher.remote}
                  <div class="info-item">
                    <span class="info-label">{t('streams.remote')}</span>
                    <code>{stream.publisher.remote}</code>
                  </div>
                {/if}
                <div class="info-item">
                  <span class="info-label">{t('streams.bitrate')}</span>
                  <strong>{formatBitrate(stream.publisher.bitrate_kbits)}</strong>
                </div>
                <div class="info-item">
                  <span class="info-label">{t('streams.readBitrate')}</span>
                  <strong>{formatBitrate(stream.publisher.read_bitrate_kbits)}</strong>
                </div>
                <div class="info-item">
                  <span class="info-label">{t('streams.writeBitrate')}</span>
                  <strong>{formatBitrate(stream.publisher.write_bitrate_kbits)}</strong>
                </div>
              </div>
            </div>
          {/if}

          <!-- Subscribers info -->
          {#if stream.subscribers?.length}
            <div class="subscribers-section">
              <h3>
                <Users size={16} />
                {t('streams.subscribers')} ({stream.subscribers.length})
              </h3>
              <div class="subscribers-list">
                {#each stream.subscribers as sub}
                  <div class="subscriber-item">
                    <span class="sub-protocol">{sub.protocol}</span>
                    <code>{sub.session_id}</code>
                    {#if sub.remote}
                      <span class="sub-remote">{sub.remote}</span>
                    {/if}
                    <span class="sub-remote">{formatBitrate(sub.bitrate_kbits || sub.write_bitrate_kbits || sub.read_bitrate_kbits)}</span>
                  </div>
                {/each}
              </div>
            </div>
          {/if}
        </section>
        {:else if activeTab === 'metrics'}
        <!-- Live metrics section -->
        <section class="metrics-section glass">
          <div class="section-header">
            <h2>
              <BarChart3 size={18} />
              {t('streams.metrics')}
            </h2>
            <div class="protocol-selector">
              <button class="protocol-btn" class:active={metricsPeriod === '5m'} onclick={() => changeMetricsPeriod('5m')}>
                {t('streams.metricsPeriod5m')}
              </button>
              <button class="protocol-btn" class:active={metricsPeriod === '15m'} onclick={() => changeMetricsPeriod('15m')}>
                {t('streams.metricsPeriod15m')}
              </button>
              <button class="protocol-btn" class:active={metricsPeriod === '30m'} onclick={() => changeMetricsPeriod('30m')}>
                {t('streams.metricsPeriod30m')}
              </button>
            </div>
          </div>

          <p class="metrics-hint">{t('streams.metricsHint')}</p>

          {#if !metricsLoading && metricSamples.length === 0}
            <div class="metrics-empty">
              <Activity size={32} />
              <p>{t('streams.metricsEmpty')}</p>
            </div>
          {/if}

          <div class="metrics-charts" class:hidden={!metricsLoading && metricSamples.length === 0}>
            <div class="metric-chart-card">
              <span class="metric-chart-title"><Activity size={14} /> {t('streams.metricFps')}</span>
              <div class="metric-chart-canvas"><canvas id="streamFpsChart"></canvas></div>
            </div>
            <div class="metric-chart-card">
              <span class="metric-chart-title"><Radio size={14} /> {t('streams.metricBitrate')}</span>
              <div class="metric-chart-canvas"><canvas id="streamBitrateChart"></canvas></div>
            </div>
            <div class="metric-chart-card">
              <span class="metric-chart-title"><Users size={14} /> {t('streams.metricSubscribers')}</span>
              <div class="metric-chart-canvas"><canvas id="streamSubsChart"></canvas></div>
            </div>
          </div>
        </section>
        {:else if activeTab === 'urls'}
        <!-- Play URLs section -->
        <section class="urls-section glass">
          <div class="section-header">
            <h2>
              <ExternalLink size={18} />
              {t('streams.playUrls')}
            </h2>
          </div>

          {#if stream.play_urls?.length}
            <div class="urls-list">
              {#each stream.play_urls as play}
                <a class="url-item" href={play.url} target="_blank" rel="noreferrer">
                  <div class="url-info">
                    <span class="url-protocol">{protocolLabel(play.protocol)}</span>
                    <code>{play.url}</code>
                  </div>
                  <ExternalLink size={14} />
                </a>
              {/each}
            </div>
          {:else}
            <div class="empty-urls">
              <p>{t('streams.noPlayURLs')}</p>
            </div>
          {/if}
        </section>
        {:else if activeTab === 'actions'}
        <!-- Actions section -->
        <section class="actions-section glass">
          <div class="section-header">
            <h2>{t('streams.actions')}</h2>
          </div>

          <div class="actions-grid">
            {#if !stream.managed}
              <button class="action-btn" onclick={() => { showPromoteDialog = true; }}>
                <ArrowUpCircle size={20} />
                <span>{t('streams.promote')}</span>
                <p>{t('streams.promoteDesc')}</p>
              </button>
            {:else if stream.management_type === 'bound'}
              <button class="action-btn" onclick={handleUnbind} disabled={operating}>
                <Unlink size={20} />
                <span>{t('streams.unbindCamera')}</span>
                <p>{t('streams.unbindCameraDesc')}</p>
              </button>
            {:else if stream.management_type === 'promoted'}
              <button class="action-btn" onclick={handleDemote} disabled={operating}>
                <Unlink size={20} />
                <span>{t('streams.demote')}</span>
                <p>{t('streams.demoteDesc')}</p>
              </button>
            {/if}

            {#if stream.publisher}
              <button class="action-btn warning" onclick={handleKickPublisher} disabled={operating}>
                <UserMinus size={20} />
                <span>{t('streams.kickPublisher')}</span>
                <p>{t('streams.kickPublisherDesc')}</p>
              </button>
            {/if}

            <button class="action-btn danger" onclick={handleDelete} disabled={operating}>
              <Trash2 size={20} />
              <span>{t('streams.deleteStream')}</span>
              <p>{t('streams.deleteStreamDesc')}</p>
            </button>
          </div>
        </section>
        {/if}
      </div>
    {/if}
  </main>
</div>

<!-- Promote Stream Dialog -->
{#if showPromoteDialog}
  <div
    class="dialog-overlay"
    role="button"
    tabindex="0"
    aria-label={t('common.cancel')}
    onclick={closePromoteDialog}
    onkeydown={(event) => handleDialogOverlayKeydown(event, closePromoteDialog)}
  >
    <div
      class="dialog glass"
      role="dialog"
      tabindex="-1"
      aria-modal="true"
      aria-labelledby="promote-stream-dialog-title"
      onclick={stopDialogClick}
      onkeydown={handleDialogKeydown}
    >
      <h3 id="promote-stream-dialog-title">{t('streams.promote')}</h3>
      <p>{t('streams.promoteDialogDesc')}</p>
      <div class="dialog-content">
        <label class="form-label">
          {t('streams.cameraName')}
          <input type="text" bind:value={promoteName} class="form-input" placeholder={t('streams.cameraNamePlaceholder')} />
        </label>
        <label class="form-label">
          {t('streams.description')}
          <input type="text" bind:value={promoteDescription} class="form-input" placeholder={t('streams.descriptionPlaceholder')} />
        </label>
        <label class="form-label">
          {t('streams.location')}
          <input type="text" bind:value={promoteLocation} class="form-input" placeholder={t('streams.locationPlaceholder')} />
        </label>
      </div>
      <div class="dialog-actions">
        <button class="btn btn-secondary" onclick={closePromoteDialog}>
          {t('common.cancel')}
        </button>
        <button class="btn btn-primary" onclick={handlePromote} disabled={!promoteName || operating}>
          {operating ? t('common.processing') : t('streams.promoteButton')}
        </button>
      </div>
    </div>
  </div>
{/if}

<style>
  .stream-detail-page {
    min-height: 100vh;
    padding-top: 68px;
    background:
      radial-gradient(circle at top left, rgba(37, 99, 235, 0.14), transparent 30%),
      radial-gradient(circle at top right, rgba(14, 165, 233, 0.14), transparent 28%),
      var(--bg-primary);
  }

  .stream-detail-shell {
    max-width: 1400px;
    margin: 0 auto;
    padding: 1rem;
    display: grid;
    gap: 1rem;
  }

  .loading-card,
  .error-card {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    padding: 3rem;
    border-radius: var(--radius-lg);
    border: 1px dashed var(--border);
    color: var(--text-secondary);
    text-align: center;
  }

  .error-card h3 {
    margin: 0;
    color: var(--color-danger);
  }

  .error-actions {
    display: flex;
    gap: 0.5rem;
  }

  .detail-header {
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
    padding: 1.25rem;
    border-radius: var(--radius-lg);
  }

  .header-left {
    display: flex;
    align-items: flex-start;
    gap: 1rem;
  }

  .header-info h1 {
    margin: 0;
    font-size: 1.5rem;
    font-weight: 700;
  }

  .header-meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    margin-top: 0.5rem;
  }

  .stream-id-badge,
  .status-badge,
  .source-badge,
  .managed-badge {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
    padding: 0.25rem 0.6rem;
    border-radius: 999px;
    font-size: 0.75rem;
    font-weight: 600;
  }

  .stream-id-badge {
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    font-family: monospace;
  }

  .status-badge {
    border: 1px solid var(--border);
  }

  .status-active {
    background: rgba(34, 197, 94, 0.15);
    color: var(--color-success);
  }

  .status-inactive {
    background: var(--bg-secondary);
    color: var(--text-secondary);
  }

  .source-badge {
    background: rgba(14, 165, 233, 0.15);
    color: var(--color-primary);
  }

  .managed-badge {
    background: rgba(168, 85, 247, 0.15);
    color: #a855f7;
  }

  .header-actions {
    display: flex;
    gap: 0.5rem;
  }

  .detail-body {
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }

  /* Tab navigation */
  .detail-tabs {
    display: flex;
    gap: 0.25rem;
    flex-wrap: wrap;
    border-bottom: 1px solid var(--border);
    padding-bottom: 0.25rem;
  }

  .detail-tab {
    display: inline-flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.55rem 1rem;
    border: none;
    background: transparent;
    color: var(--text-secondary);
    font-size: 0.85rem;
    font-weight: 600;
    cursor: pointer;
    border-radius: var(--radius-md) var(--radius-md) 0 0;
    border-bottom: 2px solid transparent;
    transition: all 0.15s ease;
  }

  .detail-tab:hover {
    color: var(--text-primary);
    background: var(--bg-secondary);
  }

  .detail-tab.active {
    color: var(--color-primary);
    border-bottom-color: var(--color-primary);
  }

  .glass {
    background: rgba(255, 255, 255, 0.03);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    backdrop-filter: blur(10px);
  }

  .section-header {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--border);
  }

  .section-header h2 {
    margin: 0;
    font-size: 1rem;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  /* Live metrics tab */
  .metrics-hint {
    margin: 0;
    padding: 0.75rem 1.25rem 0;
    font-size: 0.8rem;
    color: var(--text-secondary);
  }

  .metrics-charts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
    gap: 1rem;
    padding: 1rem 1.25rem 1.25rem;
  }

  .metrics-charts.hidden {
    display: none;
  }

  .metric-chart-card {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
    padding: 0.85rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: rgba(255, 255, 255, 0.02);
  }

  .metric-chart-title {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.8rem;
    font-weight: 600;
    color: var(--text-secondary);
  }

  .metric-chart-title :global(svg) {
    color: var(--color-primary);
  }

  .metric-chart-canvas {
    position: relative;
    height: 180px;
  }

  .metrics-empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.75rem;
    padding: 2.5rem 1.25rem;
    color: var(--text-secondary);
    text-align: center;
  }

  .protocol-selector {
    display: flex;
    gap: 0.25rem;
    flex-wrap: wrap;
  }

  .protocol-btn {
    padding: 0.35rem 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-secondary);
    font-size: 0.75rem;
    font-weight: 600;
    cursor: pointer;
    transition: all 0.15s ease;
  }

  .protocol-btn:hover {
    border-color: var(--color-primary);
    color: var(--color-primary);
  }

  .protocol-btn.active {
    background: var(--color-primary);
    border-color: var(--color-primary);
    color: white;
  }

  .player-container {
    aspect-ratio: 16 / 9;
    background: #000;
    border-radius: 0 0 var(--radius-lg) var(--radius-lg);
    overflow: hidden;
  }

  .player-inactive {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1rem;
    padding: 3rem;
    color: var(--text-secondary);
    aspect-ratio: 16 / 9;
  }

  .player-inactive {
    background: rgba(0, 0, 0, 0.2);
    border-radius: 0 0 var(--radius-lg) var(--radius-lg);
  }

  .player-inactive .hint {
    font-size: 0.85rem;
    opacity: 0.7;
  }

  .player-loading {
    display: flex;
    align-items: center;
    justify-content: center;
    aspect-ratio: 16 / 9;
    background: #000;
    border-radius: 0 0 var(--radius-lg) var(--radius-lg);
  }

  .info-grid {
    display: grid;
    grid-template-columns: repeat(2, 1fr);
    gap: 1rem;
    padding: 1.25rem;
  }

  @media (max-width: 640px) {
    .info-grid {
      grid-template-columns: 1fr;
    }
  }

  .info-item {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    padding: 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: rgba(255, 255, 255, 0.02);
  }

  .info-item :global(svg) {
    color: var(--color-primary);
    flex-shrink: 0;
    margin-top: 0.1rem;
  }

  .info-label {
    display: block;
    font-size: 0.75rem;
    color: var(--text-secondary);
    margin-bottom: 0.15rem;
  }

  .info-item strong {
    font-size: 0.9rem;
  }

  .info-item code {
    font-size: 0.8rem;
    color: var(--text-secondary);
    word-break: break-all;
  }

  .publisher-section,
  .subscribers-section {
    padding: 1.25rem;
    border-top: 1px solid var(--border);
  }

  .publisher-section h3,
  .subscribers-section h3 {
    margin: 0 0 0.75rem;
    font-size: 0.9rem;
    font-weight: 600;
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }

  .publisher-info {
    display: grid;
    gap: 0.5rem;
  }

  .subscribers-list {
    display: grid;
    gap: 0.5rem;
  }

  .subscriber-item {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    padding: 0.5rem 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: rgba(255, 255, 255, 0.02);
    font-size: 0.85rem;
  }

  .sub-protocol {
    padding: 0.15rem 0.5rem;
    border-radius: var(--radius-sm);
    background: var(--bg-secondary);
    font-size: 0.7rem;
    font-weight: 600;
    text-transform: uppercase;
  }

  .sub-remote {
    color: var(--text-secondary);
    font-size: 0.8rem;
  }

  .urls-list {
    padding: 1rem;
    display: grid;
    gap: 0.5rem;
  }

  .url-item {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 0.75rem;
    padding: 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    text-decoration: none;
    color: inherit;
    transition: all 0.15s ease;
  }

  .url-item:hover {
    border-color: var(--color-primary);
    transform: translateY(-1px);
  }

  .url-info {
    display: flex;
    align-items: center;
    gap: 0.75rem;
    min-width: 0;
  }

  .url-protocol {
    padding: 0.2rem 0.5rem;
    border-radius: var(--radius-sm);
    background: rgba(14, 165, 233, 0.15);
    color: var(--color-primary);
    font-size: 0.7rem;
    font-weight: 700;
    text-transform: uppercase;
    white-space: nowrap;
  }

  .url-info code {
    font-size: 0.8rem;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .empty-urls {
    padding: 2rem;
    text-align: center;
    color: var(--text-secondary);
  }

  .actions-grid {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
    gap: 1rem;
    padding: 1.25rem;
  }

  .action-btn {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 0.5rem;
    padding: 1.25rem;
    border-radius: var(--radius-lg);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-primary);
    cursor: pointer;
    transition: all 0.15s ease;
    text-align: center;
  }

  .action-btn:hover:not(:disabled) {
    border-color: var(--color-primary);
    transform: translateY(-2px);
  }

  .action-btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .action-btn span {
    font-weight: 600;
    font-size: 0.9rem;
  }

  .action-btn p {
    margin: 0;
    font-size: 0.75rem;
    color: var(--text-secondary);
  }

  .action-btn.warning:hover:not(:disabled) {
    border-color: var(--color-warning);
    background: rgba(234, 179, 8, 0.1);
  }

  .action-btn.warning :global(svg) {
    color: var(--color-warning);
  }

  .action-btn.danger:hover:not(:disabled) {
    border-color: var(--color-danger);
    background: rgba(239, 68, 68, 0.1);
  }

  .action-btn.danger :global(svg) {
    color: var(--color-danger);
  }

  /* Dialog styles */
  .dialog-overlay {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.5);
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 1000;
    padding: 1rem;
  }

  .dialog {
    width: 100%;
    max-width: 480px;
    padding: 1.5rem;
  }

  .dialog h3 {
    margin: 0 0 0.5rem;
    font-size: 1.1rem;
    font-weight: 600;
  }

  .dialog > p {
    margin: 0 0 1.25rem;
    color: var(--text-secondary);
    font-size: 0.9rem;
  }

  .dialog-content {
    display: grid;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }

  .form-label {
    display: grid;
    gap: 0.35rem;
    font-size: 0.85rem;
    font-weight: 500;
  }

  .form-input {
    padding: 0.6rem 0.75rem;
    border-radius: var(--radius-md);
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    color: var(--text-primary);
    font-size: 0.9rem;
  }

  .form-input:focus {
    outline: none;
    border-color: var(--color-primary);
    box-shadow: 0 0 0 2px rgba(14, 165, 233, 0.2);
  }

  .dialog-actions {
    display: flex;
    justify-content: flex-end;
    gap: 0.75rem;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }

  /* Button styles */
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.5rem;
    padding: 0.5rem 1rem;
    border-radius: var(--radius-md);
    font-size: 0.875rem;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.15s ease;
    border: 1px solid transparent;
  }

  .btn:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .btn-primary {
    background: var(--color-primary);
    color: white;
  }

  .btn-primary:hover:not(:disabled) {
    background: var(--color-primary-hover);
  }

  .btn-secondary {
    background: var(--bg-secondary);
    border-color: var(--border);
    color: var(--text-primary);
  }

  .btn-secondary:hover:not(:disabled) {
    border-color: var(--color-primary);
  }

  .btn-ghost {
    background: transparent;
    color: var(--text-secondary);
  }

  .btn-ghost:hover:not(:disabled) {
    background: var(--bg-secondary);
    color: var(--text-primary);
  }

  .btn-sm {
    padding: 0.35rem 0.75rem;
    font-size: 0.8rem;
  }

  .spinner {
    width: 2rem;
    height: 2rem;
    border: 3px solid var(--border);
    border-top-color: var(--color-primary);
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
</style>
