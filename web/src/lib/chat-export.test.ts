// Exporting a conversation (#7375): the Markdown as spec/fixtures/chat-export/cases.json says it is written, and reading
// the whole conversation from the hub: page by page back to its start, messages read whole, a history rewritten while
// reading started over once, the limit, and Cancel.
import { describe, expect, it } from "vitest";
import fixture from "../../../spec/fixtures/chat-export/cases.json";
import { exportFileName, ExportStopped, fenced, readConversation, toolSummary, transcriptMarkdown, type ExportWords } from "./chat-export";
import type { Item, ItemsPage } from "./types";

const w = fixture.words;
const words: ExportWords = {
  ...w,
  from: (name) => w.from.replace("{name}", name),
  cut: (n) => w.cut.replace("{n}", String(n)),
  partial: (from) => w.partial.replace("{from}", from),
};
const utc = (ts: string) => ts.slice(0, 16).replace("T", " ");

describe("spec/fixtures/chat-export/cases.json", () => {
  for (const c of fixture.cases) {
    it(`${c.name}: ${c.why}`, () => {
      expect(transcriptMarkdown(c.items as Item[], fixture.meta, { work: c.work, partial: c.partial, words, time: utc })).toBe(c.markdown);
    });
  }
});

describe("the pieces", () => {
  it("fences text longer than any run of backticks inside it", () => {
    expect(fenced("plain")).toBe("```\nplain\n```");
    expect(fenced("a ``` b\n````", "md")).toBe("`````md\na ``` b\n````\n`````");
    expect(fenced("trailing\n\n\n")).toBe("```\ntrailing\n```");
  });

  it("names a tool call by what it was given, one line, at most 80 characters", () => {
    expect(toolSummary({ tool_name: "Bash", tool_input: { command: "ls\n  -la" } })).toBe("Bash: ls -la");
    expect(toolSummary({ tool_name: "Grep", tool_input: { path: "Sources", pattern: "TODO" } })).toBe("Grep: TODO");
    expect(toolSummary({ tool_name: "Task", tool_input: {} })).toBe("Task");
    expect(toolSummary({ tool_input: { url: "x".repeat(200) } })).toBe(`tool: ${"x".repeat(79)}…`);
  });

  it("names the file after the title and the day, without what file systems refuse", () => {
    const day = new Date(2026, 10, 14, 9);
    expect(exportFileName("Fix: login / logout?", day)).toBe("Fix login logout 2026-11-14.md");
    expect(exportFileName("  ", day)).toBe("conversation 2026-11-14.md");
    expect(exportFileName("..hidden", day)).toBe("hidden 2026-11-14.md");
    expect(exportFileName("长".repeat(100), day)).toBe(`${"长".repeat(80)} 2026-11-14.md`);
  });
});

// A conversation of `n` items on the fake hub, answered newest first in pages of `size`, as GET …/items pages back.
function hub(n: number, size = 3, cut: number[] = []) {
  const items: Item[] = Array.from({ length: n }, (_, i) => ({
    id: `i${i + 1}`,
    seq: i + 1,
    role: i % 2 ? "assistant" : "user",
    kind: "text",
    text: cut.includes(i + 1) ? "cut" : `text ${i + 1}`,
    ...(cut.includes(i + 1) ? { full_size: 100 } : {}),
  }));
  const queries: string[] = [];
  let history = "h1";
  const page = async (q: string): Promise<ItemsPage> => {
    queries.push(q);
    const before = Number(new URLSearchParams(q).get("before_seq") ?? n + 1);
    const older = items.filter((i) => i.seq < before);
    const slice = older.slice(Math.max(0, older.length - size));
    return { items: slice, has_more: older.length > size, history };
  };
  const full = async (id: string) => ({ ...items.find((i) => i.id === id)!, text: `whole ${id}` });
  return { items, queries, page, full, rewrite: (h: string) => void (history = h) };
}

describe("reading the whole conversation", () => {
  it("pages back to the start and gives the items oldest first, each message read whole", async () => {
    const h = hub(8, 3, [2, 7]);
    const seen: number[] = [];
    const got = await readConversation({ work: false, page: h.page, full: h.full, progress: (n) => seen.push(n) });
    expect(got.partial).toBe(false);
    expect(got.items.map((i) => i.seq)).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
    expect(got.items.find((i) => i.seq === 2)).toMatchObject({ text: "whole i2", full_size: undefined });
    expect(got.items.find((i) => i.seq === 7)?.text).toBe("whole i7");
    expect(h.queries).toEqual([
      "turns=10&lazy_images=1&fold_work=1",
      "turns=10&lazy_images=1&fold_work=1&before_seq=6",
      "turns=10&lazy_images=1&fold_work=1&before_seq=3",
    ]);
    expect(seen).toEqual([3, 6, 8]);
  });

  it("reads the pages unfolded with work steps", async () => {
    const h = hub(2);
    await readConversation({ work: true, page: h.page, full: h.full });
    expect(h.queries).toEqual(["turns=10&lazy_images=1"]);
  });

  it("starts over once when the history is rewritten while reading, and says so the second time", async () => {
    const h = hub(9);
    let pages = 0;
    const page = async (q: string) => {
      pages++;
      if (pages === 2) h.rewrite("h2"); // a rewind while the first pass reads its second page
      return h.page(q);
    };
    const got = await readConversation({ work: false, page, full: h.full });
    expect(got.items).toHaveLength(9);
    expect(h.queries.filter((q) => !q.includes("before_seq"))).toHaveLength(2);

    const again = hub(9);
    let n = 0;
    const rewriting = async (q: string) => {
      if (q.includes("before_seq")) again.rewrite(`h${++n + 1}`);
      return again.page(q);
    };
    await expect(readConversation({ work: false, page: rewriting, full: again.full })).rejects.toEqual(new ExportStopped("changed"));
  });

  it("stops at its limit and says the document is partial", async () => {
    const h = hub(30, 5);
    const got = await readConversation({ work: false, page: h.page, full: h.full, maxItems: 12 });
    expect(got.partial).toBe(true);
    expect(got.items.map((i) => i.seq)).toEqual([16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30]);
  });

  it("stops when cancelled", async () => {
    const h = hub(30, 5);
    let stop = false;
    const page = async (q: string) => {
      stop = true;
      return h.page(q);
    };
    await expect(readConversation({ work: false, page, full: h.full, stopped: () => stop })).rejects.toEqual(new ExportStopped("cancelled"));
  });

  it("keeps a message cut when reading it whole fails, and stops on a page that adds nothing", async () => {
    const h = hub(4, 2, [1]);
    const got = await readConversation({ work: false, page: h.page, full: async () => Promise.reject(new Error("offline")) });
    expect(got.items[0]).toMatchObject({ text: "cut", full_size: 100 });
    const stuck = async (): Promise<ItemsPage> => ({ items: [{ id: "a", seq: 1, role: "user", kind: "text", text: "a" }], has_more: true });
    expect((await readConversation({ work: false, page: stuck, full: h.full })).items).toHaveLength(1);
  });
});
