# DLNA 局域网播放

NVR 提供可选的 UPnP MediaServer。启用后，局域网中的 DLNA 播放器可通过 SSDP 发现 NVR，浏览 **Live** 和 **Recordings** 两个目录。

## 启用

在 Web UI 的**设置**页开启 DLNA，或在 `lalmax-nvr.yaml` 中配置：

```yaml
dlna:
  enabled: true
  friendly_name: "Lalmax NVR"
  port: 8200
  include_live: true
  include_recordings: true
```

DLNA 默认关闭。HTTP 服务单独监听 `dlna.port`（默认 `8200`），不能与 `server.listen` 使用同一端口；设备发现使用 UDP `1900` 的 SSDP 组播。NVR 自动选择局域网 IPv4 地址，也可用 `dlna.interface` 指定网卡。播放器需能访问该地址和 DLNA HTTP 端口。

## 目录与播放

- **Live** 列出已登记摄像头，以及当前在线且未关联摄像头的流（包括已发布的 IPTV 频道）。摄像头即使不在线也可能出现在目录中；播放时流必须在线。直播经 `/dlna/live/{stream_id}.ts` 以 HTTP MPEG-TS 提供。
- **Recordings** 列出录像记录，文件经 `/dlna/media/{recording_id}` 以 MP4 提供。`max_browse_count` 限制目录最多列出的录像数，默认 `200`。

`include_live`、`include_recordings` 可分别隐藏两个目录。客户端能否解码取决于播放器对流内编码和 MPEG-TS/MP4 的支持。

## 访问与限制

DLNA 接口不使用 Web UI 的 Basic Auth。仅在可信局域网启用；可用 `allowed_cidrs` 限制客户端网段，例如：

```yaml
dlna:
  enabled: true
  allowed_cidrs:
    - "192.168.1.0/24"
```

`gop_cache` 控制 HTTP-TS 为新观众缓存的 GOP 数，默认 `1`，范围 `1–16`；更大的值可能让播放器更快出画，但会增加相对直播的延迟。修改该值后需重启 NVR 并重新推流才能应用到现有流。配置中还提供 `max_media_viewers`（默认 `4`），但当前实现尚未按此值限制连接数。

Web UI 修改 DLNA 设置后会尝试立即重启 DLNA 服务；若设置响应带 `dlna_error`，请检查端口、网卡和组播网络。实现入口见 [`internal/dlna/service.go`](../../internal/dlna/service.go)，配置结构与默认值见 [`internal/config/config.go`](../../internal/config/config.go)。
