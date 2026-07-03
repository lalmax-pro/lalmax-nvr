<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { 
    listRelayTasks, 
    createRelayTask, 
    deleteRelayTask, 
    startRelayTask, 
    stopRelayTask,
    getRelayTaskStats,
    listStreams
  } from '$lib/api';
  import type { RelayTask, RelayTaskStats, StreamInfo } from '$lib/api';
  import { t } from '$lib/i18n';

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

  async function loadTasks() {
    loading = tasks.length === 0;
    error = '';
    try {
      tasks = await listRelayTasks();
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to load relay tasks';
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
      error = e instanceof Error ? e.message : 'Failed to create task';
    } finally {
      creating = false;
    }
  }

  async function handleDeleteTask(taskId: string) {
    if (!confirm('Are you sure you want to delete this relay task?')) return;
    
    try {
      await deleteRelayTask(taskId);
      await loadTasks();
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to delete task';
    }
  }

  async function handleStartTask(taskId: string) {
    try {
      await startRelayTask(taskId);
      await loadTasks();
      await loadTaskStats(taskId);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to start task';
    }
  }

  async function handleStopTask(taskId: string) {
    try {
      await stopRelayTask(taskId);
      await loadTasks();
      await loadTaskStats(taskId);
    } catch (e) {
      error = e instanceof Error ? e.message : 'Failed to stop task';
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
  });
</script>

<div class="container mx-auto px-4 py-6">
  <div class="flex justify-between items-center mb-6">
    <h1 class="text-2xl font-bold">Relay Tasks</h1>
    <button 
      class="btn btn-primary"
      onclick={() => showCreateDialog = true}
    >
      Create Task
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
      <p class="text-gray-500 text-lg">No relay tasks found</p>
      <p class="text-gray-400 mt-2">Create a task to start relaying streams to other platforms</p>
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
                <p class="text-sm text-gray-500 mt-1">Target: {task.target_url}</p>
                {#if task.error_msg}
                  <p class="text-sm text-error mt-1">Error: {task.error_msg}</p>
                {/if}
              </div>
              <div class="flex gap-2">
                {#if task.status === 'stopped' || task.status === 'error'}
                  <button 
                    class="btn btn-sm btn-success"
                    onclick={() => handleStartTask(task.id)}
                  >
                    Start
                  </button>
                {:else if task.status === 'running'}
                  <button 
                    class="btn btn-sm btn-warning"
                    onclick={() => handleStopTask(task.id)}
                  >
                    Stop
                  </button>
                {/if}
                <button 
                  class="btn btn-sm btn-info"
                  onclick={() => loadTaskStats(task.id)}
                  disabled={statsLoading.get(task.id)}
                >
                  {statsLoading.get(task.id) ? 'Loading...' : 'Stats'}
                </button>
                <button 
                  class="btn btn-sm btn-error"
                  onclick={() => handleDeleteTask(task.id)}
                >
                  Delete
                </button>
              </div>
            </div>

            <div class="grid grid-cols-2 md:grid-cols-4 gap-4 mt-4 text-sm">
              <div>
                <span class="text-gray-500">Created</span>
                <p>{formatDate(task.created_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">Started</span>
                <p>{formatDate(task.started_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">Stopped</span>
                <p>{formatDate(task.stopped_at)}</p>
              </div>
              <div>
                <span class="text-gray-500">Duration</span>
                <p>{task.started_at ? formatDuration((new Date(task.stopped_at || Date.now()).getTime() - new Date(task.started_at).getTime()) / 1000) : '-'}</p>
              </div>
            </div>

            {#if taskStats.has(task.id)}
              {@const stats = taskStats.get(task.id)}
              <div class="divider">Statistics</div>
              <div class="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
                <div>
                  <span class="text-gray-500">Video FPS</span>
                  <p>{stats?.video_fps?.toFixed(1) || '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">Video Bitrate</span>
                  <p>{stats?.video_bitrate ? formatBitrate(stats.video_bitrate) : '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">Audio FPS</span>
                  <p>{stats?.audio_fps?.toFixed(1) || '-'}</p>
                </div>
                <div>
                  <span class="text-gray-500">Audio Bitrate</span>
                  <p>{stats?.audio_bitrate ? formatBitrate(stats.audio_bitrate) : '-'}</p>
                </div>
              </div>
            {/if}
          </div>
        </div>
      {/each}
    </div>
  {/if}
</div>

{#if showCreateDialog}
  <div class="modal modal-open">
    <div class="modal-box">
      <h3 class="font-bold text-lg">Create Relay Task</h3>
      
      <div class="form-control w-full mt-4">
        <label class="label">
          <span class="label-text">Source Stream</span>
        </label>
        <select 
          class="select select-bordered w-full"
          bind:value={newTaskStreamID}
        >
          <option value="">Select a stream</option>
          {#each streams as stream}
            <option value={stream.stream_id}>{stream.stream_id}</option>
          {/each}
        </select>
      </div>

      <div class="form-control w-full mt-4">
        <label class="label">
          <span class="label-text">Target RTMP URL</span>
        </label>
        <input 
          type="text" 
          placeholder="rtmp://live.example.com/stream/key" 
          class="input input-bordered w-full"
          bind:value={newTaskTargetURL}
        />
      </div>

      <div class="modal-action">
        <button 
          class="btn"
          onclick={() => showCreateDialog = false}
        >
          Cancel
        </button>
        <button 
          class="btn btn-primary"
          onclick={handleCreateTask}
          disabled={creating || !newTaskStreamID || !newTaskTargetURL}
        >
          {creating ? 'Creating...' : 'Create'}
        </button>
      </div>
    </div>
  </div>
{/if}