import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import HomeAssistantSettings from '$lib/components/HomeAssistantSettings.svelte';
import { i18nState } from '$lib/i18n';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

describe('Home Assistant publication settings', () => {
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
    await fireEvent.click(view.getByLabelText('Enable Home Assistant publication'));
    await fireEvent.input(view.getByPlaceholderText('Search by name or stream ID'), { target: { value: 'Camera 100' } });
    await fireEvent.click(view.getByText('Publish matching streams'));
    await fireEvent.click(view.getByText('Save Settings'));
    await waitFor(() => expect(fetch.mock.calls.some(([, options]) => options.method === 'PUT')).toBe(true));
    const call = fetch.mock.calls.find(([, options]) => options.method === 'PUT');
    const body = JSON.parse(call![1].body);
    expect(body.enabled).toBe(true);
    expect(body.streams).toEqual([{ stream_id: 'cam-100', app_name: 'live' }]);
    expect(body.instance_id).toBeUndefined();
  });
});
