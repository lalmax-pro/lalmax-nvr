import { attachVoIPTalk, detachVoIPTalk, dialVoIPCall, getVoIPStatus, hangupVoIPCall, keepVoIPTalk } from './api/settings';
import type { VoIPMediaSecurity } from './api/settings';

export class VoIPTalk {
  private stream: MediaStream | null = null;
  private pc: RTCPeerConnection | null = null;
  private id = '';
  private token = '';
  private generation = 0;
  private timer: ReturnType<typeof setInterval> | undefined;
  private watcher: ReturnType<typeof setInterval> | undefined;
  private polling = false;
  private connectionTimer: ReturnType<typeof setTimeout> | undefined;
  private controller: AbortController | null = null;
  private inbound = false;
  constructor(
    private audio: HTMLAudioElement,
    private state: (value: string) => void,
    private error: (value: string) => void,
    private blocked: () => void,
    private dtmfAvailable: (value: boolean) => void = () => {},
  ) {}
  get activeCallId() {
    return this.id;
  }
  async start(user: string, security: VoIPMediaSecurity) {
    this.inbound = false;
    const generation = ++this.generation;
    const current = () => generation === this.generation;
    const controller = new AbortController();
    this.controller = controller;
    this.state('preparing');
    try {
      if (!navigator.mediaDevices?.getUserMedia) throw new Error('microphone_unavailable');
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
        video: false,
      });
      if (!current()) {
        stream.getTracks().forEach((t) => t.stop());
        return;
      }
      this.stream = stream;
      const pc = new RTCPeerConnection();
      this.pc = pc;
      pc.addTrack(stream.getAudioTracks()[0], stream);
      pc.ontrack = (event) => {
        if (!current()) return;
        this.audio.srcObject = event.streams[0] || new MediaStream([event.track]);
        void this.audio.play().catch(() => this.blocked());
      };
      pc.onconnectionstatechange = () => {
        if (!current()) return;
        if (pc.connectionState === 'connected') {
          clearTimeout(this.connectionTimer);
          this.state('established');
        }
        if (pc.connectionState === 'failed') {
          this.error('webrtc_failed');
          void this.stop();
        }
      };
      await pc.setLocalDescription(await pc.createOffer());
      await this.gather(pc, controller.signal);
      if (!current()) return;
      const offer = pc.localDescription!.sdp;
      // Do not abort creation: a returned call ID must be cleaned up even if the
      // page closes while the request is in flight. Server lease covers lost replies.
      const result = await dialVoIPCall(user, security, offer);
      if (!current()) {
        await hangupVoIPCall(result.call_id).catch(() => {});
        return;
      }
      this.id = result.call_id;
      this.token = result.talk_token;
      this.state('dialing');
      this.timer = setInterval(() => {
        void keepVoIPTalk(this.id, this.token).catch(() => {
          if (current()) {
            this.error('call_expired');
            void this.stop();
          }
        });
      }, 10000);
      const deadline = Date.now() + 35000;
      while (current()) {
        const status = await getVoIPStatus(controller.signal);
        const call = status.calls.find((c) => c.call_id === this.id);
        if (!current()) return;
        if (!call || call.state === 'failed' || call.state === 'ended')
          throw new Error(call?.failure_reason || 'call_ended');
        if (call.state === 'established') {
          this.dtmfAvailable(Boolean(call.dtmf_available));
          break;
        }
        this.state(call.state);
        if (Date.now() > deadline) throw new Error('call_timeout');
        await new Promise<void>((resolve) => setTimeout(resolve, 500));
      }
      if (!current()) return;
      this.state('connecting');
      const answer = await attachVoIPTalk(this.id, this.token, offer, controller.signal);
      if (!current()) return;
      await pc.setRemoteDescription(answer);
      if (pc.connectionState !== 'connected')
        this.connectionTimer = setTimeout(() => {
          if (current()) {
            this.error('webrtc_timeout');
            void this.stop();
          }
        }, 15000);
      this.watcher = setInterval(() => {
        if (this.polling) return;
        this.polling = true;
        void getVoIPStatus(controller.signal)
          .then((status) => {
            if (!current()) return;
            const call = status.calls.find((c) => c.call_id === this.id);
            if (!call || call.state === 'ended' || call.state === 'failed') {
              if (call?.state === 'failed') this.error(call.failure_reason || 'call_ended');
              void this.stop(false);
            }
          })
          .catch(() => {})
          .finally(() => {
            this.polling = false;
          });
      }, 1000);
    } catch (e) {
      if (!current()) return;
      this.error(
        e instanceof DOMException && e.name === 'NotAllowedError'
          ? 'microphone_denied'
          : e instanceof Error
            ? e.message
            : String(e),
      );
      await this.stop();
    }
  }
  async joinInbound(id: string, token: string) {
    const generation = ++this.generation;
    const current = () => generation === this.generation;
    const controller = new AbortController();
    this.controller = controller;
    this.inbound = true;
    this.id = id;
    this.token = token;
    this.state('preparing');
    try {
      if (!navigator.mediaDevices?.getUserMedia) throw new Error('microphone_unavailable');
      const stream = await navigator.mediaDevices.getUserMedia({
        audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
        video: false,
      });
      if (!current()) {
        stream.getTracks().forEach((track) => track.stop());
        return;
      }
      this.stream = stream;
      const pc = new RTCPeerConnection();
      this.pc = pc;
      pc.addTrack(stream.getAudioTracks()[0], stream);
      pc.ontrack = (event) => {
        if (!current()) return;
        this.audio.srcObject = event.streams[0] || new MediaStream([event.track]);
        void this.audio.play().catch(() => this.blocked());
      };
      pc.onconnectionstatechange = () => {
        if (!current()) return;
        if (pc.connectionState === 'connected') {
          clearTimeout(this.connectionTimer);
          this.state('established');
        } else if (pc.connectionState === 'failed') {
          this.error('webrtc_failed');
          void this.stop();
        }
      };
      await pc.setLocalDescription(await pc.createOffer());
      await this.gather(pc, controller.signal);
      if (!current()) return;
      this.state('connecting');
      const answer = await attachVoIPTalk(id, token, pc.localDescription!.sdp, controller.signal);
      if (!current()) return;
      await pc.setRemoteDescription(answer);
      if (pc.connectionState !== 'connected')
        this.connectionTimer = setTimeout(() => {
          if (current()) {
            this.error('webrtc_timeout');
            void this.stop();
          }
        }, 15000);
      this.watchCall(id, controller.signal, current);
      this.timer = setInterval(() => {
        void keepVoIPTalk(id, token).catch(() => {
          if (current()) {
            this.error('call_expired');
            void this.stop();
          }
        });
      }, 10000);
    } catch (error) {
      if (!current()) return;
      this.error(
        error instanceof DOMException && error.name === 'NotAllowedError'
          ? 'microphone_denied'
          : error instanceof Error
            ? error.message
            : String(error),
      );
      await this.stop();
    }
  }
  private gather(pc: RTCPeerConnection, signal: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
      const finish = (err?: Error) => {
        clearTimeout(timer);
        pc.removeEventListener('icegatheringstatechange', changed);
        signal.removeEventListener('abort', abort);
        err ? reject(err) : resolve();
      };
      const changed = () => {
        if (pc.iceGatheringState === 'complete') finish();
      };
      const abort = () => finish(new Error('call_ended'));
      const timer = setTimeout(() => finish(new Error('webrtc_timeout')), 10000);
      pc.addEventListener('icegatheringstatechange', changed);
      signal.addEventListener('abort', abort, { once: true });
      if (signal.aborted) abort();
      else changed();
    });
  }
  mute(muted: boolean) {
    this.stream?.getAudioTracks().forEach((t) => {
      t.enabled = !muted;
    });
  }
  async stop(notify = true) {
    ++this.generation;
    this.controller?.abort();
    this.controller = null;
    clearInterval(this.timer);
    clearInterval(this.watcher);
    clearTimeout(this.connectionTimer);
    this.connectionTimer = undefined;
    this.timer = undefined;
    this.watcher = undefined;
    const id = this.id;
    const token = this.token;
    const inbound = this.inbound;
    this.id = '';
    this.token = '';
    this.inbound = false;
    this.stream?.getTracks().forEach((t) => t.stop());
    this.stream = null;
    if (this.pc) {
      this.pc.onconnectionstatechange = null;
      this.pc.close();
      this.pc = null;
    }
    this.audio.srcObject = null;
    this.state('idle');
    if (id && notify) {
      if (inbound) await detachVoIPTalk(id, token).catch(() => {});
      else await hangupVoIPCall(id).catch(() => {});
    }
  }

  private watchCall(id: string, signal: AbortSignal, current: () => boolean) {
    this.watcher = setInterval(() => {
      if (this.polling) return;
      this.polling = true;
      void getVoIPStatus(signal)
        .then((status) => {
          if (!current()) return;
          const call = status.calls.find((item) => item.call_id === id);
          if (!call || call.state === 'ended' || call.state === 'failed') {
            if (call?.state === 'failed') this.error(call.failure_reason || 'call_ended');
            void this.stop(false);
          }
        })
        .catch(() => {})
        .finally(() => {
          this.polling = false;
        });
    }, 1000);
  }
}
