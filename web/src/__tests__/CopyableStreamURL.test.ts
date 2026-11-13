import { cleanup, fireEvent, render, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import CopyableStreamURL from '$lib/components/CopyableStreamURL.svelte';
import { copyTextWithFallback } from '$lib/clipboard';
import { showToast } from '$lib/toast';

vi.mock('$lib/clipboard', () => ({ copyTextWithFallback: vi.fn() }));
vi.mock('$lib/toast', () => ({ showToast: vi.fn() }));

const copy = vi.mocked(copyTextWithFallback);
const toast = vi.mocked(showToast);

describe('stream URL rows', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    copy.mockResolvedValue(true);
  });

  afterEach(() => cleanup());

  it('copies HLS playback URL on row click without opening a link', async () => {
    const url = 'http://127.0.0.1/hls/cam.m3u8?token=test';
    const { container, getByRole, getByText } = render(CopyableStreamURL, { props: { protocol: 'HLS', url } });
    expect(getByText(url)).toBeTruthy();
    expect(container.querySelector('a')).toBeNull();
    const button = getByRole('button');
    expect(button.getAttribute('type')).toBe('button');
    await fireEvent.click(button);
    await waitFor(() => {
      expect(copy).toHaveBeenCalledTimes(1);
      expect(copy).toHaveBeenCalledWith(url);
      expect(toast).toHaveBeenCalledWith(expect.any(String), 'success');
    });
  });

  it('copies the complete RTMP publish URL including query credentials', async () => {
    const url = 'rtmp://127.0.0.1/live/stream0?stream_key=secret';
    const { getByRole } = render(CopyableStreamURL, { props: { protocol: 'RTMP', url } });
    await fireEvent.click(getByRole('button'));
    await waitFor(() => {
      expect(copy).toHaveBeenCalledTimes(1);
      expect(copy).toHaveBeenCalledWith(url);
    });
  });

  it('does not report copied when the clipboard fallback requires manual copying', async () => {
    copy.mockResolvedValue(false);
    const { getByRole } = render(CopyableStreamURL, { props: { protocol: 'WHEP', url: 'http://example.test/webrtc' } });
    await fireEvent.click(getByRole('button'));
    await waitFor(() => expect(copy).toHaveBeenCalledOnce());
    expect(toast).not.toHaveBeenCalled();
  });

  it('reports an error when copying fails unexpectedly', async () => {
    copy.mockRejectedValue(new Error('unavailable'));
    const { getByRole } = render(CopyableStreamURL, { props: { protocol: 'RTSP', url: 'rtsp://example.test/live' } });
    await fireEvent.click(getByRole('button'));
    await waitFor(() => expect(toast).toHaveBeenCalledWith(expect.any(String), 'error'));
  });
});
