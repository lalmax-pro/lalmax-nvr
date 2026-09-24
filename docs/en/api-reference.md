# API entry points

The default Web/API address is `http://localhost:9090`; change it with `server.listen`. A running NVR serves browsable [OpenAPI documentation](../../internal/docsportal/openapi.yaml) at `/docs/`. This page lists common endpoints. For the complete route registry, see [`internal/api/handler.go`](../../internal/api/handler.go).

Protected endpoints require credentials; the examples below use HTTP Basic Auth. Mutations and some diagnostic endpoints also require operate permission. Health and readiness endpoints are public.

## Health and logs

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/health` | NVR health |
| GET | `/api/readyz` | Readiness |
| GET | `/api/capabilities` | Capabilities |
| GET | `/api/service-logs` | Service logs; requires operate permission |

## Cameras and streams

| Method | Path | Purpose |
|--------|------|---------|
| GET / POST | `/api/cameras` | List / add devices |
| GET / PUT / DELETE | `/api/cameras/{id}` | Read / update / delete a device |
| POST | `/api/cameras/test-connection` | Check network reachability, not decoding |
| GET / POST | `/api/streams` | List / create stream records |
| GET / PUT / DELETE | `/api/streams/{stream_id}` | Read / update / delete a stream record |
| GET | `/api/streams/{stream_id}/stream/ws` | Browser WS-FLV playback proxy |
| GET | `/api/streams/{stream_id}/stream.flv` | HTTP-FLV playback proxy |
| GET | `/api/streams/{stream_id}/stream.m4s` | fMP4 playback proxy |
| GET | `/api/streams/history` | Historical streams |

Stream details include state and available `play_urls`; use those returned URLs for playback. Registering a stream as a device still uses `POST /api/streams/{stream_id}/promote` and does not create a recording plan.

## IPTV

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/iptv/imports` | Import an M3U URL or text |
| GET | `/api/iptv/imports/{id}/items` | Inspect probe results |
| POST | `/api/iptv/imports/{id}/commit` | Enroll selected channels |
| GET | `/api/iptv/channels` | List channels |
| GET | `/api/iptv/channels/{id}/playback` | Get a browser proxy playback URL |
| PUT | `/api/iptv/channels/{id}` | Update a channel, including `publish_enabled` |

Browser playback uses the same-origin HLS proxy. The NVR starts a server-side HLS pull only when a channel is published or a recording plan needs it. See [IPTV](iptv.md).

## Recording

| Method | Path | Purpose |
|--------|------|---------|
| GET / POST | `/api/recording-plans` | List / create plans |
| GET / PUT / DELETE | `/api/recording-plans/{id}` | Read / update / delete a plan |
| GET | `/api/recordings` | List recordings |
| GET | `/api/recordings/timeline` | Recording timeline |
| GET | `/api/recordings/sources?include_archived=true` | Sources including historical ones |
| GET | `/api/recordings/{id}/download` | Download a recording |

Recording plans bind to `stream_id`; adding a device or registering a stream does not start recording. See [Recording plans](recording-plans.md).

## Settings

`GET /api/settings` includes `dlna` settings; `PUT /api/settings` accepts partial updates such as `{"dlna":{"enabled":true}}`. Other settings endpoints include `/api/settings/hls`, `/api/settings/streaming`, and `/api/settings/gb28181`. See [DLNA](dlna.md) for usage.

## Example requests

```bash
curl -u admin:password http://localhost:9090/api/streams
curl -u admin:password http://localhost:9090/api/recording-plans
```
