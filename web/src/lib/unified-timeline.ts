import { getRecordingsTimeline, queryDeviceRecords, searchONVIFRecordings, normalizeProtocol } from '$lib/api';
import type { Camera, UnifiedTimelineClip } from '$lib/api';

export async function loadUnifiedTimeline(camera: Camera, start: string, end: string): Promise<UnifiedTimelineClip[]> {
  const clips: UnifiedTimelineClip[] = [];
  const proto = normalizeProtocol(camera.protocol);

  try {
    const nvr = await getRecordingsTimeline(camera.id, start, end);
    for (const e of nvr) {
      clips.push({
        id: e.id,
        camera_id: e.camera_id,
        started_at: e.started_at,
        ended_at: e.ended_at,
        duration: e.duration,
        format: e.format,
        source: 'nvr',
        merged: e.merged,
        gap_reason: e.gap_reason,
        locked: e.locked,
      });
    }
  } catch {
    // NVR disk query is the baseline; ignore empty cameras.
  }

  if (proto === 'gb28181') {
    try {
      const recs = await queryDeviceRecords({
        device_id: camera.serial_number || camera.id,
        channel_id: camera.id,
        start_time: start,
        end_time: end,
      });
      for (const day of recs.data || []) {
        for (const item of day.items || []) {
          const started = new Date(item.start * 1000).toISOString();
          const ended = new Date(item.end * 1000).toISOString();
          clips.push({
            id: `gb-${item.start}`,
            camera_id: camera.id,
            started_at: started,
            ended_at: ended,
            duration: Math.max(0, item.end - item.start),
            source: 'gb',
            token: `${item.start}`,
          });
        }
      }
    } catch {
      // device may be offline
    }
  }

  if (proto === 'onvif') {
    try {
      const res = await searchONVIFRecordings(camera.id, { start_time: start, end_time: end, max_results: 200 });
      for (const seg of res.segments || []) {
        const started = new Date(seg.start_time).getTime();
        const ended = new Date(seg.end_time).getTime();
          clips.push({
            id: `onvif-${seg.token || started}`,
            camera_id: camera.id,
            started_at: seg.start_time,
            ended_at: seg.end_time,
            duration: Number.isFinite(ended - started) ? (ended - started) / 1000 : 0,
            source: 'onvif',
            token: seg.token || seg.recording_token,
          });
      }
    } catch {
      // Profile G not available
    }
  }

  clips.sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime());
  return clips;
}
