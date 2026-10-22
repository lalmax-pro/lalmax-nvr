"""Loopback SIP device for authenticated GB28181 PTZ integration checks.

--settings points to private JSON containing password, server_port and server_id.
The fixture records only decoded PTZ commands, never SIP authentication headers.
"""

import argparse
import hashlib
import json
import re
import socket
import threading
import time
import uuid
import xml.etree.ElementTree as ET
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

DEVICE = "34020000001320000001"
CHANNEL = "34020000001320000002"


class Device:
    def __init__(self, config):
        self.config = config
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind(("127.0.0.1", 0))
        self.port = self.socket.getsockname()[1]
        self.target = ("127.0.0.1", config["server_port"])
        self.uri = f"sip:{config['server_id']}@{config['server_id'][:10]}"
        self.commands = []
        self.registered = False
        self.seq = 0
        self.lock = threading.Lock()

    def send(self, method, body="", authorization=""):
        self.seq += 1
        headers = [
            f"{method} {self.uri} SIP/2.0",
            f"Via: SIP/2.0/UDP 127.0.0.1:{self.port};branch=z9hG4bK{uuid.uuid4().hex};rport",
            f"From: <sip:{DEVICE}@{self.config['server_id'][:10]}>;tag=fixture",
            f"To: <sip:{DEVICE}@{self.config['server_id'][:10]}>",
            f"Call-ID: {uuid.uuid4().hex}",
            f"CSeq: {self.seq} {method}",
            f"Contact: <sip:{DEVICE}@127.0.0.1:{self.port}>",
            "Max-Forwards: 70",
            "Expires: 3600",
            "User-Agent: isolated-ptz-fixture",
        ]
        if authorization:
            headers.append("Authorization: " + authorization)
        if body:
            headers.append("Content-Type: Application/MANSCDP+xml")
        headers.append(f"Content-Length: {len(body.encode())}")
        self.socket.sendto(
            ("\r\n".join(headers) + "\r\n\r\n" + body).encode(), self.target
        )

    def run(self):
        self.send("REGISTER")
        threading.Thread(target=self.keepalive, daemon=True).start()
        while True:
            raw, peer = self.socket.recvfrom(65535)
            text = raw.decode(errors="replace")
            header, _, body = text.partition("\r\n\r\n")
            lines = header.split("\r\n")
            headers = {
                k.lower(): v.strip()
                for line in lines[1:]
                if ":" in line
                for k, v in [line.split(":", 1)]
            }
            if lines[0].startswith("SIP/2.0 401") and "REGISTER" in headers.get(
                "cseq", ""
            ):
                challenge = dict(
                    re.findall(r'(\w+)="([^"]+)"', headers["www-authenticate"])
                )
                realm, nonce, cnonce = (
                    challenge["realm"],
                    challenge["nonce"],
                    uuid.uuid4().hex,
                )

                def md5(value):
                    return hashlib.md5(
                        value.encode(), usedforsecurity=False
                    ).hexdigest()

                response = md5(
                    f"{md5(DEVICE + ':' + realm + ':' + self.config['password'])}:{nonce}:00000001:{cnonce}:auth:{md5('REGISTER:' + self.uri)}"
                )
                self.send(
                    "REGISTER",
                    authorization=f'Digest username="{DEVICE}",realm="{realm}",nonce="{nonce}",uri="{self.uri}",response="{response}",algorithm=MD5,qop=auth,nc=00000001,cnonce="{cnonce}"',
                )
            elif lines[0].startswith("SIP/2.0 200") and "REGISTER" in headers.get(
                "cseq", ""
            ):
                self.registered = True
            elif lines[0].startswith("MESSAGE "):
                response_headers = ["SIP/2.0 200 OK"]
                for key in ("via", "from", "to", "call-id", "cseq"):
                    response_headers.append(key + ": " + headers[key])
                response_headers.append("Content-Length: 0")
                self.socket.sendto(
                    ("\r\n".join(response_headers) + "\r\n\r\n").encode(), peer
                )
                if "<PTZCmd>" in body:
                    root = ET.fromstring(body)
                    command = root.findtext("PTZCmd")
                    with self.lock:
                        self.commands.append(
                            {
                                "command": command,
                                "channel": root.findtext("DeviceID"),
                                "time": time.monotonic(),
                            }
                        )

    def keepalive(self):
        while True:
            time.sleep(20)
            if self.registered:
                self.send(
                    "MESSAGE",
                    f"<Notify><CmdType>Keepalive</CmdType><SN>1</SN><DeviceID>{DEVICE}</DeviceID><Status>OK</Status></Notify>",
                )


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--settings", type=Path, required=True)
    parser.add_argument("--http-port", type=int, default=18083)
    args = parser.parse_args()
    device = Device(json.loads(args.settings.read_text()))
    threading.Thread(target=device.run, daemon=True).start()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            with device.lock:
                data = {
                    "registered": device.registered,
                    "commands": device.commands.copy(),
                }
            payload = json.dumps(data).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

    ThreadingHTTPServer(("127.0.0.1", args.http_port), Handler).serve_forever()
