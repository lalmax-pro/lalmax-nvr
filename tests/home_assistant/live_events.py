"""Verify live events against the isolated runtime prepared for live_smoke.py.

Requires AI webhook mode, event entities and the test automation configured in
HA. Uses private credentials from --runtime; never target a production instance.
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
root = parser.parse_args().runtime.resolve()
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


async def main():
    async with (
        aiohttp.ClientSession() as s,
        s.ws_connect(H + "/api/websocket") as ws,
    ):
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"
        await ws.send_json(
            {"id": 1, "type": "subscribe_events", "event_type": "lalmax_nvr_event"}
        )
        assert (await ws.receive_json())["success"]
        detection("ha-test-main", ["car", "motion"])
        event = (await ws.receive_json(timeout=8))["event"]["data"]
        assert event["type"] == "ai_detection"
        assert event["labels"] == ["car", "motion"]
        assert event["config_entry_id"] == eid
        assert "metadata" not in event
        assert state("binary_sensor.ha_test_main_vehicle") == "on"
        assert state("binary_sensor.ha_test_main_motion") == "on"
        passed(
            "Real HA event entity and vehicle/motion sensor receive sanitized live detection"
        )
        wait(lambda: state("binary_sensor.ha_test_main_vehicle") == "off", 75)
        assert state("binary_sensor.ha_test_main_motion") == "off"
        passed("Real HA detection sensors automatically clear after 60 seconds")
        detection("not-published", ["cat"])
        try:
            await ws.receive_json(timeout=3)
            raise AssertionError("Hidden stream leaked")
        except TimeoutError:
            pass
        publish(False)
        wait(lambda: state("event.ha_test_main_activity") == "unavailable")
        detection("ha-test-main", ["dog"])
        try:
            await ws.receive_json(timeout=3)
            raise AssertionError("Withdrawn stream leaked")
        except TimeoutError:
            pass
        assert state("binary_sensor.ha_test_main_person") == "unavailable"
        passed("Unpublished and withdrawn streams do not emit HA automation events")
        publish(True)
        wait(lambda: state("event.ha_test_main_activity") != "unavailable")
        request(ha, "POST", H + "/api/config/config_entries/entry/" + eid + "/reload")
        wait(lambda: state("event.ha_test_main_activity") != "unavailable")
        detection("ha-test-main", ["truck"])
        data = (await ws.receive_json(timeout=8))["event"]["data"]
        assert data["labels"] == ["truck"]
        assert state("binary_sensor.ha_test_main_vehicle") == "on"
        try:
            await ws.receive_json(timeout=3)
            raise AssertionError("Duplicate task/event")
        except TimeoutError:
            pass
        passed(
            "Republish and real HA integration reload resume live events without duplicate tasks"
        )


asyncio.run(main())
