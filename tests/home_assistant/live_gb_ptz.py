"""Run real HA -> NVR -> GB28181 SIP PTZ checks in the prepared runtime.

Enables loopback GB28181 with a random test password, registers a fixture, seeds
an isolated camera/binding, and controls the second synthetic stream. Requires
free loopback ports 15060 and 18083. Never run against production.
"""

import argparse
import json
import pathlib
import time

import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
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


import os
import secrets
import sqlite3
import subprocess
import sys

D = "34020000001320000001"
C = "34020000001320000002"
F = "http://127.0.0.1:18083"
gb = {
    "server_id": "34020000002000000001",
    "server_port": 15060,
    "password": secrets.token_urlsafe(20),
}
p = root / "gb-fixture.json"
p.write_text(json.dumps(gb))
os.chmod(p, 0o600)
request(
    nvr,
    "PUT",
    N + "/api/settings/gb28181",
    json={
        "enabled": True,
        "host": "127.0.0.1",
        "port": 15060,
        "id": gb["server_id"],
        "password": gb["password"],
        "media_ip": "127.0.0.1",
        "standard_version": "2016",
    },
)
with (root / "gb-fixture.log").open("a") as out:
    process = subprocess.Popen(
        [
            sys.executable,
            str(pathlib.Path(__file__).with_name("gb28181_fixture.py")),
            "--settings",
            str(p),
            "--http-port",
            "18083",
        ],
        stdout=out,
        stderr=subprocess.STDOUT,
        start_new_session=True,
    )
(root / "gb-fixture.pid").write_text(str(process.pid))
wait(lambda: requests.get(F, timeout=3).json()["registered"], 50)
passed(
    "GB28181 loopback SIP fixture authenticates with Digest and registers with the real NVR SIP server"
)
camid = D + ":" + C
db = sqlite3.connect(root / "nvr-data/lalmax-nvr.db")
db.execute(
    "INSERT INTO cameras(id,name,protocol,encoding,url,enabled,onvif_endpoint,profile_token,stream_encoding) VALUES(?,?,?,?,?,?,?,?,?)",
    (
        camid,
        "GB28181 protocol fixture",
        "gb28181",
        "h264",
        "rtsp://127.0.0.1:15544/live/ha-test-second",
        0,
        "",
        "",
        "",
    ),
)
db.execute(
    "INSERT INTO stream_bindings(stream_id,camera_id) VALUES(?,?) ON CONFLICT(stream_id) DO UPDATE SET camera_id=excluded.camera_id",
    ("ha-test-second", camid),
)
db.commit()
db.close()
wait(
    lambda: (
        request(ha, "GET", H + "/api/states/camera.ha_test_second")
        .json()["attributes"]
        .get("ptz_supported")
    ),
    65,
)
P = N + "/api/integrations/home-assistant/streams/ha-test-second/ptz"


def commands():
    return requests.get(F, timeout=3).json()["commands"]


for axis, value, bit in [
    ("pan", 0.4, 1),
    ("pan", -0.4, 2),
    ("tilt", 0.4, 8),
    ("tilt", -0.4, 4),
    ("zoom", 0.4, 16),
    ("zoom", -0.4, 32),
]:
    count = len(commands())
    r = request(
        ha,
        "POST",
        H + "/api/services/lalmax_nvr/ptz",
        json={
            "entity_id": "camera.ha_test_second",
            "mode": "continuous",
            axis: value,
            "duration": 0.1,
        },
    )
    rows = commands()[count:]
    assert len(rows) == 2, len(rows)
    for row in rows:
        data = bytes.fromhex(row["command"])
        assert len(data) == 8
        assert data[0] == 0xA5
        assert data[7] == sum(data[:7]) % 256
        assert row["channel"] == C
    assert bytes.fromhex(rows[0]["command"])[3] == bit
    assert bytes.fromhex(rows[1]["command"])[3] == 0
assert nvr.post(P, json={"mode": "absolute"}, timeout=10).status_code == 400
assert (
    nvr.post(
        P, json={"mode": "continuous", "pan": 0.2, "tilt": 0.2}, timeout=10
    ).status_code
    == 400
)
passed(
    "Real HA -> NVR -> SIP MESSAGE PTZ works in six GB28181 directions, correct channel/checksum and automatic stop; unsupported modes rejected"
)
# The protocol fixture remains for final demonstration, with no physical device movement.
print("GB28181 PTZ protocol integration checks complete", flush=True)
