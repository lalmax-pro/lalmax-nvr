import { describe, expect, it } from 'vitest';
import { mediaOffsetToWallMs, wallMsToMediaOffset, vodClips } from '../playback';
import type { Recording } from '$lib/api';

const rec = (id: string, start: string, end: string, duration: number, format: Recording['format'] = 'h264'): Recording => ({
  id,
  camera_id: 'cam',
  file_path: '',
  format,
  started_at: start,
  ended_at: end,
  duration,
  file_size: 1,
  frame_count: 1,
  merged: false,
});

describe('playback wall-clock mapping', () => {
  it('maps media time into a gap-aware wall clock', () => {
    const clips = [
      rec('a', '2026-09-02T00:00:00Z', '2026-09-02T00:10:00Z', 600),
      rec('b', '2026-09-02T00:20:00Z', '2026-09-02T00:30:00Z', 600),
    ];
    const wall = mediaOffsetToWallMs(clips, 650 * 1000);
    expect(new Date(wall).toISOString()).toBe('2026-09-02T00:20:50.000Z');
  });

  it('maps a wall-clock click onto the nearest clip', () => {
    const clips = [
      rec('a', '2026-09-02T00:00:00Z', '2026-09-02T00:10:00Z', 600),
      rec('b', '2026-09-02T00:20:00Z', '2026-09-02T00:30:00Z', 600),
    ];
    const mapped = wallMsToMediaOffset(clips, Date.parse('2026-09-02T00:25:00Z'));
    expect(mapped?.clip.id).toBe('b');
    expect(mapped?.offsetSec).toBe(300);
  });

  it('filters VOD clips', () => {
    const clips = vodClips([
      rec('a', '2026-09-02T00:00:00Z', '2026-09-02T00:01:00Z', 60, 'h264'),
      rec('b', '2026-09-02T00:01:00Z', '2026-09-02T00:02:00Z', 60, 'mjpeg'),
    ]);
    expect(clips.map(c => c.id)).toEqual(['a']);
  });
});
