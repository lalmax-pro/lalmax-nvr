<script lang="ts">
  import { onMount } from 'svelte';
  import {
    listRecordingPlans,
    createRecordingPlan,
    updateRecordingPlan,
    deleteRecordingPlan,
    listStreams,
    listCameras,
  } from '$lib/api';
  import type { RecordingPlan, RecordingMode, RecordingScheduleRange, StreamInfo, Camera } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import RecordingScheduleEditor from '$lib/components/RecordingScheduleEditor.svelte';
  import { Plus, Trash2, CalendarClock, Radio, Pause, Play, Pencil, X } from 'lucide-svelte';

  let { initialStreamId = '' }: { initialStreamId?: string } = $props();

  const modes: RecordingMode[] = ['continuous', 'scheduled', 'event', 'adaptive', 'off'];

  let plans = $state<RecordingPlan[]>([]);
  let streams = $state<StreamInfo[]>([]);
  let cameras = $state<Camera[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');

  let editing = $state<RecordingPlan | null>(null);
  let creating = $state(false);

  let formStreamID = $state('');
  let formName = $state('');
  let formMode = $state<RecordingMode>('continuous');
  let formEnabled = $state(true);
  let formWindows = $state<RecordingScheduleRange[]>([]);

  let plannedStreams = $derived(new Set(plans.map((p) => p.stream_id)));
  let unboundStreams = $derived(streams.filter((s) => !plannedStreams.has(s.stream_id)));

  function streamLabel(streamID: string): string {
    const cam = cameras.find((c) => (c.stream_id || c.id) === streamID);
    if (cam) return `${cam.name} (${streamID})`;
    const stream = streams.find((s) => s.stream_id === streamID);
    if (stream?.camera_name) return `${stream.camera_name} (${streamID})`;
    return streamID;
  }

  async function load() {
    loading = true;
    error = '';
    try {
      const [planList, streamList, camList] = await Promise.all([
        listRecordingPlans(),
        listStreams({ limit: 500 }),
        listCameras(),
      ]);
      plans = planList;
      streams = streamList.streams ?? [];
      cameras = camList;
    } catch (e) {
      console.warn('Failed to load recording plans:', e);
      error = t('recordingPlans.loadFailed');
    } finally {
      loading = false;
    }
  }

  function startCreate(streamID = '') {
    creating = true;
    editing = null;
    formStreamID = streamID || unboundStreams[0]?.stream_id || '';
    formName = '';
    formMode = 'continuous';
    formEnabled = true;
    formWindows = [];
  }

  function startEdit(plan: RecordingPlan) {
    creating = false;
    editing = plan;
    formStreamID = plan.stream_id;
    formName = plan.name;
    formMode = plan.mode;
    formEnabled = plan.enabled;
    formWindows = [...(plan.windows ?? [])];
  }

  function cancelForm() {
    creating = false;
    editing = null;
  }

  function validate(): boolean {
    if (!formStreamID.trim()) {
      showToast(t('recordingPlans.streamRequired'), 'error');
      return false;
    }
    if (formMode === 'scheduled' && formWindows.length === 0) {
      showToast(t('recordingPlans.windowsRequired'), 'error');
      return false;
    }
    if (formMode === 'scheduled') {
      for (const w of formWindows) {
        if (w.start_time === w.end_time) {
          showToast(t('cameras.recordingSchedule.invalidRange'), 'error');
          return false;
        }
      }
    }
    return true;
  }

  async function save() {
    if (saving || !validate()) return;
    saving = true;
    try {
      const body = {
        stream_id: formStreamID.trim(),
        name: formName.trim() || formStreamID.trim(),
        mode: formMode,
        enabled: formEnabled,
        windows: formMode === 'scheduled' ? formWindows : [],
      };
      if (editing) {
        await updateRecordingPlan(editing.id, body);
        showToast(t('recordingPlans.updated'), 'success');
      } else {
        await createRecordingPlan(body);
        showToast(t('recordingPlans.created'), 'success');
      }
      cancelForm();
      await load();
    } catch (e) {
      console.warn('Failed to save recording plan:', e);
      showToast(editing ? t('recordingPlans.updateFailed') : t('recordingPlans.createFailed'), 'error');
    } finally {
      saving = false;
    }
  }

  async function toggleEnabled(plan: RecordingPlan) {
    try {
      await updateRecordingPlan(plan.id, { enabled: !plan.enabled });
      await load();
    } catch (e) {
      console.warn('Failed to toggle recording plan:', e);
      showToast(t('recordingPlans.updateFailed'), 'error');
    }
  }

  async function remove(plan: RecordingPlan) {
    if (!confirm(t('recordingPlans.deleteConfirm', { stream: plan.stream_id }))) return;
    try {
      await deleteRecordingPlan(plan.id);
      showToast(t('recordingPlans.deleted'), 'success');
      if (editing?.id === plan.id) cancelForm();
      await load();
    } catch (e) {
      console.warn('Failed to delete recording plan:', e);
      showToast(t('recordingPlans.deleteFailed'), 'error');
    }
  }

  onMount(async () => {
    await load();
    const prefill = initialStreamId.trim();
    if (!prefill) return;
    const existing = plans.find((p) => p.stream_id === prefill);
    if (existing) startEdit(existing);
    else startCreate(prefill);
  });
</script>

<div class="page">
  <div class="page-header">
    <div>
      <h1 class="page-title">{t('recordingPlans.title')}</h1>
      <p class="page-hint">{t('recordingPlans.hint')}</p>
    </div>
    <button class="btn btn-primary flex items-center gap-2" onclick={startCreate} disabled={creating}>
      <Plus size={16} />
      {t('recordingPlans.create')}
    </button>
  </div>

  {#if loading}
    <div class="th-text-muted py-8">{t('common.loading')}</div>
  {:else if error}
    <div class="th-text-danger py-8">{error}</div>
  {:else}
    {#if creating || editing}
      <section class="card mb-6">
        <div class="flex items-center justify-between mb-4">
          <h2 class="th-text-secondary font-medium">
            {editing ? t('recordingPlans.edit') : t('recordingPlans.create')}
          </h2>
          <button class="btn btn-ghost p-1.5" onclick={cancelForm} title={t('common.cancel')}>
            <X size={16} />
          </button>
        </div>

        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div>
            <label class="input-label" for="plan-stream">{t('recordingPlans.stream')}</label>
            {#if editing}
              <input id="plan-stream" class="input" value={formStreamID} disabled />
            {:else}
              <input
                id="plan-stream"
                class="input"
                list="plan-stream-options"
                bind:value={formStreamID}
                placeholder="live/obs-1"
              />
              <datalist id="plan-stream-options">
                {#each unboundStreams as s}
                  <option value={s.stream_id}>{streamLabel(s.stream_id)}</option>
                {/each}
              </datalist>
              <p class="th-text-muted text-xs mt-1">{t('recordingPlans.streamHint')}</p>
            {/if}
          </div>
          <div>
            <label class="input-label" for="plan-name">{t('recordingPlans.name')}</label>
            <input id="plan-name" class="input" bind:value={formName} placeholder={formStreamID} />
          </div>
        </div>

        <div class="mt-4">
          <span class="input-label">{t('cameras.recordingMode.title')}</span>
          <div class="flex flex-wrap gap-2 mt-2">
            {#each modes as mode}
              <button
                type="button"
                class="btn px-3 py-1.5 text-sm {formMode === mode ? 'btn-primary' : 'btn-ghost'}"
                onclick={() => (formMode = mode)}
              >
                {t(`cameras.recordingMode.${mode}`)}
              </button>
            {/each}
          </div>
          <p class="th-text-muted text-xs mt-2">{t('cameras.recordingMode.hint')}</p>
        </div>

        <label class="flex items-center gap-2 mt-4 text-sm th-text-secondary">
          <input type="checkbox" bind:checked={formEnabled} />
          {t('recordingPlans.enabled')}
        </label>

        {#if formMode === 'scheduled'}
          <RecordingScheduleEditor ranges={formWindows} onchange={(next) => (formWindows = next)} />
        {/if}

        <div class="flex justify-end gap-2 mt-4">
          <button class="btn btn-ghost" onclick={cancelForm}>{t('common.cancel')}</button>
          <button class="btn btn-primary" onclick={save} disabled={saving}>
            {saving ? t('common.saving') : t('common.save')}
          </button>
        </div>
      </section>
    {/if}

    {#if plans.length === 0 && !creating}
      <div class="empty">
        <CalendarClock size={32} class="th-text-muted" />
        <p class="th-text-secondary mt-3">{t('recordingPlans.empty')}</p>
        <p class="th-text-muted text-sm mt-1">{t('recordingPlans.emptyHint')}</p>
      </div>
    {:else}
      <div class="plan-list">
        {#each plans as plan}
          <div class="plan-row">
            <div class="plan-main">
              <div class="plan-title">
                <Radio size={14} />
                <span class="font-medium">{plan.name || plan.stream_id}</span>
                {#if !plan.enabled}
                  <span class="badge off">{t('recordingPlans.paused')}</span>
                {/if}
              </div>
              <div class="plan-meta">
                <code>{plan.stream_id}</code>
                <span class="badge mode">{t(`cameras.recordingMode.${plan.mode}`)}</span>
                {#if plan.mode === 'scheduled'}
                  <span class="th-text-muted text-xs">
                    {t('recordingPlans.windowCount', { count: plan.windows?.length ?? 0 })}
                  </span>
                {/if}
              </div>
            </div>
            <div class="plan-actions">
              <button class="btn btn-ghost p-2" title={plan.enabled ? t('recordingPlans.pause') : t('recordingPlans.resume')} onclick={() => toggleEnabled(plan)}>
                {#if plan.enabled}
                  <Pause size={14} />
                {:else}
                  <Play size={14} />
                {/if}
              </button>
              <button class="btn btn-ghost p-2" title={t('common.edit')} onclick={() => startEdit(plan)}>
                <Pencil size={14} />
              </button>
              <button class="btn btn-ghost p-2" title={t('common.delete')} onclick={() => remove(plan)}>
                <Trash2 size={14} />
              </button>
            </div>
          </div>
        {/each}
      </div>
    {/if}
  {/if}
</div>

<style>
  .page {
    padding: 1.5rem 1.75rem 2.5rem;
    max-width: 960px;
  }
  .page-header {
    display: flex;
    align-items: flex-start;
    justify-content: space-between;
    gap: 1rem;
    margin-bottom: 1.5rem;
  }
  .page-title {
    font-size: 1.375rem;
    font-weight: 600;
  }
  .page-hint {
    margin-top: 0.35rem;
    color: var(--color-text-muted);
    font-size: 0.875rem;
  }
  .card {
    border: 1px solid var(--color-border);
    border-radius: 0.75rem;
    padding: 1.25rem;
    background: var(--color-surface);
  }
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    padding: 3rem 1rem;
    border: 1px dashed var(--color-border);
    border-radius: 0.75rem;
  }
  .plan-list {
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .plan-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 1rem;
    border: 1px solid var(--color-border);
    border-radius: 0.75rem;
    padding: 0.9rem 1rem;
    background: var(--color-surface);
  }
  .plan-title {
    display: flex;
    align-items: center;
    gap: 0.45rem;
  }
  .plan-meta {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    margin-top: 0.35rem;
    font-size: 0.8rem;
  }
  .plan-meta code {
    font-size: 0.75rem;
    color: var(--color-text-muted);
  }
  .plan-actions {
    display: flex;
    align-items: center;
    gap: 0.15rem;
  }
  .badge {
    font-size: 0.7rem;
    padding: 0.1rem 0.45rem;
    border-radius: 999px;
  }
  .badge.mode {
    background: color-mix(in srgb, var(--color-primary) 14%, transparent);
    color: var(--color-primary);
  }
  .badge.off {
    background: var(--color-bg-tertiary);
    color: var(--color-text-tertiary);
  }
</style>
