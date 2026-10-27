import { afterEach, expect, it, vi } from 'vitest';
import { transcodeJobAction, saveTranscodeJob } from '$lib/api/transcoding';
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
