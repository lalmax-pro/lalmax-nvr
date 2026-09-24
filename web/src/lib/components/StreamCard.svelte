<script lang="ts">
  import { t } from '$lib/i18n';
  import type { StreamInfo } from '$lib/api';
  import { Eye, Pencil, Trash2, Users } from 'lucide-svelte';

  interface Props {
    stream: StreamInfo;
    managed?: boolean;
    onsaveName?: (stream: StreamInfo, name: string) => void;
    ondelete?: (stream: StreamInfo) => void;
    deleting?: boolean;
  }

  let { stream, managed = false, onsaveName, ondelete, deleting = false }: Props = $props();

  let editingName = $state(false);
  let nameInput = $state('');
  let nameInputEl: HTMLInputElement | undefined = $state();

  let title = $derived(stream.name || (managed ? (stream.camera_name || stream.stream_id) : stream.stream_id));

  $effect(() => {
    if (editingName && nameInputEl) {
      nameInputEl.focus();
      nameInputEl.select();
    }
  });

  function startEditName(event: MouseEvent) {
    event.preventDefault();
    event.stopPropagation();
    nameInput = title;
    editingName = true;
  }

  function saveName() {
    const trimmed = nameInput.trim();
    if (trimmed !== title) {
      onsaveName?.(stream, trimmed);
    }
    editingName = false;
  }

  function cancelEditName() {
    editingName = false;
    nameInput = title;
  }

  function stopCardNavigation(event: MouseEvent) {
    if (editingName) {
      event.preventDefault();
      event.stopPropagation();
    }
  }

  function handleDelete(event: MouseEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (deleting) return;
    ondelete?.(stream);
  }

  let sourceLabel = $derived.by(() => {
    switch (stream.source_type) {
      case 'camera':
        return t('streams.sourceCamera');
      case 'iptv':
        return t('streams.sourceIPTV');
      case 'rtmp_push':
        return t('streams.sourceRTMPPush');
      case 'srt_push':
        return t('streams.sourceSRTPush');
      case 'whip_push':
        return t('streams.sourceWHIPPush');
      case 'relay_pull':
        return t('streams.sourceRelayPull');
      case 'push':
        return t('streams.sourcePush');
      default:
        return t('streams.sourceStream');
    }
  });

  let codecLabel = $derived(
    stream.video_codec ? stream.video_codec.toUpperCase() : ''
  );

  let fpsLabel = $derived(
    stream.in_fps ? `${stream.in_fps.toFixed(1)} fps` : ''
  );

  let viewerCount = $derived(stream.subscribers?.length || 0);
</script>

<a
  href={`#/streams/${encodeURIComponent(stream.stream_id)}`}
  class="card stream-card border th-border p-4 transition-all hover:border-[var(--color-primary)]"
  onclick={stopCardNavigation}
>
  <div class="flex items-start justify-between gap-2 mb-3">
    <div class="min-w-0 flex-1">
      {#if editingName}
        <input
          bind:this={nameInputEl}
          type="text"
          class="input py-1 px-2 text-sm w-full"
          bind:value={nameInput}
          maxlength="128"
          aria-label={t('streams.displayName')}
          onclick={(e) => { e.preventDefault(); e.stopPropagation(); }}
          onkeydown={(e) => {
            if (e.key === 'Enter') {
              e.preventDefault();
              saveName();
            }
            if (e.key === 'Escape') {
              e.preventDefault();
              cancelEditName();
            }
          }}
          onblur={saveName}
        />
      {:else}
        <span class="font-medium th-text-primary truncate flex items-center gap-1.5">
          <span class="truncate">{title}</span>
          {#if onsaveName}
            <button
              type="button"
              class="btn btn-ghost p-0.5 shrink-0"
              title={t('streams.editName')}
              aria-label={t('streams.editName')}
              onclick={startEditName}
            >
              <Pencil size={12} class="th-text-tertiary" />
            </button>
          {/if}
        </span>
      {/if}
    </div>
    <div class="shrink-0">
      {#if stream.active}
        <span class="badge badge-success">{t('streams.active')}</span>
      {:else}
        <span class="badge badge-neutral">{t('streams.idle')}</span>
      {/if}
    </div>
  </div>

  <div class="space-y-1.5 mb-3 flex-1">
    <div class="flex items-center gap-2 flex-wrap">
      <span class="text-xs font-medium th-text-secondary px-2 py-0.5 rounded th-bg-tertiary">
        {sourceLabel}
      </span>
      {#if codecLabel}
        <span class="text-xs th-text-tertiary px-2 py-0.5 rounded th-bg-tertiary">{codecLabel}</span>
      {/if}
      {#if fpsLabel}
        <span class="text-xs th-text-tertiary px-2 py-0.5 rounded th-bg-tertiary">{fpsLabel}</span>
      {/if}
    </div>
    <p class="text-xs th-text-tertiary truncate font-mono" title={stream.stream_id}>
      {stream.stream_id}
    </p>
  </div>

  <div class="flex items-center justify-between pt-3 border-t th-border">
    <span class="inline-flex items-center gap-1.5 text-xs th-text-secondary">
      <Users size={14} class="th-text-tertiary" />
      {viewerCount}
    </span>
    <span class="inline-flex items-center gap-1">
      {#if ondelete}
        <button
          type="button"
          class="btn btn-ghost px-2 py-1 text-sm th-color-danger"
          title={t('streams.deleteStream')}
          aria-label={t('streams.deleteStream')}
          disabled={deleting}
          onclick={handleDelete}
        >
          <Trash2 size={14} />
        </button>
      {/if}
      <span class="btn btn-ghost px-2 py-1 text-sm pointer-events-none">
        <Eye size={14} />
        <span class="hidden sm:inline">{t('streams.viewDetails')}</span>
      </span>
    </span>
  </div>
</a>

<style>
  .stream-card {
    display: flex;
    flex-direction: column;
    text-decoration: none;
    color: inherit;
  }
</style>
