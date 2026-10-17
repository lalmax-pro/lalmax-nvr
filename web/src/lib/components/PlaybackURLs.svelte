<script lang="ts">
  import { showToast } from '$lib/toast';

  let { rtspUrl = '', webrtcUrl = '', idPrefix = '' }: { rtspUrl?: string; webrtcUrl?: string; idPrefix?: string } = $props();
  let rtspInput: HTMLInputElement;
  let webrtcInput: HTMLInputElement;

  async function copyAddress(url: string, input: HTMLInputElement | undefined) {
    if (!url) return;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(url);
      } else {
        // The NVR may be accessed over plain HTTP, where Clipboard API is unavailable.
        input?.focus();
        input?.select();
        if (!document.execCommand('copy')) throw new Error('copy not supported');
      }
      showToast('地址已复制', 'success');
    } catch {
      input?.focus();
      input?.select();
      showToast('已选中地址，请按 Ctrl+C 复制', 'info');
    }
  }
</script>

<div class="flex flex-col gap-3" role="group" aria-label="播放地址">
  <div>
    <label for={`playback-rtsp-url-${idPrefix}`} class="mb-1 block text-xs font-medium">RTSP 地址</label>
    {#if rtspUrl}
      <div class="flex min-w-0 items-center gap-2">
        <input id={`playback-rtsp-url-${idPrefix}`} bind:this={rtspInput} type="text" readonly value={rtspUrl} title={rtspUrl}
          aria-label="RTSP 播放地址" onclick={(event) => event.currentTarget.select()} onfocus={(event) => event.currentTarget.select()}
          class="min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg-secondary)] px-2 py-2 font-mono text-xs text-[var(--text-primary)]" />
        <button type="button" class="btn btn-sm btn-secondary shrink-0" onclick={() => copyAddress(rtspUrl, rtspInput)}>复制</button>
      </div>
    {:else}
      <div class="text-xs text-[var(--text-secondary)]">当前无可用 RTSP 地址</div>
    {/if}
  </div>
  <div>
    <label for={`playback-webrtc-url-${idPrefix}`} class="mb-1 block text-xs font-medium">WebRTC（WHEP）地址</label>
    {#if webrtcUrl}
      <div class="flex min-w-0 items-center gap-2">
        <input id={`playback-webrtc-url-${idPrefix}`} bind:this={webrtcInput} type="text" readonly value={webrtcUrl} title={webrtcUrl}
          aria-label="WebRTC WHEP 播放地址" onclick={(event) => event.currentTarget.select()} onfocus={(event) => event.currentTarget.select()}
          class="min-w-0 flex-1 rounded border border-[var(--border)] bg-[var(--bg-secondary)] px-2 py-2 font-mono text-xs text-[var(--text-primary)]" />
        <button type="button" class="btn btn-sm btn-secondary shrink-0" onclick={() => copyAddress(webrtcUrl, webrtcInput)}>复制</button>
      </div>
    {:else}
      <div class="text-xs text-[var(--text-secondary)]">当前无可用 WebRTC 地址</div>
    {/if}
  </div>
</div>
