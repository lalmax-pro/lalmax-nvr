"""One catalog poll for all cameras."""

import asyncio
import logging
from collections import deque
from datetime import timedelta

from homeassistant.exceptions import ConfigEntryAuthFailed
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .api import CannotConnect, InvalidAuth, NVRClient
from .const import DOMAIN, EVENT_TYPES


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
        self.events_connected = False
        self._event_listeners = set()
        self._event_task = None
        self._seen_events = deque(maxlen=512)

    def async_add_event_listener(self, listener):
        """Subscribe on HA's event loop; remove the callback with the entity."""
        self._event_listeners.add(listener)
        return lambda: self._event_listeners.discard(listener)

    def _notify_event(self, event=None):
        for listener in tuple(self._event_listeners):
            listener(event)

    def async_start_events(self):
        if self.data.get("events_available"):
            self._event_task = self.config_entry.async_create_background_task(
                self.hass, self._listen_events(), "lalmax_nvr live events"
            )

    def _accept_event(self, event):
        if (
            event.get("instance_id") != self.config_entry.unique_id
            or event.get("stream_id") not in self.data["streams"]
            or type(event.get("id")) is not int
            or event["id"] <= 0
            or event.get("type") not in EVENT_TYPES
            or not isinstance(event.get("labels"), list)
            or not all(isinstance(label, str) for label in event["labels"])
        ):
            return
        key = (event["id"], event["stream_id"])
        if key in self._seen_events:
            return
        self._seen_events.append(key)
        # Own identifiers let automations distinguish multiple NVR instances.
        event = {**event, "config_entry_id": self.config_entry.entry_id}
        self.hass.bus.async_fire("lalmax_nvr_event", event)
        self._notify_event(event)

    async def _listen_events(self):
        delay = 1
        while True:
            try:
                async for event in self.client.events():
                    if event is None:
                        self.events_connected = True
                        self._notify_event()
                        delay = 1
                    else:
                        self._accept_event(event)
            except InvalidAuth:
                await self.async_request_refresh()
                return
            except CannotConnect:
                pass
            finally:
                self.events_connected = False
                self._notify_event()
            await asyncio.sleep(delay)
            delay = min(delay * 2, 30)

    async def async_shutdown(self):
        if provider := getattr(self, "webrtc", None):
            await provider.async_shutdown()
        if self._event_task:
            self._event_task.cancel()
            await asyncio.gather(self._event_task, return_exceptions=True)
            self._event_task = None
        await super().async_shutdown()

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
