/**
 * Live audio playback for the WebCodecs player.
 *
 * Handles two source codecs delivered over the WebSocket protocol:
 *   - AAC   — decoded via the WebCodecs AudioDecoder (mp4a.40.x) using the
 *             AudioSpecificConfig as `description`, output as AudioData.
 *   - G.711 — A-law / µ-law companded 8-bit samples, expanded to PCM in JS
 *             (WebCodecs AudioDecoder does not support G.711 across browsers).
 *
 * Decoded PCM is scheduled on a running cursor through the Web Audio API.
 * Playback is gated by the browser autoplay policy: the AudioContext starts
 * suspended until resume() is called from a user gesture. While suspended (or
 * muted), incoming frames are dropped rather than buffered, so resuming starts
 * from live instead of replaying a backlog.
 */

import { AudioCodecId } from './protocol';
import type { AudioCodecInfo } from './protocol';

// Lead time and the maximum scheduling latency before we snap back to live.
const SCHEDULE_LEAD = 0.08; // seconds of lead when (re)starting the cursor
const MAX_LATENCY = 0.6; // if the cursor drifts this far ahead, resync to live

export type AudioPlayerState = 'idle' | 'running' | 'suspended' | 'unsupported' | 'error';

/** Returns whether Web Audio is available in this environment. */
export function audioSupported(): boolean {
  return typeof AudioContext !== 'undefined' ||
    typeof (globalThis as Record<string, unknown>).webkitAudioContext !== 'undefined';
}

export class AudioPlayer {
  private _ctx: AudioContext | null = null;
  private _gain: GainNode | null = null;
  private _decoder: AudioDecoder | null = null; // AAC only
  private _info: AudioCodecInfo | null = null;
  private _cursor = 0; // next scheduled start time (in ctx time)
  private _basePts: number | null = null; // first frame pts (for AAC chunk timestamps)
  private _muted = true; // start muted — playback requires a user gesture
  private _volume = 1;
  private _closed = false;
  private _g711Table: Float32Array | null = null;

  /** True once configured with a usable codec. */
  get configured(): boolean {
    return this._info !== null;
  }

  get state(): AudioPlayerState {
    if (this._closed) return 'idle';
    if (!audioSupported()) return 'unsupported';
    if (!this._ctx) return 'idle';
    return this._ctx.state === 'running' ? 'running' : 'suspended';
  }

  get muted(): boolean {
    return this._muted;
  }

  /**
   * Configure the player for a codec. Safe to call again with a new codec
   * (e.g. after reconnect) — it rebuilds the decode path.
   */
  configure(info: AudioCodecInfo): void {
    if (this._closed) return;
    if (!audioSupported()) return;
    if (info.codec === AudioCodecId.None) return;

    this._teardownDecoder();
    this._info = info;
    this._basePts = null;

    if (!this._ctx) {
      try {
        this._ctx = new AudioContext();
        this._gain = this._ctx.createGain();
        this._gain.gain.value = this._muted ? 0 : this._volume;
        this._gain.connect(this._ctx.destination);
      } catch {
        this._ctx = null;
        return;
      }
    }

    if (info.codec === AudioCodecId.AAC) {
      this._setupAacDecoder(info);
    } else {
      this._g711Table = buildG711Table(info.codec === AudioCodecId.G711U);
    }
  }

  /** Feed one encoded audio frame. Dropped while suspended or muted. */
  decodeFrame(pts: number, data: Uint8Array): void {
    if (this._closed || !this._info || !this._ctx) return;
    // Drop audio unless actively playing — avoids building a backlog while the
    // context is suspended (autoplay) or the user has muted.
    if (this._muted || this._ctx.state !== 'running') return;

    if (this._info.codec === AudioCodecId.AAC) {
      this._decodeAac(pts, data);
    } else {
      this._playG711(data);
    }
  }

  /** Resume the AudioContext (call from a user gesture) and unmute. */
  async resume(): Promise<void> {
    if (this._closed || !this._ctx) return;
    try {
      await this._ctx.resume();
    } catch {
      /* ignore */
    }
    this.setMuted(false);
  }

  setMuted(muted: boolean): void {
    this._muted = muted;
    if (this._gain && this._ctx) {
      this._gain.gain.setValueAtTime(muted ? 0 : this._volume, this._ctx.currentTime);
    }
    if (muted) {
      // Reset cursor so unmuting starts from live, not a stale timeline.
      this._cursor = 0;
    }
  }

  setVolume(v: number): void {
    this._volume = Math.max(0, Math.min(1, v));
    if (this._gain && this._ctx && !this._muted) {
      this._gain.gain.setValueAtTime(this._volume, this._ctx.currentTime);
    }
  }

  /** Full cleanup. */
  close(): void {
    if (this._closed) return;
    this._closed = true;
    this._teardownDecoder();
    if (this._ctx) {
      try { this._ctx.close(); } catch { /* ignore */ }
      this._ctx = null;
    }
    this._gain = null;
    this._info = null;
    this._g711Table = null;
  }

  // ─── AAC ──────────────────────────────────────────────────────────────

  private _setupAacDecoder(info: AudioCodecInfo): void {
    if (typeof AudioDecoder === 'undefined') return;
    const objType = info.config && info.config.length > 0 ? (info.config[0] >> 3) & 0x1f : 2;
    const codec = `mp4a.40.${objType || 2}`;
    try {
      this._decoder = new AudioDecoder({
        output: (frame: AudioData) => this._onAacOutput(frame),
        error: () => { /* swallow — audio is best-effort */ },
      });
      this._decoder.configure({
        codec,
        sampleRate: info.sampleRate,
        numberOfChannels: info.channels || 1,
        description: info.config,
      });
    } catch {
      this._teardownDecoder();
    }
  }

  private _decodeAac(pts: number, data: Uint8Array): void {
    if (!this._decoder || this._decoder.state !== 'configured' || !this._info) return;
    if (this._basePts === null) this._basePts = pts;
    // Relative timestamp in microseconds (AAC AUs are independently decodable).
    const ts = Math.round(((pts - this._basePts) / this._info.sampleRate) * 1e6);
    try {
      // Copy out of the shared receive buffer — the chunk retains the bytes.
      const buf = data.slice();
      this._decoder.decode(new EncodedAudioChunk({ type: 'key', timestamp: ts, data: buf }));
    } catch {
      /* ignore */
    }
  }

  private _onAacOutput(frame: AudioData): void {
    try {
      if (this._closed || this._muted || !this._ctx || this._ctx.state !== 'running') return;
      const channels = frame.numberOfChannels;
      const sampleRate = frame.sampleRate;
      const frames = frame.numberOfFrames;
      const buffer = this._ctx.createBuffer(channels, frames, sampleRate);
      for (let ch = 0; ch < channels; ch++) {
        const dest = buffer.getChannelData(ch);
        frame.copyTo(dest, { planeIndex: ch, format: 'f32-planar' });
      }
      this._schedule(buffer);
    } catch {
      /* ignore */
    } finally {
      try { frame.close(); } catch { /* already closed */ }
    }
  }

  // ─── G.711 ────────────────────────────────────────────────────────────

  private _playG711(data: Uint8Array): void {
    if (!this._ctx || !this._g711Table || !this._info) return;
    const table = this._g711Table;
    const n = data.length;
    if (n === 0) return;
    const buffer = this._ctx.createBuffer(1, n, this._info.sampleRate);
    const dest = buffer.getChannelData(0);
    for (let i = 0; i < n; i++) dest[i] = table[data[i]];
    this._schedule(buffer);
  }

  // ─── Scheduling ───────────────────────────────────────────────────────

  private _schedule(buffer: AudioBuffer): void {
    if (!this._ctx || !this._gain) return;
    const now = this._ctx.currentTime;
    // (Re)anchor the cursor on first use or after an underrun / large drift.
    if (this._cursor < now + 0.001 || this._cursor - now > MAX_LATENCY) {
      this._cursor = now + SCHEDULE_LEAD;
    }
    const src = this._ctx.createBufferSource();
    src.buffer = buffer;
    src.connect(this._gain);
    src.start(this._cursor);
    this._cursor += buffer.duration;
  }

  private _teardownDecoder(): void {
    if (this._decoder) {
      try { if (this._decoder.state !== 'closed') this._decoder.close(); } catch { /* ignore */ }
      this._decoder = null;
    }
  }
}

// ─── G.711 expansion tables ───────────────────────────────────────────────

/**
 * Build a 256-entry lookup table mapping a companded G.711 byte to a Float32
 * sample in [-1, 1). `muLaw` selects µ-law (PCMU); otherwise A-law (PCMA).
 */
export function buildG711Table(muLaw: boolean): Float32Array {
  const table = new Float32Array(256);
  for (let i = 0; i < 256; i++) {
    const pcm = muLaw ? mulawExpand(i) : alawExpand(i);
    table[i] = Math.max(-1, Math.min(1, pcm / 32768));
  }
  return table;
}

/** ITU-T G.711 µ-law expansion to a 16-bit linear sample. */
export function mulawExpand(u: number): number {
  u = ~u & 0xff;
  let t = ((u & 0x0f) << 3) + 0x84;
  t <<= (u & 0x70) >> 4;
  return (u & 0x80) ? 0x84 - t : t - 0x84;
}

/** ITU-T G.711 A-law expansion to a 16-bit linear sample. */
export function alawExpand(a: number): number {
  a ^= 0x55;
  let t = (a & 0x0f) << 4;
  const seg = (a & 0x70) >> 4;
  if (seg === 0) {
    t += 8;
  } else if (seg === 1) {
    t += 0x108;
  } else {
    t += 0x108;
    t <<= seg - 1;
  }
  return (a & 0x80) ? t : -t;
}
