# 摄像头接入

通过 Web UI 的**设备**页添加摄像头。摄像头记录保存在 NVR 数据库中；已有推流可在**流管理**中登记为设备。是否录像由绑定流的[录像计划](recording-plans.md)决定。

## 选择接入方式

| 来源 | 如何填写 | 实现行为 |
|------|----------|----------|
| RTSP | 摄像头提供的 `rtsp://` 地址；可选 `rtsp_transport: tcp` 或 `udp` | NVR 拉取 RTSP；默认传输方式为 TCP |
| ONVIF | 设备的 ONVIF 服务地址和凭据 | 发现配置文件并获取媒体地址，再接入视频；详见 [ONVIF](onvif-guide.md) |
| HTTP JPEG | 可返回 JPEG 图片的 `http://` 或 `https://` 地址 | NVR 轮询图片，不提供音频 |
| 小米 | 通过小米账号和设备 ID 接入 | 详见 [小米摄像头](xiaomi-setup.md) |

创建摄像头 API 也接受 `rtmp-pull`、`http-flv-pull` 和 `udp-ts-pull` 作为拉流来源。IPTV 频道由独立的 [IPTV](iptv.md) 页面管理，无需添加为摄像头。RTMP、SRT、WHIP 推流可直接出现在流管理中，需要设备身份时再登记。

品牌和型号并不保证特定 URL 或编码可用。请以摄像头自身提供的媒体地址、ONVIF 返回值和实际探测结果为准。

## 添加 RTSP 摄像头

Web UI 中选择 RTSP，填写名称和媒体地址。若需要使用 API，可以发送：

```bash
curl -u admin:password -X POST http://localhost:9090/api/cameras \
  -H 'Content-Type: application/json' \
  -d '{"name":"门口","protocol":"rtsp","encoding":"h264","url":"rtsp://user:password@192.168.1.10:554/stream","rtsp_transport":"tcp"}'
```

`protocol` 为 `rtsp` 时，省略 `encoding` 会按 H.264 处理；`http` 默认为 JPEG。ONVIF 会尝试从设备配置文件识别编码。`enabled` 省略时默认为 `true`。

H.264/H.265 的 RTSP、ONVIF、小米摄像头，创建时省略 `audio_enabled` 会默认开启音频录制；可在 API 请求中显式传 `false` 关闭。MJPEG 和 HTTP JPEG 不支持音频录制。录像内容仍取决于源是否真正提供受支持的音频轨。

## 排查接入

1. 先确认 NVR 主机能访问媒体地址；RTSP 可用 `ffprobe -rtsp_transport tcp 'rtsp://…'` 查看实际编解码信息。
2. 可在 Web UI 中测试连接，或调用 `POST /api/cameras/test-connection`。此接口只检查 RTSP TCP 端口或 HTTP 端点是否可连接，**不验证视频解码或持续出流**。
3. 添加后在流详情查看当前状态和播放地址。若流在线但无法播放，检查编码、播放器支持情况及 NVR 的[服务日志](troubleshooting.md)。
4. 需要录像时，在[录像计划](recording-plans.md)中为对应 `stream_id` 建立计划；仅添加设备不会自动录像。

API 路由见 [`internal/api/handler.go`](../../internal/api/handler.go)，创建和连接探测行为见 [`internal/api/handlers_camera.go`](../../internal/api/handlers_camera.go)。
