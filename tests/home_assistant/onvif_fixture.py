"""Local ONVIF SOAP device used for HA PTZ protocol integration tests.

It simulates device responses and records operations, not physical movement.
Only bind to loopback. Never use this fixture as a real camera service.
"""

import argparse
import json
import threading
import time
import xml.etree.ElementTree as ET
from datetime import UTC, datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PROFILE = "fixture-profile"
COMMANDS = []
LOCK = threading.Lock()


def local(tag):
    return tag.rsplit("}", 1)[-1]


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        if self.path != "/commands":
            self.send_error(404)
            return
        with LOCK:
            body = json.dumps(COMMANDS).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        envelope = ET.fromstring(self.rfile.read(int(self.headers["Content-Length"])))
        body = next(node for node in envelope if local(node.tag) == "Body")
        operation = body[0]
        name = local(operation.tag)
        values = {
            local(node.tag): {**node.attrib, "value": node.text or ""}
            for node in operation.iter()
            if node is not operation
        }
        with LOCK:
            COMMANDS.append(
                {"operation": name, "time": time.monotonic(), "values": values}
            )
        address = f"http://127.0.0.1:{self.server.server_port}/onvif/service"
        now = datetime.now(UTC)
        if name == "GetSystemDateAndTime":
            result = f"<SystemDateAndTime><UTCDateTime><Time><Hour>{now.hour}</Hour><Minute>{now.minute}</Minute><Second>{now.second}</Second></Time><Date><Year>{now.year}</Year><Month>{now.month}</Month><Day>{now.day}</Day></Date></UTCDateTime></SystemDateAndTime>"
        elif name == "GetServices":
            result = "".join(
                f"<Service><Namespace>http://www.onvif.org/{version}/{service}/wsdl</Namespace><XAddr>{address}</XAddr><Version><Major>2</Major><Minor>0</Minor></Version></Service>"
                for version, service in [
                    ("ver10", "device"),
                    ("ver10", "media"),
                    ("ver20", "ptz"),
                ]
            )
        elif name == "GetCapabilities":
            result = f"<Capabilities><Media><XAddr>{address}</XAddr></Media><PTZ><XAddr>{address}</XAddr></PTZ></Capabilities>"
        elif name == "GetProfiles":
            result = f'<Profiles token="{PROFILE}"><Name>Test profile</Name><VideoSourceConfiguration token="video"/><VideoEncoderConfiguration token="encoder"><Encoding>H264</Encoding><Resolution><Width>640</Width><Height>360</Height></Resolution></VideoEncoderConfiguration><PTZConfiguration token="ptz"/></Profiles>'
        elif name == "GetDeviceInformation":
            result = "<Manufacturer>Protocol fixture</Manufacturer><Model>Test PTZ</Model><FirmwareVersion>1</FirmwareVersion><SerialNumber>isolated-test</SerialNumber><HardwareId>fixture</HardwareId>"
        elif name == "GetPresets":
            result = '<Preset token="entrance"><Name>Entrance</Name><PTZPosition><PanTilt x="0.1" y="0.2"/><Zoom x="0.3"/></PTZPosition></Preset>'
        elif name == "GetStatus":
            result = '<PTZStatus><Position><PanTilt x="0" y="0"/><Zoom x="0"/></Position><MoveStatus><PanTilt>IDLE</PanTilt><Zoom>IDLE</Zoom></MoveStatus></PTZStatus>'
        elif name in (
            "ContinuousMove",
            "AbsoluteMove",
            "RelativeMove",
            "Stop",
            "GotoPreset",
        ):
            result = ""
        elif name == "GetStreamUri":
            result = "<MediaUri><Uri>rtsp://127.0.0.1:15544/live/ha-test-main</Uri></MediaUri>"
        else:
            self.send_error(500)
            return
        payload = f'<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Body><{name}Response>{result}</{name}Response></s:Body></s:Envelope>'.encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/soap+xml")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=18081)
    args = parser.parse_args()
    server = ThreadingHTTPServer(("127.0.0.1", args.port), Handler)
    print(f"ONVIF protocol fixture listening on loopback:{args.port}", flush=True)
    server.serve_forever()
