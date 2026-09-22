<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { Maximize, Minimize, AlertCircle, RefreshCw, Volume2, VolumeX } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import type { StreamState } from '$lib/hls-errors';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';

  let {
    cameraId,
    cameraName,
    expanded = false,
    tabVisible = true,
    streamUrl = '',
    onFallbackNeeded,
  }: {
    cameraId: string;
    cameraName: string;
    expanded?: boolean;
    tabVisible?: boolean;
    streamUrl?: string;
    onFallbackNeeded?: (fallback: 'hls') => void;
  } = $props();

  const coordinator = getContext<ReconnectCoordinator | undefined>('reconnect-coordinator');

  type PlayerState = StreamState | 'loading' | 'disconnected' | 'offline';

  let streamState: PlayerState = $state('loading');
  let videoEl: HTMLVideoElement | undefined = $state();
  let unsupportedMsg: string | null = $state(null);
  let destroyed = false;
  let isMuted = $state(true);

  let mediaSource: MediaSource | null = null;
  let mediaSourceUrl: string | null = null;
  let sourceBuffer: SourceBuffer | null = null;
  let fetchController: AbortController | null = null;
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectAttempt = 0;
  const MAX_RECONNECT_DELAY = 30000;
  let lastTabVisible = true;
  let streamGeneration = 0;

  // Queue for SourceBuffer updates when it's busy
  let pendingBuffers: ArrayBuffer[] = [];
  let sourceBufferUpdating = false;

  // Fullscreen
  let containerEl: HTMLDivElement | undefined = $state();
  let isFullscreen = $state(false);

  function toggleFullscreen() {
    if (!containerEl) return;
    if (!document.fullscreenElement) {
      containerEl.requestFullscreen();
      isFullscreen = true;
    } else {
      document.exitFullscreen();
      isFullscreen = false;
    }
  }

  function handleFullscreenChange() {
    isFullscreen = !!document.fullscreenElement;
  }

  function toggleMute() {
    isMuted = !isMuted;
    if (videoEl) videoEl.muted = isMuted;
  }

  function clearMediaPipeline() {
    streamGeneration++;

    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }

    if (fetchController) {
      fetchController.abort();
      fetchController = null;
    }

    pendingBuffers = [];
    sourceBufferUpdating = false;
    sourceBuffer = null;

    if (videoEl) {
      videoEl.src = '';
    }

    if (mediaSource) {
      try {
        if (mediaSource.readyState === 'open') {
          mediaSource.endOfStream();
        }
      } catch (_) {}
      mediaSource = null;
    }

    if (mediaSourceUrl) {
      URL.revokeObjectURL(mediaSourceUrl);
      mediaSourceUrl = null;
    }
  }

  // Check MSE support
  function checkSupport(): string | null {
    if (typeof MediaSource === 'undefined') {
      return t('live.wasmPlayer.unsupported') || 'MediaSource Extensions not supported in this browser';
    }
    // Check codec support
    const isSupported = MediaSource.isTypeSupported('video/mp4; codecs="avc1.64001F"') ||
                        MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"');
    if (!isSupported) {
      return 'H.264 codec not supported by MediaSource in this browser';
    }
    return null;
  }

  // Parse fMP4 stream: find init segment boundary and feed to SourceBuffer
  // The stream format: [ftyp+moov init segment][moof+mdat]...
  // We need to find the first moof box to split init from fragments
  function findBox(data: Uint8Array, boxType: string, offset: number = 0): number {
    let pos = offset;
    while (pos < data.length - 8) {
      const size = (data[pos] << 24) | (data[pos + 1] << 16) | (data[pos + 2] << 8) | data[pos + 3];
      if (size < 8) break;
      const type = String.fromCharCode(data[pos + 4], data[pos + 5], data[pos + 6], data[pos + 7]);
      if (type === boxType) return pos;
      pos += size;
    }
    return -1;
  }

  function readBoxSize(data: Uint8Array, offset: number): number {
    if (offset + 8 > data.length) return -1;
    const size = (data[offset] << 24) | (data[offset + 1] << 16) | (data[offset + 2] << 8) | data[offset + 3];
    return size >= 8 ? size : -1;
  }

  function findBoxSignature(data: Uint8Array, boxType: string): number {
    const a = boxType.charCodeAt(0);
    const b = boxType.charCodeAt(1);
    const c = boxType.charCodeAt(2);
    const d = boxType.charCodeAt(3);
    for (let i = 4; i < data.length - 3; i++) {
      if (data[i] === a && data[i + 1] === b && data[i + 2] === c && data[i + 3] === d) {
        const size = readBoxSize(data, i - 4);
        if (size > 0 && i - 4 + size <= data.length) return i - 4;
      }
    }
    return -1;
  }

  function toHexByte(value: number): string {
    return value.toString(16).padStart(2, '0').toUpperCase();
  }

  function inferMimeTypes(initSegment: Uint8Array): string[] {
    const codecs: string[] = [];
    const avcCPos = findBoxSignature(initSegment, 'avcC');
    if (avcCPos >= 0 && avcCPos + 12 <= initSegment.length) {
      codecs.push(`avc1.${toHexByte(initSegment[avcCPos + 9])}${toHexByte(initSegment[avcCPos + 10])}${toHexByte(initSegment[avcCPos + 11])}`);
    } else if (findBoxSignature(initSegment, 'hvcC') >= 0) {
      codecs.push('hev1.1.6.L93.B0');
    } else {
      codecs.push('avc1.64001F');
    }

    if (findBoxSignature(initSegment, 'mp4a') >= 0) {
      codecs.push('mp4a.40.2');
    }

    const inferred = `video/mp4; codecs="${codecs.join(', ')}"`;
    const fallbackTypes = [
      inferred,
      'video/mp4; codecs="avc1.64001F, mp4a.40.2"',
      'video/mp4; codecs="avc1.42E01E, mp4a.40.2"',
      'video/mp4; codecs="avc1.64001F"',
      'video/mp4; codecs="avc1.42E01E"',
    ];
    return [...new Set(fallbackTypes)];
  }

  function ensureSourceBuffer(initSegment: Uint8Array, generation: number): boolean {
    if (sourceBuffer) return true;
    if (!mediaSource || mediaSource.readyState !== 'open' || destroyed || generation !== streamGeneration) return false;

    for (const mimeType of inferMimeTypes(initSegment)) {
      if (!MediaSource.isTypeSupported(mimeType)) continue;
      try {
        sourceBuffer = mediaSource.addSourceBuffer(mimeType);
        sourceBuffer.mode = 'sequence';
        sourceBuffer.addEventListener('updateend', () => {
          if (destroyed || generation !== streamGeneration || !sourceBuffer) return;
          sourceBufferUpdating = false;
          if (pendingBuffers.length > 0 && !sourceBuffer.updating) {
            const next = pendingBuffers.shift()!;
            sourceBufferUpdating = true;
            try {
              sourceBuffer.appendBuffer(next);
            } catch (e) {
              console.error('[FMP4Player] queued appendBuffer error:', e);
              sourceBufferUpdating = false;
              if (!destroyed && generation === streamGeneration) {
                streamState = 'disconnected';
                scheduleReconnect();
              }
            }
          }
        });
        sourceBuffer.addEventListener('error', (e) => {
          console.error('[FMP4Player] SourceBuffer error:', e);
          sourceBufferUpdating = false;
          if (!destroyed && generation === streamGeneration) {
            streamState = 'disconnected';
            scheduleReconnect();
          }
        });
        return true;
      } catch (e) {
        console.warn('[FMP4Player] addSourceBuffer failed:', mimeType, e);
      }
    }

    unsupportedMsg = 'No supported fMP4 codec found for MediaSource';
    return false;
  }

  async function startStream(generation: number) {
    if (destroyed || generation !== streamGeneration) return;

    const url = streamUrl || `/api/cameras/${cameraId}/stream.m4s`;

    try {
      streamState = 'loading';
      fetchController = new AbortController();

      const headers: Record<string, string> = {};
      const authHeader = getAuthHeader();
      if (authHeader) {
        headers['Authorization'] = authHeader;
      }

      const response = await fetch(url, {
        method: 'GET',
        headers,
        signal: fetchController.signal,
      });
      if (destroyed || generation !== streamGeneration) return;

      if (!response.ok) {
        if (response.status === 404) {
          streamState = 'offline';
          return;
        }
        throw new Error(`HTTP ${response.status}`);
      }

      reconnectAttempt = 0;
      coordinator?.reportSuccess?.();

      const reader = response.body?.getReader();
      if (!reader) throw new Error('No response body');

      // Collect all data and find init segment boundary
      let collected = new Uint8Array(0);
      let initSegmentAppended = false;

      const appendToSourceBuffer = (buffer: ArrayBuffer) => {
        if (
          destroyed ||
          generation !== streamGeneration ||
          !sourceBuffer ||
          !mediaSource ||
          mediaSource.readyState !== 'open'
        ) return;

        if (sourceBufferUpdating || sourceBuffer.updating) {
          pendingBuffers.push(buffer);
        } else {
          sourceBufferUpdating = true;
          try {
            sourceBuffer.appendBuffer(buffer);
          } catch (e) {
            console.error('[FMP4Player] appendBuffer error:', e);
            sourceBufferUpdating = false;
            if (!destroyed && generation === streamGeneration) {
              streamState = 'disconnected';
              scheduleReconnect();
            }
          }
        }
      };

      const processChunk = (chunk: Uint8Array) => {
        if (destroyed || generation !== streamGeneration) return;
        const newData = new Uint8Array(collected.length + chunk.length);
        newData.set(collected);
        newData.set(chunk, collected.length);
        collected = newData;

        if (!initSegmentAppended) {
          // Look for 'moof' box to find where init segment ends
          const moofPos = findBox(collected, 'moof');
          if (moofPos > 0) {
            // Everything before moof is the init segment (ftyp + moov)
            const initSegment = collected.slice(0, moofPos);
            if (!ensureSourceBuffer(initSegment, generation)) return;
            appendToSourceBuffer(initSegment.buffer);
            initSegmentAppended = true;

            // Process remaining data as fragments
            const remaining = collected.slice(moofPos);
            collected = new Uint8Array(0);
            processFragments(remaining);
          }
        } else {
          const buffered = collected;
          collected = new Uint8Array(0);
          processFragments(buffered);
        }
      };

      const processFragments = (data: Uint8Array) => {
        if (destroyed || generation !== streamGeneration) return;
        // Each media segment is usually a moof box immediately followed by mdat.
        let pos = 0;
        while (pos < data.length) {
          const moofPos = findBox(data, 'moof', pos);
          if (moofPos < 0) {
            const remaining = data.slice(pos);
            const newCollected = new Uint8Array(remaining.length);
            newCollected.set(remaining);
            collected = newCollected;
            break;
          }

          const mdatPos = findBox(data, 'mdat', moofPos);
          if (mdatPos < 0) {
            const remaining = data.slice(moofPos);
            const newCollected = new Uint8Array(remaining.length);
            newCollected.set(remaining);
            collected = newCollected;
            break;
          }

          const moofSize = readBoxSize(data, moofPos);
          const mdatSize = readBoxSize(data, mdatPos);
          if (moofSize < 0 || mdatSize < 0 || moofPos + moofSize !== mdatPos) {
            console.error('[FMP4Player] invalid fragment layout', { moofPos, mdatPos, moofSize, mdatSize });
            streamState = 'disconnected';
            scheduleReconnect();
            return;
          }

          const segmentEnd = mdatPos + mdatSize;
          if (segmentEnd > data.length) {
            const remaining = data.slice(moofPos);
            const newCollected = new Uint8Array(remaining.length);
            newCollected.set(remaining);
            collected = newCollected;
            break;
          }

          const segment = data.slice(moofPos, segmentEnd);
          appendToSourceBuffer(segment.buffer);
          pos = segmentEnd;
        }
      };

      streamState = 'playing';

      // Read the stream
      while (!destroyed && generation === streamGeneration) {
        const { done, value } = await reader.read();
        if (done) break;
        processChunk(value);
      }
    } catch (err: unknown) {
      if (destroyed) return;
      if (err instanceof Error && err.name === 'AbortError') return;

      console.error('[FMP4Player] stream error:', err);
      streamState = 'disconnected';
      scheduleReconnect();
    }
  }

  function scheduleReconnect() {
    if (destroyed) return;
    if (reconnectTimer) return;
    reconnectAttempt++;
    const delay = Math.min(1000 * Math.pow(1.5, reconnectAttempt - 1), MAX_RECONNECT_DELAY);
    const jitter = delay * (0.5 + Math.random() * 0.5);

    coordinator?.reportReconnecting?.();
    reconnectTimer = setTimeout(() => {
      if (!destroyed) cleanupAndReconnect();
    }, jitter);
  }

  function cleanupAndReconnect() {
    clearMediaPipeline();
    if (!destroyed && videoEl && cameraId) {
      initMediaSource();
    }
  }

  function initMediaSource() {
    if (destroyed || !videoEl) return;

    clearMediaPipeline();
    const generation = streamGeneration;
    const nextMediaSource = new MediaSource();
    mediaSource = nextMediaSource;
    mediaSourceUrl = URL.createObjectURL(nextMediaSource);
    videoEl.src = mediaSourceUrl;

    nextMediaSource.addEventListener('sourceopen', () => {
      if (destroyed || generation !== streamGeneration || mediaSource !== nextMediaSource) return;
      try {
        startStream(generation);
      } catch (e) {
        console.error('[FMP4Player] MediaSource init error:', e);
        unsupportedMsg = 'Failed to initialize MediaSource';
      }
    });

    nextMediaSource.addEventListener('error', (e) => {
      console.error('[FMP4Player] MediaSource error:', e);
      streamState = 'disconnected';
      scheduleReconnect();
    });
  }

  // Tab visibility: pause/resume
  $effect(() => {
    const visible = tabVisible;
    const becameVisible = visible && !lastTabVisible;
    lastTabVisible = visible;
    if (becameVisible && videoEl && streamState === 'disconnected') {
      cleanupAndReconnect();
    }
  });

  // Init on mount
  $effect(() => {
    if (videoEl && cameraId) {
      const err = checkSupport();
      if (err) {
        unsupportedMsg = err;
        streamState = 'disconnected';
        return;
      }
      initMediaSource();
    }
  });

  onDestroy(() => {
    destroyed = true;
    clearMediaPipeline();
  });
</script>

<div
  bind:this={containerEl}
  class="relative w-full h-full bg-black group"
  onfullscreenchange={handleFullscreenChange}
>
  <!-- svelte-ignore a11y_media_has_caption -->
  <video
    bind:this={videoEl}
    autoplay
    muted={isMuted}
    playsinline
    class="w-full h-full object-contain"
  ></video>

  {#if unsupportedMsg}
    <div class="absolute inset-0 flex flex-col items-center justify-center bg-black/80 gap-3">
      <AlertCircle size={32} class="text-red-400/60" />
      <p class="text-white/60 text-sm text-center max-w-xs">{unsupportedMsg}</p>
      <button
        class="text-xs text-white/40 underline"
        onclick={() => onFallbackNeeded?.('hls')}
      >
        {t('live.switchToHls') || 'Switch to HLS'}
      </button>
    </div>
  {:else if streamState === 'loading'}
    <div class="absolute inset-0 flex flex-col items-center justify-center bg-black/60 gap-2">
      <div class="w-5 h-5 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
      <span class="text-white/50 text-xs">{t('live.connecting') || 'Connecting...'}</span>
    </div>
  {:else if streamState === 'offline'}
    <div class="absolute inset-0 flex flex-col items-center justify-center bg-black/80 gap-2">
      <AlertCircle size={28} class="text-yellow-400/60" />
      <span class="text-white/50 text-xs">{t('live.offline') || 'Camera offline'}</span>
    </div>
  {:else if streamState === 'disconnected'}
    <div class="absolute inset-0 flex flex-col items-center justify-center bg-black/80 gap-2">
      <RefreshCw size={24} class="text-white/40 animate-spin" style="animation-duration: 2s;" />
      <span class="text-white/50 text-xs">{t('live.reconnecting') || 'Reconnecting...'}</span>
    </div>
  {/if}

  <button
    class="absolute bottom-10 right-2 p-1.5 rounded bg-black/50 hover:bg-black/70 text-white/70 hover:text-white transition-colors opacity-0 group-hover:opacity-100"
    onclick={(e: MouseEvent) => { e.stopPropagation(); toggleMute(); }}
    title={isMuted ? t('live.unmute') || 'Unmute' : t('live.mute') || 'Mute'}
  >
    {#if isMuted}
      <VolumeX size={16} />
    {:else}
      <Volume2 size={16} />
    {/if}
  </button>

  <!-- Fullscreen toggle (show on hover) -->
  <div class="absolute top-2 right-2 opacity-0 group-hover:opacity-100 transition-opacity">
    <button
      class="p-1.5 rounded bg-black/50 hover:bg-black/70 text-white/70 hover:text-white transition-colors"
      onclick={toggleFullscreen}
      title={isFullscreen ? t('live.exitFullscreen') : t('live.fullscreen')}
    >
      {#if isFullscreen}
        <Minimize size={16} />
      {:else}
        <Maximize size={16} />
      {/if}
    </button>
  </div>
</div>
