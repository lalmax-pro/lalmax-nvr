<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import {
    commitIPTVImport,
    createIPTVImport,
    deleteIPTVChannel,
    getStream,
    getIPTVChannelPlayback,
    getIPTVImport,
    listIPTVChannels,
    listIPTVGroups,
    listIPTVImportItems,
    listIPTVSources,
    updateIPTVChannel,
  } from '$lib/api';
import type { IPTVChannel, IPTVImportItem, IPTVImportJob, IPTVPlaybackDetails, IPTVSource } from '$lib/api';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import VideoPlayer from '../components/VideoPlayer.svelte';
  import {
    Check,
    CalendarClock,
    ExternalLink,
    RefreshCw,
    Star,
    Trash2,
    Tv,
    Upload,
  } from 'lucide-svelte';

  let loading = $state(true);
  let importing = $state(false);
  let committing = $state(false);
  let error = $state('');
  let playlistURL = $state('');
  let playlistText = $state('');
  let importName = $state('');
  let job = $state<IPTVImportJob | null>(null);
  let items = $state<IPTVImportItem[]>([]);
  let selected = $state<Set<string>>(new Set());
  let sources = $state<IPTVSource[]>([]);
  let channels = $state<IPTVChannel[]>([]);
  let groups = $state<string[]>([]);
  let groupFilter = $state('');
  let search = $state('');
  let active = $state<(IPTVChannel & { playback: IPTVPlaybackDetails }) | null>(null);
  let publishBusy = $state<Record<string, boolean>>({});
  let pollTimer: number | undefined;

  let selectableItems = $derived(items.filter((item) => item.playable || item.status === 'warning' || item.status === 'playable'));

  async function loadManaged() {
    const [src, ch, grp] = await Promise.all([listIPTVSources(), listIPTVChannels({ group: groupFilter || undefined, q: search || undefined }), listIPTVGroups()]);
    sources = src;
    channels = ch;
    groups = grp;
  }

  async function refreshImport() {
    if (!job) return;
    job = await getIPTVImport(job.id);
    const res = await listIPTVImportItems(job.id);
    items = res.items;
    const next = new Set(selected);
    for (const item of items) {
      if ((item.playable || item.status === 'playable' || item.status === 'warning') && !item.drm && item.status !== 'unsupported' && item.status !== 'failed') {
        if (selected.size === 0) next.add(item.id);
      }
    }
    if (selected.size === 0) selected = next;
  }

  async function submitImport() {
    importing = true;
    error = '';
    try {
      job = await createIPTVImport({
        name: importName.trim() || undefined,
        playlist_url: playlistText.trim() ? undefined : playlistURL.trim(),
        playlist_text: playlistText.trim() || undefined,
      });
      selected = new Set();
      await refreshImport();
      startPolling();
    } catch (e) {
      error = e instanceof Error ? e.message : t('iptv.importFailed');
    } finally {
      importing = false;
    }
  }

  function startPolling() {
    stopPolling();
    pollTimer = window.setInterval(() => {
      void refreshImport();
      if (job && (job.status === 'ready' || job.status === 'failed' || job.status === 'committed')) {
        stopPolling();
      }
    }, 1500);
  }

  function stopPolling() {
    if (pollTimer) {
      window.clearInterval(pollTimer);
      pollTimer = undefined;
    }
  }

  function toggleItem(id: string, item: IPTVImportItem) {
    if (item.drm || item.status === 'unsupported' || item.status === 'failed') return;
    const next = new Set(selected);
    if (next.has(id)) next.delete(id);
    else next.add(id);
    selected = next;
  }

  function selectPlayable() {
    selected = new Set(selectableItems.filter((item) => !item.drm && item.status !== 'unsupported' && item.status !== 'failed').map((item) => item.id));
  }

  async function commitSelected() {
    if (!job) return;
    committing = true;
    try {
      const res = await commitIPTVImport(job.id, [...selected]);
      showToast(t('iptv.commitSuccess', { count: String(res.channel_count) }), 'success');
      job = null;
      items = [];
      selected = new Set();
      await loadManaged();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('iptv.commitFailed'), 'error');
    } finally {
      committing = false;
    }
  }

  async function playChannel(ch: IPTVChannel) {
    try {
      const playback = await getIPTVChannelPlayback(ch.id);
      active = { ...ch, playback };
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('iptv.playbackFailed'), 'error');
    }
  }

  async function toggleFavorite(ch: IPTVChannel) {
    try {
      await updateIPTVChannel(ch.id, { favorite: !ch.favorite });
      await loadManaged();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('iptv.updateFailed'), 'error');
    }
  }

  async function removeChannel(ch: IPTVChannel) {
    try {
      await deleteIPTVChannel(ch.id);
      if (active?.id === ch.id) active = null;
      await loadManaged();
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('iptv.deleteFailed'), 'error');
    }
  }

  async function waitForPublishedStream(ch: IPTVChannel, waitForStart = false): Promise<boolean> {
    const attempts = waitForStart ? 12 : 1;
    for (let i = 0; i < attempts; i++) {
      try {
        const stream = await getStream(ch.stream_id);
        if (stream.active && stream.play_urls?.length) return true;
      } catch {
        // A newly enabled HLS pull may need a few seconds before lal registers it.
      }
      if (i + 1 < attempts) await new Promise((resolve) => window.setTimeout(resolve, 1000));
    }
    return false;
  }

  async function togglePublish(ch: IPTVChannel) {
    publishBusy = { ...publishBusy, [ch.id]: true };
    try {
      const enabled = !ch.publish_enabled;
      await updateIPTVChannel(ch.id, { publish_enabled: enabled });
      await loadManaged();
      if (enabled) {
        const ready = await waitForPublishedStream(ch, true);
        showToast(ready ? t('iptv.publishReady') : t('iptv.publishStarting'), ready ? 'success' : 'info');
      } else {
        showToast(t('iptv.publishDisabled'), 'success');
      }
    } catch (e) {
      showToast(e instanceof Error ? e.message : t('iptv.publishFailed'), 'error');
    } finally {
      publishBusy = { ...publishBusy, [ch.id]: false };
    }
  }

  function statusLabel(status: string): string {
    return t(`iptv.status.${status}`) || status;
  }

  onMount(async () => {
    try {
      await loadManaged();
    } catch (e) {
      error = e instanceof Error ? e.message : t('iptv.loadFailed');
    } finally {
      loading = false;
    }
  });

  onDestroy(() => {
    stopPolling();
  });
</script>

<div class="min-h-screen th-bg-primary">
  <main class="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="flex items-start justify-between gap-4 mb-6">
      <div>
        <h2 class="text-2xl font-bold th-text-primary">{t('iptv.title')}</h2>
        <p class="text-sm th-text-muted mt-1">{t('iptv.subtitle')}</p>
        <p class="text-xs th-text-tertiary mt-1">{t('iptv.playbackHint')}</p>
      </div>
      <button class="btn btn-secondary btn-sm" onclick={() => void loadManaged()}>
        <RefreshCw size={16} />
        <span>{t('common.refresh')}</span>
      </button>
    </div>

    {#if error}
      <div class="card border th-border p-4 mb-6 th-text-secondary">{error}</div>
    {/if}

    <section class="card border th-border p-4 mb-6">
      <h3 class="font-semibold th-text-primary mb-3">{t('iptv.importTitle')}</h3>
      <div class="grid gap-3 md:grid-cols-2">
        <label class="block">
          <span class="input-label">{t('iptv.importName')}</span>
          <input class="input mt-1 w-full" bind:value={importName} placeholder={t('iptv.importNameHint')} />
        </label>
        <label class="block">
          <span class="input-label">{t('iptv.playlistURL')}</span>
          <input class="input mt-1 w-full" bind:value={playlistURL} placeholder="https://example.com/live.m3u" />
        </label>
      </div>
      <label class="block mt-3">
        <span class="input-label">{t('iptv.playlistText')}</span>
        <textarea class="input mt-1 w-full h-24" bind:value={playlistText} placeholder={'#EXTM3U'}></textarea>
      </label>
      <div class="mt-3 flex justify-end">
        <button class="btn btn-primary btn-sm" onclick={() => void submitImport()} disabled={importing || (!playlistURL.trim() && !playlistText.trim())}>
          <Upload size={16} />
          <span>{importing ? t('iptv.importing') : t('iptv.import')}</span>
        </button>
      </div>
    </section>

    {#if job}
      <section class="card border th-border p-4 mb-6">
        <div class="flex items-center justify-between mb-3">
          <div>
            <h3 class="font-semibold th-text-primary">{t('iptv.reviewTitle')}</h3>
            <p class="text-xs th-text-tertiary mt-0.5">{job.name} · {statusLabel(job.status)} · {items.length}/{job.total_items}</p>
          </div>
          <div class="flex gap-2">
            <button class="btn btn-ghost btn-sm" onclick={selectPlayable}>{t('iptv.selectPlayable')}</button>
            <button class="btn btn-primary btn-sm" onclick={() => void commitSelected()} disabled={committing || selected.size === 0}>
              <Check size={16} />
              <span>{t('iptv.commit', { count: String(selected.size) })}</span>
            </button>
          </div>
        </div>
        <div class="overflow-x-auto">
          <table class="table w-full text-sm">
            <thead>
              <tr>
                <th></th>
                <th>{t('iptv.channel')}</th>
                <th>{t('iptv.group')}</th>
                <th>{t('iptv.statusLabel')}</th>
                <th>{t('iptv.codec')}</th>
              </tr>
            </thead>
            <tbody>
              {#each items as item (item.id)}
                <tr>
                  <td>
                    <input type="checkbox" checked={selected.has(item.id)} disabled={item.drm || item.status === 'unsupported' || item.status === 'failed'} onchange={() => toggleItem(item.id, item)} />
                  </td>
                  <td>{item.name}</td>
                  <td>{item.group_name || '-'}</td>
                  <td>{statusLabel(item.status)}</td>
                  <td>{[item.video_codec, item.audio_codec].filter(Boolean).join('/') || '-'}</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      </section>
    {/if}

    <section class="grid xl:grid-cols-5 gap-6">
      <div class="xl:col-span-3 min-w-0">
        <div class="flex flex-col sm:flex-row sm:items-center gap-3 mb-4">
          <select class="input" bind:value={groupFilter} onchange={() => void loadManaged()}>
            <option value="">{t('iptv.allGroups')}</option>
            {#each groups as group}
              <option value={group}>{group}</option>
            {/each}
          </select>
          <input class="input flex-1 min-w-0" bind:value={search} placeholder={t('iptv.search')} onchange={() => void loadManaged()} />
        </div>
        {#if loading}
          <p class="th-text-muted">{t('common.loading')}</p>
        {:else if channels.length === 0}
          <div class="card border th-border p-10 text-center th-text-muted">
            <Tv size={40} class="mx-auto mb-3" />
            <p>{t('iptv.empty')}</p>
          </div>
        {:else}
          <div class="grid md:grid-cols-2 gap-4">
            {#each channels as ch (ch.id)}
              <div class="card border th-border p-4 min-w-0 transition-colors" class:border-blue-500={active?.id === ch.id}>
                <div class="flex flex-col gap-2">
                  <button class="text-left min-w-0" onclick={() => void playChannel(ch)}>
                    <div class="font-medium th-text-primary truncate">{ch.name}</div>
                    <div class="text-xs th-text-tertiary">{ch.group_name || t('iptv.ungrouped')} · {statusLabel(ch.probe_status)}</div>
                  </button>
                  <div class="flex items-center justify-end gap-1 flex-wrap">
                    <button
                      class="btn btn-ghost btn-xs shrink-0 whitespace-nowrap"
                      title={ch.publish_enabled ? t('iptv.stopPublishing') : t('iptv.publish')}
                      aria-label={ch.publish_enabled ? t('iptv.stopPublishing') : t('iptv.publish')}
                      disabled={publishBusy[ch.id] || !ch.recordable}
                      onclick={() => void togglePublish(ch)}
                    >
                      {publishBusy[ch.id] ? t('iptv.publishWorking') : ch.publish_enabled ? t('iptv.stopPublishing') : t('iptv.publish')}
                    </button>
                    <button class="btn btn-ghost btn-xs" title={t('iptv.configureRecording')} onclick={() => (window.location.hash = `#/recording-plans?stream_id=${encodeURIComponent(ch.stream_id)}`)}><CalendarClock size={14} /></button>
                    <button class="btn btn-ghost btn-xs" onclick={() => void toggleFavorite(ch)}><Star size={14} /></button>
                    <button class="btn btn-ghost btn-xs" onclick={() => void removeChannel(ch)}><Trash2 size={14} /></button>
                  </div>
                </div>
                {#if ch.publish_enabled}
                  <div class="mt-3 border-t th-border pt-2">
                    <div class="flex items-center justify-between gap-2 text-xs th-text-tertiary">
                      <span>{t('iptv.publishedStream', { stream_id: ch.stream_id })}</span>
                      <a class="inline-flex items-center gap-1 text-blue-500 hover:underline whitespace-nowrap" href={`#/streams/${encodeURIComponent(ch.stream_id)}`}>
                        {t('iptv.viewStream')} <ExternalLink size={13} />
                      </a>
                    </div>
                  </div>
                {/if}
              </div>
            {/each}
          </div>
        {/if}
      </div>
      <aside class="xl:col-span-2 min-w-0 xl:sticky xl:top-6 xl:self-start">
        <div class="card border th-border overflow-hidden bg-black aspect-video">
          {#if active}
            <VideoPlayer
              cameraId={active.id}
              cameraName={active.name}
              streamUrl={active.playback.url}
              cameraProtocol="iptv"
            />
          {:else}
            <div class="h-full flex items-center justify-center text-white/50 text-sm">{t('iptv.pickChannel')}</div>
          {/if}
        </div>
        {#if active}
          <div class="mt-3 flex items-center justify-between gap-3">
            <div class="min-w-0">
              <p class="text-xs th-text-tertiary">{t('iptv.nowPlaying')}</p>
              <p class="font-medium th-text-primary truncate">{active.name}</p>
            </div>
            <span class="text-xs th-text-muted shrink-0">{active.group_name || t('iptv.ungrouped')}</span>
          </div>
        {/if}
        {#if sources.length}
          <p class="text-xs th-text-tertiary mt-3">{t('iptv.sourceCount', { count: String(sources.length) })}</p>
        {/if}
      </aside>
    </section>
  </main>
</div>
