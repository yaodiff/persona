// @vitest-environment jsdom
// One health conversation (docs/assistant/health.md) runs on the machine its space's list names: a list that can't be read says
// why with Retry instead of loading for ever, and is read again on the next hello.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ listeners: new Map<string, (() => void)[]>(), answers: [] as (() => Promise<unknown>)[], started: 0 }));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string) => (path.startsWith("/assistant?space=health") && hub.answers.length ? hub.answers.shift()!() : Promise.resolve({})),
}));
vi.mock("../../lib/store", async (orig) => ({
  ...(await orig<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: () => void) => {
    hub.listeners.set(type, [...(hub.listeners.get(type) ?? []), l]);
    return () => hub.listeners.set(type, (hub.listeners.get(type) ?? []).filter((x) => x !== l));
  },
}));
vi.mock("./reports", async (orig) => ({
  ...(await orig<typeof import("./reports")>()),
  startHealthConversation: () => (hub.started++, new Promise(() => {})),
}));
vi.mock("../chat/ChatView", () => ({
  ChatView: ({ mid, sid, headerActions, placeholder }: { mid: string; sid: string; headerActions?: React.ReactNode; placeholder?: string }) => <div><p data-chat data-placeholder={placeholder}>{`${mid}/${sid}`}</p>{headerActions}</div>,
}));

import { Health } from "./Health";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const failing = () => Promise.reject(new Error("hub away"));
const listed = () => Promise.resolve({ machine_id: "mac", conversations: [] });

beforeEach(() => {
  hub.listeners.clear();
  hub.answers = [];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("the health page's box", () => {
  it("takes no second message while a conversation is starting", async () => {
    hub.answers.push(listed);
    localStorage.setItem("draft:health-new", "first question");
    act(() => root.render(<Health fresh onToggleSidebar={() => {}} />));
    await flush();
    const send = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-composer.send")}"]`)!;
    await act(async () => send.click());
    await flush();
    expect(hub.started).toBe(1);
    expect(host.querySelector("textarea")!.disabled).toBe(true);
  });
});

describe("a health conversation", () => {
  it("says why when its space's list can't be read, and opens on Retry", async () => {
    hub.answers.push(failing, listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[role=alert]")?.textContent).toContain("hub away");
    const retry = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("重试"))!;
    await act(async () => retry.click());
    await flush();
    expect(host.querySelector("[data-chat]")?.textContent).toBe("mac/h1");
  });

  it("asks health questions in its box, as the space's home does, not the main chat's words", async () => {
    hub.answers.push(listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-chat]")?.getAttribute("data-placeholder")).toBe(t("health.ask_placeholder"));
  });

  it("reads the list again on the next hello", async () => {
    hub.answers.push(failing, listed);
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-chat]")).toBeNull();
    act(() => hub.listeners.get("hello")?.forEach((l) => l()));
    await flush();
    expect(host.querySelector("[data-chat]")?.textContent).toBe("mac/h1");
  });
});

// Entering Health (#4329): back in the latest conversation, as Codex does; the new conversation page when there is none;
// New in a conversation's header opens that page.
describe("entering Health", () => {
  const conv = (session_id: string, updated_at: string) => ({ session_id, updated_at, title: session_id, space: "health" });

  beforeEach(() => history.replaceState(null, "", "/health"));

  it("opens the latest health conversation, in place of /health", async () => {
    const before = history.length;
    hub.answers.push(() => Promise.resolve({ machine_id: "mac", conversations: [conv("old", "2026-11-01T08:00:00Z"), conv("latest", "2026-11-11T08:00:00Z")] }));
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health/latest");
    expect(history.length).toBe(before); // replaced: Back does not come back to /health and bounce
  });

  it("opens the new conversation page when the space has none", async () => {
    hub.answers.push(listed);
    act(() => root.render(<Health onToggleSidebar={() => {}} />));
    await flush();
    expect(location.pathname).toBe("/health/new");
  });

  it("New in a health conversation opens the new conversation page; the clock lists the past ones", async () => {
    hub.answers.push(() => Promise.resolve({ machine_id: "mac", conversations: [conv("h1", "2026-11-11T08:00:00Z"), conv("h2", "2026-11-10T08:00:00Z")] }));
    act(() => root.render(<Health sid="h1" onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector(`button[aria-label="${t("health.history")}"]`)).not.toBeNull();
    const fresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("health.new_chat")}"]`)!;
    await act(async () => fresh.click());
    expect(location.pathname).toBe("/health/new");
  });
});
