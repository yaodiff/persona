// @vitest-environment jsdom
// #1808 AC1 / web AC6: an explicit, hub-capability-gated session review. Reading the review's availability never
// starts one; only the menu choice POSTs the start. The same route reports { running, has_changes } so the menu can
// explain why it is disabled without asking the source Agent or inspecting its reasoning.
import { openAgentMore } from "../../test/agent-menu";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  globalThis.IntersectionObserver ??= class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } } as unknown as typeof IntersectionObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
vi.mock("../../lib/store", async (load) => ({
  ...(await load<typeof import("../../lib/store")>()),
  listenSession: () => () => {},
}));
vi.mock("../../lib/panel", async (load) => ({ ...(await load<typeof import("../../lib/panel")>()), usePanel: () => null }));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../../ui/atoms", async (load) => ({
  ...(await load<typeof import("../../ui/atoms")>()),
  toast: vi.fn(),
}));

import { loadPages } from "../../lib/lazy-page";
import { applyCompleteSessions, reset } from "../../lib/store";
import type { Session } from "../../lib/types";
import { t } from "../../i18n";
import { toast } from "../../ui/atoms";
import { ChatView } from "./ChatView";
import en from "../../i18n/locales/en/chat-session-review.json";
import zhHans from "../../i18n/locales/zh-Hans/chat-session-review.json";
import ja from "../../i18n/locales/ja/chat-session-review.json";

const reviewText = (key: keyof typeof en) => t(`chat-session-review.${key}`);

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const FEATURE = "session_review";
const reviewRoute = "/api/v1/sessions/mac/s1/review";
const available = { running: false, has_changes: true };
const requests: { url: string; method: string }[] = [];
let reviewStatus = available;
let postStatus = 200;
let postBody: Record<string, unknown> = { ok: true };

const row = (x: Partial<Session> = {}): Session => ({
  machine_id: "mac",
  session_id: "s1",
  title: "Feature work",
  cwd: "/code",
  agent: "claude",
  launcher: "claude",
  status: "idle",
  state: "idle",
  ...x,
}) as Session;

const settle = () => act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const startCalls = () => requests.filter((request) => request.url === reviewRoute && request.method === "POST");

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  requests.length = 0;
  reviewStatus = available;
  postStatus = 200;
  postBody = { ok: true };
  vi.mocked(toast).mockClear();
  vi.stubGlobal("fetch", vi.fn((input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    requests.push({ url, method });
    if (url === reviewRoute) {
      return Promise.resolve(method === "POST" ? json(postBody, postStatus) : json(reviewStatus));
    }
    return Promise.resolve(json(url.includes("/items") ? { items: [], to_seq: 0 } : {}));
  }));
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

async function open(session = row(), features: string[] = [FEATURE]) {
  act(() => applyCompleteSessions([session], [], { features }));
  act(() => root.render(<ChatView mid="mac" sid="s1" inPage />));
  for (let i = 0; i < 6; i++) await settle();
}

async function openMenu() {
  const more = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`);
  expect(more).not.toBeNull();
  act(() => more!.click());
  await loadPages();
  await settle();
  openAgentMore();
}

function reviewItem(): HTMLButtonElement | undefined {
  const label = reviewText("this_work");
  return [...document.querySelectorAll<HTMLButtonElement>('[role="menu"] [role="menuitem"]')]
    .find((item) => item.textContent?.includes(label));
}

describe("the explicit session-review menu entry", () => {
  it("leaves the existing menu alone and does not query or start a review without the hub capability", async () => {
    await open(row(), []);
    await openMenu();
    expect(reviewItem()).toBeUndefined();
    expect(requests.some((request) => request.url === reviewRoute)).toBe(false);
    expect(startCalls()).toHaveLength(0);
  });

  it("offers an enabled entry when the hub is capable and the session has a change", async () => {
    await open();
    expect(startCalls()).toHaveLength(0);
    await openMenu();
    const item = reviewItem();
    expect(item).toBeDefined();
    expect(item!.disabled).toBe(false);
    expect(startCalls()).toHaveLength(0);
  });

  it("updates the open submenu when the review check finishes", async () => {
    let finishCheck!: (response: Response) => void;
    const originalFetch = vi.mocked(fetch).getMockImplementation()!;
    vi.mocked(fetch).mockImplementation((input, init) => String(input) === reviewRoute
      ? new Promise<Response>((resolve) => { finishCheck = resolve; })
      : originalFetch(input, init));
    await open();
    await openMenu();
    expect(reviewItem()?.disabled).toBe(true);
    await act(async () => finishCheck(json(available)));
    expect(reviewItem()?.disabled).toBe(false);
  });

  it.each([
    {
      name: "the source Agent is busy",
      session: row({ status: "working", state: "working" }),
      status: available,
      reason: "busy" as const,
    },
    {
      name: "a review is already running",
      session: row(),
      status: { running: true, has_changes: true },
      reason: "running" as const,
    },
    {
      name: "the session has no change",
      session: row(),
      status: { running: false, has_changes: false },
      reason: "no_change" as const,
    },
  ])("is disabled with a reason when $name", async ({ session, status, reason }) => {
    reviewStatus = status;
    await open(session);
    await openMenu();
    const item = reviewItem();
    expect(item).toBeDefined();
    expect(item!.disabled).toBe(true);
    expect(item!.textContent).toContain(reviewText(reason));
    act(() => item!.click());
    await settle();
    expect(startCalls()).toHaveLength(0);
  });

  it("starts exactly one review on an explicit click and immediately confirms the request", async () => {
    await open();
    await openMenu();
    expect(startCalls()).toHaveLength(0);
    const item = reviewItem();
    expect(item).toBeDefined();
    await act(async () => item!.click());
    await settle();
    expect(startCalls()).toHaveLength(1);
    expect(vi.mocked(toast)).toHaveBeenCalledWith(reviewText("requested"), "ok");
  });

  it("shows the hub's localized refusal instead of replacing it with a generic error", async () => {
    const refusal = "このランチャーでは読み取り専用のレビューを開始できません。";
    postStatus = 409;
    postBody = { code: "read_only_unsupported", error: refusal };
    await open();
    await openMenu();
    const item = reviewItem();
    expect(item).toBeDefined();
    await act(async () => item!.click());
    await settle();
    expect(startCalls()).toHaveLength(1);
    expect(vi.mocked(toast)).toHaveBeenCalledWith(reviewText("failed").replace("{error}", refusal), "error");
    expect(vi.mocked(toast)).not.toHaveBeenCalledWith(t("chat-chatview.review_requested"), "ok");
  });
});

const catalogs: [string, Record<string, string>][] = [["en", en], ["zh-Hans", zhHans], ["ja", ja]];

describe("session-review web strings", () => {
  it.each(catalogs)("ships every new menu string in %s", (_language, catalog) => {
    for (const key of [
      "this_work",
      "busy",
      "running",
      "no_change",
      "checking",
      "starting",
      "requested",
      "failed",
    ]) {
      expect(catalog[key], key).toBeTypeOf("string");
      expect(catalog[key].trim(), key).not.toBe("");
    }
  });
});
