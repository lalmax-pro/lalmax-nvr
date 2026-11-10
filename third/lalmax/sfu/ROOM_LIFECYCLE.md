# Room creation and idle collection

`Manager.GetOrCreateWithLimits` atomically resolves a key to one room and
rejects requests whose limits differ from an existing room. Authentication,
authorization and deciding which rooms may be created remain the caller's
responsibility. A returned room may be closed by a concurrent collector
before a later join; handle `ErrRoomClosed` by reauthorizing and retrying.

`Manager.CollectIdle` is an explicit operation. An embedding application owns
its schedule and chooses a positive timeout; no background collector is
started automatically. A room becomes idle when its final participant leaves,
including system participants. Watches do not keep an empty room alive.
Collection and joins are serialized: a successful join keeps the room alive,
while a join after collection fails on the old generation. Collection closes
the room, sends its normal close event where the watcher buffer permits, and
returns the key and generation that were removed. Recreating a key yields a
new generation, and stale close requests cannot remove it.

The NVR runtime explicitly creates multiple rooms and optionally schedules
selective idle cleanup using `CloseIfIdle`, excluding the compatibility room.
Room creation is business-authorized; a timeout is disabled by default and
can be configured with `sfu.idle_timeout`. Reconnection credentials, persistent
room configuration and per-room timeout policy remain future work.
