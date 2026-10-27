import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import TranscodeJobs from '$lib/components/TranscodeJobs.svelte';
import { i18nState } from '$lib/i18n';

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
const profile = {id: 'h264_720p', name: '720p', revision: 2, backend: 'ffmpeg'};
it('creates a GStreamer template with its matching encoder', async () => {
 i18nState.currentLang = 'en';
 vi.stubGlobal('sessionStorage', {getItem: () => null});
 const fetch = vi.fn().mockImplementation(async (url: string, options: RequestInit) => ({ok: true, status: 200, json: async () => options.method === 'POST' ? JSON.parse(options.body as string) : url.endsWith('/profiles') ? {profiles: []} : {jobs: [], variants: [], runtime_available: true}}));
 vi.stubGlobal('fetch', fetch);
 const view = render(TranscodeJobs);
 await waitFor(() => expect(view.getByLabelText('Backend')).toBeTruthy());
 await fireEvent.change(view.getByLabelText('Backend'), {target: {value: 'gstreamer'}});
 await fireEvent.click(view.getByText('Add H.264 720p profile'));
 await waitFor(() => expect(fetch.mock.calls.some(([,o]) => o.method === 'POST')).toBe(true));
 const [,options] = fetch.mock.calls.find(([,o]) => o.method === 'POST')!;
 expect(JSON.parse(options.body)).toMatchObject({id: 'gst_h264_720p', backend: 'gstreamer', revision: 0, video: {encoder: 'x264enc'}});
});
it('creates only a disabled manual draft with the selected profile revision', async () => {
 i18nState.currentLang = 'en';
 vi.stubGlobal('sessionStorage', {getItem: () => null});
 const fetch = vi.fn().mockImplementation(async (url: string, options: RequestInit) => ({ok: true, status: options.method === 'POST' ? 201 : 200, json: async () => options.method === 'POST' ? JSON.parse(options.body as string) : url.endsWith('/profiles') ? {profiles: [profile]} : {jobs: [], runtime_available: true}}));
 vi.stubGlobal('fetch', fetch);
 const view = render(TranscodeJobs);
 await waitFor(() => expect(view.getByLabelText('Job ID')).toBeTruthy());
 await fireEvent.input(view.getByLabelText('Job ID'), {target: {value: 'door-preview'}});
 await fireEvent.input(view.getByLabelText('Existing source stream ID'), {target: {value: 'door'}});
 await fireEvent.change(view.getByLabelText('Profile'), {target: {value: profile.id}});
 await fireEvent.submit(view.getByText('Create disabled draft').closest('form')!);
 await waitFor(() => expect(fetch.mock.calls.some(([, o]) => o.method === 'POST')).toBe(true));
 const [, options] = fetch.mock.calls.find(([, o]) => o.method === 'POST')!;
 const body = JSON.parse(options.body);
 expect(body).toMatchObject({id: 'door-preview', enabled: false, run_policy: 'manual', generation: 0, profile_revision: 2, source: {stream_id: 'door', engine_id: 'embedded', app_name: 'live'}});
});
it('does not delete a job when confirmation is canceled', async () => {
 i18nState.currentLang = 'en';
 vi.stubGlobal('sessionStorage', {getItem: () => null});
 const job = {id: 'test', source: {stream_id: 'door'}, profile_id: 'h264_720p', enabled: false, generation: 1, run_policy: 'manual'};
 const fetch = vi.fn().mockImplementation(async (url: string) => ({ok: true, status: 200, json: async () => url.endsWith('/profiles') ? {profiles: [profile]} : {jobs: [job], runtime_available: true}}));
 vi.stubGlobal('fetch', fetch);
 vi.spyOn(window, 'confirm').mockReturnValue(false);
 const view = render(TranscodeJobs);
 await waitFor(() => expect(view.getByText('Delete')).toBeTruthy());
 await fireEvent.click(view.getByText('Delete'));
 expect(fetch.mock.calls.some(([, o]) => o?.method === 'DELETE')).toBe(false);
 expect((view.getByText('Start') as HTMLButtonElement).disabled).toBe(true);
});
