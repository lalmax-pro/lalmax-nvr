import { describe, expect, it } from 'vitest';
import { selectPlaybackURLs } from '$lib/playback-urls';

describe('selectPlaybackURLs', () => {
  it('retains the exact server-provided RTSP and LALMax WHEP addresses', () => {
    const id = 'device001:channel001';
    const rtsp = `rtsp://192.0.2.10:15544/live/${id}`;
    const webrtc = `http://192.0.2.10:12090/webrtc/whep?streamid=${id}&app_name=live`;
    expect(selectPlaybackURLs([
      { Protocol: 'rtsp', Available: true, PlayURL: rtsp },
      { Protocol: 'webrtc', Available: true, PlayURL: webrtc },
    ])).toEqual({ rtsp, webrtc });
  });

  it('preserves an available URL when the other protocol is unavailable', () => {
    expect(selectPlaybackURLs([
      { Protocol: 'rtsp', Available: false, PlayURL: 'rtsp://unavailable' },
      { Protocol: 'WebRTC', Available: true, PlayURL: 'http://example/whep' },
    ])).toEqual({ rtsp: '', webrtc: 'http://example/whep' });
  });

  it('handles empty, missing and unpublished protocol addresses', () => {
    expect(selectPlaybackURLs(undefined)).toEqual({ rtsp: '', webrtc: '' });
    expect(selectPlaybackURLs([
      { Protocol: 'rtsp', Available: true, PlayURL: '' },
      { Protocol: 'flv', Available: true, PlayURL: 'http://example/flv' },
    ])).toEqual({ rtsp: '', webrtc: '' });
  });
});
