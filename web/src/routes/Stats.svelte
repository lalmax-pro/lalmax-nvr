<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    getStats, listCameras, healthCheck, getSystemStats, getMergeStatus, getMergePending,
    getLocalNetworkInterfaces, getStatsTrends,
    getSystemMetricsHistory, getHourlyStats, getCameraUptimeStats, getAPIObservability,
  } from '$lib/api';
  import type {
    StorageStats, Camera, HealthResponse, SystemStats, MergeStatus, MergePending,
    NetworkInterface, SystemMetricSample, HourlyStats, CameraUptimeStat, APIObservabilityResponse,
  } from '$lib/api';
  import { t } from '$lib/i18n';
  import { formatFileSize, formatDate } from '$lib/format';

  import {
    HardDrive, BarChart3, Video, CameraIcon, Cpu, MemoryStick, Wifi,
    ChevronDown, ChevronUp, GitMerge, Activity, Clock, Server, Gauge,
  } from 'lucide-svelte';
  import {
    loadChart, createTrendChart, createCameraChart, aggregateCameraTotals, BAR_COLORS,
    createSystemMetricChart, updateSystemMetricChart, createHourlyActivityChart,
    createAPIRequestChart, createAPILatencyChart, updateAPIChart,
  } from '$lib/charts';

  // ── Tabs ──────────────────────────────────────────────────────────────────

  type StatsTab = 'overview' | 'resources' | 'api' | 'recordings' | 'cameras' | 'system';
  let activeTab = $state<StatsTab>('overview');

  const tabs: { id: StatsTab; labelKey: string; icon: any }[] = [
    { id: 'overview',   labelKey: 'stats.tabOverview',   icon: BarChart3 },
    { id: 'resources',  labelKey: 'stats.tabResources',  icon: Activity },
    { id: 'api',        labelKey: 'stats.tabAPI',        icon: Gauge },
    { id: 'recordings', labelKey: 'stats.tabRecordings', icon: Video },
    { id: 'cameras',    labelKey: 'stats.tabCameras',    icon: CameraIcon },
    { id: 'system',     labelKey: 'stats.tabSystem',     icon: Server },
  ];

  // ── Shared state ──────────────────────────────────────────────────────────

  let stats = $state<StorageStats | null>(null);
  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let error = $state('');
  let health = $state<HealthResponse | null>(null);

  // Instantaneous system resource
  let prevSystemStats = $state<SystemStats | null>(null);
  let currentSystemStats = $state<SystemStats | null>(null);
  let cpuPercent = $state<string | null>(null);
  let memoryPercent = $state<string | null>(null);
  let netRateUp = $state<string | null>(null);
  let netRateDown = $state<string | null>(null);

  // Auto-refresh
  let refreshInterval: number;

  // ── Resources tab ─────────────────────────────────────────────────────────

  let sysHistoryPeriod = $state<'5m' | '15m' | '30m' | '1h' | '6h' | '24h'>('15m');
  let sysHistorySamples = $state<SystemMetricSample[]>([]);
  let sysHistoryLoading = $state(false);
  let cpuChart: any = null;
  let memChart: any = null;
  let netChart: any = null;
  let goroutineChart: any = null;
  let sysChartsMounted = false;

  // ── API observability tab ────────────────────────────────────────────────

  let apiWindow = $state<'1m' | '5m' | '15m' | '30m' | '1h'>('5m');
  let apiObservability = $state<APIObservabilityResponse | null>(null);
  let apiLoading = $state(false);
  let apiError = $state('');
  let apiRequestChart: any = null;
  let apiLatencyChart: any = null;
  let apiAbortController: AbortController | null = null;
  let apiRefreshInterval: number;

  // ── Recordings tab ────────────────────────────────────────────────────────

  let hourlyPeriod = $state<24 | 48 | 168>(24);
  let hourlyData = $state<HourlyStats[]>([]);
  let hourlyLoading = $state(false);
  let hourlyChart: any = null;

  let trendDays = $state<7 | 14 | 30>(7);
  let trendChart: any = null;
  let cameraChart: any = null;
  let ChartJs: any = null;
  let selectedCameras = $state<Set<string>>(new Set());
  let lastTrends: any = null;
  let lastCameraTotals: Record<string, number> = {};
  let allCameraNames = $state<string[]>([]);

  // ── Cameras tab ───────────────────────────────────────────────────────────

  let stabilityDays = $state<7 | 14 | 30>(7);
  let stabilityData = $state<CameraUptimeStat[]>([]);
  let stabilityLoading = $state(false);

  // ── System tab ────────────────────────────────────────────────────────────

  let networkInterfaces = $state<NetworkInterface[]>([]);
  let networkInterfacesLoading = $state(true);
  let networkInterfacesError = $state('');
  let mergeStatus = $state<MergeStatus | null>(null);
  let mergePending = $state<MergePending | null>(null);
  let mergeCollapsed = $state(false);

  // ── Helpers ───────────────────────────────────────────────────────────────

  function formatPercentage(used: number, total: number): string {
    if (total === 0) return '0%';
    return `${((used / total) * 100).toFixed(1)}%`;
  }

  function getUsageColor(pct: number): string {
    if (pct < 50) return 'bg-[var(--color-success)]';
    if (pct < 80) return 'bg-[var(--color-warning)]';
    return 'th-bg-danger';
  }

  function getHealthDotColor(status: string): string {
    if (status === 'ok') return 'bg-[var(--color-success)]';
    if (status === 'degraded' || status === 'warning') return 'bg-[var(--color-warning)]';
    return 'bg-[var(--color-danger)]';
  }

  function getHealthBadgeClass(status: string): string {
    if (status === 'ok') return 'badge-success';
    if (status === 'degraded') return 'badge-warning';
    return 'badge-error';
  }

  function getHealthLabel(status: string): string {
    if (status === 'ok') return t('stats.healthy');
    if (status === 'degraded') return t('stats.degraded');
    return t('stats.unhealthy');
  }

  function parseGoroutineCount(msg?: string): string {
    if (!msg) return '—';
    const match = msg.match(/(\d+)/);
    return match ? match[1] : msg;
  }

  function formatOS(os?: string): string {
    if (os === 'darwin') return 'macOS';
    if (os === 'linux') return 'Linux';
    if (os === 'windows') return 'Windows';
    return os || '--';
  }

  function stabilityBadge(losses: number): { cls: string; label: string } {
    if (losses === 0) return { cls: 'badge-success', label: t('stats.stabilityGood') };
    if (losses <= 3) return { cls: 'badge-warning', label: t('stats.stabilityFair') };
    return { cls: 'badge-error', label: t('stats.stabilityPoor') };
  }

  // ── Data loaders ──────────────────────────────────────────────────────────

  async function loadStats() {
    loading = true; error = '';
    try { stats = await getStats(); }
    catch (e) { error = e instanceof Error ? e.message : t('common.failedLoadStats'); }
    finally { loading = false; }
  }

  async function loadCameras() {
    try { cameras = await listCameras(); }
    catch (e) { console.error(e); }
  }

  async function loadHealth() {
    try { health = await healthCheck(); }
    catch (e) { console.error(e); }
  }

  async function loadSystemStats() {
    try {
      const s = await getSystemStats();
      currentSystemStats = s;
      if (prevSystemStats) {
        const dt = s.timestamp - prevSystemStats.timestamp;
        if (dt > 0) {
          const totalDelta = s.cpu.total - prevSystemStats.cpu.total;
          const idleDelta  = s.cpu.idle  - prevSystemStats.cpu.idle;
          if (totalDelta > 0) cpuPercent = ((totalDelta - idleDelta) / totalDelta * 100).toFixed(1) + '%';
          netRateUp   = formatFileSize((s.network.bytes_sent - prevSystemStats.network.bytes_sent) / dt) + '/s';
          netRateDown = formatFileSize((s.network.bytes_recv - prevSystemStats.network.bytes_recv) / dt) + '/s';
        }
      }
      if (s.memory.total > 0) memoryPercent = ((s.memory.total - s.memory.available) / s.memory.total * 100).toFixed(1) + '%';
      prevSystemStats = s;
    } catch (e) { console.error(e); }
  }

  async function loadSystemHistory() {
    sysHistoryLoading = true;
    try {
      sysHistorySamples = await getSystemMetricsHistory(sysHistoryPeriod);
      if (!ChartJs) ChartJs = await loadChart();
      if (cpuChart && memChart && netChart && goroutineChart) {
        // Charts already exist — update data in place, no flicker
        updateSystemMetricChart(cpuChart, sysHistorySamples, 'cpu');
        updateSystemMetricChart(memChart, sysHistorySamples, 'mem');
        updateSystemMetricChart(netChart, sysHistorySamples, 'net');
        updateSystemMetricChart(goroutineChart, sysHistorySamples, 'goroutines');
      } else {
        window.setTimeout(() => buildSysCharts(), 30);
      }
    } catch (e) { console.error(e); }
    finally { sysHistoryLoading = false; }
  }

  async function loadAPIObservability() {
    apiAbortController?.abort();
    const controller = new AbortController();
    apiAbortController = controller;
    apiLoading = apiObservability === null;
    apiError = '';
    try {
      apiObservability = await getAPIObservability(apiWindow, controller.signal);
      if (!ChartJs) ChartJs = await loadChart();
      window.setTimeout(() => buildAPICharts(), 30);
    } catch (e) {
      if (e instanceof DOMException && e.name === 'AbortError') return;
      apiError = e instanceof Error ? e.message : t('stats.apiLoadFailed');
    } finally {
      if (apiAbortController === controller) {
        apiLoading = false;
        apiAbortController = null;
      }
    }
  }

  async function loadHourlyStats() {
    hourlyLoading = true;
    try {
      hourlyData = await getHourlyStats(hourlyPeriod);
      if (!ChartJs) ChartJs = await loadChart();
      window.setTimeout(() => buildHourlyChart(), 30);
    } catch (e) { console.error(e); }
    finally { hourlyLoading = false; }
  }

  async function loadTrends() {
    try {
      const trends = await getStatsTrends(trendDays);
      if (trends && trends.length > 0) {
        if (!ChartJs) ChartJs = await loadChart();
        buildTrendCharts(trends);
      }
    } catch (e) { console.error(e); }
  }

  async function loadStabilityData() {
    stabilityLoading = true;
    try { stabilityData = await getCameraUptimeStats(stabilityDays); }
    catch (e) { console.error(e); }
    finally { stabilityLoading = false; }
  }

  async function loadNetworkInterfaces() {
    networkInterfacesLoading = true; networkInterfacesError = '';
    try {
      const data = await getLocalNetworkInterfaces();
      networkInterfaces = data.interfaces || [];
    } catch (e) {
      networkInterfacesError = e instanceof Error ? e.message : t('stats.networkInterfacesLoadFailed');
    } finally { networkInterfacesLoading = false; }
  }

  async function loadMergeData() {
    try {
      const [s, p] = await Promise.all([getMergeStatus(), getMergePending()]);
      mergeStatus = s; mergePending = p;
    } catch (e) { console.warn(e); }
  }

  // ── Chart builders ────────────────────────────────────────────────────────

  function buildSysCharts() {
    if (!ChartJs || sysHistorySamples.length === 0) return;
    destroySysCharts();
    const cpuCtx        = document.getElementById('cpuChart')        as HTMLCanvasElement;
    const memCtx        = document.getElementById('memChart')        as HTMLCanvasElement;
    const netCtx        = document.getElementById('netChart')        as HTMLCanvasElement;
    const goroutineCtx  = document.getElementById('goroutineChart')  as HTMLCanvasElement;
    if (cpuCtx)       cpuChart       = createSystemMetricChart(ChartJs, cpuCtx,       sysHistorySamples, 'cpu',        t('stats.cpuHistory'));
    if (memCtx)       memChart       = createSystemMetricChart(ChartJs, memCtx,       sysHistorySamples, 'mem',        t('stats.memHistory'));
    if (netCtx)       netChart       = createSystemMetricChart(ChartJs, netCtx,       sysHistorySamples, 'net',        t('stats.netHistory'));
    if (goroutineCtx) goroutineChart = createSystemMetricChart(ChartJs, goroutineCtx, sysHistorySamples, 'goroutines', t('stats.goroutinesHistory'));
  }

  function destroySysCharts() {
    if (cpuChart)       { cpuChart.destroy();       cpuChart       = null; }
    if (memChart)       { memChart.destroy();       memChart       = null; }
    if (netChart)       { netChart.destroy();       netChart       = null; }
    if (goroutineChart) { goroutineChart.destroy(); goroutineChart = null; }
  }

  function buildAPICharts() {
    if (!ChartJs || !apiObservability) return;
    if (apiRequestChart && apiLatencyChart) {
      updateAPIChart(apiRequestChart, apiObservability.series, 'requests');
      updateAPIChart(apiLatencyChart, apiObservability.series, 'latency');
      return;
    }
    destroyAPICharts();
    const requestCanvas = document.getElementById('apiRequestChart') as HTMLCanvasElement;
    const latencyCanvas = document.getElementById('apiLatencyChart') as HTMLCanvasElement;
    if (requestCanvas) {
      apiRequestChart = createAPIRequestChart(
        ChartJs, requestCanvas, apiObservability.series,
        t('stats.apiRPS'), t('stats.apiServerErrors'),
      );
    }
    if (latencyCanvas) {
      apiLatencyChart = createAPILatencyChart(
        ChartJs, latencyCanvas, apiObservability.series,
        t('stats.apiP95'), t('stats.apiErrorRate'),
      );
    }
  }

  function destroyAPICharts() {
    if (apiRequestChart) { apiRequestChart.destroy(); apiRequestChart = null; }
    if (apiLatencyChart) { apiLatencyChart.destroy(); apiLatencyChart = null; }
  }

  function formatRate(rate: number): string {
    return `${(rate * 100).toFixed(rate > 0 && rate < 0.01 ? 2 : 1)}%`;
  }

  function formatLatency(ms: number): string {
    if (ms >= 1000) return `${(ms / 1000).toFixed(2)} s`;
    return `${ms.toFixed(ms < 10 ? 1 : 0)} ms`;
  }

  function methodBadgeClass(method: string): string {
    if (method === 'GET') return 'badge-info';
    if (method === 'POST') return 'badge-success';
    if (method === 'DELETE') return 'badge-error';
    return 'badge-neutral';
  }

  function buildHourlyChart() {
    if (!ChartJs) return;
    if (hourlyChart) { hourlyChart.destroy(); hourlyChart = null; }
    const ctx = document.getElementById('hourlyChart') as HTMLCanvasElement;
    if (ctx) hourlyChart = createHourlyActivityChart(ChartJs, ctx, hourlyData);
  }

  function buildTrendCharts(trends: { date: string; total_size: number; cameras?: Record<string, number> }[]) {
    const cameraTotals = aggregateCameraTotals(trends);
    lastCameraTotals = cameraTotals;
    lastTrends = trends;
    allCameraNames = Object.keys(cameraTotals);
    if (selectedCameras.size === 0 && allCameraNames.length > 0) selectedCameras = new Set(allCameraNames);
    if (trendChart)  { trendChart.destroy();  trendChart  = null; }
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
    const trendCtx  = document.getElementById('trendChart')  as HTMLCanvasElement;
    const cameraCtx = document.getElementById('cameraChart') as HTMLCanvasElement;
    if (trendCtx)  trendChart  = createTrendChart(ChartJs, trendCtx,   trends);
    if (cameraCtx) cameraChart = createCameraChart(ChartJs, cameraCtx, cameraTotals, allCameraNames, selectedCameras);
  }

  function buildCameraChart(cameraTotals: Record<string, number>) {
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
    const ctx = document.getElementById('cameraChart') as HTMLCanvasElement;
    if (ctx) cameraChart = createCameraChart(ChartJs, ctx, cameraTotals, allCameraNames, selectedCameras);
  }

  function toggleCameraFilter(name: string) {
    const s = new Set(selectedCameras);
    s.has(name) ? s.delete(name) : s.add(name);
    selectedCameras = s;
    buildCameraChart(lastCameraTotals);
  }

  function selectAllCameras()   { selectedCameras = new Set(allCameraNames); buildCameraChart(lastCameraTotals); }
  function deselectAllCameras() { selectedCameras = new Set();              buildCameraChart(lastCameraTotals); }

  // ── Tab switching ─────────────────────────────────────────────────────────

  function switchTab(tab: StatsTab) {
    // Destroy charts of the leaving tab so canvas IDs don't conflict on re-mount
    if (activeTab === 'resources')  destroySysCharts();
    if (activeTab === 'api') destroyAPICharts();
    if (activeTab === 'recordings') {
      if (trendChart)  { trendChart.destroy();  trendChart  = null; }
      if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
      if (hourlyChart) { hourlyChart.destroy(); hourlyChart = null; }
    }

    activeTab = tab;

    // Lazy-load data for the newly activated tab
    window.setTimeout(() => {
      if (tab === 'resources')  loadSystemHistory();
      if (tab === 'api')        loadAPIObservability();
      if (tab === 'recordings') { loadHourlyStats(); loadTrends(); }
      if (tab === 'cameras')    loadStabilityData();
      if (tab === 'system')     { loadNetworkInterfaces(); loadMergeData(); }
    }, 30);
  }

  // ── Reactive: reload when period selectors change ─────────────────────────

  $effect(() => {
    const _p = sysHistoryPeriod;
    if (activeTab === 'resources') loadSystemHistory();
  });

  $effect(() => {
    const _w = apiWindow;
    if (activeTab === 'api') loadAPIObservability();
  });

  $effect(() => {
    const _h = hourlyPeriod;
    if (activeTab === 'recordings') loadHourlyStats();
  });

  $effect(() => {
    const _d = trendDays;
    if (activeTab === 'recordings') loadTrends();
  });

  $effect(() => {
    const _d = stabilityDays;
    if (activeTab === 'cameras') loadStabilityData();
  });

  // ── Theme observer: rebuild visible charts ────────────────────────────────

  let themeObserver: MutationObserver | null = null;

  // ── Lifecycle ─────────────────────────────────────────────────────────────

  onMount(() => {
    // Always load shared data
    loadStats();
    loadCameras();
    loadHealth();
    loadSystemStats();
    window.setTimeout(() => loadSystemStats(), 2000);

    // Load initial tab data
    // (overview needs nothing extra; resources is lazily loaded on tab switch)

    refreshInterval = window.setInterval(() => {
      loadStats();
      loadCameras();
      loadHealth();
      loadSystemStats();
      if (activeTab === 'resources')  loadSystemHistory();
      if (activeTab === 'recordings') { loadTrends(); loadHourlyStats(); }
      if (activeTab === 'cameras')    loadStabilityData();
      if (activeTab === 'system')     { loadNetworkInterfaces(); loadMergeData(); }
    }, 30000);

    apiRefreshInterval = window.setInterval(() => {
      if (activeTab === 'api' && !document.hidden && !apiLoading) loadAPIObservability();
    }, 5000);

    themeObserver = new MutationObserver(() => {
      if (activeTab === 'resources' && sysHistorySamples.length > 0) {
        window.setTimeout(() => buildSysCharts(), 50);
      }
      if (activeTab === 'api' && apiObservability) {
        destroyAPICharts();
        window.setTimeout(() => buildAPICharts(), 50);
      }
      if (activeTab === 'recordings') {
        if (lastTrends) { window.setTimeout(() => buildTrendCharts(lastTrends), 50); }
        if (hourlyData.length > 0) { window.setTimeout(() => buildHourlyChart(), 50); }
      }
    });
    themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });

    return () => {
      clearInterval(refreshInterval);
      clearInterval(apiRefreshInterval);
      apiAbortController?.abort();
      themeObserver?.disconnect();
    };
  });

  onDestroy(() => {
    destroySysCharts();
    destroyAPICharts();
    clearInterval(apiRefreshInterval);
    apiAbortController?.abort();
    if (trendChart)  { trendChart.destroy();  trendChart  = null; }
    if (cameraChart) { cameraChart.destroy(); cameraChart = null; }
    if (hourlyChart) { hourlyChart.destroy(); hourlyChart = null; }
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">

    <!-- Page title -->
    <div class="mb-6">
      <h2 class="text-2xl font-bold th-text-primary">{t('stats.title')}</h2>
    </div>

    {#if error}
      <div class="mb-4 p-4 bg-[rgba(239,68,68,0.3)] border th-border-danger rounded-md th-color-danger">
        {error}
      </div>
    {/if}

    <!-- Tab bar -->
    <div class="flex gap-1 p-1 th-bg-secondary rounded-lg mb-6 overflow-x-auto">
      {#each tabs as tab}
        {@const Icon = tab.icon}
        <button
          onclick={() => switchTab(tab.id)}
          class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium whitespace-nowrap transition-colors
            {activeTab === tab.id
              ? 'bg-white dark:bg-gray-700 shadow-sm th-text-primary'
              : 'th-text-secondary hover:th-text-primary'}"
        >
          <Icon class="w-4 h-4" />
          {t(tab.labelKey)}
        </button>
      {/each}
    </div>

    <!-- Loading state (initial) -->
    {#if loading && !stats}
      <div class="flex justify-center items-center h-64">
        <div class="spinner spinner-lg"></div>
      </div>

    {:else if stats}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: Overview                                                      -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'overview'}
        <div class="space-y-6">

          <!-- Summary cards -->
          <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
            <div class="card p-5 border th-border">
              <div class="flex items-center justify-between mb-3">
                <h3 class="text-sm font-medium th-text-muted">{t('stats.totalStorage')}</h3>
                <HardDrive size={18} class="th-text-secondary" />
              </div>
              <p class="text-2xl font-bold th-text-primary">{formatFileSize(stats.total_bytes)}</p>
            </div>
            <div class="card p-5 border th-border">
              <div class="flex items-center justify-between mb-3">
                <h3 class="text-sm font-medium th-text-muted">{t('stats.used')}</h3>
                <BarChart3 size={18} class="th-text-secondary" />
              </div>
              <p class="text-2xl font-bold th-text-primary">
                {formatFileSize(stats.used_bytes)}
                <span class="text-sm font-normal th-text-muted">{formatPercentage(stats.used_bytes, stats.total_bytes)}</span>
              </p>
            </div>
            <div class="card p-5 border th-border">
              <div class="flex items-center justify-between mb-3">
                <h3 class="text-sm font-medium th-text-muted">{t('stats.totalRecordings')}</h3>
                <Video size={18} class="th-text-secondary" />
              </div>
              <p class="text-2xl font-bold th-text-primary">{stats.recording_count.toLocaleString()}</p>
            </div>
            <div class="card p-5 border th-border">
              <div class="flex items-center justify-between mb-3">
                <h3 class="text-sm font-medium th-text-muted">{t('stats.activeCameras')}</h3>
                <CameraIcon size={18} class="th-text-secondary" />
              </div>
              <p class="text-2xl font-bold th-text-primary">
                {cameras.filter(c => c.enabled).length}/{cameras.length}
              </p>
            </div>
          </div>

          <!-- Storage bar + system metrics side-by-side -->
          <div class="grid grid-cols-1 lg:grid-cols-3 gap-4">
            <div class="card p-5 border th-border lg:col-span-2">
              <h3 class="text-lg font-semibold th-text-primary mb-4">{t('stats.storageUsage')}</h3>
              <div class="flex justify-between text-sm mb-2">
                <span class="th-text-muted">{t('stats.usedOf', { used: formatFileSize(stats.used_bytes) })}</span>
                <span class="th-text-muted">{t('stats.freeOf', { free: formatFileSize(stats.total_bytes - stats.used_bytes) })}</span>
              </div>
              <div class="w-full th-bg-tertiary rounded-full h-4 overflow-hidden">
                <div
                  class="h-full {getUsageColor((stats.used_bytes / stats.total_bytes) * 100)} transition-all duration-500"
                  style="width: {formatPercentage(stats.used_bytes, stats.total_bytes)}"
                ></div>
              </div>
              <p class="text-sm th-text-muted mt-2">{t('stats.ofStorageUsed', { percentage: formatPercentage(stats.used_bytes, stats.total_bytes) })}</p>
            </div>

            <!-- Instant system resource mini-card -->
            <div class="card p-5 border th-border">
              <h3 class="text-sm font-semibold th-text-primary mb-3">{t('stats.systemStatus')}</h3>
              <div class="space-y-3">
                <div>
                  <div class="flex justify-between text-xs th-text-muted mb-1">
                    <span class="flex items-center gap-1"><Cpu size={12} />{t('stats.cpu')}</span>
                    <span class="font-medium th-text-primary">{cpuPercent ?? '--'}</span>
                  </div>
                  <div class="w-full th-bg-tertiary rounded-full h-1.5 overflow-hidden">
                    {#if cpuPercent}
                      <div class="h-full {getUsageColor(parseFloat(cpuPercent))} transition-all duration-500" style="width: {cpuPercent}"></div>
                    {/if}
                  </div>
                </div>
                <div>
                  <div class="flex justify-between text-xs th-text-muted mb-1">
                    <span class="flex items-center gap-1"><MemoryStick size={12} />{t('stats.memory')}</span>
                    <span class="font-medium th-text-primary">{memoryPercent ?? '--'}</span>
                  </div>
                  <div class="w-full th-bg-tertiary rounded-full h-1.5 overflow-hidden">
                    {#if memoryPercent}
                      <div class="h-full {getUsageColor(parseFloat(memoryPercent))} transition-all duration-500" style="width: {memoryPercent}"></div>
                    {/if}
                  </div>
                </div>
                <div class="flex justify-between text-xs">
                  <span class="th-text-muted flex items-center gap-1"><Wifi size={12} />{t('stats.network')}</span>
                  <span class="th-text-primary font-mono text-[11px]">↑{netRateUp ?? '--'} ↓{netRateDown ?? '--'}</span>
                </div>
                {#if health}
                  <div class="flex items-center gap-2 pt-1 border-t th-border">
                    <span class="inline-block w-2 h-2 rounded-full {getHealthDotColor(health.status)}"></span>
                    <span class="badge {getHealthBadgeClass(health.status)} text-xs">{getHealthLabel(health.status)}</span>
                  </div>
                  {#if health.uptime}
                    <div class="text-xs th-text-muted flex justify-between pt-0.5">
                      <span>{t('stats.uptime')}</span>
                      <span class="th-text-primary font-medium">{health.uptime}</span>
                    </div>
                  {/if}
                {/if}
              </div>
            </div>
          </div>

          <!-- NVR runtime + health detail -->
          {#if health}
            <div class="card p-5 border th-border">
              <h3 class="text-lg font-semibold th-text-primary mb-4">{t('stats.nvrRuntime')}</h3>
              <div class="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-x-6 gap-y-4">

                <!-- Start time -->
                <div class="sm:col-span-2">
                  <p class="text-xs th-text-muted mb-1">{t('stats.startTime')}</p>
                  <p class="text-base font-semibold th-text-primary">
                    {#if health.start_time}
                      {new Date(health.start_time).toLocaleString()}
                    {:else}
                      --
                    {/if}
                  </p>
                </div>

                <!-- Uptime -->
                <div class="sm:col-span-1">
                  <p class="text-xs th-text-muted mb-1">{t('stats.uptime')}</p>
                  <p class="text-base font-semibold th-text-primary">{health.uptime || '--'}</p>
                </div>

                <!-- OS + cores -->
                <div>
                  <p class="text-xs th-text-muted mb-1">{t('stats.system')}</p>
                  <p class="text-sm font-medium th-text-primary">
                    {currentSystemStats ? `${formatOS(currentSystemStats.system.os)}` : '--'}
                  </p>
                  {#if currentSystemStats}
                    <p class="text-xs th-text-muted">{currentSystemStats.system.cpu_cores} {t('stats.cpuCores')}</p>
                  {/if}
                </div>

                <!-- Goroutines -->
                <div>
                  <p class="text-xs th-text-muted mb-1">{t('stats.goroutines')}</p>
                  <p class="text-sm font-medium th-text-primary">{parseGoroutineCount(health.checks?.goroutines?.message)}</p>
                </div>

                <!-- Checks -->
                <div>
                  <p class="text-xs th-text-muted mb-2">{t('stats.checks')}</p>
                  <div class="flex flex-col gap-1 text-xs">
                    <span class="flex items-center gap-1.5">
                      {#if health.checks?.database?.status === 'ok'}
                        <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success)]"></span>
                      {:else}
                        <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-danger)]"></span>
                      {/if}
                      {t('stats.checkDatabase')}
                    </span>
                    <span class="flex items-center gap-1.5">
                      {#if health.checks?.storage?.status === 'ok'}
                        <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-success)]"></span>
                      {:else if health.checks?.storage?.status === 'warning'}
                        <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-warning)]"></span>
                      {:else}
                        <span class="w-1.5 h-1.5 rounded-full bg-[var(--color-danger)]"></span>
                      {/if}
                      {t('stats.checkStorage')}
                    </span>
                  </div>
                </div>

              </div>
            </div>
          {/if}

        </div>
      {/if}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: Resources                                                     -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'resources'}
        <div class="space-y-6">

          <!-- Period selector -->
          <div class="flex items-center gap-2 flex-wrap">
            <span class="text-sm th-text-muted">{t('stats.timeRange')}:</span>
            {#each (['5m', '15m', '30m', '1h', '6h', '24h'] as const) as p}
              <button
                class="px-3 py-1.5 rounded-md text-sm font-medium transition-colors {sysHistoryPeriod === p ? 'bg-[var(--color-primary)] text-white' : 'card border th-border th-text-secondary hover:th-text-primary'}"
                onclick={() => { sysHistoryPeriod = p; }}
              >
                {t(`stats.period${p}`)}
              </button>
            {/each}
            {#if sysHistoryLoading}
              <span class="spinner ml-1"></span>
            {/if}
          </div>

          {#if sysHistorySamples.length === 0 && !sysHistoryLoading}
            <div class="card p-12 text-center border th-border">
              <Activity size={36} class="th-text-muted mx-auto mb-3" />
              <p class="th-text-muted">{t('stats.noHistoryData')}</p>
            </div>
          {:else}
            <!-- Four charts: 2×2 grid -->
            <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                  <Cpu size={14} class="th-text-muted" />{t('stats.cpuHistory')}
                </h3>
                <div class="h-56">
                  <canvas id="cpuChart"></canvas>
                </div>
              </div>
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                  <MemoryStick size={14} class="th-text-muted" />{t('stats.memHistory')}
                </h3>
                <div class="h-56">
                  <canvas id="memChart"></canvas>
                </div>
              </div>
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                  <Wifi size={14} class="th-text-muted" />{t('stats.netHistory')}
                </h3>
                <div class="h-56">
                  <canvas id="netChart"></canvas>
                </div>
              </div>
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3 flex items-center gap-2">
                  <Activity size={14} class="th-text-muted" />{t('stats.goroutinesHistory')}
                </h3>
                <div class="h-56">
                  <canvas id="goroutineChart"></canvas>
                </div>
              </div>
            </div>

            <!-- Instant snapshot below charts -->
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-4">
              <div class="card p-4 border th-border">
                <p class="text-xs th-text-muted mb-1">{t('stats.cpu')} ({t('stats.timeRange')} 当前)</p>
                <p class="text-xl font-bold th-text-primary">{cpuPercent ?? '--'}</p>
                {#if currentSystemStats}
                  <p class="text-xs th-text-muted mt-1">{currentSystemStats.system.cpu_cores} {t('stats.cpuCores')}</p>
                {/if}
              </div>
              <div class="card p-4 border th-border">
                <p class="text-xs th-text-muted mb-1">{t('stats.memory')} 当前</p>
                <p class="text-xl font-bold th-text-primary">
                  {currentSystemStats ? formatFileSize(currentSystemStats.memory.total - currentSystemStats.memory.available) : '--'}
                </p>
                <p class="text-xs th-text-muted mt-1">{memoryPercent ?? '--'} {t('stats.used')}</p>
              </div>
              <div class="card p-4 border th-border">
                <p class="text-xs th-text-muted mb-1">↑ {t('stats.totalUpload')}</p>
                <p class="text-xl font-bold th-text-primary">{netRateUp ?? '--'}</p>
                <p class="text-xs th-text-muted mt-1">
                  {currentSystemStats ? formatFileSize(currentSystemStats.network.bytes_sent) : '--'} 累计
                </p>
              </div>
              <div class="card p-4 border th-border">
                <p class="text-xs th-text-muted mb-1">↓ {t('stats.totalDownload')}</p>
                <p class="text-xl font-bold th-text-primary">{netRateDown ?? '--'}</p>
                <p class="text-xs th-text-muted mt-1">
                  {currentSystemStats ? formatFileSize(currentSystemStats.network.bytes_recv) : '--'} 累计
                </p>
              </div>
            </div>
          {/if}
        </div>
      {/if}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: API observability                                             -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'api'}
        <div class="space-y-6">
          <div class="flex items-center justify-between gap-3 flex-wrap">
            <div>
              <h3 class="text-lg font-semibold th-text-primary">{t('stats.apiTitle')}</h3>
              <p class="text-xs th-text-muted mt-0.5">{t('stats.apiHint')}</p>
            </div>
            <div class="flex items-center gap-2 flex-wrap">
              <span class="text-sm th-text-muted">{t('stats.timeRange')}:</span>
              {#each (['1m', '5m', '15m', '30m', '1h'] as const) as p}
                <button
                  class="px-3 py-1.5 rounded-md text-sm font-medium transition-colors {apiWindow === p ? 'bg-[var(--color-primary)] text-white' : 'card border th-border th-text-secondary hover:th-text-primary'}"
                  onclick={() => { apiWindow = p; }}
                >
                  {t(`stats.period${p}`)}
                </button>
              {/each}
              {#if apiLoading}<span class="spinner ml-1"></span>{/if}
            </div>
          </div>

          {#if apiError}
            <div class="p-4 bg-[rgba(239,68,68,0.15)] border th-border-danger rounded-md text-[var(--color-danger)]">
              {t('stats.apiLoadFailed')}: {apiError}
            </div>
          {/if}

          {#if apiObservability}
            <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
              <div class="card p-5 border th-border">
                <p class="text-xs th-text-muted mb-2">{t('stats.apiRPS')}</p>
                <p class="text-2xl font-bold th-text-primary">{apiObservability.summary.rps.toFixed(2)}</p>
                <p class="text-xs th-text-muted mt-1">{apiObservability.summary.requests.toLocaleString()} {t('stats.apiRequests')}</p>
              </div>
              <div class="card p-5 border th-border">
                <p class="text-xs th-text-muted mb-2">{t('stats.apiP95')}</p>
                <p class="text-2xl font-bold th-text-primary">{formatLatency(apiObservability.summary.latency.p95_ms)}</p>
                <p class="text-xs th-text-muted mt-1">P99 {formatLatency(apiObservability.summary.latency.p99_ms)}</p>
              </div>
              <div class="card p-5 border th-border">
                <p class="text-xs th-text-muted mb-2">{t('stats.apiErrorRate')}</p>
                <p class="text-2xl font-bold {apiObservability.summary.error_rate > 0 ? 'text-[var(--color-danger)]' : 'th-text-primary'}">
                  {formatRate(apiObservability.summary.error_rate)}
                </p>
                <p class="text-xs th-text-muted mt-1">5xx: {apiObservability.summary.status_5xx}</p>
              </div>
              <div class="card p-5 border th-border">
                <p class="text-xs th-text-muted mb-2">{t('stats.apiInFlight')}</p>
                <p class="text-2xl font-bold th-text-primary">{apiObservability.summary.in_flight}</p>
                <p class="text-xs th-text-muted mt-1">4xx: {apiObservability.summary.status_4xx}</p>
              </div>
            </div>

            <div class="grid grid-cols-1 lg:grid-cols-2 gap-4">
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3">{t('stats.apiTrafficTrend')}</h3>
                <div class="h-56"><canvas id="apiRequestChart"></canvas></div>
              </div>
              <div class="card p-5 border th-border">
                <h3 class="text-sm font-semibold th-text-primary mb-3">{t('stats.apiLatencyTrend')}</h3>
                <div class="h-56"><canvas id="apiLatencyChart"></canvas></div>
              </div>
            </div>

            <div class="card border th-border overflow-hidden">
              <div class="p-5 border-b th-border">
                <h3 class="text-lg font-semibold th-text-primary">{t('stats.apiRoutes')}</h3>
                <p class="text-xs th-text-muted mt-0.5">{t('stats.apiRoutesHint')}</p>
              </div>
              {#if apiObservability.routes.length === 0}
                <div class="p-8 text-center th-text-muted">{t('stats.apiNoData')}</div>
              {:else}
                <div class="table-container border-0 rounded-none overflow-x-auto">
                  <table class="table">
                    <thead>
                      <tr>
                        <th>{t('stats.apiMethod')}</th>
                        <th>{t('stats.apiRoute')}</th>
                        <th class="text-right">{t('stats.apiRequests')}</th>
                        <th class="text-right">RPS</th>
                        <th class="text-right">4xx</th>
                        <th class="text-right">5xx</th>
                        <th class="text-right">P95</th>
                        <th class="text-right">P99</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each apiObservability.routes as row}
                        <tr class="transition-all duration-200 hover:th-bg-hover">
                          <td><span class="badge {methodBadgeClass(row.method)} text-xs">{row.method}</span></td>
                          <td class="font-mono text-xs th-text-primary whitespace-nowrap">{row.route}</td>
                          <td class="text-right th-text-secondary">{row.requests.toLocaleString()}</td>
                          <td class="text-right th-text-secondary">{row.rps.toFixed(2)}</td>
                          <td class="text-right {row.status_4xx > 0 ? 'text-[var(--color-warning)]' : 'th-text-muted'}">{row.status_4xx}</td>
                          <td class="text-right {row.status_5xx > 0 ? 'text-[var(--color-danger)] font-semibold' : 'th-text-muted'}">{row.status_5xx}</td>
                          <td class="text-right th-text-secondary whitespace-nowrap">{formatLatency(row.p95_ms)}</td>
                          <td class="text-right th-text-secondary whitespace-nowrap">{formatLatency(row.p99_ms)}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              {/if}
            </div>

            {#if apiObservability.recent_errors.length > 0}
              <div class="card border th-border overflow-hidden">
                <div class="p-5 border-b th-border">
                  <h3 class="text-lg font-semibold text-[var(--color-danger)]">{t('stats.apiRecentErrors')}</h3>
                </div>
                <div class="table-container border-0 rounded-none overflow-x-auto">
                  <table class="table">
                    <thead>
                      <tr>
                        <th>{t('stats.apiTime')}</th>
                        <th>{t('stats.apiMethod')}</th>
                        <th>{t('stats.apiRoute')}</th>
                        <th>{t('stats.apiStatus')}</th>
                        <th>{t('stats.apiDuration')}</th>
                        <th>Trace ID</th>
                      </tr>
                    </thead>
                    <tbody>
                      {#each apiObservability.recent_errors as item}
                        <tr>
                          <td class="text-xs th-text-muted whitespace-nowrap">{new Date(item.timestamp * 1000).toLocaleTimeString()}</td>
                          <td><span class="badge {methodBadgeClass(item.method)} text-xs">{item.method}</span></td>
                          <td class="font-mono text-xs th-text-primary whitespace-nowrap">{item.route}</td>
                          <td><span class="badge badge-error">{item.status}</span></td>
                          <td class="th-text-secondary whitespace-nowrap">{formatLatency(item.duration_ms)}</td>
                          <td class="font-mono text-xs th-text-muted" title={item.trace_id || ''}>{item.trace_id ? item.trace_id.slice(0, 16) : '—'}</td>
                        </tr>
                      {/each}
                    </tbody>
                  </table>
                </div>
              </div>
            {/if}
          {:else if apiLoading}
            <div class="card p-12 text-center border th-border">
              <div class="spinner spinner-lg mx-auto mb-3"></div>
              <p class="th-text-muted">{t('common.loading')}</p>
            </div>
          {/if}
        </div>
      {/if}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: Recordings                                                    -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'recordings'}
        <div class="space-y-6">

          <!-- Hourly activity -->
          <div class="card border th-border overflow-hidden">
            <div class="p-5 border-b th-border flex items-center justify-between flex-wrap gap-3">
              <div>
                <h3 class="text-lg font-semibold th-text-primary flex items-center gap-2">
                  <Clock size={18} class="text-accent" />{t('stats.recordingActivity')}
                </h3>
                <p class="text-xs th-text-muted mt-0.5">{t('stats.recordingActivityHint')}</p>
              </div>
              <div class="flex items-center gap-2">
                {#each ([24, 48, 168] as const) as h}
                  <button
                    class="px-3 py-1 rounded-md text-xs font-medium transition-colors {hourlyPeriod === h ? 'bg-[var(--color-primary)] text-white' : 'th-bg-tertiary th-text-secondary hover:th-text-primary'}"
                    onclick={() => { hourlyPeriod = h; }}
                  >
                    {t(`stats.hours${h}`)}
                  </button>
                {/each}
                {#if hourlyLoading}<span class="spinner ml-1"></span>{/if}
              </div>
            </div>
            <div class="p-5">
              {#if hourlyData.length === 0}
                <p class="text-sm th-text-muted text-center py-8">{t('stats.noHourlyData')}</p>
              {:else}
                <div class="h-56">
                  <canvas id="hourlyChart"></canvas>
                </div>
              {/if}
            </div>
          </div>

          <!-- Storage trend -->
          <div class="card border th-border overflow-hidden">
            <div class="p-5 border-b th-border flex items-center justify-between flex-wrap gap-3">
              <h3 class="text-lg font-semibold th-text-primary">{t('stats.storageTrend')}</h3>
              <div class="flex items-center gap-2">
                {#each ([7, 14, 30] as const) as d}
                  <button
                    class="px-3 py-1 rounded-md text-xs font-medium transition-colors {trendDays === d ? 'bg-[var(--color-primary)] text-white' : 'th-bg-tertiary th-text-secondary hover:th-text-primary'}"
                    onclick={() => { trendDays = d; }}
                  >
                    {t(`stats.days${d}`)}
                  </button>
                {/each}
              </div>
            </div>
            <div class="p-5">
              <div class="h-56">
                <canvas id="trendChart"></canvas>
              </div>
            </div>
          </div>

          <!-- Recordings by camera -->
          <div class="card border th-border overflow-hidden">
            <div class="p-5 border-b th-border">
              <h3 class="text-lg font-semibold th-text-primary">{t('stats.recordingsByCamera')}</h3>
            </div>
            {#if allCameraNames.length > 1}
              <div class="px-5 py-3 border-b th-border">
                <div class="flex items-center gap-2 mb-2">
                  <span class="text-xs th-text-muted">{t('stats.filterCameras')}</span>
                  <button class="text-xs text-[var(--color-primary)] hover:underline" onclick={selectAllCameras}>{t('stats.selectAll')}</button>
                  <span class="text-xs th-text-muted">/</span>
                  <button class="text-xs text-[var(--color-primary)] hover:underline" onclick={deselectAllCameras}>{t('stats.deselectAll')}</button>
                </div>
                <div class="flex flex-wrap gap-2">
                  {#each allCameraNames as name, i}
                    {@const isSelected = selectedCameras.has(name)}
                    <button
                      class="inline-flex items-center gap-1.5 px-3 py-1 rounded-full text-xs font-medium transition-all cursor-pointer"
                      style="background-color: {isSelected ? BAR_COLORS[i % BAR_COLORS.length] : 'var(--bg-tertiary)'}; color: {isSelected ? 'white' : 'var(--text-tertiary)'};"
                      onclick={() => toggleCameraFilter(name)}
                    >{name}</button>
                  {/each}
                </div>
              </div>
            {/if}
            <div class="p-5">
              <div class="h-56">
                <canvas id="cameraChart"></canvas>
              </div>
            </div>
          </div>

        </div>
      {/if}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: Cameras                                                       -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'cameras'}
        <div class="space-y-6">

          <!-- Camera list table -->
          <div class="card border th-border">
            <div class="p-5 border-b th-border">
              <h3 class="text-lg font-semibold th-text-primary">{t('stats.cameras')}</h3>
            </div>
            <div class="table-container border-0 rounded-none">
              {#if cameras.length === 0}
                <div class="p-8 text-center th-text-muted">{t('stats.noCameras')}</div>
              {:else}
                <table class="table">
                  <thead>
                    <tr>
                      <th>{t('stats.tableName')}</th>
                      <th>{t('stats.tableId')}</th>
                      <th>{t('stats.tableProtocol')}</th>
                      <th>{t('stats.tableStatus')}</th>
                      <th>{t('stats.tableCreatedAt')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each cameras as camera}
                      <tr class="transition-all duration-200 hover:th-bg-hover">
                        <td class="font-medium th-text-primary">
                          <span class="inline-block w-2 h-2 rounded-full mr-2 {camera.enabled ? 'bg-[var(--color-success)]' : 'bg-[var(--color-danger)]'}"></span>
                          {camera.name}
                        </td>
                        <td class="th-text-muted font-mono text-sm">{camera.id}</td>
                        <td><span class="badge badge-neutral">{camera.protocol}</span></td>
                        <td>
                          {#if camera.enabled}
                            <span class="badge badge-success">{t('stats.enabled')}</span>
                          {:else}
                            <span class="badge badge-error">{t('stats.disabled')}</span>
                          {/if}
                        </td>
                        <td class="th-text-muted text-sm whitespace-nowrap">
                          {camera.created_at ? new Date(camera.created_at).toLocaleString() : '--'}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              {/if}
            </div>
          </div>

          <!-- Camera stability -->
          <div class="card border th-border overflow-hidden">
            <div class="p-5 border-b th-border flex items-center justify-between flex-wrap gap-3">
              <div>
                <h3 class="text-lg font-semibold th-text-primary">{t('stats.cameraStability')}</h3>
                <p class="text-xs th-text-muted mt-0.5">{t('stats.cameraStabilityHint')}</p>
              </div>
              <div class="flex items-center gap-2">
                {#each ([7, 14, 30] as const) as d}
                  <button
                    class="px-3 py-1 rounded-md text-xs font-medium transition-colors {stabilityDays === d ? 'bg-[var(--color-primary)] text-white' : 'th-bg-tertiary th-text-secondary hover:th-text-primary'}"
                    onclick={() => { stabilityDays = d; }}
                  >
                    {t(`stats.days${d}`)}
                  </button>
                {/each}
                {#if stabilityLoading}<span class="spinner ml-1"></span>{/if}
              </div>
            </div>

            {#if stabilityData.length === 0}
              <p class="text-sm th-text-muted text-center py-8">{t('stats.noStabilityData')}</p>
            {:else}
              <div class="table-container border-0 rounded-none">
                <table class="table">
                  <thead>
                    <tr>
                      <th>{t('stats.colCamera')}</th>
                      <th class="text-center">{t('stats.colLosses')}</th>
                      <th class="text-center">{t('stats.colRestores')}</th>
                      <th class="text-center">{t('stats.colEvents')}</th>
                      <th class="text-center">状态</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each stabilityData as row}
                      {@const badge = stabilityBadge(row.connection_losses)}
                      <tr class="transition-all duration-200 hover:th-bg-hover">
                        <td>
                          <p class="font-medium th-text-primary">{row.camera_name !== row.camera_id ? row.camera_name : row.camera_id}</p>
                          {#if row.camera_name !== row.camera_id}
                            <p class="text-xs th-text-muted font-mono">{row.camera_id}</p>
                          {/if}
                        </td>
                        <td class="text-center">
                          {#if row.connection_losses > 0}
                            <span class="font-semibold text-[var(--color-danger)]">{row.connection_losses}</span>
                          {:else}
                            <span class="th-text-muted">0</span>
                          {/if}
                        </td>
                        <td class="text-center th-text-secondary">{row.connection_restores}</td>
                        <td class="text-center th-text-muted">{row.total_events}</td>
                        <td class="text-center">
                          <span class="badge {badge.cls} text-xs">{badge.label}</span>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>

        </div>
      {/if}

      <!-- ══════════════════════════════════════════════════════════════════ -->
      <!-- TAB: System                                                        -->
      <!-- ══════════════════════════════════════════════════════════════════ -->
      {#if activeTab === 'system'}
        <div class="space-y-6">

          <!-- Network interfaces -->
          <div class="card border th-border">
            <div class="p-5 border-b th-border flex items-center gap-2">
              <Wifi size={18} class="th-text-secondary" />
              <h3 class="text-lg font-semibold th-text-primary">{t('stats.networkInterfaces')}</h3>
            </div>
            {#if networkInterfacesLoading && networkInterfaces.length === 0}
              <div class="p-5 th-text-muted">{t('common.loading')}</div>
            {:else if networkInterfacesError}
              <div class="p-5 text-[var(--color-danger)]">{networkInterfacesError}</div>
            {:else if networkInterfaces.length === 0}
              <div class="p-5 th-text-muted">{t('stats.networkInterfacesEmpty')}</div>
            {:else}
              <div class="table-container border-0 rounded-none">
                <table class="table">
                  <thead>
                    <tr>
                      <th>{t('stats.niName')}</th>
                      <th>{t('stats.niAddresses')}</th>
                      <th>{t('stats.niSpeed')}</th>
                      <th>{t('stats.niMac')}</th>
                      <th>{t('stats.niMTU')}</th>
                      <th>{t('stats.niStatus')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each networkInterfaces as iface}
                      <tr class="transition-all duration-200 hover:th-bg-hover">
                        <td class="font-mono font-medium th-text-primary">{iface.name}</td>
                        <td class="th-text-muted font-mono text-sm">{iface.addresses?.length > 0 ? iface.addresses.join(', ') : '--'}</td>
                        <td><span class="badge {iface.speed === 'unknown' ? 'badge-neutral' : 'badge-info'}">{iface.speed}</span></td>
                        <td class="th-text-muted font-mono text-sm">{iface.hardware_addr || '--'}</td>
                        <td class="th-text-muted">{iface.mtu}</td>
                        <td>
                          {#if iface.is_up}
                            <span class="badge badge-success">{t('stats.niUp')}</span>
                          {:else}
                            <span class="badge badge-danger">{t('stats.niDown')}</span>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            {/if}
          </div>

          <!-- Merge monitoring -->
          {#if mergeStatus}
            <div class="card border th-border overflow-hidden">
              <button
                class="w-full p-5 flex items-center justify-between hover:th-bg-hover transition-colors cursor-pointer"
                onclick={() => mergeCollapsed = !mergeCollapsed}
              >
                <div class="flex items-center gap-2">
                  <GitMerge size={18} class="text-accent" />
                  <h3 class="text-lg font-semibold th-text-primary">{t('merge.status')}</h3>
                  <span class="badge {mergeStatus.enabled ? 'badge-success' : 'badge-neutral'} text-xs">
                    {mergeStatus.enabled ? t('merge.enabled') : t('merge.disabled')}
                  </span>
                  {#if mergePending && mergePending.pending && Object.keys(mergePending.pending).length > 0}
                    {@const total = Object.values(mergePending.pending).reduce((a, b) => a + b, 0)}
                    <span class="badge badge-warning text-xs">{t('merge.pendingCount', { total })}</span>
                  {/if}
                </div>
                {#if mergeCollapsed}
                  <ChevronDown size={18} class="th-text-muted" />
                {:else}
                  <ChevronUp size={18} class="th-text-muted" />
                {/if}
              </button>

              {#if !mergeCollapsed}
                <div class="px-5 pb-5 border-t th-border pt-4">
                  <div class="grid grid-cols-2 sm:grid-cols-4 gap-3 mb-4">
                    <div>
                      <p class="text-xs th-text-muted mb-1">{t('merge.lastRun')}</p>
                      <p class="text-sm font-medium th-text-primary">
                        {mergeStatus.last_run_time && mergeStatus.last_run_time !== '0001-01-01T00:00:00Z'
                          ? formatDate(mergeStatus.last_run_time) : '—'}
                      </p>
                    </div>
                    <div>
                      <p class="text-xs th-text-muted mb-1">{t('merge.segmentsMerged')}</p>
                      <p class="text-sm font-medium th-text-primary">{mergeStatus.segments_merged}</p>
                    </div>
                    <div>
                      <p class="text-xs th-text-muted mb-1">{t('merge.filesCreated')}</p>
                      <p class="text-sm font-medium th-text-primary">{mergeStatus.files_created}</p>
                    </div>
                    <div>
                      <p class="text-xs th-text-muted mb-1">{t('merge.errorCount')}</p>
                      <p class="text-sm font-medium {mergeStatus.error_count > 0 ? 'text-[var(--color-danger)]' : 'th-text-primary'}">
                        {mergeStatus.error_count}
                      </p>
                    </div>
                  </div>
                  {#if mergePending && mergePending.pending && Object.keys(mergePending.pending).length > 0}
                    <p class="text-xs th-text-muted mb-2">{t('merge.pending')}</p>
                    <div class="flex flex-wrap gap-2">
                      {#each Object.entries(mergePending.pending) as [camId, count]}
                        <span class="inline-flex items-center gap-1.5 px-2.5 py-1 rounded-md text-xs th-bg-tertiary th-text-secondary">
                          <span class="font-mono">{camId}</span>
                          <span class="font-semibold th-text-primary">{count}</span>
                        </span>
                      {/each}
                    </div>
                  {:else}
                    <p class="text-xs th-text-muted">{t('merge.noPending')}</p>
                  {/if}
                </div>
              {/if}
            </div>
          {/if}

        </div>
      {/if}

    {/if}
  </main>
</div>
