"""Live HA WebRTC test using the loopback Pion receiver on port 18084.

Run the receiver first: go run ./tests/home_assistant/webrtc_peer
Requires prepared isolated HA/NVR runtime; reconfigures no production systems.
--mode revoke toggles test publication, --mode reload reloads the test entry.
"""

import argparse
import asyncio
import json
import pathlib

import aiohttp
import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
parser.add_argument("--mode", choices=["close", "revoke", "reload"], default="close")
args = parser.parse_args()
root = args.runtime.resolve()
H = "http://127.0.0.1:18123"
N = "http://127.0.0.1:19090"
P = "http://127.0.0.1:18084"
c = json.loads((root / "credentials.json").read_text())
tok = json.loads((root / "ha-token.json").read_text())["access_token"]
nvr = requests.Session()
nvr.auth = c["username"], c["password"]
ha = requests.Session()
ha.headers["Authorization"] = "Bearer " + tok


async def main():
    async with aiohttp.ClientSession() as s, s.ws_connect(H + "/api/websocket") as ws:
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"
        await ws.send_json(
            {
                "id": 1,
                "type": "camera/webrtc/get_client_config",
                "entity_id": "camera.ha_test_main",
            }
        )
        m = await ws.receive_json()
        print("WebRTC client capability", m, flush=True)
        assert m.get("success")
        offer = (
            await asyncio.to_thread(
                requests.get, P + "/offer?candidates=false", timeout=10
            )
        ).text
        assert offer.startswith("v=0") and "a=candidate:" not in offer
        await ws.send_json(
            {
                "id": 2,
                "type": "camera/webrtc/offer",
                "entity_id": "camera.ha_test_main",
                "offer": offer,
            }
        )
        for _ in range(5):
            m = await ws.receive_json(timeout=30)
            if m.get("type") == "event":
                e = m["event"]
                print("HA signaling event", e.get("type"), flush=True)
                if e.get("type") == "error":
                    raise AssertionError(e)
                if e.get("type") == "answer":
                    a = await asyncio.to_thread(
                        requests.post, P + "/answer", data=e["answer"], timeout=5
                    )
                    assert a.ok, a.text
                    break
        else:
            raise AssertionError("no answer")
        for _ in range(60):
            st = (await asyncio.to_thread(requests.get, P + "/stats", timeout=5)).json()
            if st["packets"] > 30 and st["state"] == "connected":
                print(
                    "PASS HA WebRTC -> real H264 RTP, candidate-free offer",
                    st,
                    flush=True,
                )
                break
            await asyncio.sleep(0.5)
        else:
            raise AssertionError(st)
        await ws.send_json(
            {
                "id": 3,
                "type": "camera/stream",
                "entity_id": "camera.ha_test_main",
                "format": "hls",
            }
        )
        m = await ws.receive_json(timeout=30)
        assert m.get("success"), m
        print("PASS HLS retained alongside WebRTC", flush=True)
        if args.mode == "close":
            await ws.send_json(
                {"id": 4, "type": "unsubscribe_events", "subscription": 2}
            )
            m = await ws.receive_json(timeout=20)
            assert m.get("success")
        elif args.mode == "revoke":
            r = nvr.put(
                N + "/api/integrations/home-assistant/streams/ha-test-main",
                json={"enabled": False},
                timeout=10,
            )
            assert r.ok
        else:
            eid = json.loads((root / "entry.json").read_text())["entry_id"]
            r = ha.post(
                H + "/api/config/config_entries/entry/" + eid + "/reload", timeout=40
            )
            assert r.ok
        try:
            for _ in range(100):
                if (
                    await asyncio.to_thread(requests.get, P + "/stats", timeout=5)
                ).json()["state"] == "closed":
                    break
                await asyncio.sleep(0.5)
            else:
                raise AssertionError("RTC session not cleaned up")
        finally:
            if args.mode == "revoke":
                r = nvr.put(
                    N + "/api/integrations/home-assistant/streams/ha-test-main",
                    json={"enabled": True},
                    timeout=10,
                )
                assert r.ok
        print("PASS WebRTC session cleanup: " + args.mode, flush=True)
        await asyncio.sleep(3)
        print(
            "After cleanup",
            (await asyncio.to_thread(requests.get, P + "/stats", timeout=5)).json(),
            flush=True,
        )


asyncio.run(main())
