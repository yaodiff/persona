// @vitest-environment jsdom
// Web recording speakers UI (#17675): transcript grouping, neutral avatars, "?" screen-reader label, live/partial
// hiding, every state row, local-first optimistic naming + reconciliation + failure rollback + Undo, auto-merge &
// unmerge, minor fold, trimmed naming, Delete confirmation, low-quality rules and disagreements attribution.
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModuleKBDetailParts } from "../../core/module-host";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBDetail, KBEntry } from "../../lib/kb";
import { recording } from "./module";
import { RecordingDetail } from "./RecordingDetail";
import { RecordingSpeakersBar } from "./RecordingSpeakersDialog";
import { decodeSpeakers, type DetailSpeakers } from "./speakers";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (err: unknown) => void;
}
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (err: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const jsonResp = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });

const auth = { token: "token", tokenOwner: "owner", headers: { "X-Persona-Manager": "assistant" } };

function makeDetail(over: Partial<KBDetail> = {}): KBDetail {
  return {
    kind: "recordings",
    id: "rec-spk-1",
    path: "recordings/rec-spk-1",
    meta: {
      title: "Sprint review",
      kind: "meeting",
      complete: true,
      started_at: "2026-11-10T14:00:00+08:00",
      duration: 1800,
      device: "phone",
      language: "en",
      minutes_state: "ready",
      minutes_asked: true,
    },
    segments: [
      { start: 10, end: 30, text: "Per item keeps one large file from breaking export.", speaker: "s1" },
      { start: 35, end: 55, text: "We should show a clear warning.", speaker: "s1" },
      { start: 120, end: 145, text: "Per recording is easier to explain.", speaker: "s2" },
      { start: 180, end: 195, text: "Maybe we check later.", speaker: "s2", speaker_unsure: true },
      { start: 210, end: 218, text: "Quick note.", speaker: "s4" },
    ],
    minutes: {
      version: 1,
      revision: 1,
      generated_at: "2026-11-10T06:31:00Z",
      title: "Sprint review",
      when: "2026-11-10 14:00–14:30",
      points: [{ text: "Keep export cap", at: 10 }],
      decisions: [{ text: "Keep per-item cap", at: 35 }],
      actions: [],
      open: [],
      debates: [
        {
          question: "Should the export cap count items or recordings?",
          status: "settled",
          at: 10,
          positions: [
            { stance: "Per item", at: 10, speaker: "s1" },
            { stance: "Per recording", at: 120, speaker: "s2", also: [{ speaker: "s3" }] },
          ],
          outcome: { text: "Per item", at: 35, decision: 0 },
        },
      ],
      markdown: "# Sprint review\n",
    },
    speakers: {
      rev: 3,
      state: "ready",
      reason: "",
      quality: "ok",
      source: "hub",
      people: [
        { id: "s1", name: "Ada", seconds: 320, lines: 12, samples: [10, 35] },
        { id: "s2", seconds: 140, lines: 6, samples: [120] },
        { id: "s3", name: "Bo", seconds: 90, lines: 4 },
        { id: "s4", minor: true, seconds: 8, lines: 1 },
      ],
      candidates: ["Ada", "Grace", "Bo"],
      spans: [
        [10, 55, "s1"],
        [120, 195, "s2"],
      ],
      covered: [[0, 1800]],
      trimmed: false,
    },
    ...over,
  };
}

const entry: KBEntry = { kind: "recordings", id: "rec-spk-1", name: "Sprint review", relevance: 0 };

function renderParts(parts: ModuleKBDetailParts, scrollerRef: React.RefObject<HTMLDivElement | null>) {
  return (
    <div ref={scrollerRef} className="overflow-y-auto">
      {parts.actions}
      {parts.lead}
      {parts.sections}
      {parts.tail}
      {parts.dialogs}
    </div>
  );
}

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) {
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
  }
};

function type(input: HTMLInputElement, value: string) {
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
}

beforeEach(async () => {
  await initI18n("en", recording.locales!);
  setToken("token");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  // Clean up any portaled dialogs left in body.
  for (const el of document.querySelectorAll('[role="dialog"]')) el.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("RecordingDetail speakers", () => {
  const mount = (detail: KBDetail, reload = vi.fn(async () => true)) => {
    const scroller = createRef<HTMLDivElement>();
    act(() => {
      root.render(
        <RecordingDetail
          kind="recordings"
          id={detail.id}
          detail={detail}
          entry={entry}
          auth={auth}
          current={() => true}
          manager="assistant"
          reload={reload}
          onSaved={() => {}}
          onOpen={() => {}}
          scroller={scroller}
        >
          {(parts) => renderParts(parts, scroller)}
        </RecordingDetail>,
      );
    });
    return { scroller, reload };
  };

  it("groups consecutive transcript lines with neutral avatars, '?' aria-label and attributes debates", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({}))));
    mount(makeDetail());
    await flush();

    // Consecutive s1 lines form one group; s2 sure + s2 unsure form two groups; s4 minor is marked unsure.
    const groups = [...host.querySelectorAll<HTMLElement>("[data-speaker-group]")];
    expect(groups.map((g) => [g.dataset.speakerGroup, g.dataset.speakerUnsure ?? ""])).toEqual([
      ["s1", ""],
      ["s2", ""],
      ["s2", "true"],
      ["s4", "true"],
    ]);

    // Every line preserves its anchor id.
    expect(host.querySelector("#recording-line-10")).not.toBeNull();
    expect(host.querySelector("#recording-line-35")).not.toBeNull();
    expect(host.querySelector("#recording-line-120")).not.toBeNull();

    // Avatars are neutral and hidden from screen readers.
    const avatars = [...host.querySelectorAll<HTMLElement>("[data-speaker-avatar]")];
    expect(avatars.length).toBeGreaterThanOrEqual(4);
    for (const a of avatars) {
      expect(a.getAttribute("aria-hidden")).toBe("true");
      expect(a.className).toContain("bg-field");
      expect(a.className).toContain("text-ink-2");
    }

    // "?" unsure mark carries localized "possibly" aria-label.
    const marks = [...host.querySelectorAll<HTMLElement>("[data-speaker-unsure-mark]")];
    expect(marks.length).toBe(2);
    for (const m of marks) {
      expect(m.textContent).toBe("?");
      expect(m.getAttribute("role")).toBe("img");
      expect(m.getAttribute("aria-label")).toBe("possibly");
    }

    // Disagreements show Ada and "Speaker 2, Bo" (from speaker + also).
    const positions = [...host.querySelectorAll("[data-minutes-debate-position]")].map((el) => el.textContent);
    expect(positions[0]).toContain("Ada");
    expect(positions[1]).toContain("Speaker 2, Bo");
  });

  it("hides everything about speakers when absent, cleared, or while transcript is partial (live)", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({}))));

    mount(makeDetail({ speakers: undefined }));
    await flush();
    expect(host.querySelector("[data-speakers-bar]")).toBeNull();
    expect(host.querySelector("[data-transcript-speakers]")).toBeNull();

    mount(makeDetail({ speakers: { rev: 4, state: "cleared" } }));
    await flush();
    expect(host.querySelector("[data-speakers-bar]")).toBeNull();
    expect(host.querySelector("[data-transcript-speakers]")).toBeNull();

    // Partial / still transcribing (live): no speakers shown.
    mount(
      makeDetail({
        meta: {
          title: "Live meeting",
          kind: "meeting",
          complete: false,
          started_at: "2026-11-10T14:00:00+08:00",
          duration: 60,
          device: "phone",
        },
      }),
    );
    await flush();
    expect(host.querySelector("[data-speakers-bar]")).toBeNull();
    expect(host.querySelector("[data-transcript-speakers]")).toBeNull();
  });

  it("updates transcript and debates immediately on optimistic rename, supports Undo, and rolls back on failure", async () => {
    const firstWrite = deferred<Response>();
    const secondWrite = deferred<Response>();
    const thirdWrite = deferred<Response>();
    let call = 0;
    const bodies: unknown[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/recordings/rec-spk-1/speakers")) {
          bodies.push(JSON.parse(String(init?.body ?? "{}")));
          call++;
          if (call === 1) return firstWrite.promise;
          if (call === 2) return secondWrite.promise;
          return thirdWrite.promise;
        }
        return Promise.resolve(jsonResp({}));
      }),
    );

    const detail = makeDetail();
    mount(detail);
    await flush();

    // Open Naming dialog.
    act(() => host.querySelector<HTMLButtonElement>("[data-speakers-open]")!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dialog).not.toBeNull();

    // Minor speaker s4 is folded under "Rarely spoke (1)".
    expect(dialog.querySelector('[data-speaker-row="s4"]')).toBeNull();
    act(() => dialog.querySelector<HTMLButtonElement>("[data-speakers-minor-toggle]")!.click());
    expect(dialog.querySelector('[data-speaker-row="s4"]')).not.toBeNull();

    // Click candidate chip "Grace" on s2: BEFORE fetch resolves, transcript & debates immediately show "Grace"!
    const s2Row = dialog.querySelector<HTMLElement>('[data-speaker-row="s2"]')!;
    act(() => s2Row.querySelector<HTMLButtonElement>('[data-speaker-candidate="Grace"]')!.click());

    expect(host.querySelector('[data-speaker-group="s2"]')?.textContent).toContain("Grace");
    expect(host.querySelectorAll("[data-minutes-debate-position]")[1]?.textContent).toContain("Grace, Bo");

    // Hub answers with updated rev=4 and server undo op.
    const afterRename = structuredClone(detail.speakers as Record<string, unknown>);
    afterRename.rev = 4;
    (afterRename.people as Array<Record<string, unknown>>)[1]!.name = "Grace";
    firstWrite.resolve(jsonResp({ speakers: afterRename, undo: [{ op: "name", id: "s2", name: "" }] }));
    await flush();

    expect(bodies[0]).toEqual({ rev: 3, ops: [{ op: "name", id: "s2", name: "Grace" }] });
    // Undo bar appears; clicking Undo immediately reverts to "Speaker 2".
    const undoBtn = dialog.querySelector<HTMLButtonElement>("[data-speakers-undo-bar] button")!;
    expect(undoBtn).not.toBeNull();
    act(() => undoBtn.click());
    expect(host.querySelector('[data-speaker-group="s2"]')?.textContent).toContain("Speaker 2");

    const afterUndo = structuredClone(detail.speakers as Record<string, unknown>);
    afterUndo.rev = 5;
    secondWrite.resolve(jsonResp({ speakers: afterUndo, undo: [{ op: "name", id: "s2", name: "Grace" }] }));
    await flush();
    expect(bodies[1]).toEqual({ rev: 4, ops: [{ op: "name", id: "s2", name: "" }] });

    // Now test failure rollback: rename s2 to "Lin", server responds 500 -> reverts to "Speaker 2" and shows alert!
    const s2Input = dialog.querySelector<HTMLInputElement>('[data-speaker-input="s2"]')!;
    act(() => type(s2Input, "Lin"));
    act(() => s2Input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(host.querySelector('[data-speaker-group="s2"]')?.textContent).toContain("Lin");

    thirdWrite.resolve(jsonResp({ error: "disk full" }, 500));
    await flush();
    expect(host.querySelector('[data-speaker-group="s2"]')?.textContent).toContain("Speaker 2");
    expect(dialog.querySelector('[data-speakers-error][role="alert"]')).not.toBeNull();
  });

  it("auto-merges when given an existing name, unmerges back unnamed, and clears on single confirmation", async () => {
    const detail = makeDetail();
    const sent: unknown[] = [];

    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (url.includes("/recordings/rec-spk-1/speakers")) {
          const body = JSON.parse(String(init?.body ?? "{}")) as { rev: number; ops: Array<Record<string, unknown>> };
          sent.push(body);
          const op = body.ops[0]!;
          if (op.op === "name" && op.id === "s2" && op.name === "Ada") {
            return Promise.resolve(
              jsonResp({
                speakers: {
                  ...(detail.speakers as Record<string, unknown>),
                  rev: 4,
                  people: [
                    { id: "s1", name: "Ada", seconds: 460, lines: 18, samples: [10, 35, 120] },
                    { id: "s2", merged_into: "s1" },
                    { id: "s3", name: "Bo", seconds: 90, lines: 4 },
                    { id: "s4", minor: true, seconds: 8, lines: 1 },
                  ],
                },
                undo: [{ op: "unmerge", id: "s2" }],
              }),
            );
          }
          if (op.op === "unmerge" && op.id === "s2") {
            return Promise.resolve(
              jsonResp({
                speakers: { ...(detail.speakers as Record<string, unknown>), rev: 5 },
                undo: [{ op: "merge", from: "s2", into: "s1" }],
              }),
            );
          }
          if (op.op === "clear") {
            return Promise.resolve(
              jsonResp({
                speakers: { rev: 6, state: "cleared", people: [], spans: [], covered: [], trimmed: false },
                undo: [],
              }),
            );
          }
        }
        return Promise.resolve(jsonResp({}));
      }),
    );

    mount(detail);
    await flush();

    act(() => host.querySelector<HTMLButtonElement>("[data-speakers-open]")!.click());
    await flush();
    const dialog = document.querySelector<HTMLElement>('[role="dialog"]')!;

    // Tap "Ada" candidate on s2 -> auto-merges s2 into s1.
    act(() => dialog.querySelector<HTMLButtonElement>('[data-speaker-row="s2"] [data-speaker-candidate="Ada"]')!.click());
    await flush();
    expect(dialog.querySelector('[data-merged-speaker="s2"]')).not.toBeNull();
    expect(dialog.querySelector('[data-speaker-row="s2"]')).toBeNull();

    // Unmerge s2 -> comes back unnamed as Speaker 2.
    act(() => dialog.querySelector<HTMLButtonElement>('[data-merged-speaker="s2"] button')!.click());
    await flush();
    expect(dialog.querySelector('[data-merged-speaker="s2"]')).toBeNull();
    expect(dialog.querySelector('[data-speaker-row="s2"]')?.textContent).toContain("Speaker 2");

    // Open menu -> Delete this meeting's speakers -> confirm once -> page looks like no speakers.
    act(() => dialog.querySelector<HTMLButtonElement>(`button[aria-label="${t("recording.speakers_menu")}"]`)!.click());
    await flush();
    const deleteItem = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')].find((el) =>
      el.textContent?.includes(t("recording.speakers_delete")),
    )!;
    act(() => deleteItem.click());
    await flush();

    const confirmDialog = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(confirmDialog.textContent).toContain(t("recording.speakers_delete_title"));
    const confirmBtn = [...confirmDialog.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent?.trim() === t("recording.speakers_delete_short"),
    )!;
    act(() => confirmBtn.click());
    await flush();

    expect(host.querySelector("[data-speakers-bar]")).toBeNull();
    expect(host.querySelector("[data-transcript-speakers]")).toBeNull();
    expect(sent.map((b) => (b as { ops: Array<{ op: string }> }).ops[0]?.op)).toEqual(["name", "unmerge", "clear"]);
  });

  it("handles low quality (all '?', notice line, no speaker attribution) and trimmed naming", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({}))));

    const lowDetail = makeDetail({
      speakers: {
        ...(makeDetail().speakers as Record<string, unknown>),
        quality: "low",
      },
    });
    mount(lowDetail);
    await flush();

    expect(host.querySelector("[data-speakers-low-quality]")?.textContent).toContain(
      t("recording.speakers_low_quality"),
    );
    const groups = [...host.querySelectorAll<HTMLElement>("[data-speaker-group]")];
    expect(groups.length).toBeGreaterThan(0);
    for (const g of groups) {
      expect(g.dataset.speakerUnsure).toBe("true");
      expect(g.querySelector("[data-speaker-unsure-mark]")?.getAttribute("aria-label")).toBe("possibly");
    }
    // No debate position carries speaker attribution on low quality.
    const posTexts = [...host.querySelectorAll("[data-minutes-debate-position]")].map((el) => el.textContent ?? "");
    expect(posTexts[0]).not.toContain("Ada");
    expect(posTexts[1]).not.toContain("Speaker 2");
    expect(posTexts[1]).not.toContain("Bo");

    // Trimmed recording: naming dialog still opens with names, stats and attributed positions (no samples).
    const trimmedDetail = makeDetail({
      meta: { ...(makeDetail().meta as Record<string, unknown>), trimmed_at: "2026-11-25T00:00:00Z" },
      segments: [],
      speakers: {
        ...(makeDetail().speakers as Record<string, unknown>),
        spans: [],
        trimmed: true,
      },
    });
    mount(trimmedDetail);
    await flush();
    act(() => host.querySelector<HTMLButtonElement>("[data-speakers-open]")!.click());
    await flush();
    const dlg = document.querySelector<HTMLElement>('[role="dialog"]')!;
    expect(dlg.querySelector('[data-speaker-row="s1"]')?.textContent).toContain("Ada");
    expect(dlg.querySelector('[data-speaker-row="s1"]')?.textContent).toContain("spoke 5 min · 12 lines");
    expect(dlg.querySelector("[data-speaker-samples]")).toBeNull();
    expect(dlg.querySelector('[data-speaker-row="s1"] [data-speaker-positions]')?.textContent).toContain("Per item");
  });

  it("renders every state row in RecordingSpeakersBar with no 'try again' button", () => {
    const renderBar = (raw: unknown) => {
      const spk: DetailSpeakers | null = decodeSpeakers(raw);
      act(() => {
        root.render(<RecordingSpeakersBar speakers={spk} onOpenNaming={() => {}} onDelete={() => {}} />);
      });
    };

    renderBar({ state: "waiting" });
    expect(host.textContent).toContain("Telling speakers apart…");

    renderBar({ state: "waiting", done: 185 });
    expect(host.textContent).toContain("Telling speakers apart (3 min)…");

    renderBar({ state: "waiting", done: 185, end: 1800 });
    expect(host.textContent).toContain("Telling speakers apart (3 of 30 min)…");

    const reasons = [
      ["too_long", t("recording.speakers_failed_too_long")],
      ["timeout", t("recording.speakers_failed_timeout")],
      ["engine_unavailable", t("recording.speakers_failed_engine_unavailable")],
      ["no_audio", t("recording.speakers_failed_no_audio")],
      ["gave_up", t("recording.speakers_failed_other")],
      ["audio_gone", t("recording.speakers_failed_other")],
      ["error", t("recording.speakers_failed_other")],
    ] as const;

    for (const [reason, expectedText] of reasons) {
      renderBar({ state: "failed", reason });
      expect(host.textContent, reason).toContain(expectedText);
      const buttons = [...host.querySelectorAll("button")].map((b) => b.textContent?.trim());
      expect(buttons, reason).toEqual([t("recording.speakers_delete_short")]);
    }

    renderBar({ state: "failed", reason: "unknown_future_reason" });
    expect(host.innerHTML).toBe("");
  });
});
