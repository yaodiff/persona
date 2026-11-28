// @vitest-environment jsdom
// An agent's live text (#1309): a Claude session Persona started streams its reply as `delta` like the main chat; the
// reply's item replaces it, a delta that comes after the item (the hub hands its last live text to a page that subscribed
// mid-message) is not shown twice, and a chat that gets no delta has none. Only the newest items touch it: an older page,
// an opened fold or an earlier message landing late leaves the words of the one being written alone, since the node
// sends nothing new while a tool runs.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ItemsEvent, listenSession } from "../../lib/store";
import type { Item, ItemsPage } from "../../lib/types";
import type { Transcript } from "./useTranscript";

type Listener = Parameters<typeof listenSession>[2];
let listener: Listener | undefined;

const prompt: Item = { id: "u5#0", seq: 5, role: "user", kind: "text", text: "Write about the sea, then list the folder.", ts: "2026-11-07T10:00:00Z" } as Item;
const said = (seq: number, text: string): Item => ({ id: `a${seq}#0`, seq, role: "assistant", kind: "text", text, ts: "2026-11-07T10:00:05Z" }) as Item;
const call = (seq: number): Item[] => [
  { id: `t${seq}#0`, seq, role: "assistant", kind: "tool_use", tool_use_id: `toolu_${seq}`, tool_name: "Bash", ts: "2026-11-07T10:00:06Z" } as Item,
  { id: `r${seq + 1}#0`, seq: seq + 1, role: "user", kind: "tool_result", tool_use_id: `toolu_${seq}`, text: "ok", ts: "2026-11-07T10:00:08Z" } as Item,
];
// what the fake hub has besides the newest page: the turn before (an older page) and a fold's steps
const earlier = [{ id: "u1#0", seq: 1, role: "user", kind: "text", text: "Earlier question", ts: "2026-11-07T09:00:00Z" } as Item, said(2, "Earlier answer.")];
const folded = [said(3, "Checked the folder first."), ...call(3)];

let apiOverride: ((path: string) => Promise<ItemsPage> | undefined) | undefined;

vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: vi.fn(async (path: string): Promise<ItemsPage> => {
    const custom = apiOverride?.(path);
    if (custom) return custom;
    if (path.includes("before_seq=")) return { items: earlier, to_seq: 2 } as ItemsPage;
    if (path.includes("through_seq=")) return { items: folded, to_seq: 4 } as ItemsPage;
    return { items: [prompt], to_seq: 5, has_more: true } as ItemsPage;
  }),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  send: vi.fn(),
  listenSession: (_mid: string, _sid: string, l: Listener) => {
    listener = l;
    return () => (listener = undefined);
  },
}));

const { useTranscript } = await import("./useTranscript");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let tr: Transcript;
let assistant = false;
function Probe() {
  tr = useTranscript("macbook", "a1f3c2d4-0000-4000-8000-000000000001", { assistant });
  return null;
}

let root: Root;
const mount = async () => {
  root = createRoot(document.createElement("div"));
  act(() => root.render(<Probe />));
  await act(async () => {}); // the first page
};
beforeEach(() => {
  vi.useFakeTimers();
  assistant = false;
  apiOverride = undefined;
});
afterEach(() => {
  apiOverride = undefined;
  act(() => root.unmount());
  vi.useRealTimers();
});

const delta = async (text: string) => {
  act(() => listener!.delta!(text));
  await act(async () => void vi.advanceTimersByTime(500)); // past the throttle
};
const items = (list: Item[], from: number) => act(() => listener!.items!({ items: list, from_seq: from, to_seq: list.at(-1)!.seq } as ItemsEvent));

describe("an agent's live text", () => {
  beforeEach(mount);

  it("streams by lines until the transcript has the reply, which replaces it", async () => {
    await delta("Sea 1: waves.\n");
    expect(tr.streaming).toBe("Sea 1: waves.\n");
    await delta("Sea 1: waves.\nSea 2: tides.");
    expect(tr.streaming).toBe("Sea 1: waves.\nSea 2: tides.");
    items([said(6, "Sea 1: waves.\nSea 2: tides.")], 5);
    expect(tr.streaming).toBe("");
  });

  it("keeps the final delta visible across the empty end marker until its transcript item lands", async () => {
    await delta("Closing words.");
    await delta("");
    expect(tr.streaming).toBe("Closing words.");
    expect(tr.streamEnded).toBe(true);
    expect(tr.streamEndedThrough).toBe(5);
    items([said(6, "Closing words.")], 5);
    expect(tr.streaming).toBe("");
    expect(tr.streamEndedThrough).toBeUndefined();
    expect(tr.items.at(-1)?.text).toBe("Closing words.");
  });

  it("drops an ended preview when a new exchange opens", async () => {
    await delta("Unpersisted.");
    await delta("");
    items([{ ...prompt, id: "u6", seq: 6 }], 5);
    expect(tr.streaming).toBe("");
  });

  it("does not show again what the reply's item already holds", async () => {
    items([said(6, "Mountain 1: peaks.\nMountain 2: snow.")], 5);
    await delta("Mountain 1: peaks.\n"); // late: the transcript was first
    expect(tr.streaming).toBe("");
    await delta("Mountain 1: peaks.\nMountain 2: snow.");
    expect(tr.streaming).toBe("");
  });

  it("retires landed live words before a new user message or later tool result arrives", async () => {
    await delta("The previous reply.");
    items([said(6, "The previous reply.")], 5);
    expect(tr.streaming).toBe("");
    items([{ ...prompt, id: "u7#0", seq: 7, text: "Check the risks." }, ...call(8)], 6);
    expect(tr.streaming).toBe("");
    await delta("The new reply.");
    expect(tr.streaming).toBe("The new reply.");
  });

  it("retires a replay that was already landed before its delta arrived", async () => {
    items([said(6, "The previous reply.")], 5);
    await delta("The previous reply.");
    items([{ ...prompt, id: "u7#0", seq: 7, text: "Check the risks." }], 6);
    expect(tr.streaming).toBe("");
    await delta("The previous reply.");
    expect(tr.streaming).toBe("The previous reply.");
  });

  it("shows what goes past the words it has, and the next message", async () => {
    items([said(6, "Block one.")], 5);
    await delta("Block one.\n\nBlock two.\n");
    expect(tr.streaming).toBe("Block two.\n");
    await delta("After the tool.\n");
    expect(tr.streaming).toBe("After the tool.\n");
  });

  it("is none without a delta", async () => {
    items([said(6, "A reply that only the transcript brought.")], 5);
    await act(async () => void vi.advanceTimersByTime(1000));
    expect(tr.streaming).toBe("");
  });

  it("stays while an older page loads and a fold opens", async () => {
    await delta("Running the tests now.\n");
    await act(() => tr.loadEarlier());
    expect(tr.items.map((i) => i.seq)).toContain(2);
    expect(tr.streaming).toBe("Running the tests now.\n");
    await act(() => tr.expandFold({ id: "f3#0", seq: 3, role: "assistant", kind: "fold", fold: { through_seq: 4 } } as unknown as Item));
    expect(tr.items.map((i) => i.seq)).toContain(3);
    expect(tr.streaming).toBe("Running the tests now.\n");
  });

  it("stays when the message before it lands late, and goes when its own does", async () => {
    // the words before the tool, the call and its result land only once the next message is on screen
    await delta("Running the tests now.\n");
    items([said(6, "I'll update the names first."), ...call(7)], 5);
    expect(tr.streaming).toBe("Running the tests now.\n");
    items([said(9, "Running the tests now.")], 8);
    expect(tr.streaming).toBe("");
  });

  it("is not taken by the message before when both begin alike", async () => {
    items([said(6, "I'll update the names.\nThen I'll run the tests."), ...call(7)], 5);
    await delta("I'll update the names.\n");
    expect(tr.streaming).toBe("I'll update the names.\n");
  });
});

describe("the assistant's live text", () => {
  beforeEach(async () => {
    assistant = true;
    await mount();
  });

  it("is its reply as it came, which any reply item ends", async () => {
    items([said(6, "An earlier reply that begins alike.")], 5);
    await delta("An earlier");
    expect(tr.streaming).toBe("An earlier");
    items([said(7, "Something else.")], 6);
    expect(tr.streaming).toBe("");
  });

  it("stays while an older page loads", async () => {
    await delta("Writing the reply");
    await act(() => tr.loadEarlier());
    expect(tr.streaming).toBe("Writing the reply");
  });

  it("keeps live progress streaming when a late same-seq wake notice updates an earlier thinking step", async () => {
    items([said(6, "Full completed reply."), { id: "7@r#0", seq: 7, role: "assistant", kind: "thinking", text: "Checking background task." } as Item], 5);
    await delta("Running follow-up step...");
    expect(tr.streaming).toBe("Running follow-up step...");
    // Retroactive in-place update delivers the #-1 wake notice at seq 7 without clearing live streaming.
    items([
      { id: "6@c#-1", seq: 7, role: "system", kind: "notice", wake: true, text: "Background task updated" } as Item,
      { id: "7@r#0", seq: 7, role: "assistant", kind: "thinking", text: "Checking background task." } as Item,
    ], 7);
    expect(tr.streaming).toBe("Running follow-up step...");
    expect(tr.items.map((i) => i.wake ? "wake" : i.kind)).toEqual(["text", "text", "wake", "thinking"]);
  });

  it("keeps a preview with a late echo until the next input really arrives", async () => {
    items([said(6, "The prior turn has an answer.")], 5);
    await delta("The turn is answering.");
    items([{ ...prompt, id: "u7#0", seq: 7 }], 6);
    expect(tr.streaming).toBe("The turn is answering.");
    items([{ ...prompt, id: "u7#1", seq: 8, kind: "image" }], 7);
    expect(tr.streaming).toBe("The turn is answering.");
    await delta("");
    expect(tr.streamEndedThrough).toBe(8);
    items([{ ...prompt, id: "u9", seq: 9, kind: "image" }], 8);
    expect(tr.streaming).toBe("");
    expect(tr.streamEndedThrough).toBeUndefined();
  });

  it("drops an already bound reply before an old end marker can attach to the next input", async () => {
    await delta("The reply to the already echoed input.");
    items([{ ...prompt, id: "u6", seq: 6 }], 5);
    await delta("");
    expect(tr.streaming).toBe("");
    expect(tr.streamEndedThrough).toBeUndefined();
  });
});

describe("itemCoalescer coherency across page commits, replays, reads, rewrites, and live handoff", () => {
  it("preserves buffered live items when a non-replacing reconnect readPage commits inside the 48ms window", async () => {
    await mount();
    // Leading-edge emit at t=0
    items([said(6, "First live reply")], 5);
    expect(tr.items.map((i) => i.id)).toContain("a6#0");

    // Buffered non-critical live item at t=5ms (covered advances to 7)
    vi.advanceTimersByTime(5);
    items([said(7, "Buffered second reply")], 6);
    expect(tr.items.map((i) => i.id)).not.toContain("a7#0");

    // Reconnect triggers readPage(false) returning an older HTTP snapshot (to_seq: 6) inside the 48ms window
    apiOverride = () => Promise.resolve({ items: [prompt, said(6, "First live reply")], to_seq: 6, has_more: true } as ItemsPage);
    vi.advanceTimersByTime(5);
    await act(async () => {
      listener!.reconnected!();
    });
    await act(async () => void vi.advanceTimersByTime(100));

    expect(tr.items.map((i) => i.id)).toContain("a7#0");
    expect(tr.items.find((i) => i.id === "a7#0")?.text).toBe("Buffered second reply");
  });

  it("preserves buffered live items on a non-skipping socket replay (including empty replay) inside the 48ms window", async () => {
    await mount();
    items([said(6, "First")], 5);
    vi.advanceTimersByTime(5);
    items([said(7, "Buffered before replay")], 6);
    expect(tr.items.map((i) => i.id)).not.toContain("a7#0");

    vi.advanceTimersByTime(5);
    act(() =>
      listener!.items!({
        items: [prompt, said(6, "First")],
        from_seq: 5,
        to_seq: 6,
        has_more: true,
      } as ItemsEvent),
    );
    await act(async () => void vi.advanceTimersByTime(100));

    expect(tr.items.map((i) => i.id)).toContain("a7#0");
    expect(tr.items.find((i) => i.id === "a7#0")?.text).toBe("Buffered before replay");

    // Also verify empty non-skipping replay preserves a newly buffered item
    items([said(8, "Second leading")], 7);
    vi.advanceTimersByTime(5);
    items([said(9, "Buffered before empty replay")], 8);
    expect(tr.items.map((i) => i.id)).not.toContain("a9#0");
    vi.advanceTimersByTime(5);
    act(() => listener!.items!({ machine_id: "macbook", session_id: "a1f3c2d4-0000-4000-8000-000000000001", items: [], from_seq: 9, to_seq: 9, has_more: true }));
    await act(async () => void vi.advanceTimersByTime(100));
    expect(tr.items.find((i) => i.id === "a9#0")?.text).toBe("Buffered before empty replay");
  });

  it("does not let a buffered live item overwrite a newer expandFold or gap-fill pull read", async () => {
    await mount();
    items([said(6, "First")], 5);
    vi.advanceTimersByTime(5);
    // Buffered older version of a7#0 inside the 48ms window
    items([said(7, "stale v1")], 6);
    expect(tr.items.map((i) => i.id)).not.toContain("a7#0");

    // expandFold lands a newer version of a7#0 inside the same 48ms window
    apiOverride = (path) => {
      if (path.includes("through_seq=")) {
        return Promise.resolve({ items: [said(7, "fresh v2")], to_seq: 7 } as ItemsPage);
      }
      return undefined;
    };
    vi.advanceTimersByTime(5);
    await act(() =>
      tr.expandFold({ id: "f7#0", seq: 7, role: "assistant", kind: "fold", fold: { through_seq: 7 } } as unknown as Item),
    );
    await act(async () => void vi.advanceTimersByTime(100));
    expect(tr.items.find((i) => i.id === "a7#0")?.text).toBe("fresh v2");

    // Gap-fill pull() flushes buffered older item before applying newer after_seq result
    let resolvePull!: (p: ItemsPage) => void;
    apiOverride = (path) => {
      if (path.includes("after_seq=")) {
        return new Promise<ItemsPage>((r) => { resolvePull = r; });
      }
      return undefined;
    };
    // Trigger gap (from_seq: 10 > covered 7) to schedule pull at +1500ms
    act(() => listener!.items!({ items: [said(10, "After gap")], from_seq: 10, to_seq: 10 } as ItemsEvent));
    // Advance to trigger gap-fill pull (in flight)
    await act(async () => void vi.advanceTimersByTime(1500));
    // While pull is in flight, emit leading edge then buffer stale a8#0 inside 48ms window
    items([said(11, "Leading before pull resolves")], 10);
    vi.advanceTimersByTime(5);
    items([said(8, "stale pull v1")], 7);
    expect(tr.items.map((i) => i.id)).not.toContain("a8#0");

    // Pull resolves inside the 48ms window with fresh a8#0
    await act(async () => {
      resolvePull({ items: [said(8, "fresh pull v2"), said(9, "Gap step 9")], to_seq: 9 } as ItemsPage);
    });
    await act(async () => void vi.advanceTimersByTime(100));
    expect(tr.items.find((i) => i.id === "a8#0")?.text).toBe("fresh pull v2");
  });

  it("cancels buffered items from a superseded history when rewritten() fires", async () => {
    apiOverride = () => Promise.resolve({ items: [prompt], to_seq: 5, has_more: false, history: "h1" } as ItemsPage);
    await mount();

    act(() => listener!.items!({ items: [said(6, "h1 step 1")], from_seq: 5, to_seq: 6, history: "h1" } as ItemsEvent));
    vi.advanceTimersByTime(5);
    act(() => listener!.items!({ items: [said(7, "h1 stale buffered")], from_seq: 6, to_seq: 7, history: "h1" } as ItemsEvent));
    expect(tr.items.map((i) => i.id)).not.toContain("a7#0");

    // Keep the replacement readPage pending while the 48ms window elapses
    let resolveNext!: (p: ItemsPage) => void;
    apiOverride = () => new Promise<ItemsPage>((r) => { resolveNext = r; });

    vi.advanceTimersByTime(5);
    act(() => listener!.items!({ items: [said(1, "h2 start")], from_seq: 0, to_seq: 1, history: "h2" } as ItemsEvent));
    await act(async () => void vi.advanceTimersByTime(100));

    expect(tr.items.map((i) => i.id)).not.toContain("a7#0");

    // Cleanly resolve the pending read
    await act(async () => {
      resolveNext({ items: [said(1, "h2 start")], to_seq: 1, has_more: false, history: "h2" } as ItemsPage);
    });
    expect(tr.items.map((i) => i.id)).toEqual(["a1#0"]);
  });

  it("commits the final assistant reply synchronously when it ends an active streaming preview inside the 48ms window", async () => {
    assistant = true;
    await mount();
    await delta("Final answer.");
    expect(tr.streaming).toBe("Final answer.");

    // Trigger leading-edge emit with a tool item (streaming preview stays active)
    items([{ id: "t6#0", seq: 6, role: "assistant", kind: "tool_use", tool_use_id: "toolu_6", tool_name: "Read" } as Item], 5);
    expect(tr.streaming).toBe("Final answer.");

    // Within 48ms, the final assistant reply arrives and ends the live preview: it must commit immediately with no blank flash
    vi.advanceTimersByTime(5);
    items([said(7, "Final answer.")], 6);

    expect(tr.streaming).toBe("");
    expect(tr.items.find((i) => i.id === "a7#0")?.text).toBe("Final answer.");
  });

  it("commits buffered agent block synchronously on partial unlandedLive trim from onItems and onDelta", async () => {
    await mount();
    // Case 1a: onDelta active, leading-edge tool emit, then Block one arrives inside 48ms window and trims streaming to Block two
    await delta("Block one.\n\nBlock two.\n");
    expect(tr.streaming).toBe("Block one.\n\nBlock two.\n");
    items([{ id: "t6#0", seq: 6, role: "assistant", kind: "tool_use", tool_use_id: "toolu_6", tool_name: "Read" } as Item], 5);
    vi.advanceTimersByTime(5);
    items([said(7, "Block one.")], 6);
    expect(tr.streaming).toBe("Block two.\n");
    expect(tr.items.find((i) => i.id === "a7#0")?.text).toBe("Block one.");

    // Case 1b: Block three buffered in coalescer when onDelta arrives and trims against latest
    await act(async () => void vi.advanceTimersByTime(100));
    items([{ id: "tr8#0", seq: 8, role: "user", kind: "tool_result", tool_use_id: "toolu_6", text: "ok" } as Item], 7);
    vi.advanceTimersByTime(5);
    items([said(9, "Block three.")], 8);
    expect(tr.items.map((i) => i.id)).not.toContain("a9#0");

    vi.advanceTimersByTime(5);
    act(() => listener!.delta!("Block three.\n\nBlock four.\n"));
    expect(tr.items.find((i) => i.id === "a9#0")?.text).toBe("Block three.");
    expect(tr.streaming).toBe("Block four.\n");
  });

  it("renders large item batches via transition without dropping items or delaying subsequent live streaming", async () => {
    await mount();
    const bulk: Item[] = Array.from({ length: 120 }, (_, i) => said(10 + i, `bulk-${i}`));
    items(bulk, 5);
    expect(tr.items.filter((i) => i.id.startsWith("a") && i.text?.startsWith("bulk-"))).toHaveLength(120);

    // Subsequent single live item after the coalesce window still lands immediately.
    await act(async () => void vi.advanceTimersByTime(100));
    items([said(130, "live-after-bulk")], 129);
    expect(tr.items.find((i) => i.id === "a130#0")?.text).toBe("live-after-bulk");
  });
});
