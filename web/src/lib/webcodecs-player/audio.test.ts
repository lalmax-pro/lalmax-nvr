import { describe, it, expect } from 'vitest';
import { mulawExpand, alawExpand, buildG711Table } from './audio';
import {
  decodeAudioCodecInfo,
  decodeAudioFrame,
  AudioCodecId,
  MsgType,
} from './protocol';

// ─── Wire helpers (mirror the Go encoder) ──────────────────────────────────

function encodeAudioCodecInfo(
  codec: number,
  sampleRate: number,
  channels: number,
  config?: Uint8Array,
): ArrayBuffer {
  const cfg = config ?? new Uint8Array(0);
  const buf = new ArrayBuffer(9 + cfg.length);
  const dv = new DataView(buf);
  dv.setUint8(0, MsgType.AudioCodecInfo);
  dv.setUint8(1, codec);
  dv.setUint32(2, sampleRate);
  dv.setUint8(6, channels);
  dv.setUint16(7, cfg.length);
  new Uint8Array(buf, 9).set(cfg);
  return buf;
}

function encodeAudioFrame(pts: number, data: Uint8Array): ArrayBuffer {
  const buf = new ArrayBuffer(9 + data.length);
  const dv = new DataView(buf);
  dv.setUint8(0, MsgType.AudioFrame);
  dv.setBigInt64(1, BigInt(pts));
  new Uint8Array(buf, 9).set(data);
  return buf;
}

describe('decodeAudioCodecInfo', () => {
  it('decodes AAC with ASC config', () => {
    const asc = new Uint8Array([0x12, 0x10]);
    const aci = decodeAudioCodecInfo(encodeAudioCodecInfo(AudioCodecId.AAC, 44100, 2, asc));
    expect(aci.codec).toBe(AudioCodecId.AAC);
    expect(aci.sampleRate).toBe(44100);
    expect(aci.channels).toBe(2);
    expect(Array.from(aci.config!)).toEqual([0x12, 0x10]);
  });

  it('decodes G.711 with no config', () => {
    const aci = decodeAudioCodecInfo(encodeAudioCodecInfo(AudioCodecId.G711A, 8000, 1));
    expect(aci.codec).toBe(AudioCodecId.G711A);
    expect(aci.sampleRate).toBe(8000);
    expect(aci.channels).toBe(1);
    expect(aci.config).toBeUndefined();
  });

  it('throws on wrong message type', () => {
    const buf = encodeAudioCodecInfo(AudioCodecId.AAC, 44100, 2);
    new DataView(buf).setUint8(0, 0x02);
    expect(() => decodeAudioCodecInfo(buf)).toThrow();
  });
});

describe('decodeAudioFrame', () => {
  it('decodes pts and payload', () => {
    const af = decodeAudioFrame(encodeAudioFrame(123456, new Uint8Array([1, 2, 3, 4])));
    expect(af.pts).toBe(123456);
    expect(Array.from(af.data)).toEqual([1, 2, 3, 4]);
  });

  it('handles empty payload', () => {
    const af = decodeAudioFrame(encodeAudioFrame(0, new Uint8Array(0)));
    expect(af.data.length).toBe(0);
  });
});

describe('G.711 expansion', () => {
  it('µ-law: silence byte maps near zero', () => {
    // 0xFF is the µ-law encoding of 0; expands to a tiny magnitude.
    expect(Math.abs(mulawExpand(0xff))).toBeLessThan(10);
  });

  it('A-law: 0xD5 maps near zero', () => {
    // 0xD5 (0x55 ^ 0x80) is the A-law encoding nearest zero.
    expect(Math.abs(alawExpand(0xd5))).toBeLessThan(20);
  });

  it('µ-law is monotonic across the positive half', () => {
    // Decoded magnitude should grow as the code moves away from the zero point.
    expect(mulawExpand(0x80)).toBeGreaterThan(mulawExpand(0xb0));
  });

  it('builds a 256-entry float table in [-1, 1]', () => {
    const table = buildG711Table(false);
    expect(table.length).toBe(256);
    for (const v of table) {
      expect(v).toBeGreaterThanOrEqual(-1);
      expect(v).toBeLessThanOrEqual(1);
    }
  });
});
