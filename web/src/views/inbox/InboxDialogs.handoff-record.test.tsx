// @vitest-environment jsdom
// "Hand to AI" records where an item went (DECISIONS D3, docs/assistant/inbox.md "Item actions"): once the text got to a
// new Persona chat, a running agent or a new agent, one more `POST /inbox/{id}/handoff` (no `text`: the record only) with
// the session, its kind, the conversation's title and a fresh `client_id`; a refusal (an older hub's 400, 404, 405, any
// other) is ignored without a word, and nothing is recorded when the text did not get there.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const world = vi.hoisted(() => ({ sessions: {} as Record<string, unknown[]>, machines: [] as unknown[] }));
vi.mock("../../lib/store", async (orig) => {
  const real = await orig<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(sel: (s: ReturnType<typeof real.getState>) => T) =>
      real.useStore((s) => sel({ ...s, sessions: world.sessions, machines: world.machines } as ReturnType<typeof real.getState>)),
  };
});
vi.mock("../../lib/router", () => ({ navigate: vi.fn() }));
vi.mock("../../lib/new-agent", async (orig) => ({ ...(await orig<typeof import("../../lib/new-agent")>()), openNewAgent: vi.fn() }));
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast: vi.fn() }));

import { setPairedToken, setToken } from "../../lib/api";
import { reloadInbox, resetInboxForTests, type InboxItem } from "../../lib/inbox";
import { resetMessageFeedback, settleMessageEcho } from "../../lib/message-send";
import { openNewAgent, type NewAgentPreset } from "../../lib/new-agent";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { keepFailed, retryOutbox } from "../../lib/outbox";
import { navigate } from "../../lib/router";
import type { Session } from "../../lib/types";
import { HandOffDialog } from "./InboxDialogs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const item = (manager = "assistant"): InboxItem => ({
  id: "lark:f1:1", manager, source: "lark", kind: "dm", account: "feishu", account_name: "Lark", title: "Example", preview: "text", count: 1,
  updated_at: "2026-11-01T09:00:00Z", tier: "action", lark: { feed_id: "f1", is_group: false },
  mutes: [{ tag: "chat", value: "oc_example", name: "Example" }],
});
const agent = { machine_id: "mac", session_id: "s1", title: "Release check", cwd: "/code", status: "idle", state: "idle" } as Session;
const started = { machine_id: "laptop", session_id: "s-9", title: "Fresh agent", cwd: "/code", status: "idle" } as Session;

let host: HTMLDivElement;
let root: Root;
type Call = { url: string; method: string; header: string | null; body: Record<string, unknown> | undefined };
const calls: Call[] = [];
/** What the hub answers for the record, and for the conversations a hand-off starts. */
const hub = { record: 200 as number, start: 200 as number, startTitle: "Tool setup" as string | undefined };

beforeEach(() => {
  calls.length = 0;
  localStorage.clear();
  resetMessageFeedback();
  setToken("synthetic-handoff-token");
  resetManagerChoice();
  resetInboxForTests();
  setRosterDefault("assistant");
  world.sessions = { mac: [agent] };
  world.machines = [{ id: "mac", name: "Studio", online: true }];
  hub.record = 200;
  hub.start = 200;
  hub.startTitle = "Tool setup";
  vi.mocked(openNewAgent).mockClear();
  vi.mocked(navigate).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const path = String(url);
    const method = init?.method ?? "GET";
    calls.push({ url: path, method, header: new Headers(init?.headers).get("X-Persona-Manager"), body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (path.endsWith("/handoff") && method === "POST") {
      // the main conversation's own hand-off carries the text; a record does not
      const json = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {};
      if ("text" in json) return answer(200, { item: item(), session: { machine_id: "mac", session_id: "main-a" } });
      return answer(hub.record, { error: "refused" });
    }
    if (path.endsWith("/assistant/conversations")) {
      if (hub.start >= 400) return answer(hub.start, { error: "refused" });
      return answer(200, { session: { machine_id: "mac", session_id: "s-new", ...(hub.startTitle ? { title: hub.startTitle } : {}) } });
    }
    if (path.includes("/inbox?")) return answer(200, { items: [item()] });
    if (path.includes("/inbox/")) return answer(200, item());
    return answer(200, {});
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

function answer(status: number, body: unknown) {
  return { ok: status < 300, status, text: async () => JSON.stringify(body), headers: new Headers() } as Response;
}

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  setToken("");
  resetMessageFeedback();
  resetManagerChoice();
  resetInboxForTests();
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const render = (it: InboxItem = item()) => act(() => root.render(<HandOffDialog item={it} onClose={() => {}} />));
const pick = (label: string) => {
  const radio = [...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes(label)) as HTMLButtonElement;
  expect(radio).toBeTruthy();
  act(() => radio.click());
};
const sendButton = () => [...document.body.querySelectorAll("button")].find((b) => b.textContent?.trim() === "发送") as HTMLButtonElement;
const sends = () => calls.filter((c) => c.method === "POST" && c.url.endsWith("/handoff"));
const records = () => sends().filter((c) => !("text" in (c.body ?? {})));
const messageCalls = () => calls.filter((c) => c.method === "POST" && c.url === "/api/v1/sessions/mac/s1/messages");

/** Starts the real dialog-to-outbox path but leaves each message POST under the test's control. */
async function sendAgentHeld() {
  await reloadInbox();
  const fetcher = vi.mocked(fetch);
  const answer0 = fetcher.getMockImplementation()!;
  const pending: { resolve: (r: Response) => void; reject: (e: unknown) => void }[] = [];
  fetcher.mockImplementation((url, init) => {
    if (String(url) === "/api/v1/sessions/mac/s1/messages") {
      calls.push({
        url: String(url), method: init?.method ?? "GET", header: new Headers(init?.headers).get("X-Persona-Manager"),
        body: init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : undefined,
      });
      return new Promise<Response>((resolve, reject) => pending.push({ resolve, reject }));
    }
    return answer0(url, init);
  });
  render();
  pick("Release check");
  await act(async () => sendButton().click());
  await vi.waitFor(() => expect(messageCalls()).toHaveLength(1));
  const body = messageCalls()[0].body!;
  return {
    id: String(body.client_msg_id),
    text: String(body.text),
    count: () => pending.length,
    respond(index: number, status: number, responseBody: unknown = {}) {
      pending[index].resolve(answer(status, responseBody));
    },
    fail(index: number, error: unknown) {
      pending[index].reject(error);
    },
  };
}

describe("Hand to AI: where the item went", () => {
  it("a new Persona chat records the conversation it started, with no text", async () => {
    await reloadInbox();
    render();
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    const body = records()[0].body!;
    expect(body).toEqual({
      session: { machine_id: "mac", session_id: "s-new" },
      kind: "conversation",
      title: "Tool setup",
      client_id: expect.stringMatching(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/),
    });
    expect(body).not.toHaveProperty("text");
    expect(records()[0].url).toBe("/api/v1/inbox/lark:f1:1/handoff");
    // after the conversation was started, not before
    expect(calls.findIndex((c) => c.url.endsWith("/assistant/conversations"))).toBeLessThan(calls.indexOf(records()[0]));
  });

  it("a running agent records that agent, as an agent", async () => {
    await reloadInbox();
    render();
    act(() => ([...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes("Release check")) as HTMLButtonElement).click());
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    const body = records()[0].body!;
    expect(body.kind).toBe("agent");
    expect(body.session).toEqual({ machine_id: "mac", session_id: "s1" });
    expect(body).not.toHaveProperty("text");
    expect(body.title).toBe("Release check");
    // after the message got there, not before
    expect(calls.findIndex((c) => c.url === "/api/v1/sessions/mac/s1/messages")).toBeLessThan(calls.indexOf(records()[0]));
  });

  it("New agent… records the agent it started, as an agent", async () => {
    await reloadInbox();
    render();
    await act(async () => (document.body.querySelector('[data-testid="handoff-new-agent"]') as HTMLButtonElement).click());
    await vi.waitFor(() => expect(openNewAgent).toHaveBeenCalledTimes(1));
    const preset = vi.mocked(openNewAgent).mock.calls[0][0] as NewAgentPreset;
    act(() => preset.onStarted!(started));
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    const body = records()[0].body!;
    expect(body.kind).toBe("agent");
    expect(body.session).toEqual({ machine_id: "laptop", session_id: "s-9" });
    expect(body).not.toHaveProperty("text");
    expect(body.title).toBe("Fresh agent");
  });

  it("names an agent with no title by its folder, as the row does, and never by the localized 'untitled'", async () => {
    // a session the hub gave no title: the row shows the folder's name
    world.sessions = { mac: [{ ...agent, title: "", cwd: "/work/release-check" }] };
    await reloadInbox();
    render();
    const row = [...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes("release-check")) as HTMLButtonElement;
    expect(row).toBeTruthy();
    act(() => row.click());
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    expect(records()[0].body!.title).toBe("release-check");
    expect(String(records()[0].body!.title)).not.toContain("未命名");

    // and a started agent with none: its folder too
    calls.length = 0;
    render();
    await act(async () => (document.body.querySelector('[data-testid="handoff-new-agent"]') as HTMLButtonElement).click());
    await vi.waitFor(() => expect(openNewAgent).toHaveBeenCalledTimes(1));
    const preset = vi.mocked(openNewAgent).mock.calls[0][0] as NewAgentPreset;
    act(() => preset.onStarted!({ ...started, title: undefined as unknown as string } as Session));
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    expect(records()[0].body!.title).toBe("code");
  });

  it("gives every record its own client_id", async () => {
    await reloadInbox();
    render();
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    render();
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(2));
    expect(records()[1].body!.client_id).not.toBe(records()[0].body!.client_id);
  });

  it("names the item's own manager on the record, as on every request about it", async () => {
    await reloadInbox();
    render(item("m-1"));
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    expect(records()[0].header).toBe("m-1");
  });
});

describe("Hand to AI: a record that fails", () => {
  it("says nothing and still closes the dialog and opens the conversation, for 400, 404 and 405", async () => {
    for (const status of [400, 404, 405]) {
      hub.record = status;
      calls.length = 0;
      await reloadInbox();
      const onClose = vi.fn();
      act(() => root.render(<HandOffDialog item={item()} onClose={onClose} />));
      pick("Persona（新对话）");
      await act(async () => sendButton().click());
      await vi.waitFor(() => expect(records()).toHaveLength(1));
      await flush();
      expect(onClose).toHaveBeenCalled();
      expect(navigate).toHaveBeenCalled();
      expect(document.body.textContent).not.toContain("refused");
    }
  });

  it("says nothing and tries once only for another failure", async () => {
    hub.record = 500;
    await reloadInbox();
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item()} onClose={onClose} />));
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    await flush();
    expect(records()).toHaveLength(1);
    expect(onClose).toHaveBeenCalled();
    expect(document.body.textContent).not.toContain("refused");
  });
});

describe("Hand to AI: nothing records what did not get there", () => {
  it("a new chat the manager refused records nothing", async () => {
    hub.start = 409;
    await reloadInbox();
    render();
    pick("Persona（新对话）");
    await act(async () => sendButton().click());
    await flush();
    expect(calls.some((c) => c.url.endsWith("/assistant/conversations"))).toBe(true);
    expect(records()).toHaveLength(0);
  });

  it("an agent whose message was refused records nothing", async () => {
    await reloadInbox();
    const fetcher = vi.mocked(fetch);
    const answer0 = fetcher.getMockImplementation()!;
    fetcher.mockImplementation(async (url, init) => {
      if (String(url) === "/api/v1/sessions/mac/s1/messages") {
        calls.push({ url: String(url), method: init?.method ?? "GET", header: null, body: undefined });
        return answer(409, { error: "refused" });
      }
      return answer0(url, init);
    });
    render();
    act(() => ([...document.body.querySelectorAll('[role="radio"]')].find((b) => b.textContent?.includes("Release check")) as HTMLButtonElement).click());
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(calls.some((c) => c.url === "/api/v1/sessions/mac/s1/messages")).toBe(true));
    await flush();
    expect(records()).toHaveLength(0);
  });
});

describe("Hand to AI: delivery authority", () => {
  it("records hub acceptance, not the local enqueue", async () => {
    const held = await sendAgentHeld();
    await flush();
    expect(records()).toHaveLength(0);

    held.respond(0, 200);
    await vi.waitFor(() => expect(records()).toHaveLength(1));
    expect(records()[0].body!.session).toEqual({ machine_id: "mac", session_id: "s1" });
  });

  it("records a current flight that already echoed even when its HTTP response later fails", async () => {
    const held = await sendAgentHeld();
    settleMessageEcho("mac", "s1", held.id);
    held.respond(0, 409, { error: "late refusal", delivered: "none" });
    await vi.waitFor(() => expect(records()).toHaveLength(1));
  });

  it("does not record a failure after a same-id retry replaced the flight", async () => {
    const held = await sendAgentHeld();
    // Another view has exposed Retry for this unconfirmed send. The real retry keeps its id and supersedes its flight.
    keepFailed("mac", "s1", { id: held.id, text: held.text, images: [], at: Date.now() }, new TypeError("Failed to fetch"));
    const retried = retryOutbox("mac", "s1", held.id);
    try {
      held.respond(0, 409, { error: "superseded refusal", delivered: "none" });
      await vi.waitFor(() => expect(held.count()).toBe(2));
      expect(messageCalls()[1].body!.client_msg_id).toBe(held.id);
      await flush();
      expect(records()).toHaveLength(0);
    } finally {
      if (held.count() > 1) {
        held.respond(1, 409, { error: "retry refused", delivered: "none" });
        expect(await retried).toBe(false);
      }
    }
  });

  it("does not record a failure after the login identity changed", async () => {
    const held = await sendAgentHeld();
    setPairedToken("synthetic-replacement-token");
    held.respond(0, 409, { error: "retired refusal", delivered: "none" });
    await flush();
    expect(records()).toHaveLength(0);
  });

  it("does not record a cancelled send", async () => {
    const held = await sendAgentHeld();
    held.fail(0, new DOMException("cancelled", "AbortError"));
    await flush();
    expect(records()).toHaveLength(0);
  });

  it("does not record an ordinary send failure", async () => {
    const held = await sendAgentHeld();
    held.respond(0, 500, { error: "send failed", delivered: "none" });
    await flush();
    expect(records()).toHaveLength(0);
  });
});

describe("Hand to AI: the main conversation", () => {
  it("keeps its one hand-off, with the text, and records nothing after it", async () => {
    await reloadInbox();
    const onClose = vi.fn();
    act(() => root.render(<HandOffDialog item={item()} onClose={onClose} />));
    await act(async () => sendButton().click());
    await vi.waitFor(() => expect(sends()).toHaveLength(1));
    await flush();
    expect(sends()).toHaveLength(1);
    expect(sends()[0].body).toHaveProperty("text");
    expect(onClose).toHaveBeenCalled();
  });
});
