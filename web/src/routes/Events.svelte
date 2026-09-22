<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { listEvents, acknowledgeEvent, listCameras, listAlarmRules, createAlarmRule, deleteAlarmRule, subscribeNvrEvents } from '$lib/api';
  import type { Camera, EventsResponse, NvrEvent, AlarmRule } from '$lib/api';
  import { formatDate } from '$lib/format';
  import { t } from '$lib/i18n';
  import { Activity, AlertCircle, Check, ExternalLink, RefreshCw } from 'lucide-svelte';
  import Pagination from '../components/Pagination.svelte';

  let { initialCameraId = '' }: { initialCameraId?: string } = $props();

  let events = $state<NvrEvent[]>([]);
  let cameras = $state<Camera[]>([]);
  let total = $state(0);
  let loading = $state(true);
  let error = $state('');
  let cameraFilter = $state(untrack(() => initialCameraId));
  let sourceFilter = $state('');
  let statusFilter = $state('');
  let page = $state(0);
  const pageSize = 20;
  let rules = $state<AlarmRule[]>([]);
  let ruleName = $state('');
  let ruleAction = $state<'record' | 'webhook' | 'goto_preset'>('record');
  let ruleTarget = $state('');
  let ruleSource = $state('');

  const sources = [
    { value: '', label: () => t('events.filters.allSources') },
    { value: 'health', label: () => t('events.sources.health') },
    { value: 'recorder', label: () => t('events.sources.recorder') },
    { value: 'ai', label: () => t('events.sources.ai') },
    { value: 'mqtt', label: () => t('events.sources.mqtt') },
  ];

  const statuses = [
    { value: '', label: () => t('events.filters.allStatuses') },
    { value: 'open', label: () => t('events.status.open') },
    { value: 'acknowledged', label: () => t('events.status.acknowledged') },
  ];

  function getCameraName(cameraId: string): string {
    const camera = cameras.find((c) => c.id === cameraId);
    return camera?.name || cameraId || t('streams.unknown');
  }

  function sourceLabel(source: string): string {
    switch (source) {
      case 'health': return t('events.sources.health');
      case 'recorder': return t('events.sources.recorder');
      case 'ai': return t('events.sources.ai');
      case 'mqtt': return t('events.sources.mqtt');
      default: return source;
    }
  }

  function typeLabel(type: string): string {
    switch (type) {
      case 'connection_lost': return t('health.eventTypes.connectionLost');
      case 'connection_restored': return t('health.eventTypes.connectionRestored');
      case 'stream_anomaly': return t('health.eventTypes.streamAnomaly');
      case 'freeze_detected': return t('health.eventTypes.freezeDetected');
      case 'freeze_recovered': return t('health.eventTypes.freezeRecovered');
      case 'recorder_reconnected': return t('events.types.recorderReconnected');
      default: return type;
    }
  }

  function severityClass(severity: string): string {
    switch (severity) {
      case 'critical': return 'badge badge-danger';
      case 'warning': return 'badge bg-amber-100 text-amber-800 dark:bg-amber-900/50 dark:text-amber-300';
      default: return 'badge badge-neutral';
    }
  }

  function statusClass(status: string): string {
    return status === 'acknowledged' ? 'badge badge-success' : 'badge badge-neutral';
  }

  function statusLabel(status: string): string {
    return status === 'acknowledged' ? t('events.status.acknowledged') : t('events.status.open');
  }

  function openRecording(event: NvrEvent) {
    if (!event.recording_id) return;
    window.location.hash = `#/recordings/${encodeURIComponent(event.recording_id)}`;
  }

  async function ack(event: NvrEvent) {
    if (event.status === 'acknowledged') return;
    try {
      await acknowledgeEvent(event.id);
      events = events.map((item) =>
        item.id === event.id
          ? { ...item, status: 'acknowledged', acknowledged_at: new Date().toISOString() }
          : item
      );
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    }
  }

  async function loadEvents() {
    loading = true;
    error = '';
    try {
      const response: EventsResponse = await listEvents({
        camera_id: cameraFilter || undefined,
        source: sourceFilter || undefined,
        status: statusFilter || undefined,
        limit: pageSize,
        offset: page * pageSize,
      });
      events = response.events;
      total = response.total;
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    } finally {
      loading = false;
    }
  }

  async function loadCameras() {
    try {
      cameras = await listCameras();
    } catch (e) {
      console.warn('Failed to load cameras:', e);
    }
  }

  let currentPage = $derived(page + 1);
  let totalPages = $derived(Math.ceil(total / pageSize));

  function handlePageChange(newPage: number) {
    page = newPage - 1;
    window.scrollTo(0, 0);
  }

  let prevCameraFilter = $state('');
  let prevSourceFilter = $state('');
  let prevStatusFilter = $state('');
  $effect(() => {
    if (cameraFilter !== prevCameraFilter || sourceFilter !== prevSourceFilter || statusFilter !== prevStatusFilter) {
      prevCameraFilter = cameraFilter;
      prevSourceFilter = sourceFilter;
      prevStatusFilter = statusFilter;
      page = 0;
    }
  });

  $effect(() => {
    const _ = [cameraFilter, sourceFilter, statusFilter, page];
    loadEvents();
  });

  $effect(() => {
    const cam = cameraFilter;
    const src = sourceFilter;
    return subscribeNvrEvents(
      { camera_id: cam || undefined, source: src || undefined },
      () => { void loadEvents(); },
      { debounceMs: 400 },
    );
  });

  async function loadRules() {
    try { rules = await listAlarmRules(); } catch { rules = []; }
  }

  async function addRule() {
    try {
      await createAlarmRule({
        name: ruleName || ruleAction,
        camera_id: cameraFilter,
        source: ruleSource,
        action: ruleAction,
        action_target: ruleTarget,
        enabled: true,
      });
      ruleName = '';
      ruleTarget = '';
      await loadRules();
    } catch (e) {
      error = e instanceof Error ? e.message : t('common.error');
    }
  }

  onMount(() => {
    loadCameras();
    loadRules();
  });
</script>

<div class="min-h-screen th-bg-primary ">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div>
        <h1 class="text-2xl font-semibold th-text-primary">{t('events.title')}</h1>
        <p class="text-sm th-text-secondary mt-1">{t('events.subtitle')}</p>
      </div>
      <button class="btn btn-secondary btn-sm inline-flex items-center gap-2" onclick={loadEvents}>
        <RefreshCw size={16} />
        <span>{t('common.refresh')}</span>
      </button>
    </div>

    <div class="card p-5 mb-6 border th-border">
      <div class="grid grid-cols-1 sm:grid-cols-3 gap-3">
        <div>
          <label for="event-camera-filter" class="input-label">{t('health.filter.camera')}</label>
          <select id="event-camera-filter" class="input" bind:value={cameraFilter}>
            <option value="">{t('health.filter.all')}</option>
            {#each cameras as camera}
              <option value={camera.id}>{camera.name}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="event-source-filter" class="input-label">{t('events.filters.source')}</label>
          <select id="event-source-filter" class="input" bind:value={sourceFilter}>
            {#each sources as source}
              <option value={source.value}>{source.label()}</option>
            {/each}
          </select>
        </div>
        <div>
          <label for="event-status-filter" class="input-label">{t('events.filters.status')}</label>
          <select id="event-status-filter" class="input" bind:value={statusFilter}>
            {#each statuses as status}
              <option value={status.value}>{status.label()}</option>
            {/each}
          </select>
        </div>
      </div>
    </div>

    <div class="card p-5 mb-6 border th-border">
      <h2 class="text-sm font-semibold th-text-primary mb-3">{t('events.rules.title')}</h2>
      <div class="flex flex-wrap gap-2 mb-3">
        <input class="input" style="max-width:10rem" placeholder={t('events.rules.name')} bind:value={ruleName} />
        <select class="input" style="max-width:8rem" bind:value={ruleSource}>
          <option value="">{t('events.filters.allSources')}</option>
          <option value="health">health</option>
          <option value="recorder">recorder</option>
          <option value="ai">ai</option>
          <option value="mqtt">mqtt</option>
        </select>
        <select class="input" style="max-width:9rem" bind:value={ruleAction}>
          <option value="record">{t('events.rules.record')}</option>
          <option value="webhook">Webhook</option>
          <option value="goto_preset">{t('events.rules.preset')}</option>
        </select>
        <input class="input" style="max-width:16rem" placeholder={t('events.rules.target')} bind:value={ruleTarget} />
        <button class="btn btn-primary btn-sm" onclick={addRule}>{t('events.rules.add')}</button>
      </div>
      {#each rules as rule (rule.id)}
        <div class="flex items-center justify-between py-2 border-t th-border text-sm">
          <span>{rule.name} · {rule.action} {rule.action_target} · {rule.source || '*'} / {rule.camera_id || '*'}</span>
          <button class="btn btn-ghost btn-sm" onclick={async () => { await deleteAlarmRule(rule.id); await loadRules(); }}>{t('recordings.page.delete')}</button>
        </div>
      {/each}
    </div>

    {#if error}
      <div class="card border th-border-danger p-8 text-center mb-6">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={40} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={loadEvents} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    <div class="card border th-border">
      {#if loading && events.length === 0}
        <div class="p-6 space-y-4">
          {#each Array(6) as _}
            <div class="grid grid-cols-5 gap-4 items-center">
              <div class="h-4 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 th-bg-tertiary rounded animate-pulse"></div>
              <div class="h-4 th-bg-tertiary rounded animate-pulse"></div>
            </div>
          {/each}
        </div>
      {:else if events.length === 0}
        <div class="p-12 text-center">
          <div class="flex justify-center mb-4 th-text-muted">
            <Activity size={48} />
          </div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('events.empty')}</h3>
        </div>
      {:else}
        <div class="table-container th-border">
          <table class="table">
            <thead>
              <tr>
                <th>{t('health.time')}</th>
                <th>{t('health.camera')}</th>
                <th>{t('events.source')}</th>
                <th>{t('health.eventType')}</th>
                <th>{t('events.severity')}</th>
                <th>{t('health.status')}</th>
                <th>{t('health.message')}</th>
                <th>{t('streams.actions')}</th>
              </tr>
            </thead>
            <tbody>
              {#each events as event (event.id)}
                <tr class="transition-all duration-200 hover:th-bg-hover">
                  <td class="whitespace-nowrap">{formatDate(event.started_at)}</td>
                  <td><span class="font-medium th-text-primary">{getCameraName(event.camera_id)}</span></td>
                  <td><span class="badge badge-neutral text-xs">{sourceLabel(event.source)}</span></td>
                  <td>{typeLabel(event.type)}</td>
                  <td><span class={severityClass(event.severity)}>{event.severity}</span></td>
                  <td><span class={statusClass(event.status)}>{statusLabel(event.status)}</span></td>
                  <td class="th-text-secondary text-sm max-w-[280px] truncate" title={event.message}>{event.message || '-'}</td>
                  <td>
                    <div class="flex items-center gap-2">
                      <button
                        class="btn btn-ghost btn-sm icon-btn"
                        title={t('events.ack')}
                        disabled={event.status === 'acknowledged'}
                        onclick={() => ack(event)}
                      >
                        <Check size={16} />
                      </button>
                      <button
                        class="btn btn-ghost btn-sm icon-btn"
                        title={t('events.openRecording')}
                        disabled={!event.recording_id}
                        onclick={() => openRecording(event)}
                      >
                        <ExternalLink size={16} />
                      </button>
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>

        {#if totalPages > 1}
          <div class="px-4 py-2 border-t th-border">
            <span class="text-sm th-text-muted">
              {t('recordings.showing', {
                start: String(page * pageSize + 1),
                end: String(Math.min(page * pageSize + events.length, total)),
                total: String(total)
              })}
            </span>
          </div>
          <Pagination {currentPage} {totalPages} onPageChange={handlePageChange} />
        {/if}

        {#if loading && events.length > 0}
          <div class="px-4 py-2 th-bg-secondary border-t th-border text-center">
            <span class="text-sm th-text-muted">{t('recordings.refreshing')}</span>
          </div>
        {/if}
      {/if}
    </div>
  </main>
</div>

<style>
  .icon-btn {
    width: 2rem;
    height: 2rem;
    padding: 0;
    display: inline-flex;
    align-items: center;
    justify-content: center;
  }
</style>
