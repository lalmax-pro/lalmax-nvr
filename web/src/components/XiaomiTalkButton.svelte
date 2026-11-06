<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Mic, MicOff, Loader2 } from 'lucide-svelte';
  import { t } from '$lib/i18n';
  import { showToast } from '$lib/toast';
  import { getAuthToken } from '$lib/api/client';
  import { createTalkAudioEncoder, type TalkAudioEncoder } from '$lib/xiaomiTalkAudio';

  let { cameraId }: { cameraId: string } = $props();
  let isTalking = $state(false);
  let isConnecting = $state(false);
  let ws: WebSocket | null = null;
  let audioContext: AudioContext | null = null;
  let mediaStream: MediaStream | null = null;
  let processor: ScriptProcessorNode | null = null;
  let encoder: TalkAudioEncoder | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let generation = 0;

  function fail(error?: Error) {
    showToast(t(error?.message === 'unsupported_codec' ? 'live.talk.unsupportedCodec' : 'live.talk.error'), 'error');
    stopTalk();
  }

  async function startTalk() {
    if (isTalking || isConnecting) return;
    isConnecting = true;
    const session = ++generation;
    try {
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: { channelCount: 1, echoCancellation: true, noiseSuppression: true, autoGainControl: true },
        video: false,
      });
      if (session !== generation) { stream.getTracks().forEach(track => track.stop()); return; }
      mediaStream = stream;
      const params = new URLSearchParams({ camera_id: cameraId });
      const token = getAuthToken();
      if (token) params.set('token', token);
      const socket = new WebSocket(`${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}/api/xiaomi/talk/ws?${params}`);
      ws = socket;
      socket.binaryType = 'arraybuffer';
      let initializing = false;
      // The server must tell us the speaker codec before microphone capture starts.
      socket.onmessage = async (event) => {
        if (session !== generation || initializing || typeof event.data !== 'string') return;
        try {
          const status = JSON.parse(event.data);
          if (status.status !== 'connected') return;
          initializing = true;
          const audio = await createTalkAudioEncoder(status.speaker_codec, (packet) => {
            if (session !== generation || socket.readyState !== WebSocket.OPEN) return;
            if (socket.bufferedAmount > 64000) { fail(); return; }
            socket.send(packet);
          }, (error) => { if (session === generation) fail(error); });
          if (session !== generation) { audio.close(); return; }
          encoder = audio;
          audioContext = new AudioContext({ sampleRate: audio.sampleRate });
          if (audioContext.sampleRate !== audio.sampleRate) throw new Error('unsupported_codec');
          await audioContext.resume();
          if (session !== generation) return;
          const source = audioContext.createMediaStreamSource(stream);
          processor = audioContext.createScriptProcessor(1024, 1, 1);
          processor.onaudioprocess = (event) => {
            try { audio.push(event.inputBuffer.getChannelData(0)); }
            catch (error) { if (session === generation) fail(error as Error); }
          };
          source.connect(processor);
          processor.connect(audioContext.destination);
          if (timer) { clearTimeout(timer); timer = null; }
          isTalking = true;
          isConnecting = false;
          showToast(t('live.talk.started'), 'success');
        } catch (error) { if (session === generation) fail(error as Error); }
      };
      socket.onerror = () => { if (session === generation) fail(); };
      socket.onclose = () => {
        if (session !== generation) return;
        showToast(t(isTalking ? 'live.talk.stopped' : 'live.talk.error'), isTalking ? 'info' : 'error');
        stopTalk();
      };
      timer = setTimeout(() => {
        if (session === generation && isConnecting) {
          showToast(t('live.talk.timeout'), 'error');
          stopTalk();
        }
      }, 45000);
    } catch (error) {
      if (session !== generation) return;
      const name = error instanceof DOMException ? error.name : '';
      showToast(t(name === 'NotAllowedError' ? 'live.talk.permissionDenied' : name === 'NotFoundError' ? 'live.talk.noMicrophone' : 'live.talk.error'), 'error');
      stopTalk();
    }
  }

  function stopTalk() {
    generation++;
    isTalking = false;
    isConnecting = false;
    if (timer) { clearTimeout(timer); timer = null; }
    if (processor) { processor.onaudioprocess = null; processor.disconnect(); processor = null; }
    if (encoder) { encoder.close(); encoder = null; }
    if (audioContext) { void audioContext.close(); audioContext = null; }
    if (mediaStream) { mediaStream.getTracks().forEach(track => track.stop()); mediaStream = null; }
    if (ws) { ws.onmessage = null; ws.onclose = null; ws.onerror = null; ws.close(); ws = null; }
  }

  function toggleTalk() {
    if (isTalking) { stopTalk(); showToast(t('live.talk.stopped'), 'info'); }
    else { void startTalk(); }
  }
  onDestroy(stopTalk);
</script>

<button
  onclick={toggleTalk}
  disabled={isConnecting}
  class="talk-btn {isTalking ? 'talking' : ''} {isConnecting ? 'connecting' : ''}"
  title={isTalking ? (t('live.talk.stop') || '停止对讲') : (t('live.talk.start') || '开始对讲')}
>
  {#if isConnecting}
    <Loader2 size={20} class="animate-spin" />
  {:else if isTalking}
    <MicOff size={20} />
  {:else}
    <Mic size={20} />
  {/if}
</button>

<style>
  .talk-btn {
    width: 40px;
    height: 40px;
    border-radius: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--color-primary);
    color: white;
    border: none;
    cursor: pointer;
    transition: all 0.2s ease;
  }
  .talk-btn:hover:not(:disabled) { transform: scale(1.05); }
  .talk-btn.talking {
    background: var(--color-danger, #ef4444);
    animation: pulse 1.5s infinite;
  }
  .talk-btn.connecting { background: var(--color-warning, #f59e0b); cursor: wait; }
  .talk-btn:disabled { opacity: 0.7; cursor: not-allowed; }
  @keyframes pulse {
    0%, 100% { box-shadow: 0 2px 8px rgba(239, 68, 68, 0.3); }
    50% { box-shadow: 0 4px 16px rgba(239, 68, 68, 0.5); }
  }
</style>
