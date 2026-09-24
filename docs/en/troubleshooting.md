# Troubleshooting

Check the NVR first, then the source, stream, playback protocol, and recording plan. `localhost:9090` below is the default Web/API address; the actual port follows `server.listen`.

## The NVR is unreachable

```bash
curl -i http://localhost:9090/api/health
curl -i http://localhost:9090/api/readyz
```

These endpoints do not require login. If the connection fails, inspect process logs, `server.listen`, and port availability. If an endpoint reports an error, inspect the checks in its response. A running NVR serves API documentation at `/docs/`.

## A camera does not come online

1. Check source reachability from the NVR host. For RTSP, `ffprobe -rtsp_transport tcp 'rtsp://…'` can reveal the actual audio and video codecs.
2. The Web UI connection check calls `POST /api/cameras/test-connection`. It checks only RTSP TCP port or HTTP endpoint reachability and cannot prove that media decodes.
3. Check whether the device is enabled and verify the RTSP URL, credentials, transport, or ONVIF endpoint. See [Camera ingest](camera-guide.md) and [ONVIF](onvif-guide.md).
4. With an account that has operate permission, inspect `GET /api/service-logs` and search for the camera name or stream ID.

## A stream is online but playback fails

Request `GET /api/streams/{stream_id}` and inspect whether media is active and which `play_urls` are available. Browser playback usually uses NVR proxy paths such as `/api/streams/{stream_id}/stream/*`, `stream.flv`, `stream.m4s`, or `stream/ws`. If a path returns 404, first verify the stream ID, publication state, and whether that protocol is present in `play_urls`.

IPTV channel playback on its own page uses the same-origin HLS proxy. To play over NVR protocols such as RTSP or FLV, enable **Publish** on the channel card. Publication and recording pulls require `media.mode: embedded`. See [IPTV](iptv.md).

## Recordings are missing

1. Inspect `GET /api/recording-plans`: a plan must refer to the correct `stream_id`, be enabled, and match the current mode or schedule window.
2. Inspect `GET /api/streams/{stream_id}`: the source stream must be available. Adding or registering a device does not start recording by itself.
3. Look for historical sources on the recordings page. `GET /api/recordings/sources?include_archived=true` includes sources with historical recordings. Deleting a stream record does not itself delete recording files.

See [Recording plans](recording-plans.md) and [Recording flow](recording-flow.md).

## A DLNA player cannot find the NVR

Confirm DLNA is enabled in Settings, the player and NVR can communicate on the LAN, SSDP multicast on UDP `1900` is available, and the DLNA HTTP port (default `8200`) is reachable. If `allowed_cidrs` is set, the player's IP must be in an allowed range. See [DLNA](dlna.md).

## Logs and routes

```bash
curl -u admin:password 'http://localhost:9090/api/service-logs?level=error&limit=200'
```

`/api/service-logs` requires operate permission. You can also inspect the terminal or container logs where the NVR runs. See [`internal/api/handler.go`](../../internal/api/handler.go) for routes.
