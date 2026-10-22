"""Scope and lifecycle checks for the optional WebRTC provider."""

import asyncio
from types import SimpleNamespace
from unittest.mock import AsyncMock

import pytest
from aiohttp import ClientSession, web

from custom_components.lalmax_nvr.api import CannotConnect, NVRClient
from custom_components.lalmax_nvr.webrtc import NVRWebRTCProvider


@pytest.mark.asyncio
async def test_provider_pending_close_shutdown_and_source_scope():
    ready, release = asyncio.Event(), asyncio.Event()

    async def negotiate(*args):
        ready.set()
        await release.wait()
        return "v=0", "signed-token"

    client = SimpleNamespace(
        create_webrtc=negotiate, close_webrtc=AsyncMock(return_value=True)
    )
    coordinator = SimpleNamespace(
        config_entry=SimpleNamespace(data={"low_latency": True}),
        data={
            "streams": {
                "main": {
                    "rtsp_url": "rtsp://nvr/live/main?token=media",
                    "active": True,
                    "webrtc_supported": True,
                }
            }
        },
        client=client,
        hass=SimpleNamespace(async_create_task=asyncio.create_task),
    )
    provider = NVRWebRTCProvider(coordinator)
    source = "rtsp://viewer:secret@nvr/live/main?token=media"
    assert provider.async_is_supported(source)
    assert not provider.async_is_supported("rtsp://other/live/main?token=media")
    assert not provider.async_is_supported("invalid")
    camera = SimpleNamespace(
        entity_id="camera.main", stream_source=AsyncMock(return_value=source)
    )
    messages = []
    task = asyncio.create_task(
        provider.async_handle_async_webrtc_offer(
            camera, "offer", "pending", messages.append
        )
    )
    await ready.wait()
    provider.async_close_session("pending")
    release.set()
    await task
    assert not messages and not provider.sessions
    client.close_webrtc.assert_awaited_once_with("main", "signed-token")
    await provider.async_handle_async_webrtc_offer(
        camera, "offer", "active", messages.append
    )
    assert len(messages) == 1 and "active" in provider.sessions
    await provider.async_unregister_camera(camera)
    await provider.async_shutdown()
    assert not provider.sessions and not provider.close_tasks
    assert client.close_webrtc.await_count == 2
    assert not provider.async_is_supported(source)


@pytest.mark.asyncio
async def test_client_chunked_answer_and_no_location_follow(aiohttp_server):
    closed = []
    mode = "valid"

    async def negotiate(request):
        if mode == "redirect":
            raise web.HTTPTemporaryRedirect("http://untrusted.invalid/secret")
        answer = web.StreamResponse(
            status=201,
            headers={"Location": "https://untrusted.invalid/session/signed-token"},
        )
        await answer.prepare(request)
        await answer.write(b"v=0\r\n")
        await asyncio.sleep(0.01)
        await answer.write(b"a=test\r\n" if mode == "valid" else b"x" * 262144)
        await answer.write_eof()
        return answer

    async def cleanup(request):
        closed.append(request.match_info["token"])
        return web.Response(status=204)

    app = web.Application()
    app.router.add_post(
        "/api/integrations/home-assistant/streams/main/webrtc", negotiate
    )
    app.router.add_delete(
        "/api/integrations/home-assistant/streams/main/webrtc/{token}", cleanup
    )
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        client = NVRClient(session, str(server.make_url("")), "viewer", "secret")
        answer, token = await client.create_webrtc("main", "offer")
        assert answer == "v=0\r\na=test\r\n" and token == "signed-token"
        assert await client.close_webrtc("main", token)
        mode = "large"
        with pytest.raises(CannotConnect):
            await client.create_webrtc("main", "offer")
        assert closed == ["signed-token", "signed-token"]
        mode = "redirect"
        with pytest.raises(CannotConnect):
            await client.create_webrtc("main", "offer")
