"""Short detection pulses; no invented persistent motion state."""

from datetime import timedelta

from homeassistant.components.binary_sensor import (
    BinarySensorDeviceClass,
    BinarySensorEntity,
)
from homeassistant.core import callback
from homeassistant.helpers.event import async_call_later

from .push_entity import NVRPushEntity, async_setup_push_entities

PARALLEL_UPDATES = 0
DETECTION_HOLD = timedelta(seconds=60)
LABELS = {
    "motion": {"motion"},
    "person": {"person", "human"},
    "vehicle": {"car", "truck", "bus", "motorcycle", "bicycle", "vehicle"},
}


async def async_setup_entry(hass, entry, async_add_entities):
    async_setup_push_entities(
        hass,
        entry,
        async_add_entities,
        "binary_sensor",
        lambda coordinator, entry, sid: [
            NVRDetection(coordinator, entry, sid, kind) for kind in LABELS
        ],
    )


class NVRDetection(NVRPushEntity, BinarySensorEntity):
    _attr_is_on = False

    def __init__(self, coordinator, entry, sid, kind):
        super().__init__(coordinator, entry, sid, kind)
        self.kind = kind
        self._attr_translation_key = kind
        self._attr_device_class = (
            BinarySensorDeviceClass.MOTION
            if kind == "motion"
            else BinarySensorDeviceClass.OCCUPANCY
        )
        self._cancel_clear = None

    @callback
    def _clear(self, _=None):
        self._cancel_timer()
        self._attr_is_on = False
        self.async_write_ha_state()

    @callback
    def _cancel_timer(self):
        if self._cancel_clear:
            self._cancel_clear()
            self._cancel_clear = None

    @callback
    def _handle_event(self, event):
        if not self.available:
            self._clear()
            return
        if (
            event
            and event["stream_id"] == self.stream_id
            and (
                event["type"] == "motion"
                and self.kind == "motion"
                or event["type"] == "ai_detection"
                and LABELS[self.kind].intersection(event["labels"])
            )
        ):
            self._cancel_timer()
            self._attr_is_on = True
            self._cancel_clear = async_call_later(
                self.hass, DETECTION_HOLD, self._clear
            )
        super()._handle_event(event)

    @callback
    def _handle_coordinator_update(self):
        if not self.available:
            self._clear()
        super()._handle_coordinator_update()

    async def async_will_remove_from_hass(self):
        self._cancel_timer()
        await super().async_will_remove_from_hass()
