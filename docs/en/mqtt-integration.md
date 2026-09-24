# MQTT event triggers

The MQTT client is disabled by default. When enabled, the NVR subscribes to `{mqtt.topic}/trigger/+` and treats the last topic segment as a **camera ID**. A message can trigger that camera's event or adaptive recording plan on its bound stream. Without such a plan, recording does not start.

```yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  topic: "lalmax-nvr"
  client_id: "lalmax-nvr"
  username: ""
  password: ""
```

Publish JSON to `lalmax-nvr/trigger/front-door`:

```json
{"action":"record"}
```

`record`, `start`, and `trigger` signal activity. `stop`, `end`, and `off` end the current event window; adaptive recording settles according to its own activity window. A message `duration` field has no effect; NVR event post-roll settings control duration. The client retries an initial connection every five seconds and supports automatic reconnect after connecting.

Example:

```bash
mosquitto_pub -h 192.168.1.100 -t 'lalmax-nvr/trigger/front-door' -m '{"action":"record"}'
```

First create an `event` or `adaptive` [recording plan](recording-plans.md) for the stream bound to `front-door`. See [`internal/mqtt/client.go`](../../internal/mqtt/client.go) for topic and action parsing and [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go) for recording callbacks.
