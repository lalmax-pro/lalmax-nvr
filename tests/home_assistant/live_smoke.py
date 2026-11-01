"""Run real NVR/HA integration checks in an already prepared isolated runtime.

The runtime contains credentials.json, ha-token.json, entry.json, nvr.yaml,
ha/, nvr.pid and ha.pid. These private files must never be committed.
See docs/zh/home-assistant.md for the product setup steps.
"""

import argparse
import asyncio
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urljoin

import aiohttp
import requests


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--runtime", type=Path, required=True)
    parser.add_argument("--nvr-binary", type=Path, required=True)
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args()
    root = args.runtime.resolve()
    credentials = json.loads((root / "credentials.json").read_text())
    token = json.loads((root / "ha-token.json").read_text())["access_token"]
    entry_id = json.loads((root / "entry.json").read_text())["entry_id"]
    nvr_url, ha_url = "http://127.0.0.1:19090", "http://127.0.0.1:18123"
    nvr = requests.Session()
    nvr.auth = credentials["username"], credentials["password"]
    ha = requests.Session()
    ha.headers["Authorization"] = "Bearer " + token
    results = json.loads((root / "results.json").read_text()) if args.resume else []

    def passed(name):
        results.append({"test": name, "result": "passed"})
        (root / "results.json").write_text(json.dumps(results, indent=2))
        print("PASS", name, flush=True)

    def wait_for(check, timeout=75):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if value := check():
                    return value
            except (requests.RequestException, KeyError, ValueError):
                pass
            time.sleep(1)
        raise AssertionError("Timed out waiting for an integration state change")

    def request(client, method, url, **kwargs):
        result = client.request(method, url, timeout=30, **kwargs)
        assert result.ok, f"{method} {url}: HTTP {result.status_code}"
        return result

    def camera(sid):
        data = request(ha, "GET", ha_url + "/api/states").json()
        return next(
            (s for s in data if s["entity_id"] == "camera." + sid.replace("-", "_")),
            None,
        )

    def available(sid, expected):
        state = camera(sid)
        return (
            state if state and (state["state"] != "unavailable") == expected else None
        )

    def publish(sid, enabled):
        return request(
            nvr,
            "PUT",
            nvr_url + "/api/integrations/home-assistant/streams/" + sid,
            json={"enabled": enabled},
        ).json()

    def settings(enabled):
        cfg = request(nvr, "GET", nvr_url + "/api/settings/home-assistant").json()
        cfg.pop("instance_id")
        cfg["enabled"] = enabled
        return request(
            nvr, "PUT", nvr_url + "/api/settings/home-assistant", json=cfg
        ).json()

    def publisher(sid):
        ffmpeg = os.environ.get("FFMPEG_BINARY", "ffmpeg")
        with (root / (sid + ".log")).open("a") as output:
            proc = subprocess.Popen(
                [
                    ffmpeg,
                    "-hide_banner",
                    "-loglevel",
                    "error",
                    "-re",
                    "-f",
                    "lavfi",
                    "-i",
                    "testsrc2=size=640x360:rate=15",
                    "-f",
                    "lavfi",
                    "-i",
                    "sine=frequency=440:sample_rate=44100",
                    "-c:v",
                    "libx264",
                    "-preset",
                    "ultrafast",
                    "-tune",
                    "zerolatency",
                    "-pix_fmt",
                    "yuv420p",
                    "-g",
                    "15",
                    "-c:a",
                    "aac",
                    "-f",
                    "flv",
                    f"rtmp://127.0.0.1:11935/live/{sid}",
                ],
                stdout=output,
                stderr=subprocess.STDOUT,
                start_new_session=True,
            )
        (root / (sid + ".pid")).write_text(str(proc.pid))
        return proc

    def stop(proc):
        if isinstance(proc, int):
            os.kill(proc, signal.SIGTERM)
        else:
            proc.terminate()
            proc.wait(timeout=15)

    async def ws_command(message):
        async with (
            aiohttp.ClientSession() as session,
            session.ws_connect(ha_url + "/api/websocket") as ws,
        ):
            await ws.receive_json()
            await ws.send_json({"type": "auth", "access_token": token})
            assert (await ws.receive_json())["type"] == "auth_ok"
            await ws.send_json({"id": 1, **message})
            result = await ws.receive_json(timeout=30)
            assert result.get("success"), result.get("error")
            return result.get("result")

    def snapshot(sid):
        result = request(
            ha, "GET", ha_url + "/api/camera_proxy/camera." + sid.replace("-", "_")
        )
        assert result.content.startswith(b"\xff\xd8")
        assert len(result.content) > 1000
        (root / (sid + "-snapshot.jpg")).write_bytes(result.content)

    def video(sid):
        info = asyncio.run(
            ws_command(
                {
                    "type": "camera/stream",
                    "entity_id": "camera." + sid.replace("-", "_"),
                    "format": "hls",
                }
            )
        )
        endpoint = urljoin(ha_url, info["url"])
        playlist = request(ha, "GET", endpoint).text
        if "#EXT-X-STREAM-INF" in playlist:
            assert "avc1." in playlist and "mp4a." in playlist
            endpoint = urljoin(
                endpoint,
                next(
                    line
                    for line in playlist.splitlines()
                    if line and not line.startswith("#")
                ),
            )
        playlist = wait_for(
            lambda: (
                text
                if "#EXTINF" in (text := request(ha, "GET", endpoint).text)
                else None
            ),
            30,
        )
        segment = next(
            line for line in playlist.splitlines() if line and not line.startswith("#")
        )
        payload = request(ha, "GET", urljoin(endpoint, segment)).content
        assert len(payload) > 1000

    if not args.resume:
        # These synthetic slots are deliberately independent of camera devices/recording plans.
        for sid in ["ha-test-main", "ha-test-second"]:
            response = nvr.post(
                nvr_url + "/api/streams",
                json={"stream_id": sid, "name": sid, "input_mode": "push"},
                timeout=30,
            )
            assert response.status_code in (200, 201, 409)
        settings(True)
        publish("ha-test-main", True)
        main_publisher = publisher("ha-test-main")
        wait_for(lambda: available("ha-test-main", True))
        snapshot("ha-test-main")
        video("ha-test-main")
        passed("H.264/AAC live RTSP Digest + playback token → HA JPEG and HLS media")

        # Real validation in the HA configuration flow, not just client mocks.
        form = request(
            ha,
            "POST",
            ha_url + "/api/config/config_entries/flow",
            json={"handler": "lalmax_nvr"},
        ).json()
        values = {
            "url": nvr_url,
            "username": credentials["username"],
            "password": "intentionally-invalid",
        }
        result = request(
            ha,
            "POST",
            ha_url + "/api/config/config_entries/flow/" + form["flow_id"],
            json=values,
        ).json()
        assert result["errors"]["base"] == "invalid_auth"
        values["password"] = credentials["password"]
        result = request(
            ha,
            "POST",
            ha_url + "/api/config/config_entries/flow/" + form["flow_id"],
            json=values,
        ).json()
        assert result["type"] == "abort" and result["reason"] == "already_configured"
        passed(
            "Invalid HTTP credentials rejected; duplicate NVR prevented by instance UUID"
        )

        second = publisher("ha-test-second")
        publish("ha-test-second", True)
        wait_for(lambda: available("ha-test-second", True))
        snapshot("ha-test-second")
        video("ha-test-second")
        passed("New publication dynamically creates a second playable camera")

        request(
            nvr,
            "PUT",
            nvr_url + "/api/streams/ha-test-second",
            json={"name": "Second Camera Renamed"},
        )
        wait_for(
            lambda: (
                state
                if (state := camera("ha-test-second"))["attributes"].get(
                    "friendly_name"
                )
                == "Second Camera Renamed"
                else None
            )
        )
        passed("Stream rename syncs to HA without replacing the entity")

        publish("ha-test-second", False)
        wait_for(lambda: available("ha-test-second", False))
        response = nvr.get(
            nvr_url
            + "/api/integrations/home-assistant/streams/ha-test-second/snapshot",
            timeout=10,
        )
        assert response.status_code == 404
        passed(
            "Unpublish withdraws snapshot access and makes the HA camera unavailable"
        )
        publish("ha-test-second", True)
        wait_for(lambda: available("ha-test-second", True))
        passed("Republish restores the existing camera identity")

        stop(second)
        wait_for(lambda: available("ha-test-second", False))
        second = publisher("ha-test-second")
        wait_for(lambda: available("ha-test-second", True))
        passed("Publisher disconnect and recovery update HA availability")

        settings(False)
        wait_for(lambda: available("ha-test-main", False))
        assert (
            request(nvr, "GET", nvr_url + "/api/integrations/home-assistant").json()[
                "streams"
            ]
            == []
        )
        settings(True)
        wait_for(lambda: available("ha-test-main", True))
        passed("Global disable/enable withdraws and restores publications")

        response = requests.get(
            nvr_url + "/api/integrations/home-assistant", timeout=10
        )
        assert response.status_code == 401
        viewer = requests.Session()
        viewer.auth = "ha-viewer", "local-viewer-test-only-2026"
        assert (
            viewer.get(
                nvr_url + "/api/integrations/home-assistant", timeout=10
            ).status_code
            == 200
        )
        assert (
            viewer.put(
                nvr_url + "/api/integrations/home-assistant/streams/ha-test-main",
                json={"enabled": False},
                timeout=10,
            ).status_code
            == 403
        )
        passed("Anonymous access denied; ordinary user can read but cannot publish")

    else:
        main_publisher = int((root / "ha-test-main.pid").read_text())
        second = int((root / "ha-test-second.pid").read_text())

    # Reconfigure to the same instance using a different HTTP hostname and viewer credentials.
    form = request(
        ha,
        "POST",
        ha_url + "/api/config/config_entries/flow",
        json={
            "handler": "lalmax_nvr",
            "entry_id": entry_id,
        },
    ).json()
    values = {
        "url": "http://localhost:19090",
        "username": "ha-viewer",
        "password": "local-viewer-test-only-2026",
        "rtsp_username": credentials["rtsp_username"],
        "rtsp_password": credentials["rtsp_password"],
    }
    result = request(
        ha,
        "POST",
        ha_url + "/api/config/config_entries/flow/" + form["flow_id"],
        json=values,
    ).json()
    assert result["type"] == "abort" and result["reason"] == "reconfigure_successful", (
        result.get("errors")
    )
    wait_for(lambda: available("ha-test-main", True))
    snapshot("ha-test-main")
    passed(
        "Reconfigure address and independent viewer/RTSP credentials; reload keeps identity"
    )

    users = request(nvr, "GET", nvr_url + "/api/users").json()
    viewer_id = next(user["id"] for user in users if user["username"] == "ha-viewer")
    rotated_password = "local-viewer-rotated-test-2026"
    request(
        nvr,
        "PUT",
        nvr_url + f"/api/users/{viewer_id}",
        json={"password": rotated_password},
    )
    wait_for(lambda: available("ha-test-main", False))
    flows = wait_for(
        lambda: asyncio.run(ws_command({"type": "config_entries/flow/progress"}))
    )
    flow = next(
        flow
        for flow in flows
        if flow["handler"] == "lalmax_nvr" and flow["context"]["source"] == "reauth"
    )
    form = request(
        ha, "GET", ha_url + "/api/config/config_entries/flow/" + flow["flow_id"]
    ).json()
    values["password"] = rotated_password
    result = request(
        ha,
        "POST",
        ha_url + "/api/config/config_entries/flow/" + form["flow_id"],
        json=values,
    ).json()
    assert result["type"] == "abort" and result["reason"] == "reauth_successful", (
        result.get("errors")
    )
    wait_for(lambda: available("ha-test-main", True))
    snapshot("ha-test-main")
    passed(
        "Actual password rotation triggers reauthentication and restores video access"
    )

    identity = request(nvr, "GET", nvr_url + "/api/settings/home-assistant").json()[
        "instance_id"
    ]
    stop(main_publisher)
    stop(second)
    pid = int((root / "nvr.pid").read_text())
    os.kill(pid, signal.SIGTERM)
    wait_for(
        lambda: (
            subprocess.run(
                ["kill", "-0", str(pid)], stderr=subprocess.DEVNULL, check=False
            ).returncode
            != 0
        ),
        20,
    )
    with (root / "nvr-console.log").open("a") as output:
        proc = subprocess.Popen(
            [str(args.nvr_binary), "--config", str(root / "nvr.yaml")],
            cwd=root,
            stdout=output,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    (root / "nvr.pid").write_text(str(proc.pid))
    wait_for(
        lambda: (
            request(nvr, "GET", nvr_url + "/api/settings/home-assistant").json()[
                "instance_id"
            ]
            == identity
        )
    )
    main_publisher, second = publisher("ha-test-main"), publisher("ha-test-second")
    wait_for(lambda: available("ha-test-main", True))
    video("ha-test-main")
    passed("NVR restart preserves database identity, publication choices and HA entity")

    pid = int((root / "ha.pid").read_text())
    os.kill(pid, signal.SIGTERM)
    wait_for(
        lambda: (
            subprocess.run(
                ["kill", "-0", str(pid)], stderr=subprocess.DEVNULL, check=False
            ).returncode
            != 0
        ),
        20,
    )
    with (root / "ha-console.log").open("a") as output:
        proc = subprocess.Popen(
            [
                sys.executable,
                "-m",
                "homeassistant",
                "--config",
                str(root / "ha"),
                "--log-file",
                str(root / "ha.log"),
            ],
            cwd=root,
            stdout=output,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    (root / "ha.pid").write_text(str(proc.pid))
    wait_for(lambda: available("ha-test-main", True))
    video("ha-test-main")
    passed("HA restart restores cameras and live playback without duplicate entities")

    states = request(ha, "GET", ha_url + "/api/states").json()
    assert (
        len(
            [
                state
                for state in states
                if state["entity_id"].startswith("camera.ha_test_main")
            ]
        )
        == 1
    )
    passed("Stable entity count after lifecycle tests")
    print(
        f"Completed {len(results)} real integration checks; logs and images in {root}",
        flush=True,
    )


if __name__ == "__main__":
    main()
