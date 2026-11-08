# LiveKit Track to lal Group adapter

This package converts authorized H.264 or Opus RTP into lal custom-publisher
frames. It is independent of LiveKit signaling: the caller owns room and track
authorization, subscriber lifetime, codec validation, keyframe requests and the
protected Group target.

The H.264 adapter supports packetization mode 1, caches SPS/PPS and waits for an
IDR before producing Annex-B frames. It rejects unsupported slices, SSRC and
timestamp changes and incomplete input; packet loss requires another keyframe.
The Opus adapter accepts one 48 kHz stream. Neither adapter transcodes media.

For combined audio/video, `RunAVToLal` maps both tracks using RTCP Sender Report
NTP/RTP references from the same publisher and sends them through a bounded
reordering queue. The caller must verify report freshness, SSRC and track
identity. A stalled sink can delay bridge cleanup but cannot block LiveKit's
other subscribers. `internal/livekitsfu` supplies the LiveKit subscription and
stops the bridge when the source, room or connection changes.
