"""Bounded PTZ pulses as native HA buttons."""

from homeassistant.components.button import ButtonEntity

from .control import async_ptz
from .push_entity import NVRStreamEntity, async_setup_push_entities

PARALLEL_UPDATES = 1
CONTROLS = {
    "left": {"pan": -0.5},
    "right": {"pan": 0.5},
    "up": {"tilt": 0.5},
    "down": {"tilt": -0.5},
    "zoom_in": {"zoom": 0.5},
    "zoom_out": {"zoom": -0.5},
    "stop": {"mode": "stop"},
}


async def async_setup_entry(hass, entry, async_add_entities):
    async_setup_push_entities(
        hass,
        entry,
        async_add_entities,
        "button",
        lambda coordinator, entry, sid: [
            NVRPTZButton(coordinator, entry, sid, key) for key in CONTROLS
        ],
        feature="ptz_supported",
    )


class NVRPTZButton(NVRStreamEntity, ButtonEntity):
    def __init__(self, coordinator, entry, sid, key):
        super().__init__(coordinator, entry, sid, "ptz_" + key)
        self.key = key
        self._attr_translation_key = "ptz_" + key
        self._attr_icon = "mdi:stop" if key == "stop" else "mdi:camera-control"

    @property
    def available(self):
        return super().available and self.coordinator.data["streams"].get(
            self.stream_id, {}
        ).get("ptz_supported", False)

    async def async_press(self):
        await async_ptz(
            self.coordinator,
            self.stream_id,
            {
                "mode": "continuous",
                "duration": 0.5,
                **CONTROLS[self.key],
            },
        )
