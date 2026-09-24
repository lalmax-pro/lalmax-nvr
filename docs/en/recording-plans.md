# Recording plans

Recording is driven by **plans** attached to **lalmax streams** (`stream_id`), not cameras.

A stream with **no plan is not recorded**. Registering a stream as a device (the old “promote” action) also **does not start recording**.

Web UI: **Recording plans** in the sidebar (`#/recording-plans`). REST: `/api/recording-plans`.

## Stream, device, plan

| | Stream | Device (Camera) | Recording plan |
|---|---|---|---|
| What | lalmax group `live/{stream_id}` | Name, location, PTZ, dashboard, groups | When to persist |
| How | RTSP/ONVIF pull, GB28181, RTMP/SRT/WHIP publish | Add a camera, or **register** an existing stream as a device | Create on the Recording plans page |
| Recording | Only if a plan is active | Does not decide recording | Sole policy |

Typical use:

- **Record a push stream only**: create a plan. No device needed.
- **Manage a camera**: add a device or register the stream; create a plan on its `stream_id` to record.
- **Register as device**: give the stream an operations identity (name, map, dashboard). The API is still `POST /api/streams/{stream_id}/promote`.

The recordings page lists **recording sources**: cameras, plan-only streams, and leftover stream IDs that still have files. `GET /api/recordings/sources?include_archived=true` is the sidebar. Timeline, VOD, and file lists still query by the source `id` (`camera_id` in `recordings`).

## Modes

| `mode` | Behavior |
|--------|----------|
| `continuous` | Record 24/7 |
| `scheduled` | Record only inside weekly `windows` |
| `event` | Record during MQTT / ONVIF motion (and similar) event windows |
| `adaptive` | Sparse IDRs when calm, full speed on activity; spacing from the camera `adaptive.timelapse_interval` |
| `off` | Plan exists but nothing is written (preview still works) |

`enabled: false` and `mode: off` both stop writers. One plan per stream.

Flowcharts for start, write, and stop are in [Recording flow](recording-flow.md).

## Scheduler

`RecordingPlanner` loads desired state from `recording_plans`. `RecordingScheduler` reconciles "the plan wants disk" with "the stream is in lalmax":

- Both true: `TaskManager.Ensure`
- Plan off, event window ended, or the stream has left lalmax: `TaskManager.Stop`
- Whether the stream is registered as a device does not change this

Device offline, device deletion, and stream down stop that stream's task immediately. Process shutdown calls `StopAll`. When the stream returns to lalmax and the plan still wants recording, the scheduler calls `Ensure` again.

In embedded mode, frames come from an in-process subscription; HTTP mode falls back to lalmax RTSP. MJPEG, HTTP JPEG, Xiaomi, and timelapse still write through the device recorder.

## API

```
GET    /api/recording-plans
POST   /api/recording-plans
GET    /api/recording-plans/{id}
PUT    /api/recording-plans/{id}
DELETE /api/recording-plans/{id}
```

```bash
curl -u admin:password -X POST http://localhost:9090/api/recording-plans \
  -H 'Content-Type: application/json' \
  -d '{"stream_id":"obs-1","name":"OBS","mode":"continuous","enabled":true}'
```

For `scheduled`, `windows` is `{day_of_week:0-6, start_time:"HH:MM", end_time:"HH:MM"}`.

Camera JSON `recording_mode` is read-only: the bound stream’s plan, or `off` if none. `POST /api/cameras/:id/pause-recording` sets that stream’s plan to `enabled: false`.

Recording rows carry `stream_id`. With a device, `camera_id` is the device ID; for plan-only recording both IDs are the stream ID.
