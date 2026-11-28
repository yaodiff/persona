// @vitest-environment jsdom
// If reading from the hub fails, show "Couldn't load main conversation: reason" and Retry, not the welcome page (#5174).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { getState, reset } from "../lib/store";
import { PersonaAssistant } from "./PersonaAssistant";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

vi.mock("./chat/ChatView", () => ({
  ChatView: () => <div data-testid="chat-view">ChatView</div>,
}));

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
  reset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  reset();
});

describe("PersonaAssistant 主对话 hub 读失败 (#5174)", () => {
  it("hub 读失败时写「没读到主对话：原因」+「重试」，不画欢迎页", async () => {
    Object.assign(getState(), {
      loaded: true,
      assistant: null,
      assistantError: "HTTP 503: Service Unavailable",
    });

    await act(async () => {
      root.render(<PersonaAssistant onToggleSidebar={() => {}} />);
    });
    await flush();

    expect(host.textContent).not.toContain("有什么我可以帮你的？");
    expect(host.textContent).toContain("无法加载主对话：HTTP 503: Service Unavailable");
    const retryBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"));
    expect(retryBtn).toBeDefined();
  });

  it("点击重试时调用 refreshAssistant 重新读取", async () => {
    Object.assign(getState(), {
      loaded: true,
      assistant: null,
      assistantError: "连不上 hub",
    });

    let fetchCount = 0;
    vi.stubGlobal("fetch", vi.fn().mockImplementation(async (url: string) => {
      if (url.endsWith("/assistant")) {
        fetchCount++;
        return new Response(JSON.stringify({ machine_id: "hub", main: "c1", conversations: [{ session_id: "c1" }] }), { status: 200 });
      }
      return new Response(JSON.stringify({}), { status: 200 });
    }));

    await act(async () => {
      root.render(<PersonaAssistant onToggleSidebar={() => {}} />);
    });
    await flush();

    const retryBtn = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"))!;
    expect(retryBtn).toBeDefined();

    await act(async () => {
      retryBtn.click();
    });
    await flush();

    expect(fetchCount).toBe(1);
    expect(host.querySelector('[data-testid="chat-view"]')).not.toBeNull();
  });

  it("正常无对话时（loaded: true 且无 error）画欢迎页", async () => {
    Object.assign(getState(), {
      loaded: true,
      assistant: { machine_id: "hub", main: "", conversations: [] },
      assistantError: null,
    });

    await act(async () => {
      root.render(<PersonaAssistant onToggleSidebar={() => {}} />);
    });
    await flush();

    expect(host.textContent).toContain("有什么我可以帮你的？");
  });
});
