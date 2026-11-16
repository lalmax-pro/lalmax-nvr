# NVR-07 部署与运行规范

## 1. 部署基线

**OPS-001｜先确定媒体拓扑再开放端口。** 部署者必须列出实际使用的接入、播放、信令、文件和观测协议，只映射这些端口。默认端口和启用条件见[系统架构](architecture.md)。

**OPS-002｜数据与配置必须持久化。** 至少需要保护配置文件、应用数据库、录像文件以及配置加密密钥。只备份数据库不能恢复录像媒体。

**OPS-003｜生产使用已验证版本。** 应固定 Release 标签或镜像摘要，完成测试后升级；不应让生产环境无审查地跟随 `latest`。

**OPS-004｜核心运行无需 CGO。** Go 构建使用 `CGO_ENABLED=0`，Web 前端编译后嵌入二进制。可选快照、转码、媒体检测可能需要 FFmpeg/FFprobe/GStreamer，不得把“单文件”扩展为所有可选功能都无外部工具依赖。

## 2. Docker 部署

### 2.1 Compose

仓库 [`docker-compose.yml`](../../docker-compose.yml) 提供完整示例：

```bash
docker compose up -d
docker compose ps
docker compose logs --tail 200 lalmax-nvr
```

默认关键约定：

| 项 | 当前值 |
|----|--------|
| 镜像 | `ghcr.io/lalmax-pro/lalmax-nvr:latest` |
| 容器配置 | `/data/lalmax-nvr.yaml` |
| 数据挂载 | 宿主机 `./data` → 容器 `/data` |
| 数据环境变量 | `NVR_DATA_DIR=/data` |
| 运行用户 | 默认 UID/GID `1000:1000`，可通过 `NVR_UID/NVR_GID` 调整 |
| 健康检查 | 容器内 `lalmax-nvr health`，默认检查本机 `:9090` |

**OPS-101｜挂载路径与变量一致。** 修改容器内挂载点时，必须同时修改 `NVR_DATA_DIR`；仅修改宿主机左侧路径通常不需要改变容器内 `/data`。

**OPS-102｜权限按当前镜像处理。** 入口脚本默认以 root 启动后修正数据目录所有权，再切换到 UID/GID 1000。旧文档中的 UID 65534 不适用于当前镜像。入口中的递归 `chown` 可能在大目录上耗时，应为挂载目录预设置合理所有权。

**OPS-103｜最小端口映射。** 仅本机或网关访问时，可将 `9090` 绑定到指定管理地址。公网/局域网客户端需要直连媒体时才映射 `12090`、`4888`、`15544` 等；FTP、GB28181、JT 等未用端口应从生产 Compose 删除或保持模块关闭。

### 2.2 Host 网络与组播

ONVIF WS-Discovery 使用 UDP 组播，DLNA 使用 SSDP 组播。Docker bridge 下这些发现能力通常不可用。

需要自动发现时，可使用 `network_mode: host` 或经过验证的组播网络方案。使用 host 模式时：

- 删除/忽略 `ports` 映射，因为容器直接共享主机网络栈；
- 重新审查所有默认开启的 FTP、WebDAV 和媒体监听，host 模式没有端口映射形成的隔离；
- 不需要组播时，可使用 ONVIF 手动探测或直接填写端点，无需改成 host 模式。

### 2.3 本地镜像

```bash
docker build -t lalmax-nvr:local .
```

多阶段 Dockerfile 构建前端和 Go 二进制，运行时镜像包含 FFmpeg/FFprobe；GStreamer 需构建参数 `WITH_GSTREAMER=true` 并验证目标架构插件。启用转码前必须使用实际模板探测，不应仅因镜像包含工具就假定所有编码器可用。

## 3. 二进制与源码部署

### 3.1 预构建二进制

```bash
./lalmax-nvr init \
  --password 'replace-with-a-private-password' \
  --data-dir /var/lib/lalmax-nvr \
  --config config/lalmax-nvr.yaml
./lalmax-nvr -config config/lalmax-nvr.yaml
```

应由服务管理器负责自动重启、日志和信号停止。进程响应 SIGINT/SIGTERM，并使用 30 秒关闭超时；服务管理器超时不应明显短于该值。

### 3.2 源码构建

```bash
./scripts/unix/build.sh
./scripts/unix/test.sh
./scripts/unix/run.sh
```

脚本会构建当前前端、嵌入静态资源并编译 Go；构建成功不代表测试成功。Windows 使用 `scripts/windows/nvr.ps1` 或批处理入口。完整参数见[`scripts/README.md`](../../scripts/README.md)。

## 4. 反向代理与 HTTPS

### 4.1 基本规则

**OPS-201｜HTTPS 终止于受控网关。** 对非可信网络提供 Web/UI/API 时必须使用 HTTPS，并将后端 `9090` 限制到网关可访问范围。

**OPS-202｜长连接必须单独验收。** 代理需支持 WebSocket Upgrade、SSE、FLV/fMP4 长响应和录像 Range 请求；不得因普通 JSON API 可用就推断实时播放可用。应关闭不必要的响应缓冲，并根据长连接设置读写/空闲超时。

**OPS-203｜WebRTC 不是纯 HTTP 反向代理。** WHEP/WHIP 的 HTTP 信令可代理，ICE 仍需客户端访问候选地址及 UDP/TCP 端口。必须验证实际 SDP/ICE 配置和网络路径。

Caddy 最小示例：

```caddyfile
nvr.example.com {
    reverse_proxy 127.0.0.1:9090
}
```

Nginx 必须包含 Host、转发协议以及 WebSocket 所需 Upgrade/Connection 头；生产还应配置合适的 `proxy_read_timeout`、请求体上限和访问控制。WebDAV 使用额外 HTTP 方法，网关不得只放行 GET/POST。

## 5. 配置变更与重启

**OPS-301｜区分保存、重载和重启。** Web 保存设置会更新配置；`/api/config/reload` 只更新当前 API 配置及已接入的特定状态，不保证数据库、媒体监听、FTP、SIP 等全部重建。涉及这些结构的变更应重启并做协议级验收。

**OPS-302｜外部编辑先检测冲突。** Watcher 通过 mtime/size 识别外部变化，Web 保存可用冲突保护避免静默覆盖。自动化配置管理应串行化写入，不要并发编辑 YAML 和调用设置 API。

**OPS-303｜停止是有顺序的操作。** 正常停止会取消后台任务、停止 HTTP/FTP/媒体及录像并关闭数据库。升级和备份应使用正常停止，不应把强制 kill 当作常规维护方式。

## 6. 健康检查与观测

| 检查 | 语义 | 用途 |
|------|------|------|
| `GET /api/health` | 进程存活及是否需要初始化 | liveness；返回 200 不代表 DB/磁盘就绪 |
| `GET /api/readyz` | DB 可 ping、磁盘 <95%、goroutines <5000 | readiness；失败为 503 |
| `GET /api/health/details` | 受保护的组件与摄像头详情 | 运维诊断 |
| `GET /metrics` | Prometheus 指标 | 全局或独立 Basic Auth |
| `/api/service-logs` | 受保护的进程日志环形缓冲 | 操作权限；进程重启后内存内容不持久 |
| 本地 traces / OTLP | 调用链、指标、日志 | 按配置保存或导出 |

**OPS-401｜监控不能只看进程。** 至少监控 readiness、磁盘使用率、录像写入失败、流离线/重连、队列丢帧和关键协议可达性。`cleanup` 阈值和 readyz 95% 是独立阈值。

日志配置、OTLP 和远程日志见[配置规范](configuration.md)。将日志导出到外部系统前必须落实传输与访问控制。

## 7. 备份与恢复

### 7.1 数据库备份 API

`POST /api/backup` 需要操作权限，在 **配置文件所在目录** 的 `backups/` 下创建 `nvr-backup-YYYYMMDD-HHMMSS.db`；`GET /api/backups` 返回文件名列表。

- SQLite 使用 `VACUUM INTO`，避免直接复制正在写入的 WAL 数据库造成不一致。
- PostgreSQL/MySQL 的活动数据也通过当前实现导出为 SQLite 备份文件；这不是原生数据库物理备份的替代品。
- 备份 API 不包含录像文件、YAML、媒体引擎配置或加密密钥。

### 7.2 完整恢复材料

| 材料 | 位置/方式 | 注意 |
|------|-----------|------|
| 主 YAML | 当前 `-config` 路径 | 包含敏感信息，限制访问 |
| 配置密钥 | 环境密钥、密钥文件或 `<config>.encryption-key` | 与密文分离保管；丢失无法解密 |
| 数据库 | 默认 `{root_dir}/lalmax-nvr.db`，或外部 DB | 使用一致性备份 |
| 录像与快照等 | `storage.root_dir` 下实际文件 | 大容量，需独立备份策略 |
| 生成的媒体配置 | 默认 `{root_dir}/config/lalmax.conf.json` | 可由主配置重新生成，但定制内容需确认 |

完整文件级快照应先正常停止服务，或使用能同时保证数据库与文件一致性的快照机制。恢复演练必须在隔离环境验证配置解密、数据库可读、录像可回放和协议端口，而不只检查压缩包存在。

## 8. 升级与回滚

1. 阅读目标版本 Release Notes 和配置变更。
2. 备份配置、数据库、密钥和关键录像；记录当前版本/镜像摘要。
3. 在测试环境验证现有配置迁移、设备上线、直播、录像、回放和集成。
4. 正常停止旧版本，部署固定的新版本，观察启动迁移日志。
5. 验证 `/api/health`、`/api/readyz` 和实际业务链路。
6. 回滚前确认新版本是否进行了旧版本无法读取的数据库迁移；必要时使用升级前备份，不得直接假设任意版本可双向回滚。

数据库在线迁移到 PostgreSQL/MySQL 是独立操作，见[数据库迁移](database-migration.md)，不要与版本升级混为一步。

## 9. 上线验收

- 进程存活、就绪和详细健康均符合预期。
- 管理员/普通用户/匿名权限、HTTPS 和 `/metrics` 认证通过。
- 每种启用来源至少完成一路真实推拉流和目标客户端播放。
- 创建计划后产生可关闭、可回放、可下载录像；断流恢复和时间轴缺口符合预期。
- 磁盘阈值、保留、锁定和备份恢复经过验证。
- 防火墙只开放必要端口，ONVIF/DLNA 组播、GB28181 RTP、VoIP RTP、WebRTC ICE 等按真实网络验证。
- 日志、指标、追踪和外部集成不包含不应外发的秘密或隐私数据。
