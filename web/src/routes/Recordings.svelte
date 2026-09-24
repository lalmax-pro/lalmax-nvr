<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { listRecordings, listCameras, listRecordingSources, deleteRecording, downloadRecording, getRecordingsTimeline, listEvents, batchDeleteRecordings, setRecordingLocked, getDeviceGroupTree, listGroupChannels, getVodExportUrl, startDevicePlayback, getONVIFReplayURI } from '$lib/api';
  import type { Recording, RecordingSource, Camera, TimelineEntry, NvrEvent, DeviceGroupTreeNode, UnifiedSource, UnifiedTimelineClip } from '$lib/api';
  import { loadUnifiedTimeline } from '$lib/unified-timeline';
  import { formatDuration, formatFileSize } from '$lib/format';
  import { showToast } from '$lib/toast';
  import { t } from '$lib/i18n';
  import { wallMsToHour, wallMsToMediaOffset, vodClips, dayStartFromISO } from '$lib/playback';
  import { Trash2, Download, ChevronLeft, ChevronRight, Calendar, AlertCircle, Play, Search, Video, Lock, Unlock } from 'lucide-svelte';
  import Timeline from '$lib/components/Timeline.svelte';
  import InlinePlayer from '$lib/components/InlinePlayer.svelte';
  import RecordingCalendar from '$lib/components/RecordingCalendar.svelte';
  import VodPlayer from '$lib/components/VodPlayer.svelte';

  let { initialCameraId = '' }: { initialCameraId?: string } = $props();

  let cameras = $state<Camera[]>([]);
  let recordingSources = $state<RecordingSource[]>([]);
  let selectedCameraId = $state(untrack(() => initialCameraId));
  let selectedDate = $state(formatDateForInput(new Date()));
  let selectedHour = $state<number>(-1);
  let recordings = $state<Recording[]>([]);
  let loading = $state(false);
  let error = $state('');
  let selectedRecording = $state<Recording | null>(null);
  let deleteConfirm = $state<Recording | null>(null);
  let continuousPlay = $state(false);
  let vodSeekMs = $state(0);
  let cameraQuery = $state('');
  let formatFilter = $state('');
  let mergedFilter = $state('');
  let showCalendar = $state(false);
  let timelineEntries = $state<TimelineEntry[]>([]);
  let dayEvents = $state<NvrEvent[]>([]);
  let playheadHour = $state(-1);
  let playbackRate = $state(1);
  let selectedIds = $state<Set<string>>(new Set());
  let groupTree = $state<DeviceGroupTreeNode[]>([]);
  let groupCameraIds = $state<Record<number, string[]>>({});
  let expandedGroups = $state<Set<number>>(new Set());
  let inlineSeekSec = $state(0);
  let sourceFilter = $state<UnifiedSource | 'all'>('all');
  let unifiedClips = $state<UnifiedTimelineClip[]>([]);
  let markInHour = $state(-1);
  let markOutHour = $state(-1);
  let remotePlayUrl = $state('');
  let remotePlayLabel = $state('');

  let selectedCamera = $derived(cameras.find(c => c.id === selectedCameraId));
  let selectedCameraArchived = $derived(selectedCamera?.archived === true);
  let dayRange = $derived.by(() => {
    const start = new Date(selectedDate);
    start.setHours(0, 0, 0, 0);
    const end = new Date(selectedDate);
    end.setHours(23, 59, 59, 999);
    return { start: start.toISOString(), end: end.toISOString() };
  });

  let visibleCameras = $derived(
    cameras.filter(c => {
      if (!cameraQuery.trim()) return true;
      const q = cameraQuery.trim().toLowerCase();
      return c.name.toLowerCase().includes(q) || c.id.toLowerCase().includes(q);
    })
  );
  let visibleStreamSources = $derived(
    recordingSources.filter(source => {
      if (source.kind === 'camera') return false;
      if (!cameraQuery.trim()) return true;
      const q = cameraQuery.trim().toLowerCase();
      return source.name.toLowerCase().includes(q) || source.id.toLowerCase().includes(q);
    })
  );
  let visibleSourceCount = $derived(visibleCameras.length + visibleStreamSources.length);

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

  function formatDateForInput(d: Date): string {
    const year = d.getFullYear();
    const month = String(d.getMonth() + 1).padStart(2, '0');
    const day = String(d.getDate()).padStart(2, '0');
    return `${year}-${month}-${day}`;
  }

  function formatTime(dateStr: string): string {
    return new Date(dateStr).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
  }

  function cameraStatusDot(camera: Camera): string {
    if (camera.archived) return 'dot-muted';
    if (!camera.enabled) return 'dot-muted';
    if (camera.status === 'recording') return 'dot-live';
    if (camera.status === 'error' || camera.status === 'offline') return 'dot-danger';
    if (camera.status === 'reconnecting' || camera.status === 'paused') return 'dot-warn';
    return 'dot-idle';
  }

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

  async function loadRecordingSources() {
    const [cameraResult, sourceResult] = await Promise.allSettled([
      listCameras(undefined, true),
      listRecordingSources(true),
    ]);

    if (cameraResult.status === 'fulfilled') cameras = cameraResult.value;
    else console.error('Failed to load cameras:', cameraResult.reason);

    if (sourceResult.status === 'fulfilled') recordingSources = sourceResult.value;
    else console.error('Failed to load recording sources:', sourceResult.reason);

    if (!selectedCameraId) {
      selectedCameraId = cameras[0]?.id ?? recordingSources[0]?.id ?? '';
    }
  }

  async function loadRecordings() {
    if (!selectedCameraId) {
      recordings = [];
      timelineEntries = [];
      unifiedClips = [];
      return;
    }

    loading = true;
    error = '';
    unifiedClips = [];

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
      try {
        timelineEntries = await getRecordingsTimeline(selectedCameraId, dayStart.toISOString(), dayEnd.toISOString());
        if (selectedCamera) {
          unifiedClips = await loadUnifiedTimeline(selectedCamera, dayStart.toISOString(), dayEnd.toISOString());
        } else {
          unifiedClips = [];
        }
      } catch {
        timelineEntries = recordings.map(r => ({
          id: r.id,
          camera_id: r.camera_id,
          started_at: r.started_at,
          ended_at: r.ended_at,
          duration: r.duration,
          format: r.format,
          merged: r.merged,
          gap_reason: r.gap_reason,
          locked: r.locked,
        }));
      }
      try {
        const ev = await listEvents({
          camera_id: selectedCameraId,
          since: dayStart.toISOString(),
          until: dayEnd.toISOString(),
          limit: 200,
        });
        dayEvents = ev.events || [];
      } catch {
        dayEvents = [];
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('recordings.page.loadFailed');
    } finally {
      loading = false;
    }
  }

  function selectCamera(id: string) {
    if (selectedCameraId === id) return;
    selectedCameraId = id;
    selectedRecording = null;
    continuousPlay = false;
    sourceFilter = 'all';
  }

  function handleTimelineSelect(recording: Recording | TimelineEntry | UnifiedTimelineClip) {
    const clip = recording as UnifiedTimelineClip;
    if (clip.source === 'gb' || clip.source === 'onvif') {
      void playRemoteClip(clip);
      return;
    }
    remotePlayUrl = '';
    selectedRecording = recordings.find(r => r.id === recording.id) || (recording as Recording);
  }

  async function playRemoteClip(clip: UnifiedTimelineClip) {
    if (!selectedCamera) return;
    try {
      if (clip.source === 'gb') {
        const res = await startDevicePlayback({
          device_id: selectedCamera.serial_number || selectedCamera.id,
          channel_id: selectedCamera.id,
          start_time: clip.started_at,
          end_time: clip.ended_at,
        });
        const url = res.urls?.[0]?.url || res.url || '';
        remotePlayUrl = url;
        remotePlayLabel = `GB28181 ${clip.started_at}`;
      } else if (clip.source === 'onvif' && clip.token) {
        const res = await getONVIFReplayURI(selectedCamera.id, clip.token);
        remotePlayUrl = res.uri;
        remotePlayLabel = `ONVIF ${clip.token}`;
      }
      selectedRecording = null;
      continuousPlay = false;
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.page.remotePlayFailed'), 'error');
    }
  }

  function handleTimelineSeek(recording: Recording | TimelineEntry | UnifiedTimelineClip, offsetSec: number) {
    const clip = recording as UnifiedTimelineClip;
    if (clip.source === 'gb' || clip.source === 'onvif') {
      void playRemoteClip(clip);
      return;
    }
    remotePlayUrl = '';
    const full = recordings.find(r => r.id === recording.id) || (recording as Recording);
    selectedRecording = full;
    const startMs = new Date(recording.started_at).getTime();
    const wallMs = startMs + offsetSec * 1000;
    playheadHour = wallMsToHour(wallMs, dayStartFromISO(selectedDate));
    if (continuousPlay) {
      const mapped = wallMsToMediaOffset(vodClips(recordings), wallMs);
      vodSeekMs = mapped?.mediaMs || 0;
    } else {
      inlineSeekSec = offsetSec;
    }
  }

  function handleVodTime(mediaMs: number) {
    const mapped = wallMsToMediaOffset(vodClips(recordings), mediaOffsetToApproxWall(mediaMs));
    if (mapped) playheadHour = wallMsToHour(new Date(mapped.clip.started_at).getTime() + mapped.offsetSec * 1000, dayStartFromISO(selectedDate));
  }

  function mediaOffsetToApproxWall(mediaMs: number): number {
    let acc = 0;
    for (const clip of vodClips(recordings)) {
      const dur = Math.max(0, (clip.duration || 0) * 1000);
      if (mediaMs <= acc + dur) return new Date(clip.started_at).getTime() + (mediaMs - acc);
      acc += dur;
    }
    return dayStartFromISO(selectedDate).getTime();
  }

  function handleInlineTime(offsetSec: number) {
    if (!selectedRecording) return;
    playheadHour = wallMsToHour(new Date(selectedRecording.started_at).getTime() + offsetSec * 1000, dayStartFromISO(selectedDate));
  }

  function handleEventSelect(event: NvrEvent) {
    if (event.recording_id) {
      const rec = recordings.find(r => r.id === event.recording_id);
      if (rec) selectedRecording = rec;
    }
    playheadHour = wallMsToHour(new Date(event.started_at).getTime(), dayStartFromISO(selectedDate));
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

  async function handleToggleLock(recording: Recording) {
    try {
      await setRecordingLocked(recording.id, !recording.locked);
      recordings = recordings.map(r => r.id === recording.id ? { ...r, locked: !r.locked } : r);
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.page.lockFailed'), 'error');
    }
  }

  function toggleSelected(id: string) {
    const next = new Set(selectedIds);
    if (next.has(id)) next.delete(id); else next.add(id);
    selectedIds = next;
  }

  async function handleBatchDelete() {
    if (selectedIds.size === 0) return;
    try {
      await batchDeleteRecordings([...selectedIds]);
      recordings = recordings.filter(r => !selectedIds.has(r.id) || r.locked);
      selectedIds = new Set();
      showToast(t('recordings.page.batchDeleted'), 'success');
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('recordings.page.deleteFailed'), 'error');
    }
  }

  async function loadGroups() {
    try {
      groupTree = await getDeviceGroupTree();
      const ids: Record<number, string[]> = {};
      for (const node of groupTree) {
        await collectGroupCameras(node, ids);
      }
      groupCameraIds = ids;
    } catch {
      groupTree = [];
    }
  }

  async function collectGroupCameras(node: DeviceGroupTreeNode, ids: Record<number, string[]>) {
    try {
      const channels = await listGroupChannels(node.id);
      ids[node.id] = channels.map(c => c.device_id || c.channel_id).filter(Boolean);
    } catch {
      ids[node.id] = [];
    }
    for (const child of node.children || []) {
      await collectGroupCameras(child, ids);
    }
  }

  function handleMark(hour: number, kind: 'in' | 'out') {
    if (kind === 'in') markInHour = hour;
    else markOutHour = hour;
    if (markInHour >= 0 && markOutHour >= 0 && markOutHour < markInHour) {
      const tmp = markInHour;
      markInHour = markOutHour;
      markOutHour = tmp;
    }
  }

  function exportClip() {
    if (!selectedCameraId || markInHour < 0 || markOutHour <= markInHour) {
      showToast(t('recordings.page.exportHint'), 'error');
      return;
    }
    const start = new Date(selectedDate);
    start.setHours(0, 0, 0, 0);
    const from = new Date(start.getTime() + markInHour * 3600000);
    const to = new Date(start.getTime() + markOutHour * 3600000);
    window.open(getVodExportUrl(selectedCameraId, from.toISOString(), to.toISOString()), '_blank');
  }

  let visibleTimeline = $derived(
    sourceFilter === 'all' || (sourceFilter === 'nvr' && unifiedClips.length === 0)
      ? (unifiedClips.length ? unifiedClips : timelineEntries)
      : unifiedClips.filter(c => c.source === sourceFilter)
  );

  function toggleGroup(id: number) {
    const next = new Set(expandedGroups);
    if (next.has(id)) next.delete(id); else next.add(id);
    expandedGroups = next;
  }

  function mergeLabel(recording: Recording): string {
    if (recording.archived) return t('recordings.page.archived');
    if (recording.merge_status === 'merged') return t('recordings.page.merged');
    if (recording.merge_status === 'pending') return t('recordings.page.pending');
    if (recording.merge_status === 'failed') return t('recordings.page.failed');
    return t('recordings.page.original');
  }

  $effect(() => {
    const _ = [selectedCameraId, selectedDate, selectedCameraArchived];
    if (selectedCameraId) {
      loadRecordings();
    }
  });

  onMount(() => {
    loadRecordingSources();
    loadGroups();
  });
</script>

<div class="playback-page">
  <div class="toolbar">
    <div class="toolbar-dates">
      <label for="date-select" class="sr-only">{t('recordings.page.date')}</label>
      <input
        id="date-select"
        type="date"
        class="input date-input"
        bind:value={selectedDate}
        max={formatDateForInput(new Date())}
      />
      <button onclick={prevDay} class="btn btn-ghost btn-sm" title={t('recordings.page.previousDay')}>
        <ChevronLeft size={16} />
      </button>
      <button onclick={nextDay} class="btn btn-ghost btn-sm" title={t('recordings.page.nextDay')}>
        <ChevronRight size={16} />
      </button>
      <button onclick={goToToday} class="btn btn-secondary btn-sm">{t('recordings.page.today')}</button>
      <button
        onclick={() => showCalendar = !showCalendar}
        class="btn btn-sm {showCalendar ? 'btn-primary' : 'btn-ghost'}"
        title={t('recordings.calendar.toggle')}
        aria-pressed={showCalendar}
      >
        <Calendar size={16} />
      </button>
      <button
        onclick={() => { continuousPlay = !continuousPlay; }}
        class="btn btn-sm {continuousPlay ? 'btn-primary' : 'btn-ghost'}"
        title={t('recordings.continuousHint')}
        aria-pressed={continuousPlay}
      >
        {t('recordings.continuousPlay')}
      </button>
      <select id="hour-select" class="input hour-input" bind:value={selectedHour}>
        <option value={-1}>{t('recordings.page.allHours')}</option>
        {#each Array(24) as _, h}
          <option value={h}>{String(h).padStart(2, '0')}:00</option>
        {/each}
      </select>
      <select class="input hour-input" bind:value={playbackRate} title={t('recordings.page.speed')}>
        {#each [0.5, 1, 2, 4, 8] as rate}
          <option value={rate}>{rate}x</option>
        {/each}
      </select>
      <select class="input hour-input" bind:value={sourceFilter} title={t('recordings.page.source')}>
        <option value="all">{t('recordings.page.allSources')}</option>
        <option value="nvr">NVR</option>
        <option value="gb">GB28181</option>
        <option value="onvif">ONVIF</option>
      </select>
      <button class="btn btn-secondary btn-sm" onclick={exportClip} title={t('recordings.page.exportHint')}>
        {t('recordings.page.export')}
      </button>
      {#if selectedIds.size > 0}
        <button class="btn btn-danger btn-sm" onclick={handleBatchDelete}>
          {t('recordings.page.batchDelete')} ({selectedIds.size})
        </button>
      {/if}
    </div>
    {#if showCalendar && selectedCameraId}
      <div class="calendar-pop">
        <RecordingCalendar
          cameraId={selectedCameraId}
          {selectedDate}
          onselect={(d) => { selectedDate = d; showCalendar = false; }}
        />
      </div>
    {/if}
  </div>

  <div class="playback-body">
    <aside class="device-pane">
      <div class="pane-head">
        <h3>{t('recordings.page.devices')}</h3>
        <span class="count">{visibleSourceCount}</span>
      </div>
      <div class="search-wrap">
        <Search size={14} class="search-icon" />
        <input
          class="input search-input"
          placeholder={t('recordings.page.searchCamera')}
          bind:value={cameraQuery}
        />
      </div>
      <div class="device-list">
        {#if cameras.length === 0 && visibleStreamSources.length === 0}
          <p class="empty-hint">{t('recordings.page.noSources')}</p>
        {:else}
          {#if visibleCameras.length > 0 && groupTree.length > 0}
          {#each groupTree as node (node.id)}
            <button class="device-item" onclick={() => toggleGroup(node.id)}>
              <span class="device-name">{expandedGroups.has(node.id) ? '▾' : '▸'} {node.name}</span>
            </button>
            {#if expandedGroups.has(node.id)}
              {#each (groupCameraIds[node.id] || []).map(id => cameras.find(c => c.id === id)).filter((c): c is Camera => !!c) as camera (camera.id)}
                <button
                  class="device-item nested"
                  class:active={selectedCameraId === camera.id}
                  onclick={() => selectCamera(camera.id)}
                >
                  <span class="status-dot {cameraStatusDot(camera)}"></span>
                  <span class="device-name">{camera.name}</span>
                </button>
              {/each}
            {/if}
          {/each}
          {#each visibleCameras.filter(c => !Object.values(groupCameraIds).flat().includes(c.id)) as camera (camera.id)}
            <button class="device-item" class:active={selectedCameraId === camera.id} onclick={() => selectCamera(camera.id)}>
              <span class="status-dot {cameraStatusDot(camera)}"></span>
              <span class="device-name">{camera.name}</span>
            </button>
          {/each}
          {:else if visibleCameras.length > 0}
          {#each visibleCameras as camera (camera.id)}
            <button
              class="device-item"
              class:active={selectedCameraId === camera.id}
              onclick={() => selectCamera(camera.id)}
            >
              <span class="status-dot {cameraStatusDot(camera)}"></span>
              <span class="device-name">{camera.name}</span>
              {#if camera.archived}
                <span class="badge badge-warning text-xs">{t('recordings.page.archived')}</span>
              {/if}
            </button>
          {/each}
          {/if}
          {#each visibleStreamSources as source (source.id)}
            <button class="device-item" class:active={selectedCameraId === source.id} onclick={() => selectCamera(source.id)}>
              <span class="status-dot dot-muted"></span>
              <span class="device-name">{source.name || source.stream_id || source.id}</span>
              <span class="badge badge-neutral text-xs">{t(`recordings.page.kind${source.kind === 'plan' ? 'Plan' : 'Stream'}`)}</span>
            </button>
          {/each}
          {#if visibleSourceCount === 0}
            <p class="empty-hint">{t('recordings.page.noCameras')}</p>
          {/if}
        {/if}
      </div>
    </aside>

    <section class="player-pane">
      {#if error}
        <div class="player-empty">
          <AlertCircle size={28} class="th-color-danger" />
          <p class="th-text-secondary">{error}</p>
          <button onclick={loadRecordings} class="btn btn-primary btn-sm">{t('recordings.page.retry')}</button>
        </div>
      {:else if remotePlayUrl}
        <div class="player-fill">
          <div class="px-4 py-2 text-sm th-text-secondary">{remotePlayLabel}</div>
          <video class="w-full h-full object-contain bg-black" controls autoplay src={remotePlayUrl}>
            <track kind="captions" />
          </video>
        </div>
      {:else if continuousPlay && selectedCameraId}
        <VodPlayer
          cameraId={selectedCameraId}
          start={dayRange.start}
          end={dayRange.end}
          seekToMs={vodSeekMs}
          {playbackRate}
          onTime={handleVodTime}
          embedded
        />
      {:else if selectedRecording}
        <InlinePlayer
          recording={selectedRecording}
          allRecordings={recordings}
          onClose={handleClosePlayer}
          onNavigate={handleNavigate}
          {playbackRate}
          seekOffsetSec={inlineSeekSec}
          onTime={handleInlineTime}
          embedded
        />
      {:else}
        <div class="player-empty">
          <Video size={40} class="th-text-muted" />
          <p class="th-text-secondary">{selectedCameraId ? t('recordings.page.noPlayer') : t('recordings.page.selectCamera')}</p>
        </div>
      {/if}
    </section>

    <aside class="file-pane">
      <div class="pane-head">
        <h3>{t('recordings.page.files')}</h3>
        <span class="count">{filteredRecordings.length}</span>
      </div>
      <div class="file-filters">
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
      <div class="file-list">
        {#if !selectedCameraId}
          <p class="empty-hint">{t('recordings.page.selectDeviceHint')}</p>
        {:else if loading}
          <div class="file-loading"><div class="spinner"></div></div>
        {:else if filteredRecordings.length === 0}
          <p class="empty-hint">{t('recordings.page.noRecordings')}</p>
        {:else}
          {#each filteredRecordings as recording (recording.id)}
            <div
              class="file-item"
              class:active={selectedRecording?.id === recording.id}
              role="button"
              tabindex="0"
              onclick={() => handleTimelineSelect(recording)}
              onkeydown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleTimelineSelect(recording); } }}
            >
              <input type="checkbox" checked={selectedIds.has(recording.id)} onclick={(e) => { e.stopPropagation(); toggleSelected(recording.id); }} />
              <div class="file-meta">
                <span class="file-time">{formatTime(recording.started_at)}</span>
                <span class="file-sub">{formatDuration(recording.duration)} · {formatFileSize(recording.file_size)}</span>
              </div>
              <div class="file-tags">
                <span class="badge badge-neutral text-xs">{recording.format.toUpperCase()}</span>
                <span class="badge text-xs {recording.merge_status === 'merged' ? 'badge-success' : recording.merge_status === 'failed' ? 'badge-error' : recording.archived ? 'badge-warning' : 'badge-neutral'}">{mergeLabel(recording)}</span>
              </div>
              <div class="file-actions">
                <button class="btn btn-ghost btn-sm" onclick={(e) => { e.stopPropagation(); handleTimelineSelect(recording); }} title={t('recordings.page.play')}>
                  <Play size={13} />
                </button>
                <button class="btn btn-ghost btn-sm" onclick={(e) => { e.stopPropagation(); handleDownload(recording); }} title={t('recordings.page.download')}>
                  <Download size={13} />
                </button>
                <button class="btn btn-ghost btn-sm" onclick={(e) => { e.stopPropagation(); handleToggleLock(recording); }} title={recording.locked ? t('recordings.page.unlock') : t('recordings.page.lock')}>
                  {#if recording.locked}<Lock size={13} />{:else}<Unlock size={13} />{/if}
                </button>
                <button class="btn btn-ghost btn-sm th-color-danger" disabled={recording.locked} onclick={(e) => { e.stopPropagation(); deleteConfirm = recording; }} title={t('recordings.page.delete')}>
                  <Trash2 size={13} />
                </button>
              </div>
            </div>
          {/each}
        {/if}
      </div>
    </aside>
  </div>

  <div class="timeline-pane">
    {#if selectedCameraId}
      <Timeline
        recordings={visibleTimeline.length ? visibleTimeline : recordings}
        selectedRecording={selectedRecording ?? undefined}
        {selectedHour}
        {playheadHour}
        events={dayEvents}
        {markInHour}
        {markOutHour}
        onSelect={handleTimelineSelect}
        onHourSelect={handleHourSelect}
        onSeek={handleTimelineSeek}
        onEventSelect={handleEventSelect}
        onMark={handleMark}
      />
    {:else}
      <p class="empty-hint">{t('recordings.page.selectDeviceHint')}</p>
    {/if}
  </div>
</div>

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

<style>
  .playback-page {
    height: calc(100vh - 56px);
    display: flex;
    flex-direction: column;
    background: var(--bg-primary);
    min-height: 0;
    overflow: hidden;
  }

  .toolbar {
    position: relative;
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--border);
    background: var(--bg-elevated);
    flex-shrink: 0;
  }

  .toolbar-dates {
    display: flex;
    align-items: center;
    gap: 0.375rem;
    flex-wrap: wrap;
  }

  .date-input {
    width: auto;
    min-width: 10rem;
    padding: 0.35rem 0.6rem;
  }

  .hour-input {
    width: auto;
    min-width: 7rem;
    padding: 0.35rem 0.6rem;
  }

  .calendar-pop {
    position: absolute;
    top: calc(100% + 4px);
    left: 0.75rem;
    z-index: 20;
    width: 18rem;
    padding: 0.75rem;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    box-shadow: var(--shadow-lg);
  }

  .playback-body {
    flex: 1;
    display: grid;
    grid-template-columns: 220px minmax(0, 1fr) 300px;
    min-height: 0;
  }

  .device-pane,
  .file-pane {
    display: flex;
    flex-direction: column;
    min-height: 0;
    background: var(--bg-elevated);
    border-right: 1px solid var(--border);
  }

  .file-pane {
    border-right: none;
    border-left: 1px solid var(--border);
  }

  .pane-head {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.625rem 0.75rem 0.375rem;
    flex-shrink: 0;
  }

  .pane-head h3 {
    margin: 0;
    font-size: 0.8rem;
    font-weight: 600;
    letter-spacing: 0.04em;
    text-transform: uppercase;
    color: var(--text-secondary);
  }

  .count {
    font-size: 0.7rem;
    color: var(--text-tertiary);
    font-variant-numeric: tabular-nums;
  }

  .search-wrap {
    position: relative;
    padding: 0 0.75rem 0.5rem;
  }

  .search-wrap :global(.search-icon) {
    position: absolute;
    left: 1.1rem;
    top: 50%;
    transform: translateY(-70%);
    color: var(--text-tertiary);
    pointer-events: none;
  }

  .search-input {
    padding-left: 1.75rem;
    height: 2rem;
    font-size: 0.8rem;
  }

  .device-list,
  .file-list {
    flex: 1;
    overflow-y: auto;
    min-height: 0;
  }

  .device-item {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    width: 100%;
    padding: 0.55rem 0.75rem;
    background: transparent;
    border: none;
    border-left: 2px solid transparent;
    color: var(--text-primary);
    text-align: left;
    cursor: pointer;
  }

  .device-item:hover {
    background: var(--bg-hover);
  }

  .device-item.active {
    background: color-mix(in srgb, var(--color-primary) 12%, transparent);
    border-left-color: var(--color-primary);
  }

  .device-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.85rem;
  }

  .status-dot {
    width: 7px;
    height: 7px;
    border-radius: 999px;
    flex-shrink: 0;
  }

  .dot-live { background: var(--color-success); }
  .dot-danger { background: var(--color-danger); }
  .dot-warn { background: var(--color-warning); }
  .dot-idle { background: var(--text-tertiary); }
  .dot-muted { background: var(--border-hover); }

  .player-pane {
    min-width: 0;
    min-height: 0;
    background: #000;
    display: flex;
    flex-direction: column;
  }

  .player-pane :global(.player-fill),
  .player-pane :global(#inline-player) {
    flex: 1;
    min-height: 0;
  }

  .player-empty {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 0.75rem;
    color: var(--text-tertiary);
    background: var(--bg-secondary);
  }

  .file-filters {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0.375rem;
    padding: 0 0.75rem 0.5rem;
  }

  .device-item.nested {
    padding-left: 1.5rem;
  }

  .file-item {
    display: grid;
    grid-template-columns: auto 1fr auto;
    grid-template-rows: auto auto;
    gap: 0.15rem 0.5rem;
    padding: 0.55rem 0.75rem;
    border-bottom: 1px solid var(--border);
    cursor: pointer;
  }

  .file-item:hover {
    background: var(--bg-hover);
  }

  .file-item.active {
    background: color-mix(in srgb, var(--color-primary) 12%, transparent);
  }

  .file-meta {
    display: flex;
    flex-direction: column;
    min-width: 0;
  }

  .file-time {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.8rem;
    color: var(--text-primary);
  }

  .file-sub {
    font-size: 0.7rem;
    color: var(--text-tertiary);
  }

  .file-tags {
    display: flex;
    gap: 0.25rem;
    align-items: center;
    justify-content: flex-end;
  }

  .file-actions {
    grid-column: 1 / -1;
    display: none;
    justify-content: flex-end;
    gap: 0.125rem;
  }

  .file-item:hover .file-actions,
  .file-item.active .file-actions,
  .file-item:focus-within .file-actions {
    display: flex;
  }

  .timeline-pane {
    flex-shrink: 0;
    padding: 0.75rem 1rem 1rem;
    border-top: 1px solid var(--border);
    background: var(--bg-elevated);
  }

  .empty-hint {
    padding: 1.25rem 0.75rem;
    text-align: center;
    font-size: 0.8rem;
    color: var(--text-tertiary);
  }

  .file-loading {
    display: flex;
    justify-content: center;
    padding: 2rem 0;
  }

  .sr-only {
    position: absolute;
    width: 1px;
    height: 1px;
    padding: 0;
    margin: -1px;
    overflow: hidden;
    clip: rect(0, 0, 0, 0);
    white-space: nowrap;
    border: 0;
  }

  @media (max-width: 1023px) {
    .playback-body {
      grid-template-columns: 180px minmax(0, 1fr) 240px;
    }
  }

  @media (max-width: 767px) {
    .playback-page {
      height: auto;
      overflow: visible;
    }

    .playback-body {
      display: flex;
      flex-direction: column;
      min-height: 70vh;
    }

    .device-pane,
    .file-pane {
      border: none;
      border-bottom: 1px solid var(--border);
      max-height: 220px;
    }

    .player-pane {
      min-height: 240px;
    }
  }
</style>
