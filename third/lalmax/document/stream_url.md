# 流地址说明

本文档使用 `conf/lalmax.conf.json` 的默认配置举例，默认流名为 `test110`。

## 基本规则

- `lal` 原生能力使用 `lal` 配置段中的端口，例如 RTMP、RTSP、HTTP-FLV、HLS-TS、HTTP-TS。
- `lalmax` 扩展能力使用 `lalmax` 配置段中的端口，例如 SRT、WHIP/WHEP、HTTP-FMP4、HLS-FMP4/LLHLS。
- 当前 lal 使用简单流管理时主要按 `streamName` 匹配。示例中的 `/live/test110` 里，`test110` 是流名，`live` 可作为常用路径前缀。
- lalmax 扩展拉流接口支持可选 `app_name` 参数，用于未来多 appName 同 streamName 的精确匹配；不传时仍按历史 `streamName` 兼容查找。
- HTTP-FLV、HTTP-TS、HLS-TS 的路径还受 `lal.httpflv.url_pattern`、`lal.httpts.url_pattern`、`lal.hls.url_pattern` 影响。示例配置中 HTTP-FLV 的 `url_pattern` 为 `/`，因此 `/live/test110.flv` 可用。
- HTTPS、RTMPS、RTSPS 依赖配置中的证书文件，浏览器或播放器可能需要信任测试证书。

## 推流地址

### RTMP

```text
rtmp://127.0.0.1:1935/live/test110
```

FFmpeg 示例：

```bash
ffmpeg -re -i demo.flv -c:a copy -c:v copy -f flv rtmp://127.0.0.1:1935/live/test110
```

如果开启 RTMPS：

```text
rtmps://127.0.0.1:4935/live/test110
```

### RTSP

```text
rtsp://127.0.0.1:5544/live/test110
```

FFmpeg 示例：

```bash
ffmpeg -re -i demo.flv -c:a copy -c:v copy -f rtsp rtsp://127.0.0.1:5544/live/test110
```

如果开启 RTSPS：

```text
rtsps://127.0.0.1:5322/live/test110
```

### SRT

```text
srt://127.0.0.1:6001?streamid=#!::h=test110,m=publish
```

`h` 表示流名，`m=publish` 表示推流。

### WebRTC WHIP

```text
http://127.0.0.1:1290/webrtc/whip?streamid=test110
https://127.0.0.1:1233/webrtc/whip?streamid=test110
```

WHIP 使用 HTTP POST 传输 SDP offer，通常由 OBS、WHIP 客户端或 WebRTC 工具调用。

### GB28181

GB28181 不是普通 URL 推流。设备通过 SIP 注册到 lalmax，平台再通过 API 控制播放。详见 [gb28181.md](./gb28181.md)。

### JT1078

> **实验性功能**：JT1078/JT808 当前仅通过软件模拟终端和自动化测试验证，尚未使用真实车载终端验证。设备兼容性及实际运行稳定性仍需验证。

JT1078 也不是普通 URL 推流。终端在收到 JT/T 808 的直播 `0x9101` 或回放 `0x9201` 后，把音视频帧推到 lalmax 的 TCP 或 UDP 单端口（信令里 TCP/UDP 端口二选一）：

```text
tcp://127.0.0.1:1078
udp://127.0.0.1:1078
```

流名由包头生成：`{sim}_{channel}`，例如 `1003_1`。视频为 H.264 / H.265，音频为 AAC / G.711A / G.711U。MP3 和 G.726 帧会被丢弃。播放仍走现有拉流地址，例如：

```text
http://127.0.0.1:8080/live/1003_1.flv
```

## 拉流地址

### RTMP

```text
rtmp://127.0.0.1:1935/live/test110
```

ffplay 示例：

```bash
ffplay rtmp://127.0.0.1:1935/live/test110
```

### RTSP

```text
rtsp://127.0.0.1:5544/live/test110
```

ffplay 示例：

```bash
ffplay rtsp://127.0.0.1:5544/live/test110
```

如果开启 RTSPS：

```text
rtsps://127.0.0.1:5322/live/test110
```

### HTTP-FLV

```text
http://127.0.0.1:8080/live/test110.flv
https://127.0.0.1:4433/live/test110.flv
```

ffplay 示例：

```bash
ffplay http://127.0.0.1:8080/live/test110.flv
```

### HTTP-TS

需要启用 `lal.httpts.enable`。

```text
http://127.0.0.1:8080/live/test110.ts
https://127.0.0.1:4433/live/test110.ts
```

### HLS-TS

需要启用 `lal.hls.enable`。

```text
http://127.0.0.1:8080/hls/test110/playlist.m3u8
http://127.0.0.1:8080/hls/test110/record.m3u8
http://127.0.0.1:8080/hls/test110.m3u8
```

### SRT

```text
srt://127.0.0.1:6001?streamid=#!::h=test110,m=request
```

`h` 表示流名，`m=request` 表示拉流。

### WebRTC WHEP

```text
http://127.0.0.1:1290/webrtc/whep?streamid=test110
https://127.0.0.1:1233/webrtc/whep?streamid=test110
```

如果需要指定 appName：

```text
http://127.0.0.1:1290/webrtc/whep?streamid=test110&app_name=live
```

WHEP 使用 HTTP POST 传输 SDP offer，通常由 WHEP 播放器或 WebRTC 工具调用。

### Jessibuca DataChannel

```text
webrtc://127.0.0.1:1290/webrtc/play/live/test110
```

如果需要指定 appName：

```text
webrtc://127.0.0.1:1290/webrtc/play/live/test110?app_name=live
```

### HTTP-FMP4

```text
http://127.0.0.1:1290/live/m4s/test110.mp4
https://127.0.0.1:1233/live/m4s/test110.mp4
```

如果需要指定 appName：

```text
http://127.0.0.1:1290/live/m4s/test110.mp4?app_name=live
```

### HLS-FMP4/LLHLS

需要启用 `lalmax.fmp4_config.hls.enable`。

```text
http://127.0.0.1:1290/live/hls/test110/index.m3u8
https://127.0.0.1:1233/live/hls/test110/index.m3u8
```

如果需要指定 appName：

```text
http://127.0.0.1:1290/live/hls/test110/index.m3u8?app_name=live
```

如果需要低延迟 HLS，设置 `lalmax.fmp4_config.hls.low_latency` 为 `true`。
