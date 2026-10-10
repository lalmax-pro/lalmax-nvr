import { render, fireEvent, cleanup, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import VoIPDTMFPad from '$lib/components/VoIPDTMFPad.svelte';

const api = vi.hoisted(() => ({ send: vi.fn() }));
vi.mock('$lib/api/settings', () => ({ sendVoIPDTMF: api.send }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe('VoIP DTMF keypad', () => {
  it('sends the selected key to the current call', async () => {
    api.send.mockResolvedValue({ status: 'sent' });
    const view = render(VoIPDTMFPad, { props: { callId: 'sip-call' } });
    await fireEvent.click(view.getByRole('button', { name: 'Send DTMF #' }));
    await waitFor(() => expect(api.send).toHaveBeenCalledWith('sip-call', '#'));
  });

  it('shows a negotiation error from the SIP peer', async () => {
    api.send.mockRejectedValue(new Error('telephone-event was not negotiated'));
    const view = render(VoIPDTMFPad, { props: { callId: 'sip-call' } });
    await fireEvent.click(view.getByRole('button', { name: 'Send DTMF 5' }));
    await waitFor(() => expect(view.getByRole('alert').textContent).toContain('telephone-event'));
  });
});
