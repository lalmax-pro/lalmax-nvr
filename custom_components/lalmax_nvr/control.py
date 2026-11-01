"""Shared, explicit errors for HA actions and control buttons."""

from homeassistant.exceptions import HomeAssistantError

from .api import CannotConnect, InvalidAuth, PermissionDenied


async def async_ptz(coordinator, sid, command=None):
    if not coordinator.data["streams"].get(sid, {}).get("ptz_supported"):
        raise HomeAssistantError("This publication has no supported PTZ camera")
    try:
        return await coordinator.client.ptz(sid, command)
    except PermissionDenied as err:
        raise HomeAssistantError(
            "PTZ control requires an NVR super administrator account"
        ) from err
    except InvalidAuth as err:
        raise HomeAssistantError(
            "NVR credentials were rejected; update integration credentials"
        ) from err
    except CannotConnect as err:
        raise HomeAssistantError(
            "PTZ device, command or publication is unavailable"
        ) from err
