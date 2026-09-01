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
  }

  let { cameraId, start, end, seekToMs = $bindable(0) }: Props = $props();

  let videoEl: HTMLVideoElement | undefined = $state();
  let hls: Hls | null = null;
  let error = $state('');
  let lastWall = 0;

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
    hls = new HlsCtor({
      ...createHlsConfig('hls'),
      liveDurationInfinity: false,
      lowLatencyMode: false,
    });
    hls.loadSource(url);
    hls.attachMedia(videoEl);
    hls.on(HlsCtor.Events.MANIFEST_PARSED, () => {
      if (seekToMs > 0 && videoEl) {
        videoEl.currentTime = seekToMs / 1000;
      }
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
    if (videoEl && hls && ms >= 0) {
      videoEl.currentTime = ms / 1000;
    }
  });

  onDestroy(() => destroyHls());

  export function seekWallClock(offsetMs: number) {
    seekToMs = offsetMs;
    if (videoEl) videoEl.currentTime = offsetMs / 1000;
  }
</script>

<div class="card border th-border overflow-hidden">
  <div class="px-4 py-2 th-bg-secondary border-b th-border text-sm th-text-secondary">
    {t('recordings.continuousPlaying')}
  </div>
  {#if error}
    <div class="p-8 text-center th-color-danger">{error}</div>
  {:else}
    <video
      bind:this={videoEl}
      class="w-full max-h-[60vh] bg-black"
      controls
      ontimeupdate={() => { lastWall = (videoEl?.currentTime || 0) * 1000; }}
    >
      <track kind="captions" />
    </video>
  {/if}
</div>
