# 故障排除

按“进程 → 配置 → 来源 → 运行流 → 播放 → 录像 → 存储”逐层定位。不要用一个层面的成功替代另一个层面的验收。

以下默认地址为 `http://localhost:9090`；实际值以 `server.listen` 和部署拓扑为准。示例密码均为占位符。

## 1. 建立诊断基线

```bash
curl -i http://localhost:9090/api/health
curl -i http://localhost:9090/api/readyz
curl -u 'admin:replace-with-a-private-password' \
  http://localhost:9090/api/health/details
```

| 结果 | 含义 | 下一步 |
|------|------|--------|
| `/api/health` 无法连接 | 进程、监听、端口或网络问题 | 查进程日志、监听地址、容器映射和防火墙 |
| health=200，readyz=503 | 进程活着但未就绪 | 查 DB、磁盘是否达到 95%、goroutine 是否达到 5000 |
| 受保护请求 503 `SETUP_REQUIRED` | 未完成密码初始化 | 完成 Web 初始化或使用 `init`/`NVR_PASSWORD` 自动初始化 |
| 401 | 未认证或账号密码错误 | 检查 Basic 头、用户启用状态；不要先改业务配置 |
| 403 | 已登录但角色无操作权限 | 使用 `super_admin` 或调整账号，不要重复请求 |
| 429 | 触发认证或接口限流 | 停止重试风暴，检查失败客户端和共享出口 IP |

服务日志需要操作权限：

```bash
curl -u 'admin:replace-with-a-private-password' \
  'http://localhost:9090/api/service-logs?level=error&limit=200'
```

也可使用 `docker compose logs --tail 200 lalmax-nvr` 或仓库脚本 `scripts/unix/logs.sh`。内嵌引擎日志会汇入 NVR 服务日志并标记 `lal/lalmax`。

## 2. 配置无法启动或未生效

### 启动即退出

1. 查看第一条 `config` / `config validation` / `init` 错误。
2. 对照[配置规范](configuration.md)和当前 `config/config.example.yaml` 检查字段名、时长、端口及协议编码。
3. 检查存储根目录可写、数据库 DSN 可连接、目标数据库权限足够。
4. 使用加密配置时，检查密钥是否可用；密钥错误/缺失不能通过反复重启修复。

当前 YAML 解析不保证拒绝未知字段，因此“能启动”不能证明字段名正确。

### 修改 YAML 后行为没有变化

- 确认进程实际 `-config` 路径；`init` 和主程序的默认路径不同。
- Docker 默认读取 `/data/lalmax-nvr.yaml`，不是仓库 `config/lalmax-nvr.yaml`。
- `NVR_PASSWORD` 只用于自动初始化，不是已有配置的通用密码覆盖。
- `/api/config/reload` 不重建所有监听器。监听地址、数据库、媒体引擎、FTP/SIP 等结构变更应重启并验证。
- Unix 脚本会按脚本 `DATA_DIR` 同步配置中的 `root_dir`，手动改动可能被下一次脚本运行覆盖。

## 3. 设备或来源无法上线

### RTSP / ONVIF

1. 从 **NVR 所在网络命名空间** 测试设备，而不是只从个人电脑测试。
2. 检查 URL、设备账号、RTSP TCP/UDP 和实际编码：

   ```bash
   ffprobe -rtsp_transport tcp 'rtsp://device-address/stream'
   ```

3. 连接测试只检查端点可达，不证明认证、解码或录像正常。
4. ONVIF 自动发现依赖 UDP 组播；bridge 网络失败时改用手动探测/直接端点，或正确配置 host 网络。
5. ONVIF 发现成功不等于取得可用流；继续检查 profile、GetStreamUri 和 RTSP 媒体。

### GB28181

- 设备必须能向 NVR 的 SIP 地址 REGISTER；平台随后 INVITE。
- 设备必须能把 PS/RTP 推到 `gb28181.media_ip` 和协商端口；只开放 SIP 5060 不够。
- 检查 20 位平台/设备 ID、注册密码、Catalog 通道和 NAT/防火墙。
- 不得用“NVR 能否 RTSP 拉设备”替代国标链路排查。详见[GB28181 指南](gb28181-guide.md)。

### RTMP / SRT / WHIP 推流

1. 检查对应模块和端口已启用、映射并放行。
2. 从流详情取得 `ingest_urls`，检查流 ID/别名和按流令牌。
3. RTMP 默认要求 `stream_key`；SRT 使用 streamid 的 `u`；WHIP 还需 ICE `4888/udp,tcp` 可达。
4. 检查流是否被封禁、是否已有冲突发布者，以及源编码是否受支持。
5. 不要通过关闭匿名认证来“验证网络”；这会改变安全边界。

### VoIP

分别检查 SIP 注册/呼叫信令、RTP/SRTP 媒体、PBX 注册、WebRTC 浏览器对讲。`voip.enabled` 要求 embedded 媒体模式；远程终端还需要正确的 `sip_ip`、`media_ip` 和 UDP 媒体端口范围。详见[VoIP](voip.md)。

## 4. 流在线但无法播放

```bash
curl -u 'admin:replace-with-a-private-password' \
  http://localhost:9090/api/streams/STREAM_ID
```

依次核对：

1. `active`、实际 `video_codec/audio_codec` 和 `last_frame_time`。
2. `play_urls` 中是否有目标协议，使用返回 URL 而不是旧文档拼接地址。
3. 浏览器是否支持编码和播放技术；H.265、音频和 WebCodecs 能力不能只看 NVR 协议表。
4. HLS/LL-HLS 需要 `hls.enabled=true`；默认关闭。HLS 与录像开关无关。
5. WebRTC 检查 WHEP 信令和 ICE；仅 9090/12090 HTTP 可达不代表媒体连接成功。
6. 直连 `12090` 播放默认也要求 NVR 账号；配置了按流播放令牌时还需满足该策略。
7. 经代理时检查 WebSocket Upgrade、SSE/FLV 长连接、响应缓冲、超时及 Range。

IPTV 仅浏览器播放和“发布到 NVR”是不同链路；需要 RTSP/FLV 等协议时应启用发布。Emby 片尾默认下线，只有启用循环才重新播放。

## 5. 有直播但没有录像

按顺序检查：

1. `GET /api/recording-plans` 是否存在绑定目标 `stream_id` 的计划。
2. `enabled=true`，模式不是 `off`；`scheduled` 当前处于本地时间窗口，`event` 当前有活动事件。
3. 目标流在媒体引擎中存活；只创建推流槽位但无发布者不会产生录像。
4. 设备/流编码和录像路径是否受支持；MJPEG、HTTP JPEG、延时摄影和 adaptive 走不同执行路径。
5. 是否刚创建片段而尚未到关键帧切分点；最终可回放记录需要片段完成收尾。
6. 存储目录可写、磁盘未满，日志无封装、索引或重命名错误。

登记为设备不会自动创建计划。计划刚修改会立即请求对账，后台另有 30 秒周期；持续失败应看日志而非反复创建重复计划（重复返回 409）。

详见[录像与回放](recording-plans.md)和[录制流程](recording-flow.md)。

## 6. 找不到历史录像或回放异常

- 用 `GET /api/recordings/sources?include_archived=true` 查历史来源，不要只看当前设备列表。
- 录像的 `camera_id` 是来源归属 ID，纯流录像可能等于 `stream_id`；删除流/设备不必然删除文件。
- 用时间范围和来源检查 `/api/recordings/timeline`，再查具体录像的 `file_path`、格式和合并状态。
- 连续 VOD 中允许缺口；断流、无计划窗口和修复段可能形成 discontinuity。
- 文件存在于磁盘但无索引、或索引存在但文件缺失时，查看启动时的临时文件清理、孤立文件重建和清理日志；不要手工随意移动文件后仍期待索引一致。

## 7. 磁盘持续增长或录像被提前删除

1. 检查全局和设备 `retention_days`；活动设备上的 0 回退全局，不是永久。
2. 检查磁盘阈值清理；超过阈值会删除最早的可删除录像，即使尚未到保留期。
3. 检查 `locked`：锁定录像不参与常规清理且不能直接删除。
4. 无设备纯流录像不能依赖逐设备时间保留路径，必须监控磁盘阈值和额外清理策略。
5. 归档保留的 0 与活动设备不同，表示永久保留。
6. 文件权限错误、非录像文件、锁定过多和删除失败都可能使清理无法降低占用；清理不是容量保证。

## 8. Docker、文件访问与发现

### 数据目录权限

当前镜像默认 UID/GID 1000，不是旧文档中的 65534。检查宿主机目录所有者、挂载和 `NVR_UID/NVR_GID`；大目录上的入口递归 `chown` 可能增加启动时间。

### FTP 登录失败

当前 FTP 支持全局 `auth.password_hash` 的 bcrypt 校验，不再存在“哈希必然不能登录”的旧限制。它不使用 Web 多用户账号。检查全局用户名/密码、2121 和被动端口；协议没有 TLS，且具备写/删除能力，详见[FTP](ftp-integration.md)。

### WebDAV 写操作被拒绝

默认 `webdav.read_write=false`，只允许读相关方法。开启写模式前必须评估文件与数据库一致性；网管/代理也必须允许 PROPFIND 等方法。

### DLNA 找不到 NVR

确认 `dlna.enabled=true`、UDP 1900 组播、独立 HTTP 8200、播放器网段及 `allowed_cidrs`。只映射 8200 不提供 SSDP 发现；可对照[DLNA](dlna.md)检查 advertise URL 和网卡。

## 9. 提交问题前应收集的信息

- 版本（`lalmax-nvr -version`）、部署方式、操作系统/架构和相关配置片段（**脱敏**）。
- health/readyz 状态、相关流详情、计划详情和实际使用的协议。
- 从首次异常开始的服务日志和必要 trace；包含时间、流 ID、设备 ID 和复现步骤。
- 网络方向：谁能访问谁、实际监听/通告地址、NAT 和防火墙，不只写“端口已开”。
- 录像问题附相关录像元数据和文件状态；播放问题附浏览器/播放器及实际编码。

不得提交真实密码、base64 账号令牌、完整含凭据 URL、DSN、配置密钥、私钥或未经处理的隐私画面。
