// @vitest-environment jsdom
// A session's Side sessions panel (#7944): the Team panel's strip over its side sessions, flat, needs you first and the
// stopped ones last, each pill named by its title and state; under it the shown side's own conversation, landed at the
// message a link asked for; "No side sessions" once none is left.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} })) as unknown as typeof window.matchMedia;
});
const shown = vi.hoisted(() => ({ sid: "", embedded: false, landAt: undefined as string | undefined }));
vi.mock("./ChatView", () => ({
  ChatView: ({ sid, embedded, landAt }: { sid: string; embedded?: boolean; landAt?: string }) => {
    Object.assign(shown, { sid, embedded: Boolean(embedded), landAt });
    return <div data-side-chat={sid} />;
  },
}));
import { applyCompleteSessions, reset } from "../../lib/store";
import { getPanel, openPanel } from "../../lib/panel";
import { setSideFold } from "../../lib/side-fold";
import type { Session } from "../../lib/types";
import { sideHint, SidesPanel } from "./TeamPanel";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const row = (id: string, extra: Partial<Session> = {}): Session => ({ machine_id: "mac", session_id: id, title: id, cwd: "/code", status: "idle", state: "idle", ...extra }) as Session;
const parent = (sides: number) => row("review", { title: "Review PR 42", children: sides ? { total: sides, sides } : undefined });
const side = (id: string, extra: Partial<Session> = {}) => row(id, { folded_under: "review", folded_as: "side", ...extra });

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  Object.assign(shown, { sid: "", embedded: false, landAt: undefined });
  setSideFold(true);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  openPanel(null);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  setSideFold(false);
});

const pills = () => [...host.querySelectorAll<HTMLButtonElement>("[role=tab]")];

describe("SidesPanel", () => {
  it("lists the sides flat in the panel's order, opens on the one that needs you, and shows its own conversation", () => {
    act(() => applyCompleteSessions([
      parent(4),
      side("chat", { title: "Side chat", updated_at: "2026-11-15T10:09:00Z" }),
      side("old", { title: "Why is CI slow", state: "offline", status: "offline", updated_at: "2026-11-15T10:30:00Z" }),
      side("btw", { title: "btw: can it merge", state: "needs_you", status: "blocked", updated_at: "2026-11-15T10:04:00Z" }),
      // a side of a side folds under the same parent, flat; a Codex child of a side is not a pill
      side("deep", { title: "Deeper", state: "working", status: "working", parent_agent_id: "chat" }),
      row("kid", { folded_under: "review", folded_side: "btw", is_subagent: true, parent_session_id: "btw" }),
    ]));
    act(() => root.render(<SidesPanel mid="mac" sid="review" onClose={() => {}} />));
    expect(pills().map((p) => p.textContent)).toEqual(["btw: can it merge", "Deeper", "Side chat", "Why is CI slow"]);
    // the state is said in each pill's hover text and spoken name: no engine or model name
    expect(pills().map((p) => p.getAttribute("aria-label"))).toEqual(["btw: can it merge · 需要你", "Deeper · 工作中", "Side chat · 已看", "Why is CI slow · 未运行"]);
    expect(pills()[0]!.getAttribute("aria-selected")).toBe("true");
    expect(host.querySelector("[role=tablist]")?.getAttribute("aria-label")).toBe("分支会话");
    expect(shown).toEqual({ sid: "btw", embedded: true, landAt: undefined });
    expect(host.querySelector("header")?.textContent).toContain("btw: can it merge");
    act(() => pills()[2]!.click());
    expect(getPanel()).toEqual({ kind: "sides", mid: "mac", sid: "review", side: "chat" });
  });

  it("shows the side a link asked for, landed at its message, and says when none is left", () => {
    act(() => applyCompleteSessions([parent(2), side("a", { state: "needs_you", status: "blocked" }), side("b")]));
    act(() => root.render(<SidesPanel mid="mac" sid="review" side="b" at="item-7" onClose={() => {}} />));
    expect(shown).toEqual({ sid: "b", embedded: true, landAt: "item-7" });
    act(() => {
      reset();
      applyCompleteSessions([parent(0)], [], { loaded: true });
    });
    act(() => root.render(<SidesPanel mid="mac" sid="review" onClose={() => {}} />));
    expect(pills()).toHaveLength(0);
    expect(host.textContent).toContain("暂无分支会话");
  });

  it("names a side's state, not its engine", () => {
    expect(sideHint(side("r", { agent: "codex", title: "Review", state: "unread" }), false)).toBe("Review · 完成未看");
  });
});
