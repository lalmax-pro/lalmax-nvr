# Home Assistant

Publish selected lalmax-nvr streams as Home Assistant camera entities, with live video, snapshots and availability. HA reads RTSP from the NVR, reusing existing ingest. Publishing does not start recording or connect HA to the original camera.

## Installation

1. Copy the complete repository directory `custom_components/lalmax_nvr` into the same path under your HA configuration directory. Restart HA. The tested version is **Home Assistant 2026.10.2**. This is a manually installed custom integration, not a built-in or HACS integration.
2. Open **NVR Settings → Home Assistant**, enable publication, select streams and save. Search and bulk selection are supported. Stream cards also have individual publication buttons. Unregistered push streams can be published without creating camera devices.
3. Optionally enter an RTSP server origin such as `rtsp://192.168.1.20:15544`, without credentials or a stream path. Otherwise the media engine URL is used. The HA client replaces advertised loopback hosts with the NVR HTTP hostname. With a separate media engine, explicitly configure its reachable address.
4. In HA, use **Settings → Devices & services → Add integration → lalmax-nvr**. Enter the NVR HTTP URL, username and password. A dedicated ordinary NVR user is sufficient for reads.
5. If `media.rtsp_auth_enable` is enabled, enter the RTSP username and password separately. HTTP and RTSP credentials are independent. RTSP uses TCP and supports Basic/Digest authentication.
6. Add the resulting camera entities to your dashboard:

```yaml
type: picture-entity
entity: camera.front_door
camera_view: live
```

## Lifecycle and authentication

The integration fetches the complete catalog every 30 seconds, without the ordinary stream list's 100-item page limit. New publications create entities automatically. Offline streams retain identity and recover when the same stream returns. Identity and publication settings are persisted in the NVR database and survive restarts, reloads and database backups. Do not connect two simultaneously running database clones to the same HA as separate instances: they share an instance UUID.

Unpublishing or disabling publication makes the entity unavailable on the next refresh and stops its HA stream worker. Entity registry records remain available for manual cleanup. Republishing restores the same entity. Deleting a source leaves a selected offline publication; deselect it explicitly when permanent removal is intended.

NVR HTTP authentication is required for all integration APIs. Publication changes require a super administrator. The catalog excludes upstream camera URLs/passwords and RTSP account credentials. When `auth.playback_tokens` is configured, the catalog includes each published stream's playback token in its RTSP URL; protect it as a media credential and do not publish it in logs. Streams without a configured token remain unavailable without breaking other entries.

Unpublishing does not invalidate RTSP credentials held by other clients or grant strict per-stream isolation to an NVR user. Existing media authorization remains in effect.

## Snapshots and compatibility

H.264 video and AAC audio are the primary targets. H.265 depends on HA and browser support; automatic transcoding is not included. Prefer an H.264 substream when available.

NVR snapshots require FFmpeg on PATH and cache frames for 10 seconds. Without NVR FFmpeg, HA uses its Stream integration to generate still images. Stream snapshots never substitute an unrelated camera's image just because it has a management binding.

Check HTTP and RTSP connectivity separately. A successful HTTP configuration flow does not verify RTSP authentication. A snapshot response of 503 means FFmpeg is absent or the frame cannot currently be captured. Verify published/online status, codec, RTSP credentials and per-stream playback tokens when live playback fails. IPTV sources must first enter the lalmax media engine.

Use the HA integration's **Reconfigure** menu to change address or credentials. Invalid HTTP credentials trigger reauthentication. Reconfiguration to a different NVR identity is rejected.

| Method | API path | Purpose |
|---|---|---|
| GET / PUT | `/api/settings/home-assistant` | Read settings / completely replace publication settings (admin) |
| PUT | `/api/integrations/home-assistant/streams/{stream_id}` | Select/unselect a stream with `{"enabled":true}` or `false` (admin) |
| GET | `/api/integrations/home-assistant` | Complete published catalog, instance ID and snapshot capability |
| GET | `/api/integrations/home-assistant/streams/{stream_id}/snapshot` | Published stream JPEG |

Playback, PTZ and HACS distribution are being implemented in subsequent stages. Track implementation and validation in [Issue #46](https://github.com/lalmax-pro/lalmax-nvr/issues/46).


## Detection events and automations

Each published stream has an Activity event entity and Motion, Person and Vehicle binary sensors. Events arrive over an authenticated SSE connection. The NVR checks the current publication list for every event, including camera-to-stream bindings. Only normalized event types and detection labels are forwarded; arbitrary metadata, messages and filesystem paths are excluded.

The AI webhook/browser detector supplies `ai_detection` labels. `motion` labels and MQTT trigger action `motion` drive the Motion sensor; people and vehicles drive their respective sensors. The integration consumes existing detections; it does not perform inference. Configure the detector or MQTT ingress in the NVR first. Detection sensors reset 60 seconds after the last matching detection and clear on disconnection or unpublication. A sensor indicates a recent detection, not continuous occupancy.

Use HA event-entity triggers or the `lalmax_nvr_event` bus event. For example:

```yaml
triggers:
  - trigger: event
    event_type: lalmax_nvr_event
    event_data:
      stream_id: front-door
      type: ai_detection
conditions:
  - condition: template
    value_template: "{{ 'person' in trigger.event.data.labels }}"
actions:
  - action: light.turn_on
    target:
      entity_id: light.entrance
```

Events include `instance_id`, `config_entry_id`, `stream_id`, `id`, `type`, `source`, `started_at` and `labels`. Filter by instance or config entry when multiple NVRs use the same stream ID. Types are `ai_detection`, `motion`, `mqtt_trigger`, `recorder_reconnected` and `health`. The connection reconnects with bounded backoff; event entities become unavailable during interruptions. This is a live feed with duplicate suppression, not durable delivery: events during disconnections are not replayed. Historical events remain in the NVR event timeline. Reverse proxies must forward `/api/integrations/home-assistant/events` without response buffering and permit long-lived connections.
