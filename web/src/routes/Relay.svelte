<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { 
    listRelayTasks, 
    createRelayTask, 
    deleteRelayTask, 
    startRelayTask, 
    stopRelayTask,
    getRelayTaskStats,
    getRelayTaskStatsHistory,
    listStreams
  } from '$lib/api';
  import type { RelayTask, RelayTaskStats, StatsHistory, StreamInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { Chart, registerables } from 'chart.js';

  Chart.register(...registerables);

  let loading = $state(true);
  let error = $state('');
  let tasks = $state<RelayTask[]>([]);
  let streams = $state<StreamInfo[]>([]);
  let refreshTimer: number | undefined;

  // Create task form
  let showCreateDialog = $state(false);
  let newTaskStreamID = $state('');
  let newTaskTargetURL = $state('');
  let creating = $state(false);

  // Task stats
  let taskStats = $state<Map<string, RelayTaskStats>>(new Map());
  let statsLoading = $state<Map<string, boolean>>(new Map());
  
  // Stats history and charts
  let taskStatsHistory = $state<Map<string, StatsHistory>>(new Map());
  let statsHistoryLoading = $state<Map<string, boolean>>(new Map());
  let chartInstances = $state<Map<string, Chart>>(new Map());
  let selectedDuration = $state<Map<string, number>>(new Map());

  async function loadTasks() {
    loading = tasks.length === 0;
    error = '';
    try {
      tasks = await listRelayTasks();
    } catch (e) {
      error = e instanceof Error ? e.message : t('relay.loadFailed');
    } finally {
      loading = false;
    }
  }

  async function loadStreams() {
    try {
      const result = await listStreams({ managed: true, limit: 100 });
      streams = result.streams;
    } catch (e) {
      console.error('Failed to load streams:', e);
    }
  }

  async function loadTaskStats(taskId: string) {
    statsLoading.set(taskId, true);
    statsLoading = new Map(statsLoading);
    try {
      const stats = await getRelayTaskStats(taskId);
      taskStats.set(taskId, stats);
      taskStats = new Map(taskStats);
    } catch (e) {
      console.error('Failed to load task stats:', e);
    } finally {
      statsLoading.set(taskId, false);
      statsLoading = new Map(statsLoading);
    }
  }

  async function loadTaskStatsHistory(taskId: string, durationMinutes: number = 60) {
    statsHistoryLoading.set(taskId, true);
    statsHistoryLoading = new Map(statsHistoryLoading);
    selectedDuration.set(taskId, durationMinutes);
    selectedDuration = new Map(selectedDuration);
    
    try {
      const history = await getRelayTaskStatsHistory(taskId, durationMinutes);
      taskStatsHistory.set(taskId, history);
      taskStatsHistory = new Map(taskStatsHistory);
      
      // Create or update chart
      setTimeout(() => createStatsChart(taskId, history), 100);
    } catch (e) {
      console.error('Failed to load task stats history:', e);
    } finally {
      statsHistoryLoading.set(taskId, false);
      statsHistoryLoading = new Map(statsHistoryLoading);
    }
  }

  function createStatsChart(taskId: string, history: StatsHistory) {
    const canvas = document.getElementById(`chart-${taskId}`) as HTMLCanvasElement;
    if (!canvas) return;

    // Destroy existing chart
    const existingChart = chartInstances.get(taskId);
    if (existingChart) {
      existingChart.destroy();
    }

    const labels = history.samples.map(s => {
      const date = new Date(s.timestamp);
      return date.toLocaleTimeString();
    });

    const chart = new Chart(canvas, {
      type: 'line',
      data: {
        labels,
        datasets: [
          {
            label: 'Video FPS',
            data: history.samples.map(s => s.video_fps),
            borderColor: 'rgb(59, 130, 246)',
            backgroundColor: 'rgba(59, 130, 246, 0.1)',
            yAxisID: 'y',
            tension: 0.3,
          },
          {
            label: 'Video Bitrate (Kbps)',
            data: history.samples.map(s => s.video_bitrate / 1000),
            borderColor: 'rgb(16, 185, 129)',
            backgroundColor: 'rgba(16, 185, 129, 0.1)',
            yAxisID: 'y1',
            tension: 0.3,
          },
        ],
      },
      options: {
        responsive: true,
        interaction: {
          mode: 'index',
          intersect: false,
        },
        scales: {
          y: {
            type: 'linear',
            display: true,
            position: 'left',
            title: {
              display: true,
              text: 'FPS',
            },
          },
          y1: {
            type: 'linear',
            display: true,
            position: 'right',
            title: {
              display: true,
              text: 'Kbps',
            },
            grid: {
              drawOnChartArea: false,
            },
          },
        },
        plugins: {
          legend: {
            position: 'top',
          },
          title: {
            display: true,
            text: t('relay.chartTitle'),
          },
        },
      },
    });

    chartInstances.set(taskId, chart);
    chartInstances = new Map(chartInstances);
  }

  async function handleCreateTask() {
    if (!newTaskStreamID || !newTaskTargetURL) return;
    
    creating = true;
    try {
      await createRelayTask({
        stream_id: newTaskStreamID,
        target_url: newTaskTargetURL,
      });
      showCreateDialog = false;
      newTaskStreamID = '';
      newTaskTargetURL = '';
      await loadTasks();
    } catch (e) {
      error = e instanceof Error ? e.message : t('relay.createFailed');
    } finally {
      creating = false;
    }
  }

  async function handleDeleteTask(taskId: string) {
    if (!confirm(t('relay.deleteConfirm'))) return;
    
    try {
      await deleteRelayTask(taskId);
      await loadTasks();
    } catch (e) {
      error = e instanceof Error ? e.message : t('relay.deleteFailed');
    }
  }

  async function handleStartTask(taskId: string) {
    try {
      await startRelayTask(taskId);
      await loadTasks();
      await loadTaskStats(taskId);
    } catch (e) {
      error = e instanceof Error ? e.message : t('relay.startFailed');
    }
  }

  async function handleStopTask(taskId: string) {
    try {
      await stopRelayTask(taskId);
      await loadTasks();
      await loadTaskStats(taskId);
    } catch (e) {
      error = e instanceof Error ? e.message : t('relay.stopFailed');
    }
  }

  function formatDuration(seconds: number): string {
    if (seconds < 60) return `${Math.round(seconds)}s`;
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.round(seconds % 60)}s`;
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return `${hours}h ${minutes}m`;
  }

  function formatBitrate(bits: number): string {
    if (bits < 1000) return `${bits} bps`;
    if (bits < 1000000) return `${(bits / 1000).toFixed(1)} Kbps`;
    return `${(bits / 1000000).toFixed(2)} Mbps`;
  }

  function formatDate(dateStr: string | undefined): string {
    if (!dateStr) return '-';
    return new Date(dateStr).toLocaleString();
  }

  onMount(() => {
    loadTasks();
    loadStreams();
    refreshTimer = window.setInterval(() => {
      loadTasks();
      // Refresh stats for running tasks
      tasks.forEach(task => {
        if (task.status === 'running') {
          loadTaskStats(task.id);
        }
      });
    }, 5000);
  });

  onDestroy(() => {
    if (refreshTimer) {
      clearInterval(refreshTimer);
    }
    // Destroy all chart instances
    chartInstances.forEach(chart => chart.destroy());
  });
</script>

<div class="container mx-auto px-4 py-6">
  <div class="flex justify-between items-center mb-6">
    <h1 class="text-2xl font-bold">{t('relay.title')}</h1>
    <button 
      class="btn btn-primary"
      onclick={() => showCreateDialog = true}
    >
      {t('relay.create')}
    </button>
  </div>

  {#if error}
    <div class="alert alert-error mb-4">
      <span>{error}</span>
    </div>
  {/if}

  {#if loading}
    <div class="flex justify-center items-center h-64">
      <span class="loading loading-spinner loading-lg"></span>
    </div>
  {:else if tasks.length === 0}
    <div class="text-center py-12">
      <p class="text-gray-500 text-lg">{t('relay.noTasks')}</p>
      <p class="text-gray-400 mt-2">{t('relay.noTasksHint')}</p>
    </div>
  {:else}
    <div class="grid gap-4">
      {#each tasks as task (task.id)}
        <div class="card bg-base-100 shadow-xl">
          <div class="card-body">
            <div class="flex justify-between items-start">
              <div>
                <h2 class="card-title">
                  <span class="badge {task.status === 'running' ? 'badge-success' : task.status === 'error' ? 'badge-error' : 'badge-ghost'}">
                    {task.status}
                  </span>
                  {task.stream_id}
                </h2>
                <p class="text-sm text-gray-500 mt-1">{t('relay.target')}{task.target_url}</p>
                {#if task.error_msg}
                  <p class="text-sm text-error mt-1">{t('relay.errorLabel')}{task.error_msg}</p>
                {/if}
              </div>
              <div class="flex gap-2">
                {#if task.status === 'stopped' || task.status === 'error'}
                  <button 
                    class="btn btn-sm btn-success"
                    onclick={() => handleStartTask(task.id)}
                  >
                    {t('relay.start')}
                  </button>
                {:else if task.status === 'running'}
                  <button 
                    class="btn btn-sm btn-warning"
                    onclick={() => handleStopTask(task.id)}
                  >
                    {t('relay.stop')}
                  </button>
                {/if}
                <button 
                  class="btn btn-sm btn-info"
                  onclick={() => loadTaskStats(task.id)}
                  disabled={statsLoading.get(task.id)}
                >
                  {statsLoading.get(task.id) ? t('common.loading') : t('relay.stats')}
                </button>
                <button 
                  class="btn btn-sm btn-error"
                  onclick={() => handleDeleteTask(task.id)}
                >
                  {t('relay.delete')}
                </button>
              </div>
            </div>

            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mt-4 text-sm">
              <div>
                <span class="text-gray-500">{t('relay.created')}</span>
                <p>{formatDate(task.created_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">{t('relay.started')}</span>
                <p>{formatDate(task.started_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">{t('relay.stopped')}</span>
                <p>{formatDate(task.stopped_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">{t('relay.duration')}</span>
                <p>{task.started_at ? formatDuration((new Date(task.stopped_at || Date.now()).getTime() - new Date(task.started_at).getTime()) / 1000) : '-'}</p>
              </div>
            </div>

            {#if taskStats.has(task.id)}
              {@const stats = taskStats.get(task.id)}
              <div class="divider">{t('relay.realtimeStats')}</div>
              <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
                <div>
                  <span class="text-gray-500">{t('relay.videoFps')}</span>
                  <p>{stats?.video_fps?.toFixed(1) || '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">{t('relay.videoBitrate')}</span>
                  <p>{stats?.video_bitrate ? formatBitrate(stats.video_bitrate) : '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">{t('relay.audioFps')}</span>
                  <p>{stats?.audio_fps?.toFixed(1) || '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">{t('relay.audioBitrate')}</span>
                  <p>{stats?.audio_bitrate ? formatBitrate(stats.audio_bitrate) : '-'}</p>
                </div>
              </div>
            {/if}

            <div class="divider">{t('relay.historyStats')}</div>
            <div class="flex gap-2 mb-4">
              <button 
                class="btn btn-sm {selectedDuration.get(task.id) === 15 ? 'btn-primary' : 'btn-outline'}"
                onclick={() => loadTaskStatsHistory(task.id, 15)}
                disabled={statsHistoryLoading.get(task.id)}
              >
                15 min
              </button>
              <button 
                class="btn btn-sm {selectedDuration.get(task.id) === 30 ? 'btn-primary' : 'btn-outline'}"
                onclick={() => loadTaskStatsHistory(task.id, 30)}
                disabled={statsHistoryLoading.get(task.id)}
              >
                30 min
              </button>
              <button 
                class="btn btn-sm {selectedDuration.get(task.id) === 60 || !selectedDuration.has(task.id) ? 'btn-primary' : 'btn-outline'}"
                onclick={() => loadTaskStatsHistory(task.id, 60)}
                disabled={statsHistoryLoading.get(task.id)}
              >
                1 hour
              </button>
              <button 
                class="btn btn-sm {selectedDuration.get(task.id) === 180 ? 'btn-primary' : 'btn-outline'}"
                onclick={() => loadTaskStatsHistory(task.id, 180)}
                disabled={statsHistoryLoading.get(task.id)}
              >
                3 hours
              </button>
              <button 
                class="btn btn-sm {selectedDuration.get(task.id) === 1440 ? 'btn-primary' : 'btn-outline'}"
                onclick={() => loadTaskStatsHistory(task.id, 1440)}
                disabled={statsHistoryLoading.get(task.id)}
              >
                24 hours
              </button>
            </div>
            
            {#if statsHistoryLoading.get(task.id)}
              <div class="flex justify-center items-center h-64">
                <span class="loading loading-spinner loading-lg"></span>
              </div>
            {:else if taskStatsHistory.has(task.id) && taskStatsHistory.get(task.id)?.samples?.length > 0}
              <div class="h-80">
                <canvas id="chart-{task.id}"></canvas>
              </div>
            {:else}
              <div class="text-center py-8 text-gray-500">
                {t('relay.historyEmpty')}
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

{#if showCreateDialog}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50" role="dialog">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('relay.createTitle')}</h3>
      
      <div class="mb-4">
        <label for="relay-stream" class="input-label">{t('relay.sourceStream')}</label>
        <select 
          id="relay-stream"
          class="input mt-1"
          bind:value={newTaskStreamID}
        >
          <option value="">{t('relay.selectStream')}</option>
          {#each streams as stream}
            <option value={stream.stream_id}>{stream.stream_id}</option>
          {/each}
        </select>
      </div>

      <div class="mb-6">
        <label for="relay-target" class="input-label">{t('relay.targetUrl')}</label>
        <input 
          id="relay-target"
          type="text" 
          placeholder={t('relay.targetPlaceholder')} 
          class="input mt-1"
          bind:value={newTaskTargetURL}
        />
      </div>

      <div class="flex gap-3 justify-end">
        <button 
          class="btn btn-secondary"
          onclick={() => showCreateDialog = false}
        >
          {t('relay.cancel')}
        </button>
        <button 
          class="btn btn-primary"
          onclick={handleCreateTask}
          disabled={creating || !newTaskStreamID || !newTaskTargetURL}
        >
          {creating ? t('relay.creating') : t('common.confirm')}
        </button>
      </div>
    </div>
  </div>
{/if}