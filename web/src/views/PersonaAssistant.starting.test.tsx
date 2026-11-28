// @vitest-environment jsdom
// The first message while its conversation starts (#1697): the new-chat page takes the conversation's layout at once (its top
// bar titled "New chat" as the conversation page calls an untitled one, the message as a bubble, "Preparing" under it),
// says "Still starting <launcher>…" after 8 seconds, and a refused start goes back to the new-chat page with the words in the box.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getState, reset } from "../lib/store";
import { sendDraft } from "../lib/drafts";
import { dropPick, NEW_CONVERSATION, pickEffort, pickLauncher, pickModel, pickedServiceTier, pickServiceTier } from "../lib/assistant-chat";
import { ASSISTANT_START_SERVICE_TIER_FEATURE } from "../lib/assistant-models";
import { chooseManager } from "../lib/manager-choice";
import type { Session } from "../lib/types";
import { panelStoreKey } from "../ui/model-panel/store";
import { PersonaAssistant, SLOW_START_MS } from "./PersonaAssistant";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

type Started = { session: Session };
const starts = vi.hoisted(() => [] as { resolve: (r: Started) => void; reject: (e: unknown) => void; body: Record<string, unknown> }[]);
vi.mock("../lib/assistant-start", () => ({
  startConversation: (body: Record<string, unknown>) => new Promise<Started>((resolve, reject) => starts.push({ resolve, reject, body })),
}));

const navigate = vi.hoisted(() => vi.fn());
vi.mock("../lib/router", async (orig) => ({ ...(await orig<typeof import("../lib/router")>()), navigate }));

vi.mock("../lib/readiness", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../lib/readiness")>();
  return {
    ...actual,
    useReadiness: () => ({ readiness: null, probing: false }),
    useReadinessGate: () => ({ step: null, blocked: false, readiness: null, help: () => {}, confirm: () => {} }),
    refreshReadiness: () => Promise.resolve(),
    passesBySelf: () => false,
    codedFailure: () => null,
  };
});

const WORDS = "帮我规划一下这个周末的行程";
let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};

const send = async () => {
  const area = host.querySelector("textarea")!;
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, WORDS);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const button = host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!;
  await act(async () => button.click());
  await flush();
};

const waiting = () => host.querySelector("[data-start-pending]");
const sentBody = () => JSON.parse(JSON.stringify(starts[0].body)) as Record<string, unknown>;
const headerTitle = () => host.querySelector("header h1, header [data-header-title]")?.textContent ?? host.querySelector("header")?.textContent ?? "";

beforeEach(() => {
  reset();
  dropPick(NEW_CONVERSATION);
  starts.length = 0;
  navigate.mockClear();
  Object.assign(getState(), {
    loaded: true,
    assistant: { machine_id: "hub", main: "c1", conversations: [{ machine_id: "hub", session_id: "c1", title: "" }] },
    assistantError: null,
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  localStorage.removeItem(panelStoreKey("assistant", "recent"));
  act(() => root.unmount());
  host.remove();
  reset();
  dropPick(NEW_CONVERSATION);
  chooseManager(null);
});

describe("新对话页：第一条消息发出、会话还没开起来 (#1697)", () => {
  it("立刻换成对话页的样子：页头「新对话」、气泡、「正在准备」，欢迎语收起，输入框不能发", async () => {
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    expect(host.textContent).toContain("有什么我可以帮你的？");

    await send();
    expect(starts).toHaveLength(1);
    expect(waiting()).not.toBeNull();
    expect(headerTitle()).toContain("新对话");
    expect(headerTitle()).not.toContain(WORDS);
    expect(waiting()!.querySelector("[data-user-bubble]")?.textContent).toContain(WORDS);
    expect(waiting()!.textContent).toContain("正在准备");
    expect(host.textContent).not.toContain("有什么我可以帮你的？");
    expect(host.querySelector("textarea")!.disabled).toBe(true);
  });

  it("超过 8 秒，「正在准备」换成「还在启动 …」", async () => {
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    vi.useFakeTimers();
    await send();
    act(() => vi.advanceTimersByTime(SLOW_START_MS - 100));
    expect(waiting()!.textContent).toContain("正在准备");
    act(() => vi.advanceTimersByTime(200));
    expect(waiting()!.textContent).toContain("仍在启动");
  });

  it("开起来了：去那个会话", async () => {
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    await act(async () => starts[0].resolve({ session: { machine_id: "hub", session_id: "c2", title: "" } as Session }));
    await flush();
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "c2" });
  });

  it("被拒绝：回到新对话页，文字回到输入框", async () => {
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    await act(async () => starts[0].reject(new Error("no launcher")));
    await flush();
    expect(waiting()).toBeNull();
    expect(host.textContent).toContain("有什么我可以帮你的？");
    expect(host.querySelector("textarea")!.value).toBe(WORDS);
    expect(host.querySelector("textarea")!.disabled).toBe(false);
    expect(navigate).not.toHaveBeenCalled();
  });

  it.each(["priority", "ultrafast"])("sends the explicit %s tier with the first request, independently of effort, then clears it", async (tier) => {
    Object.assign(getState(), { features: [ASSISTANT_START_SERVICE_TIER_FEATURE] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickEffort(NEW_CONVERSATION, "high");
    pickServiceTier(tier);
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    expect(sentBody()).toMatchObject({ launcher: "codex", model: "gpt-fast", effort: "high", service_tier: tier });
    await act(async () => starts[0].resolve({ session: { machine_id: "hub", session_id: "c2", title: "" } as Session }));
    await flush();
    expect(pickedServiceTier()).toBeUndefined();
  });

  it("a Fast-only Recent pick names the displayed launcher, model and effort in the first request", async () => {
    Object.assign(getState(), { features: [ASSISTANT_START_SERVICE_TIER_FEATURE] });
    localStorage.setItem(panelStoreKey("assistant", "recent"), JSON.stringify([{ provider: "codex", model: "gpt-fast", effort: "low", fast: true }]));
    const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/codex/models")) return json({ agent: "codex", models: [
        { id: "gpt-fast", name: "GPT Fast", efforts: ["low", "high"], service_tiers: [{ id: "default", name: "Standard" }, { id: "priority", name: "Fast" }] },
      ] });
      if (url.includes("/launchers?purpose=assistant")) return json({
        launchers: [{ name: "codex", source: "builtin", agent: "codex", agent_type: "codex" }],
        default: { provider: "codex", model: "gpt-fast", effort: "low" },
      });
      return json({ ok: true });
    }));
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    const pill = host.querySelector<HTMLButtonElement>("[data-new-conversation] button[data-model-pill]")!;
    expect(pill.getAttribute("data-model-pill")).toBe("ready");
    await act(async () => pill.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(document.querySelector("[data-panel-quick]")).not.toBeNull();
    await flush();
    const recent = document.querySelector<HTMLElement>('[data-panel-row="recent:0"]')!;
    expect(recent.getAttribute("aria-disabled")).toBeNull();
    expect(recent.querySelector("[data-row-setup]")?.getAttribute("title")).toContain("Fast");
    await act(async () => recent.click());
    await flush();
    await send();
    expect(sentBody()).toMatchObject({ launcher: "codex", model: "gpt-fast", effort: "low", service_tier: "priority" });
  });

  it("an untouched first request has no service_tier", async () => {
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    expect(sentBody()).not.toHaveProperty("service_tier");
  });

  it("an older hub gets no service_tier even when a downgraded page held a draft", async () => {
    Object.assign(getState(), { features: [] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickServiceTier("priority");
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    expect(sentBody()).toMatchObject({ launcher: "codex", model: "gpt-fast" });
    expect(sentBody()).not.toHaveProperty("service_tier");
  });

  it("a manager change disarms Fast before the destination catalog answers", async () => {
    Object.assign(getState(), { features: [ASSISTANT_START_SERVICE_TIER_FEATURE] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickServiceTier("priority");
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    act(() => chooseManager("other-manager"));
    await flush();
    expect(pickedServiceTier()).toBeUndefined();
    await send();
    expect(sentBody()).not.toHaveProperty("service_tier");
  });

  it("a refused start keeps Fast with the restored draft", async () => {
    Object.assign(getState(), { features: [ASSISTANT_START_SERVICE_TIER_FEATURE] });
    pickLauncher(NEW_CONVERSATION, "codex");
    pickModel(NEW_CONVERSATION, "gpt-fast");
    pickServiceTier("priority");
    await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
    await flush();
    await send();
    await act(async () => starts[0].reject(new Error("no launcher")));
    await flush();
    expect(pickedServiceTier()).toBe("priority");
    expect(sentBody()).toHaveProperty("service_tier", "priority");
  });

  it("picking the displayed concrete default model pins it in the first request (including with an image), while Follow default clears it (#13567)", async () => {
    const origCreate = URL.createObjectURL;
    const origRevoke = URL.revokeObjectURL;
    URL.createObjectURL = vi.fn(() => "blob:test/photo");
    URL.revokeObjectURL = vi.fn();
    try {
      Object.assign(getState(), {
        assistant: { machine_id: "hub-opus", main: "c1", conversations: [{ machine_id: "hub-opus", session_id: "c1", title: "" }] },
      });
      const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
      vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/claude/models")) return json({ agent: "claude", models: [
          { id: "default", name: "Default (recommended)", default: false, resolved: "claude-opus-5-5", efforts: ["low", "medium", "high"] },
          { id: "opus", name: "Opus 5.5", default: true, resolved: "claude-opus-5-5", efforts: ["low", "medium", "high"] },
          { id: "haiku", name: "Haiku 5.5", resolved: "claude-haiku-5-5" },
        ] });
        if (url.includes("/launchers?purpose=assistant")) return json({
          launchers: [{ name: "claude", source: "builtin" }],
          default: { provider: "claude", model: "opus", effort: "high" },
        });
        return json({ ok: true });
      }));
      await act(async () => root.render(<PersonaAssistant fresh onToggleSidebar={() => {}} />));
      await flush();
      const pill = host.querySelector<HTMLButtonElement>("[data-new-conversation] button[data-model-pill]")!;
      expect(pill.textContent).toContain("Opus 5.5");
      await act(async () => pill.click());
      await act(async () => vi.dynamicImportSettled());
      await flush();
      const opusRow = [...document.querySelectorAll<HTMLElement>("[data-panel-row]")].find((r) => r.getAttribute("data-panel-row")?.includes("opus"))!;
      expect(opusRow).toBeDefined();
      await act(async () => opusRow.click());
      await flush();
      const imageData = btoa("sample-image");
      act(() => void sendDraft("persona-new", { type: "image", image: { name: "photo.png", mime: "image/png", data: imageData } }));
      await flush();
      await send();
      expect(sentBody()).toMatchObject({ launcher: "claude", model: "opus", effort: "high", images: [{ mime: "image/png", data: imageData }] });
      // Refusing the start keeps the draft; opening the panel and clicking Follow default returns to following default.
      await act(async () => starts[0].reject(new Error("unresolved")));
      await flush();
      await act(async () => host.querySelector<HTMLButtonElement>("[data-new-conversation] button[data-model-pill]")!.click());
      await flush();
      const followBtn = document.querySelector<HTMLButtonElement>('[data-pick-strip="pick"] button')!;
      expect(followBtn).not.toBeNull();
      await act(async () => followBtn.click());
      await flush();
      await send();
      expect(starts).toHaveLength(2);
      const secondBody = JSON.parse(JSON.stringify(starts[1].body)) as Record<string, unknown>;
      expect(secondBody).not.toHaveProperty("launcher");
      expect(secondBody).not.toHaveProperty("model");
      expect(secondBody).not.toHaveProperty("effort");
    } finally {
      URL.createObjectURL = origCreate;
      URL.revokeObjectURL = origRevoke;
    }
  });
});
