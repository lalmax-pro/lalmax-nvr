<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { getHomeAssistantSettings, saveHomeAssistantSettings } from '$lib/api/home-assistant';
  import type { HomeAssistantSettings } from '$lib/api/home-assistant';
  import { listStreams } from '$lib/api/streams';
  import type { StreamInfo } from '$lib/api/streams';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';

  let settings = $state<HomeAssistantSettings | null>(null);
  let streams = $state<StreamInfo[]>([]);
  let loading = $state(true);
  let saving = $state(false);
  let error = $state('');
  let query = $state('');
  let original = $state('');
  const controller = new AbortController();
  let selected = $derived(new Set(settings?.streams.map(s => s.stream_id) ?? []));
  let rows = $derived.by(() => {
    const byID = new Map(streams.map(s => [s.stream_id, s]));
    for (const p of settings?.streams ?? []) {
      if (!byID.has(p.stream_id)) byID.set(p.stream_id, { stream_id: p.stream_id, name: p.name, active: false } as StreamInfo);
    }
    const search = query.toLowerCase();
    return [...byID.values()].filter(s => `${s.stream_id} ${s.name || s.camera_name || ''}`.toLowerCase().includes(search));
  });
  let dirty = $derived(settings !== null && JSON.stringify(settings) !== original);

  async function load() {
    loading = true;
    error = '';
    try {
      const cfg = await getHomeAssistantSettings(controller.signal);
      const all: StreamInfo[] = [];
      let offset = 0;
      while (true) {
        const page = await listStreams({ limit: 100, offset }, controller.signal);
        all.push(...page.streams);
        offset += page.streams.length;
        if (offset >= page.total || page.streams.length === 0) break;
      }
      if (controller.signal.aborted) return;
      settings = cfg;
      original = JSON.stringify(cfg);
      streams = all;
    } catch (e) {
      if (!controller.signal.aborted) error = e instanceof Error ? e.message : t('ha.loadFailed');
    } finally { loading = false; }
  }

  function select(ids: StreamInfo[], enabled: boolean) {
    if (!settings) return;
    const next = new Map(settings.streams.map(s => [s.stream_id, s]));
    for (const row of ids) {
      if (enabled) {
        if (!next.has(row.stream_id)) next.set(row.stream_id, { stream_id: row.stream_id, app_name: row.app_name || 'live' });
      } else next.delete(row.stream_id);
    }
    settings.streams = [...next.values()];
  }

  async function save() {
    if (!settings) return;
    saving = true; error = '';
    try {
      settings = await saveHomeAssistantSettings(settings, controller.signal);
      original = JSON.stringify(settings);
      showToast(t('ha.saved'), 'success');
    } catch (e) { error = e instanceof Error ? e.message : t('ha.saveFailed'); }
    finally { saving = false; }
  }

  onMount(() => { void load(); });
  onDestroy(() => controller.abort());
</script>

<section class="card p-6 border th-border space-y-5" aria-label="Home Assistant">
  <div>
    <h3 class="text-lg font-semibold th-text-primary">Home Assistant</h3>
    <p class="text-sm th-text-secondary mt-1">{t('ha.description')}</p>
  </div>
  {#if error}<p class="text-sm th-color-danger" role="alert">{error}</p>{/if}
  {#if loading}
    <p class="th-text-secondary" role="status">{t('common.loading')}</p>
  {:else if settings}
    <fieldset class="space-y-5" disabled={saving}>
      <label class="flex items-center gap-2 text-sm th-text-primary">
        <input type="checkbox" bind:checked={settings.enabled} /> {t('ha.enabled')}
      </label>
      <div>
        <label class="input-label" for="ha-rtsp">{t('ha.rtspBase')}</label>
        <input id="ha-rtsp" class="input w-full" bind:value={settings.rtsp_base_url} placeholder="rtsp://192.168.1.20:15544" />
        <p class="text-xs th-text-tertiary mt-1">{t('ha.rtspHint')}</p>
      </div>
      <div class="space-y-3">
        <div class="flex flex-wrap gap-2 items-center">
          <label class="input-label" for="ha-search">{t('ha.streams')}</label>
          <span class="text-sm th-text-secondary">{t('ha.selected', { count: settings.streams.length })}</span>
        </div>
        <input id="ha-search" class="input w-full" bind:value={query} placeholder={t('ha.search')} />
        <div class="flex gap-2 flex-wrap">
          <button type="button" class="btn btn-secondary btn-sm" onclick={() => select(rows, true)}>{t('ha.selectVisible')}</button>
          <button type="button" class="btn btn-secondary btn-sm" onclick={() => select(rows, false)}>{t('ha.clearVisible')}</button>
        </div>
        <div class="max-h-96 overflow-auto border th-border rounded-lg divide-y th-border">
          {#each rows as stream (stream.stream_id)}
            <label class="flex items-center gap-3 p-3 text-sm th-text-primary">
              <input type="checkbox" aria-label={stream.name || stream.camera_name || stream.stream_id} checked={selected.has(stream.stream_id)} onchange={(e) => select([stream], e.currentTarget.checked)} />
              <span class="min-w-0 flex-1"><span class="block truncate">{stream.name || stream.camera_name || stream.stream_id}</span><span class="text-xs th-text-tertiary break-all">{stream.stream_id}</span></span>
              <span class="badge {stream.active ? 'badge-success' : 'badge-neutral'}">{stream.active ? t('streams.active') : t('streams.idle')}</span>
              <span class="th-text-tertiary">{stream.video_codec || ''}</span>
            </label>
          {:else}<p class="p-4 text-sm th-text-secondary">{t('ha.empty')}</p>{/each}
        </div>
      </div>
      <button type="button" class="btn btn-primary" disabled={!dirty || saving} onclick={save}>{saving ? t('settings.saving') : t('settings.save')}</button>
    </fieldset>
    <div class="border-t th-border pt-4 space-y-2 text-sm th-text-secondary">
      <p>{t('ha.install')}</p>
      <p>{t('ha.credentials')}</p>
      <p>{t('ha.scope')}</p>
      <a class="text-blue-500 underline" href="https://github.com/lalmax-pro/lalmax-nvr/blob/main/docs/en/home-assistant.md" target="_blank" rel="noreferrer">{t('ha.guide')}</a>
      <p class="text-xs break-all">{t('ha.instance')}: {settings.instance_id}</p>
    </div>
  {:else}
    <button type="button" class="btn btn-secondary" onclick={load}>{t('common.retry')}</button>
  {/if}
</section>
