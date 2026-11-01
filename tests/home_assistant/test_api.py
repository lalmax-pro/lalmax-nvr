"""Exercise the HA client against real HTTP responses, including auth failures."""

import pytest
from aiohttp import BasicAuth, ClientSession, web

from custom_components.lalmax_nvr.api import (
    CannotConnect,
    InvalidAuth,
    NVRClient,
    normalize_url,
)


@pytest.mark.asyncio
async def test_catalog_auth_large_directory_and_loopback(aiohttp_server):
    async def catalog(request):
        assert BasicAuth.decode(request.headers["Authorization"]) == BasicAuth(
            "viewer", "private"
        )
        return web.json_response(
            {
                "instance_id": "stable-instance",
                "enabled": True,
                "streams": [
                    {
                        "stream_id": f"s-{i}",
                        "active": True,
                        "rtsp_url": f"rtsp://localhost:15544/custom/s-{i}?token=media-only",
                    }
                    for i in range(125)
                ],
            }
        )

    app = web.Application()
    app.router.add_get("/api/integrations/home-assistant", catalog)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        result = await NVRClient(
            session, str(server.make_url("")), "viewer", "private"
        ).catalog()
    assert len(result["streams"]) == 125
    assert (
        result["streams"]["s-0"]["rtsp_url"]
        == "rtsp://127.0.0.1:15544/custom/s-0?token=media-only"
    )
    assert "private" not in str(result)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "status,error",
    [
        (401, InvalidAuth),
        (403, InvalidAuth),
        (503, CannotConnect),
        (302, CannotConnect),
    ],
)
async def test_catalog_errors_do_not_follow_redirects(aiohttp_server, status, error):
    async def catalog(request):
        return web.Response(status=status, headers={"Location": "/must-not-follow"})

    app = web.Application()
    app.router.add_get("/api/integrations/home-assistant", catalog)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        with pytest.raises(error):
            await NVRClient(
                session, str(server.make_url("")), "viewer", "secret"
            ).catalog()


@pytest.mark.asyncio
async def test_disabled_and_unplayable_publications(aiohttp_server):
    payload = {
        "instance_id": "id",
        "enabled": True,
        "streams": [{"stream_id": "offline", "active": True, "rtsp_url": ""}],
    }

    async def catalog(request):
        return web.json_response(payload)

    app = web.Application()
    app.router.add_get("/api/integrations/home-assistant", catalog)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        client = NVRClient(session, str(server.make_url("")), "viewer", "secret")
        result = await client.catalog()
        assert not result["streams"]["offline"]["active"]
        payload["enabled"] = False
        assert (await client.catalog())["streams"] == {}


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "payload",
    [
        {},
        {
            "instance_id": "id",
            "enabled": True,
            "streams": [
                {"stream_id": "a", "active": True, "rtsp_url": "http://remote/private"}
            ],
        },
    ],
)
async def test_invalid_catalog(aiohttp_server, payload):
    async def catalog(request):
        return web.json_response(payload)

    app = web.Application()
    app.router.add_get("/api/integrations/home-assistant", catalog)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        with pytest.raises(CannotConnect):
            await NVRClient(
                session, str(server.make_url("")), "viewer", "secret"
            ).catalog()


@pytest.mark.parametrize(
    "url",
    [
        "rtsp://host/live/a",
        "http://user:secret@host",
        "http://host?token=x",
        "http://host/#x",
        "host:9090",
    ],
)
def test_url_validation(url):
    with pytest.raises(ValueError):
        normalize_url(url)


@pytest.mark.asyncio
async def test_snapshot_scoped_endpoint(aiohttp_server):
    async def snapshot(request):
        assert request.match_info["stream_id"] == "gb:1"
        assert BasicAuth.decode(request.headers["Authorization"]).login == "viewer"
        return web.Response(body=b"\xff\xd8jpeg", content_type="image/jpeg")

    app = web.Application()
    app.router.add_get(
        "/api/integrations/home-assistant/streams/{stream_id}/snapshot", snapshot
    )
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        assert (
            await NVRClient(
                session, str(server.make_url("")), "viewer", "secret"
            ).snapshot("gb:1")
            == b"\xff\xd8jpeg"
        )
