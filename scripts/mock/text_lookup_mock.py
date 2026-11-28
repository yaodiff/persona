"""The text_lookup extension point as the manager answers it (#10977), for mock_hub.py's MOCK_TEXT_LOOKUP (iOS
TextLookupModel and its screenshots). A made-up dictionary of a few English words; nothing reaches a real extension.

MOCK_TEXT_LOOKUP=1            the feature text_lookup, an enabled extension "Relingo" that looks words up
MOCK_TEXT_LOOKUP=off          the feature, but no extension answers (available: false): clients stay inert
MOCK_TEXT_LOOKUP_UNDERLINE=1  underline_saved: true
MOCK_TEXT_LOOKUP_LATENCY=0.8  seconds every lookup waits (the card's loading state)
GET /mock/text-lookup?available=0|1&underline=0|1&fail=0|1&latency=S   changes it while the mock runs and sends
                              text_lookup_changed; fail=1 answers lookups 502 (the card's Retry). No query: the state.

Standard library only.
"""
import json
import os
import time
from urllib.parse import unquote

MODE = os.environ.get("MOCK_TEXT_LOOKUP", "")
ENABLED = MODE != ""
PATH = "/api/v1/text-lookup"

STATE = {
    "available": MODE not in ("", "off", "0"),
    "underline": os.environ.get("MOCK_TEXT_LOOKUP_UNDERLINE") == "1",
    "fail": False,
    "latency": float(os.environ.get("MOCK_TEXT_LOOKUP_LATENCY", "0.4") or 0),
}
SAVED = {"context"}  # lemmas saved in the made-up vocabulary list
LOG = []  # what the apps sent: (method, path, body), for the tests

# word -> (lemma, phonetic, senses). Forms point at their lemma.
DICTIONARY = {
    "context": ("context", "/ˈkɒntekst/", [("n.", "the situation in which something happens and that helps explain it"),
                                           ("n.", "the words around a word or passage that help fix its meaning")]),
    "deploy": ("deploy", "/dɪˈplɔɪ/", [("v.", "to put a new version of software into use on a server"),
                                       ("v.", "to move people or equipment into position for use")]),
    "latency": ("latency", "/ˈleɪtənsi/", [("n.", "the delay between asking for something and getting it")]),
    "summary": ("summary", "/ˈsʌməri/", [("n.", "a short statement of the main points")]),
    "weather": ("weather", "/ˈweðə(r)/", [("n.", "the state of the air at a place and time: rain, wind, sun")]),
    "widget": ("widget", "/ˈwɪdʒɪt/", [("n.", "a small part of a screen that shows information or a control")]),
    "landscape": ("landscape", "/ˈlændskeɪp/", [("n.", "the shape of a screen wider than it is tall"),
                                                ("n.", "everything you can see across an area of land")]),
    "review": ("review", "/rɪˈvjuː/", [("v.", "to look at something again carefully to judge it"),
                                       ("n.", "a careful look at something to judge it")]),
}
FORMS = {"deployed": "deploy", "deploys": "deploy", "deploying": "deploy", "contexts": "context", "widgets": "widget",
         "reviewed": "review", "reviews": "review", "summaries": "summary"}


# An English row the assistant put into the Inbox, for the lookup over an Inbox item (-openInboxItem assistant:tl1).
INBOX_BODY = ("Deploy summary: the weather widget now works in landscape.\n\n"
              "Review the release notes before Friday; the context for each change is in its pull request.")


def inbox_row(at):
    return {"id": "assistant:tl1", "source": "assistant", "kind": "assistant_post", "account": "assistant", "account_name": "Assistant",
            "title": "Release summary", "preview": "Deploy summary: the weather widget now works in landscape.", "count": 1,
            "started_at": at, "updated_at": at, "tier": "fyi", "status": "fyi",
            "summary": "Deploy summary: the weather widget now works in landscape.", "body": INBOX_BODY,
            "reason": "Your release task puts every run here", "read": False, "context": "Release summary\n" + INBOX_BODY}


def state():
    if not STATE["available"]:
        return {"available": False, "underline_saved": False, "words": []}
    return {"available": True, "extension": "relingo", "contribution": "relingo", "name": "Relingo",
            "underline_saved": STATE["underline"], "words": sorted(SAVED)}


def lookup(word):
    w = word.strip().lower().replace("’", "'")
    lemma = FORMS.get(w, w)
    entry = DICTIONARY.get(lemma)
    if not entry:
        return {"found": False, "word": word, "senses": [], "saved": lemma in SAVED}
    lemma, phonetic, senses = entry
    return {"found": True, "word": word, "lemma": lemma, "phonetic": phonetic,
            "senses": [{"pos": p, "text": t} for p, t in senses], "saved": lemma in SAVED}


def features(base):
    return base + (["text_lookup"] if ENABLED and "text_lookup" not in base else [])


def _unavailable(h):
    h.send_json({"error": "no extension looks words up", "code": "text_lookup_unavailable"}, 404)


def route(h, method, p, q, body, broadcast):
    """Answers the text-lookup routes and /mock/text-lookup; False for anything else (and for all of it when off)."""
    if not ENABLED:
        return False
    if p == "/mock/text-lookup" and method == "GET":
        changed = False
        for k in ("available", "underline", "fail"):
            if k in q:
                STATE[k] = q[k][0] == "1"
                changed = True
        if "latency" in q:
            STATE["latency"] = float(q["latency"][0] or 0)
        if q.get("reset", [""])[0] == "1":
            SAVED.clear(); SAVED.add("context"); LOG.clear(); changed = True
        if changed:
            broadcast({"type": "text_lookup_changed"})
        h.send_json(dict(STATE, saved=sorted(SAVED), log=LOG[-50:]))
        return True
    if not p.startswith(PATH):
        return False
    try:
        req = json.loads(body or b"{}") if body else {}
    except ValueError:
        req = {}
    LOG.append([method, p, req])
    if p == PATH and method == "GET":
        h.send_json(state())
        return True
    if not STATE["available"]:
        _unavailable(h)
        return True
    if p == PATH + "/lookup" and method == "POST":
        if STATE["latency"]:
            time.sleep(STATE["latency"])
        if STATE["fail"]:
            h.send_json({"error": "Relingo didn't answer in time", "code": "extension_unavailable"}, 502)
            return True
        word = str(req.get("word") or "").strip()
        if not word:
            h.send_json({"error": "word is required", "code": "invalid_request"}, 400)
            return True
        h.send_json(lookup(word))
        return True
    if p == PATH + "/words" and method == "POST":
        word = str(req.get("word") or "").strip().lower()
        if not word:
            h.send_json({"error": "word is required", "code": "invalid_request"}, 400)
            return True
        SAVED.add(FORMS.get(word, word))
        broadcast({"type": "text_lookup_changed"})
        h.send_json({"saved": True})
        return True
    if p.startswith(PATH + "/words/") and method == "DELETE":
        word = unquote(p[len(PATH + "/words/"):]).strip().lower()
        SAVED.discard(FORMS.get(word, word))
        broadcast({"type": "text_lookup_changed"})
        h.send_json({"saved": False})
        return True
    h.send_json({"error": "not found", "code": "not_found"}, 404)
    return True
