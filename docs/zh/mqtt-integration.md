# MQTT 事件触发

MQTT 客户端默认关闭。启用后，NVR 订阅 `{mqtt.topic}/trigger/+`，把主题最后一段当作**摄像头 ID**。消息可触发该摄像头所绑定流的事件或自适应录像计划；没有相应计划时不会开始录像。

```yaml
mqtt:
  enabled: true
  broker: "tcp://192.168.1.100:1883"
  topic: "lalmax-nvr"
  client_id: "lalmax-nvr"
  username: ""
  password: ""
```

向 `lalmax-nvr/trigger/front-door` 发布 JSON：

```json
{"action":"record"}
```

`record`、`start`、`trigger` 会触发活动。`stop`、`end`、`off` 会结束当前事件窗口；自适应录像由活动窗口自行收束。消息中的 `duration` 字段不会生效，时长由 NVR 的事件后录配置控制。连接失败时客户端每 5 秒重试，连接建立后支持自动重连。

示例命令：

```bash
mosquitto_pub -h 192.168.1.100 -t 'lalmax-nvr/trigger/front-door' -m '{"action":"record"}'
```

先为 `front-door` 的绑定流建立 `event` 或 `adaptive` [录像计划](recording-plans.md)。主题解析和动作过滤见 [`internal/mqtt/client.go`](../../internal/mqtt/client.go)，触发录像的处理见 [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go)。
