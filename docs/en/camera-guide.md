# Camera ingest

Add cameras on the Web UI **Devices** page. Camera records are stored in the NVR database; an existing pushed stream can be registered as a device from **Stream management**. A [recording plan](recording-plans.md) on the bound stream determines whether it is recorded.

## Choose a source

| Source | What to provide | Behavior |
|--------|-----------------|----------|
| RTSP | The camera's `rtsp://` URL; optional `rtsp_transport: tcp` or `udp` | The NVR pulls RTSP; TCP is the default transport |
| ONVIF | ONVIF service endpoint and credentials | Discovers profiles and obtains a media URL; see [ONVIF](onvif-guide.md) |
| HTTP JPEG | An `http://` or `https://` URL that returns JPEG images | The NVR polls images; audio is unavailable |
| Xiaomi | Xiaomi account and device ID | See [Xiaomi cameras](xiaomi-setup.md) |

The camera creation API also accepts `rtmp-pull`, `http-flv-pull`, and `udp-ts-pull` as pull sources. Manage IPTV channels in the separate [IPTV](iptv.md) page; they need not be added as cameras. RTMP, SRT, and WHIP pushes can appear directly in Stream management and can be registered as devices when needed.

A brand or model name does not guarantee a particular URL or codec. Use the media URL supplied by the camera, ONVIF responses, and actual probe results.

## Add an RTSP camera

Select RTSP in the Web UI and enter a name and media URL. To use the API:

```bash
curl -u admin:password -X POST http://localhost:9090/api/cameras \
  -H 'Content-Type: application/json' \
  -d '{"name":"Front door","protocol":"rtsp","encoding":"h264","url":"rtsp://user:password@192.168.1.10:554/stream","rtsp_transport":"tcp"}'
```

When `protocol` is `rtsp`, omitting `encoding` defaults to H.264; `http` defaults to JPEG. ONVIF attempts to detect the encoding from device profiles. `enabled` defaults to `true` when omitted.

For H.264/H.265 RTSP, ONVIF, and Xiaomi cameras, omitting `audio_enabled` on creation enables audio recording by default; pass `false` explicitly in the API to disable it. MJPEG and HTTP JPEG do not support audio recording. Recorded audio still depends on the source providing a supported audio track.

## Diagnose ingest

1. Check that the NVR host can reach the media URL. For RTSP, `ffprobe -rtsp_transport tcp 'rtsp://…'` can show the actual codecs.
2. Use the Web UI connection check or `POST /api/cameras/test-connection`. This endpoint checks only whether the RTSP TCP port or HTTP endpoint is reachable; it **does not verify decoding or sustained media delivery**.
3. After adding the camera, inspect its current status and playback URLs in Stream details. If it is online but playback fails, check codecs, player support, and the NVR [service logs](troubleshooting.md).
4. To record, create a [recording plan](recording-plans.md) for its `stream_id`; adding a device alone does not start recording.

See [`internal/api/handler.go`](../../internal/api/handler.go) for routes and [`internal/api/handlers_camera.go`](../../internal/api/handlers_camera.go) for creation and connection checks.
