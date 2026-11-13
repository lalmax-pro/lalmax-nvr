import { afterEach, expect, it, vi } from 'vitest';
import { createRelayTask } from '$lib/api/relay';

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it('creates a relay using logical source and transcode job, never the physical output ID', async () => {
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  const fetch = vi.fn().mockResolvedValue({ ok: true, status: 201, json: async () => ({
    id: 'relay-1', stream_id: 'door', transcode_job_id: 'low720', status: 'stopped',
  }) });
  vi.stubGlobal('fetch', fetch);
  await createRelayTask({
    stream_id: 'door', transcode_job_id: 'low720',
    target_url: 'rtmp://relay.example/live/door',
  });
  expect(fetch.mock.calls[0][0]).toBe('/api/relay/tasks');
  const [_, options] = fetch.mock.calls[0];
  const body = JSON.parse(options.body);
  expect(body).toEqual({
    stream_id: 'door', transcode_job_id: 'low720',
    target_url: 'rtmp://relay.example/live/door',
  });
  expect(JSON.stringify(body)).not.toContain('nvr_tc_');
});
