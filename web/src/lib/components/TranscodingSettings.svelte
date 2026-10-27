<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { t } from '$lib/i18n';
  import { getTranscodeCapabilities, probeTranscodeCapabilities } from '$lib/api/transcoding';
  import type { CapabilityReport, TranscodeBackend } from '$lib/api/transcoding';

  let backend = $state<TranscodeBackend>('ffmpeg');
  let reports = $state<CapabilityReport[]>([]);
  let loading = $state(true);
  let checking = $state(false);
  let error = $state('');
  const controller = new AbortController();
  let report = $derived(reports.find(r => r.backend === backend));

  async function load() {
    error = ''; loading = true;
    try {
      const result = await getTranscodeCapabilities(controller.signal);
      if (!controller.signal.aborted) reports = result.reports;
    } catch (e) {
      if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('transcode.error');
    } finally { loading = false; }
  }
  async function probe() {
    if (checking) return;
    checking = true; error = '';
    try {
      const result = await probeTranscodeCapabilities(backend, controller.signal);
      if (!controller.signal.aborted) reports = [...reports.filter(r => r.backend !== result.backend), result];
    } catch (e) {
      if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('transcode.error');
    } finally { checking = false; }
  }
  onMount(() => { void load(); });
  onDestroy(() => controller.abort());
</script>

<section class="card p-6 border th-border space-y-5" aria-label={t('transcode.title')}>
  <header>
    <h3 class="text-lg font-semibold th-text-primary">{t('transcode.title')}</h3>
    <p class="text-sm th-text-secondary mt-1">{t('transcode.description')}</p>
  </header>
  {#if error}<p class="text-sm th-color-danger" role="alert">{error}</p>{/if}
  {#if loading}
    <div role="status" class="th-text-secondary">{t('common.loading')}</div>
  {:else}
    <div class="flex flex-wrap items-end gap-3">
      <div class="space-y-2">
        <label class="input-label" for="transcode-backend">{t('transcode.backend')}</label>
        <select id="transcode-backend" class="input" bind:value={backend} disabled={checking}>
          <option value="ffmpeg">FFmpeg</option>
          <option value="gstreamer">GStreamer</option>
        </select>
      </div>
      <button type="button" class="btn btn-primary active:scale-[0.98]" onclick={probe} disabled={checking}>
        {checking ? t('transcode.checking') : t('transcode.probe')}
      </button>
      <button type="button" class="btn btn-secondary" onclick={load} disabled={checking}>{t('transcode.refresh')}</button>
    </div>
    <div aria-live="polite" aria-busy={checking} class="space-y-3">
      {#if report}
        <p class="text-sm font-medium th-text-primary">
          {report.stale ? t('transcode.stale') : report.can_enable ? t('transcode.ready') : t('transcode.notReady')}
        </p>
        <p class="text-xs th-text-tertiary">{t('transcode.checkedAt')}: {new Date(report.checked_at).toLocaleString()}</p>
        <ul class="divide-y th-border">
          {#each report.checks as check}
            <li class="py-3 space-y-1">
              <div class="flex flex-wrap justify-between gap-2 text-sm">
                <span class="font-medium th-text-primary">{check.name}</span>
                <span class:th-color-danger={check.status === 'failed'}>{t(`transcode.status.${check.status}`)}</span>
              </div>
              <p class="text-xs th-text-secondary break-words">{check.detail}</p>
            </li>
          {/each}
        </ul>
      {:else}<p class="text-sm th-text-secondary">{t('transcode.empty')}</p>{/if}
    </div>
    <p class="text-xs th-text-tertiary">{t('transcode.boundary')}</p>
  {/if}
</section>
