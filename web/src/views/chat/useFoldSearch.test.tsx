// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { searchChat } from "../../lib/chat-search-match";
import type { Item, ItemsPage } from "../../lib/types";
import { useFoldSearch, type FoldSearch } from "./useFoldSearch";

interface Pending { path: string; resolve: (page: ItemsPage) => void; reject: (e: Error) => void }
const calls: Pending[] = [];
const requested: string[] = [];
vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: (path: string) => { requested.push(path); return new Promise((resolve, reject) => calls.push({ path, resolve, reject })); },
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const item = (id: string, seq: number, text: string): Item => ({ id, seq, role: "assistant", kind: "tool_result", text });
let root: Root;
let result: FoldSearch;
function Probe({ seq, id = "fold:t2", on = true }: { seq: number; id?: string; on?: boolean }) {
  const fold: Item = { id, seq: 1, role: "assistant", kind: "fold", fold: { through_seq: seq, through_id: id, items: 1, tools: 1, thoughts: 0 } };
  result = useFoldSearch("m", "s", "history", [fold], on);
  return null;
}
const render = (seq: number, id?: string, on = true) => act(() => root.render(<Probe seq={seq} id={id} on={on} />));
const flush = async () => { for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); }); };
const answer = async (index: number, items: Item[]) => act(async () => calls.splice(index, 1)[0].resolve({ items }));
const reject = async () => act(async () => calls.shift()!.reject(new Error("offline")));

beforeEach(() => {
  calls.length = 0;
  requested.length = 0;
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
});

describe("folded search ranges", () => {
  it("refetches when through_seq or through_id grows and does not double-charge the old items", async () => {
    await render(4);
    await answer(0, [item("old", 3, "OldNeedle")]);
    await render(6);
    await answer(0, [item("old", 3, "OldNeedle"), item("new", 5, "NewNeedle")]);
    expect(requested).toHaveLength(2);
    expect(requested[1]).toContain("through_seq=6");
    expect(searchChat(result.items, "NewNeedle").hits).toHaveLength(1);
    expect(result.partial).toBe(false);

    await render(6);
    await flush();
    expect(requested).toHaveLength(2); // unchanged range: no new read

    await render(6, "fold:t2-new");
    await answer(0, [item("new", 5, "NewNeedle")]);
    expect(requested).toHaveLength(3);
    expect(result.partial).toBe(false);
  });

  it("clears a failed fold's partial warning after a fully successful retry", async () => {
    await render(4);
    await reject();
    await flush();
    expect(result.partial).toBe(true);

    await render(4, undefined, false);
    await render(4);
    await answer(0, [item("retry", 5, "RetryNeedle")]);
    expect(searchChat(result.items, "RetryNeedle").hits).toHaveLength(1);
    expect(result.partial).toBe(false);
  });
});
