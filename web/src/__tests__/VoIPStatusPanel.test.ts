import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { describe, it, expect, afterEach, vi } from 'vitest';
import VoIPStatusPanel from '$lib/components/VoIPStatusPanel.svelte';
import { i18nState } from '$lib/i18n';
const mocks = vi.hoisted(() => ({ status: vi.fn(), hangup: vi.fn(), history: vi.fn() }));
vi.mock('$lib/api', () => ({ getVoIPStatus: mocks.status, hangupVoIPCall: mocks.hangup, getVoIPCallHistory: mocks.history }));
afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  mocks.history.mockResolvedValue({ items: [], total: 0, limit: 20, offset: 0 });
});
describe('VoIP call management', () => {
  it('shows registered endpoints and held calls, then refreshes after hangup', async () => {
    i18nState.currentLang = 'en';
    const initial = {
      enabled: true,
      endpoints: [
        {
          user: '1001',
          user_agent: 'Linphone',
          remote_addr: '127.0.0.1:5070',
          transport: 'TCP',
          expires_at: '2026-10-09T10:00:00Z',
        },
      ],
      calls: [
        {
          call_id: 'call-1',
          stream_id: 'voip_1001_call1',
          from_user: '1001',
          to_user: 'nvr',
          state: 'established',
          held: true,
          duration_seconds: 42,
          audio_codec: 'OPUS',
          video_codec: 'H264',
          transport: 'TCP',
          remote_addr: '127.0.0.1:5070',
        },
      ],
    };
    mocks.status
      .mockResolvedValueOnce(initial)
      .mockResolvedValue({ enabled: true, endpoints: initial.endpoints, calls: [] });
    mocks.hangup.mockResolvedValue({});
    const view = render(VoIPStatusPanel);
    await waitFor(() => expect(view.getByText('OPUS / H264')).toBeTruthy());
    expect(view.getByText('42s')).toBeTruthy();
    expect(view.getByRole('link', { name: '1001 → nvr' }).getAttribute('href')).toBe('#/streams/voip_1001_call1');
    await fireEvent.click(view.getByRole('button', { name: /call-1/ }));
    await waitFor(() => expect(mocks.hangup).toHaveBeenCalledWith('call-1'));
    await waitFor(() => expect(view.queryByText('OPUS / H264')).toBeNull());
  });
  it('keeps the current call visible and reports a hangup failure', async () => {
    i18nState.currentLang = 'en';
    mocks.status.mockResolvedValue({
      enabled: true,
      endpoints: [],
      calls: [
        {
          call_id: 'call-2',
          stream_id: 'voip_two',
          from_user: '1002',
          to_user: 'nvr',
          state: 'established',
          duration_seconds: 4,
          transport: 'UDP',
        },
      ],
    });
    mocks.hangup.mockRejectedValue(new Error('Call cannot be ended'));
    const view = render(VoIPStatusPanel);
    await waitFor(() => expect(view.getByRole('link', { name: '1002 → nvr' })).toBeTruthy());
    await fireEvent.click(view.getByRole('button', { name: /call-2/ }));
    await waitFor(() => expect(view.getByRole('alert').textContent).toContain('Call cannot be ended'));
    expect(view.getByRole('link', { name: '1002 → nvr' })).toBeTruthy();
  });
  it('offers browser talk for an established inbound call', async () => {
    i18nState.currentLang = 'en';
    mocks.status.mockResolvedValue({
      enabled: true,
      endpoints: [],
      calls: [
        {
          call_id: 'inbound-1',
          from_user: 'door',
          to_user: 'nvr',
          direction: 'inbound',
          state: 'established',
          talk_available: true,
          browser_ready: false,
          duration_seconds: 4,
          transport: 'UDP',
        },
      ],
    });
    const view = render(VoIPStatusPanel);
    await waitFor(() => expect(view.getByRole('button', { name: 'Join talk' })).toBeTruthy());
  });
});
