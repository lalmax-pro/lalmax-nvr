# API 入口

默认 Web/API 地址为 `http://localhost:9090`，可通过 `server.listen` 修改。运行中的 NVR 在 `/docs/` 提供可浏览的 [OpenAPI 规范](../../internal/docsportal/openapi.yaml)。以下列出常用接口；完整路由以 [`internal/api/handler.go`](../../internal/api/handler.go) 的注册代码为准。

受保护接口需要登录凭据；以下示例使用 HTTP Basic Auth。修改、删除和部分诊断接口还需要操作权限。健康检查与就绪检查为公开接口。

## 健康与日志

| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/api/health` | NVR 健康状态 |
| GET | `/api/readyz` | 就绪状态 |
| GET | `/api/capabilities` | 功能能力 |
| GET | `/api/service-logs` | 服务日志；需要操作权限 |

## 摄像头与流

| 方法 | 路径 | 用途 |
|------|------|------|
| GET / POST | `/api/cameras` | 列出 / 添加设备 |
| GET / PUT / DELETE | `/api/cameras/{id}` | 查看 / 更新 / 删除设备 |
| POST | `/api/cameras/test-connection` | 检查网络可达性，不验证解码 |
| GET / POST | `/api/streams` | 列出 / 创建流记录 |
| GET / PUT / DELETE | `/api/streams/{stream_id}` | 查看 / 更新 / 删除流记录 |
| GET | `/api/streams/{stream_id}/stream/ws` | 浏览器 WS-FLV 播放代理 |
| GET | `/api/streams/{stream_id}/stream.flv` | HTTP-FLV 播放代理 |
| GET | `/api/streams/{stream_id}/stream.m4s` | fMP4 播放代理 |
| GET | `/api/streams/history` | 历史流 |

流详情响应包含状态和可用的 `play_urls`。具体的播放 URL 应以响应为准。注册流为设备的接口仍是 `POST /api/streams/{stream_id}/promote`；此操作不会自动创建录像计划。

## IPTV

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/iptv/imports` | 导入 M3U URL 或文本 |
| GET | `/api/iptv/imports/{id}/items` | 查看探测结果 |
| POST | `/api/iptv/imports/{id}/commit` | 纳管选中的频道 |
| GET | `/api/iptv/channels` | 列出频道 |
| GET | `/api/iptv/channels/{id}/playback` | 获取浏览器代理播放地址 |
| PUT | `/api/iptv/channels/{id}` | 更新频道，包括 `publish_enabled` |

浏览器播放使用同源 HLS 代理。只有启用频道发布或录像计划需要该频道时，NVR 才启动服务端 HLS 拉流；详见 [IPTV](iptv.md)。

## 录像

| 方法 | 路径 | 用途 |
|------|------|------|
| GET / POST | `/api/recording-plans` | 列出 / 创建录像计划 |
| GET / PUT / DELETE | `/api/recording-plans/{id}` | 查看 / 更新 / 删除计划 |
| GET | `/api/recordings` | 列出录像 |
| GET | `/api/recordings/timeline` | 录像时间轴 |
| GET | `/api/recordings/sources?include_archived=true` | 包含历史来源的录像来源列表 |
| GET | `/api/recordings/{id}/download` | 下载录像文件 |

录像计划按 `stream_id` 绑定，添加设备或登记流不会自动录像。详见[录像计划](recording-plans.md)。

## 设置

`GET /api/settings` 返回包含 `dlna` 在内的系统设置；`PUT /api/settings` 可提交部分设置，例如 `{"dlna":{"enabled":true}}`。其他设置还有 `/api/settings/hls`、`/api/settings/streaming`、`/api/settings/gb28181` 等路径。DLNA 的使用方法见 [DLNA](dlna.md)。

## 请求示例

```bash
curl -u admin:password http://localhost:9090/api/streams
curl -u admin:password http://localhost:9090/api/recording-plans
```
