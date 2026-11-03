// MISS speakers consume raw 40 ms frames, never WebM/Ogg containers.
export const MISS_PCM = 1024;
export const MISS_PCMA = 1027;
export const MISS_OPUS = 1032;

export interface TalkAudioEncoder {
  sampleRate: number;
  push(samples: Float32Array): void;
  close(): void;
}

function linearToALaw(sample: number): number {
  let pcm = Math.trunc(Math.max(-1, Math.min(1, sample)) * (sample < 0 ? 32768 : 32767));
  const mask = pcm >= 0 ? 0xd5 : 0x55;
  if (pcm < 0) pcm = -pcm - 1;
  let compressed: number;
  if (pcm < 256) compressed = pcm >> 4;
  else {
    let exponent = 7;
    for (let bit = 0x4000; (pcm & bit) === 0 && exponent > 0; bit >>= 1) exponent--;
    compressed = (exponent << 4) | ((pcm >> (exponent + 3)) & 0x0f);
  }
  return (compressed ^ mask) & 0xff;
}

export async function createTalkAudioEncoder(
  codec: number,
  send: (packet: ArrayBuffer) => void,
  onError: (error: Error) => void,
): Promise<TalkAudioEncoder> {
  if (![MISS_PCM, MISS_PCMA, MISS_OPUS].includes(codec)) throw new Error('unsupported_codec');
  const sampleRate = codec === MISS_OPUS ? 48000 : 8000;
  const frameSamples = sampleRate * 0.04;
  const frame = new Float32Array(frameSamples);
  let used = 0, timestamp = 0, closed = false;
  let encoder: any = null;
  const globals = globalThis as any;

  if (codec === MISS_OPUS) {
    if (!globals.AudioEncoder || !globals.AudioData) throw new Error('unsupported_codec');
    // https://www.w3.org/TR/webcodecs-opus-codec-registration/
    const config = {
      codec: 'opus', sampleRate, numberOfChannels: 1, bitrate: 32000,
      opus: { format: 'opus', frameDuration: 40000 },
    };
    const support = await globals.AudioEncoder.isConfigSupported(config);
    if (!support.supported) throw new Error('unsupported_codec');
    encoder = new globals.AudioEncoder({
      output: (chunk: any) => {
        if (closed) return;
        const packet = new ArrayBuffer(chunk.byteLength);
        chunk.copyTo(packet);
        send(packet);
      },
      error: (error: Error) => { if (!closed) onError(error); },
    });
    try { encoder.configure(config); }
    catch (error) { encoder.close(); throw error; }
  }

  return {
    sampleRate,
    push(samples) {
      if (closed) return;
      for (let offset = 0; offset < samples.length && !closed;) {
        const count = Math.min(frameSamples - used, samples.length - offset);
        frame.set(samples.subarray(offset, offset + count), used);
        used += count;
        offset += count;
        if (used !== frameSamples) continue;
        used = 0;
        if (codec === MISS_OPUS) {
          if (encoder.encodeQueueSize > 4) { onError(new Error('audio_backpressure')); return; }
          const data = new globals.AudioData({
            format: 'f32', sampleRate, numberOfFrames: frameSamples,
            numberOfChannels: 1, timestamp, data: frame,
          });
          try { encoder.encode(data); } finally { data.close(); }
          timestamp += 40000;
        } else if (codec === MISS_PCM) {
          const packet = new ArrayBuffer(frameSamples * 2);
          const view = new DataView(packet);
          for (let i = 0; i < frameSamples; i++) {
            const value = Math.max(-1, Math.min(1, frame[i]));
            view.setInt16(i * 2, Math.trunc(value * (value < 0 ? 32768 : 32767)), true);
          }
          send(packet);
        } else {
          send(Uint8Array.from(frame, linearToALaw).buffer);
        }
      }
    },
    close() {
      closed = true;
      if (encoder && encoder.state !== 'closed') encoder.close();
    },
  };
}
