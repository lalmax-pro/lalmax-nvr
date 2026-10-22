"""One catalog poll for all cameras."""

import logging
from datetime import timedelta

from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .api import CannotConnect, InvalidAuth, NVRClient
from .const import DOMAIN


class NVRCoordinator(DataUpdateCoordinator):
    """Keep publication and connection failures distinct from stream outages."""

    def __init__(self, hass, entry, client: NVRClient) -> None:
        super().__init__(
            hass,
            logging.getLogger(__name__),
            config_entry=entry,
            name=DOMAIN,
            update_interval=timedelta(seconds=30),
            always_update=False,
        )
        self.client = client

    async def _async_update_data(self) -> dict:
        try:
            data = await self.client.catalog()
        except InvalidAuth as err:
            raise ConfigEntryAuthFailed("NVR credentials were rejected") from err
        except CannotConnect as err:
            raise UpdateFailed("NVR catalog is unavailable") from err
        if data["instance_id"] != self.config_entry.unique_id:
            raise UpdateFailed("The address now belongs to a different NVR instance")
        return data
