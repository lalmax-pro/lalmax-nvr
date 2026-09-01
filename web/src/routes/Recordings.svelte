<script lang="ts">
  import { onMount, onDestroy, untrack } from 'svelte';
  import { listRecordings, listCameras, deleteRecording, downloadRecording } from '$lib/api';
  import type { Recording, Camera } from '$lib/api';
  import { formatDate, formatDuration, formatFileSize } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { t } from '$lib/i18n';
  import { Trash2, Download, ChevronLeft, ChevronRight, Calendar, AlertCircle, Play } from 'lucide-svelte';
  import Timeline from '$lib/components/Timeline.svelte';
  import InlinePlayer from '$lib/components/InlinePlayer.svelte';
  import RecordingCalendar from '$lib/components/RecordingCalendar.svelte';
  import VodPlayer from '$lib/components/VodPlayer.svelte';

  let { initialCameraId = '' }: { initialCameraId?: string } = $props();

  // State
  let cameras = $state<Camera[]>([]);
  let selectedCameraId = $state(untrack(() => initialCameraId));
  let selectedDate = $state(formatDateForInput(new Date()));
  let selectedHour = $state<number>(-1); // -1 = all hours
  let recordings = $state<Recording[]>([]);
  let loading = $state(false);
  let error = $state('');
  let selectedRecording = $state<Recording | null>(null);
  let deleteConfirm = $state<Recording | null>(null);
  let continuousPlay = $state(false);
  let vodSeekMs = $state(0);

  let selectedCamera = $derived(cameras.find(c => c.id === selectedCameraId));
  let selectedCameraArchived = $derived(selectedCamera?.archived === true);
  let dayRange = $derived.by(() => {
    const start = new Date(selectedDate);
    start.setHours(0, 0, 0, 0);
    const end = new Date(selectedDate);
    end.setHours(23, 59, 59, 999);
    return { start: start.toISOString(), end: end.toISOString() };
  });

  // Filters for recording list
  let formatFilter = $state('');
  let mergedFilter = $state('');

  // Calendar (shows which days have recordings)
  let showCalendar = $state(false);

  // Helpers
  function formatDateForInput(d: Date): string {
    const year = d.getFullYear();
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  }

  function getCameraName(cameraId: string): string {
    return cameras.find(c => c.id === cameraId)?.name || cameraId;
  }

  function formatTime(dateStr: string): string {
    return new Date(dateStr).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }

  // Navigation
  function prevDay() {
    const d = new Date(selectedDate);
    d.setDate(d.getDate() - 1);
    selectedDate = formatDateForInput(d);
  }

  function nextDay() {
    const d = new Date(selectedDate);
    d.setDate(d.getDate() + 1);
    selectedDate = formatDateForInput(d);
  }

  function goToToday() {
    selectedDate = formatDateForInput(new Date());
  }

  // Data loading
  async function loadCameras() {
    try {
      // Archived devices still own recordings and must remain selectable here.
      cameras = await listCameras(undefined, true);
      if (cameras.length > 0 && !selectedCameraId) {
        selectedCameraId = cameras[0].id;
      }
    } catch (e) {
      console.error('Failed to load cameras:', e);
    }
  }

  async function loadRecordings() {
    if (!selectedCameraId) {
      recordings = [];
      return;
    }

    loading = true;
    error = '';

    try {
      const dayStart = new Date(selectedDate);
      dayStart.setHours(0, 0, 0, 0);
      const dayEnd = new Date(selectedDate);
      dayEnd.setHours(23, 59, 59, 999);

      const response = await listRecordings({
        camera_id: selectedCameraId,
        archived: selectedCameraArchived ? true : undefined,
        start: dayStart.toISOString(),
        end: dayEnd.toISOString(),
        limit: 1000,
        sort_by: 'started_at',
        order: 'asc',
      });

      recordings = response.recordings || [];
    } catch (e) {
      error = e instanceof Error ? e.message : t('recordings.page.loadFailed');
    } finally {
      loading = false;
    }
  }

  // Filtered recordings for list
  let filteredRecordings = $derived(
    recordings.filter(r => {
      if (formatFilter && r.format !== formatFilter) return false;
      if (mergedFilter === 'true' && !r.merged) return false;
      if (mergedFilter === 'false' && r.merged) return false;
      if (selectedHour >= 0) {
        const hour = new Date(r.started_at).getHours();
        if (hour !== selectedHour) return false;
      }
      return true;
    })
  );

  // Actions
  function handleTimelineSelect(recording: Recording) {
    selectedRecording = recording;
  }

  function mediaOffsetMs(recording: Recording, offsetSec: number): number {
    let acc = 0;
    const vodRecs = recordings.filter(r => r.format === 'h264' || r.format === 'h265' || r.format === 'timelapse');
    for (const r of vodRecs) {
      if (r.id === recording.id) {
        return acc + offsetSec * 1000;
      }
      acc += (r.duration || 0) * 1000;
    }
    return acc;
  }

  function handleTimelineSeek(recording: Recording, offsetSec: number) {
    selectedRecording = recording;
    if (continuousPlay) {
      vodSeekMs = mediaOffsetMs(recording, offsetSec);
    }
  }

  function handleClosePlayer() {
    selectedRecording = null;
  }

  function handleNavigate(recording: Recording) {
    selectedRecording = recording;
  }

  function handleHourSelect(hour: number) {
    selectedHour = hour;
  }

  async function handleDelete(recording: Recording) {
    try {
      await deleteRecording(recording.id);
      recordings = recordings.filter(r => r.id !== recording.id);
      if (selectedRecording?.id === recording.id) {
        selectedRecording = null;
      }
      showToast(t('recordings.page.deleted'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.page.deleteFailed'), 'error');
    }
    deleteConfirm = null;
  }

  async function handleDownload(recording: Recording) {
    try {
      await downloadRecording(recording.id);
    } catch (e) {
      showToast(t('recordings.page.downloadFailed'), 'error');
    }
  }

  // Load data when camera or date changes
  $effect(() => {
    const _ = [selectedCameraId, selectedDate, selectedCameraArchived];
    if (selectedCameraId) {
      loadRecordings();
    }
  });

  onMount(() => {
    loadCameras();
  });
</script>

<div class="min-h-screen th-bg-primary ">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <!-- Header -->
    <h2 class="text-2xl font-bold th-text-primary mb-6">{t('recordings.page.title')}</h2>

    <!-- Camera + Date Selector -->
    <div class="card p-4 mb-6 border th-border">
      <div class="flex flex-wrap gap-4 items-end">
        <!-- Camera dropdown -->
        <div class="flex-1 min-w-[200px]">
          <label for="camera-select" class="input-label">{t('recordings.page.camera')}</label>
          <select id="camera-select" class="input" bind:value={selectedCameraId}>
            {#if cameras.length === 0}
              <option value="">{t('recordings.page.noCameras')}</option>
            {/if}
            {#each cameras as camera}
              <option value={camera.id}>
                {camera.name}{camera.archived ? ` (${t('recordings.page.archived')})` : ''}
              </option>
            {/each}
          </select>
        </div>

        <!-- Date picker -->
        <div class="flex items-end gap-2">
          <div>
            <label for="date-select" class="input-label">{t('recordings.page.date')}</label>
            <input
              id="date-select"
              type="date"
              class="input"
              bind:value={selectedDate}
              max={formatDateForInput(new Date())}
            />
          </div>
          <button onclick={prevDay} class="btn btn-ghost btn-sm" title={t('recordings.page.previousDay')}>
            <ChevronLeft size={18} />
          </button>
          <button onclick={nextDay} class="btn btn-ghost btn-sm" title={t('recordings.page.nextDay')}>
            <ChevronRight size={18} />
          </button>
          <button onclick={goToToday} class="btn btn-secondary btn-sm">
            {t('recordings.page.today')}
          </button>
          <button
            onclick={() => showCalendar = !showCalendar}
            class="btn btn-sm {showCalendar ? 'btn-primary' : 'btn-ghost'}"
            title={t('recordings.calendar.toggle')}
            aria-pressed={showCalendar}
          >
            <Calendar size={18} />
          </button>
          <button
            onclick={() => { continuousPlay = !continuousPlay; }}
            class="btn btn-sm {continuousPlay ? 'btn-primary' : 'btn-ghost'}"
            title={t('recordings.continuousHint')}
            aria-pressed={continuousPlay}
          >
            {t('recordings.continuousPlay')}
          </button>
        </div>

        <!-- Hour selector -->
        <div class="flex-1 min-w-[120px]">
          <label for="hour-select" class="input-label">{t('recordings.page.hour')}</label>
          <select id="hour-select" class="input" bind:value={selectedHour}>
            <option value={-1}>{t('recordings.page.allHours')}</option>
            {#each Array(24) as _, h}
              <option value={h}>{String(h).padStart(2, '0')}:00</option>
            {/each}
          </select>
        </div>
      </div>

      {#if showCalendar && selectedCameraId}
        <div class="mt-4 pt-4 border-t th-border max-w-xs">
          <RecordingCalendar
            cameraId={selectedCameraId}
            {selectedDate}
            onselect={(d) => { selectedDate = d; }}
          />
        </div>
      {/if}
    </div>

    <!-- Timeline -->
    {#if selectedCameraId}
      <div class="card p-4 mb-6 border th-border">
        <Timeline
          {recordings}
          selectedRecording={selectedRecording ?? undefined}
          {selectedHour}
          onSelect={handleTimelineSelect}
          onHourSelect={handleHourSelect}
          onSeek={handleTimelineSeek}
        />
      </div>
    {/if}

    <!-- Inline / continuous Player -->
    {#if continuousPlay && selectedCameraId}
      <div class="mb-6">
        <VodPlayer
          cameraId={selectedCameraId}
          start={dayRange.start}
          end={dayRange.end}
          seekToMs={vodSeekMs}
        />
      </div>
    {:else if selectedRecording}
      <div class="mb-6">
        <InlinePlayer
          recording={selectedRecording}
          allRecordings={recordings}
          onClose={handleClosePlayer}
          onNavigate={handleNavigate}
        />
      </div>
    {/if}

    <!-- Error -->
    {#if error}
      <div class="card border th-border-danger p-6 mb-6 text-center">
        <div class="flex justify-center mb-3 th-color-danger">
          <AlertCircle size={32} />
        </div>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadRecordings} class="btn btn-primary btn-sm">Retry</button>
      </div>
    {/if}

    <!-- Recording List -->
    {#if selectedCameraId}
      <div class="card border th-border">
        <!-- List header -->
        <div class="flex items-center justify-between px-4 py-3 border-b th-border">
          <h3 class="text-lg font-semibold th-text-primary">
            {t('recordings.page.recordingsCount', { count: filteredRecordings.length })}
          </h3>
          <div class="flex gap-2">
            <select class="input input-sm" bind:value={formatFilter}>
              <option value="">{t('recordings.page.allFormats')}</option>
              <option value="h264">H.264</option>
              <option value="h265">H.265</option>
              <option value="mjpeg">MJPEG</option>
              <option value="timelapse">Timelapse</option>
            </select>
            <select class="input input-sm" bind:value={mergedFilter}>
              <option value="">{t('recordings.page.all')}</option>
              <option value="true">{t('recordings.page.merged')}</option>
              <option value="false">{t('recordings.page.original')}</option>
            </select>
          </div>
        </div>

        <!-- List content -->
        {#if loading}
          <div class="p-8 text-center">
            <div class="spinner spinner-lg"></div>
          </div>
        {:else if filteredRecordings.length === 0}
          <div class="p-8 text-center">
            <Calendar size={48} class="mx-auto mb-4 th-text-muted" />
            <p class="th-text-secondary">{t('recordings.page.noRecordings')}</p>
          </div>
        {:else}
          <div class="overflow-x-auto">
            <table class="table">
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Duration</th>
                  <th>Size</th>
                  <th>Format</th>
                  <th>Status</th>
                  <th class="text-right">Actions</th>
                </tr>
              </thead>
              <tbody>
                {#each filteredRecordings as recording (recording.id)}
                  <tr
                    class="cursor-pointer hover:th-bg-hover transition-colors"
                    class:th-bg-active={selectedRecording?.id === recording.id}
                    onclick={() => handleTimelineSelect(recording)}
                  >
                    <td class="font-mono text-sm">{formatTime(recording.started_at)}</td>
                    <td>{formatDuration(recording.duration)}</td>
                    <td>{formatFileSize(recording.file_size)}</td>
                    <td>
                      <span class="badge badge-neutral text-xs">
                        {recording.format.toUpperCase()}
                      </span>
                    </td>
                    <td>
                      {#if recording.archived}
                        <span class="badge badge-warning text-xs">{t('recordings.page.archived')}</span>
                      {:else if recording.merge_status === 'merged'}
                        <span class="badge badge-success text-xs">{t('recordings.page.merged')}</span>
                      {:else if recording.merge_status === 'pending'}
                        <span class="badge badge-neutral text-xs">{t('recordings.page.pending')}</span>
                      {:else if recording.merge_status === 'failed'}
                        <span class="badge badge-danger text-xs">{t('recordings.page.failed')}</span>
                      {:else}
                        <span class="badge badge-neutral text-xs">{t('recordings.page.original')}</span>
                      {/if}
                    </td>
                    <td class="text-right">
                      <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
                      <div class="flex justify-end gap-1" role="presentation" onclick={(e) => e.stopPropagation()}>
                        <button
                          onclick={() => handleTimelineSelect(recording)}
                          class="btn btn-ghost btn-sm"
                          title={t('recordings.page.play')}
                        >
                          <Play size={14} />
                        </button>
                        <button
                          onclick={() => handleDownload(recording)}
                          class="btn btn-ghost btn-sm"
                          title={t('recordings.page.download')}
                        >
                          <Download size={14} />
                        </button>
                        <button
                          onclick={() => deleteConfirm = recording}
                          class="btn btn-ghost btn-sm th-color-danger"
                          title={t('recordings.page.delete')}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </div>
    {:else}
      <!-- No camera selected -->
      <div class="card p-12 text-center border th-border">
        <Calendar size={48} class="mx-auto mb-4 th-text-muted" />
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('recordings.page.selectCamera')}</h3>
        <p class="text-sm th-text-muted">{t('recordings.page.selectCameraHint')}</p>
      </div>
    {/if}
  </main>
</div>

<!-- Delete confirmation modal -->
{#if deleteConfirm}
  <div class="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
    <div class="card max-w-md w-full p-6">
      <h3 class="text-lg font-semibold th-text-primary mb-4">{t('recordings.page.deleteTitle')}</h3>
      <p class="th-text-secondary mb-6">
        {t('recordings.page.deleteMessage', { time: formatTime(deleteConfirm.started_at) })}
      </p>
      <div class="flex gap-3 justify-end">
        <button onclick={() => deleteConfirm = null} class="btn btn-secondary">
          {t('recordings.page.cancel')}
        </button>
        <button onclick={() => handleDelete(deleteConfirm)} class="btn btn-danger">
          {t('recordings.page.delete')}
        </button>
      </div>
    </div>
  </div>
{/if}
