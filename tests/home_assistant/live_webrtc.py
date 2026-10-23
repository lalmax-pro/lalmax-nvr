"""Live HA WebRTC test using the loopback Pion receiver on port 18084.

Run the receiver first: go run ./tests/home_assistant/webrtc_peer
Requires prepared isolated HA/NVR runtime; reconfigures no production systems.
--mode revoke toggles test publication, --mode reload reloads the test entry.
"""

import argparse
import asyncio
import io
import json
import pathlib

import aiohttp
import av
import requests

parser = argparse.ArgumentParser()
parser.add_argument("--runtime", type=pathlib.Path, required=True)
parser.add_argument("--mode", choices=["close", "revoke", "reload"], default="close")
parser.add_argument("--codec", choices=["h264", "h265"], default="h264")
parser.add_argument("--relay", action="store_true", help="require HA-configured TURN")
parser.add_argument("--stream-id", help="override the synthetic NVR stream")
parser.add_argument("--entity", help="HA camera entity for the overridden stream")
parser.add_argument("--width", type=int, default=640)
parser.add_argument("--height", type=int, default=360)
args = parser.parse_args()
if bool(args.stream_id) != bool(args.entity):
    parser.error("--stream-id and --entity must be supplied together")
if args.width <= 0 or args.height <= 0:
    parser.error("expected video dimensions must be positive")
sid = args.stream_id or ("ha-test-main" if args.codec == "h264" else "ha-test-second")
entity = args.entity or (
    "camera.ha_test_main" if args.codec == "h264" else "camera.ha_test_second"
)
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
    if args.codec == "h265":
        incompatible = await asyncio.to_thread(
            requests.get, P + "/offer?candidates=false&codec=h264", timeout=10
        )
        rejected = await asyncio.to_thread(
            nvr.post,
            N + "/api/integrations/home-assistant/streams/" + sid + "/webrtc",
            data=incompatible.text,
            headers={"Content-Type": "application/sdp"},
            timeout=10,
        )
        assert rejected.status_code == 400, rejected.status_code
        print("PASS H.264-only offer rejected for HEVC source", flush=True)
    async with aiohttp.ClientSession() as s, s.ws_connect(H + "/api/websocket") as ws:
        await ws.receive_json()
        await ws.send_json({"type": "auth", "access_token": tok})
        assert (await ws.receive_json())["type"] == "auth_ok"
        # Capabilities are a WebSocket API result, not camera state attributes.
        # Wait for publication restoration by HA's polling coordinator.
        for attempt in range(90):
            await ws.send_json(
                {
                    "id": 100 + attempt,
                    "type": "camera/capabilities",
                    "entity_id": entity,
                }
            )
            capability = await ws.receive_json(timeout=10)
            if (
                capability.get("success")
                and "web_rtc" in capability["result"]["frontend_stream_types"]
            ):
                assert "hls" in capability["result"]["frontend_stream_types"]
                break
            await asyncio.sleep(0.5)
        else:
            raise AssertionError("HA camera WebRTC capability did not recover")
        await ws.send_json(
            {
                "id": 200,
                "type": "camera/webrtc/get_client_config",
                "entity_id": entity,
            }
        )
        m = await ws.receive_json()
        assert m.get("success")
        configuration = m["result"]["configuration"]
        print("PASS HA WebRTC client configuration", flush=True)
        if args.relay:
            assert any(
                url.startswith("turn:")
                for server in configuration["iceServers"]
                for url in server["urls"]
            ), "configure a TURN server in the isolated HA web_rtc configuration"
        offer = (
            await asyncio.to_thread(
                requests.post,
                P
                + "/offer?candidates=false&codec="
                + args.codec
                + ("&relay=true" if args.relay else ""),
                json=configuration if args.relay else {},
                timeout=10,
            )
        ).text
        assert offer.startswith("v=0") and "a=candidate:" not in offer
        await ws.send_json(
            {
                "id": 201,
                "type": "camera/webrtc/offer",
                "entity_id": entity,
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
                    assert args.codec.upper() + "/90000" in e["answer"]
                    a = await asyncio.to_thread(
                        requests.post, P + "/answer", data=e["answer"], timeout=5
                    )
                    assert a.ok, a.text
                    break
        else:
            raise AssertionError("no answer")
        for _ in range(60):
            st = (await asyncio.to_thread(requests.get, P + "/stats", timeout=5)).json()
            if st["packets"] > 100 and st["state"] == "connected":
                print(
                    "PASS HA WebRTC -> real "
                    + args.codec.upper()
                    + " RTP, candidate-free offer",
                    st,
                    flush=True,
                )
                break
            await asyncio.sleep(0.5)
        else:
            raise AssertionError(st)
        assert st["codec"].lower() == "video/" + args.codec
        if args.relay:
            assert st.get("local_candidate_type") == "relay", st
            print(
                "PASS selected ICE candidate uses authenticated TURN relay", flush=True
            )
        video = (await asyncio.to_thread(requests.get, P + "/video", timeout=5)).content
        with av.open(
            io.BytesIO(video), format="hevc" if args.codec == "h265" else "h264"
        ) as container:
            decoded = 0
            for frame in container.decode(video=0):
                assert (frame.width, frame.height) == (args.width, args.height)
                decoded += 1
                if decoded >= 3:
                    break
            assert decoded >= 3
        print(
            "PASS actual received WebRTC video decodes",
            args.codec,
            len(video),
            decoded,
            flush=True,
        )
        await ws.send_json(
            {
                "id": 202,
                "type": "camera/stream",
                "entity_id": entity,
                "format": "hls",
            }
        )
        m = await ws.receive_json(timeout=30)
        assert m.get("success"), m
        print("PASS HLS retained alongside WebRTC", flush=True)
        if args.mode == "close":
            await ws.send_json(
                {"id": 203, "type": "unsubscribe_events", "subscription": 201}
            )
            m = await ws.receive_json(timeout=20)
            assert m.get("success")
        elif args.mode == "revoke":
            r = nvr.put(
                N + "/api/integrations/home-assistant/streams/" + sid,
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
                    N + "/api/integrations/home-assistant/streams/" + sid,
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
