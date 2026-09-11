import type { Recording } from '$lib/api';

export type TimelineClip = {
  id: string;
  started_at: string;
  ended_at: string;
  duration: number;
  format?: string;
};

export function dayStartFromISO(dateStr: string): Date {
  const d = new Date(dateStr);
  d.setHours(0, 0, 0, 0);
  return d;
}

export function wallMsToHour(wallMs: number, dayStart: Date): number {
  return (wallMs - dayStart.getTime()) / 3600000;
}

export function hourToWallMs(hour: number, dayStart: Date): number {
  return dayStart.getTime() + hour * 3600000;
}

export function mediaOffsetToWallMs(clips: TimelineClip[], mediaMs: number): number {
  let acc = 0;
  for (const clip of clips) {
    const dur = Math.max(0, (clip.duration || 0) * 1000);
    if (mediaMs <= acc + dur) {
      return new Date(clip.started_at).getTime() + (mediaMs - acc);
    }
    acc += dur;
  }
  if (clips.length === 0) return 0;
  return new Date(clips[clips.length - 1].ended_at).getTime();
}

export function wallMsToMediaOffset(clips: TimelineClip[], wallMs: number): { clip: TimelineClip; offsetSec: number; mediaMs: number } | null {
  let acc = 0;
  let nearest: { clip: TimelineClip; offsetSec: number; mediaMs: number; dist: number } | null = null;
  for (const clip of clips) {
    const start = new Date(clip.started_at).getTime();
    const end = new Date(clip.ended_at).getTime();
    const durMs = Math.max(0, end - start);
    if (wallMs >= start && wallMs <= end) {
      return { clip, offsetSec: (wallMs - start) / 1000, mediaMs: acc + (wallMs - start) };
    }
    const distStart = Math.abs(wallMs - start);
    const distEnd = Math.abs(wallMs - end);
    if (!nearest || distStart < nearest.dist) {
      nearest = { clip, offsetSec: 0, mediaMs: acc, dist: distStart };
    }
    if (distEnd < nearest.dist) {
      nearest = { clip, offsetSec: durMs / 1000, mediaMs: acc + durMs, dist: distEnd };
    }
    acc += durMs;
  }
  return nearest ? { clip: nearest.clip, offsetSec: nearest.offsetSec, mediaMs: nearest.mediaMs } : null;
}

export function vodClips(recordings: Recording[]): TimelineClip[] {
  return recordings.filter(r => r.format === 'h264' || r.format === 'h265' || r.format === 'timelapse');
}
