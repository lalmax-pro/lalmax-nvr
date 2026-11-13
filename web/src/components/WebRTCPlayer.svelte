<script lang="ts">
  import { onDestroy, getContext } from 'svelte';
  import { t } from '$lib/i18n';
  import { AlertCircle, RefreshCw, ImageIcon, Volume2, VolumeX } from 'lucide-svelte';
  import { getAuthHeader } from '$lib/api';
  import { getSnapshotUrl } from '$lib/api/cameras';
  import { captureFrame } from '$lib/freeze-frame';
  import { sendTelemetry } from '$lib/telemetry';
  import { whepPlaybackReady, whepFirstVideoFrameTimedOut, sdpHasActiveVideoMedia } from '$lib/whep-readiness';
  import type { StreamState } from '$lib/hls-errors';
  import type { ReconnectCoordinator } from '$lib/reconnect-coordinator.svelte';

  let {
    cameraId,
    cameraName,
    expanded = false,
    tabVisible = true,
    whepUrl = '',
    expectVideo,
  }: {
    cameraId: string;
    cameraName: string;
    expanded?: boolean;
    tabVisible?: boolean;
    whepUrl?: string;
    expectVideo?: boolean; // false for legitimate audio-only VoIP/standalone streams
  } = $props();

  // Reconnection coordinator from Dashboard context
  const coordinator = getContext<ReconnectCoordinator | undefined>('reconnect-coordinator');
  let coordinatedTimer: ReturnType<typeof setTimeout> | null = null;
  let hasActiveCoordinatedReconnect = false;
  // A queued reconnect has no slot yet; an in-flight failed attempt must
  // release its granted slot before requesting the next retry.
  let reconnectSlotGranted = false;
  let coordinatedAttemptRunning = false;
  function finishCoordinatedReconnect() {
    if (!coordinator || !hasActiveCoordinatedReconnect) return;
    if (reconnectSlotGranted) coordinator.completeReconnect(cameraId);
    else coordinator.cancelRequest(cameraId);
    hasActiveCoordinatedReconnect = false;
    reconnectSlotGranted = false;
    coordinatedAttemptRunning = false;
  }

  function coordinatedReconnect(reconnectFn: () => void) {
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    if (!coordinator) {
      reconnectFn();
      return;
    }
    finishCoordinatedReconnect();
    hasActiveCoordinatedReconnect = true;
    const execute = () => {
      coordinatedTimer = null;
      if (destroyed) { finishCoordinatedReconnect(); return; }
      coordinatedAttemptRunning = true;
      reconnectFn();
    };
    const delay = coordinator.requestReconnect(cameraId, (grantedDelay) => {
      reconnectSlotGranted = true;
      coordinatedTimer = setTimeout(execute, grantedDelay);
    });
    if (delay >= 0) {
      reconnectSlotGranted = true;
      coordinatedTimer = setTimeout(execute, delay);
    }
  }
  type WebrtcState = 'connecting' | 'connected' | 'disconnected' | 'failed';

  let streamState: StreamState | 'loading' = $state('loading');
  let webrtcState: WebrtcState = $state('connecting');
  let hasFirstVideoFrame = $state(false);
  let negotiatedHasVideo: boolean | null = $state(null);
  let connectedAt = 0;
  // Explicit caller knowledge wins; otherwise follow the actual WHEP SDP answer.
  // Before an answer exists, retain video black-screen detection conservatively.
  function shouldExpectVideo(): boolean {
    return expectVideo ?? negotiatedHasVideo ?? true;
  }
  let videoFrameCallbackId: number | null = null;
  let videoEl: HTMLVideoElement | undefined = $state();
  let pc: RTCPeerConnection | null = null;
  let sessionUrl: string | null = null;
  let connectionEpoch = 0;
  let activeWhepRequest: AbortController | null = null;

  function releaseWhep(url: string) {
    const auth = getAuthHeader();
    void fetch(url, { method: 'DELETE', headers: auth ? { Authorization: auth } : {} }).catch(() => {});
  }

  function whepSessionLocation(response: Response, camera: string): string | null {
    const location = response.headers.get('Location');
    if (!location) return null;
    // Relative Location values are resolved against the request URL, not the dashboard URL.
    return new URL(location, response.url || new URL(`/api/cameras/${camera}/stream/webrtc`, window.location.href).href).href;
  }
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  let reconnectAttempts = 0;
  const maxReconnectAttempts = 5;
  const reconnectDelays = [2000, 4000, 8000, 16000, 32000];

  // Zombie detection
  let zombieInterval: ReturnType<typeof setInterval> | null = null;
  let lastPlaybackTime = 0;
  let zombieCount = 0;
let destroyed = false;

  // Audio mute state — starts muted for autoplay policy
  let isMuted = $state(true);
  function toggleMute() {
    isMuted = !isMuted;
    if (videoEl) videoEl.muted = isMuted;
  }

  // Freeze frame — prevents black flash during reconnection
  let frozenFrameUrl: string | null = $state(null);
  let showFrozenFrame = $state(false);
  let freezeClearTimer: ReturnType<typeof setTimeout> | null = null;

  // Snapshot fallback — degrades to static snapshots after max reconnect failures
  let snapshotMode = $state(false);
  let snapshotSrc = $state('');
  let snapshotRefreshTimer: ReturnType<typeof setInterval> | null = null;
  let snapshotRestoreTimer: ReturnType<typeof setTimeout> | null = null;
  const SNAPSHOT_REFRESH_MS = 2000;
  const SNAPSHOT_RESTORE_MS = 5 * 60 * 1000;
  let lastTabVisible = true;
  let disconnectGraceTimer: ReturnType<typeof setTimeout> | null = null;

  function captureFreezeFrame() {
    if (frozenFrameUrl) return;
    const frame = captureFrame(videoEl ?? null);
    if (frame) {
      frozenFrameUrl = frame;
      showFrozenFrame = true;
    }
  }

  function clearFreezeFrame() {
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    showFrozenFrame = false;
    freezeClearTimer = setTimeout(() => {
      frozenFrameUrl = null;
      freezeClearTimer = null;
    }, 350);
  }

  function dispatchStateChange(state: StreamState | 'loading') {
    const event = new CustomEvent('statechange', {
      bubbles: true,
      detail: { cameraId, state },
    });
    videoEl?.parentElement?.dispatchEvent(event);
  }

  $effect(() => {
    dispatchStateChange(streamState);
  });

  function updateStreamState() {
    if (snapshotMode) {
      streamState = 'snapshot';
      return;
    }
    const prevState = streamState;
    if (whepPlaybackReady(webrtcState, shouldExpectVideo(), hasFirstVideoFrame)) {
      streamState = 'playing';
      if (prevState !== 'playing' && frozenFrameUrl) clearFreezeFrame();
      if (coordinator && hasActiveCoordinatedReconnect) {
        coordinator.completeReconnect(cameraId);
        hasActiveCoordinatedReconnect = false;
      }
    } else if (webrtcState === 'connecting' || webrtcState === 'connected') {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'buffering';
    } else if (webrtcState === 'disconnected') {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'buffering';
    } else {
      if (prevState === 'playing') captureFreezeFrame();
      streamState = 'error';
    }
  }

  $effect(() => {
    updateStreamState();
  });

  // ICE connected only means transport is ready, not that a decodable video
  // frame has reached the <video>. Prevent "green online but black screen".
  function markFirstVideoFrame() {
    if (destroyed || !shouldExpectVideo() || hasFirstVideoFrame || !videoEl) return;
    if (videoEl.videoWidth === 0 || videoEl.videoHeight === 0) return;
    hasFirstVideoFrame = true;
    reconnectAttempts = 0;
    finishCoordinatedReconnect();
  }

  function watchFirstVideoFrame(peer: RTCPeerConnection, epoch: number) {
    if (!videoEl || !shouldExpectVideo() || hasFirstVideoFrame || videoFrameCallbackId !== null) return;
    const element = videoEl;
    if ('requestVideoFrameCallback' in element) {
      videoFrameCallbackId = element.requestVideoFrameCallback(() => {
        videoFrameCallbackId = null;
        if (!destroyed && epoch === connectionEpoch && pc === peer) markFirstVideoFrame();
      });
    }
  }

  function getBackoffDelay(): number {
    if (reconnectAttempts >= maxReconnectAttempts) return reconnectDelays[reconnectDelays.length - 1];
    return reconnectDelays[reconnectAttempts];
  }

  function scheduleReconnect() {
    // One pending retry per player. Multiple ICE state callbacks must not
    // schedule concurrent offers or consume extra coordinator slots.
    if (reconnectTimer || coordinatedTimer) return;
    if (hasActiveCoordinatedReconnect) {
      if (!coordinatedAttemptRunning) return; // queued, or not yet started
      // A granted WHEP attempt failed before ever producing a frame.
      // Release its slot; otherwise future retries are permanently blocked.
      finishCoordinatedReconnect();
    }
    if (reconnectAttempts >= maxReconnectAttempts) {
      enterSnapshotMode();
      return;
    }
    captureFreezeFrame();
    reconnectAttempts++;

    const doReconnect = () => {
      reconnectTimer = null;
      if (coordinator && hasActiveCoordinatedReconnect) coordinatedAttemptRunning = true;
      destroyPeerConnection();
      initWebRTC();
    };

    if (coordinator) {
      // Use coordinator's delay instead of per-player backoff
      if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
      hasActiveCoordinatedReconnect = true;
      const delay = coordinator.requestReconnect(cameraId, (grantedDelay) => {
        reconnectSlotGranted = true;
        coordinatedTimer = setTimeout(() => {
          coordinatedTimer = null;
          doReconnect();
        }, grantedDelay);
      });
      if (delay >= 0) {
        reconnectSlotGranted = true;
        coordinatedTimer = setTimeout(() => {
          coordinatedTimer = null;
          doReconnect();
        }, delay);
      }
      return;
    }

    const delay = getBackoffDelay();
    reconnectTimer = setTimeout(doReconnect, delay);
  }

  function enterSnapshotMode() {
    if (snapshotMode) return;
    console.warn(`WebRTC max retries reached for ${cameraId}, entering snapshot fallback`);
    sendTelemetry('stream_degradation', cameraId, undefined, { protocol: 'webrtc', target: 'snapshot' });
    snapshotMode = true;
    streamState = 'snapshot';
    destroyPeerConnection();
    refreshSnapshot();
    snapshotRefreshTimer = setInterval(refreshSnapshot, SNAPSHOT_REFRESH_MS);
    snapshotRestoreTimer = setTimeout(exitSnapshotMode, SNAPSHOT_RESTORE_MS);
  }

  function refreshSnapshot() {
    const authHeader = getAuthHeader();
    let url = getSnapshotUrl(cameraId);
    if (authHeader) {
      const token = authHeader.replace('Basic ', '');
      url += `?token=${encodeURIComponent(token)}`;
    }
    snapshotSrc = `${url}&_t=${Date.now()}`;
  }

  function exitSnapshotMode() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    streamState = 'loading';
    webrtcState = 'connecting';
    initWebRTC();
  }

  function stopSnapshotMode() {
    snapshotMode = false;
    snapshotSrc = '';
    if (snapshotRefreshTimer) { clearInterval(snapshotRefreshTimer); snapshotRefreshTimer = null; }
    if (snapshotRestoreTimer) { clearTimeout(snapshotRestoreTimer); snapshotRestoreTimer = null; }
  }

  function destroyPeerConnection() {
    connectionEpoch++;
    if (videoFrameCallbackId !== null && videoEl && 'cancelVideoFrameCallback' in videoEl) {
      videoEl.cancelVideoFrameCallback(videoFrameCallbackId);
    }
    videoFrameCallbackId = null;
    hasFirstVideoFrame = false;
    connectedAt = 0;
    activeWhepRequest?.abort();
    activeWhepRequest = null;
    if (reconnectTimer) {
      clearTimeout(reconnectTimer);
      reconnectTimer = null;
    }
    if (zombieInterval) {
      clearInterval(zombieInterval);
      zombieInterval = null;
    }

    if (disconnectGraceTimer) { clearTimeout(disconnectGraceTimer); disconnectGraceTimer = null; }

    // Release the server-side WHEP subscriber before discarding the connection.
    if (sessionUrl) {
      releaseWhep(sessionUrl);
      sessionUrl = null;
    }

    if (pc) {
      try {
        pc.close();
      } catch (e) { console.warn('WebRTC close error:', e); }
      pc = null;
    }
    // Avoid treating the last frame of an old peer as proof that the next
    // connection is rendering. A captured freeze frame is handled separately.
    if (videoEl) videoEl.srcObject = null;
  }

  function startZombieDetector() {
    if (zombieInterval) clearInterval(zombieInterval);
    lastPlaybackTime = videoEl?.currentTime ?? 0;
    zombieCount = 0;

    zombieInterval = setInterval(() => {
      if (!videoEl || !pc) return;
      const now = Date.now();

      // Do not penalize hidden tabs, but detect ICE-connected sessions
      // which never deliver their FIRST decoded video frame.
      if (!tabVisible || document.hidden) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
        return;
      }
      if (whepFirstVideoFrameTimedOut(connectedAt, now, shouldExpectVideo(), hasFirstVideoFrame, tabVisible && !document.hidden)) {
        console.warn(`WebRTC connected without first video frame for ${cameraId}, retrying`);
        sendTelemetry('stream_degradation', cameraId, undefined, { protocol: 'webrtc', reason: 'first_video_frame_timeout' });
        scheduleReconnect();
        return;
      }
      if (videoEl.paused) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
        return;
      }

      // Check if video is stuck (readyState 0 for >20s or no playback progress for >60s)
      if (videoEl.readyState === 0) {
        zombieCount++;
        if (zombieCount >= 4) {
          // ~20s stuck — reconnect
          console.warn(`WebRTC zombie detected for ${cameraId}, reconnecting`);
          zombieCount = 0;
          captureFreezeFrame();
          destroyPeerConnection();
          initWebRTC();
          return;
        }
      } else if (videoEl.currentTime !== lastPlaybackTime) {
        lastPlaybackTime = videoEl.currentTime;
        zombieCount = 0;
      } else {
        zombieCount++;
        if (zombieCount >= 12) {
          // ~60s no progress — reconnect
          console.warn(`WebRTC zombie (no progress) for ${cameraId}, reconnecting`);
          zombieCount = 0;
          reconnectAttempts = 0;
          captureFreezeFrame();
          destroyPeerConnection();
          initWebRTC();
          return;
        }
      }
    }, 5000);
  }

  async function initWebRTC() {
    if (!videoEl) return;

    // Clear any stale zombie detector
    if (zombieInterval) { clearInterval(zombieInterval); zombieInterval = null; }

    if (!videoEl) return;
    if (destroyed) return;

    streamState = 'loading';
    webrtcState = 'connecting';
    hasFirstVideoFrame = false;
    negotiatedHasVideo = null;
    connectedAt = 0;
    const epoch = ++connectionEpoch;
    const isCurrent = (candidate: RTCPeerConnection) => !destroyed && epoch === connectionEpoch && pc === candidate;

    try {
      const iceServers: RTCConfiguration = {
        iceServers: [], // WHEP typically uses no STUN/TURN for LAN
      };

      const peerConnection = new RTCPeerConnection(iceServers);
      pc = peerConnection;

      // Add recvonly transceiver for video
      peerConnection.addTransceiver('video', { direction: 'recvonly' });
      // Add recvonly transceiver for audio (will be rejected by server if no audio)
      peerConnection.addTransceiver('audio', { direction: 'recvonly' });

      // Handle incoming tracks
      peerConnection.ontrack = (event) => {
        if (!isCurrent(peerConnection) || !videoEl) return;
        // WHEP can deliver streamless tracks (msid omitted). A track is
        // still valid in that case and must be attached to the video element.
        if (event.streams[0]) {
          videoEl.srcObject = event.streams[0];
        } else {
          const current = videoEl.srcObject instanceof MediaStream
            ? videoEl.srcObject : new MediaStream();
          if (!current.getTracks().includes(event.track)) current.addTrack(event.track);
          videoEl.srcObject = current;
        }
        videoEl.play().catch(() => {});
        if (event.track.kind === 'video') watchFirstVideoFrame(peerConnection, epoch);
      };

      // Handle connection state changes
      peerConnection.onconnectionstatechange = () => {
        if (!isCurrent(peerConnection)) return;
        const state = peerConnection.connectionState;
        switch (state) {
          case 'connected':
            if (disconnectGraceTimer) { clearTimeout(disconnectGraceTimer); disconnectGraceTimer = null; }
            if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
            if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
            finishCoordinatedReconnect();
            webrtcState = 'connected';
            connectedAt = Date.now();
            watchFirstVideoFrame(peerConnection, epoch);
            if (!shouldExpectVideo() || hasFirstVideoFrame) reconnectAttempts = 0;
            startZombieDetector();
            break;
          case 'disconnected':
            webrtcState = 'disconnected';
            // Allow transient ICE interruptions from background throttling to recover.
            if (!disconnectGraceTimer) {
              disconnectGraceTimer = setTimeout(() => {
                disconnectGraceTimer = null;
                if (isCurrent(peerConnection) && peerConnection.connectionState === 'disconnected') scheduleReconnect();
              }, 5000);
            }
            break;
          case 'failed':
            if (disconnectGraceTimer) { clearTimeout(disconnectGraceTimer); disconnectGraceTimer = null; }
            webrtcState = 'failed';
            scheduleReconnect();
            break;
          case 'closed':
            break;
        }
      };

      // Create offer
      const offer = await peerConnection.createOffer();
      await peerConnection.setLocalDescription(offer);
      if (!isCurrent(peerConnection)) return;

      // Wait for ICE gathering to complete
      await new Promise<void>((resolve) => {
        if (peerConnection.iceGatheringState === 'complete') {
          resolve();
        } else {
          const iceAc = new AbortController();
          peerConnection.addEventListener('icegatheringstatechange', () => {
            if (peerConnection.iceGatheringState === 'complete') {
              iceAc.abort();
              resolve();
            }
          }, { signal: iceAc.signal });
          // Timeout fallback (5s)
          setTimeout(resolve, 5000);
        }
      });
      if (!isCurrent(peerConnection)) return;

      // Send SDP offer to WHEP endpoint
      const authHeader = getAuthHeader();
      const headers: Record<string, string> = {
        'Content-Type': 'application/sdp',
      };
      if (authHeader) headers['Authorization'] = authHeader;

      const endpoint = whepUrl || `/api/cameras/${cameraId}/stream/webrtc`;
      const request = new AbortController();
      activeWhepRequest = request;
      const response = await fetch(endpoint, {
        signal: request.signal,
        method: 'POST',
        headers,
        body: peerConnection.localDescription!.sdp,
      });

      if (!response.ok) {
        const errorText = await response.text().catch(() => 'Unknown error');
        throw new Error(`WHEP server error: ${response.status} ${errorText}`);
      }
      const createdSession = whepSessionLocation(response, cameraId);
      if (!isCurrent(peerConnection)) {
        if (createdSession) releaseWhep(createdSession);
        return;
      }
      sessionUrl = createdSession;
      if (activeWhepRequest === request) activeWhepRequest = null;

      // Parse SDP answer
      const answerSDP = await response.text();
      if (destroyed) return;
      // Audio-only streams are valid WHEP sessions. Use negotiated media
      // capabilities rather than source_type labels to decide whether a frame is required.
      negotiatedHasVideo = sdpHasActiveVideoMedia(answerSDP);
      const answer = new RTCSessionDescription({
        type: 'answer',
        sdp: answerSDP,
      });

      if (!isCurrent(peerConnection)) return;
      await peerConnection.setRemoteDescription(answer);
    } catch (e) {
      if (pc && !isCurrent(pc)) return;
      console.warn('WebRTC init failed:', e);
      webrtcState = 'failed';
      scheduleReconnect();
    }
  }

  function handleReconnect() {
    stopSnapshotMode();
    reconnectAttempts = 0;
    webrtcState = 'connecting';
    captureFreezeFrame();
    coordinatedReconnect(() => {
      destroyPeerConnection();
      initWebRTC();
    });
  }

  // Main lifecycle
  $effect(() => {
    const _id = cameraId;
    if (!_id) return;

    stopSnapshotMode();
    destroyPeerConnection();
    streamState = 'loading';
    webrtcState = 'connecting';

    const timer = setTimeout(() => initWebRTC(), 50);
    return () => {
      clearTimeout(timer);
      destroyPeerConnection();
    };
  });

  // Keep WHEP alive across browser tab switches, rather than recreating
  // the subscriber and causing a black frame on every focus change.
  $effect(() => {
    const visible = tabVisible;
    const becameVisible = visible && !lastTabVisible;
    lastTabVisible = visible;
    if (becameVisible && !destroyed) {
      videoEl?.play().catch(() => {});
      if (!pc || pc.connectionState === 'failed' || pc.connectionState === 'closed') {
        destroyPeerConnection();
        initWebRTC();
      }
    }
  });

  onDestroy(() => {
    destroyed = true;
    stopSnapshotMode();
    if (coordinatedTimer) { clearTimeout(coordinatedTimer); coordinatedTimer = null; }
    finishCoordinatedReconnect();
    if (freezeClearTimer) { clearTimeout(freezeClearTimer); freezeClearTimer = null; }
    frozenFrameUrl = null;
    destroyPeerConnection();
  });

  // Derived
  let showOverlay = $derived(
    streamState === 'loading' || streamState === 'error' || streamState === 'buffering',
  );
  let overlayClass = $derived(
    streamState === 'loading'
      ? 'opacity-100'
      : streamState === 'error'
        ? 'opacity-100'
        : streamState === 'buffering'
          ? 'opacity-60'
          : streamState === 'snapshot'
            ? 'opacity-0 pointer-events-none'
            : 'opacity-0 pointer-events-none',
  );
  let dotColor = $derived(
    streamState === 'playing'
      ? 'bg-green-500'
      : streamState === 'buffering'
        ? 'bg-yellow-500 animate-pulse'
        : streamState === 'snapshot'
          ? 'bg-blue-500 animate-pulse'
          : streamState === 'error'
            ? 'bg-red-500'
            : 'bg-gray-400',
  );
  let dotTitle = $derived(
    streamState === 'playing'
      ? t('dashboard.live')
      : streamState === 'buffering'
        ? t('dashboard.buffering')
        : streamState === 'snapshot'
          ? t('dashboard.snapshotMode')
          : streamState === 'error'
            ? t('dashboard.errorState')
            : t('live.webrtc.connecting'),
  );
</script>

<!-- svelte-ignore binding_property_non_reactive -->
<div class="relative w-full h-full bg-black overflow-hidden group">
  <!-- Freeze frame — last good frame shown during reconnection -->
  {#if frozenFrameUrl}
    <img
      src={frozenFrameUrl}
      alt=""
      class="absolute inset-0 w-full h-full object-contain transition-opacity duration-300 {showFrozenFrame ? 'opacity-100' : 'opacity-0 pointer-events-none'}"
      aria-hidden="true"
    />
  {/if}

  {#if snapshotMode}
    <img
      src={snapshotSrc}
      alt="{cameraName} snapshot"
      class="absolute inset-0 w-full h-full object-contain"
    />
    <!-- Snapshot mode indicator -->
    <div class="absolute top-8 left-2 flex items-center gap-1.5 px-2 py-1 rounded bg-blue-500/20 border border-blue-400/30 z-20">
      <ImageIcon size={12} class="text-blue-400" />
      <span class="text-blue-300 text-xs">{t('live.snapshot.fallback')}</span>
    </div>
    <button
      onclick={handleReconnect}
      class="absolute bottom-14 left-1/2 -translate-x-1/2 flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors z-20"
    >
      <RefreshCw size={12} />
      {t('common.retry')}
    </button>
  {:else}
    <video
      bind:this={videoEl}
      class="w-full h-full object-contain"
      autoplay
      muted={isMuted}
      onloadeddata={markFirstVideoFrame}
      playsinline
      aria-label="{cameraName} — {dotTitle}"
    >
      {t('live.videoUnsupportedTag')}
    </video>
  {/if}

  <!-- Overlay -->
  <div
    class="absolute inset-0 flex items-center justify-center transition-opacity duration-200 {overlayClass}"
  >
    {#if streamState === 'loading'}
      <div class="absolute inset-0 overflow-hidden">
        <div
          class="absolute inset-0"
          style="background: linear-gradient(90deg, transparent 0%, rgba(255,255,255,0.04) 40%, rgba(255,255,255,0.08) 50%, rgba(255,255,255,0.04) 60%, transparent 100%); background-size: 200% 100%; animation: shimmer 1.8s ease-in-out infinite;"
        ></div>
      </div>
    {:else if streamState === 'error'}
      <div class="absolute inset-0 bg-black/70"></div>
      <div class="relative flex flex-col items-center gap-3 z-10">
        <AlertCircle size={28} class="text-red-400" />
        <span class="text-white/70 text-xs">{t('live.streamErrorRetries')}</span>
        <button
          onclick={handleReconnect}
          class="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-white/10 text-white/80 text-xs hover:bg-white/20 transition-colors"
        >
          <RefreshCw size={12} />
          {t('common.retry')}
        </button>
      </div>
    {:else if streamState === 'buffering'}
      <div class="relative flex items-center gap-2">
        <div class="w-3 h-3 border-2 border-white/30 border-t-white/80 rounded-full animate-spin"></div>
        <span class="text-white/50 text-xs">{t('live.loading')}</span>
      </div>
    {/if}
  </div>

  <!-- State dot -->
  <span
    class="absolute top-2 left-2 w-2 h-2 {dotColor} rounded-full z-10"
    title={dotTitle}
  ></span>

  <!-- Camera name bar -->
  <div
    class="absolute bottom-0 left-0 right-0 bg-gradient-to-t from-black/70 to-transparent px-3 py-2 z-10"
  >
    <div class="flex items-center gap-2">
      <span class="text-white text-sm font-medium truncate">{cameraName || cameraId}</span>
      <span class="text-white/50 text-xs">WebRTC</span>
    </div>
  </div>

  <!-- Mute/Unmute button -->
  <button
    onclick={(e: MouseEvent) => { e.stopPropagation(); toggleMute(); }}
    class="absolute bottom-10 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100 z-10"
    title={isMuted ? t('live.unmute') || 'Unmute' : t('live.mute') || 'Mute'}
  >
    {#if isMuted}
      <VolumeX size={16} />
    {:else}
      <Volume2 size={16} />
    {/if}
  </button>

  <!-- Expand/Shrink -->
  {#if expanded}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('shrink', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all z-10"
      title={t('dashboard.backToGrid')}
    >
      <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"></polyline><polyline points="20 10 14 10 14 4"></polyline><line x1="14" y1="10" x2="21" y2="3"></line><line x1="3" y1="21" x2="10" y2="14"></line></svg>
    </button>
  {:else}
    <button
      onclick={(e: MouseEvent) => {
        e.stopPropagation();
        videoEl?.parentElement?.dispatchEvent(new CustomEvent('expand', { bubbles: true, detail: { cameraId } }));
      }}
      class="absolute top-2 right-2 p-1.5 rounded-md bg-black/50 text-white/70 hover:text-white hover:bg-black/70 transition-all opacity-0 group-hover:opacity-100 z-10"
      title={t('dashboard.fullscreen')}
    >
      <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"></polyline><polyline points="9 21 3 21 3 15"></polyline><line x1="21" y1="3" x2="14" y2="10"></line><line x1="3" y1="21" x2="10" y2="14"></line></svg>
    </button>
  {/if}
</div>

<style>
  @keyframes shimmer {
    0% {
      background-position: -200% 0;
    }
    100% {
      background-position: 200% 0;
    }
  }
</style>
