#!/usr/bin/env python3
"""Synthetic inbox chain fixture for InboxReportedDetailUITests.

The Mac runner starts this foreground process and owns its lifetime. Use --port 0
(or an explicitly free port), allow 15 seconds for the READY line, and pass its URL
as TEST_RUNNER_PERSONA_MOCK_HUB. Stop only the printed PID with SIGTERM, wait up to
five seconds, and SIGKILL that same PID only if it remains. The fixture creates and
removes its own temporary HOME; it never reads or changes production hub state.
"""
import argparse
import importlib.util
import os
from pathlib import Path
import signal
import sys
import tempfile


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, default=int(os.environ.get("PORT", "0")))
    args = parser.parse_args()
    if not 0 <= args.port <= 65535:
        parser.error("--port must be between 0 and 65535")

    with tempfile.TemporaryDirectory(prefix="persona-reported-detail-") as home:
        os.environ["HOME"] = home
        for key in list(os.environ):
            if key.startswith("MOCK_"):
                del os.environ[key]
        os.environ["PORT"] = str(args.port)
        os.environ["MOCK_INBOX_EVENTS"] = "1"
        os.environ["MOCK_MANAGERS"] = "0"

        root = Path(__file__).resolve().parents[3]
        spec = importlib.util.spec_from_file_location("reported_detail_mock", root / "scripts/mock/mock_hub.py")
        hub = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = hub
        spec.loader.exec_module(hub)

        def reported(identifier, detail, at, **fields):
            return {"id": identifier, "at": hub.ago(minutes=at), "kind": "reported", "via": "assistant",
                    "text": "Assistant says: " + identifier, "detail": detail, **fields}

        short = "A\nB"
        long = "L\nM\nN\nO\nP"
        control = "C\nD\nE\nF\nG"
        linked = "Q\nR\nS\nT\nU"
        messages = long
        item = {
            "id": "reported-detail-fixture",
            "source": "chat",
            "kind": "dm",
            "account": "chat",
            "account_name": "Example chat",
            "title": "Reported detail fixture",
            "where": "Example chat",
            "sender": "Example sender",
            "preview": "Synthetic reported detail rows",
            "count": 1,
            "started_at": hub.ago(minutes=60),
            "updated_at": hub.ago(minutes=1),
            "tier": "fyi",
            "status": "working",
            "read": True,
            "chain": [
                # Omitted messages and an explicitly empty messages array cover both plain forms.
                reported("reported-short", short, 50),
                reported("reported-long", long, 49, messages=[]),
                {"id": "control-plain", "at": hub.ago(minutes=48), "kind": "triaged", "text": control},
                reported("reported-link", linked, 47, link={"type": "source"}),
                reported("reported-messages", messages, 46,
                         messages=[{"sender": "Example agent", "text": "Expansion marker",
                                    "at": hub.ago(minutes=46)}]),
            ],
        }
        hub.INBOX[:] = [item]

        server = hub.ThreadingHTTPServer(("127.0.0.1", args.port), hub.H)
        server.daemon_threads = True
        hub.PORT = server.server_port
        hub.ENDPOINTS.append(hub.endpoint("127.0.0.1", hub.PORT, "http", "lan"))
        signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
        print(f"REPORTED_DETAIL_HUB_READY url=http://127.0.0.1:{hub.PORT} pid={os.getpid()}", flush=True)
        try:
            server.serve_forever()
        except KeyboardInterrupt:
            pass
        finally:
            server.server_close()


if __name__ == "__main__":
    main()
