"""Native HA event entities for live NVR events."""

from homeassistant.components.event import EventEntity
from homeassistant.core import callback

from .const import EVENT_TYPES
from .push_entity import NVRPushEntity, async_setup_push_entities

PARALLEL_UPDATES = 0


async def async_setup_entry(hass, entry, async_add_entities):
    async_setup_push_entities(
        hass,
        entry,
        async_add_entities,
        "event",
        lambda coordinator, entry, sid: [NVREvent(coordinator, entry, sid)],
    )


class NVREvent(NVRPushEntity, EventEntity):
    _attr_translation_key = "activity"
    _attr_event_types = EVENT_TYPES

    def __init__(self, coordinator, entry, sid):
        super().__init__(coordinator, entry, sid, "activity")

    @callback
    def _handle_event(self, event):
        if event and event["stream_id"] == self.stream_id:
            self._trigger_event(event["type"], event)
        super()._handle_event(event)
