"""Validate native HEVC snapshot, HLS and recording through real HA.

Requires the isolated runtime with ha-test-second publishing HEVC/AAC. Uses
existing FFmpeg-generated input, never installs/transcodes media at runtime.
Enables its recording plan temporarily; run only against isolated test data.
"""

import argparse
import asyncio
import io
import json
import pathlib
import time
from urllib.parse import urljoin

import aiohttp
import av
import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
root = parser.parse_args().runtime.resolve()
c = json.loads((root / "credentials.json").read_text())
tok = json.loads((root / "ha-token.json").read_text())["access_token"]
eid = json.loads((root / "entry.json").read_text())["entry_id"]
H = "http://127.0.0.1:18123"
N = "http://127.0.0.1:19090"
sid = "ha-test-second"
entity = "camera.ha_test_second"
nvr = requests.Session()
nvr.auth = c["username"], c["password"]
ha = requests.Session()
ha.headers["Authorization"] = "Bearer " + tok


async def command(body):
    async with aiohttp.ClientSession() as s, s.ws_connect(H + "/api/websocket") as ws:
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"
        await ws.send_json({"id": 1, **body})
        r = await ws.receive_json(timeout=40)
        assert r.get("success"), r.get("error")
        return r["result"]


def req(s, m, u, **kw):
    r = s.request(m, u, timeout=45, **kw)
    assert r.ok, (r.status_code, u)
    return r


def wait(fn, timeout=100):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if v := fn():
                return v
        except (AssertionError, requests.RequestException):
            pass
        time.sleep(1)
    raise AssertionError("timeout")


state = wait(
    lambda: (
        x
        if (x := req(ha, "GET", H + "/api/states/" + entity).json())["attributes"].get(
            "video_codec"
        )
        == "H265"
        else None
    )
)
capabilities = asyncio.run(
    command({"type": "camera/capabilities", "entity_id": entity})
)
assert "web_rtc" in capabilities["frontend_stream_types"]
assert "hls" in capabilities["frontend_stream_types"]
print(
    "PASS HA tracks actual HEVC codec and advertises WebRTC alongside HLS", flush=True
)
r = req(ha, "GET", H + "/api/camera_proxy/" + entity)
assert r.content.startswith(b"\xff\xd8")
print("PASS actual HEVC JPEG through HA", len(r.content), flush=True)
info = asyncio.run(
    command({"type": "camera/stream", "entity_id": entity, "format": "hls"})
)
u = urljoin(H, info["url"])
master = req(ha, "GET", u).text
print("HLS master", master, flush=True)
assert "hvc1." in master or "hev1." in master
u = urljoin(u, next(l for l in master.splitlines() if l and not l.startswith("#")))
playlist = wait(lambda: v if "#EXTINF" in (v := req(ha, "GET", u).text) else None, 40)
(root / "hevc-playlist.m3u8").write_text(playlist)
init = next(
    l.split('URI="', 1)[1].split('"', 1)[0]
    for l in playlist.splitlines()
    if l.startswith("#EXT-X-MAP")
)
seg = next(l for l in playlist.splitlines() if l and not l.startswith("#"))
media = (
    req(ha, "GET", urljoin(u, init)).content + req(ha, "GET", urljoin(u, seg)).content
)
with av.open(io.BytesIO(media)) as v:
    assert v.streams.video[0].codec_context.name == "hevc"
    assert next(v.decode(video=0)).width == 640
with av.open(io.BytesIO(media)) as v:
    assert next(v.decode(audio=0)).samples > 0
print("PASS actual HA HLS remux decodes HEVC + AAC", len(media), flush=True)
cutoff = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
plans = req(nvr, "GET", N + "/api/recording-plans").json()
print("plans shape", type(plans).__name__, flush=True)
(root / "hevc-cutoff.json").write_text(json.dumps({"since": cutoff}))
plan = next(
    p
    for p in (plans if isinstance(plans, list) else plans["plans"])
    if p["stream_id"] == sid
)
r = req(
    nvr, "PUT", N + "/api/recording-plans/" + plan["id"], json={**plan, "enabled": True}
)
try:
    base = N + "/api/integrations/home-assistant/streams/" + sid + "/recordings"
    rows = wait(
        lambda: [
            v
            for v in req(nvr, "GET", base).json()["recordings"]
            if v["started_at"] > cutoff
        ],
        110,
    )
    rid = rows[0]["id"]
    url = asyncio.run(
        command(
            {
                "type": "media_source/resolve_media",
                "media_content_id": "media-source://lalmax_nvr/"
                + eid
                + "/"
                + sid
                + "/clip/"
                + rid,
            }
        )
    )["url"]
    r = requests.get(urljoin(H, url), timeout=30)
    assert r.ok
    with av.open(io.BytesIO(r.content)) as v:
        assert v.streams.video[0].codec_context.name == "hevc"
        assert next(v.decode(video=0)).width == 640
        v.seek(10_000_000)
        assert next(v.decode(video=0)).width == 640
    with av.open(io.BytesIO(r.content)) as v:
        assert next(v.decode(audio=0)).samples > 0
    print("PASS HA recording HEVC + AAC decode and seek", len(r.content), flush=True)
finally:
    req(
        nvr,
        "PUT",
        N + "/api/recording-plans/" + plan["id"],
        json=plan,
    )
