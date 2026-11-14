"""The mock hub records desktop client registration: register_desktop on a socket, withdrawn by unregister_desktop or by
the socket closing (GET /mock/desktops), and lists desktop_unregister only with MOCK_DESKTOPS=1."""
import base64
import json
import os
import socket
import time
import unittest
from urllib.parse import quote
from urllib.request import Request, urlopen

from test_mock_hub_mesh import Mock


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


class Socket:
    """A WebSocket to the mock, enough to send one masked text frame at a time."""

    def __init__(self, port, token=None, query_token=False):
        self.sock = socket.create_connection(("127.0.0.1", port), timeout=3)
        stream = self.sock.makefile("rb")
        key = base64.b64encode(os.urandom(16)).decode()
        credential = "" if query_token else f"Authorization: Bearer {token}\r\n" if token else ""
        query = f"?token={quote(token or '')}" if query_token else ""
        self.sock.sendall((f"GET /api/v1/ws{query} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nUpgrade: websocket\r\n"
                           f"Connection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
                           f"{credential}\r\n").encode())
        status = stream.readline()
        stream.close()
        if b" 101 " not in status:
            raise AssertionError(status)

    def send(self, message):
        data = json.dumps(message).encode()
        mask = os.urandom(4)
        assert len(data) < 126
        self.sock.sendall(bytes([0x81, 0x80 | len(data)]) + mask + bytes(b ^ mask[i % 4] for i, b in enumerate(data)))

    def close(self):
        self.sock.close()


class DesktopsMockTests(unittest.TestCase):
    def hub(self, **env):
        hub = Mock(free_port(), **env)
        self.addCleanup(hub.stop)
        return hub

    def state(self, hub, want):
        """The registration state once it is `want` (the mock reads a socket's message on its own thread)."""
        deadline = time.monotonic() + 5
        while True:
            state = hub.get("/mock/desktops")
            if want(state) or time.monotonic() > deadline:
                return state
            time.sleep(0.05)

    def test_register_then_unregister_and_close(self):
        hub = self.hub(MOCK_DESKTOPS="1")
        self.assertEqual(hub.get("/mock/desktops"), {"registered": [], "log": []})
        first = Socket(hub.port)
        self.addCleanup(first.close)
        first.send({"type": "unregister_desktop"})  # nothing registered on this socket: not a withdrawal
        first.send({"type": "register_desktop", "device_name": "Persona desktop"})
        state = self.state(hub, lambda s: s["registered"])
        self.assertEqual(state, {"registered": ["Persona desktop"], "log": [{"event": "register", "device_name": "Persona desktop"}]})
        first.send({"type": "unregister_desktop"})
        state = self.state(hub, lambda s: not s["registered"])
        self.assertEqual([e["event"] for e in state["log"]], ["register", "unregister"])
        first.send({"type": "register_desktop", "device_name": "Persona desktop"})
        self.state(hub, lambda s: s["registered"])
        first.close()
        state = self.state(hub, lambda s: not s["registered"])
        self.assertEqual(state["registered"], [])
        self.assertEqual([e["event"] for e in state["log"]], ["register", "unregister", "register", "close"])

    def test_answer_only_cannot_register(self):
        hub = self.hub(MOCK_PAIR_TOKEN="jp_desktop_test")
        request = Request(hub.base + "/api/v1/auth/tokens", method="POST",
                          data=json.dumps({"kind": "watch", "name": "Test watch"}).encode(),
                          headers={"Authorization": "Bearer jp_desktop_test", "Content-Type": "application/json"})
        with urlopen(request, timeout=5) as response:
            credential = json.load(response)
        self.assertEqual(credential["role"], "answer")
        watch = Socket(hub.port, credential["token"])
        self.addCleanup(watch.close)
        for _ in range(3):
            watch.send({"type": "register_desktop", "device_name": "Not a desktop"})
            state = self.state(hub, lambda s: s["registered"])
            self.assertEqual(state, {"registered": [], "log": []})

    def test_full_credential_can_register(self):
        hub = self.hub(MOCK_PAIR_TOKEN="jp_desktop_test")
        request = Request(hub.base + "/api/v1/auth/tokens", method="POST",
                          data=json.dumps({"kind": "phone", "name": "Test phone"}).encode(),
                          headers={"Authorization": "Bearer jp_desktop_test", "Content-Type": "application/json"})
        with urlopen(request, timeout=5) as response:
            credential = json.load(response)
        self.assertEqual(credential["role"], "full")
        phone = Socket(hub.port, credential["token"], query_token=True)
        self.addCleanup(phone.close)
        phone.send({"type": "register_desktop", "device_name": "Persona desktop"})
        state = self.state(hub, lambda s: s["registered"])
        self.assertEqual(state["registered"], ["Persona desktop"])

    def test_feature_only_when_asked(self):
        self.assertNotIn("desktop_unregister", self.hub().get("/api/v1/health")["features"])
        features = self.hub(MOCK_DESKTOPS="1").get("/api/v1/health")["features"]
        self.assertIn("desktops", features)
        self.assertIn("desktop_unregister", features)


if __name__ == "__main__":
    unittest.main()
