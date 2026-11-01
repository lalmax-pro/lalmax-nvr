"""Browse NVR recordings and proxy authenticated, seekable MP4 playback."""

from datetime import timedelta
from urllib.parse import quote, unquote

from aiohttp import ClientError, ClientTimeout, web
from homeassistant.components.http import KEY_HASS, HomeAssistantView
from homeassistant.components.http.auth import async_sign_path
from homeassistant.components.media_player import BrowseError, MediaClass, MediaType
from homeassistant.components.media_source import (
    BrowseMediaSource,
    MediaSource,
    PlayMedia,
    Unresolvable,
)

from .api import CannotConnect, InvalidAuth
from .const import DOMAIN


class NVRMediaError(BrowseError, Unresolvable):
    """Report the same scoped-resource error in browsing and resolution."""


def loaded_entry(hass, entry_id):
    """Never resolve an unloaded entry or another integration's entry."""
    return next(
        (
            e
            for e in hass.config_entries.async_loaded_entries(DOMAIN)
            if e.entry_id == entry_id
        ),
        None,
    )


def folder(identifier, title, children):
    return BrowseMediaSource(
        domain=DOMAIN,
        identifier=identifier,
        title=title,
        media_class=MediaClass.DIRECTORY,
        media_content_type=MediaType.VIDEO,
        can_play=False,
        can_expand=True,
        children=children,
    )


async def async_get_media_source(hass):
    return NVRMediaSource(hass)


class NVRMediaSource(MediaSource):
    name = "lalmax-nvr recordings"

    def __init__(self, hass):
        super().__init__(DOMAIN)
        self.hass = hass

    def parse(self, identifier):
        parts = identifier.split("/") if identifier else []
        if not parts:
            return None, None, None, None
        entry = loaded_entry(self.hass, parts[0])
        if entry is None:
            raise NVRMediaError("NVR integration is not loaded")
        if len(parts) == 1:
            return entry, None, None, None
        sid = unquote(parts[1])
        if sid not in entry.runtime_data.data["streams"]:
            raise NVRMediaError("Stream is no longer published")
        if len(parts) == 2:
            return entry, sid, "page", "0"
        if len(parts) != 4 or parts[2] not in ("page", "clip"):
            raise NVRMediaError("Invalid recording identifier")
        return entry, sid, parts[2], unquote(parts[3])

    async def async_browse_media(self, item):
        entry, sid, kind, value = self.parse(item.identifier)
        if entry is None:
            return folder(
                "",
                self.name,
                [
                    folder(e.entry_id, e.title, [])
                    for e in self.hass.config_entries.async_loaded_entries(DOMAIN)
                ],
            )
        if sid is None:
            return folder(
                entry.entry_id,
                entry.title,
                [
                    folder(
                        f"{entry.entry_id}/{quote(s, safe='')}",
                        data.get("name") or s,
                        [],
                    )
                    for s, data in entry.runtime_data.data["streams"].items()
                ],
            )
        if kind != "page" or not value.isdecimal() or int(value) > 1000000:
            raise NVRMediaError("Invalid recording page")
        offset = int(value)
        try:
            data = await entry.runtime_data.client.recording_json(sid, offset=offset)
            rows, total = data["recordings"], data["total"]
            if not isinstance(rows, list) or type(total) is not int:
                raise ValueError
            prefix = f"{entry.entry_id}/{quote(sid, safe='')}"
            children = [
                BrowseMediaSource(
                    domain=DOMAIN,
                    identifier=f"{prefix}/clip/{quote(rec['id'], safe='')}",
                    title=f"{rec['started_at']} · {rec['duration']:.0f}s",
                    media_class=MediaClass.VIDEO,
                    media_content_type="video/mp4",
                    can_play=True,
                    can_expand=False,
                )
                for rec in rows
            ]
            if offset + 100 < total:
                children.append(
                    folder(f"{prefix}/page/{offset + 100}", "Next 100 recordings", [])
                )
            return folder(
                item.identifier,
                entry.runtime_data.data["streams"][sid].get("name") or sid,
                children,
            )
        except (CannotConnect, InvalidAuth, KeyError, TypeError, ValueError) as err:
            raise NVRMediaError("Unable to browse NVR recordings") from err

    async def async_resolve_media(self, item):
        entry, sid, kind, value = self.parse(item.identifier)
        if not entry or not sid or kind != "clip" or not value:
            raise NVRMediaError("Choose a recording to play")
        try:
            await entry.runtime_data.client.recording_json(sid, value)
        except (CannotConnect, InvalidAuth) as err:
            raise NVRMediaError("Recording is unavailable") from err
        path = "/api/lalmax_nvr/recordings/" + "/".join(
            quote(s, safe="") for s in (entry.entry_id, sid, value)
        )
        return PlayMedia(
            async_sign_path(self.hass, path, timedelta(hours=1)), "video/mp4"
        )


class NVRRecordingView(HomeAssistantView):
    """Stream bytes with bounded memory; signed HA URLs contain no NVR secrets."""

    url = "/api/lalmax_nvr/recordings/{entry_id}/{stream_id}/{recording_id}"
    name = "api:lalmax_nvr:recordings"
    requires_auth = True

    async def get(self, request, entry_id, stream_id, recording_id):
        return await self._proxy(request, entry_id, stream_id, recording_id)

    async def head(self, request, entry_id, stream_id, recording_id):
        return await self._proxy(request, entry_id, stream_id, recording_id)

    async def _proxy(self, request, entry_id, stream_id, recording_id):
        entry = loaded_entry(request.app[KEY_HASS], entry_id)
        if entry is None or stream_id not in entry.runtime_data.data["streams"]:
            raise web.HTTPNotFound
        client = entry.runtime_data.client
        headers = {"Accept-Encoding": "identity"}
        headers.update(
            {
                key: request.headers[key]
                for key in ("Range", "If-Range")
                if key in request.headers
            }
        )
        try:
            async with client.session.request(
                request.method,
                client.recording_url(stream_id, recording_id) + "/file",
                auth=client.auth,
                headers=headers,
                allow_redirects=False,
                timeout=ClientTimeout(total=None, sock_connect=15, sock_read=30),
            ) as upstream:
                if upstream.status not in (200, 206, 304, 416):
                    if upstream.status == 404:
                        raise web.HTTPNotFound
                    raise web.HTTPBadGateway
                forwarded = {
                    key: upstream.headers[key]
                    for key in (
                        "Content-Length",
                        "Content-Range",
                        "Accept-Ranges",
                        "ETag",
                        "Last-Modified",
                    )
                    if key in upstream.headers
                }
                forwarded.update(
                    {"Content-Type": "video/mp4", "Cache-Control": "no-store"}
                )
                response = web.StreamResponse(status=upstream.status, headers=forwarded)
                await response.prepare(request)
                if request.method != "HEAD":
                    async for chunk in upstream.content.iter_chunked(65536):
                        await response.write(chunk)
                await response.write_eof()
                return response
        except (ClientError, TimeoutError) as err:
            raise web.HTTPBadGateway from err
