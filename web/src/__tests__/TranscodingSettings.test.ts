import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import TranscodingSettings from '$lib/components/TranscodingSettings.svelte';
import { i18nState } from '$lib/i18n';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it('does not probe on load and runs explicit detection while disabled', async () => {
  i18nState.currentLang = 'en';
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  const report = { backend: 'ffmpeg', profile_id: 'default_ffmpeg', checked_at: new Date().toISOString(), stale: false, can_enable: true, checks: [{ name: 'engine_roundtrip', status: 'not_tested', detail: 'Not validated' }] };
  const fetch = vi.fn().mockImplementation(async (_url: string, options: RequestInit) => ({ ok: true, json: async () => options.method === 'POST' ? report : { enabled: false, reports: [] } }));
  vi.stubGlobal('fetch', fetch);
  const view = render(TranscodingSettings);
  await waitFor(() => expect(view.getByText('Check transcoding capabilities')).toBeTruthy());
  expect(fetch.mock.calls.some(([, options]) => options.method === 'POST')).toBe(false);
  await fireEvent.click(view.getByText('Check transcoding capabilities'));
  await waitFor(() => expect(view.getByText('Template media self-test passed')).toBeTruthy());
  expect(view.getByText('Not tested')).toBeTruthy();
  const post = fetch.mock.calls.find(([, options]) => options.method === 'POST');
  expect(JSON.parse(post![1].body)).toEqual({ backend: 'ffmpeg' });
});

it('shows stale results without claiming readiness', async () => {
  i18nState.currentLang = 'en';
  vi.stubGlobal('sessionStorage', { getItem: () => null });
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, json: async () => ({ enabled: false, reports: [{ backend: 'ffmpeg', checked_at: new Date().toISOString(), stale: true, can_enable: false, checks: [] }] }) }));
  const view = render(TranscodingSettings);
  await waitFor(() => expect(view.getByText('Result expired or configuration changed. Run the check again.')).toBeTruthy());
  expect(view.queryByText('Template media self-test passed')).toBeNull();
});
