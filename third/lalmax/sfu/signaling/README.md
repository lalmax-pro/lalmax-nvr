# SFU control WebSocket

This package provides an opt-in `http.Handler`; it is not mounted by the server yet and does not bind or transmit Room media. SDP negotiation is optional.

The embedding server must supply all of: an existing room manager, trusted authentication that returns the principal, participant and room scope, a fail-closed `access.Authorizer`, a snapshot visibility filter, and an origin policy. Never construct the identity from untrusted message fields. Raw Room APIs bypass this wrapper. The filter must remove all data the principal cannot see and be safe for concurrent use. Connection or credential revocation must be managed by the embedding server; existing idle connections and media are not actively revoked here.

After a successful upgrade the client receives a `snapshot`. The `Snapshot` Go struct is encoded with its exported JSON field names; generation and sequence come from the room. Later updates are full filtered snapshots. If the bounded room watcher overflows, the connection closes: reconnect and obtain a new snapshot. Event and command response ordering may interleave.

Client commands are JSON with a nonempty `id` and a `type`:

- `publish`: `kind`, `source`, `codec`, and optional `track_id` (the client WebRTC track ID).
- `unpublish`, `subscribe`, `unsubscribe`: `publication_id`.
- `offer`: `sdp`, an initial fully gathered SDP offer (at most 64 KiB). When a trusted negotiator factory is configured, the result contains the fully gathered SDP answer in `sdp`. Client-initiated offer/answer renegotiation is supported on the same connection. Trickle ICE and ICE restart are not yet validated.

Replies have `type: "result"`, the original `id`, and either `publication` for successful publish or `error` for failure. No error details from policy providers are returned. The server limits incoming messages and write time; production deployment must also supply connection admission limits, idle timeouts, authentication, revocation and secure transport.

The optional `PionNegotiator` uses a PeerConnection configured by the trusted factory. The factory must enforce media permissions and codec/transport policy; receiving an answer does not establish an authorized Room media binding. Track negotiation, RTCP handling, automatic subscriptions and GroupBridge require later work.

A trusted `NewSessionNegotiator` may receive the authorized control session and use `Session.StartIncoming` from its `OnTrack` callback. The embedding application must bind each remote track to the correct already-authorized publication ID and validate SDP profile/fmtp and transport policy. The session checks media kind and MIME type, rechecks the publish policy, and uses the current participant generation. Inbound forwarding ends when the context, publication or session ends; successful unpublish and session close cancel the binding. This does not actively revoke existing media when an external policy changes.

A trusted session-aware factory may call `Session.StartOutgoing` for an existing subscription, after adding the appropriate local track to its PeerConnection. It checks the current session, subscription policy, media kind and MIME; the factory still owns codec fmtp/profile validation, SDP timing and RTCP. Stopping the outgoing binding cleans up its subscription. Active media is not automatically revoked when external policy changes. Dynamic track changes need a new client offer after the corresponding control operation.

When `track_id` is provided, it is mapped atomically to the resulting publication within that participant session. The session-aware factory can call `Session.StartMappedIncoming` in `OnTrack`; unknown or empty Pion track IDs are rejected. The mapping is not an SDP MID mapping and does not support multiple simulcast tracks sharing an ID. Authentication, SDP profile/fmtp validation and factory registration remain the embedding application’s responsibility.

`RoomFactory.NewNegotiator` is an opt-in session-aware factory that assembles mapped incoming tracks and the session's existing authorized outgoing subscriptions. It requires trusted `NewPeer` and `Codec` callbacks and explicit fanout limits. Incoming track IDs and subscriptions must be declared before the offer that negotiates them. Later subscription changes and newly published tracks require a new client offer. The codec callback must validate actual source/receiver profile and fmtp, not just MIME. Failure or cancellation during setup can close already-started outgoing bindings and remove their subscriptions; clients should resubscribe before retrying. The factory drains subscriber RTCP but does not yet route PLI/FIR upstream. Application mounting, authentication, token revocation, signaling-visible errors and GroupBridge remain the embedding server's responsibility.

A publication snapshot exposes `RTPReady` once its fanout is enabled. This indicates that the media path has been initialized, not that a keyframe is available or decoding is possible. Clients should wait for readiness before subscribing; a failed offer closes the media session and requires reconnect.
