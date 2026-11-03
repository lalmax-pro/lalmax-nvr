import { cleanup, fireEvent, render } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import XiaomiTalkButton from '../components/XiaomiTalkButton.svelte';
vi.mock('$lib/i18n', () => ({ t: (key: string) => key }));
vi.mock('$lib/toast', () => ({ showToast: vi.fn() }));
vi.mock('$lib/api/client', () => ({ getAuthToken: () => 'test-token' }));
let sockets: Socket[] = [];
class Socket {
  static OPEN = 1;
  readyState = 1;
  bufferedAmount = 0;
  onmessage: ((event: any) => void) | null = null;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  binaryType = '';
  close = vi.fn();
  send = vi.fn();
  constructor(public url: string) { sockets.push(this); }
}
function fixture(getUserMedia = vi.fn()) {
  const track = { stop: vi.fn() };
  const stream = { getTracks: () => [track] };
  const processor = { onaudioprocess: null as any, connect: vi.fn(), disconnect: vi.fn() };
  const createProcessor = vi.fn(() => processor);
  const close = vi.fn().mockResolvedValue(undefined);
  class Context {
    sampleRate: number;
    destination = {};
    close = close;
    resume = vi.fn().mockResolvedValue(undefined);
    createScriptProcessor = createProcessor;
    createMediaStreamSource = vi.fn(() => ({ connect: vi.fn() }));
    constructor(options: any) { this.sampleRate = options.sampleRate; }
  }
  vi.stubGlobal('WebSocket', Socket);
  vi.stubGlobal('AudioContext', Context);
  vi.stubGlobal('navigator', { mediaDevices: { getUserMedia } });
  return { track, stream, getUserMedia, createProcessor, processor, close };
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.clearAllMocks(); sockets = []; });
describe('Xiaomi talk negotiation', () => {
  it('waits for speaker format, sends 40 ms frames, and releases resources', async () => {
    const f = fixture();
    f.getUserMedia.mockResolvedValue(f.stream);
    const ui = render(XiaomiTalkButton, { cameraId: 'cam' });
    await fireEvent.click(ui.getByRole('button'));
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    const socket = sockets[0];
    socket.onopen?.();
    expect(f.createProcessor).not.toHaveBeenCalled();
    await socket.onmessage?.({ data: JSON.stringify({ status: 'connected', speaker_codec: 1024 }) });
    await vi.waitFor(() => expect(f.createProcessor).toHaveBeenCalledOnce());
    f.processor.onaudioprocess({ inputBuffer: { getChannelData: () => new Float32Array(1024) } });
    expect(socket.send).toHaveBeenCalledTimes(3);
    expect(socket.send.mock.calls[0][0].byteLength).toBe(640);
    ui.unmount();
    expect(f.track.stop).toHaveBeenCalledOnce();
    expect(socket.close).toHaveBeenCalledOnce();
    expect(f.close).toHaveBeenCalledOnce();
  });
  it('releases microphone permission that arrives after unmount', async () => {
    let resolve!: (value: any) => void;
    const f = fixture(vi.fn(() => new Promise(r => { resolve = r; })));
    const ui = render(XiaomiTalkButton, { cameraId: 'cam' });
    await fireEvent.click(ui.getByRole('button'));
    ui.unmount();
    resolve(f.stream);
    await vi.waitFor(() => expect(f.track.stop).toHaveBeenCalledOnce());
    expect(sockets).toHaveLength(0);
  });
  it('releases microphone when the speaker codec is unavailable', async () => {
    const f = fixture();
    vi.stubGlobal('AudioEncoder', undefined);
    f.getUserMedia.mockResolvedValue(f.stream);
    const ui = render(XiaomiTalkButton, { cameraId: 'cam' });
    await fireEvent.click(ui.getByRole('button'));
    await vi.waitFor(() => expect(sockets).toHaveLength(1));
    await sockets[0].onmessage?.({ data: JSON.stringify({ status: 'connected', speaker_codec: 1032 }) });
    expect(f.track.stop).toHaveBeenCalledOnce();
    expect(sockets[0].close).toHaveBeenCalledOnce();
    expect(f.createProcessor).not.toHaveBeenCalled();
  });
});
