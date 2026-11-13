import { afterEach, expect, it, vi } from 'vitest';
import { transcodeJobAction, saveTranscodeJob, listStreamTranscodeVariants, transcodePreviewFLVURL, updateTranscodeProfile, deleteTranscodeProfile, acquireTranscodeDemandLease, renewTranscodeDemandLease, releaseTranscodeDemandLease } from '$lib/api/transcoding';
import type { TranscodeJob } from '$lib/api/transcoding';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const job: TranscodeJob = { id: 'camera-job', source: {engine_id: 'embedded', stream_id: 'camera', app_name: 'live'}, profile_id: 'h264', profile_revision: 1, enabled: false, run_policy: 'manual', generation: 3 };
it('handles 204 stop and carries the expected generation', async () => {
 vi.stubGlobal('sessionStorage', { getItem: () => null });
 const json = vi.fn();
 const fetch = vi.fn().mockResolvedValue({ok: true, status: 204, json});
 vi.stubGlobal('fetch', fetch);
 await transcodeJobAction(job, 'stop');
 expect(fetch.mock.calls[0][0]).toBe('/api/transcoding/jobs/camera-job/stop?generation=3');
 expect(json).not.toHaveBeenCalled();
});
it('creates a draft without converting it to an enabled job', async () => {
 vi.stubGlobal('sessionStorage', { getItem: () => null });
 const fetch = vi.fn().mockResolvedValue({ok: true, status: 201, json: async () => job});
 vi.stubGlobal('fetch', fetch);
 await saveTranscodeJob({...job, generation: 0});
 expect(fetch.mock.calls[0][0]).toBe('/api/transcoding/jobs');
 const options = fetch.mock.calls[0][1];
 expect(options.method).toBe('POST');
 expect(JSON.parse(options.body).enabled).toBe(false);
});

it('queries derived outputs by exact source and keeps physical URLs out of preview', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  const fetch = vi.fn().mockResolvedValue({ok: true, status: 200, json: async () => ({variants: [{id: 'v1',job_id:'job',source_id:'site/area/camera',state:'running'}]})});
  vi.stubGlobal('fetch', fetch);
  const source = 'site/area/camera';
  const variants = await listStreamTranscodeVariants(source);
  expect(variants.variants[0].source_id).toBe(source);
  expect(fetch.mock.calls[0][0]).toBe('/api/streams/site%2Farea%2Fcamera/variants');
  expect(transcodePreviewFLVURL(source, 'job')).toBe('/api/streams/site%2Farea%2Fcamera/variants/job/stream.flv');
});

it('edits or deletes templates with an expected revision', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  const fetch = vi.fn().mockResolvedValue({ok:true,status:200,json:async () => ({})});
  vi.stubGlobal('fetch', fetch);
  const profile = {
    id:'video',name:'H.264',revision:7,backend:'ffmpeg' as const,
    video:{codec:'h264',encoder:'libx264',max_width:1280,max_height:720,fps:15,bitrate_kbps:1500,gop_frames:30},
    audio:{codec:'aac',sample_rate:48000,channels:1,bitrate_kbps:64},
  };
  await updateTranscodeProfile(profile);
  expect(fetch.mock.calls[0][0]).toBe('/api/transcoding/profiles/video');
  expect(fetch.mock.calls[0][1].method).toBe('PATCH');
  expect(JSON.parse(fetch.mock.calls[0][1].body).revision).toBe(7);
  fetch.mockResolvedValueOnce({ok:true,status:204});
  await deleteTranscodeProfile(profile);
  expect(fetch.mock.calls[1][0]).toBe('/api/transcoding/profiles/video?revision=7');
  expect(fetch.mock.calls[1][1].method).toBe('DELETE');
});

it('keeps independent demand tokens across start, renewal and final release', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  const token = 'a-runtime-token';
  const fetch = vi.fn().mockImplementation(async (url: string, opts: RequestInit) => ({
    ok: true, status: url.endsWith('/leases/release') ? 204 : opts.method === 'POST' && url.endsWith('/leases?generation=3&wait_ms=20000') ? 201 : 200,
    json: async () => url.endsWith('/renew')
      ? {token,job_id:'job',generation:3,expires_at:'2026-10-29T00:00:00Z'}
      : {lease:{token,job_id:'job',generation:3,expires_at:'2026-10-29T00:00:00Z'},runtime:{state:'running'}},
  }));
  vi.stubGlobal('fetch', fetch);
  const result = await acquireTranscodeDemandLease('job', 3);
  expect(result.runtime.state).toBe('running');
  expect(fetch.mock.calls[0][0]).toBe('/api/transcoding/jobs/job/leases?generation=3&wait_ms=20000');
  await renewTranscodeDemandLease(token);
  expect(fetch.mock.calls[1][0]).toBe('/api/transcoding/leases/renew');
  await releaseTranscodeDemandLease(token);
  expect(fetch.mock.calls[2][0]).toBe('/api/transcoding/leases/release');
  expect(fetch.mock.calls[2][1].method).toBe('POST');
  expect(JSON.parse(fetch.mock.calls[2][1].body)).toEqual({token});
  expect(JSON.parse(fetch.mock.calls[1][1].body)).toEqual({token});
});

it('rejects unbounded first-subscriber waits before any network request', async () => {
  const fetch = vi.fn();
  vi.stubGlobal('fetch', fetch);
  expect(() => acquireTranscodeDemandLease('job', 1, 20001)).toThrow();
  expect(() => acquireTranscodeDemandLease('job', 1, -1)).toThrow();
  expect(fetch).not.toHaveBeenCalled();
});
