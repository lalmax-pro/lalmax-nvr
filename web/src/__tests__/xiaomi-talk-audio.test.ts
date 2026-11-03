import { afterEach, describe, expect, it, vi } from 'vitest';
import { createTalkAudioEncoder, MISS_PCMA, MISS_PCM, MISS_OPUS } from '$lib/xiaomiTalkAudio';
afterEach(() => vi.unstubAllGlobals());
describe('Xiaomi speaker frames', () => {
  it('buffers microphone callbacks into exact 40 ms PCMA packets', async () => {
    const send = vi.fn();
    const audio = await createTalkAudioEncoder(MISS_PCMA, send, vi.fn());
    expect(audio.sampleRate).toBe(8000);
    audio.push(new Float32Array(1024));
    expect(send).toHaveBeenCalledTimes(3);
    for (const [packet] of send.mock.calls) {
      expect(packet.byteLength).toBe(320);
      expect([...new Uint8Array(packet)]).toEqual(Array(320).fill(0xd5));
    }
    audio.push(new Float32Array(256));
    expect(send).toHaveBeenCalledTimes(4);
    audio.close();
    audio.push(new Float32Array(320));
    expect(send).toHaveBeenCalledTimes(4);
  });
  it('encodes signed PCM in little endian instead of relabeling A-law', async () => {
    const send = vi.fn();
    const audio = await createTalkAudioEncoder(MISS_PCM, send, vi.fn());
    const frame = new Float32Array(320);
    frame.set([1, -1]);
    audio.push(frame);
    const bytes = new Uint8Array(send.mock.calls[0][0]);
    expect(bytes.byteLength).toBe(640);
    expect([...bytes.slice(0, 6)]).toEqual([0xff, 0x7f, 0, 0x80, 0, 0]);
  });
  it('encodes A-law polarity and clipping', async () => {
    const send = vi.fn();
    const audio = await createTalkAudioEncoder(MISS_PCMA, send, vi.fn());
    const frame = new Float32Array(320);
    frame.set([1, -1, 2, -2]);
    audio.push(frame);
    expect([...new Uint8Array(send.mock.calls[0][0]).slice(0, 4)]).toEqual([0xaa, 0x2a, 0xaa, 0x2a]);
  });
  it('refuses unsupported formats and missing Opus encoders', async () => {
    vi.stubGlobal('AudioEncoder', undefined);
    await expect(createTalkAudioEncoder(123, vi.fn(), vi.fn())).rejects.toThrow('unsupported_codec');
    await expect(createTalkAudioEncoder(MISS_OPUS, vi.fn(), vi.fn())).rejects.toThrow('unsupported_codec');
  });
  it('encodes raw Opus with 40 ms timestamps and releases AudioData', async () => {
    const frames: any[] = [];
    const closeData = vi.fn(), configure = vi.fn(), close = vi.fn();
    class Encoder {
      state = 'configured';
      encodeQueueSize = 0;
      configure = configure;
      close = close;
      static isConfigSupported = vi.fn().mockResolvedValue({ supported: true });
      constructor(private handlers: any) {}
      encode(data: any) {
        frames.push(data.init);
        this.handlers.output({ byteLength: 2, copyTo: (packet: ArrayBuffer) => new Uint8Array(packet).set([0x78, 1]) });
      }
    }
    vi.stubGlobal('AudioEncoder', Encoder);
    vi.stubGlobal('AudioData', class { close = closeData; constructor(public init: any) {} });
    const send = vi.fn();
    const audio = await createTalkAudioEncoder(MISS_OPUS, send, vi.fn());
    audio.push(new Float32Array(3840));
    expect(configure).toHaveBeenCalledWith(expect.objectContaining({
      codec: 'opus', sampleRate: 48000, opus: { format: 'opus', frameDuration: 40000 },
    }));
    expect(frames.map(f => [f.numberOfFrames, f.timestamp])).toEqual([[1920, 0], [1920, 40000]]);
    expect([...new Uint8Array(send.mock.calls[0][0])]).toEqual([0x78, 1]);
    expect(closeData).toHaveBeenCalledTimes(2);
    audio.close();
    expect(close).toHaveBeenCalledOnce();
  });
});
