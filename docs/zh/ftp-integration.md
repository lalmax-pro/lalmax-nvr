# FTP 文件访问

NVR 内置 FTP 服务，默认开启，控制端口为 `2121`，被动传输端口默认 `2122-2140`。FTP 根目录映射到 `storage.root_dir`，**没有固定的 `/recordings/` 或 `/snapshots/` 虚拟目录**；请以实际存储目录为准。

```yaml
ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
```

FTP 实现是**明文 FTP**，没有 TLS。认证只接受配置的全局 `auth.username` 和 `auth.password`，不支持匿名访问。当前启动流程通常会把明文密码转换成 `password_hash` 并清空 `auth.password`，而 FTP 尚不能校验哈希密码；因此常规初始化后的 FTP 登录可能失败。这是当前实现限制，不能通过 Web UI 账号或只填 `password_hash` 解决。

FTP 文件系统支持列目录、下载及写入、删除等操作，因此应只向可信网络开放。通过 FTP 上传录像时，目标路径必须是 `/{camera_id}/{filename}`。服务端会将文件改名为 `{camera_id}_{timestamp_ms}.{ext}` 并写入该摄像头目录，传输结束后登记一条录像记录。其他文件操作直接作用于存储根目录，不会自动维护所有相关数据库状态。

如需使用基于现有哈希认证的文件浏览，可使用 [WebDAV](webdav-integration.md) 的只读模式。FTP 认证和路径行为见 [`internal/ftp/server.go`](../../internal/ftp/server.go)；服务启动时传入的凭据见 [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go)。
