// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Item } from "../../lib/types";
import type { Panel } from "../../lib/panel";

const CWD = "/work/demo";
const holder = vi.hoisted(() => ({
  transcript: null as any,
  features: [] as string[],
}));
vi.mock("../chat/transcript-share", () => ({ useSharedTranscript: () => holder.transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...actual,
    useStore: (selector: (state: any) => unknown) => selector({
      features: holder.features,
      sessions: { m: [{ machine_id: "m", session_id: "s", cwd: CWD }] },
      sessionSummaries: [],
    }),
  };
});

import { closePanel, getPanel, openPanel, usePanel } from "../../lib/panel";
import { ChangesPanel } from "./ChangesPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let sequence = 0;
let host: HTMLDivElement;
let root: Root;

function item(partial: Partial<Item>): Item {
  sequence += 1;
  return { id: `i${sequence}#0`, seq: sequence, role: "assistant", kind: "text", ts: new Date(sequence * 1000).toISOString(), ...partial } as Item;
}
function user(id: string): Item {
  return item({ id: `${id}#0`, role: "user", kind: "text", text: id });
}
function edit(id: string, path: string, before: string, after: string): Item[] {
  return [
    item({ id: `${id}#0`, kind: "tool_use", tool_name: "Edit", tool_use_id: `call-${id}`, tool_input: { file_path: path, old_string: before, new_string: after } }),
    item({ id: `${id}-result#0`, role: "user", kind: "tool_result", tool_use_id: `call-${id}`, text: "ok" }),
  ];
}
function folded(path: string): Item {
  return item({
    kind: "fold",
    fold: { items: 2, through_seq: 999, tools: 2, thoughts: 0, changes: [{ path, add: 2, del: 1, exact: true }] },
  } as Partial<Item>);
}
function transcript(items: Item[], more = false) {
  holder.transcript = {
    items,
    loading: false,
    firstRead: true,
    error: "",
    hasMore: more,
    loadingEarlier: false,
    loadEarlier: vi.fn(async () => {}),
    expandFold: vi.fn(async () => true),
    streaming: "",
    notLive: false,
    reload: vi.fn(),
  };
  return holder.transcript;
}
function opened(path = `${CWD}/a.ts`, call = "call-e1"): Extract<Panel, { kind: "changes" }> {
  return { kind: "changes", mid: "m", sid: "s", at: { path, call } };
}
function Harness() {
  const current = usePanel();
  if (!current || current.kind !== "changes") return null;
  return <StrictMode><ChangesPanel mid="m" sid="s" opened={current} onClose={closePanel} /></StrictMode>;
}
function render(panel = opened()) {
  act(() => {
    openPanel(panel);
    root.render(<Harness />);
  });
}
async function until(ok: () => boolean) {
  for (let n = 0; n < 100 && !ok(); n++) await act(async () => { await new Promise((r) => setTimeout(r, 5)); });
  expect(ok()).toBe(true);
}
async function click(selector: string) {
  const element = host.querySelector<HTMLElement>(selector);
  expect(element, selector).not.toBeNull();
  await act(async () => { element!.click(); });
}

beforeEach(() => {
  sequence = 0;
  holder.features = [];
  localStorage.removeItem("persona.changesTreeWidth");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => closePanel());
});
afterEach(() => {
  act(() => closePanel());
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("ChangesPanel", () => {
  it("resizes the edited-file tree with keys, remembers it on reopen and resets it", async () => {
    transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old", "new")]);
    render();
    const handle = () => host.querySelector<HTMLElement>('[data-changes-panel] [role="separator"]')!;
    expect(handle()).not.toBeNull();
    expect(handle().getAttribute("aria-valuenow")).toBe("192");
    const key = async (value: string) => act(async () => {
      handle().dispatchEvent(new KeyboardEvent("keydown", { key: value, bubbles: true }));
    });
    await key("ArrowRight");
    expect(handle().getAttribute("aria-valuenow")).toBe("208");
    expect(localStorage.getItem("persona.changesTreeWidth")).toBe("208");
    act(() => closePanel());
    render();
    expect(handle().getAttribute("aria-valuenow")).toBe("208");
    expect(document.getElementById(handle().getAttribute("aria-controls")!)).not.toBeNull();
    await key("Home");
    expect(handle().getAttribute("aria-valuenow")).toBe("160");
    await key("Enter");
    expect(handle().getAttribute("aria-valuenow")).toBe("192");
    expect(localStorage.getItem("persona.changesTreeWidth")).toBeNull();
  });

  it("fits the tree to the panel without losing its remembered width", async () => {
    let room = 600;
    let changed = () => {};
    vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(() => ({ width: room }) as DOMRect);
    vi.stubGlobal("ResizeObserver", class {
      constructor(callback: () => void) { changed = callback; }
      observe() {} disconnect() {}
    });
    localStorage.setItem("persona.changesTreeWidth", "320");
    transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old", "new")]);
    render();
    const handle = () => host.querySelector<HTMLElement>('[data-changes-panel] [role="separator"]')!;
    expect(handle()).not.toBeNull();
    expect(handle().getAttribute("aria-valuenow")).toBe("320");
    room = 360;
    act(() => changed());
    expect(handle().getAttribute("aria-valuenow")).toBe("200");
    expect(localStorage.getItem("persona.changesTreeWidth")).toBe("320");
    room = 600;
    act(() => changed());
    expect(handle().getAttribute("aria-valuenow")).toBe("320");
    vi.unstubAllGlobals();
  });

  it("shows this exchange without Git, selects another file and opens it", async () => {
    transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...edit("e2", `${CWD}/b.ts`, "old b", "new b")]);
    render();
    await until(() => host.querySelectorAll("[data-change-file]").length === 2);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("new a");
    expect(host.querySelector("[data-change-diff]")?.textContent).not.toContain("new b");
    expect(host.querySelector("[data-review-with-git]")).toBeNull();

    await click(`[data-change-file="${CWD}/b.ts"]`);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("new b");
    expect(getPanel()).toMatchObject({ kind: "changes", at: { path: `${CWD}/b.ts` } });

    await click("[data-open-file]");
    expect(getPanel()).toEqual({ kind: "file", mid: "m", sid: "s", path: `${CWD}/b.ts`, cwd: CWD });
  });

  it("loads earlier pages for the whole-session scope", async () => {
    const tr = transcript([user("u2"), ...edit("e2", `${CWD}/b.ts`, "old b", "new b")], true);
    tr.loadEarlier.mockImplementation(async () => {
      tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...tr.items];
      tr.hasMore = false;
    });
    render(opened(`${CWD}/b.ts`, "call-e2"));
    await click('[data-change-scope="session"]');
    await until(() => tr.loadEarlier.mock.calls.length > 0);
    render(opened(`${CWD}/b.ts`, "call-e2"));
    await until(() => host.querySelectorAll("[data-change-file]").length === 2);
    expect(host.textContent).toContain("a.ts");
    expect(host.textContent).toContain("b.ts");
  });

  it("keeps the current file when another file changes and offers a new-changes pill", async () => {
    const tr = transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")]);
    const panel = opened();
    render(panel);
    await until(() => Boolean(host.querySelector("[data-change-diff]")));
    const before = host.querySelector("[data-change-diff]")?.textContent;

    tr.items = [...tr.items, ...edit("e2", `${CWD}/b.ts`, "old b", "new b")];
    render(panel);
    await until(() => Boolean(host.querySelector("[data-new-changes]")));
    expect(host.querySelector("[data-change-diff]")?.textContent).toBe(before);
    expect(host.querySelector("[data-new-changes]")?.textContent).toContain("1");

    await click("[data-new-changes]");
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("new b");
    expect(host.querySelector("[data-new-changes]")).toBeNull();
  });

  it("expands folds for the selected file and draws each edit call in order", async () => {
    const tr = transcript([user("u1"), folded(`${CWD}/a.ts`)]);
    tr.expandFold.mockImplementation(async () => {
      tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...edit("e2", `${CWD}/a.ts`, "new a", "newest a")];
      return true;
    });
    render();
    await until(() => tr.expandFold.mock.calls.length > 0);
    expect(tr.expandFold).toHaveBeenCalledWith(expect.objectContaining({ kind: "fold" }));
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 2);
    const calls = [...host.querySelectorAll<HTMLElement>("[data-change-call]")].map((el) => el.dataset.changeCall);
    expect(calls).toEqual(["call-e1", "call-e2"]);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("newest a");
  });

  it("restarts an in-flight fold read when its effect is cleaned up", async () => {
    const tr = transcript([user("u1"), folded(`${CWD}/a.ts`)]);
    const first = vi.fn(() => new Promise<boolean>(() => {}));
    tr.expandFold = first;
    render();
    await until(() => first.mock.calls.length === 1);

    const second = vi.fn(async () => {
      tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
      return true;
    });
    tr.expandFold = second;
    render();
    await until(() => second.mock.calls.length === 1);
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 1);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("new a");
  });

  it("re-expands the selected fold when a subscription replay folds it again", async () => {
    const fold = folded(`${CWD}/a.ts`);
    const tr = transcript([user("u1"), fold]);
    tr.expandFold.mockImplementation(async () => {
      tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
      return true;
    });
    render();
    await until(() => tr.expandFold.mock.calls.length === 1);
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 1);

    tr.items = [user("u1"), fold];
    render();
    await until(() => tr.expandFold.mock.calls.length === 2);
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 1);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("new a");
  });

  it("waits for the requested historical call instead of substituting the latest exchange", async () => {
    const tr = transcript([user("u2"), ...edit("e2", `${CWD}/b.ts`, "old b", "new b")], true);
    render(opened(`${CWD}/a.ts`, "call-e1"));
    await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
    expect(host.querySelector("[data-change-diff]")?.textContent).not.toContain("new b");
    expect(host.querySelector("[data-change-file][aria-current]")).toBeNull();

    tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...tr.items];
    tr.hasMore = false;
    render(opened(`${CWD}/a.ts`, "call-e1"));
    await until(() => host.querySelector("[data-change-diff]")?.textContent.includes("new a") ?? false);
    expect(host.querySelector("[data-change-diff]")?.textContent).not.toContain("new b");
  });

  it("keeps a partial fold retryable until every page has loaded", async () => {
    const fold = folded(`${CWD}/a.ts`);
    const tr = transcript([user("u1"), fold]);
    tr.expandFold.mockImplementation(async () => {
      tr.items = [user("u1"), fold, ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
      return false;
    });
    render();
    await until(() => Boolean(host.querySelector("[data-change-diff] [role=alert]")));
    expect(host.textContent).toContain("重试");

    tr.expandFold.mockImplementationOnce(async () => {
      tr.items = [user("u1"), fold, ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...edit("e2", `${CWD}/a.ts`, "new a", "newest a")];
      return true;
    });
    await click("[data-change-diff] [role=alert] button");
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 2);
    expect(host.querySelector("[data-change-diff]")?.textContent).toContain("newest a");
  });

  it("reports a live other-file edit that arrives while whole-session history is loading", async () => {
    const older = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
    const tr = transcript([user("u2"), ...edit("e2", `${CWD}/b.ts`, "old b", "new b")], true);
    render(opened(`${CWD}/b.ts`, "call-e2"));
    await click('[data-change-scope="session"]');
    await until(() => tr.loadEarlier.mock.calls.length > 0);

    tr.items = [...tr.items, ...edit("e3", `${CWD}/live.ts`, "old live", "new live")];
    render(opened(`${CWD}/b.ts`, "call-e2"));
    expect(host.querySelector("[data-new-changes]")).toBeNull();

    tr.items = [...older, ...tr.items];
    tr.hasMore = false;
    render(opened(`${CWD}/b.ts`, "call-e2"));
    await until(() => Boolean(host.querySelector("[data-new-changes]")));
    expect(host.querySelector("[data-new-changes]")?.textContent).toContain("1");
    await click("[data-new-changes]");
    expect(host.querySelector("[data-change-file][aria-current]")?.getAttribute("data-change-file")).toBe(`${CWD}/live.ts`);
  });

  it("keeps an earlier-page failure retryable while resolving a historical call", async () => {
    const tr = transcript([user("u2"), ...edit("e2", `${CWD}/b.ts`, "old b", "new b")], true);
    tr.error = "history unavailable";
    render(opened(`${CWD}/a.ts`, "call-e1"));
    const alert = host.querySelector("[data-resolving-call] [role=alert]");
    expect(alert?.textContent).toContain("history unavailable");
    await click("[data-resolving-call] [role=alert] button");
    expect(tr.reload).toHaveBeenCalledTimes(1);
  });

  it("keeps a fold failure retryable while resolving a historical call", async () => {
    const tr = transcript([user("u1"), folded(`${CWD}/a.ts`)]);
    tr.expandFold.mockImplementation(async () => { throw new Error("fold unavailable"); });
    render(opened(`${CWD}/a.ts`, "call-e1"));
    await until(() => host.querySelector("[data-resolving-call] [role=alert]")?.textContent?.includes("fold unavailable") ?? false);

    tr.expandFold.mockImplementationOnce(async () => {
      tr.items = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
      return true;
    });
    await click("[data-resolving-call] [role=alert] button");
    render(opened(`${CWD}/a.ts`, "call-e1"));
    await until(() => host.querySelector("[data-change-diff]")?.textContent?.includes("new a") ?? false);
  });

  it("seeds fold results as history but still reports a later live append", async () => {
    const initial = [user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")];
    const fold = folded(`${CWD}/a.ts`);
    fold.fold!.through_seq = 10;
    const tr = transcript([...initial, fold]);
    tr.expandFold.mockImplementation(async () => {
      tr.items = [...initial, ...edit("e2", `${CWD}/b.ts`, "old b", "new b"), ...edit("e3", `${CWD}/a.ts`, "new a", "newest a")];
      return true;
    });
    render();
    await until(() => tr.expandFold.mock.calls.length > 0);
    render();
    await until(() => host.querySelectorAll("[data-change-call]").length === 2);
    expect(host.querySelector("[data-new-changes]")).toBeNull();

    sequence = 10;
    tr.items = [...tr.items, ...edit("live", `${CWD}/live.ts`, "old live", "new live")];
    render();
    await until(() => Boolean(host.querySelector("[data-new-changes]")));
    await click("[data-new-changes]");
    expect(host.querySelector("[data-change-file][aria-current]")?.getAttribute("data-change-file")).toBe(`${CWD}/live.ts`);
  });

  it("scrolls to the requested call again after switching files", async () => {
    vi.spyOn(HTMLElement.prototype, "offsetTop", "get").mockImplementation(function (this: HTMLElement) {
      const element = this;
      if (element.dataset.changeCall === "call-e1") return 40;
      if (element.dataset.changeCall === "call-e2") return 240;
      return 0;
    });
    transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a"), ...edit("e2", `${CWD}/a.ts`, "new a", "newest a"), ...edit("e3", `${CWD}/b.ts`, "old b", "new b")]);
    render(opened(`${CWD}/a.ts`, "call-e1"));
    const body = host.querySelector<HTMLDivElement>("[data-change-diff]")!;
    await until(() => body.scrollTop === 40);

    await click(`[data-change-file="${CWD}/b.ts"]`);
    await until(() => body.scrollTop === 0);
    render(opened(`${CWD}/a.ts`, "call-e1"));
    await until(() => body.scrollTop === 40);
  });

  it("offers branch review at the selected file when Git review is available", async () => {
    holder.features = ["git_review"];
    transcript([user("u1"), ...edit("e1", `${CWD}/a.ts`, "old a", "new a")]);
    render();
    await until(() => Boolean(host.querySelector("[data-review-with-git]")));
    await click("[data-review-with-git]");
    expect(getPanel()).toEqual({ kind: "review", mid: "m", sid: "s", cwd: CWD, scope: "branch", at: { path: `${CWD}/a.ts`, file: true } });
  });

  it("renders unified inset rounded tree rows, neutral folder icons, and thin scrollbar", async () => {
    transcript([user("u1"), ...edit("e1", `${CWD}/src/a.ts`, "old a", "new a"), ...edit("e2", `${CWD}/src/b.ts`, "old b", "new b")]);
    render(opened(`${CWD}/src/a.ts`, "call-e1"));
    await until(() => host.querySelectorAll("[data-change-file]").length === 2);

    const nav = host.querySelector<HTMLElement>("nav")!;
    expect(nav.className.split(" ")).toEqual(expect.arrayContaining(["px-1.5", "py-1", "scrollbar-thin"]));

    const folder = host.querySelector<HTMLButtonElement>("nav button[aria-expanded]")!;
    expect(folder.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "text-tree-row", "hover:bg-hover"]));
    expect(folder.style.paddingLeft).toBe("8px");
    expect(folder.querySelectorAll("svg").length).toBe(2);
    for (const svg of folder.querySelectorAll("svg")) {
      expect(svg.getAttribute("class")).toContain("text-ink-3");
      expect(svg.getAttribute("class")).not.toContain("text-accent");
    }

    const selected = host.querySelector<HTMLButtonElement>(`[data-change-file="${CWD}/src/a.ts"]`)!;
    expect(selected.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "text-tree-row", "bg-hover-2", "font-medium", "text-ink"]));

    const unselected = host.querySelector<HTMLButtonElement>(`[data-change-file="${CWD}/src/b.ts"]`)!;
    expect(unselected.className.split(" ")).toEqual(expect.arrayContaining(["h-7", "rounded-md", "text-tree-row", "hover:bg-hover"]));
    expect(unselected.className.split(" ")).not.toContain("bg-hover-2");
  });
});
