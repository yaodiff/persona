import { describe, expect, it } from "vitest";
import { t } from "../../i18n";
import { recordingView, transcriptLineId, transcriptTarget } from "./recording-view";
import { exportTranscriptText, stepTranscriptMatch, transcriptMatches, transcriptMatchParts, transcriptRows } from "./transcript-review";

describe("transcript review", () => {
  function mixed() {
    return recordingView("recordings", { bookmarks: [{ at: 5, label: "Start" }, { at: 5 }, { at: 7, label: "étude" }] }, [
      { start: 5, end: 6, text: "日本語の案 [a-z]+ e\u0301tude" },
      { start: 5, end: 7, text: "A plan for release" },
      { start: 5, end: 7, text: "Assistant echo", echo: true },
      { start: 7, end: 8, text: "The plan changed" },
    ])!;
  }

  it("keeps citation anchors and gives each same-time match a distinct stable target", () => {
    const rec = mixed();
    const rows = transcriptRows(rec);
    expect(rows.map((row) => row.text)).toEqual(rec.lines.map((line) => line.text));
    expect(new Set(rows.map((row) => row.id)).size).toBe(rows.length);
    expect(rows[0].id).toBe(transcriptTarget(rec.lines, 5));
    expect(rows[2].id).toBe(transcriptLineId(5, true));
    expect(transcriptRows({ lines: [...rec.lines] }).map((row) => row.id)).toEqual(rows.map((row) => row.id));
  });

  it("matches literal multilingual phrases and bookmark labels, once per row", () => {
    const rows = transcriptRows(mixed());
    expect(transcriptMatches(rows, "  [a-z]+ ").map((row) => row.id)).toEqual([rows[0].id]);
    expect(transcriptMatches(rows, "語の").map((row) => row.id)).toEqual([rows[0].id]);
    expect(transcriptMatches(rows, "ÉTUDE").map((row) => row.id)).toEqual([rows[0].id, rows[5].id]);
    expect(transcriptMatches(rows, "plan changed")).toEqual([rows[4]]);
    expect(transcriptMatches(rows, t("recording.bookmark"))).toEqual([rows[2], rows[3], rows[5]]);
    expect(transcriptMatches(rows, " ")).toEqual([]);
    expect(transcriptMatches(rows, "absent")).toEqual([]);
  });

  it("marks repeated literal hits in the source text without splitting normalized characters", () => {
    const text = "İ / e\u0301tude [+] / ÉTUDE";
    const parts = transcriptMatchParts(text, "Étude");
    expect(parts.filter((part) => part.hit).map((part) => part.text)).toEqual(["e\u0301tude", "ÉTUDE"]);
    expect(parts.map((part) => part.text).join("")).toBe(text);
    expect(transcriptMatchParts(text, "i")[0]).toEqual({ text: "İ", hit: true });
    expect(transcriptMatchParts(text, "[+]").filter((part) => part.hit)).toEqual([{ text: "[+]", hit: true }]);
    expect(transcriptMatchParts(text, "unknown")).toEqual([{ text, hit: false }]);
  });

  it("cycles forward and backward, including after the selected match disappeared", () => {
    const matches = transcriptMatches(transcriptRows(mixed()), "PLAN");
    const [first, last] = matches;
    expect(stepTranscriptMatch(matches, null, 1)).toBe(first.id);
    expect(stepTranscriptMatch(matches, first.id, 1)).toBe(last.id);
    expect(stepTranscriptMatch(matches, last.id, 1)).toBe(first.id);
    expect(stepTranscriptMatch(matches, first.id, -1)).toBe(last.id);
    expect(stepTranscriptMatch(matches, "removed", -1)).toBe(last.id);
    expect(stepTranscriptMatch([first], first.id, -1)).toBe(first.id);
    expect(stepTranscriptMatch([], last.id, 1)).toBeNull();
  });

  it("preserves the retained wording, order and timezone clock in an export", () => {
    const rec = recordingView("recordings", {
      kind: "day", tz: "Asia/Tokyo", started_at: "2026-05-07T23:00:00Z", bookmarks: [{ at: 1 }, { at: 2, label: "確認" }],
    }, [{ start: 0, end: 1, text: "Go live\nsecond line" }])!;
    const bookmark = t("recording.bookmark");
    expect(exportTranscriptText(transcriptRows(rec))).toBe(`08:00:00  Go live\nsecond line\n08:00:01  ${bookmark}\n08:00:02  ${bookmark} · 確認`);
    expect(exportTranscriptText([])).toBe("");
  });

  it("removes expired speech before rendering, citation lookup, search or export", () => {
    const rec = recordingView("recordings", { trimmed_at: "2026-05-09T03:00:00Z", bookmarks: [{ at: 1, label: "Meeting" }] }, [
      { start: 0, end: 1, text: "Expired words" }, { start: 2, end: 3, text: "Echo", echo: true },
    ])!;
    expect(rec.lines.map((line) => line.text)).toEqual(["Meeting"]);
    expect(transcriptTarget(rec.lines, 1)).toBeUndefined();
    const stale = [...rec.lines, { seconds: 0, clock: "0:00", text: "Stale speech" }];
    const rows = transcriptRows({ ...rec, lines: stale });
    expect(transcriptMatches(rows, "speech")).toEqual([]);
    expect(exportTranscriptText(rows)).toBe(`0:01  ${t("recording.bookmark")} · Meeting`);
  });
});
