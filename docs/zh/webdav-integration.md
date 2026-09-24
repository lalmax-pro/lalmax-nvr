# WebDAV 文件访问

WebDAV 默认开启，地址为 NVR Web/API 地址加 `webdav.path_prefix`（默认 `http://localhost:9090/dav/`）。它直接映射 `storage.root_dir`，目录结构以实际文件为准，不保证存在 `/recordings/` 或 `/snapshots/` 两个固定子目录。

```yaml
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
```

WebDAV 使用 NVR 全局 HTTP Basic Auth。默认只读，允许 `GET`、`HEAD`、`OPTIONS` 和 `PROPFIND`：

```bash
curl -u admin:password -X PROPFIND -H 'Depth: 1' http://localhost:9090/dav/
```

设置 `read_write: true` 后，服务还接受 `PUT`、`MKCOL`、`DELETE`、`COPY`、`MOVE`、`LOCK` 和 `UNLOCK`。`PUT` 成功后会尝试把上传文件登记为录像，并将路径的第一段当作摄像头名称；若找不到同名设备，可能创建新设备。其他写操作作用于文件系统，并不保证同步更新录像数据库，使用前应备份数据。默认只读模式适合浏览和下载。

如果修改了 `path_prefix`，客户端 URL 也要相应修改。实现见 [`internal/webdav/server.go`](../../internal/webdav/server.go)，服务挂载见 [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go)。
