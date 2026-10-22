"""Live cameras backed by NVR RTSP redistribution."""

from homeassistant.components.camera import Camera, CameraEntityFeature
from homeassistant.const import CONF_URL
from homeassistant.core import callback
from homeassistant.helpers import device_registry as dr
from homeassistant.helpers import entity_registry as er
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity
from yarl import URL

from .const import CONF_RTSP_PASSWORD, CONF_RTSP_USERNAME, DOMAIN

PARALLEL_UPDATES = 0


async def async_setup_entry(hass, entry, async_add_entities) -> None:
    """Add newly published streams without reloading the integration."""
    coordinator = entry.runtime_data
    known = set()

    @callback
    def discover() -> None:
        registry = er.async_get(hass)
        new_ids = {
            sid
            for sid in coordinator.data["streams"]
            if sid not in known
            or registry.async_get_entity_id(
                "camera", DOMAIN, f"{entry.unique_id}:{sid}"
            )
            is None
        }
        known.update(new_ids)
        async_add_entities(
            [NVRCamera(coordinator, entry, sid) for sid in sorted(new_ids)]
        )

    entry.async_on_unload(coordinator.async_add_listener(discover))
    discover()


class NVRCamera(CoordinatorEntity, Camera):
    """A published stream keeps its entity identity across outages and renames."""

    _attr_supported_features = CameraEntityFeature.STREAM
    _attr_has_entity_name = True
    _attr_name = None
    _attr_brand = "lalmax-nvr"

    def __init__(self, coordinator, entry, stream_id: str) -> None:
        Camera.__init__(self)
        CoordinatorEntity.__init__(self, coordinator)
        self.entry = entry
        self.stream_id = stream_id
        self._last_source = self.stream_data.get("rtsp_url")
        self._attr_unique_id = f"{entry.unique_id}:{stream_id}"
        self.stream_options["rtsp_transport"] = "tcp"

    @property
    def stream_data(self) -> dict:
        return self.coordinator.data["streams"].get(self.stream_id, {})

    @property
    def available(self) -> bool:
        return super().available and bool(self.stream_data.get("active"))

    @property
    def device_info(self) -> DeviceInfo:
        return DeviceInfo(
            identifiers={(DOMAIN, self.unique_id)},
            name=self.stream_data.get("name") or self.stream_id,
            manufacturer="lalmax-nvr",
            model="Published stream",
            configuration_url=self.entry.data[CONF_URL],
        )

    @property
    def use_stream_for_stills(self) -> bool:
        return not self.coordinator.data.get("snapshot_available", False)

    async def stream_source(self) -> str | None:
        if not self.available:
            return None
        source = URL(self.stream_data["rtsp_url"])
        if username := self.entry.data.get(CONF_RTSP_USERNAME):
            source = source.with_user(username).with_password(
                self.entry.data.get(CONF_RTSP_PASSWORD, "")
            )
        return str(source)

    async def async_camera_image(self, width=None, height=None) -> bytes | None:
        if not self.available:
            return None
        return await self.coordinator.client.snapshot(self.stream_id)

    @callback
    def _handle_coordinator_update(self) -> None:
        registry = dr.async_get(self.hass)
        device = registry.async_get_device_by_identifier(
            (DOMAIN, self.unique_id), self.entry.entry_id
        )
        if device and self.stream_data:
            name = self.stream_data.get("name") or self.stream_id
            if device.name != name:
                registry.async_update_device(device.id, name=name)
        # Close an existing HA stream when its publication/source is revoked.
        if self.stream is not None:
            source = self.stream_data.get("rtsp_url")
            if not self.available or source != getattr(self, "_last_source", source):
                old_stream, self.stream = self.stream, None
                self.hass.async_create_task(self._stop_revoked_stream(old_stream))
        self._last_source = self.stream_data.get("rtsp_url")
        super()._handle_coordinator_update()

    async def _stop_revoked_stream(self, stream) -> None:
        """Revocation must also stop streams with HA preloading enabled."""
        preload = stream.dynamic_stream_settings.preload_stream
        stream.dynamic_stream_settings.preload_stream = False
        try:
            await stream.stop()
        finally:
            stream.dynamic_stream_settings.preload_stream = preload
