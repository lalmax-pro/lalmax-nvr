"""Control permissions must not turn into HTTP reauthentication failures."""

from types import SimpleNamespace

import pytest
from aiohttp import ClientSession, web
from homeassistant.exceptions import HomeAssistantError

from custom_components.lalmax_nvr.api import NVRClient, PermissionDenied
from custom_components.lalmax_nvr.control import async_ptz


@pytest.mark.asyncio
async def test_readonly_control_permission_and_unpublished_guard(aiohttp_server):
    calls = []

    async def control(request):
        calls.append(await request.json())
        raise web.HTTPForbidden

    app = web.Application()
    app.router.add_post("/api/integrations/home-assistant/streams/main/ptz", control)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        client = NVRClient(session, str(server.make_url("")), "viewer", "secret")
        with pytest.raises(PermissionDenied):
            await client.ptz("main", {"mode": "stop"})
        coordinator = SimpleNamespace(
            client=client, data={"streams": {"main": {"ptz_supported": True}}}
        )
        with pytest.raises(HomeAssistantError, match="super administrator"):
            await async_ptz(coordinator, "main", {"mode": "stop"})
        with pytest.raises(HomeAssistantError, match="no supported PTZ"):
            await async_ptz(coordinator, "hidden", {"mode": "stop"})
        assert len(calls) == 2
