import { afterEach, describe, expect, it, vi } from 'vitest';
import { VoIPTalk } from '$lib/voipTalk';
const api = vi.hoisted(() => ({ dial: vi.fn(), attach: vi.fn(), status: vi.fn(), hangup: vi.fn(), keep: vi.fn(), detach: vi.fn() }));
vi.mock('$lib/api/settings', () => ({
  dialVoIPCall: api.dial,
  attachVoIPTalk: api.attach,
  getVoIPStatus: api.status,
  hangupVoIPCall: api.hangup,
  keepVoIPTalk: api.keep,
  detachVoIPTalk: api.detach,
}));
let instances: MockPC[] = [];
class MockPC extends EventTarget {
  iceGatheringState = 'complete';
  connectionState = 'new';
  localDescription = { sdp: 'browser-offer' };
  ontrack: ((event: any) => void) | null = null;
  onconnectionstatechange: (() => void) | null = null;
  close = vi.fn();
  addTrack = vi.fn();
  createOffer = vi.fn().mockResolvedValue({ type: 'offer', sdp: 'browser-offer' });
  setLocalDescription = vi.fn().mockResolvedValue(undefined);
  setRemoteDescription = vi.fn(async () => {
    this.connectionState = 'connected';
    this.onconnectionstatechange?.();
  });
  constructor() {
    super();
    instances.push(this);
  }
}
function fixture(getUserMedia = vi.fn()) {
  vi.stubGlobal('RTCPeerConnection', MockPC);
  vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } });
  const track = { enabled: true, stop: vi.fn() };
  const stream = { getTracks: () => [track], getAudioTracks: () => [track] };
  const audio = { srcObject: null, play: vi.fn().mockResolvedValue(undefined) };
  const state = vi.fn(),
    error = vi.fn();
  const client = new VoIPTalk(audio as unknown as HTMLAudioElement, state, error, vi.fn());
  return { client, track, stream, audio, state, error, getUserMedia };
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  instances = [];
  vi.useRealTimers();
});
describe('VoIP browser talk lifecycle', () => {
  it('does not dial when microphone permission is denied', async () => {
    const f = fixture();
    f.getUserMedia.mockRejectedValue(new DOMException('Denied', 'NotAllowedError'));
    await f.client.start('1001', '');
    expect(api.dial).not.toHaveBeenCalled();
    expect(f.error).toHaveBeenCalledWith('microphone_denied');
  });
  it('cleans up late microphone access after cancellation', async () => {
    let resolve!: (v: unknown) => void;
    const f = fixture(
      vi.fn(
        () =>
          new Promise((r) => {
            resolve = r;
          }),
      ),
    );
    const start = f.client.start('1001', '');
    await f.client.stop();
    resolve(f.stream);
    await start;
    expect(f.track.stop).toHaveBeenCalled();
    expect(api.dial).not.toHaveBeenCalled();
  });
  it('hangs up a late call creation reply after cancellation', async () => {
    let resolve!: (v: unknown) => void;
    const f = fixture();
    f.getUserMedia.mockResolvedValue(f.stream);
    api.dial.mockImplementation(
      () =>
        new Promise((r) => {
          resolve = r;
        }),
    );
    api.hangup.mockResolvedValue({});
    const start = f.client.start('1001', 'rtp');
    await vi.waitFor(() => expect(api.dial).toHaveBeenCalled());
    await f.client.stop();
    resolve({ call_id: 'late', talk_token: 'token' });
    await start;
    expect(api.hangup).toHaveBeenCalledWith('late');
    expect(instances[0].close).toHaveBeenCalled();
  });
  it('negotiates duplex audio, mutes, and releases all resources', async () => {
    const f = fixture();
    f.getUserMedia.mockResolvedValue(f.stream);
    api.dial.mockResolvedValue({ call_id: 'call', talk_token: 'token' });
    api.status.mockResolvedValue({ calls: [{ call_id: 'call', state: 'established' }] });
    api.attach.mockResolvedValue({ type: 'answer', sdp: 'answer' });
    api.hangup.mockResolvedValue({});
    await f.client.start('1001', 'dtls');
    expect(api.attach).toHaveBeenCalledWith('call', 'token', 'browser-offer', expect.any(AbortSignal));
    expect(f.state).toHaveBeenCalledWith('established');
    f.client.mute(true);
    expect(f.track.enabled).toBe(false);
    f.client.mute(false);
    expect(f.track.enabled).toBe(true);
    await f.client.stop();
    expect(f.track.stop).toHaveBeenCalled();
    expect(instances[0].close).toHaveBeenCalled();
    expect(api.hangup).toHaveBeenCalledWith('call');
    expect(f.audio.srcObject).toBeNull();
  });
  it('shows the SIP failure and releases the microphone', async () => {
    const f = fixture();
    f.getUserMedia.mockResolvedValue(f.stream);
    api.dial.mockResolvedValue({ call_id: 'busy', talk_token: 'token' });
    api.status.mockResolvedValue({
      calls: [{ call_id: 'busy', state: 'failed', failure_reason: 'SIP 486 Busy Here' }],
    });
    api.hangup.mockResolvedValue({});
    await f.client.start('1001', '');
    expect(f.error).toHaveBeenCalledWith('SIP 486 Busy Here');
    expect(f.track.stop).toHaveBeenCalled();
    expect(api.attach).not.toHaveBeenCalled();
  });
  it('joins an inbound SIP call and leaves WebRTC without hanging up SIP', async () => {
    const f = fixture();
    f.getUserMedia.mockResolvedValue(f.stream);
    api.attach.mockResolvedValue({ type: 'answer', sdp: 'answer' });
    api.status.mockResolvedValue({ calls: [{ call_id: 'inbound', state: 'established' }] });
    api.detach.mockResolvedValue({});
    await f.client.joinInbound('inbound', 'short-lived-token');
    expect(api.attach).toHaveBeenCalledWith('inbound', 'short-lived-token', 'browser-offer', expect.any(AbortSignal));
    expect(f.state).toHaveBeenCalledWith('established');
    await f.client.stop();
    expect(api.detach).toHaveBeenCalledWith('inbound', 'short-lived-token');
    expect(api.hangup).not.toHaveBeenCalled();
    expect(f.track.stop).toHaveBeenCalled();
    expect(instances[0].close).toHaveBeenCalled();
  });
});
