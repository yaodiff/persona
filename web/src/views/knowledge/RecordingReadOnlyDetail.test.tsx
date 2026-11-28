// @vitest-environment jsdom
// Read-only recording detail when the recording module is off (#15214, #15530):
// calm neutral paused note for paused="module_off", safe handling of unknown/empty paused values,
// stored content preservation, and ABA / stale async safety for photo lists and image blobs.
import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModuleKBDetailParts } from "../../core/module-host";
import { t } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBDetail, KBEntry, RecordingPhoto } from "../../lib/kb";
import { RecordingReadOnlyDetail } from "./RecordingReadOnlyDetail";

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

const auth = { token: "token", tokenOwner: "owner", headers: { "X-Persona-Manager": "assistant" } };
const scroller = createRef<HTMLDivElement>();

const jsonResp = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });

const blobResp = (text: string) =>
  new Response(new Blob([text], { type: "image/png" }), { headers: { "Content-Type": "image/png" } });

const photo = (n: number, id: string, caption: string): RecordingPhoto => ({
  n,
  id,
  at: n * 5,
  mime: "image/png",
  width: 100,
  height: 80,
  caption,
  cited: false,
  forwarded: false,
  added_at: "2026-11-10T06:00:05Z",
});

const recordingDetail = (id: string, over: Partial<KBDetail> = {}): KBDetail => ({
  kind: "recordings",
  id,
  path: `recordings/${id}`,
  meta: { title: `Recording ${id}`, started_at: "2026-11-10T06:00:00Z", kind: "meeting" },
  segments: [{ start: 0, end: 4, text: `Transcript for ${id}` }],
  ...over,
});

const recordingEntry = (id: string): KBEntry => ({
  kind: "recordings",
  id,
  name: `Recording ${id}`,
  relevance: 0,
});

function renderParts(parts: ModuleKBDetailParts) {
  return (
    <main>
      {parts.actions}
      {parts.lead}
      {parts.sections}
      {parts.tail}
      {parts.dialogs}
    </main>
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

beforeEach(() => {
  setToken("token");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("RecordingReadOnlyDetail", () => {
  const mount = (detail?: KBDetail, current = () => true) => {
    const id = detail?.id ?? "rec-empty";
    act(() => {
      root.render(
        <RecordingReadOnlyDetail
          kind="recordings"
          id={id}
          detail={detail}
          entry={recordingEntry(id)}
          auth={auth}
          current={current}
          manager="assistant"
          reload={async () => true}
          onSaved={() => {}}
          onOpen={() => {}}
          scroller={scroller}
        >
          {renderParts}
        </RecordingReadOnlyDetail>,
      );
    });
  };

  it("renders a calm neutral paused strip for paused='module_off' while keeping stored content readable", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({ photos: [] }))));
    mount(
      recordingDetail("rec-paused", {
        paused: "module_off",
        segments: [{ start: 0, end: 5, text: "Its minutes were never written" }],
      }),
    );
    await flush();

    const strip = host.querySelector<HTMLElement>('[role="note"][data-recording-paused="module_off"]');
    expect(strip).not.toBeNull();
    expect(strip?.textContent).toContain(t("knowledge-kbdetail.recording_paused_module_off"));
    expect(strip?.className).toContain("bg-inset");
    expect(strip?.className).toContain("text-ink-2");
    expect(strip?.className).not.toMatch(/red|orange|caution|danger/i);
    expect(host.textContent).toContain("Its minutes were never written");
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("keeps stored minutes readable alongside the paused strip when paused='module_off'", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({ photos: [] }))));
    mount(
      recordingDetail("rec-paused-with-minutes", {
        paused: "module_off",
        minutes: {
          version: 1,
          revision: 1,
          generated_at: "2026-11-10T07:00:00Z",
          title: "Saved minutes",
          when: "",
          markdown: "## Older minutes\n\nKeep the stored decisions.",
        },
      }),
    );
    await flush();

    expect(host.querySelector('[data-recording-paused="module_off"]')).not.toBeNull();
    expect(host.textContent).toContain("Keep the stored decisions.");
    expect(host.textContent).toContain("Transcript for rec-paused-with-minutes");
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("ignores unknown, empty, absent and non-string paused values without showing module-off copy", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({ photos: [] }))));
    for (const paused of ["future_reason", "", undefined, 42 as unknown as string]) {
      mount(recordingDetail("rec-other", { paused }));
      await flush();
      expect(host.querySelector("[data-recording-paused]")).toBeNull();
      expect(host.textContent).not.toContain(t("knowledge-kbdetail.recording_paused_module_off"));
      expect(host.textContent).toContain("Transcript for rec-other");
    }
  });

  it("prevents ABA stale photo list and blob leakage across recording switches and revokes object URLs", async () => {
    const reqs = new Map<string, Deferred<Response>[]>();
    const pushReq = (key: string) => {
      const d = deferred<Response>();
      const list = reqs.get(key) ?? [];
      list.push(d);
      reqs.set(key, list);
      return d.promise;
    };
    let urlSeq = 0;
    const createObjectURL = vi.fn(() => `blob:photo-${++urlSeq}`);
    const revokeObjectURL = vi.fn();
    URL.createObjectURL = createObjectURL;
    URL.revokeObjectURL = revokeObjectURL;

    const requested: string[] = [];
    vi.stubGlobal("fetch", vi.fn((url: string) => {
      const path = url.replace(/^.*\/api\/v1/, "");
      requested.push(path);
      return pushReq(path);
    }));

    // Mount A and resolve its photos + blob
    mount(recordingDetail("rec-a"));
    reqs.get("/recordings/rec-a/photos")![0]!.resolve(jsonResp({ photos: [photo(1, "pa1", "Alpha whiteboard")] }));
    await flush();
    reqs.get("/recordings/rec-a/photos/1")![0]!.resolve(blobResp("alpha-bytes"));
    await flush();
    expect(host.textContent).toContain("Alpha whiteboard");
    expect(host.querySelector<HTMLImageElement>("img")?.src).toBe("blob:photo-1");

    // Switch A -> B: A's photos must clear synchronously before B resolves, with no bogus B/photos/1 request
    mount(recordingDetail("rec-b"));
    expect(host.textContent).not.toContain("Alpha whiteboard");
    expect(host.querySelector("img")).toBeNull();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:photo-1");
    expect(requested).not.toContain("/recordings/rec-b/photos/1");

    // Switch B -> A (ABA) while B's photo list is still pending
    mount(recordingDetail("rec-a"));
    expect(host.textContent).not.toContain("Alpha whiteboard");
    expect(reqs.get("/recordings/rec-a/photos")).toHaveLength(2);

    // Resolve B late (ignoring abort): must NOT leak into A
    reqs.get("/recordings/rec-b/photos")![0]!.resolve(jsonResp({ photos: [photo(1, "pb1", "Beta leak")] }));
    await flush();
    expect(host.textContent).not.toContain("Beta leak");

    // Resolve the second A photo list + blob, then switch away and resolve a stale blob after cleanup
    reqs.get("/recordings/rec-a/photos")![1]!.resolve(jsonResp({ photos: [photo(2, "pa2", "Alpha fresh")] }));
    await flush();
    expect(host.textContent).toContain("Alpha fresh");
    // Switch to C while A's second blob is still pending
    mount(recordingDetail("rec-c"));
    const callsBeforeStaleBlob = createObjectURL.mock.calls.length;
    reqs.get("/recordings/rec-a/photos/2")![0]!.resolve(blobResp("stale-alpha-2"));
    await flush();
    expect(createObjectURL).toHaveBeenCalledTimes(callsBeforeStaleBlob);
    expect(host.querySelector("img")).toBeNull();
  });

  it("shows stored speakers, neutral avatars and '?' aria-label with zero edit controls when recording module is off", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({ photos: [] }))));
    mount(
      recordingDetail("rec-ro-speakers", {
        paused: "module_off",
        segments: [
          { start: 0, end: 5, text: "First line by Ada", speaker: "s1" },
          { start: 6, end: 10, text: "Second line by Ada", speaker: "s1" },
          { start: 15, end: 20, text: "Uncertain line", speaker: "s2", speaker_unsure: true },
        ],
        speakers: {
          rev: 2,
          state: "ready",
          quality: "ok",
          people: [
            { id: "s1", name: "Ada", seconds: 120, lines: 2 },
            { id: "s2", seconds: 60, lines: 1 },
          ],
          candidates: [],
          spans: [],
          covered: [],
          trimmed: false,
        },
      }),
    );
    await flush();

    expect(host.querySelector('[data-readonly-speakers="ready"]')?.textContent).toContain("Ada");
    expect(host.querySelector('[data-readonly-speakers="ready"]')?.textContent).toContain(
      t("knowledge-kbdetail.recording_speaker_n", { n: 2 }),
    );
    const groups = [...host.querySelectorAll<HTMLElement>("[data-speaker-group]")];
    expect(groups.map((g) => [g.dataset.speakerGroup, g.dataset.speakerUnsure ?? ""])).toEqual([
      ["s1", ""],
      ["s2", "true"],
    ]);
    for (const av of host.querySelectorAll("[data-speaker-avatar]")) {
      expect(av.getAttribute("aria-hidden")).toBe("true");
    }
    const unsure = host.querySelector("[data-speaker-unsure-mark]");
    expect(unsure?.getAttribute("role")).toBe("img");
    expect(unsure?.getAttribute("aria-label")).toBe(t("knowledge-kbdetail.recording_speaker_possibly"));
    expect(host.querySelectorAll("button")).toHaveLength(0);
  });

  it("renders low-quality, waiting and failed speaker states read-only and ignores cleared or malformed blocks", async () => {
    vi.stubGlobal("fetch", vi.fn(() => Promise.resolve(jsonResp({ photos: [] }))));

    mount(
      recordingDetail("rec-ro-low", {
        segments: [{ start: 0, end: 5, text: "Line one", speaker: "s1" }],
        speakers: { rev: 1, state: "ready", quality: "low", people: [{ id: "s1" }] },
      }),
    );
    await flush();
    expect(host.querySelector("[data-speakers-low-quality]")?.textContent).toContain(
      t("knowledge-kbdetail.recording_speakers_low_quality"),
    );
    expect(host.querySelector('[data-speaker-group="s1"]')?.getAttribute("data-speaker-unsure")).toBe("true");
    expect(host.querySelectorAll("button")).toHaveLength(0);

    mount(
      recordingDetail("rec-ro-wait", {
        speakers: { rev: 1, state: "waiting", done: 120, end: 600 },
      }),
    );
    await flush();
    expect(host.querySelector('[data-readonly-speakers="waiting"]')?.textContent).toContain(
      t("knowledge-kbdetail.recording_speakers_waiting_progress", { done: 2, total: 10 }),
    );
    expect(host.querySelectorAll("button")).toHaveLength(0);

    mount(
      recordingDetail("rec-ro-fail", {
        speakers: { rev: 1, state: "failed", reason: "too_long" },
      }),
    );
    await flush();
    expect(host.querySelector('[data-readonly-speakers="failed"]')?.textContent).toContain(
      t("knowledge-kbdetail.recording_speakers_failed_too_long"),
    );
    expect(host.querySelectorAll("button")).toHaveLength(0);

    for (const speakers of [{ state: "cleared" }, { state: "unknown_future" }, "broken"]) {
      mount(recordingDetail("rec-ro-hidden", { speakers }));
      await flush();
      expect(host.querySelector("[data-readonly-speakers]")).toBeNull();
      expect(host.querySelectorAll("button")).toHaveLength(0);
    }
  });
});
