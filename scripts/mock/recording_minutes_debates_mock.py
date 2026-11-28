"""Mock hub domain: disagreements in meeting minutes (#16728, docs/assistant/meeting-minutes.md "Disagreements (#16728)").

Controlled at runtime without adding any MOCK_* environment switch (scripts/README.md):
  GET  /mock/recording_minutes_debates              -> {"mode": "off"|"on"|"malformed"}
  POST /mock/recording_minutes_debates {"mode": …}  -> switches mode; /mock/reset restores "off".

When mode is "off" (default), minutes and Markdown are completely untouched.
When mode is "on", recording_minutes attaches synthetic disagreements referencing the meeting's own segment timestamps
and kept decisions, and minutes_markdown renders the Disagreements section in both summary-card and legacy layouts
byte for byte as the hub's renderMinutes does.
When mode is "malformed", recording_minutes attaches a debates block that cleans down to nothing while leaving the rest
of the minutes and Markdown unchanged.
"""
import copy
from datetime import datetime, timedelta
import math

MODES = ("off", "on", "malformed")
STATE = {"mode": "off"}
HUB = None  # mock_hub.py's globals (mock_domains.Hub), set in register

# Fallback catalog entries matching server/internal/i18n/locales/*/kb-minutes.json.
_DEBATE_WORDS = {  # i18n-ignore: fixture words
    "en": {
        "debates": "Disagreements",
        "debate_open": "still disagreed",
        "debate_settled": "settled",
        "debate_status": " ({status})",
        "debate_outcome": "Outcome: ",
    },
    "zh-Hans": {
        "debates": "分歧",
        "debate_open": "仍有分歧",
        "debate_settled": "已谈拢",
        "debate_status": "（{status}）",
        "debate_outcome": "结果：",
    },
    "ja": {
        "debates": "意見の相違",
        "debate_open": "未合意",
        "debate_settled": "合意済み",
        "debate_status": "（{status}）",
        "debate_outcome": "結果：",
    },
}

# Synthetic sample debates in en, zh-Hans and ja (all data made up).
_SAMPLE_TEXT = {  # i18n-ignore: fixture words
    "en": {
        "q1": "Should the export cap count items or recordings?",
        "s1a": "Per item: one large file should not fail the whole export",
        "s1b": "Per recording: easier to explain",
        "o1": "Per item for this release",
        "q2": "Should the weekly digest default to on?",
        "s2a": "Default on so new users see the recap",
        "s2b": "Default off to avoid unsolicited email",
    },
    "zh-Hans": {
        "q1": "导出上限按单条还是按整份录音计算？",
        "s1a": "按单条算，避免一个大文件拖垮整次导出",
        "s1b": "按整份录音算，规则更好解释",
        "o1": "本版先按单条计算",
        "q2": "周报邮件是否默认开启？",
        "s2a": "默认开启，新用户能直接看到",
        "s2b": "默认关闭，避免打扰",
    },
    "ja": {
        "q1": "上限は項目ごとか録音全体か？",
        "s1a": "項目ごとにして1件の大きなファイルで全体を止めない",
        "s1b": "録音全体の方が説明しやすい",
        "o1": "今版では項目ごとにする",
        "q2": "週次ダイジェストを既定でオンにするか？",
        "s2a": "既定でオンにして新規ユーザーに届ける",
        "s2b": "既定でオフにして通知過多を避ける",
    },
}

# Cleans down to nothing under the contract's "Clients" rules: non-objects, blank question, too few valid positions,
# non-array positions. A client that keeps any of them shows words that say what it got wrong.
MINUTES_DEBATES_MALFORMED = [
    "not-an-object",
    None,
    42,
    {"question": "   ", "status": "open", "positions": [{"stance": "Blank question A", "at": 1}, {"stance": "Blank question B", "at": 2}]},
    {"question": "Only one valid stance", "status": "open", "positions": [
        {"stance": "One stance", "at": 10}, {"stance": "   ", "at": 20}, "bad", None]},
    {"question": "Non-array positions", "positions": {"stance": "Not a list"}},
]


def _non_neg(v):
    if type(v) in (int, float) and math.isfinite(v) and v >= 0:
        return v
    return None


def _non_blank(v):
    return v if isinstance(v, str) and v.strip() != "" else None


def clean_debates(raw, decisions_len=0):
    """What a client keeps of detail.minutes.debates by docs/assistant/meeting-minutes.md "Clients" rules."""
    if not isinstance(raw, list):
        return []
    out = []
    for entry in raw:
        if not isinstance(entry, dict):
            continue
        q = _non_blank(entry.get("question"))
        raw_pos = entry.get("positions")
        if q is None or not isinstance(raw_pos, list):
            continue
        positions = []
        for p in raw_pos:
            if not isinstance(p, dict):
                continue
            stance = _non_blank(p.get("stance"))
            if stance is None:
                continue
            positions.append({"stance": stance, "at": _non_neg(p.get("at")), "who": _non_blank(p.get("who"))})
        if len(positions) < 2:
            continue
        st = entry.get("status")
        status = st if st in ("open", "settled") else None
        outcome = None
        if status == "settled" and isinstance(entry.get("outcome"), dict):
            oo = entry["outcome"]
            text = _non_blank(oo.get("text"))
            if text is not None:
                dec_val = oo.get("decision")
                dec = None
                if type(dec_val) in (int, float) and math.isfinite(dec_val) and dec_val >= 0 and dec_val == int(dec_val):
                    idx = int(dec_val)
                    if 0 <= idx < decisions_len:
                        dec = idx
                outcome = {"text": text, "at": _non_neg(oo.get("at")), "decision": dec}
        out.append({
            "question": q,
            "topic": _non_blank(entry.get("topic")),
            "status": status,
            "at": _non_neg(entry.get("at")),
            "positions": positions,
            "outcome": outcome,
        })
    return out


def _t(lang, key, **args):
    s = HUB.minutes_t(lang, key, **args)
    if s == key and key in _DEBATE_WORDS.get(lang, {}):
        s = _DEBATE_WORDS[lang][key]
        for k, v in args.items():
            s = s.replace("{" + k + "}", str(v))
    return s


def _render_debates_section(debates, clock, lang, esc):
    out = "\n## " + _t(lang, "debates") + "\n"
    for d in debates:
        out += "\n### " + esc(d["question"])
        if d["status"] == "open":
            out += _t(lang, "debate_status", status=_t(lang, "debate_open"))
        elif d["status"] == "settled":
            out += _t(lang, "debate_status", status=_t(lang, "debate_settled"))
        out += "\n"
        for p in d["positions"]:
            line = "- " + clock(p["at"] if p["at"] is not None else 0) + esc(p["stance"])
            if p["who"]:
                line += HUB.minutes_t(lang, "who", who=esc(p["who"]))
            out += line + "\n"
        if d["status"] == "settled" and d["outcome"] and d["outcome"]["text"]:
            o = d["outcome"]
            out += "- " + clock(o["at"] if o["at"] is not None else 0) + _t(lang, "debate_outcome") + esc(o["text"]) + "\n"
    return out


def _wrap_minutes_markdown(orig_markdown):
    def minutes_markdown(m, body, lang, ov=None):
        kept = clean_debates(m.get("debates"), len(m.get("decisions") or []))
        if not kept:
            return orig_markdown(m, body, lang, ov)
        try:
            start = datetime.fromisoformat(str(body.get("started_at")).replace("Z", "+00:00")).astimezone()
        except (TypeError, ValueError):
            start = None
        clock = lambda sec: "[" + ((start + timedelta(microseconds=math.trunc(sec * 1_000_000 if isinstance(sec, (int, float)) else 0)))
                                   .strftime("%H:%M:%S") if start else "00:00:00") + "] "
        photo = lambda x: HUB.minutes_t(lang, "photo", n=x["photo"]) if x.get("photo") else ""
        title = m.get("title") or ("录音" if lang == "zh-Hans" else  # i18n-ignore: the recording's name
                                   HUB.minutes_t(lang, "untitled", when=start.strftime("%Y-%m-%d %H:%M") if start else ""))
        esc = (lambda s: str(s).replace("*", "\\*")) if ov else str
        out = "# " + esc(title) + "\n\n- " + HUB.minutes_t(lang, "when", when=m.get("when") or "") + "\n"
        if m.get("where"):
            out += "- " + HUB.minutes_t(lang, "where", where=esc(m["where"])) + "\n"
        if m.get("attendees"):
            out += "- " + HUB.minutes_t(lang, "attendees", names=HUB.minutes_t(lang, "list_sep").join(map(esc, m["attendees"]))) + "\n"
        if ov:
            if ov.get("headline"):
                out += "\n" + esc(ov["headline"]) + "\n"
            if ov.get("conclusions"):
                out += "\n## " + HUB.minutes_t(lang, "conclusions") + "\n\n"
                out += "".join("- " + clock(c["at"]) + HUB.minutes_bold(c["text"], c.get("strong")) + photo(c) + "\n" for c in ov["conclusions"])
            out += _render_debates_section(kept, clock, lang, esc)
            named = len(ov["groups"]) > 1
            for g in ov["groups"]:
                if named:
                    out += "\n## " + esc(g["title"]) + "\n"
                for t in g["topics"]:
                    out += "\n" + ("### " if named else "## ") + esc(t["title"]) + "\n\n"
                    out += "".join("- " + clock(x["at"]) + (HUB.minutes_t(lang, "decided") if x.get("decision") else "")
                                   + HUB.minutes_bold(x["text"], x.get("strong"))
                                   + (HUB.minutes_t(lang, "who", who=esc(x["who"])) if x.get("who") else "")
                                   + photo(x) + "\n" for x in t["bullets"])
        else:
            if m.get("points"):
                out += "\n## " + HUB.minutes_t(lang, "points") + "\n\n"
                out += "".join("- " + clock(x["at"]) + x["text"]
                               + (HUB.minutes_t(lang, "who", who=x["who"]) if x.get("who") else "")
                               + photo(x) + "\n" for x in m["points"])
            if m.get("decisions"):
                out += "\n## " + HUB.minutes_t(lang, "decisions") + "\n\n"
                out += "".join("- " + clock(x["at"]) + x["text"] + photo(x) + "\n" for x in m["decisions"])
        if m.get("actions"):
            out += "\n## " + HUB.minutes_t(lang, "actions") + "\n\n"
            out += "".join("- " + clock(a["at"]) + (HUB.minutes_t(lang, "owner", who=esc(a["who"])) if a.get("who") else "")
                           + esc(a["what"])
                           + (HUB.minutes_t(lang, "by", by=esc(a.get("by") or a.get("due_date"))) if a.get("by") or a.get("due_date") else "")
                           + photo(a) + "\n" for a in m["actions"])
        if not ov:
            out += _render_debates_section(kept, clock, lang, esc)
        if m.get("open"):
            out += "\n## " + HUB.minutes_t(lang, "open") + "\n\n"
            out += "".join("- " + clock(x["at"]) + esc(x["text"]) + photo(x) + "\n" for x in m["open"])
        if m.get("partial"):
            out += "\n> " + HUB.minutes_t(lang, "partial") + "\n"
        if ov:
            out += "\n*" + HUB.minutes_t(lang, "ai_note") + "*\n"
        return out.rstrip("\n")

    return minutes_markdown


def _sample_debates(out, body, rid):
    segs = [x for x in (HUB.transcription_record(rid) if rid and HUB.transcription_hub(rid) else body.get("segments", []))
            if x.get("text") and not x.get("echo")]
    if len(segs) < 2:
        return []
    lang = out.get("language") or "zh-Hans"
    words = _SAMPLE_TEXT.get(lang, _SAMPLE_TEXT["zh-Hans"])
    attendees = [a for a in (out.get("attendees") or []) if isinstance(a, str) and a.strip()]
    who1 = attendees[0] if len(attendees) >= 1 else None
    who2 = attendees[1] if len(attendees) >= 2 else None
    first_topic = None
    ov = out.get("overview")
    if isinstance(ov, dict) and isinstance(ov.get("groups"), list) and ov["groups"]:
        g0 = ov["groups"][0]
        if isinstance(g0, dict) and isinstance(g0.get("topics"), list) and g0["topics"]:
            t0 = g0["topics"][0]
            if isinstance(t0, dict) and isinstance(t0.get("title"), str) and t0["title"].strip():
                first_topic = t0["title"]

    t0, t1 = segs[0]["start"], segs[1]["start"]
    decisions = out.get("decisions") or []
    d1 = {
        "question": words["q1"],
        "status": "settled" if decisions else "open",
        "at": min(t0, t1),
        "positions": [
            {"stance": words["s1a"], "at": t0, **({"who": who1} if who1 else {})},
            {"stance": words["s1b"], "at": t1},
        ],
    }
    if first_topic:
        d1["topic"] = first_topic
    if decisions:
        d1["outcome"] = {"text": words["o1"], "at": decisions[0]["at"], "decision": 0}
    debates = [d1]
    if len(segs) >= 4:
        t2, t3 = segs[2]["start"], segs[3]["start"]
        debates.append({
            "question": words["q2"],
            "status": "open",
            "at": min(t2, t3),
            "positions": [
                {"stance": words["s2a"], "at": t2, **({"who": who1} if who1 else {})},
                {"stance": words["s2b"], "at": t3, **({"who": who2} if who2 else {})},
            ],
        })
    debates.sort(key=lambda d: d["at"])
    return debates


def _wrap_recording_minutes(orig_recording_minutes):
    def recording_minutes(body, rid=None):
        out = orig_recording_minutes(body, rid)
        mode = STATE["mode"]
        if mode == "off":
            return out
        if mode == "malformed":
            out["debates"] = copy.deepcopy(MINUTES_DEBATES_MALFORMED)
            return out
        debates = _sample_debates(out, body, rid)
        if debates:
            out["debates"] = debates
            shape = HUB.RECORDING_MINUTES_SHAPE[0]
            ov = out.get("overview") if shape == "overview" else None
            out["markdown"] = HUB.minutes_markdown(out, body, out.get("language") or "zh-Hans", ov)
        return out

    return recording_minutes


def _handle_get(h, _req):
    h.send_json({"mode": STATE["mode"]})


def _handle_post(h, req):
    try:
        body = req.json()
    except ValueError:
        h.send_json({"error": "invalid JSON"}, status=400)
        return
    if not isinstance(body, dict) or body.get("mode") not in MODES:
        h.send_json({"error": "mode must be off, on or malformed"}, status=400)
        return
    STATE["mode"] = body["mode"]
    h.send_json({"mode": STATE["mode"]})


def register(d):
    global HUB
    HUB = d.hub
    STATE["mode"] = "off"
    d.route("GET", r"/mock/recording_minutes_debates", _handle_get)
    d.route("POST", r"/mock/recording_minutes_debates", _handle_post)
    d.on_reset(lambda: STATE.update(mode="off"))
    d.hub.minutes_markdown = _wrap_minutes_markdown(d.hub.minutes_markdown)
    d.hub.recording_minutes = _wrap_recording_minutes(d.hub.recording_minutes)
