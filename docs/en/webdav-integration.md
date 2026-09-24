# WebDAV file access

WebDAV is enabled by default. Its URL is the NVR Web/API address plus `webdav.path_prefix` (default `http://localhost:9090/dav/`). It maps directly to `storage.root_dir`; browse the actual files instead of assuming fixed `/recordings/` or `/snapshots/` subdirectories.

```yaml
webdav:
  enabled: true
  path_prefix: "/dav"
  read_write: false
```

WebDAV uses the NVR's global HTTP Basic Auth. The default is read-only and permits `GET`, `HEAD`, `OPTIONS`, and `PROPFIND`:

```bash
curl -u admin:password -X PROPFIND -H 'Depth: 1' http://localhost:9090/dav/
```

With `read_write: true`, the service also accepts `PUT`, `MKCOL`, `DELETE`, `COPY`, `MOVE`, `LOCK`, and `UNLOCK`. After a successful `PUT`, it attempts to register the uploaded file as a recording and treats the first path segment as a camera name; it may create a device if that name does not exist. Other write operations act on the filesystem and do not guarantee corresponding recording database updates, so back up data before using them. The default read-only mode is suitable for browsing and downloads.

If you change `path_prefix`, update the client URL too. See [`internal/webdav/server.go`](../../internal/webdav/server.go) for behavior and [`cmd/lalmax-nvr/main.go`](../../cmd/lalmax-nvr/main.go) for route mounting.
