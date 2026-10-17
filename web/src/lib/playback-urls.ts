/**
 * Pick the URLs advertised by NVR's per-camera protocol endpoint.
 * Keeping the server-provided absolute WebRTC (WHEP) URL preserves the
 * configured public hostname, port and stream ID (including GB28181 colons).
 */
export interface PlaybackProtocol {
  Protocol: string;
  Available?: boolean;
  PlayURL?: string;
}

export interface PlaybackURLs {
  rtsp: string;
  webrtc: string;
}

export function selectPlaybackURLs(protocols: PlaybackProtocol[] | undefined): PlaybackURLs {
  function urlFor(protocol: string): string {
    return protocols?.find(
      (entry) => entry.Protocol?.toLowerCase() === protocol &&
        entry.Available !== false && Boolean(entry.PlayURL)
    )?.PlayURL || '';
  }
  return { rtsp: urlFor('rtsp'), webrtc: urlFor('webrtc') };
}
