import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import HomeAssistantSettings from '$lib/components/HomeAssistantSettings.svelte';
import { i18nState, t } from '$lib/i18n';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('Home Assistant publication settings', () => {
  it('selects only an output belonging to the source without changing identity or starting it', async () => {
    i18nState.currentLang = 'en';
    vi.stubGlobal('sessionStorage', {getItem: () => null});
    const cfg = {instance_id: 'stable', enabled: true, rtsp_base_url: '', streams: [{stream_id: 'door', app_name: 'live'}]};
    const fetch = vi.fn().mockImplementation(async (url: string, options: RequestInit) => {
      if (options.method === 'PUT') return {ok: true, json: async () => ({...cfg, ...JSON.parse(options.body as string)})};
      if (url.includes('/settings/')) return {ok: true, json: async () => cfg};
      if (url.includes('/transcoding/jobs')) return {ok: true, json: async () => ({jobs: [{id: 'door-low', profile_id: '720p', source: {stream_id: 'door'}}, {id: 'other-low', profile_id: '720p', source: {stream_id: 'other'}}]})};
      return {ok: true, json: async () => ({streams: [{stream_id: 'door', name: 'Door', active: true}], total: 1})};
    });
    vi.stubGlobal('fetch', fetch);
    const view = render(HomeAssistantSettings);
    await waitFor(() => expect(view.getByText('Load transcoding outputs')).toBeTruthy());
    await fireEvent.click(view.getByText('Load transcoding outputs'));
    await waitFor(() => expect(view.getByLabelText('Playback output for Door')).toBeTruthy());
    expect(view.queryByText('other-low · 720p')).toBeNull();
    await fireEvent.change(view.getByLabelText('Playback output for Door'), {target: {value: 'door-low'}});
    await fireEvent.click(view.getByText(t('settings.save')));
    await waitFor(() => expect(fetch.mock.calls.some(([, o]) => o.method === 'PUT')).toBe(true));
    const [, options] = fetch.mock.calls.find(([, o]) => o.method === 'PUT')!;
    expect(JSON.parse(options.body).streams).toEqual([{stream_id: 'door', app_name: 'live', transcode_job_id: 'door-low'}]);
    expect(fetch.mock.calls.some(([url]) => url.includes('/start'))).toBe(false);
  });
  it('revokes prior preload when a publication selects a different output', async () => {
    i18nState.currentLang = 'en';
    vi.stubGlobal('sessionStorage', { getItem: () => null });
    const cfg = {
      instance_id: 'stable', enabled: true, rtsp_base_url: '',
      streams: [{ stream_id: 'door', app_name: 'live', transcode_job_id: 'door-old', preload_transcode: true }],
    };
    const fetch = vi.fn().mockImplementation(async (url: string, options: RequestInit) => {
      if (options.method === 'PUT') return { ok: true, json: async () => ({ ...cfg, ...JSON.parse(options.body as string) }) };
      if (url.includes('/settings/')) return { ok: true, json: async () => cfg };
      if (url.includes('/transcoding/jobs')) return {
        ok: true,
        json: async () => ({ jobs: [
          { id: 'door-old', profile_id: '720p', run_policy: 'on_demand', source: { stream_id: 'door' } },
          { id: 'door-new', profile_id: '720p', run_policy: 'on_demand', source: { stream_id: 'door' } },
        ] }),
      };
      return { ok: true, json: async () => ({
        streams: [{ stream_id: 'door', name: 'Door', active: true }], total: 1,
      }) };
    });
    vi.stubGlobal('fetch', fetch);
    const view = render(HomeAssistantSettings);
    await waitFor(() => expect(view.getByText('Load transcoding outputs')).toBeTruthy());
    await fireEvent.click(view.getByText('Load transcoding outputs'));
    await waitFor(() => expect(view.getByLabelText('Playback output for Door')).toBeTruthy());
    await fireEvent.change(view.getByLabelText('Playback output for Door'), { target: { value: 'door-new' } });
    await fireEvent.click(view.getByText(t('settings.save')));
    await waitFor(() => expect(fetch.mock.calls.some(([, o]) => o.method === 'PUT')).toBe(true));
    const [, options] = fetch.mock.calls.find(([, o]) => o.method === 'PUT')!;
    expect(JSON.parse(options.body).streams).toEqual([{
      stream_id: 'door', app_name: 'live', transcode_job_id: 'door-new',
    }]);
    expect(fetch.mock.calls.some(([url]) => url.includes('/start'))).toBe(false);
  });
  it('loads all pages, selects matching streams and saves without the instance identity', async () => {
    i18nState.currentLang = 'en';
    vi.stubGlobal('sessionStorage', { getItem: () => null });
    const cfg = { instance_id: 'stable-id', enabled: false, rtsp_base_url: '', streams: [] };
    const fetch = vi.fn().mockImplementation(async (url: string, options: RequestInit) => {
      if (options.method === 'PUT') return { ok: true, json: async () => ({ ...cfg, ...JSON.parse(options.body as string) }) };
      if (url.includes('/settings/')) return { ok: true, json: async () => cfg };
      const offset = Number(new URL(url, 'http://localhost').searchParams.get('offset'));
      const streams = Array.from({ length: offset ? 1 : 100 }, (_, i) => ({ stream_id: `cam-${offset + i}`, name: `Camera ${offset + i}`, active: true }));
      return { ok: true, json: async () => ({ streams, total: 101, limit: 100, offset }) };
    });
    vi.stubGlobal('fetch', fetch);
    const view = render(HomeAssistantSettings);
    await waitFor(() => expect(view.getByLabelText('Camera 100')).toBeTruthy());
    await fireEvent.click(view.getByLabelText(t('ha.enabled')));
    await fireEvent.input(view.getByPlaceholderText(t('ha.search')), { target: { value: 'Camera 100' } });
    await fireEvent.click(view.getByText(t('ha.selectVisible')));
    await fireEvent.click(view.getByText(t('settings.save')));
    await waitFor(() => expect(fetch.mock.calls.some(([, options]) => options.method === 'PUT')).toBe(true));
    const call = fetch.mock.calls.find(([, options]) => options.method === 'PUT');
    const body = JSON.parse(call![1].body);
    expect(body.enabled).toBe(true);
    expect(body.streams).toEqual([{ stream_id: 'cam-100', app_name: 'live' }]);
    expect(body.instance_id).toBeUndefined();
  });
});
