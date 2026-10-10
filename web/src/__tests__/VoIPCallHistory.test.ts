import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import VoIPCallHistory from '$lib/components/VoIPCallHistory.svelte';
import { i18nState } from '$lib/i18n';

const mocks = vi.hoisted(() => ({ history: vi.fn() }));
vi.mock('$lib/api', () => ({ getVoIPCallHistory: mocks.history }));

afterEach(() => { cleanup(); vi.clearAllMocks(); });

describe('VoIP call history', () => {
  it('shows persisted calls and paginates', async () => {
    i18nState.currentLang = 'en';
    mocks.history
      .mockResolvedValueOnce({ items: [{ call_id: 'c1', direction: 'inbound', from_user: 'door', to_user: 'nvr', outcome: 'missed', started_at: '2026-10-10T01:00:00Z', ended_at: '2026-10-10T01:00:30Z', duration_seconds: 0, failure_reason: 'no answer', transport: 'tls' }], total: 21, limit: 20, offset: 0 })
      .mockResolvedValueOnce({ items: [{ call_id: 'c2', direction: 'outbound', from_user: 'nvr', to_user: '6002', outcome: 'completed', started_at: '2026-10-10T02:00:00Z', answered_at: '2026-10-10T02:00:01Z', ended_at: '2026-10-10T02:00:11Z', duration_seconds: 10, stream_id: 'voip_6002_c2' }], total: 21, limit: 20, offset: 20 });
    const view = render(VoIPCallHistory);
    await waitFor(() => expect(view.getByText('Missed')).toBeTruthy());
    expect(view.getByLabelText('door → nvr')).toBeTruthy();
    expect(view.getByText('no answer').textContent).toBeTruthy();
    await fireEvent.click(view.getByRole('button', { name: 'Next' }));
    await waitFor(() => expect(mocks.history).toHaveBeenLastCalledWith(20, 20, expect.any(AbortSignal)));
    await waitFor(() => expect(view.getByRole('link', { name: 'View live stream' }).getAttribute('href')).toBe('#/streams/voip_6002_c2'));
  });
});
