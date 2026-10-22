"""Authenticated, read-only client for the NVR's publication catalog."""

import ipaddress
import json
from urllib.parse import quote

from aiohttp import BasicAuth, ClientError, ClientSession, ClientTimeout
from yarl import URL

from .const import CATALOG_PATH


class CannotConnect(Exception):
    """The NVR cannot be reached or returned an invalid response."""


class InvalidAuth(Exception):
    """The NVR rejected the HTTP credentials."""


def normalize_url(raw: str) -> str:
    """Require an explicit HTTP(S) origin, optionally with a proxy path."""
    url = URL(raw.strip())
    if (
        url.scheme not in ("http", "https")
        or not url.host
        or url.user is not None
        or url.query_string
        or url.fragment
    ):
        raise ValueError("Expected an HTTP(S) URL without credentials or query")
    return str(url).rstrip("/")


class NVRClient:
    """Never forward HTTP account credentials to RTSP or arbitrary media URLs."""

    def __init__(
        self, session: ClientSession, url: str, username: str, password: str
    ) -> None:
        self.session = session
        self.url = normalize_url(url)
        self.auth = BasicAuth(username, password)

    async def catalog(self) -> dict:
        """Fetch all published streams in a single stable catalog."""
        try:
            async with self.session.get(
                f"{self.url}/{CATALOG_PATH}",
                auth=self.auth,
                allow_redirects=False,
                timeout=ClientTimeout(total=15),
            ) as response:
                if response.status in (401, 403):
                    raise InvalidAuth
                if response.status != 200:
                    raise CannotConnect(f"NVR HTTP status {response.status}")
                data = await response.json()
            if (
                not isinstance(data, dict)
                or not isinstance(data.get("instance_id"), str)
                or not data["instance_id"]
                or not isinstance(data.get("enabled"), bool)
                or not isinstance(data.get("streams"), list)
            ):
                raise CannotConnect("Invalid NVR catalog")
            streams = {}
            for stream in data["streams"]:
                if (
                    not isinstance(stream, dict)
                    or not isinstance(stream.get("stream_id"), str)
                    or not stream["stream_id"]
                    or not isinstance(stream.get("active"), bool)
                    or not isinstance(stream.get("rtsp_url"), str)
                ):
                    raise CannotConnect("Invalid NVR stream")
                media_url = URL(stream["rtsp_url"])
                if not stream["rtsp_url"]:
                    streams[stream["stream_id"]] = dict(stream, active=False)
                    continue
                if media_url.scheme != "rtsp" or not media_url.host:
                    raise CannotConnect("Invalid NVR playback URL")
                stream = dict(stream)
                # Embedded installations commonly advertise loopback by default.
                try:
                    local = ipaddress.ip_address(media_url.host)
                    replace_host = local.is_loopback or local.is_unspecified
                except ValueError:
                    replace_host = media_url.host == "localhost"
                if replace_host:
                    media_url = media_url.with_host(URL(self.url).host)
                stream["rtsp_url"] = str(media_url.with_user(None))
                streams[stream["stream_id"]] = stream
            data["streams"] = streams if data["enabled"] else {}
            return data
        except (ClientError, TimeoutError, ValueError, TypeError, KeyError) as err:
            raise CannotConnect("Unable to read NVR catalog") from err

    async def snapshot(self, stream_id: str) -> bytes | None:
        """Fetch only the authenticated NVR snapshot endpoint."""
        try:
            async with self.session.get(
                f"{self.url}/{CATALOG_PATH}/streams/{quote(stream_id, safe='')}/snapshot",
                auth=self.auth,
                allow_redirects=False,
                timeout=ClientTimeout(total=15),
            ) as response:
                if response.status != 200:
                    return None
                if not response.headers.get("Content-Type", "").startswith("image/"):
                    return None
                return await response.read()
        except (TimeoutError, ClientError):
            return None

    async def events(self):
        """Read the publication-scoped live feed; None marks a connection."""
        try:
            async with self.session.get(
                f"{self.url}/{CATALOG_PATH}/events",
                auth=self.auth,
                allow_redirects=False,
                timeout=ClientTimeout(total=None, sock_connect=15, sock_read=45),
            ) as response:
                if response.status in (401, 403):
                    raise InvalidAuth
                if response.status != 200 or not response.headers.get(
                    "Content-Type", ""
                ).startswith("text/event-stream"):
                    raise CannotConnect("NVR event feed unavailable")
                yield None
                data = []
                async for raw in response.content:
                    line = raw.decode("utf-8").rstrip("\r\n")
                    if not line:
                        if data:
                            try:
                                event = json.loads("\n".join(data))
                            except (ValueError, TypeError):
                                event = None
                            if isinstance(event, dict):
                                yield event
                        data = []
                    elif line.startswith("data:"):
                        data.append(line[5:].lstrip(" "))
                        if sum(map(len, data)) > 65536:
                            raise CannotConnect("NVR event exceeds maximum size")
        except (ClientError, TimeoutError, ValueError) as err:
            raise CannotConnect("NVR event connection interrupted") from err
