"""Home Assistant integration for published lalmax-nvr streams."""

from homeassistant.const import CONF_PASSWORD, CONF_URL, CONF_USERNAME, Platform
from homeassistant.helpers import config_validation as cv
from homeassistant.helpers.aiohttp_client import async_get_clientsession

from .api import NVRClient
from .const import DOMAIN
from .coordinator import NVRCoordinator

CONFIG_SCHEMA = cv.config_entry_only_config_schema(DOMAIN)
PLATFORMS = [Platform.CAMERA, Platform.EVENT, Platform.BINARY_SENSOR]


async def async_setup_entry(hass, entry) -> bool:
    """Validate availability before forwarding the camera platform."""
    client = NVRClient(
        async_get_clientsession(hass),
        entry.data[CONF_URL],
        entry.data[CONF_USERNAME],
        entry.data[CONF_PASSWORD],
    )
    coordinator = NVRCoordinator(hass, entry, client)
    await coordinator.async_config_entry_first_refresh()
    entry.runtime_data = coordinator
    await hass.config_entries.async_forward_entry_setups(entry, PLATFORMS)
    coordinator.async_start_events()
    return True


async def async_unload_entry(hass, entry) -> bool:
    """Unload cameras and their catalog listeners."""
    if not await hass.config_entries.async_unload_platforms(entry, PLATFORMS):
        return False
    await entry.runtime_data.async_shutdown()
    return True


async def async_remove_config_entry_device(hass, entry, device) -> bool:
    """Allow HA to clean up devices only after their publication is withdrawn."""
    current = entry.runtime_data.data["streams"]
    identifiers = {(DOMAIN, f"{entry.unique_id}:{sid}") for sid in current}
    return not bool(device.identifiers & identifiers)
