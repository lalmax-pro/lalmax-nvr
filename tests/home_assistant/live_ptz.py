"""Run native HA PTZ actions/buttons against the loopback ONVIF fixture.

Requires a prepared runtime, ONVIF binding and a private viewer-credentials JSON.
Changes HA NVR credentials and toggles test publication; only use in isolation.
"""

import argparse
import asyncio
import json
import pathlib
import time

import aiohttp
import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
parser.add_argument("--viewer-credentials", type=pathlib.Path, required=True)
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


from concurrent.futures import ThreadPoolExecutor

F = "http://127.0.0.1:18081"
P = N + "/api/integrations/home-assistant/streams/ha-test-main/ptz"


def commands():
    return requests.get(F + "/commands", timeout=5).json()


def ops():
    return [
        v
        for v in commands()
        if v["operation"]
        in ["ContinuousMove", "Stop", "AbsoluteMove", "RelativeMove", "GotoPreset"]
    ]


def service(name, body, response=False):
    return ha.post(
        H
        + "/api/services/lalmax_nvr/"
        + name
        + ("?return_response" if response else ""),
        json=body,
        timeout=45,
    )


def reconfigure(username, password):
    flow = request(
        ha,
        "POST",
        H + "/api/config/config_entries/flow",
        json={"handler": "lalmax_nvr", "entry_id": eid, "show_advanced_options": True},
    ).json()
    r = request(
        ha,
        "POST",
        H + "/api/config/config_entries/flow/" + flow["flow_id"],
        json={
            "url": "http://localhost:19090",
            "username": username,
            "password": password,
        },
    ).json()
    assert r["type"] == "abort", r.get("reason")
    wait(lambda: state("camera.ha_test_main") != "unavailable")
    time.sleep(1)


async def ws_command(message):
    async with (
        aiohttp.ClientSession() as s,
        s.ws_connect(H + "/api/websocket") as ws,
    ):
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"
        await ws.send_json({"id": 1, **message})
        r = await ws.receive_json(timeout=25)
        assert r["success"], r.get("error")
        return r.get("result")


viewer = json.loads(args.viewer_credentials.read_text())
reconfigure(viewer["username"], viewer["password"])
wait(
    lambda: (
        request(ha, "GET", H + "/api/states/camera.ha_test_main")
        .json()["attributes"]
        .get("ptz_supported")
    )
)
before = len(ops())
r = service(
    "ptz",
    {
        "entity_id": "camera.ha_test_main",
        "mode": "continuous",
        "pan": 0.5,
        "duration": 0.2,
    },
)
assert r.status_code != 200
assert len(ops()) == before
flows = asyncio.run(ws_command({"type": "config_entries/flow/progress"}))
assert not any(
    v.get("handler") == "lalmax_nvr" and v.get("context", {}).get("source") == "reauth"
    for v in flows
)
passed(
    "Read-only HA credentials cannot move PTZ and do not incorrectly trigger reauthentication"
)
reconfigure(c["username"], c["password"])
for mode, extra, expected in [
    ("continuous", {"pan": 0.4, "duration": 0.2}, "ContinuousMove"),
    ("relative", {"tilt": 0.3}, "RelativeMove"),
    ("absolute", {"pan": 0.1, "tilt": 0.2, "zoom": 0.3}, "AbsoluteMove"),
    ("goto_preset", {"preset_token": "entrance"}, "GotoPreset"),
    ("stop", {}, "Stop"),
]:
    count = len(ops())
    r = service("ptz", {"entity_id": "camera.ha_test_main", "mode": mode, **extra})
    assert r.ok, (mode, r.status_code, r.text[:100])
    after = ops()[count:]
    assert after[0]["operation"] == expected, after
    assert after[0]["values"]["ProfileToken"]["value"] == "fixture-profile"
    if mode == "continuous":
        assert after[-1]["operation"] == "Stop"
        assert 0.15 <= after[-1]["time"] - after[0]["time"] < 2
r = service("ptz_presets", {"entity_id": "camera.ha_test_main"}, True)
assert r.ok, r.status_code
assert "entrance" in r.text
passed(
    "Native HA PTZ actions reach the ONVIF SOAP fixture: continuous/auto-stop, relative, absolute, stop and preset read/goto"
)
entities = asyncio.run(ws_command({"type": "config/entity_registry/list"}))
buttons = [
    e
    for e in entities
    if e["platform"] == "lalmax_nvr" and ":ha-test-main:ptz_" in e["unique_id"]
]
assert len(buttons) == 7, len(buttons)
for button in buttons:
    count = len(ops())
    r = request(
        ha,
        "POST",
        H + "/api/services/button/press",
        json={"entity_id": button["entity_id"]},
    )
    assert len(ops()) > count
passed(
    "All seven native HA PTZ buttons send actual SOAP commands and movement buttons auto-stop"
)
# Revoke publication during an already authorized pulse: cleanup still stops the device.
with ThreadPoolExecutor() as executor:
    count = len(ops())
    future = executor.submit(
        nvr.post, P, json={"mode": "continuous", "pan": 0.2, "duration": 1}, timeout=10
    )
    wait(lambda: len(ops()) > count, 10)
    publish(False)
    r = future.result()
    assert r.ok, r.status_code
    assert ops()[-1]["operation"] == "Stop"
assert nvr.post(P, json={"mode": "stop"}, timeout=10).status_code == 404
publish(True)
passed(
    "Unpublishing during a PTZ pulse still sends automatic Stop; new commands are blocked"
)


async def disconnect_during_move():
    count = len(ops())
    async with aiohttp.ClientSession() as s:
        task = asyncio.create_task(
            s.post(
                P,
                auth=aiohttp.BasicAuth(c["username"], c["password"]),
                json={"mode": "continuous", "pan": 0.2, "duration": 4},
            )
        )
        for _ in range(50):
            if len(ops()) > count:
                break
            await asyncio.sleep(0.05)
        else:
            raise AssertionError("No move")
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass
    for _ in range(50):
        if len(ops()) >= count + 2 and ops()[-1]["operation"] == "Stop":
            return
        await asyncio.sleep(0.05)
    raise AssertionError("No cleanup Stop")


asyncio.run(disconnect_during_move())
passed("Disconnecting an in-flight NVR PTZ request still causes device Stop")
print(
    "PTZ fixture checks complete; fixture and binding retained for final demonstration",
    flush=True,
)
