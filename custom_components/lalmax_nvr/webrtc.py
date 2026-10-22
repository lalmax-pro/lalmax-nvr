"""Optional WHEP provider retaining HA's HLS playback path."""

import asyncio

from homeassistant.components.camera.webrtc import (
    CameraWebRTCProvider,
    WebRTCAnswer,
    WebRTCError,
)
from homeassistant.core import callback
from yarl import URL

from .api import CannotConnect, InvalidAuth
from .const import CONF_LOW_LATENCY, DOMAIN


class NVRWebRTCProvider(CameraWebRTCProvider):
    def __init__(self, coordinator):
        self.coordinator = coordinator
        self.sessions = {}
        self.pending = {}
        self.close_tasks = set()
        self.closed = False

    @property
    def domain(self):
        return DOMAIN

    def _stream_id(self, source):
        if self.closed or not self.coordinator.config_entry.data.get(
            CONF_LOW_LATENCY, False
        ):
            return None
        try:
            source = str(URL(source).with_user(None))
        except (TypeError, ValueError):
            return None
        return next(
            (
                sid
                for sid, stream in self.coordinator.data["streams"].items()
                if stream.get("webrtc_supported")
                and stream.get("active")
                and stream.get("rtsp_url") == source
            ),
            None,
        )

    @callback
    def async_is_supported(self, stream_source):
        return self._stream_id(stream_source) is not None

    async def async_handle_async_webrtc_offer(
        self, camera, offer_sdp, session_id, send_message
    ):
        sid = self._stream_id(await camera.stream_source())
        if sid is None:
            send_message(
                WebRTCError(
                    "unavailable", "Published stream is not available for WebRTC"
                )
            )
            return
        self.pending[session_id] = camera.entity_id
        try:
            answer, token = await self.coordinator.client.create_webrtc(sid, offer_sdp)
            if self.closed or session_id not in self.pending:
                await self.coordinator.client.close_webrtc(sid, token)
                return
            self.sessions[session_id] = (camera.entity_id, sid, token)
            send_message(WebRTCAnswer(answer))
        except (CannotConnect, InvalidAuth):
            send_message(
                WebRTCError(
                    "negotiation_failed",
                    "NVR WebRTC negotiation failed; HLS remains available",
                )
            )
        finally:
            self.pending.pop(session_id, None)

    async def async_on_webrtc_candidate(self, session_id, candidate):
        # NVR returns a complete ICE answer. The controlling viewer initiates
        # connectivity checks; Pion learns peer-reflexive remote candidates.
        # WHEP PATCH is not required for this LAN path (verified without any
        # candidates in the initial offer). No separate candidate request is sent.
        return

    @callback
    def async_close_session(self, session_id):
        self.pending.pop(session_id, None)
        session = self.sessions.pop(session_id, None)
        if session is not None:
            _, sid, token = session
            task = self.coordinator.hass.async_create_task(
                self.coordinator.client.close_webrtc(sid, token)
            )
            self.close_tasks.add(task)
            task.add_done_callback(self.close_tasks.discard)

    async def async_unregister_camera(self, camera):
        for session_id, entity_id in tuple(self.pending.items()):
            if entity_id == camera.entity_id:
                self.async_close_session(session_id)
        for session_id, (entity_id, _, _) in tuple(self.sessions.items()):
            if entity_id == camera.entity_id:
                self.async_close_session(session_id)

    async def async_shutdown(self):
        self.closed = True
        self.pending.clear()
        for session_id in tuple(self.sessions):
            self.async_close_session(session_id)
        if self.close_tasks:
            await asyncio.gather(*tuple(self.close_tasks), return_exceptions=True)
