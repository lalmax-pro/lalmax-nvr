# DLNA playback on the LAN

The NVR includes an optional UPnP MediaServer. When enabled, DLNA players on the LAN can discover it over SSDP and browse **Live** and **Recordings**.

## Enable it

Enable DLNA on the Web UI **Settings** page, or add this to `lalmax-nvr.yaml`:

```yaml
dlna:
  enabled: true
  friendly_name: "Lalmax NVR"
  port: 8200
  include_live: true
  include_recordings: true
```

DLNA is disabled by default. Its HTTP server listens on a separate `dlna.port` (default `8200`), which must differ from `server.listen`. Discovery uses SSDP multicast on UDP `1900`. The NVR selects a LAN IPv4 address automatically; set `dlna.interface` to choose a network interface. Players must be able to reach that address and the DLNA HTTP port.

## Browsing and playback

- **Live** contains registered cameras and currently active streams that are not associated with a camera, including published IPTV channels. A camera can appear while offline, but its stream must be active to play. Live video is served as HTTP MPEG-TS at `/dlna/live/{stream_id}.ts`.
- **Recordings** contains recording entries served as MP4 at `/dlna/media/{recording_id}`. `max_browse_count` caps the number of recordings listed (default `200`).

Use `include_live` and `include_recordings` to hide either directory. Playback also depends on the client's support for the stream codecs and MPEG-TS/MP4.

## Access and limits

DLNA endpoints do not use the Web UI's Basic Auth. Enable DLNA only on a trusted LAN; restrict clients with `allowed_cidrs` if needed:

```yaml
dlna:
  enabled: true
  allowed_cidrs:
    - "192.168.1.0/24"
```

`gop_cache` controls how many GOPs HTTP-TS caches for a new viewer (default `1`, range `1–16`). A larger value may start playback faster but increase delay behind live. After changing it, restart the NVR and republish streams to apply it to existing streams. The `max_media_viewers` setting also exists (default `4`), but the current implementation does not enforce this limit.

When you save DLNA settings in the Web UI, the NVR attempts to restart the DLNA service immediately. If the response contains `dlna_error`, check the port, network interface, and multicast network. See [`internal/dlna/service.go`](../../internal/dlna/service.go) for the implementation and [`internal/config/config.go`](../../internal/config/config.go) for settings and defaults.
