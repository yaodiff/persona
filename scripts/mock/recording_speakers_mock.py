"""Mock hub domain: meeting speakers (#17674, docs/assistant/meeting-minutes.md "Speakers (#17674)").

Registered in scripts/mock/mock_domains.py with no MOCK_* environment switches (scripts/README.md):
  - advertises feature `meeting_speakers` in hello / /api/v1/health whenever hub transcription is on;
  - seeds synthetic hub-transcribed sample meetings (ready, waiting, low quality, failed, trimmed) into
    KB["recordings"] and KB_DETAIL (without polluting phone sync's RECORDINGS);
  - serves POST /api/v1/recordings/{id}/speakers with all four ops (name, merge, unmerge, clear), rev /
    409 speakers_stale, 409 speakers_cleared, automatic merge, unmerge returning unnamed, minor recomputation,
    server-computed undo, detail updates, and meeting_live broadcast;
  - serves GET /mock/recording_speakers and POST /mock/recording_speakers/seed for fixture replay tests.
"""
import copy
import re

HUB = None  # mock_hub.py's globals (mock_domains.Hub), set in register

FLOAT_EPS = 1e-9
MINOR_THRESHOLD_SECONDS = 20.0
MAX_NAME_RUNES = 100
SURE_MIN_SHARE = 0.70
SURE_MIN_SECONDS = 1.0
MAX_SAMPLES = 3
OWNER_DEFAULT = "Grace"

SPEAKER_ID_RE = re.compile(r"^s[1-9][0-9]*$")

# Deterministic UUIDs for synthetic speaker-separation sample meetings.
SAMPLE_READY = "7a100000-0000-4000-8000-000000000001"
SAMPLE_WAITING = "7a100000-0000-4000-8000-000000000002"
SAMPLE_LOW_QUALITY = "7a100000-0000-4000-8000-000000000003"
SAMPLE_FAILED = "7a100000-0000-4000-8000-000000000004"
SAMPLE_TRIMMED = "7a100000-0000-4000-8000-000000000005"

SAMPLE_IDS = {
    "ready": SAMPLE_READY,
    "waiting": SAMPLE_WAITING,
    "low_quality": SAMPLE_LOW_QUALITY,
    "failed": SAMPLE_FAILED,
    "trimmed": SAMPLE_TRIMMED,
}

# Internal per-recording speaker state (rev, stored roster, stats, spans, covered, timing, trimmed, etc.).
SPEAKER_STORE = {}


class EditError(Exception):
    def __init__(self, code, status=400, message=None):
        super().__init__(message or code)
        self.code = code
        self.status = status
        self.message = message or code


def _speaker_num(sid):
    return int(sid[1:])


def _valid_speaker_id(sid):
    return isinstance(sid, str) and bool(SPEAKER_ID_RE.match(sid))


def _canonical_id(people, sid):
    by_id = {p["id"]: p for p in people}
    cur = sid
    for _ in range(len(people) + 1):
        p = by_id.get(cur)
        if not p or not p.get("merged_into"):
            return cur
        nxt = p["merged_into"]
        if nxt == cur:
            return cur
        cur = nxt
    return cur


def _merged_seconds(stats, people):
    secs = {}
    for sid, st in (stats or {}).items():
        canon = _canonical_id(people, sid)
        secs[canon] = secs.get(canon, 0.0) + float(st[0])
    return secs


def _sum_stats(stats, people):
    secs = {}
    lines = {}
    for sid, st in (stats or {}).items():
        canon = _canonical_id(people, sid)
        secs[canon] = secs.get(canon, 0.0) + float(st[0])
        lines[canon] = lines.get(canon, 0) + int(st[1])
    return {cid: [secs[cid], lines[cid]] for cid in secs}


def _apply_minor(stats, people):
    secs = _merged_seconds(stats, people)
    for p in people:
        if p.get("merged_into"):
            p.pop("minor", None)
            continue
        if secs.get(p["id"], 0.0) + FLOAT_EPS < MINOR_THRESHOLD_SECONDS:
            p["minor"] = True
        else:
            p.pop("minor", None)


def _norm_person(p):
    out = {"id": p["id"]}
    if p.get("name"):
        out["name"] = p["name"]
    if p.get("me"):
        out["me"] = True
    if p.get("merged_into"):
        out["merged_into"] = p["merged_into"]
    if p.get("minor"):
        out["minor"] = True
    return out


def _index_of(people, sid):
    for i, p in enumerate(people):
        if p["id"] == sid:
            return i
    return -1


def _validate_name(raw):
    if not isinstance(raw, str):
        raise EditError("recordingspeakers.name.invalid")
    if "\n" in raw or "\r" in raw:
        raise EditError("recordingspeakers.name.multiline")
    trimmed = raw.strip()
    if trimmed == "":
        return ""
    if len(trimmed) > MAX_NAME_RUNES:
        raise EditError("recordingspeakers.name.too_long")
    return trimmed


def _name_undo_op(sid, name, me):
    op = {"op": "name", "id": sid, "name": name or ""}
    if me:
        op["me"] = True
    return op


def apply_edit(roster, stats, ops, owner_name=OWNER_DEFAULT):
    """Apply ops atomically to roster matching server/internal/recordingspeakers/rules.go.

    Returns (new_roster, undo_ops, cleared). Raises EditError on invalid input.
    """
    if not isinstance(ops, list) or len(ops) == 0:
        raise EditError("recordingspeakers.ops.empty")

    people = [_norm_person(p) for p in (roster.get("people") or [])]
    edits = dict(roster.get("edits") or {})
    next_num = int(roster.get("next") or 1)
    undo_groups = []
    cleared = False

    for raw_op in ops:
        if not isinstance(raw_op, dict):
            raise EditError("recordingspeakers.op.unknown")
        kind = raw_op.get("op")
        if kind == "name":
            sid = raw_op.get("id")
            if not _valid_speaker_id(sid):
                raise EditError("recordingspeakers.speaker.format")
            idx = _index_of(people, sid)
            if idx < 0:
                raise EditError("recordingspeakers.speaker.not_found")
            if people[idx].get("merged_into"):
                raise EditError("recordingspeakers.speaker.merged")
            me = bool(raw_op.get("me", False))
            raw_name = raw_op.get("name", "")
            if raw_name is None:
                raw_name = ""
            if me and isinstance(raw_name, str) and raw_name.strip() == "" and "\n" not in raw_name and "\r" not in raw_name:
                raw_name = owner_name or ""
            target_name = _validate_name(raw_name)
            old_name = people[idx].get("name", "")
            old_me = bool(people[idx].get("me", False))

            other_idx = -1
            if target_name:
                for i, p in enumerate(people):
                    if i != idx and not p.get("merged_into") and p.get("name") == target_name:
                        other_idx = i
                        break

            prev_owner_idx = -1
            if me:
                for i, p in enumerate(people):
                    if i != idx and i != other_idx and not p.get("merged_into") and p.get("me"):
                        prev_owner_idx = i
                        break

            if other_idx < 0:
                ug = []
                if prev_owner_idx >= 0:
                    po = people[prev_owner_idx]
                    po.pop("me", None)
                    ug.append(_name_undo_op(po["id"], po.get("name", ""), True))
                if old_name != target_name or old_me != me:
                    if target_name:
                        people[idx]["name"] = target_name
                    else:
                        people[idx].pop("name", None)
                    if me:
                        people[idx]["me"] = True
                    else:
                        people[idx].pop("me", None)
                    ug = [_name_undo_op(sid, old_name, old_me)] + ug
                if ug:
                    undo_groups.append(ug)
            else:
                secs = _merged_seconds(stats, people)
                p_sec = secs.get(people[idx]["id"], 0.0)
                o_sec = secs.get(people[other_idx]["id"], 0.0)
                if p_sec > o_sec + FLOAT_EPS:
                    from_idx, into_idx = other_idx, idx
                else:
                    from_idx, into_idx = idx, other_idx
                from_id = people[from_idx]["id"]
                into_id = people[into_idx]["id"]
                other_old_me = bool(people[other_idx].get("me", False))

                children = [p["id"] for p in people if p.get("merged_into") == from_id]
                merged_me = me or old_me or other_old_me

                prev_owner_restore = None
                if prev_owner_idx >= 0:
                    po = people[prev_owner_idx]
                    po.pop("me", None)
                    prev_owner_restore = _name_undo_op(po["id"], po.get("name", ""), True)

                people[into_idx]["name"] = target_name
                if merged_me:
                    people[into_idx]["me"] = True
                else:
                    people[into_idx].pop("me", None)
                people[into_idx].pop("merged_into", None)

                people[from_idx].pop("name", None)
                people[from_idx].pop("me", None)
                people[from_idx].pop("minor", None)
                people[from_idx]["merged_into"] = into_id
                for p in people:
                    if p.get("merged_into") == from_id:
                        p["merged_into"] = into_id

                ug = []
                if from_idx == idx:
                    ug.append({"op": "unmerge", "id": from_id})
                    for cid in children:
                        ug.append({"op": "unmerge", "id": cid})
                        ug.append({"op": "merge", "from": cid, "into": from_id})
                    if old_name or old_me:
                        ug.append(_name_undo_op(from_id, old_name, old_me))
                    if merged_me != other_old_me:
                        ug.append(_name_undo_op(into_id, target_name, other_old_me))
                else:
                    ug.append(_name_undo_op(into_id, old_name, old_me))
                    ug.append({"op": "unmerge", "id": from_id})
                    for cid in children:
                        ug.append({"op": "unmerge", "id": cid})
                        ug.append({"op": "merge", "from": cid, "into": from_id})
                    ug.append(_name_undo_op(from_id, target_name, other_old_me))
                if prev_owner_restore is not None:
                    ug.append(prev_owner_restore)
                undo_groups.append(ug)

            edits["name"] = edits.get("name", 0) + 1

        elif kind == "merge":
            frm = raw_op.get("from")
            into = raw_op.get("into")
            if not _valid_speaker_id(frm) or not _valid_speaker_id(into):
                raise EditError("recordingspeakers.speaker.format")
            from_idx = _index_of(people, frm)
            into_idx = _index_of(people, into)
            if from_idx < 0 or into_idx < 0:
                raise EditError("recordingspeakers.speaker.not_found")
            if people[from_idx].get("merged_into"):
                raise EditError("recordingspeakers.speaker.already_merged")
            target_id = _canonical_id(people, into)
            if target_id == frm:
                raise EditError("recordingspeakers.merge.same_speaker")

            from_old_name = people[from_idx].get("name", "")
            from_old_me = bool(people[from_idx].get("me", False))
            children = [p["id"] for p in people if p.get("merged_into") == frm]

            people[from_idx]["merged_into"] = target_id
            people[from_idx].pop("name", None)
            people[from_idx].pop("me", None)
            people[from_idx].pop("minor", None)
            for p in people:
                if p.get("merged_into") == frm:
                    p["merged_into"] = target_id

            ug = [{"op": "unmerge", "id": frm}]
            for cid in children:
                ug.append({"op": "unmerge", "id": cid})
                ug.append({"op": "merge", "from": cid, "into": frm})
            if from_old_name or from_old_me:
                ug.append(_name_undo_op(frm, from_old_name, from_old_me))
            undo_groups.append(ug)
            edits["merge"] = edits.get("merge", 0) + 1

        elif kind == "unmerge":
            sid = raw_op.get("id")
            if not _valid_speaker_id(sid):
                raise EditError("recordingspeakers.speaker.format")
            idx = _index_of(people, sid)
            if idx < 0:
                raise EditError("recordingspeakers.speaker.not_found")
            prev_target = people[idx].get("merged_into")
            if not prev_target:
                raise EditError("recordingspeakers.speaker.not_merged")
            people[idx].pop("merged_into", None)
            people[idx].pop("name", None)
            people[idx].pop("me", None)
            undo_groups.append([{"op": "merge", "from": sid, "into": prev_target}])
            edits["unmerge"] = edits.get("unmerge", 0) + 1

        elif kind == "clear":
            people = []
            undo_groups = []
            cleared = True
            edits["clear"] = edits.get("clear", 0) + 1

        else:
            raise EditError("recordingspeakers.op.unknown")

    _apply_minor(stats, people)
    undo = []
    for grp in reversed(undo_groups):
        undo.extend(grp)
    clean_edits = {k: v for k, v in edits.items() if v}
    return {"next": next_num, "people": [_norm_person(p) for p in people], "edits": clean_edits}, undo, cleared


def _merge_intervals(ivs):
    valid = sorted((float(a), float(b)) for a, b in ivs if b > a)
    if not valid:
        return []
    out = [list(valid[0])]
    for a, b in valid[1:]:
        if a <= out[-1][1]:
            if b > out[-1][1]:
                out[-1][1] = b
        else:
            out.append([a, b])
    return out


def _overlap(a, b):
    tot = 0.0
    i = j = 0
    while i < len(a) and j < len(b):
        lo = max(a[i][0], b[j][0])
        hi = min(a[i][1], b[j][1])
        if hi > lo:
            tot += hi - lo
        if a[i][1] < b[j][1]:
            i += 1
        else:
            j += 1
    return tot


def _project_record(rec):
    """Build detail.speakers and updated segments from an internal speaker store record."""
    state = rec["state"]
    trimmed = bool(rec.get("trimmed", False))
    people = rec.get("people") or []
    stats = rec.get("stats") or {}
    spans = rec.get("spans") or []
    covered = rec.get("covered") or []
    quality = rec.get("quality", "")

    ds = {
        "rev": rec["rev"],
        "state": state,
        "reason": rec.get("reason", ""),
        "quality": quality,
        "source": rec.get("source", ""),
        "people": [],
        "candidates": list(rec.get("candidates") or []),
        "spans": [] if (trimmed or state != "ready") else copy.deepcopy(spans),
        "covered": [] if (trimmed or state != "ready") else copy.deepcopy(covered),
        "trimmed": trimmed,
    }
    if state == "waiting":
        ds["done"] = rec.get("done", 0.0)
        ds["end"] = rec.get("end", 0.0)

    segs = copy.deepcopy(rec.get("base_segments")) if not trimmed else None
    samples_by_id = {}

    if segs is not None:
        if state == "ready" and spans:
            by_orig = {}
            for s0, s1, sid in spans:
                by_orig.setdefault(sid, []).append((s0, s1))
            merged_orig = {sid: _merge_intervals(ivs) for sid, ivs in by_orig.items()}
            merged_cov = _merge_intervals(covered)
            minor_set = {p["id"] for p in people if p.get("minor") and not p.get("merged_into")}
            timing = rec.get("timing", "turn")
            by_speaker_cands = {}
            for sg in segs:
                sg.pop("speaker", None)
                sg.pop("speaker_unsure", None)
                if sg.get("echo"):
                    continue
                if sg.get("source") == "hub" and timing == "spread":
                    continue
                a, b = float(sg["start"]), float(sg["end"])
                dur = max(0.0, b - a)
                if not merged_cov or (dur > 0 and _overlap([[a, b]], merged_cov) + FLOAT_EPS < dur * 0.50):
                    continue
                by_canon = {}
                total = 0.0
                for orig_id, ivs in merged_orig.items():
                    ov = _overlap([[a, b]], ivs)
                    if ov > 0:
                        canon = _canonical_id(people, orig_id)
                        by_canon[canon] = by_canon.get(canon, 0.0) + ov
                        total += ov
                if total <= 0:
                    continue
                best_id = sorted(by_canon.keys(), key=_speaker_num)[0]
                best_sec = by_canon[best_id]
                for cid in sorted(by_canon.keys(), key=_speaker_num)[1:]:
                    if by_canon[cid] > best_sec + FLOAT_EPS:
                        best_id, best_sec = cid, by_canon[cid]
                sure = (
                    quality == "ok"
                    and best_sec > total * SURE_MIN_SHARE + FLOAT_EPS
                    and best_sec + FLOAT_EPS >= SURE_MIN_SECONDS
                    and best_id not in minor_set
                )
                sg["speaker"] = best_id
                if not sure:
                    sg["speaker_unsure"] = True
                else:
                    by_speaker_cands.setdefault(best_id, []).append((dur, a))
            for sid, cands in by_speaker_cands.items():
                cands.sort(key=lambda x: (-x[0], x[1]))
                samples_by_id[sid] = sorted(x[1] for x in cands[:MAX_SAMPLES])
        else:
            for sg in segs:
                sg.pop("speaker", None)
                sg.pop("speaker_unsure", None)

    if state != "cleared":
        summed = _sum_stats(stats, people)
        for p in people:
            dp = _norm_person(p)
            if not dp.get("merged_into"):
                st = summed.get(dp["id"])
                if st:
                    dp["seconds"] = st[0]
                    dp["lines"] = st[1]
                if samples_by_id.get(dp["id"]):
                    dp["samples"] = samples_by_id[dp["id"]]
            ds["people"].append(dp)

    return ds, segs


def _sync_detail(rid):
    rec = SPEAKER_STORE[rid]
    ds, segs = _project_record(rec)
    detail = HUB.KB_DETAIL.get(("recordings", rid))
    if detail is None:
        return ds
    detail["speakers"] = ds
    if segs is not None:
        detail["segments"] = segs
    elif rec.get("trimmed"):
        detail.pop("segments", None)
    # Keep debate speaker attributions aligned with merges / clear.
    minutes = detail.get("minutes")
    if isinstance(minutes, dict) and isinstance(minutes.get("debates"), list):
        people = rec.get("people") or []
        for d in minutes["debates"]:
            for pos in d.get("positions") or []:
                if rec["state"] == "cleared":
                    pos.pop("speaker", None)
                    pos.pop("speaker_unsure", None)
                    for al in pos.get("also") or []:
                        al.pop("speaker", None)
                        al.pop("speaker_unsure", None)
                else:
                    if pos.get("speaker"):
                        pos["speaker"] = _canonical_id(people, pos["speaker"])
                    for al in pos.get("also") or []:
                        if al.get("speaker"):
                            al["speaker"] = _canonical_id(people, al["speaker"])
    return ds


def _seed_sample(rid, title, started_at, duration, rec, detail_extra):
    entry = {
        "kind": "recordings",
        "id": rid,
        "name": title,
        "one_line": detail_extra.get("one_line", title),
        "importance": "P1",
        "relevance": 70,
        "updated": started_at,
        "extra": "phone",
        "recording_kind": "meeting",
    }
    HUB.KB["recordings"] = [e for e in HUB.KB.get("recordings", []) if e["id"] != rid] + [entry]
    meta = {
        "id": rid,
        "kind": "meeting",
        "title": title,
        "name": title,
        "name_source": "title",
        "started_at": started_at,
        "duration": duration,
        "device": "phone",
        "language": "en-US",
        "complete": True,
        "transcription": {"mode": "hub", "state": "final", "gaps": []},
    }
    if rec.get("trimmed"):
        meta["trimmed_at"] = started_at
        meta["minutes_state"] = "ready"
    elif "minutes" in detail_extra:
        meta["minutes_state"] = "ready"
    detail = {
        "summary": detail_extra.get("summary", ""),
        "meta": meta,
        "recent": detail_extra.get("recent", ""),
    }
    if "minutes" in detail_extra:
        detail["minutes"] = detail_extra["minutes"]
    HUB.KB_DETAIL[("recordings", rid)] = detail
    SPEAKER_STORE[rid] = rec
    _sync_detail(rid)


def seed_all():
    """Seed the five synthetic speaker-separation sample meetings into KB["recordings"] and KB_DETAIL."""
    SPEAKER_STORE.clear()
    now_iso = HUB.ago(hours=3)

    # 1. Ready sample: timing "turn", named + unnamed + minor + merged, 2 debates with speaker/speaker_unsure/also.
    ready_segments = [
        {"start": 10.0, "end": 40.0, "text": "We should keep speaker timelines local on the hub.", "source": "hub"},
        {"start": 42.0, "end": 75.0, "text": "Agreed, and let owners name speakers after the meeting.", "source": "hub"},
        {"start": 78.0, "end": 120.0, "text": "What about exporting the transcript with labels?", "source": "hub"},
        {"start": 122.0, "end": 152.0, "text": "Per-item export limits avoid failing the whole archive.", "source": "hub"},
        {"start": 154.0, "end": 162.0, "text": "Sounds reasonable.", "source": "hub"},
        {"start": 165.0, "end": 205.0, "text": "Let us ship the storage routes first and iterate.", "source": "hub"},
    ]
    ready_minutes = {
        "version": 1,
        "revision": 1,
        "title": "Architecture Review (Speakers Ready)",
        "when": "2026-11-26 10:00",
        "start": now_iso,
        "end": HUB.ago(hours=2),
        "attendees": ["Ada", "Grace", "Lin"],
        "points": [
            {"text": "Speaker timelines stay local on the hub with no voiceprints", "at": 10.0, "who": "Ada"},
            {"text": "Per-item export limits keep large attachments isolated", "at": 122.0, "who": "Ada"},
        ],
        "decisions": [
            {"text": "Keep speaker separation local and ship storage routes first", "at": 165.0},
        ],
        "actions": [
            {"id": "a1", "who": "Grace", "what": "Verify module-off reads in CI", "by": "Friday", "at": 42.0},
        ],
        "open": [
            {"text": "Whether to export speaker labels by default", "at": 78.0},
        ],
        "debates": [
            {
                "question": "Should the export cap count items or recordings?",
                "status": "settled",
                "at": 122.0,
                "positions": [
                    {
                        "stance": "Per item: one large file should not fail the whole export",
                        "at": 122.0,
                        "span": [122.0, 152.0],
                        "over": {"s4": 30.0},
                        "said_by": "s1",
                        "speaker": "s1",
                        "also": [
                            {"at": 42.0, "span": [42.0, 75.0], "over": {"s2": 33.0}, "said_by": "s2", "speaker": "s2"},
                        ],
                        "who": "Ada",
                    },
                    {
                        "stance": "Per recording: easier to explain to users",
                        "at": 78.0,
                        "span": [78.0, 120.0],
                        "over": {"s3": 42.0},
                        "said_by": "s3",
                        "speaker": "s3",
                    },
                ],
                "outcome": {"text": "Per item for this release", "at": 165.0, "decision": 0},
            },
            {
                "question": "Should speaker labels appear in default exports?",
                "status": "open",
                "at": 78.0,
                "positions": [
                    {
                        "stance": "Include numbered speakers by default",
                        "at": 78.0,
                        "span": [78.0, 120.0],
                        "over": {"s3": 42.0},
                        "speaker": "s3",
                    },
                    {
                        "stance": "Wait until the owner names speakers",
                        "at": 154.0,
                        "span": [154.0, 162.0],
                        "over": {"s5": 8.0},
                        "speaker": "s5",
                        "speaker_unsure": True,
                    },
                ],
            },
        ],
        "language": "en",
        "markdown": "# Architecture Review (Speakers Ready)\n\n## Disagreements\n\n### Should the export cap count items or recordings? (settled)\n- [10:02:02] Per item: one large file should not fail the whole export (Ada, Grace)\n- [10:01:18] Per recording: easier to explain to users (Speaker 3)\n",
    }
    _seed_sample(
        SAMPLE_READY,
        "Architecture Review (Speakers Ready)",
        now_iso,
        420.0,
        {
            "rev": 3,
            "state": "ready",
            "reason": "",
            "quality": "ok",
            "source": "hub",
            "timing": "turn",
            "next": 6,
            "people": [
                {"id": "s1", "name": "Ada"},
                {"id": "s2", "name": "Grace", "me": True},
                {"id": "s3"},
                {"id": "s4", "merged_into": "s1"},
                {"id": "s5", "minor": True},
            ],
            "stats": {
                "s1": [260.0, 3],
                "s2": [210.0, 3],
                "s3": [150.0, 2],
                "s4": [60.0, 1],
                "s5": [11.5, 1],
            },
            "edits": {"name": 2, "merge": 1},
            "candidates": ["Grace", "Ada", "Lin", "Sam"],
            "spans": [
                [10.0, 40.0, "s1"],
                [42.0, 75.0, "s2"],
                [78.0, 120.0, "s3"],
                [122.0, 152.0, "s4"],
                [154.0, 162.0, "s5"],
                [165.0, 205.0, "s1"],
            ],
            "covered": [[0.0, 420.0]],
            "base_segments": ready_segments,
            "trimmed": False,
        },
        {
            "one_line": "Speaker timelines stay local on the hub.",
            "summary": "## Architecture Review\n\nSpeaker timelines stay local on the hub.\n",
            "minutes": ready_minutes,
        },
    )

    # 2. Waiting sample (in-progress speaker separation with done/end).
    _seed_sample(
        SAMPLE_WAITING,
        "Sprint Sync (Speakers Waiting)",
        HUB.ago(hours=2),
        600.0,
        {
            "rev": 1,
            "state": "waiting",
            "reason": "",
            "quality": "",
            "source": "hub",
            "timing": "turn",
            "done": 180.0,
            "end": 600.0,
            "next": 1,
            "people": [],
            "stats": {},
            "edits": {},
            "candidates": ["Grace", "Ada"],
            "spans": [],
            "covered": [],
            "base_segments": [
                {"start": 0.0, "end": 25.0, "text": "Let us review the sprint backlog.", "source": "hub"},
            ],
            "trimmed": False,
        },
        {"one_line": "Separating speakers in progress."},
    )

    # 3. Ready + quality low sample (every labelled segment gets speaker_unsure: true).
    _seed_sample(
        SAMPLE_LOW_QUALITY,
        "Hallway Chat (Low Quality Speakers)",
        HUB.ago(hours=4),
        120.0,
        {
            "rev": 1,
            "state": "ready",
            "reason": "",
            "quality": "low",
            "source": "hub",
            "timing": "turn",
            "next": 3,
            "people": [
                {"id": "s1"},
                {"id": "s2"},
            ],
            "stats": {"s1": [30.0, 1], "s2": [20.0, 1]},
            "edits": {},
            "candidates": ["Grace", "Ada"],
            "spans": [[5.0, 35.0, "s1"], [36.0, 56.0, "s2"]],
            "covered": [[0.0, 60.0]],
            "base_segments": [
                {"start": 5.0, "end": 35.0, "text": "Quick hallway check-in about the schedule.", "source": "hub"},
                {"start": 36.0, "end": 56.0, "text": "Sounds good, we will follow up tomorrow.", "source": "hub"},
            ],
            "trimmed": False,
        },
        {"one_line": "Short meeting with low speaker separation quality."},
    )

    # 4. Failed (too_long) sample.
    _seed_sample(
        SAMPLE_FAILED,
        "All-Day Workshop (Speakers Failed)",
        HUB.ago(hours=6),
        18000.0,
        {
            "rev": 1,
            "state": "failed",
            "reason": "too_long",
            "quality": "",
            "source": "",
            "timing": "turn",
            "next": 1,
            "people": [],
            "stats": {},
            "edits": {},
            "candidates": ["Grace"],
            "spans": [],
            "covered": [],
            "base_segments": [
                {"start": 0.0, "end": 30.0, "text": "Welcome to the workshop.", "source": "hub"},
            ],
            "trimmed": False,
        },
        {"one_line": "Recording exceeded speaker separation duration limit."},
    )

    # 5. Trimmed sample (transcript expired, spans empty, editing names/merges/clear still works).
    _seed_sample(
        SAMPLE_TRIMMED,
        "Quarterly Retro (Trimmed Speakers)",
        HUB.ago(days=30),
        1800.0,
        {
            "rev": 2,
            "state": "ready",
            "reason": "",
            "quality": "ok",
            "source": "hub",
            "timing": "turn",
            "next": 4,
            "people": [
                {"id": "s1", "name": "Ada"},
                {"id": "s2", "name": "Grace", "me": True},
                {"id": "s3"},
            ],
            "stats": {"s1": [640.0, 28], "s2": [520.0, 22], "s3": [310.0, 14]},
            "edits": {"name": 2},
            "candidates": ["Grace", "Ada", "Lin"],
            "spans": [],
            "covered": [],
            "base_segments": None,
            "trimmed": True,
        },
        {
            "one_line": "Transcript expired; minutes and speaker names kept.",
            "minutes": {
                "version": 1,
                "revision": 1,
                "title": "Quarterly Retro (Trimmed Speakers)",
                "when": "2026-10-27 14:00",
                "attendees": ["Ada", "Grace"],
                "points": [{"text": "Retrospective summary kept past transcript retention", "at": 120.0}],
                "decisions": [],
                "actions": [],
                "open": [],
                "language": "en",
                "markdown": "# Quarterly Retro (Trimmed Speakers)\n\n- Retrospective summary kept past transcript retention\n",
            },
        },
    )


def _handle_speakers_edit(h, req):
    if HUB.is_read_only(h):
        h.send_json({"error": "This device can't do this.", "code": "forbidden"}, status=403)
        return
    rid = req.match["id"].lower()
    if rid in HUB.RECORDINGS_DELETED:
        h.send_json({"error": "This recording was deleted.", "code": "recording_deleted"}, status=410)
        return
    if HUB.MEETING_MODE.get("kb_read_only"):
        h.send_json({"error": "Knowledge base is read-only.", "code": "kb_read_only"}, status=403)
        return
    if rid not in SPEAKER_STORE:
        if rid in HUB.RECORDINGS or ("recordings", rid) in HUB.KB_DETAIL:
            h.send_json({"error": "This recording isn't a meeting.", "code": "not_meeting"}, status=409)
            return
        h.send_json({"error": "There is no upload of this recording yet.", "code": "recording_not_found"}, status=404)
        return
    try:
        body = req.json()
    except ValueError:
        h.send_json({"error": "invalid JSON"}, status=400)
        return
    if not isinstance(body, dict):
        h.send_json({"error": "invalid request"}, status=400)
        return
    rev = body.get("rev")
    ops = body.get("ops")
    if type(rev) is not int or rev < 0 or not isinstance(ops, list) or len(ops) == 0:
        h.send_json({"error": "invalid speaker request"}, status=400)
        return

    rec = SPEAKER_STORE[rid]
    all_clear = all(isinstance(o, dict) and o.get("op") == "clear" for o in ops)
    if rec["state"] == "cleared":
        if not all_clear:
            h.send_json({"error": "Speakers were cleared for this meeting.", "code": "speakers_cleared"}, status=409)
            return
        if rev != rec["rev"]:
            h.send_json({"error": "The speaker list changed.", "code": "speakers_stale"}, status=409)
            return
        ds = _sync_detail(rid)
        h.send_json({"speakers": ds, "undo": []})
        return
    if rev != rec["rev"]:
        h.send_json({"error": "The speaker list changed.", "code": "speakers_stale"}, status=409)
        return

    try:
        new_roster, undo, cleared = apply_edit(rec, rec.get("stats") or {}, ops, owner_name=OWNER_DEFAULT)
    except EditError as err:
        h.send_json({"error": err.message, "code": err.code}, status=err.status)
        return

    rec["next"] = new_roster["next"]
    rec["people"] = new_roster["people"]
    rec["edits"] = new_roster["edits"]
    if cleared:
        rec["state"] = "cleared"
        rec["reason"] = ""
        rec["quality"] = ""
        rec["source"] = ""
        rec["people"] = []
        rec["stats"] = {}
        rec["spans"] = []
        rec["covered"] = []
    rec["rev"] += 1
    ds = _sync_detail(rid)
    HUB.broadcast({"type": "meeting_live", "recording_id": rid, "changed": ["speakers"], "revs": {"speakers": rec["rev"]}})
    h.send_json({"speakers": ds, "undo": undo})


def _handle_mock_get(h, _req):
    h.send_json({
        "samples": SAMPLE_IDS,
        "states": {rid: {"rev": r["rev"], "state": r["state"], "edits": r.get("edits", {})} for rid, r in SPEAKER_STORE.items()},
    })


def _handle_mock_seed(h, req):
    """Test helper route: seed a custom speaker state under a given recording UUID for fixture replay."""
    try:
        body = req.json()
    except ValueError:
        h.send_json({"error": "invalid JSON"}, status=400)
        return
    if not isinstance(body, dict) or not isinstance(body.get("id"), str):
        h.send_json({"error": "id is required"}, status=400)
        return
    rid = body["id"].lower()
    rec = {
        "rev": int(body.get("rev", 1)),
        "state": body.get("state", "ready"),
        "reason": body.get("reason", ""),
        "quality": body.get("quality", "ok"),
        "source": body.get("source", "hub"),
        "timing": body.get("timing", "turn"),
        "next": int(body.get("next", 1)),
        "people": copy.deepcopy(body.get("people") or []),
        "stats": copy.deepcopy(body.get("stats") or {}),
        "edits": copy.deepcopy(body.get("edits") or {}),
        "candidates": list(body.get("candidates") or []),
        "spans": copy.deepcopy(body.get("spans") or []),
        "covered": copy.deepcopy(body.get("covered") or []),
        "base_segments": copy.deepcopy(body.get("segments") or []),
        "trimmed": bool(body.get("trimmed", False)),
    }
    _seed_sample(rid, body.get("title", "Seeded Speakers Meeting"), HUB.ago(hours=1), 600.0, rec, {})
    h.send_json({"id": rid, "speakers": HUB.KB_DETAIL[("recordings", rid)]["speakers"]})


def register(d):
    global HUB
    HUB = d.hub
    d.feature("meeting_speakers", when=lambda: d.hub.meeting_transcription_on())
    d.route("POST", r"/api/v1/recordings/(?P<id>[^/]+)/speakers", _handle_speakers_edit)
    d.route("GET", r"/mock/recording_speakers", _handle_mock_get)
    d.route("POST", r"/mock/recording_speakers/seed", _handle_mock_seed)
    d.on_reset(seed_all)
    seed_all()
