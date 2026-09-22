<script lang="ts">
  import { onMount } from 'svelte';
  import { AlertCircle, ClipboardList, Copy, Download, Pause, Play, RefreshCw, ScrollText } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { listServiceLogs, subscribeServiceLogs } from '$lib/api';
  import type { ServiceLogEntry } from '$lib/api';
  import { formatDate } from '$lib/format';
  import { showToast } from '$lib/toast';
  import OperationLogs from './OperationLogs.svelte';

  interface Props {
    initialTab?: string;
  }

  let { initialTab = 'operations' }: Props = $props();

  let tab = $derived(initialTab === 'service' ? 'service' : 'operations');

  function setTab(next: 'operations' | 'service') {
    window.location.hash = next === 'service' ? '#/logs/service' : '#/logs';
  }

  let logs = $state<ServiceLogEntry[]>([]);
  let loading = $state(false);
  let error = $state('');
  let levelFilter = $state('info');
  let query = $state('');
  let appliedQuery = $state('');
  let follow = $state(true);
  let paused = $state(false);
  let truncated = $state(false);
  let capacity = $state(0);
  let logPath = $state('');
  let logBox: HTMLDivElement | undefined = $state();
  let unsubscribe: (() => void) | undefined;

  const maxLines = 2000;

  function levelClass(level: string): string {
    switch (level.toUpperCase()) {
      case 'ERROR': return 'log-error';
      case 'WARN':
      case 'WARNING': return 'log-warn';
      case 'DEBUG': return 'log-debug';
      default: return 'log-info';
    }
  }

  function formatAttrs(attrs?: Record<string, unknown>): string {
    if (!attrs) return '';
    return Object.entries(attrs).map(([k, v]) => `${k}=${String(v)}`).join(' ');
  }

  function formatLine(entry: ServiceLogEntry): string {
    const attrs = formatAttrs(entry.attrs);
    return `${entry.time} ${entry.level} ${entry.msg}${attrs ? ' ' + attrs : ''}`;
  }

  function appendLog(entry: ServiceLogEntry) {
    if (paused) return;
    if (logs.length && logs[logs.length - 1].seq >= entry.seq) return;
    logs = [...logs, entry].slice(-maxLines);
    if (follow) {
      queueMicrotask(() => {
        if (logBox) logBox.scrollTop = logBox.scrollHeight;
      });
    }
  }

  async function loadLogs() {
    loading = true;
    error = '';
    try {
      const res = await listServiceLogs({
        level: levelFilter,
        q: appliedQuery || undefined,
        limit: 500,
      });
      logs = res.logs || [];
      truncated = res.truncated;
      capacity = res.capacity;
      logPath = res.path || '';
      if (follow) {
        queueMicrotask(() => {
          if (logBox) logBox.scrollTop = logBox.scrollHeight;
        });
      }
    } catch (e) {
      error = e instanceof Error ? e.message : t('logs.serviceLoadFailed');
    } finally {
      loading = false;
    }
  }

  function restartStream() {
    unsubscribe?.();
    unsubscribe = subscribeServiceLogs({
      level: levelFilter,
      q: appliedQuery || undefined,
    }, appendLog);
  }

  async function copyLogs() {
    const text = logs.map(formatLine).join('\n');
    try {
      await navigator.clipboard.writeText(text);
      showToast(t('logs.copied'), 'success');
    } catch {
      showToast(t('logs.copyFailed'), 'error');
    }
  }

  function downloadLogs() {
    const blob = new Blob([logs.map(formatLine).join('\n') + '\n'], { type: 'text/plain' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = `lalmax-nvr-service-logs.txt`;
    a.click();
    URL.revokeObjectURL(url);
  }

  $effect(() => {
    if (tab !== 'service') {
      unsubscribe?.();
      unsubscribe = undefined;
      return;
    }
    void levelFilter;
    void appliedQuery;
    loadLogs();
    restartStream();
    return () => {
      unsubscribe?.();
      unsubscribe = undefined;
    };
  });

  onMount(() => {
    return () => unsubscribe?.();
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-6">
      <h1 class="text-2xl font-semibold th-text-primary">{t('logs.title')}</h1>
      <p class="text-sm th-text-secondary mt-1">{t('logs.subtitle')}</p>
    </div>

    <div class="flex gap-1 p-1 th-bg-secondary rounded-lg mb-6 w-fit">
      <button
        class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium {tab === 'operations' ? 'bg-white dark:bg-gray-700 shadow-sm th-text-primary' : 'th-text-secondary'}"
        onclick={() => setTab('operations')}
      >
        <ClipboardList size={16} />
        {t('logs.tabOperations')}
      </button>
      <button
        class="flex items-center gap-2 px-4 py-2 rounded-md text-sm font-medium {tab === 'service' ? 'bg-white dark:bg-gray-700 shadow-sm th-text-primary' : 'th-text-secondary'}"
        onclick={() => setTab('service')}
      >
        <ScrollText size={16} />
        {t('logs.tabService')}
      </button>
    </div>

    {#if tab === 'operations'}
      <OperationLogs embedded />
    {:else}
      <div class="card p-5 mb-6 border th-border">
        <div class="grid grid-cols-1 sm:grid-cols-4 gap-3">
          <div>
            <label class="input-label" for="service-log-level">{t('logs.level')}</label>
            <select id="service-log-level" class="input" bind:value={levelFilter}>
              <option value="debug">DEBUG</option>
              <option value="info">INFO</option>
              <option value="warn">WARN</option>
              <option value="error">ERROR</option>
            </select>
          </div>
          <div class="sm:col-span-2">
            <label class="input-label" for="service-log-q">{t('logs.search')}</label>
            <input
              id="service-log-q"
              class="input"
              bind:value={query}
              placeholder={t('logs.searchPlaceholder')}
              onkeydown={(e) => { if (e.key === 'Enter') appliedQuery = query.trim(); }}
              onblur={() => { appliedQuery = query.trim(); }}
            />
          </div>
          <div class="flex items-end gap-2">
            <button class="btn btn-secondary btn-sm inline-flex items-center gap-2" onclick={() => { appliedQuery = query.trim(); loadLogs(); }} disabled={loading}>
              <RefreshCw size={16} class={loading ? 'animate-spin' : ''} />
              {t('common.refresh')}
            </button>
          </div>
        </div>
        {#if logPath || !loading}
          <p class="text-xs th-text-muted mt-3">
            {#if logPath}
              {t('logs.fileSource', { name: logPath.split(/[/\\]/).pop() || logPath })}
              <span class="ml-1">{logPath}</span>
            {:else}
              {t('logs.memorySource')}
            {/if}
          </p>
        {/if}
        <div class="flex flex-wrap items-center gap-3 mt-4 text-sm">
          <label class="inline-flex items-center gap-2">
            <input type="checkbox" bind:checked={follow} />
            {t('logs.follow')}
          </label>
          <button class="btn btn-ghost btn-sm inline-flex items-center gap-1" onclick={() => paused = !paused}>
            {#if paused}
              <Play size={14} /> {t('logs.resume')}
            {:else}
              <Pause size={14} /> {t('logs.pause')}
            {/if}
          </button>
          <button class="btn btn-ghost btn-sm inline-flex items-center gap-1" onclick={copyLogs}>
            <Copy size={14} /> {t('logs.copy')}
          </button>
          <button class="btn btn-ghost btn-sm inline-flex items-center gap-1" onclick={downloadLogs}>
            <Download size={14} /> {t('logs.download')}
          </button>
          {#if truncated}
            <span class="th-text-muted">
              {logPath ? t('logs.fileTruncated') : t('logs.truncated', { n: String(capacity) })}
            </span>
          {/if}
        </div>
      </div>

      {#if error}
        <div class="card border th-border-danger p-8 text-center mb-6">
          <div class="flex justify-center mb-4 th-color-danger"><AlertCircle size={40} /></div>
          <h3 class="text-lg font-medium th-text-primary mb-2">{t('logs.serviceLoadFailed')}</h3>
          <p class="th-text-secondary mb-4">{error}</p>
          <button onclick={loadLogs} class="btn btn-primary btn-sm">{t('common.retry')}</button>
        </div>
      {/if}

      <div class="card border th-border overflow-hidden">
        <div bind:this={logBox} class="log-terminal font-mono text-xs leading-5 overflow-auto max-h-[70vh] p-4">
          {#if logs.length === 0 && !loading}
            <p class="th-text-muted">{t('logs.serviceEmpty')}</p>
          {:else}
            {#each logs as entry (entry.seq)}
              <div class={levelClass(entry.level)}>
                <span class="th-text-muted">{formatDate(entry.time)}</span>
                <span class="log-level">{entry.level}</span>
                <span>{entry.msg}</span>
                {#if entry.attrs}
                  <span class="th-text-muted"> {formatAttrs(entry.attrs)}</span>
                {/if}
              </div>
            {/each}
          {/if}
        </div>
      </div>
    {/if}
  </main>
</div>

<style>
  .log-terminal {
    background: color-mix(in srgb, var(--color-bg-secondary, #111) 88%, black);
  }
  .log-level {
    display: inline-block;
    min-width: 4.5em;
    font-weight: 600;
  }
  .log-error { color: #f87171; }
  .log-warn { color: #fbbf24; }
  .log-info { color: inherit; }
  .log-debug { opacity: 0.7; }
</style>
