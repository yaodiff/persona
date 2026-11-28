// @vitest-environment jsdom
// Full subagent detail panel (#14323, revising #5030):
// header back action, progressive provenance chip, Stop gated on interrupt capability, ⋯ menu ("Show in chat" only when
// parent has launching call; "Open as page" -> standalone=1), and embedded ChatView read-only vs interactive composer.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia ??= () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const chatProps = vi.hoisted(() => ({ calls: [] as { mid: string; sid: string; embedded?: boolean }[] }));
vi.mock("./ChatView", () => ({
  ChatView: (props: { mid: string; sid: string; embedded?: boolean }) => {
    chatProps.calls.push({ mid: props.mid, sid: props.sid, embedded: props.embedded });
    return <div data-mock-embedded-chat={`${props.mid}/${props.sid}`} />;
  },
}));

const { setToken } = await import("../../lib/api");
const { provideBackgroundChat } = await import("../../lib/background-panel");
const { getState, reset, upsertSession } = await import("../../lib/store");
const { publishTranscript, unpublishTranscript } = await import("./transcript-share");
const { SubagentPanel } = await import("./SubagentPanel");

import type { Interaction, Machine, Session } from "../../lib/types";
import type { Transcript } from "./useTranscript";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [
  { id: "laptop", name: "Laptop", online: true },
  { id: "workstation", name: "Workstation", online: true },
];
const parent: Session = {
  machine_id: "laptop",
  session_id: "p1",
  title: "Parent session",
  cwd: "/code/app",
  agent: "claude",
  status: "working",
  state: "working",
  subagents: [
    { id: "a1", session_id: "p1@a1", description: "Inspect auth layer", running: true, tool_use_id: "tool-1" },
  ],
};
const remoteChild: Session = {
  machine_id: "workstation",
  session_id: "c-remote",
  title: "Run Linux integration tests",
  cwd: "/code/app",
  agent: "codex",
  status: "working",
  state: "working",
  folded_under: "p1",
  folded_under_machine_id: "laptop",
  is_subagent: true,
  capabilities: { read: true, send: true, interrupt: true },
};

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  setToken("tok");
  chatProps.calls = [];
  reset();
  Object.assign(getState(), { machines, loaded: true });
  upsertSession(parent);
  upsertSession(remoteChild);
  history.replaceState(null, "", "/s/laptop/p1");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  vi.unstubAllGlobals();
});

describe("SubagentPanel", () => {
  it("shows Session overview back button, provenance chip, Stop button when interruptible, and embeds ChatView", async () => {
    const onClose = vi.fn();
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ state: "ok" }), { headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    act(() =>
      root.render(
        <SubagentPanel
          mid="workstation"
          sid="c-remote"
          parentMid="laptop"
          parentSid="p1"
          call="child:c-remote"
          from="overview"
          onClose={onClose}
        />,
      ),
    );

    const back = host.querySelector<HTMLButtonElement>("[data-subagent-back]")!;
    expect(back.textContent).toContain("会话概览");
    expect(host.querySelector("[data-subagent-provenance]")?.textContent).toBe("Workstation · Codex");
    expect(host.querySelector('[data-mock-embedded-chat="workstation/c-remote"]')).not.toBeNull();
    expect(chatProps.calls.at(-1)).toEqual({ mid: "workstation", sid: "c-remote", embedded: true });

    // Stop button is shown because remoteChild is running and capabilities.interrupt === true
    const stopBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("停止"))!;
    expect(stopBtn).toBeDefined();
    await act(async () => {
      stopBtn.click();
      await Promise.resolve();
    });
    expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("/sessions/workstation/c-remote/interrupt"), expect.objectContaining({ method: "POST" }));

    // Back returns to Session overview via onClose
    act(() => back.click());
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("hides Stop when non-interruptible, gates Show in chat on parent call presence, and opens standalone page", () => {
    const onClose = vi.fn();
    const jumpToCall = vi.fn();
    const stopLoop = vi.fn();
    let hasCallAnswer = true;
    const unregister = provideBackgroundChat("laptop", "p1", {
      stopLoop,
      jumpToCall,
      hasCall: () => hasCallAnswer,
    });

    try {
      // Embedded <parent>@<agent> has no standalone interrupt capability -> no Stop button
      act(() =>
        root.render(
          <SubagentPanel
            mid="laptop"
            sid="p1@a1"
            parentMid="laptop"
            parentSid="p1"
            call="tool-1"
            from="overview"
            onClose={onClose}
          />,
        ),
      );
      expect([...host.querySelectorAll("button")].some((b) => b.textContent?.includes("停止"))).toBe(false);
      // Same machine + same harness -> no provenance chip
      expect(host.querySelector("[data-subagent-provenance]")).toBeNull();

      // Open ⋯ menu when parent has launching call -> shows both "定位到调用处" and "在独立页面打开"
      const moreBtn = host.querySelector<HTMLButtonElement>('button[aria-label="更多操作"]')!;
      act(() => moreBtn.click());
      const items = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
      expect(items.map((el) => el.textContent?.trim())).toEqual(["定位到调用处", "在独立页面中打开"]);

      // Clicking "定位到调用处" calls jumpToCall("tool-1")
      act(() => items[0]!.click());
      expect(jumpToCall).toHaveBeenCalledWith("tool-1");

      // When parent does NOT have the launching call, "定位到调用处" is omitted
      hasCallAnswer = false;
      act(() => moreBtn.click());
      const itemsNoCall = [...document.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
      expect(itemsNoCall.map((el) => el.textContent?.trim())).toEqual(["在独立页面中打开"]);

      // Clicking "在独立页面打开" navigates to ?standalone=1 and closes panel
      act(() => itemsNoCall[0]!.click());
      expect(location.pathname).toBe("/s/laptop/p1%40a1");
      expect(location.search).toBe("?standalone=1");
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      unregister();
    }
  });

  it("preserves failed status for embedded subagents from shared parent transcript and ignores supervisor-held cards", () => {
    const failedParent: Session = {
      ...parent,
      subagents: [{ id: "a-fail", session_id: "p1@a-fail", description: "Failing check", running: false, tool_use_id: "tool-fail" }],
    };
    upsertSession(failedParent);
    const owner = {};
    publishTranscript(owner, "laptop", "p1", {
      items: [{ id: "r1#0", seq: 2, role: "user", kind: "tool_result", tool_use_id: "tool-fail", is_error: true, text: "boom" }],
    } as Transcript);

    try {
      act(() =>
        root.render(
          <SubagentPanel mid="laptop" sid="p1@a-fail" parentMid="laptop" parentSid="p1" call="tool-fail" from="overview" onClose={() => {}} />,
        ),
      );
      const statusEl = host.querySelector("[data-subagent-panel-status]")!;
      expect(statusEl.getAttribute("data-subagent-panel-status")).toBe("failed");
      expect(statusEl.textContent).toContain("失败");
      expect(statusEl.innerHTML).toContain("text-red");

      // Supervisor-held card on standalone child should read as running, not needs_you
      const heldChild: Session = { ...remoteChild, state: "needs_you", status: "blocked" };
      upsertSession(heldChild);
      const heldIx: Interaction = {
        id: "ix-sup",
        machine_id: "workstation",
        session_id: "c-remote",
        folded_under: "p1",
        folded_under_machine_id: "laptop",
        kind: "permission",
        delegating: true,
        tool_name: "Bash",
      };
      Object.assign(getState(), { interactions: [heldIx] });
      act(() =>
        root.render(
          <SubagentPanel mid="workstation" sid="c-remote" parentMid="laptop" parentSid="p1" from="overview" onClose={() => {}} />,
        ),
      );
      expect(host.querySelector("[data-subagent-panel-status]")?.getAttribute("data-subagent-panel-status")).toBe("running");
    } finally {
      unpublishTranscript(owner, "laptop", "p1");
    }
  });
});
