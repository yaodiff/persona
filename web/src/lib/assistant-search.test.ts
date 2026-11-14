import { describe, expect, it, vi } from "vitest";

const calls = vi.hoisted(() => ({ paths: [] as string[], missing: true }));
vi.mock("./api", async (original) => {
  const real = await original<typeof import("./api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      calls.paths.push(path);
      if (calls.missing) throw new real.ApiError(404, "not found", {});
      return { results: [], partial: false };
    }),
  };
});
import { leadSnippet, markParts, mergeSearch, searchAssistantChats, SNIPPET_LEAD, type AssistantSearchAnswer } from "./assistant-search";

describe("markParts", () => {
  it("cuts a snippet at its matches", () => {
    expect(markParts("我想去青石巷 BlueLane 看看", [[7, 15]])).toEqual([["我想去青石巷 ", false], ["BlueLane", true], [" 看看", false]]);
    expect(markParts("ab ab", [[3, 5], [0, 2]])).toEqual([["ab", true], [" ", false], ["ab", true]]);
  });

  it("keeps ranges inside the snippet and in order", () => {
    expect(markParts("short", [[2, 99], [1, 3]])).toEqual([["s", false], ["ho", true], ["rt", true]]);
    expect(markParts("x", [])).toEqual([["x", false]]);
  });

  it("starts the line just before the first match for the row's width (#7045)", () => {
    // the manager's snippet can start 40 characters before the match: a phone row keeps 8, a wide one 20
    const lead = "早".repeat(30) + "😀 前面的话 ";
    const snippet = "…" + lead + "BlueLane 看看";
    const at = ("…" + lead).length;
    const phone = markParts(snippet, [[at, at + 8]], "phone");
    expect(phone.map((p) => p[0]).join("")).toBe("…" + Array.from(lead).slice(-SNIPPET_LEAD.phone).join("") + "BlueLane 看看");
    expect(phone.filter((p) => p[1]).map((p) => p[0])).toEqual(["BlueLane"]);
    const wide = markParts(snippet, [[at, at + 8]], "wide");
    expect(Array.from(wide[0][0]).length).toBe(1 + SNIPPET_LEAD.wide);
    expect(wide.filter((p) => p[1]).map((p) => p[0])).toEqual(["BlueLane"]);
    // the cut never splits the emoji (two UTF-16 units) and moves every range
    const emoji = leadSnippet("一二三四五六七八九十😀ab", [[12, 14]], 1);
    expect(emoji).toEqual({ snippet: "…😀ab", ranges: [[3, 5]] });
    // a match near the start, or no match: as it was; a range past the end cuts at the end, never past it
    expect(markParts("我想去青石巷 BlueLane 看看", [[7, 15]], "phone")).toEqual([["我想去青石巷 ", false], ["BlueLane", true], [" 看看", false]]);
    expect(markParts("x", [], "phone")).toEqual([["x", false]]);
    expect(leadSnippet("short", [[99, 120]], 2).snippet).toBe("…rt");
  });
});

describe("mergeSearch", () => {
  const chat = (session_id: string) => ({ session_id });
  const answer: AssistantSearchAnswer = {
    results: [
      { session_id: "b", machine_id: "m", title: "B", hits: [{ item_id: "i1", seq: 1, role: "user", snippet: "s", ranges: [[0, 1]] }] },
      { session_id: "z", machine_id: "m", title: "Z", archived: true, hits: [] },
    ],
    partial: false,
  };
  it("is the local matches until the manager answers", () => {
    expect(mergeSearch([chat("a")], undefined, [chat("a")], () => chat("?")).map((x) => x.chat.session_id)).toEqual(["a"]);
  });

  it("puts the manager's results first, then the local matches it did not return", () => {
    const made: string[] = [];
    const merged = mergeSearch([chat("a"), chat("b")], answer, [chat("a"), chat("b")], (r) => (made.push(r.session_id), chat(r.session_id)));
    expect(merged.map((x) => [x.chat.session_id, x.hit?.item_id])).toEqual([["b", "i1"], ["z", undefined], ["a", undefined]]);
    expect(made).toEqual(["z"]); // a chat the page does not list is made from the result
  });
});

describe("searchAssistantChats", () => {
  it("remembers a manager without the route and asks it no more", async () => {
    expect(await searchAssistantChats("trip")).toBeNull();
    calls.missing = false;
    expect(await searchAssistantChats("trip")).toBeNull();
    expect(calls.paths).toEqual(["/assistant/search?q=trip&limit=30"]);
  });
});
