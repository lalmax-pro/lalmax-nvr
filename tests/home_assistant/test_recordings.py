"""Exercise recording browsing and byte-range proxying with real HTTP bytes."""

from types import SimpleNamespace
from unittest.mock import patch

import pytest
from aiohttp import ClientSession, web
from homeassistant.components.http import KEY_HASS
from homeassistant.components.media_source import MediaSourceError

from custom_components.lalmax_nvr.api import NVRClient
from custom_components.lalmax_nvr.media_source import NVRMediaSource, NVRRecordingView


@pytest.mark.asyncio
async def test_browse_pagination_and_signed_resolution():
    class Client:
        async def recording_json(self, sid, recording_id="", offset=0):
            assert sid == "published"
            if recording_id:
                assert recording_id == "clip"
                return {"id": recording_id}
            return {
                "recordings": [
                    {"id": "clip", "started_at": "2026-10-22T00:00:00Z", "duration": 30}
                ],
                "total": 125,
            }

    entry = SimpleNamespace(
        entry_id="entry",
        title="NVR",
        runtime_data=SimpleNamespace(
            data={"streams": {"published": {"name": "Door"}}}, client=Client()
        ),
    )
    hass = SimpleNamespace(
        config_entries=SimpleNamespace(async_loaded_entries=lambda domain: [entry])
    )
    source = NVRMediaSource(hass)
    page = await source.async_browse_media(
        SimpleNamespace(identifier="entry/published")
    )
    assert len(page.children) == 2
    assert page.children[-1].identifier == "entry/published/page/100"
    with patch(
        "custom_components.lalmax_nvr.media_source.async_sign_path",
        lambda hass, path, duration: path + "?authSig=HA-only",
    ):
        media = await source.async_resolve_media(
            SimpleNamespace(identifier="entry/published/clip/clip")
        )
    assert (
        media.url == "/api/lalmax_nvr/recordings/entry/published/clip?authSig=HA-only"
    )
    assert media.mime_type == "video/mp4"
    for identifier in ["other/published", "entry/hidden", "entry/published/page/-1"]:
        with pytest.raises(MediaSourceError):
            await source.async_browse_media(SimpleNamespace(identifier=identifier))


@pytest.mark.asyncio
async def test_streaming_proxy_forwards_ranges_and_nvr_revocation(aiohttp_server):
    revoked = False

    async def upstream(request):
        assert request.headers["Authorization"].startswith("Basic ")
        if revoked:
            raise web.HTTPNotFound
        assert request.headers["Range"] == "bytes=4-7"
        return web.Response(
            body=b"4567",
            status=206,
            headers={"Content-Range": "bytes 4-7/16", "Accept-Ranges": "bytes"},
        )

    nvr_app = web.Application()
    nvr_app.router.add_get(
        "/api/integrations/home-assistant/streams/published/recordings/clip/file",
        upstream,
    )
    nvr_server = await aiohttp_server(nvr_app)
    async with ClientSession() as session:
        client = NVRClient(session, str(nvr_server.make_url("")), "reader", "private")
        entry = SimpleNamespace(
            entry_id="entry",
            runtime_data=SimpleNamespace(
                data={"streams": {"published": {}}}, client=client
            ),
        )
        hass = SimpleNamespace(
            config_entries=SimpleNamespace(async_loaded_entries=lambda domain: [entry])
        )
        view = NVRRecordingView()

        async def proxy(request):
            return await view.get(request, "entry", "published", "clip")

        app = web.Application()
        app[KEY_HASS] = hass
        app.router.add_get("/proxy", proxy)
        server = await aiohttp_server(app)
        async with session.get(
            server.make_url("/proxy"), headers={"Range": "bytes=4-7"}
        ) as response:
            assert response.status == 206
            assert response.headers["Content-Range"] == "bytes 4-7/16"
            assert await response.read() == b"4567"
        revoked = True
        async with session.get(
            server.make_url("/proxy"), headers={"Range": "bytes=4-7"}
        ) as response:
            assert response.status == 404
