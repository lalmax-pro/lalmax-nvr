"""UI setup, reauthentication and reconfiguration."""

import probatio as vol
from homeassistant.config_entries import ConfigFlow
from homeassistant.const import CONF_PASSWORD, CONF_URL, CONF_USERNAME
from homeassistant.helpers.aiohttp_client import async_get_clientsession
from homeassistant.helpers.selector import (
    TextSelector,
    TextSelectorConfig,
    TextSelectorType,
)

from .api import CannotConnect, InvalidAuth, NVRClient, normalize_url
from .const import CONF_RTSP_PASSWORD, CONF_RTSP_USERNAME, DOMAIN


class NVRConfigFlow(ConfigFlow, domain=DOMAIN):
    """Identify NVR instances by persisted UUID, not their network address."""

    VERSION = 1

    async def async_step_user(self, user_input=None):
        return await self._configure("user", user_input)

    async def async_step_reauth(self, entry_data):
        return await self.async_step_reauth_confirm()

    async def async_step_reauth_confirm(self, user_input=None):
        return await self._configure("reauth_confirm", user_input)

    async def async_step_reconfigure(self, user_input=None):
        return await self._configure("reconfigure", user_input)

    async def _configure(self, step, user_input):
        errors = {}
        entry = None
        if step == "reauth_confirm":
            entry = self._get_reauth_entry()
        elif step == "reconfigure":
            entry = self._get_reconfigure_entry()
        defaults = dict(entry.data) if entry else {}
        if user_input is not None:
            values = {**defaults, **user_input}
            defaults.update(user_input)
            try:
                values[CONF_URL] = normalize_url(values[CONF_URL])
                client = NVRClient(
                    async_get_clientsession(self.hass),
                    values[CONF_URL],
                    values[CONF_USERNAME],
                    values[CONF_PASSWORD],
                )
                data = await client.catalog()
            except InvalidAuth:
                errors["base"] = "invalid_auth"
            except CannotConnect:
                errors["base"] = "cannot_connect"
            except ValueError:
                errors[CONF_URL] = "invalid_url"
            else:
                await self.async_set_unique_id(data["instance_id"])
                if entry:
                    self._abort_if_unique_id_mismatch()
                    return self.async_update_reload_and_abort(entry, data=values)
                self._abort_if_unique_id_configured()
                return self.async_create_entry(title="lalmax-nvr", data=values)
        schema = {
            vol.Required(CONF_URL, default=defaults.get(CONF_URL, "http://")): str,
            vol.Required(CONF_USERNAME, default=defaults.get(CONF_USERNAME, "")): str,
            vol.Required(CONF_PASSWORD): TextSelector(
                TextSelectorConfig(type=TextSelectorType.PASSWORD)
            ),
            vol.Optional(
                CONF_RTSP_USERNAME, default=defaults.get(CONF_RTSP_USERNAME, "")
            ): str,
            vol.Optional(
                CONF_RTSP_PASSWORD, default=defaults.get(CONF_RTSP_PASSWORD, "")
            ): TextSelector(TextSelectorConfig(type=TextSelectorType.PASSWORD)),
        }
        return self.async_show_form(
            step_id=step, data_schema=vol.Schema(schema), errors=errors
        )
