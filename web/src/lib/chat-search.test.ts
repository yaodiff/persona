import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/chat-search/cases.json";
import { fetchFoldsForSearch, foldHolding, lineOfItem, loadEarlierForSearch, nodeItemIds, SEARCH_PAGES, untilStill, type SearchLoadState } from "./chat-search";
import { assistantCardText, groupHits, searchChat, searchFields, SEARCH_LIMIT, snippetParts } from "./chat-search-match";
import { chatLines } from "./turns";
import type { Item } from "./types";

const item = (id: string, seq: number, kind: Item["kind"], extra: Partial<Item> = {}): Item => ({ id, seq, role: "assistant", kind, ...extra });

const chat: Item[] = [
  item("u1#0", 1, "text", { role: "user", text: "find the Parser bug" }),
  item("th1#0", 2, "thinking", { text: "the parser drops the last token" }),
  item("tu1#0", 3, "tool_use", { tool_name: "Grep", tool_use_id: "c1", tool_input: { pattern: "parseLine", path: "src" } }),
  item("tr1#0", 4, "tool_result", { role: "user", tool_use_id: "c1", text: "src/parser.go:12: func parseLine" }),
  item("n1#0", 5, "notice", { text: "Background task finished" }),
  item("a1#0", 6, "text", { text: "Fixed the parser." }),
];

const cases = fixture.cases as unknown as { name: string; item: Item; fields: [string, string][] }[];

describe("searchFields (spec/fixtures/chat-search)", () => {
  it.each(cases.map((c) => [c.name, c] as const))("%s", (_, c) => {
    expect(searchFields(c.item)).toEqual(c.fields);
  });

  it("never matches a hidden tag, and never shows one in a snippet", () => {
    const items = cases.map((c) => c.item);
    for (const word of ["ActionSecret", "persona-action", "ViewErr", "beach.jpg", "InjectedRaw", "RawFollowup", "persona-supervision", "fallback", "persona-view"]) {
      expect(searchChat(items, word).hits, word).toEqual([]);
    }
    for (const hit of searchChat(items, "路线").hits) expect(hit.snippet).not.toMatch(/<\/?persona-|"fallback"/);
  });

  it("finds a quoted context as Quoted, an injected message by its line, a card by its fallback", () => {
    const items = cases.map((c) => c.item);
    expect(searchChat(items, "HiddenCtx").hits.map((h) => h.category)).toEqual(["quoted"]);
    expect(searchChat(items, "示例任务验收").hits.map((h) => h.category)).toEqual(["event"]);
    expect(searchChat(items, "三条路线对比").hits.map((h) => `${h.itemId} ${h.category}`)).toEqual(["a12#0 text"]);
  });
});

describe("assistantCardText", () => {
  it("is the reply itself without a card", () => {
    expect(assistantCardText("plain words")).toBe("plain words");
  });

  it("puts an old persona-card's title in its place", () => {
    expect(assistantCardText('a\n```persona-card\n{"title":"Trip"}\n```\nb')).toBe("a\nTrip\nb");
  });
});

describe("searchFields", () => {
  it("searches a quoted context apart from your words", () => {
    const u = item("u#0", 1, "text", { role: "user", text: '<persona-context title="doc">quoted body</persona-context>\nhello' });
    expect(searchFields(u)).toEqual([["text", "hello"], ["quoted", "doc\nquoted body"]]);
  });

  it("searches a tool call's name and the strings in its input, not its keys", () => {
    const fields = searchFields(chat[2]);
    expect(fields).toContainEqual(["tool_name", "Grep"]);
    expect(fields).toContainEqual(["tool_input", "parseLine\nsrc"]);
    expect(fields.some(([, t]) => t.includes("pattern"))).toBe(false);
  });

  it("leaves images and folds out", () => {
    expect(searchFields(item("i#0", 1, "image"))).toEqual([]);
    expect(searchFields(item("f#0", 1, "fold"))).toEqual([]);
  });

  it("searches a background task's note as a system event", () => {
    const n = item("n#0", 1, "notice", { task: { kind: "shell", status: "completed", title: "npm test", detail: "42 passed" } as Item["task"] });
    expect(searchFields(n)).toContainEqual(["event", "npm test\n42 passed\ncompleted"]);
  });
});

describe("searchChat", () => {
  it("finds words, thinking, tool input and output and events, newest first, ignoring case", () => {
    const r = searchChat(chat, "PARSE");
    expect(r.truncated).toBe(false);
    expect(r.hits.map((h) => `${h.itemId} ${h.category}`)).toEqual([
      "a1#0 text",
      "tr1#0 tool_output",
      "tu1#0 tool_input",
      "th1#0 thinking",
      "u1#0 text",
    ]);
    expect(searchChat(chat, "background").hits.map((h) => h.category)).toEqual(["event"]);
    expect(searchChat(chat, "grep").hits.map((h) => h.category)).toEqual(["tool_name"]);
  });

  it("keeps the live copy when an expanded fold's cached step has the same ID and seq", () => {
    const live = item("r#0", 4, "tool_result", { role: "user", tool_use_id: "c1", text: "NewNeedle" });
    const cached = { ...live, text: "OldNeedle" };
    const items = [live, cached]; // ChatView's exact corpus order: current live items, then cached fold steps.
    expect(searchChat(items, "NewNeedle").hits.map((h) => h.itemId)).toEqual([live.id]);
    expect(searchChat(items, "OldNeedle").hits).toEqual([]);

    const older = item("same#0", 3, "text", { text: "OlderNeedle" });
    const newer = item("same#0", 4, "text", { text: "NewerNeedle" });
    expect(searchChat([older, newer], "NewerNeedle").hits.map((h) => h.itemId)).toEqual([newer.id]);
    expect(searchChat([older, newer], "OlderNeedle").hits).toEqual([]);
  });

  it("marks every match in the snippet", () => {
    const [hit] = searchChat([item("x#0", 1, "text", { text: "ab AB ab" })], "ab").hits;
    expect(hit.ranges).toEqual([[0, 2], [3, 5], [6, 8]]);
    expect(snippetParts(hit)).toEqual([["ab", true], [" ", false], ["AB", true], [" ", false], ["ab", true]]);
  });

  it("cuts long text around the first match, with ellipses and on one line", () => {
    const text = `${"a".repeat(100)}\nNEEDLE\n${"b".repeat(200)}`;
    const [hit] = searchChat([item("x#0", 1, "text", { text })], "needle").hits;
    expect(hit.snippet.startsWith("…")).toBe(true);
    expect(hit.snippet.endsWith("…")).toBe(true);
    expect(hit.snippet).not.toContain("\n");
    const [[a, b]] = hit.ranges;
    expect(hit.snippet.slice(a, b)).toBe("NEEDLE");
  });

  it("does not cut a surrogate pair in half", () => {
    const text = `${"x".repeat(44)}😀needle`;
    const [hit] = searchChat([item("x#0", 1, "text", { text })], "needle").hits;
    expect(hit.snippet).toContain("😀");
  });

  it("stops at the limit and says there were more", () => {
    const many = Array.from({ length: SEARCH_LIMIT + 5 }, (_, i) => item(`m${i}#0`, i, "text", { text: "same word" }));
    const r = searchChat(many, "word");
    expect(r.hits).toHaveLength(SEARCH_LIMIT);
    expect(r.truncated).toBe(true);
  });

  it("finds nothing for an empty query", () => {
    expect(searchChat(chat, "  ").hits).toEqual([]);
  });

  it("groups by category in the order they first come", () => {
    expect(groupHits(searchChat(chat, "parse").hits).map(([c, h]) => [c, h.length])).toEqual([
      ["text", 2],
      ["tool_output", 1],
      ["tool_input", 1],
      ["thinking", 1],
    ]);
  });
});

describe("lineOfItem", () => {
  it("finds the exchange a step, its result and the answer are on", () => {
    const lines = chatLines(chat);
    const seg = lines.find((l) => l.kind === "segment")!.id;
    for (const id of ["u1#0", "th1#0", "tr1#0", "a1#0"]) expect(lineOfItem(lines, chat, id)).toBe(seg);
  });

  it("falls back to the line of the item before it (another block of your message)", () => {
    const items = [item("u2#0", 1, "text", { role: "user", text: "one" }), item("u2#1", 1, "text", { role: "user", text: "two" }), item("a2#0", 2, "text", { text: "ok" })];
    const lines = chatLines(items);
    expect(lineOfItem(lines, items, "u2#1")).toBe(lines[0].id);
    expect(lineOfItem(lines, items, "nope")).toBeUndefined();
  });
});

describe("nodeItemIds", () => {
  it("collects a group's items, nested groups and tool results included", () => {
    const use = chat[2];
    const ids = nodeItemIds([
      { kind: "step", step: { kind: "thinking", id: "th1#0", item: chat[1] } },
      { kind: "group", id: "g", live: false, children: [{ kind: "step", step: { kind: "tool", id: use.id, use, result: chat[3], images: [] } }] },
    ]);
    expect(ids).toEqual(["th1#0", "tu1#0", "tr1#0"]);
  });
});

describe("loadEarlierForSearch", () => {
  /** A transcript with `older` pages left before seq 100; `fail` makes a load bring nothing and set the error. */
  function history(older: number, o: { fail?: (page: number) => boolean; empty?: (page: number) => boolean; error?: string } = {}) {
    let first = 100;
    let left = older;
    let loads = 0;
    const state: SearchLoadState = { items: [item("x", first, "text")], hasMore: older > 0, loading: false, loadingEarlier: false, error: o.error ?? "" };
    const load = async () => {
      const page = loads++;
      if (o.fail?.(page)) return void (state.error = "network down");
      if (o.empty?.(page)) return;
      first -= 1;
      left -= 1;
      state.items = [item(`p${page}`, first, "text"), ...state.items];
      state.hasMore = left > 0;
    };
    return { state, load, get loads() { return loads; } };
  }
  const run = (h: ReturnType<typeof history>, extra: Partial<Parameters<typeof loadEarlierForSearch>[0]> = {}) => {
    const pages: number[] = [];
    return loadEarlierForSearch({ get: () => h.state, load: h.load, frame: async () => {}, stopped: () => false, onPage: (n) => pages.push(n), ...extra }).then((r) => [r, pages] as const);
  };

  it("pages to the start, counting each page in", async () => {
    const h = history(3);
    expect(await run(h)).toEqual(["start", [1, 2, 3]]);
  });

  it("is at the start already when nothing is older", async () => {
    const h = history(0);
    expect(await run(h)).toEqual(["start", []]);
    expect(h.loads).toBe(0);
  });

  it("stops after SEARCH_PAGES pages", async () => {
    const h = history(50);
    const [r, pages] = await run(h);
    expect(r).toBe("page_cap");
    expect(pages).toHaveLength(SEARCH_PAGES);
  });

  it("stops between pages when asked", async () => {
    const h = history(10);
    let n = 0;
    const [r, pages] = await run(h, { stopped: () => n++ >= 2 });
    expect(r).toBe("stopped");
    expect(pages).toEqual([1, 2]);
  });

  it("does not start while the transcript is loading", async () => {
    const h = history(5);
    h.state.loadingEarlier = true;
    expect(await run(h)).toEqual(["busy", []]);
    expect(h.loads).toBe(0);
  });

  it("says a page that brought nothing", async () => {
    const h = history(5, { empty: (p) => p === 1 });
    expect(await run(h)).toEqual(["no_progress", [1]]);
  });

  it("says a failed page, even with the same error as an earlier failure", async () => {
    expect(await run(history(5, { fail: (p) => p === 0 }))).toEqual(["load_failed", []]);
    expect(await run(history(5, { fail: () => true, error: "network down" }))).toEqual(["load_failed", []]);
  });
});

describe("untilStill", () => {
  /** Frames 16 ms apart; `ys` is where the thing is each frame (null: not there yet). */
  function frames(ys: (number | null)[], o: { giveUpMs?: number; cancelAt?: number } = {}) {
    let clock = 0;
    let n = 0;
    return untilStill<number>({
      frame: async () => {
        clock += 16;
        n++;
      },
      now: () => clock,
      cancelled: () => o.cancelAt !== undefined && n >= o.cancelAt,
      giveUpMs: o.giveUpMs ?? 1000,
      locate: () => {
        const y = ys[Math.min(n - 1, ys.length - 1)];
        return y === null ? null : { y, value: n };
      },
    });
  }

  it("waits for the rows above to stop moving", async () => {
    // opening above it pushes it down for a few frames, then it holds for three
    expect(await frames([null, 10, 40, 80, 100, 100, 100, 100])).toEqual({ value: 8 }); // at 100 from frame 5, held for frames 6–8
  });

  it("gives what it found last when it never holds still", async () => {
    let y = 0;
    const r = await untilStill<number>({ frame: async () => {}, now: () => (y += 100), cancelled: () => false, giveUpMs: 500, locate: () => ({ y, value: y }) });
    expect(r).toEqual({ value: 600 }); // the clock read when it gave up (500 ms after it started at 100)
  });

  it("is missing when nothing ever turns up", async () => {
    expect(await frames([null], { giveUpMs: 100 })).toBe("missing");
  });

  it("stops when another go starts", async () => {
    expect(await frames([null, 10, 20, 30], { cancelAt: 3 })).toBe("cancelled");
  });
});

describe("fetchFoldsForSearch (#6281)", () => {
  const fold = (seq: number, through: number): Item => item(`fold:${seq}`, seq, "fold", { fold: { items: through - seq + 1, tools: 1, thoughts: 0, through_seq: through } });
  const steps = (f: Item, text = "x"): Item[] => [item(`s${f.seq}#0`, f.seq, "tool_result", { role: "user", tool_use_id: `c${f.seq}`, text })];

  it("fetches the newest folds first, two at a time, and hands each in", async () => {
    const folds = [fold(1, 3), fold(10, 12), fold(20, 25)];
    let running = 0;
    let most = 0;
    const order: number[] = [];
    const got: string[] = [];
    const r = await fetchFoldsForSearch({
      folds,
      load: async (f) => {
        running++;
        most = Math.max(most, running);
        order.push(f.seq);
        await new Promise((ok) => setTimeout(ok, 1));
        running--;
        return steps(f);
      },
      onFold: (f) => got.push(f.id),
      stopped: () => false,
    });
    expect(order).toEqual([20, 10, 1]);
    expect(most).toBe(2);
    expect(got.sort()).toEqual(["fold:1", "fold:10", "fold:20"]);
    expect(r).toEqual({ failed: 0, capped: false, stopped: false });
  });

  it("stops at the budget and says some were left", async () => {
    const got: string[] = [];
    const r = await fetchFoldsForSearch({ folds: [fold(1, 2), fold(5, 6), fold(9, 9)], load: async (f) => steps(f, "y".repeat(10)), onFold: (f) => got.push(f.id), stopped: () => false, budget: 15, concurrency: 1 });
    expect(got).toEqual(["fold:9", "fold:5"]);
    expect(r.capped).toBe(true);
  });

  it("counts a fold that fails and goes on with the others", async () => {
    const got: string[] = [];
    const r = await fetchFoldsForSearch({ folds: [fold(1, 2), fold(5, 6)], load: async (f) => (f.seq === 5 ? Promise.reject(new Error("503")) : steps(f)), onFold: (f) => got.push(f.id), stopped: () => false });
    expect(got).toEqual(["fold:1"]);
    expect(r.failed).toBe(1);
  });

  it("stops between folds when asked, and hands nothing in after", async () => {
    let stop = false;
    const got: string[] = [];
    const r = await fetchFoldsForSearch({ folds: [fold(1, 2), fold(5, 6)], load: async (f) => { stop = true; return steps(f); }, onFold: (f) => got.push(f.id), stopped: () => stop, concurrency: 1 });
    expect(got).toEqual([]);
    expect(r.stopped).toBe(true);
  });

  it("finds a folded step's hit, and the fold it came from", async () => {
    const f = fold(4, 6);
    const fetched = new Map([[f.id, { fold: f, items: steps(f, "CopperKey 铜钥匙 挂在门后。") }]]);
    const chatItems = [item("u3#0", 3, "text", { role: "user", text: "钥匙在哪" }), f, ...fetched.get(f.id)!.items];
    const [hit] = searchChat(chatItems, "copperkey").hits;
    expect(hit).toMatchObject({ itemId: "s4#0", category: "tool_output" });
    expect(foldHolding(fetched, hit.itemId)).toBe(f);
    expect(foldHolding(fetched, "u3#0")).toBeUndefined();
  });
});
