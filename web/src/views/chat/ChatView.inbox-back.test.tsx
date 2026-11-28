// @vitest-environment jsdom
// Which full conversation pages offer the Inbox way-back strip, and its precedence over the hand-off strip.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});

const origin = vi.hoisted(() => ({ current: { id: "i1", title: "Release review" } as { id: string; title?: string } | undefined }));
vi.mock("../../lib/inbox-origin", () => ({ useInboxOrigin: () => origin.current }));
vi.mock("../../lib/panel", async (load) => ({ ...(await load<typeof import("../../lib/panel")>()), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../../lib/store", async (load) => ({
  ...(await load<typeof import("../../lib/store")>()),
  listenSession: () => () => {},
}));

import { applyCompleteSessions, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { ChatView } from "./ChatView";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// Negative assertions must not pass merely because the lazy strip has not loaded yet. This also loads BackStrip's module.
beforeAll(async () => { await import("./InboxBack"); });

const row = () => ({ machine_id: "m1", session_id: "s1", title: "Checks", cwd: "", status: "idle", state: "idle", origin: "inbox", origin_ref: "i1" }) as Session;
const settle = () => act(async () => { await new Promise((r) => setTimeout(r, 0)); });

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  localStorage.clear();
  history.replaceState(null, "", "/s/m1/s1");
  origin.current = { id: "i1", title: "Release review" };
  vi.stubGlobal("fetch", vi.fn((url: string) => Promise.resolve(new Response(JSON.stringify(String(url).includes("/items") ? { items: [], to_seq: 0 } : {}), { status: 200, headers: { "Content-Type": "application/json" } }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  history.replaceState(null, "", "/");
  reset();
  vi.unstubAllGlobals();
});

async function open(view: React.ReactElement) {
  act(() => applyCompleteSessions([row()]));
  act(() => root.render(view));
  for (let i = 0; i < 6; i++) await settle();
}

describe("the Inbox strip's place in ChatView", () => {
  it("appears on an Agent's full page", async () => {
    await open(<ChatView mid="m1" sid="s1" inPage />);
    await vi.waitFor(() => expect(host.querySelector("[data-inbox-back]")).not.toBeNull(), { timeout: 2_000 });
  });

  it("appears on a non-main Persona conversation", async () => {
    await open(<ChatView mid="m1" sid="s1" assistant />);
    await vi.waitFor(() => expect(host.querySelector("[data-inbox-back]")).not.toBeNull(), { timeout: 2_000 });
  });

  it("does not appear on the main conversation", async () => {
    await open(<ChatView mid="m1" sid="s1" assistant mainCache={{ scope: "test" }} />);
    expect(host.querySelector("[data-inbox-back]")).toBeNull();
  });

  it("does not appear in a side panel or a Team panel", async () => {
    await open(<ChatView mid="m1" sid="s1" panel={{ kind: "agent", onClose: () => {} }} />);
    expect(host.querySelector("[data-inbox-back]")).toBeNull();

    act(() => root.unmount());
    root = createRoot(host);
    await open(<ChatView mid="m1" sid="s1" embedded />);
    expect(host.querySelector("[data-inbox-back]")).toBeNull();
  });

  it("keeps only the hand-off strip when both ways back exist", async () => {
    history.replaceState({ handoff: { path: "/persona/c0" } }, "", "/s/m1/s1");
    await open(<ChatView mid="m1" sid="s1" inPage />);
    await vi.waitFor(() => expect(host.querySelector("[data-handoff-back]")).not.toBeNull(), { timeout: 2_000 });
    expect(host.querySelector("[data-inbox-back]")).toBeNull();
    act(() => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-backstrip.handoff_dismiss")}"]`)!.click());
    expect(host.querySelector("[data-handoff-back]")).toBeNull();
    expect(host.querySelector("[data-inbox-back]")).toBeNull();
  });
});
