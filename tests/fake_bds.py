"""模拟 BDS 服务器: HTTP 信令 (MOTD + SDP answer) + UDP 数据面 echo。

用法: python tests/fake_bds.py
监听: HTTP 127.0.0.1:19501, UDP 127.0.0.1:19511
"""
import json
import socket
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

HTTP_PORT = 19501
UDP_PORT = 19511

counter = [0]

SDP_TEMPLATE = (
    "v=0\r\n"
    "o=- 1000000001 2 IN IP4 127.0.0.1\r\n"
    "s=-\r\n"
    "t=0 0\r\n"
    "a=group:BUNDLE 0\r\n"
    "a=ice-ufrag:{ufrag}\r\n"
    "a=ice-pwd:{pwd}\r\n"
    "a=fingerprint:sha-256 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF"
    ":00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF\r\n"
    "a=setup:active\r\n"
    "m=application {port} UDP/DTLS/SCTP webrtc-datachannel\r\n"
    "c=IN IP4 127.0.0.1\r\n"
    "a=candidate:1000000001 1 udp 2122129151 127.0.0.1 {port} typ host generation 0 network-id 1 network-cost 10\r\n"
)


def udp_listener():
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", UDP_PORT))
    print(f"[fake-bds] udp echo on :{UDP_PORT}", flush=True)
    while True:
        data, addr = sock.recvfrom(65535)
        print(f"[fake-bds] udp from {addr} len={len(data)} head={data[:4].hex()}", flush=True)
        sock.sendto(data, addr)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({
            "name": "fake-bds", "protocol": 2193, "version": "test",
            "players": 0, "maxPlayers": 10, "gameType": 0,
        }).encode()
        self._respond(200, "application/json", body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        offer = self.rfile.read(length)
        counter[0] += 1
        print(f"[fake-bds] POST {self.path}, offer {len(offer)}B", flush=True)
        sdp = SDP_TEMPLATE.format(
            ufrag=f"fakeuf{counter[0]:04d}",
            pwd="fakepwd1234567890123456789",
            port=UDP_PORT,
        ).encode()
        self._respond(200, "application/sdp", sdp)

    def _respond(self, code, ctype, body):
        self.send_response(code)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


if __name__ == "__main__":
    threading.Thread(target=udp_listener, daemon=True).start()
    print(f"[fake-bds] http on :{HTTP_PORT}", flush=True)
    HTTPServer(("127.0.0.1", HTTP_PORT), Handler).serve_forever()
