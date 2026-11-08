# SFU bridge registry

The registry owns server-side bridge handles and enforces the authenticated
owner and management policy on each view, stop, playback and remove operation.
Credentials are never included in status or list responses. A terminal result
is reported only after the service revokes its playback grant and releases the
publisher. Finished entries remain available briefly and can be removed or
pruned; active entries are never pruned.

The LiveKit integration in `cmd/lalmax-nvr` validates the room generation,
track identity and codec, subscribes as a hidden participant, and generates a
protected Group stream ID. Its media adapter is in `internal/livekitsfu`.
