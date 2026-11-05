# SFU control WebSocket

This package provides an opt-in `http.Handler`; it is not mounted by the server yet and does not negotiate or transmit media.

The embedding server must supply all of: an existing room manager, trusted authentication that returns the principal, participant and room scope, a fail-closed `access.Authorizer`, a snapshot visibility filter, and an origin policy. Never construct the identity from untrusted message fields. Raw Room APIs bypass this wrapper. The filter must remove all data the principal cannot see and be safe for concurrent use. Connection or credential revocation must be managed by the embedding server; existing idle connections and media are not actively revoked here.

After a successful upgrade the client receives a `snapshot`. The `Snapshot` Go struct is encoded with its exported JSON field names; generation and sequence come from the room. Later updates are full filtered snapshots. If the bounded room watcher overflows, the connection closes: reconnect and obtain a new snapshot. Event and command response ordering may interleave.

Client commands are JSON with a nonempty `id` and a `type`:

- `publish`: `kind`, `source`, `codec`.
- `unpublish`, `subscribe`, `unsubscribe`: `publication_id`.

Replies have `type: "result"`, the original `id`, and either `publication` for successful publish or `error` for failure. No error details from policy providers are returned. The server limits incoming messages and write time; production deployment must also supply connection admission limits, idle timeouts, authentication, revocation and secure transport.

SDP/ICE, Track negotiation, RTCP handling, automatic subscriptions and GroupBridge are intentionally outside this control-only protocol and require later work.
