"""Validate streaming, duplicate suppression and detection expiry."""

import asyncio
from collections import deque
from types import SimpleNamespace
from unittest.mock import Mock, patch

import pytest
from aiohttp import ClientSession, web

from custom_components.lalmax_nvr.api import NVRClient
from custom_components.lalmax_nvr.binary_sensor import NVRDetection
from custom_components.lalmax_nvr.coordinator import NVRCoordinator


@pytest.mark.asyncio
async def test_sse_frames_heartbeats_and_malformed_data(aiohttp_server):
    async def stream(request):
        response = web.StreamResponse(headers={"Content-Type": "text/event-stream"})
        await response.prepare(request)
        for chunk in [
            b": ping\n\nevent: nvr\ndata: {bad}\n\n",
            b'data: {"id":',
            b"12}\n\n",
        ]:
            await response.write(chunk)
        return response

    app = web.Application()
    app.router.add_get("/api/integrations/home-assistant/events", stream)
    server = await aiohttp_server(app)
    async with ClientSession() as session:
        client = NVRClient(session, str(server.make_url("")), "viewer", "secret")
        assert [event async for event in client.events()] == [None, {"id": 12}]


def test_scope_instance_and_duplicate_suppression():
    coordinator = object.__new__(NVRCoordinator)
    coordinator.config_entry = SimpleNamespace(unique_id="nvr", entry_id="entry")
    coordinator.data = {"streams": {"published": {}}}
    coordinator.hass = SimpleNamespace(bus=SimpleNamespace(async_fire=Mock()))
    coordinator._seen_events = deque(maxlen=512)
    coordinator._event_listeners = {Mock()}
    event = {
        "id": 1,
        "instance_id": "nvr",
        "stream_id": "published",
        "type": "ai_detection",
        "labels": ["person"],
    }
    for invalid in [
        {**event, "stream_id": "hidden"},
        {**event, "instance_id": "other"},
        {**event, "id": True},
        {**event, "labels": "person"},
    ]:
        coordinator._accept_event(invalid)
    coordinator.hass.bus.async_fire.assert_not_called()
    coordinator._accept_event(event)
    coordinator._accept_event(event)
    coordinator.hass.bus.async_fire.assert_called_once()


@pytest.mark.asyncio
async def test_detection_pulses_expire_and_disconnect_clears():
    coordinator = SimpleNamespace(
        data={"streams": {"stream": {}}},
        last_update_success=True,
        events_connected=True,
    )
    entity = NVRDetection(
        coordinator, SimpleNamespace(unique_id="nvr"), "stream", "person"
    )
    entity.hass = SimpleNamespace()
    entity.async_write_ha_state = Mock()
    callbacks = []

    def later(hass, delay, callback):
        callbacks.append(callback)
        return Mock()

    event = {"stream_id": "stream", "type": "ai_detection", "labels": ["person"]}
    with patch("custom_components.lalmax_nvr.binary_sensor.async_call_later", later):
        entity._handle_event({**event, "labels": ["car"]})
        assert not entity.is_on
        entity._handle_event(event)
        assert entity.is_on
        callbacks[-1]()
        assert not entity.is_on
        entity._handle_event(event)
        coordinator.events_connected = False
        entity._handle_event(None)
        assert not entity.is_on
        assert not entity.available
    await asyncio.sleep(0)
