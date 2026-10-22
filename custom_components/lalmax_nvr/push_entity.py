"""Shared identity and lifecycle for publication-scoped event entities."""

from homeassistant.const import CONF_URL
from homeassistant.core import callback
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN


def async_setup_push_entities(hass, entry, async_add_entities, platform, factory):
    """Discover entities as publications are added or removed from the registry."""
    coordinator = entry.runtime_data
    known = set()

    @callback
    def discover():
        if not coordinator.data.get("events_available"):
            return
        registry = er.async_get(hass)
        entities = []
        for sid in coordinator.data["streams"]:
            for entity in factory(coordinator, entry, sid):
                uid = entity.unique_id
                if uid not in known or not registry.async_get_entity_id(
                    platform, DOMAIN, uid
                ):
                    known.add(uid)
                    entities.append(entity)
        if entities:
            async_add_entities(entities)

    entry.async_on_unload(coordinator.async_add_listener(discover))
    discover()


class NVRPushEntity(CoordinatorEntity):
    """Use the camera's device, with availability tied to the event connection."""

    _attr_has_entity_name = True

    def __init__(self, coordinator, entry, stream_id, suffix):
        super().__init__(coordinator)
        self.entry = entry
        self.stream_id = stream_id
        self._attr_unique_id = f"{entry.unique_id}:{stream_id}:{suffix}"

    @property
    def available(self):
        return (
            super().available
            and self.coordinator.events_connected
            and self.stream_id in self.coordinator.data["streams"]
        )

    @property
    def device_info(self):
        return DeviceInfo(
            identifiers={(DOMAIN, f"{self.entry.unique_id}:{self.stream_id}")},
            name=self.coordinator.data["streams"].get(self.stream_id, {}).get("name")
            or self.stream_id,
            manufacturer="lalmax-nvr",
            model="Published stream",
            configuration_url=self.entry.data[CONF_URL],
        )

    async def async_added_to_hass(self):
        await super().async_added_to_hass()
        self.async_on_remove(
            self.coordinator.async_add_event_listener(self._handle_event)
        )

    @callback
    def _handle_event(self, event):
        self.async_write_ha_state()
