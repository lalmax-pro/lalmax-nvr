<script lang="ts">
  import { onMount } from 'svelte';
  import { getVoIPCallHistory } from '$lib/api';
  import type { VoIPCallHistoryEntry } from '$lib/api';
  import { t } from '$lib/i18n';

  const pageSize = 20;
  let items = $state<VoIPCallHistoryEntry[]>([]);
  let total = $state(0);
  let offset = $state(0);
  let loading = $state(true);
  let error = $state('');
  let stopped = false;
  let controller: AbortController | undefined;

  async function load(nextOffset = offset) {
    if (stopped) return;
    controller?.abort();
    controller = new AbortController();
    loading = true;
    try {
      const result = await getVoIPCallHistory(pageSize, nextOffset, controller.signal);
      if (!stopped) {
        items = result.items;
        total = result.total;
        offset = result.offset;
        error = '';
      }
    } catch (e) {
      if (!stopped && !(e instanceof DOMException && e.name === 'AbortError')) {
        error = e instanceof Error ? e.message : String(e);
      }
    } finally {
      if (!stopped) loading = false;
    }
  }

  function outcomeLabel(outcome: string) {
    const key = `settings.voip.historyOutcome.${outcome}`;
    return t(key) === key ? outcome : t(key);
  }

  function formatDate(value: string) {
    return new Date(value).toLocaleString();
  }

  function formatDuration(seconds: number) {
    const minutes = Math.floor(seconds / 60);
    const remainder = seconds % 60;
    return minutes ? `${minutes}${t('settings.voip.minutes')} ${remainder}${t('settings.voip.seconds')}` : `${remainder}${t('settings.voip.seconds')}`;
  }

  onMount(() => {
    void load(0);
    const timer = setInterval(() => void load(offset), 15000);
    return () => {
      stopped = true;
      controller?.abort();
      clearInterval(timer);
    };
  });
</script>

<section class="overflow-hidden rounded-xl border th-border th-bg-primary" aria-label={t('settings.voip.callHistory')}>
  <header class="flex flex-wrap items-center justify-between gap-3 border-b th-border px-4 py-4 sm:px-5">
    <div>
      <h3 class="font-semibold th-text-primary">{t('settings.voip.callHistory')}</h3>
      <p class="mt-1 text-xs th-text-secondary">{t('settings.voip.callHistoryHint')}</p>
    </div>
    <button class="btn btn-ghost text-sm" type="button" onclick={() => load(offset)} disabled={loading}>
      {loading ? t('common.loading') : t('settings.voip.refreshHistory')}
    </button>
  </header>

  {#if error}
    <p class="m-4 rounded-lg border border-red-500/30 p-3 text-sm th-color-danger" role="alert">{error}</p>
  {:else if loading && items.length === 0}
    <div class="space-y-3 p-5" role="status" aria-label={t('common.loading')}>
      {#each [1, 2, 3] as row (row)}<div class="h-12 animate-pulse rounded-lg th-bg-secondary"></div>{/each}
    </div>
  {:else if items.length === 0}
    <div class="px-5 py-12 text-center">
      <div class="mx-auto mb-3 grid h-11 w-11 place-items-center rounded-full th-bg-secondary th-text-secondary" aria-hidden="true">☎</div>
      <p class="text-sm font-medium th-text-primary">{t('settings.voip.emptyHistory')}</p>
      <p class="mt-1 text-xs th-text-secondary">{t('settings.voip.emptyHistoryHint')}</p>
    </div>
  {:else}
    <div class="divide-y th-border">
      {#each items as item (item.call_id)}
        <article class="grid gap-3 px-4 py-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:px-5">
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <span class={`inline-flex rounded-full px-2.5 py-1 text-xs font-medium ${item.outcome === 'completed' ? 'bg-emerald-500/10 text-emerald-600' : item.outcome === 'missed' || item.outcome === 'failed' ? 'bg-red-500/10 text-red-600' : 'th-bg-secondary th-text-secondary'}`}>
                {outcomeLabel(item.outcome)}
              </span>
              <span class="text-xs th-text-tertiary">{item.direction === 'inbound' ? t('settings.voip.inbound') : t('settings.voip.outbound')}</span>
              <span class="truncate text-sm font-medium th-text-primary" aria-label={`${item.from_user} → ${item.to_user}`}>{item.from_user} <span class="th-text-tertiary">→</span> {item.to_user}</span>
            </div>
            <div class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs th-text-secondary">
              <time datetime={item.ended_at}>{formatDate(item.ended_at)}</time>
              <span>{item.transport?.toUpperCase() || 'SIP'}</span>
              {#if item.audio_codec}<span>{item.audio_codec}{item.video_codec ? ` / ${item.video_codec}` : ''}</span>{/if}
              {#if item.failure_reason}<span class="th-color-danger">{item.failure_reason}</span>{/if}
            </div>
          </div>
          <div class="flex items-center gap-2 sm:justify-end">
            <span class="text-xs th-text-secondary">{formatDuration(item.duration_seconds)}</span>
            {#if item.stream_id}<a class="btn btn-ghost text-xs" href={`#/streams/${encodeURIComponent(item.stream_id)}`}>{t('settings.voip.viewStream')}</a>{/if}
          </div>
        </article>
      {/each}
    </div>
    <footer class="flex items-center justify-between border-t th-border px-4 py-3 text-xs th-text-secondary sm:px-5">
      <span>{t('settings.voip.historyCount', { start: offset + 1, end: Math.min(offset + items.length, total), total })}</span>
      <div class="flex gap-2">
        <button class="btn btn-ghost text-xs" type="button" onclick={() => load(Math.max(0, offset - pageSize))} disabled={loading || offset === 0}>{t('settings.voip.historyPrevious')}</button>
        <button class="btn btn-ghost text-xs" type="button" onclick={() => load(offset + pageSize)} disabled={loading || offset + items.length >= total}>{t('settings.voip.historyNext')}</button>
      </div>
    </footer>
  {/if}
</section>
