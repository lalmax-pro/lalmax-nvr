<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { createStream, listStreams, subscribeNvrEvents, updateStream } from '$lib/api';
  import type { StreamInfo } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import StreamCard from '$lib/components/StreamCard.svelte';
  import Pagination from '../components/Pagination.svelte';
  import {
    AlertCircle,
    Camera,
    Copy,
    Plus,
    RefreshCw,
    SatelliteDish,
    Search,
    X,
  } from 'lucide-svelte';

  const PAGE_SIZE = 12;
  const streamIDPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

  let loading = $state(true);
  let error = $state('');
  let searchQuery = $state('');
  let searchApplied = $state('');
  let searchTimer: number | undefined;

  let managedStreams = $state<StreamInfo[]>([]);
  let externalStreams = $state<StreamInfo[]>([]);
  let managedTotal = $state(0);
  let externalTotal = $state(0);
  let managedPage = $state(1);
  let externalPage = $state(1);
  let refreshTimer: number | undefined;
  let stopEvents: (() => void) | undefined;
  let createOpen = $state(false);
  let createMode = $state<'push' | 'pull'>('push');
  let createStreamID = $state('');
  let createName = $state('');
  let createSourceURL = $state('');
  let creating = $state(false);
  let createError = $state('');
  let createdResult = $state<StreamInfo | null>(null);

  let managedTotalPages = $derived(Math.max(1, Math.ceil(managedTotal / PAGE_SIZE)));
  let externalTotalPages = $derived(Math.max(1, Math.ceil(externalTotal / PAGE_SIZE)));

  async function loadStreams(options?: { silent?: boolean }) {
    if (!options?.silent) {
      loading = managedStreams.length === 0 && externalStreams.length === 0;
    }
    error = '';
    try {
      const q = searchApplied || undefined;
      const [managedRes, externalRes] = await Promise.all([
        listStreams({
          q,
          managed: true,
          limit: PAGE_SIZE,
          offset: (managedPage - 1) * PAGE_SIZE,
        }),
        listStreams({
          q,
          managed: false,
          limit: PAGE_SIZE,
          offset: (externalPage - 1) * PAGE_SIZE,
        }),
      ]);
      managedTotal = managedRes.total;
      externalTotal = externalRes.total;

      const maxManagedPage = Math.max(1, Math.ceil(managedTotal / PAGE_SIZE));
      const maxExternalPage = Math.max(1, Math.ceil(externalTotal / PAGE_SIZE));
      let reload = false;
      if (managedPage > maxManagedPage) {
        managedPage = maxManagedPage;
        reload = true;
      }
      if (externalPage > maxExternalPage) {
        externalPage = maxExternalPage;
        reload = true;
      }
      if (reload) {
        return loadStreams(options);
      }

      managedStreams = managedRes.streams;
      externalStreams = externalRes.streams;
    } catch (e) {
      error = e instanceof Error ? e.message : t('streams.loadFailed');
    } finally {
      loading = false;
    }
  }

  function scheduleSearch(value: string) {
    searchQuery = value;
    if (searchTimer) {
      window.clearTimeout(searchTimer);
    }
    searchTimer = window.setTimeout(() => {
      searchApplied = searchQuery.trim();
      managedPage = 1;
      externalPage = 1;
      void loadStreams();
    }, 300);
  }

  function clearSearch() {
    searchQuery = '';
    searchApplied = '';
    managedPage = 1;
    externalPage = 1;
    if (searchTimer) {
      window.clearTimeout(searchTimer);
    }
    void loadStreams();
  }

  function handleManagedPageChange(page: number) {
    managedPage = page;
    void loadStreams({ silent: true });
  }

  function handleExternalPageChange(page: number) {
    externalPage = page;
    void loadStreams({ silent: true });
  }

  function emptyMessage(managed: boolean): string {
    if (searchApplied) return t('streams.noSearchResults');
    return managed ? t('streams.managedEmpty') : t('streams.externalEmpty');
  }

  function openCreateDialog() {
    createOpen = true;
    createMode = 'push';
    createStreamID = '';
    createName = '';
    createSourceURL = '';
    creating = false;
    createError = '';
    createdResult = null;
  }

  function closeCreateDialog() {
    createOpen = false;
  }

  async function submitCreate() {
    const streamID = createStreamID.trim();
    if (!streamIDPattern.test(streamID)) {
      createError = t('streams.streamIdHint');
      return;
    }
    const sourceURL = createSourceURL.trim();
    if (createMode === 'pull' && !sourceURL) {
      createError = t('streams.sourceURLHint');
      return;
    }
    creating = true;
    createError = '';
    try {
      const info = await createStream({
        stream_id: streamID,
        name: createName.trim() || undefined,
        input_mode: createMode,
        source_url: createMode === 'pull' ? sourceURL : undefined,
      });
      createdResult = info;
      if (createMode === 'push' && !(info.ingest_urls?.length)) {
        createError = t('streams.noIngestEnabled');
      }
      externalPage = 1;
      await loadStreams({ silent: true });
    } catch (e) {
      createError = e instanceof Error ? e.message : t('streams.createFailed');
    } finally {
      creating = false;
    }
  }

  async function handleSaveName(stream: StreamInfo, name: string) {
    try {
      await updateStream(stream.stream_id, { name });
      showToast(t('streams.nameUpdated'), 'success');
      await loadStreams({ silent: true });
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('streams.nameUpdateFailed'), 'error');
    }
  }

  async function copyPublishURL(url: string) {
    try {
      await navigator.clipboard.writeText(url);
      showToast(t('streams.copied'), 'success');
    } catch {
      showToast(t('streams.copyFailed'), 'error');
    }
  }

  onMount(() => {
    void loadStreams();
    stopEvents = subscribeNvrEvents({}, () => { void loadStreams({ silent: true }); }, { debounceMs: 500 });
    refreshTimer = window.setInterval(() => {
      void loadStreams({ silent: true });
    }, 15000);
  });

  onDestroy(() => {
    stopEvents?.();
    if (refreshTimer) {
      window.clearInterval(refreshTimer);
    }
    if (searchTimer) {
      window.clearTimeout(searchTimer);
    }
  });
</script>

{#snippet streamGrid(items: StreamInfo[], managed: boolean)}
  {#if !loading && items.length === 0}
    <div class="card border th-border p-12 text-center col-span-full">
      <div class="flex justify-center mb-4 th-text-muted">
        {#if managed}
          <Camera size={48} />
        {:else}
          <SatelliteDish size={48} />
        {/if}
      </div>
      <p class="text-sm th-text-muted">{emptyMessage(managed)}</p>
    </div>
  {:else}
    {#each items as stream (stream.stream_id)}
      <StreamCard {stream} {managed} onsaveName={handleSaveName} />
    {/each}
  {/if}
{/snippet}

<div class="min-h-screen th-bg-primary ">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex flex-col sm:flex-row items-start sm:items-center justify-between mb-6 gap-3">
      <div>
        <h2 class="text-2xl font-bold th-text-primary">{t('streams.title')}</h2>
        <p class="text-sm th-text-muted mt-1">{t('streams.subtitle')}</p>
      </div>
      <button class="btn btn-secondary btn-sm" onclick={() => void loadStreams()} disabled={loading}>
        <span class={loading ? 'spin' : ''}>
          <RefreshCw size={16} />
        </span>
        <span>{t('common.refresh')}</span>
      </button>
    </div>

    <div class="card border th-border p-4 mb-6">
      <label for="stream-search" class="input-label">{t('streams.searchLabel')}</label>
      <div class="relative mt-1">
        <Search size={16} class="absolute left-3 top-1/2 -translate-y-1/2 th-text-tertiary pointer-events-none" />
        <input
          id="stream-search"
          type="search"
          class="input pl-9 pr-9"
          placeholder={t('streams.searchPlaceholder')}
          value={searchQuery}
          oninput={(e) => scheduleSearch(e.currentTarget.value)}
        />
        {#if searchQuery}
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 btn btn-ghost btn-xs px-1"
            onclick={clearSearch}
            aria-label={t('streams.clearSearch')}
          >
            <X size={14} />
          </button>
        {/if}
      </div>
      {#if searchApplied}
        <p class="text-xs th-text-tertiary mt-2">
          {t('streams.searchSummary', { query: searchApplied, count: String(managedTotal + externalTotal) })}
        </p>
      {/if}
    </div>

    {#if error}
      <div class="card border th-border-danger p-8 text-center mb-6">
        <div class="flex justify-center mb-4 th-color-danger">
          <AlertCircle size={48} />
        </div>
        <h3 class="text-lg font-medium th-text-primary mb-2">{t('common.error')}</h3>
        <p class="th-text-secondary mb-4">{error}</p>
        <button onclick={() => void loadStreams()} class="btn btn-primary btn-sm">{t('common.retry')}</button>
      </div>
    {/if}

    {#if loading && managedStreams.length === 0 && externalStreams.length === 0}
      <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
        {#each Array(6) as _}
          <div class="card border th-border p-4 space-y-3 animate-pulse">
            <div class="flex items-center justify-between">
              <div class="h-4 w-28 th-bg-tertiary rounded"></div>
              <div class="h-5 w-16 th-bg-tertiary rounded-full"></div>
            </div>
            <div class="flex gap-2">
              <div class="h-5 w-16 th-bg-tertiary rounded"></div>
              <div class="h-5 w-12 th-bg-tertiary rounded"></div>
            </div>
            <div class="h-3 w-full th-bg-tertiary rounded"></div>
            <div class="border-t th-border pt-3 flex justify-between">
              <div class="h-4 w-10 th-bg-tertiary rounded"></div>
              <div class="h-6 w-16 th-bg-tertiary rounded"></div>
            </div>
          </div>
        {/each}
      </div>
    {:else}
      <section class="mb-8">
        <div class="flex items-center justify-between gap-3 mb-4">
          <div>
            <h3 class="text-lg font-semibold th-text-primary">{t('streams.managedTitle')}</h3>
            <p class="text-xs th-text-tertiary mt-0.5">{t('streams.managedHint')}</p>
          </div>
          <span class="badge badge-neutral shrink-0">{managedTotal}</span>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {@render streamGrid(managedStreams, true)}
        </div>

        {#if managedTotal > PAGE_SIZE}
          <div class="card border th-border mt-4 overflow-hidden">
            <div class="px-4 py-2 border-b th-border text-sm th-text-muted">
              {t('streams.pageSummary', {
                start: String((managedPage - 1) * PAGE_SIZE + 1),
                end: String(Math.min(managedPage * PAGE_SIZE, managedTotal)),
                total: String(managedTotal),
              })}
            </div>
            <Pagination
              currentPage={managedPage}
              totalPages={managedTotalPages}
              onPageChange={handleManagedPageChange}
            />
          </div>
        {/if}
      </section>

      <section>
        <div class="flex items-center justify-between gap-3 mb-4">
          <div>
            <h3 class="text-lg font-semibold th-text-primary">{t('streams.externalTitle')}</h3>
            <p class="text-xs th-text-tertiary mt-0.5">{t('streams.externalHint')}</p>
          </div>
          <div class="flex items-center gap-2 shrink-0">
            <button type="button" class="btn btn-primary btn-xs" onclick={openCreateDialog}>
              <Plus size={14} />
              <span>{t('streams.createStream')}</span>
            </button>
            <span class="badge badge-neutral">{externalTotal}</span>
          </div>
        </div>

        <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">
          {@render streamGrid(externalStreams, false)}
        </div>

        {#if externalTotal > PAGE_SIZE}
          <div class="card border th-border mt-4 overflow-hidden">
            <div class="px-4 py-2 border-b th-border text-sm th-text-muted">
              {t('streams.pageSummary', {
                start: String((externalPage - 1) * PAGE_SIZE + 1),
                end: String(Math.min(externalPage * PAGE_SIZE, externalTotal)),
                total: String(externalTotal),
              })}
            </div>
            <Pagination
              currentPage={externalPage}
              totalPages={externalTotalPages}
              onPageChange={handleExternalPageChange}
            />
          </div>
        {/if}
      </section>
    {/if}
  </main>
</div>

{#if createOpen}
  <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
    <button type="button" class="fixed inset-0 bg-black/60" aria-label={t('streams.done')} onclick={closeCreateDialog}></button>
    <div class="relative th-bg-secondary rounded-xl shadow-xl w-full max-w-lg max-h-[90vh] overflow-y-auto p-6" role="dialog" aria-modal="true" aria-labelledby="create-stream-title">
      <h3 id="create-stream-title" class="text-lg font-semibold th-text-primary">{t('streams.createStreamTitle')}</h3>
      <p class="text-sm th-text-muted mt-1">{t('streams.createStreamDesc')}</p>

      {#if createdResult}
        {#if createdResult.source_type === 'relay_pull'}
          <p class="text-sm th-text-secondary mt-4">{t('streams.pullReady')}</p>
          <div class="border th-border rounded-lg p-3 mt-3">
            <div class="flex items-center justify-between gap-2 mb-1">
              <span class="text-xs font-semibold th-text-primary">{t('streams.pullSource')}</span>
              <button type="button" class="btn btn-ghost btn-xs" onclick={() => copyPublishURL(createdResult?.source_url || '')}>
                <Copy size={14} />
                <span>{t('streams.copy')}</span>
              </button>
            </div>
            <code class="block text-xs break-all th-text-secondary">{createdResult.source_url}</code>
          </div>
        {:else}
          <p class="text-sm th-text-secondary mt-4">{t('streams.pushUrlsReady')}</p>
          {#if createdResult.ingest_urls?.length}
            <div class="mt-3 space-y-2">
              {#each createdResult.ingest_urls as ingest (ingest.protocol)}
                <div class="border th-border rounded-lg p-3">
                  <div class="flex items-center justify-between gap-2 mb-1">
                    <span class="text-xs font-semibold th-text-primary">{ingest.protocol.toUpperCase()}</span>
                    <button type="button" class="btn btn-ghost btn-xs" onclick={() => copyPublishURL(ingest.url)}>
                      <Copy size={14} />
                      <span>{t('streams.copy')}</span>
                    </button>
                  </div>
                  <code class="block text-xs break-all th-text-secondary">{ingest.url}</code>
                </div>
              {/each}
            </div>
          {/if}
        {/if}
        {#if createError}
          <p class="text-sm th-color-danger mt-3">{createError}</p>
        {/if}
        <div class="flex justify-end mt-5">
          <button type="button" class="btn btn-primary btn-sm" onclick={closeCreateDialog}>{t('streams.done')}</button>
        </div>
      {:else}
        <form class="mt-4 space-y-4" onsubmit={(e) => { e.preventDefault(); void submitCreate(); }}>
          <div class="flex gap-2" role="group" aria-label={t('streams.createStreamTitle')}>
            <button type="button" class="btn btn-sm {createMode === 'push' ? 'btn-primary' : 'btn-secondary'}" onclick={() => createMode = 'push'} disabled={creating}>
              {t('streams.inputPush')}
            </button>
            <button type="button" class="btn btn-sm {createMode === 'pull' ? 'btn-primary' : 'btn-secondary'}" onclick={() => createMode = 'pull'} disabled={creating}>
              {t('streams.inputPull')}
            </button>
          </div>
          <div>
            <label for="create-stream-id" class="input-label">{t('streams.streamId')}</label>
            <input
              id="create-stream-id"
              class="input mt-1"
              autocomplete="off"
              placeholder={t('streams.streamIdPlaceholder')}
              bind:value={createStreamID}
              disabled={creating}
            />
            <p class="text-xs th-text-tertiary mt-1">{t('streams.streamIdHint')}</p>
          </div>
          <div>
            <label for="create-stream-name" class="input-label">{t('streams.displayName')}</label>
            <input
              id="create-stream-name"
              class="input mt-1"
              autocomplete="off"
              placeholder={t('streams.displayNamePlaceholder')}
              bind:value={createName}
              disabled={creating}
            />
          </div>
          {#if createMode === 'pull'}
            <div>
              <label for="create-source-url" class="input-label">{t('streams.sourceURL')}</label>
              <input
                id="create-source-url"
                class="input mt-1"
                autocomplete="off"
                placeholder={t('streams.sourceURLPlaceholder')}
                bind:value={createSourceURL}
                disabled={creating}
              />
              <p class="text-xs th-text-tertiary mt-1">{t('streams.sourceURLHint')}</p>
            </div>
          {/if}
          {#if createError}
            <p class="text-sm th-color-danger">{createError}</p>
          {/if}
          <div class="flex justify-end gap-2">
            <button type="button" class="btn btn-secondary btn-sm" onclick={closeCreateDialog} disabled={creating}>{t('common.cancel')}</button>
            <button type="submit" class="btn btn-primary btn-sm" disabled={creating || !createStreamID.trim() || (createMode === 'pull' && !createSourceURL.trim())}>
              {creating ? t('streams.creating') : t('streams.create')}
            </button>
          </div>
        </form>
      {/if}
    </div>
  </div>
{/if}

<style>
  .spin {
    animation: spin 1s linear infinite;
  }

  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
</style>
