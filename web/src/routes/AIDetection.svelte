<script lang="ts">
  import { onMount } from 'svelte';
  import {
    getAiStatus,
    subscribeAiEvents,
    subscribeMultimodalEvents,
    getMultimodalStatus,
    listAiDetections,
    listAiAnalyses,
    listCameras,
  } from '$lib/api';
  import type { AiStatusResponse, AiDetectionEvent, Camera, MultimodalStatus, MultimodalAnalysisEvent } from '$lib/api';
  import { showToast } from '$lib/toast';
  import {
    Brain, RefreshCw, Activity,
    AlertTriangle, CheckCircle, XCircle, Cpu,
    Eye, MessageSquare, Clock, Camera as CameraIcon,
  } from 'lucide-svelte';

  // State
  let aiStatus = $state<AiStatusResponse | null>(null);
  let multimodalStatus = $state<MultimodalStatus | null>(null);
  let loading = $state(true);
  let cameras = $state<Camera[]>([]);
  let events = $state<AiDetectionEvent[]>([]);
  let multimodalEvents = $state<MultimodalAnalysisEvent[]>([]);
  let eventCleanup: (() => void) | null = null;
  let multimodalCleanup: (() => void) | null = null;
  let selectedCamera = $state<string>('all');
  let activeTab = $state<'detection' | 'analysis'>('detection');
  let selectedImage = $state<string | null>(null);

  // Load AI status
  async function loadAiStatus() {
    loading = true;
    try {
      aiStatus = await getAiStatus();
      if (aiStatus.backend === 'multimodal') {
        multimodalStatus = await getMultimodalStatus();
      }
    } catch (e) {
      console.warn('Failed to load AI status:', e);
    } finally {
      loading = false;
    }
  }

  // Load cameras
  async function loadCameras() {
    try {
      cameras = await listCameras() || [];
    } catch (e) {
      console.warn('Failed to load cameras:', e);
    }
  }

  async function loadHistory() {
    try {
      const [detectionHistory, analysisHistory] = await Promise.all([
        listAiDetections({ limit: 100 }),
        listAiAnalyses({ limit: 50 }),
      ]);
      events = detectionHistory.detections || [];
      multimodalEvents = analysisHistory.analyses || [];
    } catch (e) {
      console.warn('Failed to load AI history:', e);
    }
  }

  // Subscribe to SSE events
  function startEventStream() {
    eventCleanup = subscribeAiEvents(
      (event) => {
        events = [event, ...events.slice(0, 99)];
      },
      (error) => {
        console.warn('AI SSE error:', error);
      }
    );
  }

  // Subscribe to multimodal events
  function startMultimodalStream() {
    multimodalCleanup = subscribeMultimodalEvents(
      (event) => {
        multimodalEvents = [event, ...multimodalEvents.slice(0, 49)];
      },
      (error) => {
        console.warn('Multimodal SSE error:', error);
      }
    );
  }

  // Format confidence as percentage
  function formatConfidence(confidence: number): string {
    return `${Math.round(confidence * 100)}%`;
  }

  // Get backend label
  function getBackendLabel(backend: string): string {
    switch (backend) {
      case 'webhook': return 'Webhook 推送';
      case 'disabled': return '已禁用';
      default: return '未知';
    }
  }

  // Get backend color
  function getBackendColor(backend: string): string {
    switch (backend) {
      case 'http': return 'text-blue-500';
      case 'webhook': return 'text-purple-500';
      case 'multimodal': return 'text-green-500';
      case 'disabled': return 'text-gray-500';
      default: return 'text-gray-500';
    }
  }

  // Format timestamp
  function formatTime(ts: number): string {
    return new Date(ts).toLocaleString();
  }

  // Filter events by camera
  function filterByCamera(events: MultimodalAnalysisEvent[]): MultimodalAnalysisEvent[] {
    if (selectedCamera === 'all') return events;
    return events.filter(e => e.camera_id === selectedCamera);
  }

  // Get safety level color
  function getSafetyLevelColor(analysis: string): string {
    if (analysis.includes('危险') || analysis.includes('警告')) {
      return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200';
    }
    if (analysis.includes('注意')) {
      return 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900 dark:text-yellow-200';
    }
    return 'bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200';
  }

  // Extract safety level from analysis
  function extractSafetyLevel(analysis: string): string {
    const levels = ['危险', '警告', '注意', '正常'];
    for (const level of levels) {
      if (analysis.includes(level)) return level;
    }
    return '正常';
  }

  onMount(() => {
    loadAiStatus();
    loadCameras();
    loadHistory();
    startEventStream();
    startMultimodalStream();

    return () => {
      if (eventCleanup) eventCleanup();
      if (multimodalCleanup) multimodalCleanup();
    };
  });
</script>

<main class="max-w-[1400px] mx-auto px-4 sm:px-6 lg:px-8 py-6">
  <div class="flex items-center justify-between mb-6">
    <h1 class="text-2xl font-bold th-text-primary flex items-center gap-2">
      <Brain size={28} />
      AI 智能分析
    </h1>
    <button class="btn btn-ghost" onclick={() => { loadAiStatus(); loadCameras(); }}>
      <RefreshCw size={16} class={loading ? 'animate-spin' : ''} />
    </button>
  </div>

  <!-- AI Status Card -->
  <div class="card border th-border p-6 mb-6">
    <h2 class="text-lg font-semibold th-text-primary mb-4 flex items-center gap-2">
      <Cpu size={20} /> 服务状态
    </h2>

    {#if loading}
      <div class="flex items-center justify-center py-8">
        <RefreshCw class="w-6 h-6 animate-spin th-text-secondary" />
      </div>
    {:else if aiStatus}
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
        <!-- Backend Type -->
        <div class="p-4 rounded-lg bg-gray-50 dark:bg-gray-800">
          <div class="text-sm th-text-secondary mb-1">后端类型</div>
          <div class="flex items-center gap-2">
            {#if aiStatus.backend === 'multimodal'}
              <CheckCircle size={20} class="text-green-500" />
            {:else if aiStatus.backend === 'http'}
              <CheckCircle size={20} class="text-blue-500" />
            {:else if aiStatus.backend === 'webhook'}
              <CheckCircle size={20} class="text-purple-500" />
            {:else}
              <XCircle size={20} class="text-gray-500" />
            {/if}
            <span class="font-medium th-text-primary">{getBackendLabel(aiStatus.backend)}</span>
          </div>
        </div>

        <!-- Available -->
        <div class="p-4 rounded-lg bg-gray-50 dark:bg-gray-800">
          <div class="text-sm th-text-secondary mb-1">可用性</div>
          <div class="flex items-center gap-2">
            {#if aiStatus.available}
              <span class="px-2 py-1 text-xs rounded-full bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200">
                可用
              </span>
            {:else}
              <span class="px-2 py-1 text-xs rounded-full bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200">
                不可用
              </span>
            {/if}
          </div>
        </div>

        <!-- Multimodal Provider -->
        {#if aiStatus.backend === 'multimodal' && multimodalStatus}
          <div class="p-4 rounded-lg bg-gray-50 dark:bg-gray-800">
            <div class="text-sm th-text-secondary mb-1">模型提供商</div>
            <div class="font-medium th-text-primary">
              {multimodalStatus.active_provider || '未配置'}
            </div>
          </div>
        {/if}
      </div>

      <!-- Status Message -->
      {#if aiStatus.reason}
        <div class="mt-4 p-3 rounded-lg bg-yellow-50 dark:bg-yellow-900/20 border border-yellow-200 dark:border-yellow-800">
          <div class="flex items-center gap-2 text-yellow-800 dark:text-yellow-200">
            <AlertTriangle size={16} />
            <span class="text-sm">{aiStatus.reason}</span>
          </div>
        </div>
      {/if}

      <!-- Backend Description -->
      {#if aiStatus.backend === 'multimodal' && aiStatus.available}
        <div class="mt-4 p-3 rounded-lg bg-green-50 dark:bg-green-900/20 border border-green-200 dark:border-green-800">
          <div class="text-sm text-green-800 dark:text-green-200">
            <div class="font-medium mb-1">大模型分析模式</div>
            <p>使用大模型对监控画面进行智能分析，提供场景描述、异常检测和安全建议。</p>
            <p class="mt-1">分析结果将实时显示在下方。</p>
          </div>
        </div>
      {/if}
    {:else}
      <div class="text-center py-8 th-text-secondary">
        无法获取 AI 服务状态
      </div>
    {/if}
  </div>

  <!-- Camera AI Toggle -->
  {#if aiStatus && aiStatus.available}
    <div class="card border th-border p-6 mb-6">
      <h2 class="text-lg font-semibold th-text-primary mb-4 flex items-center gap-2">
        <Activity size={20} /> 摄像头 AI 检测
      </h2>

      {#if cameras.length === 0}
        <div class="text-center py-8 th-text-secondary">
          暂无摄像头
        </div>
      {:else}
        <div class="space-y-3">
          <div class="text-sm th-text-secondary mb-2">
            <Activity size={14} class="inline mr-1" />
            ai-detector 将自动订阅以下 RTSP 摄像头流进行检测
          </div>
          {#each cameras as camera}
            <div class="flex items-center justify-between p-3 rounded-lg bg-gray-50 dark:bg-gray-800">
              <div>
                <div class="font-medium th-text-primary">{camera.name}</div>
                <div class="text-sm th-text-secondary">{camera.id}</div>
              </div>
              <span class="text-xs px-2 py-1 rounded bg-gray-200 dark:bg-gray-700 th-text-secondary">
                {camera.enabled ? '运行中' : '已停用'}
              </span>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Tab Navigation -->
  <div class="flex gap-2 mb-6">
    <button
      class="px-4 py-2 rounded-lg font-medium transition-colors {activeTab === 'detection' ? 'bg-blue-600 text-white' : 'bg-gray-100 dark:bg-gray-800 th-text-secondary'}"
      onclick={() => activeTab = 'detection'}
    >
      <Eye size={16} class="inline mr-1" />
      实时检测
    </button>
    <button
      class="px-4 py-2 rounded-lg font-medium transition-colors {activeTab === 'analysis' ? 'bg-green-600 text-white' : 'bg-gray-100 dark:bg-gray-800 th-text-secondary'}"
      onclick={() => activeTab = 'analysis'}
    >
      <MessageSquare size={16} class="inline mr-1" />
      大模型分析
    </button>
  </div>

  <!-- Detection Events -->
  {#if activeTab === 'detection'}
    <div class="card border th-border p-6">
      <h2 class="text-lg font-semibold th-text-primary mb-4 flex items-center gap-2">
        <Eye size={20} /> 实时检测事件
      </h2>

      {#if events.length === 0}
        <div class="text-center py-8 th-text-secondary">
          <Eye size={48} class="mx-auto mb-4 opacity-50" />
          <p>暂无检测事件</p>
          <p class="text-sm mt-2">当 AI 检测到目标时，事件将显示在这里</p>
        </div>
      {:else}
        <div class="overflow-x-auto max-h-[500px] overflow-y-auto">
          <table class="w-full">
            <thead class="sticky top-0 bg-white dark:bg-gray-900">
              <tr class="border-b th-border">
                <th class="px-4 py-3 text-left text-sm font-medium th-text-secondary">时间</th>
                <th class="px-4 py-3 text-left text-sm font-medium th-text-secondary">摄像头</th>
                <th class="px-4 py-3 text-left text-sm font-medium th-text-secondary">检测结果</th>
                <th class="px-4 py-3 text-left text-sm font-medium th-text-secondary">数量</th>
              </tr>
            </thead>
            <tbody>
              {#each events as event}
                <tr class="border-b th-border hover:bg-gray-50 dark:hover:bg-gray-800">
                  <td class="px-4 py-3 text-sm th-text-secondary whitespace-nowrap">
                    <Clock size={14} class="inline mr-1" />
                    {formatTime(event.timestamp || event.pts)}
                  </td>
                  <td class="px-4 py-3 text-sm font-mono th-text-primary">
                    <CameraIcon size={14} class="inline mr-1" />
                    {event.camera_id}
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex items-center gap-3">
                      {#if event.image_url}
                        <img 
                          src={event.image_url} 
                          alt="" 
                          class="h-12 w-16 object-cover rounded border th-border cursor-pointer hover:opacity-80 transition-opacity" 
                          onclick={() => selectedImage = event.image_url}
                        />
                      {/if}
                      <div class="flex flex-wrap gap-1">
                        {#each event.detections as det}
                          <span class="px-2 py-1 text-xs rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200">
                            {det.label} ({formatConfidence(det.confidence)}){#if det.track_id != null} #{det.track_id}{/if}
                          </span>
                        {/each}
                      </div>
                    </div>
                  </td>
                  <td class="px-4 py-3 text-sm th-text-secondary">
                    {event.detections.length}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </div>
  {/if}

  <!-- Multimodal Analysis Results -->
  {#if activeTab === 'analysis'}
    <div class="card border th-border p-6">
      <div class="flex items-center justify-between mb-4">
        <h2 class="text-lg font-semibold th-text-primary flex items-center gap-2">
          <MessageSquare size={20} /> 大模型分析结果
        </h2>

        <!-- Camera Filter -->
        <select
          class="input w-48"
          bind:value={selectedCamera}
        >
          <option value="all">所有摄像头</option>
          {#each cameras as camera}
            <option value={camera.id}>{camera.name}</option>
          {/each}
        </select>
      </div>

      {#if multimodalEvents.length === 0}
        <div class="text-center py-12 th-text-secondary">
          <Brain size={48} class="mx-auto mb-4 opacity-50" />
          <p class="text-lg">暂无分析结果</p>
          <p class="text-sm mt-2">大模型分析结果将显示在这里</p>
          <p class="text-sm mt-1">请确保已配置大模型 API 并启用分析功能</p>
        </div>
      {:else}
        <div class="space-y-4 max-h-[600px] overflow-y-auto">
          {#each filterByCamera(multimodalEvents) as event}
            <div class="border rounded-lg th-border overflow-hidden">
              <!-- Header -->
              <div class="p-4 bg-gray-50 dark:bg-gray-800 border-b th-border">
                <div class="flex items-center justify-between">
                  <div class="flex items-center gap-3">
                    <CameraIcon size={18} class="th-text-secondary" />
                    <div>
                      <div class="font-medium th-text-primary">
                        {cameras.find(c => c.id === event.camera_id)?.name || event.camera_id}
                      </div>
                      <div class="text-sm th-text-secondary">
                        <Clock size={12} class="inline mr-1" />
                        {formatTime(event.timestamp)}
                      </div>
                    </div>
                  </div>

                  <!-- Safety Level Badge -->
                  <span class="px-3 py-1 text-sm font-medium rounded-full {getSafetyLevelColor(event.analysis)}">
                    {extractSafetyLevel(event.analysis)}
                  </span>
                </div>
              </div>

              <!-- Analysis Content -->
              <div class="p-4">
                {#if event.image_url}
                  <img src={event.image_url} alt="" class="mb-4 max-h-72 w-full object-contain rounded border th-border bg-black" />
                {/if}
                <!-- Labels -->
                {#if event.labels && event.labels.length > 0}
                  <div class="flex flex-wrap gap-2 mb-3">
                    {#each event.labels as label}
                      <span class="px-2 py-1 text-xs rounded-full bg-gray-100 dark:bg-gray-700 th-text-secondary">
                        {label}
                      </span>
                    {/each}
                  </div>
                {/if}

                <!-- Analysis Text -->
                <div class="prose dark:prose-invert max-w-none">
                  <p class="th-text-primary whitespace-pre-wrap">{event.analysis}</p>
                </div>

                {#if event.trigger_detections && event.trigger_detections.length > 0}
                  <div class="mt-3 flex flex-wrap gap-2">
                    {#each event.trigger_detections as det}
                      <span class="px-2 py-1 text-xs rounded-full bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200">
                        {det.label} ({formatConfidence(det.confidence)}){#if det.track_id != null} #{det.track_id}{/if}
                      </span>
                    {/each}
                  </div>
                {/if}

                <!-- Metadata -->
                {#if event.metadata}
                  <div class="mt-3 pt-3 border-t th-border">
                    <div class="flex flex-wrap gap-4 text-xs th-text-tertiary">
                      {#if event.metadata.provider}
                        <span>提供商: {event.metadata.provider}</span>
                      {/if}
                      {#if event.metadata.model}
                        <span>模型: {event.metadata.model}</span>
                      {/if}
                      {#if event.metadata.latency}
                        <span>耗时: {event.metadata.latency}</span>
                      {/if}
                      {#if event.confidence}
                        <span>置信度: {formatConfidence(event.confidence)}</span>
                      {/if}
                    </div>
                  </div>
                {/if}
              </div>
            </div>
          {/each}
        </div>
      {/if}
    </div>
  {/if}

  <!-- Image Modal -->
  {#if selectedImage}
    <div 
      class="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-4"
      onclick={() => selectedImage = null}
    >
      <div class="relative max-w-4xl max-h-[90vh]">
        <img 
          src={selectedImage} 
          alt="Detection" 
          class="max-w-full max-h-[90vh] object-contain rounded-lg"
        />
        <button 
          class="absolute top-2 right-2 bg-white/90 dark:bg-gray-800/90 rounded-full p-2 hover:bg-white dark:hover:bg-gray-800 transition-colors"
          onclick={() => selectedImage = null}
        >
          <XCircle size={24} />
        </button>
      </div>
    </div>
  {/if}
</main>
