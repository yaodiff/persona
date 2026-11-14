"""Web Push as the hub answers it (#7459, docs/web/web.md "Web Push", docs/protocol/protocol.md "Web Push"), for mock_hub.py's
MOCK_WEB_PUSH (web/e2e/web-push.mjs, screenshots of Settings › Notifications).

MOCK_WEB_PUSH=1       the routes, no browser subscribed yet
MOCK_WEB_PUSH=failed  one browser (web-mock-failed) subscribed whose last push the push service refused
MOCK_WEB_PUSH_TEST=fail  Send Test answers 502 push_failed, as a push service that refuses it
GET /mock/web_push     what the browsers PUT (the e2e reads the endpoint its browser subscribed with)

The VAPID key is RFC 8291 Appendix A's application server key, a published example (web/e2e/web-push.mjs --live signs
with its private half), unless MOCK_WEB_PUSH_KEY gives another public key. The mock pushes nothing itself: the e2e
hands its browser a push through the DevTools protocol, or has the Go sender push it (--live). Standard library only.
"""
import json
import os
from datetime import datetime, timezone
from urllib.parse import unquote, urlparse

# RFC 8291 Appendix A: the application server's public key (its private key is in web/e2e/web-push.mjs).
EXAMPLE_KEY = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
SERVICES = (("FCM", "fcm.googleapis.com", False), ("Mozilla", "push.services.mozilla.com", True),
            ("Apple", "push.apple.com", True), ("Windows", "notify.windows.com", True))
PREFIX = "/api/v1/push/web"

SUBS = {}  # device -> what it PUT, with created_at / updated_at and its last delivery


def mode():
    return os.environ.get("MOCK_WEB_PUSH", "")


def key():
    return os.environ.get("MOCK_WEB_PUSH_KEY") or EXAMPLE_KEY


def _now():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def service(host):
    host = (host or "").lower().rstrip(".")
    for name, h, suffix in SERVICES:
        if host == h or (suffix and host.endswith("." + h)):
            return name
    return ""


def status(device):
    s = SUBS.get(device)
    if not s:
        return {"subscribed": False}
    host = (urlparse(s["endpoint"]).hostname or "").lower()
    out = {"subscribed": True, "service": service(host), "host": host, "browser": s.get("browser", ""),
           "current": s.get("key") == key(), "endpoint": s["endpoint"], "created_at": s["created_at"], "updated_at": s["updated_at"]}
    for k in ("last_ok_at", "failed_at", "status", "error"):
        if k in s:
            out[k] = s[k]
    return out


if mode() == "failed":
    SUBS["web-mock-failed"] = {"endpoint": "https://web.push.apple.com/QGuQyavXutnMHmock", "key": key(), "browser": "Safari · iPhone",
                               "created_at": _now(), "updated_at": _now(), "failed_at": _now(), "status": 403,
                               "error": "403 BadJwtToken"}


def route(h, method, p, body=b""):
    """True when it answered: the Web Push routes while MOCK_WEB_PUSH is set (else an older hub's 404), and /mock/web_push."""
    if not mode():
        return False
    if p == "/mock/web_push" and method == "GET":
        h.send_json({"key": key(), "subscriptions": SUBS})
        return True
    if p == PREFIX and method == "GET":
        h.send_json({"key": key()})
        return True
    if not p.startswith(PREFIX + "/"):
        return False
    rest = p[len(PREFIX) + 1:]
    test = rest.endswith("/test")
    device = unquote(rest[: -len("/test")] if test else rest)
    if not device or "/" in device:
        return False
    if method == "GET" and not test:
        h.send_json(status(device))
        return True
    if method == "PUT" and not test:
        try:
            b = json.loads(body or b"{}")
        except ValueError:
            b = {}
        endpoint, keys = str(b.get("endpoint") or ""), b.get("keys") or {}
        u = urlparse(endpoint)
        if u.scheme != "https" or not service(u.hostname):
            h.send_json({"error": "%s is not a push service Persona sends to." % (u.hostname or endpoint), "code": "unknown_push_service"}, 400)
            return True
        if not keys.get("p256dh") or not keys.get("auth"):
            h.send_json({"error": "This browser's push subscription keys are invalid.", "code": "bad_keys"}, 400)
            return True
        if b.get("key") != key():
            h.send_json({"error": "The hub's push key has changed. Subscribe again.", "code": "key_changed", "key": key()}, 409)
            return True
        cur = SUBS.get(device) or {}
        SUBS[device] = {"endpoint": endpoint, "keys": keys, "key": b.get("key"), "locale": b.get("locale", ""),
                        "features": b.get("features") or [], "browser": str(b.get("browser") or "")[:64],
                        "created_at": cur.get("created_at") if cur.get("endpoint") == endpoint else _now(), "updated_at": _now()}
        for k in ("last_ok_at", "failed_at", "status", "error"):
            if cur.get("endpoint") == endpoint and k in cur:
                SUBS[device][k] = cur[k]
        h.send_json(status(device))
        return True
    if method == "DELETE" and not test:
        h.send_json({"ok": True, "removed": SUBS.pop(device, None) is not None})
        return True
    if method == "POST" and test:
        s = SUBS.get(device)
        if not s:
            h.send_json({"error": "This browser has not turned on notifications while Persona is closed.", "code": "not_subscribed"}, 404)
            return True
        if os.environ.get("MOCK_WEB_PUSH_TEST") == "fail":
            s.update({"failed_at": _now(), "status": 410, "error": "410 push subscription has unsubscribed or expired"})
            h.send_json({"error": "The push service did not take the notification: 410 push subscription has unsubscribed or expired",
                         "code": "push_failed", "push_status": 410, "gone": True}, 502)
            return True
        s["last_ok_at"] = _now()
        for k in ("failed_at", "status", "error"):
            s.pop(k, None)
        h.send_json({"ok": True, "push_status": 201})
        return True
    return False
