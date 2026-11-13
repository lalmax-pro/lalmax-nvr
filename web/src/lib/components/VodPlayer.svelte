<script lang="ts">
  import { onDestroy } from 'svelte';
  import { getVodPlaylistUrl } from '$lib/api';
  import { createHlsConfig } from '$lib/hls-config';
  import { t } from '$lib/i18n';
  import type Hls from 'hls.js';

  interface Props {
    cameraId: string;
    start: string;
    end: string;
    seekToMs?: number;
    embedded?: boolean;
    playbackRate?: number;
    onTime?: (mediaMs: number) => void;
  }

  let { cameraId, start, end, seekToMs = $bindable(0), embedded = false, playbackRate = 1, onTime }: Props = $props();

  let videoEl: HTMLVideoElement | undefined = $state();
  let hls: Hls | null = null;
  let error = $state('');
  let lastWall = 0;
  let pendingSeekMs: number | null = null;
  let sourceReady = false;
  function applySeek() {
    if (!videoEl || !sourceReady || pendingSeekMs === null || videoEl.readyState < 1) return;
    const seconds = Math.max(0, pendingSeekMs / 1000);
    try {
      videoEl.currentTime = Number.isFinite(videoEl.duration) ? Math.min(seconds, videoEl.duration) : seconds;
      pendingSeekMs = null;
    } catch (err) { console.warn('VOD seek postponed until media becomes seekable', err); }
  }

  async function load() {
    error = '';
    const url = getVodPlaylistUrl(cameraId, start, end);
    const HlsMod = await import('hls.js');
    const HlsCtor = HlsMod.default;
    if (!HlsCtor.isSupported() || !videoEl) {
      error = t('recordings.continuousUnsupported');
      return;
    }
    destroyHls();
    sourceReady = false;
    pendingSeekMs = Number.isFinite(seekToMs) ? Math.max(0, seekToMs) : 0;
    hls = new HlsCtor({
      ...createHlsConfig('hls'),
      liveDurationInfinity: false,
      lowLatencyMode: false,
    });
    hls.loadSource(url);
    hls.attachMedia(videoEl);
    hls.on(HlsCtor.Events.MANIFEST_PARSED, () => {
      sourceReady = true;
      applySeek();
      void videoEl?.play().catch(() => {});
    });
    hls.on(HlsCtor.Events.ERROR, (_ev, data) => {
      if (!data.fatal) return;
      if (data.details === HlsCtor.ErrorDetails.FRAG_LOAD_ERROR || data.details === HlsCtor.ErrorDetails.FRAG_LOAD_TIMEOUT) {
        const restore = lastWall;
        destroyHls();
        void load().then(() => {
          seekToMs = restore;
        });
        return;
      }
      error = t('recordings.continuousFailed');
    });
  }

  function destroyHls() {
    if (hls) {
      hls.destroy();
      hls = null;
    }
  }

  $effect(() => {
    if (!videoEl) return;
    const _id = cameraId;
    const _start = start;
    const _end = end;
    void load();
  });

  $effect(() => {
    const ms = seekToMs;
    if (Number.isFinite(ms) && ms >= 0) {
      pendingSeekMs = ms;
      applySeek();
    }
  });

  $effect(() => {
    if (videoEl) videoEl.playbackRate = playbackRate;
  });

  onDestroy(() => destroyHls());

  export function seekWallClock(offsetMs: number) {
    seekToMs = offsetMs;
    pendingSeekMs = offsetMs;
    applySeek();
  }
</script>

<div class="overflow-hidden {embedded ? 'player-fill' : 'card border th-border'}">
  <div class="px-4 py-2 th-bg-secondary {embedded ? '' : 'border-b th-border'} text-sm th-text-secondary">
    {t('recordings.continuousPlaying')}
  </div>
  {#if error}
    <div class="p-8 text-center th-color-danger">{error}</div>
  {:else}
    <div class={embedded ? 'player-stage bg-black' : ''}>
      <video
        bind:this={videoEl}
        class={embedded ? 'w-full h-full object-contain bg-black' : 'w-full max-h-[60vh] bg-black'}
        controls
        onloadedmetadata={applySeek}
        oncanplay={applySeek}
        ontimeupdate={() => {
          lastWall = (videoEl?.currentTime || 0) * 1000;
          onTime?.(lastWall);
        }}
      >
        <track kind="captions" />
      </video>
    </div>
  {/if}
</div>

<style>
  .player-fill {
    display: flex;
    flex-direction: column;
    height: 100%;
    min-height: 0;
    background: var(--bg-elevated);
  }
  .player-stage {
    flex: 1;
    min-height: 0;
    display: flex;
    align-items: center;
    justify-content: center;
  }
</style>
