"""Exercise real NVR recording, native HA media browsing and MP4 playback.

Use only the prepared isolated runtime from live_smoke.py. Enables recording
plans for both synthetic streams and toggles publication. Requires PyAV and
valid runtime token files. Optional --since selects freshly recorded clips.
"""

import argparse
import asyncio
import io
import json
import pathlib
import time

import aiohttp
import av
import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
parser.add_argument("--since", default="")
args = parser.parse_args()
root = args.runtime.resolve()
c = json.loads((root / "credentials.json").read_text())
tok = json.loads((root / "ha-token.json").read_text())["access_token"]
eid = json.loads((root / "entry.json").read_text())["entry_id"]
N = "http://127.0.0.1:19090"
H = "http://127.0.0.1:18123"
nvr = requests.Session()
nvr.auth = c["username"], c["password"]
ha = requests.Session()
ha.headers["Authorization"] = "Bearer " + tok


def request(s, m, url, **kwargs):
    r = s.request(m, url, timeout=20, **kwargs)
    assert r.ok, (r.status_code, url)
    return r


def state(entity):
    return request(ha, "GET", H + "/api/states/" + entity).json()["state"]


def wait(check, timeout=80):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            if v := check():
                return v
        except (requests.RequestException, AssertionError):
            pass
        time.sleep(1)
    raise AssertionError("Timed out waiting")


def passed(name):
    p = root / "results.json"
    r = json.loads(p.read_text())
    r.append({"test": name, "result": "passed"})
    p.write_text(json.dumps(r, indent=2))
    print("PASS", name, flush=True)


def publish(enabled):
    request(
        nvr,
        "PUT",
        N + "/api/integrations/home-assistant/streams/ha-test-main",
        json={"enabled": enabled},
    )


def detection(sid, labels):
    request(
        nvr,
        "POST",
        N + "/api/ai/webhook",
        json={
            "camera_id": sid,
            "detections": [
                {"label": l, "confidence": 0.9, "box": [0.1, 0.1, 0.5, 0.9]}
                for l in labels
            ],
        },
    )


for sid in ["ha-test-main", "ha-test-second"]:
    request(
        nvr,
        "PUT",
        N + "/api/integrations/home-assistant/streams/" + sid,
        json={"enabled": True},
    )
    r = nvr.post(
        N + "/api/recording-plans",
        json={
            "stream_id": sid,
            "name": "HA playback test",
            "mode": "continuous",
            "enabled": True,
        },
        timeout=10,
    )
    assert r.status_code in (201, 409), r.status_code
print("Real recording plans enabled", flush=True)
base = N + "/api/integrations/home-assistant/streams/ha-test-main/recordings"
cutoff = args.since
rows = wait(
    lambda: [
        v
        for v in request(nvr, "GET", base).json()["recordings"]
        if not cutoff or v["started_at"] > cutoff
    ],
    100,
)
rid = rows[0]["id"]
assert "file_path" not in rows[0]
passed(
    "NVR records real synthetic H.264/AAC into MP4 and exposes scoped recording metadata"
)


async def commands():
    async with (
        aiohttp.ClientSession() as s,
        s.ws_connect(H + "/api/websocket") as ws,
    ):
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"

        async def cmd(i, body):
            await ws.send_json({"id": i, **body})
            r = await ws.receive_json(timeout=30)
            assert r.get("success"), r.get("error")
            return r["result"]

        root = await cmd(
            1,
            {
                "type": "media_source/browse_media",
                "media_content_id": "media-source://lalmax_nvr",
            },
        )
        assert root["children"]
        entries = await cmd(
            2,
            {
                "type": "media_source/browse_media",
                "media_content_id": "media-source://lalmax_nvr/" + eid,
            },
        )
        assert entries["children"]
        clips = await cmd(
            3,
            {
                "type": "media_source/browse_media",
                "media_content_id": "media-source://lalmax_nvr/"
                + eid
                + "/ha-test-main",
            },
        )
        clip = next(
            c
            for c in clips["children"]
            if c["can_play"] and rid in c["media_content_id"]
        )
        assert clip["media_content_type"] == "video/mp4"
        data = await cmd(
            4,
            {
                "type": "media_source/resolve_media",
                "media_content_id": clip["media_content_id"],
            },
        )
        assert data["mime_type"] == "video/mp4"
        return data["url"]


url = asyncio.run(commands())
url = H + url if url.startswith("/") else url
assert "authSig=" in url
assert c["password"] not in url
assert "@" not in url
passed(
    "Native HA media browser lists NVR instance/streams/clips and resolves a signed HA playback URL"
)
r = requests.get(url, timeout=30)
assert r.status_code == 200, (r.status_code, r.text[:150])
assert len(r.content) > 10000
container = av.open(io.BytesIO(r.content))
assert len(container.streams.video) == 1
assert len(container.streams.audio) == 1
frames = [next(container.decode(video=0)) for _ in range(3)]
assert frames[0].width == 640
audio = av.open(io.BytesIO(r.content))
assert next(audio.decode(audio=0)).samples > 0
audio.close()
container.seek(10000000)
assert next(container.decode(video=0)).width == 640
container.close()
passed("Actual HA MP4 bytes decode video/audio metadata and support a 10-second seek")
r = requests.get(url, headers={"Range": "bytes=1000-1999"}, timeout=20)
assert r.status_code == 206
assert len(r.content) == 1000
assert r.headers["Content-Range"].startswith("bytes 1000-1999/")
r = requests.head(url, timeout=20)
assert r.status_code == 200
assert int(r.headers["Content-Length"]) > 10000
assert requests.get(url.split("?")[0], timeout=10).status_code == 401
passed(
    "HA recording proxy preserves byte ranges and HEAD; unsigned anonymous playback is rejected"
)
request(
    nvr,
    "PUT",
    N + "/api/integrations/home-assistant/streams/ha-test-main",
    json={"enabled": False},
)
assert requests.get(url, timeout=10).status_code == 404
request(
    nvr,
    "PUT",
    N + "/api/integrations/home-assistant/streams/ha-test-main",
    json={"enabled": True},
)
wait(lambda: state("camera.ha_test_main") != "unavailable")
assert requests.get(url, timeout=20).status_code == 200
assert (
    nvr.get(
        N
        + "/api/integrations/home-assistant/streams/ha-test-second/recordings/"
        + rid
        + "/file",
        timeout=10,
    ).status_code
    == 404
)
passed(
    "Old signed HA URL stops immediately after unpublish; republish restores; cross-stream clip access rejected"
)
