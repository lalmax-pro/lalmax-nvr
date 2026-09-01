<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getCameraFlow } from '$lib/api';
  import type { CameraFlow } from '$lib/api';
  import { t } from '$lib/i18n';

  interface Props {
    cameraId: string;
  }

  let { cameraId }: Props = $props();
  let flow = $state<CameraFlow | null>(null);
  let error = $state('');
  let timer: ReturnType<typeof setInterval> | undefined;

  async function refresh() {
    try {
      flow = await getCameraFlow(cameraId);
      error = '';
    } catch (e) {
      error = e instanceof Error ? e.message : 'flow failed';
    }
  }

  onMount(() => {
    void refresh();
    timer = setInterval(() => { void refresh(); }, 2000);
  });

  onDestroy(() => {
    if (timer) clearInterval(timer);
  });

  function viewerSummary(viewers: Record<string, number>): string {
    const parts = Object.entries(viewers).filter(([, n]) => n > 0).map(([k, n]) => `${k}:${n}`);
    return parts.length ? parts.join(' · ') : t('flow.noViewers');
  }
</script>

<div class="mt-3 rounded-md border th-border p-2 text-xs font-mono th-bg-secondary">
  {#if error}
    <p class="th-text-muted">{error}</p>
  {:else if !flow}
    <p class="th-text-muted">{t('common.loading')}</p>
  {:else}
    <div class="space-y-1 th-text-secondary">
      <div>
        <span class="th-text-muted">{t('flow.source')}</span>
        {flow.source.url_host || '—'}
        {#if flow.source.transport} ({flow.source.transport}){/if}
        · {flow.source.active ? t('flow.active') : t('flow.idle')}
      </div>
      <div class="pl-3 border-l th-border">
        <span class="th-text-muted">{t('flow.engine')}</span>
        {#if flow.engine}
          {flow.engine.video_codec || '—'}
          {#if flow.engine.fps} · {flow.engine.fps.toFixed(1)} fps{/if}
          {#if flow.engine.last_frame_age_s >= 0}
            · {t('flow.age', { sec: flow.engine.last_frame_age_s.toFixed(1) })}
          {/if}
        {:else}
          {t('flow.idle')}
        {/if}
      </div>
      <div class="pl-6 border-l th-border">
        <span class="th-text-muted">{t('flow.recording')}</span>
        {flow.recording.status}
        {#if flow.recording.paused} · {t('flow.paused')}{/if}
        {#if flow.recording.merge_pending > 0}
          · {t('flow.mergePending', { n: flow.recording.merge_pending })}
        {/if}
      </div>
      <div class="pl-6 border-l th-border">
        <span class="th-text-muted">{t('flow.viewers')}</span>
        {viewerSummary(flow.viewers_by_protocol)}
        {#if flow.substream.active} · {t('flow.subActive')}{/if}
      </div>
    </div>
  {/if}
</div>
