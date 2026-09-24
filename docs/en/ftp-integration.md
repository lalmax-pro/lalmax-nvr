# FTP file access

The built-in FTP service is enabled by default. Its control port is `2121`, with passive transfer ports `2122-2140` by default. The FTP root maps to `storage.root_dir`; there are **no fixed `/recordings/` or `/snapshots/` virtual directories**. Browse the actual storage layout.

```yaml
ftp:
  enabled: true
  port: 2121
  passive_port_range: "2122-2140"
```

The implementation uses **plain FTP**, without TLS. Authentication accepts only the configured global `auth.username` and `auth.password`, and rejects anonymous access. Startup normally converts a plaintext password to `password_hash` and clears `auth.password`, while FTP cannot verify the hash yet. As a result, FTP login may fail after normal initialization. A Web UI account or a hash-only configuration does not resolve this current limitation.

The FTP filesystem supports listing, downloading, writing, and deleting files, so expose it only to a trusted network. To upload a recording over FTP, use `/{camera_id}/{filename}`. The server renames it to `{camera_id}_{timestamp_ms}.{ext}` in that camera's directory and inserts a recording row after transfer completion. Other file operations act directly on the storage root and do not automatically maintain every related database state.

For file browsing with the existing hash-based authentication, use [WebDAV](webdav-integration.md) in read-only mode. See [`internal/ftp/server.go`](../../internal/ftp/server.go) for authentication and paths, and [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go) for credential wiring.
