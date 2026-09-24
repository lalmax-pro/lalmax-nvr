# 故障排除

先确认 NVR 本身是否可用，再依次检查来源、流、播放协议和录像计划。以下地址中的 `localhost:9090` 是默认 Web/API 地址；实际端口以 `server.listen` 为准。

## NVR 无法访问

```bash
curl -i http://localhost:9090/api/health
curl -i http://localhost:9090/api/readyz
```

这两个接口无需登录。若连接失败，先看进程日志、`server.listen` 与端口占用；若返回错误状态，再查看响应里的检查项。运行中的 API 文档位于 `/docs/`。

## 摄像头在线失败

1. 在 NVR 所在主机上确认源地址可达。RTSP 可用 `ffprobe -rtsp_transport tcp 'rtsp://…'` 查看实际音视频编码。
2. Web UI 的连接测试对应 `POST /api/cameras/test-connection`，只检查 RTSP TCP 端口或 HTTP 端点可达，不能证明媒体流可解码。
3. 查看设备是否启用，以及 RTSP 地址、凭据、传输方式或 ONVIF 地址是否正确。配置方式见[摄像头接入](camera-guide.md)和 [ONVIF](onvif-guide.md)。
4. 用有操作权限的账号查看 `GET /api/service-logs`，按摄像头名称或流 ID 搜索错误。

## 流在线但无法播放

先请求 `GET /api/streams/{stream_id}`，检查是否有活动媒体流和 `play_urls`。浏览器直播通常通过 NVR 的 `/api/streams/{stream_id}/stream/*`、`stream.flv`、`stream.m4s` 或 `stream/ws` 等代理路径播放；若这些路径返回 404，先确认流 ID、流是否已发布，以及所用协议是否在 `play_urls` 中。

IPTV 的频道页面播放走同源 HLS 代理；如需 RTSP、FLV 等 NVR 协议播放，应在频道卡片启用**发布**。发布和录像拉流要求 `media.mode: embedded`。详见 [IPTV](iptv.md)。

## 找不到录像

1. 查看 `GET /api/recording-plans`：计划应绑定正确的 `stream_id`，处于启用状态，并且当前时间符合其模式或时间窗。
2. 查看 `GET /api/streams/{stream_id}`：源流必须可用。仅添加或登记设备不会自动录像。
3. 在录像页查找历史来源；`GET /api/recordings/sources?include_archived=true` 会包含仍有历史录像的来源。删除流记录并不等于删除其录像文件。

详见[录像计划](recording-plans.md)和[录制流程](recording-flow.md)。

## DLNA 设备找不到 NVR

确认已在设置中启用 DLNA，播放器与 NVR 在可互通的局域网，UDP `1900` 组播可达，且 DLNA HTTP 端口（默认 `8200`）可访问。若设置了 `allowed_cidrs`，确认播放器 IP 在允许网段内。详细配置见 [DLNA](dlna.md)。

## 日志与接口

```bash
curl -u admin:password 'http://localhost:9090/api/service-logs?level=error&limit=200'
```

`/api/service-logs` 需要操作权限；也可直接查看启动 NVR 的终端或容器日志。接口路由见 [`internal/api/handler.go`](../../internal/api/handler.go)。
