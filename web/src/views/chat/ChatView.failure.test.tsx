// @vitest-environment jsdom
// A message that did not go stays in the chat as a failed bubble with why, Retry and Delete, the box empty (#1122). A
// refused send in a Persona conversation also gets a card above the composer (docs/assistant/onboarding.md 4.6); the next
// send that goes through takes it away, so it does not linger over a later setup step.
import { act, useEffect, useReducer } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Outgoing } from "./Composer";
import type { Machine, Session } from "../../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const searchRenders = vi.hoisted(() => ({ count: 0 }));
vi.mock("./ChatSearch", async load => {
  const real = await load<typeof import("./ChatSearch")>();
  return { ...real, ChatSearch: (props: Parameters<typeof real.ChatSearch>[0]) => {
    searchRenders.count++;
    return <real.ChatSearch {...props} />;
  } };
});

const captured = vi.hoisted(() => ({
  onSend: undefined as ((out: Outgoing) => Promise<boolean>) | undefined,
  session: { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" } as Session | undefined,
  composer: undefined as { placeholder?: string; disabled?: boolean; blocked?: { label: string }; onSendNow?: () => Promise<void>; top?: React.ReactNode } | undefined,
  // set: the hub answers the exact-session lookup (useRuntimeSession) and the page is connected
  features: [] as string[],
  // set: the computers the page knows (an Agent's computer away, #10661)
  machines: undefined as Machine[] | undefined,
}));

vi.mock("./Composer", async (importOriginal) => ({
  ...await importOriginal<typeof import("./Composer")>(),
  Composer: ({ onSend, ...props }: NonNullable<typeof captured.composer> & { onSend: (out: Outgoing) => Promise<boolean> }) => {
    captured.onSend = onSend;
    captured.composer = props;
    return props.top;
  },
}));
// one transcript for every render: a new one each time would re-run the sending bubble's echo check forever
const transcript = vi.hoisted(() => ({ items: [] as unknown[], loading: false, firstRead: true, error: "", hasMore: false, streaming: "", reload: vi.fn() }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    getState: () => (captured.features.length ? { ...store.getState(), features: captured.features } : store.getState()),
    // drawn again on every change, as the real one is (a failed message is kept in the store's outbox)
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) => {
      const [, changed] = useReducer((n: number) => n + 1, 0);
      useEffect(() => store.subscribe(changed), []);
      return select({ ...store.getState(), sessions: { hub: captured.session ? [captured.session] : [] }, ...(captured.features.length ? { features: captured.features, conn: "online" as const } : {}),
        ...(captured.machines ? { machines: captured.machines } : {}) });
    },
  };
});
vi.mock("../../lib/panel", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/panel")>(),
  usePanel: () => null,
}));
vi.mock("../../lib/router", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/router")>(),
  useRoute: () => ({ name: "session", mid: "hub", id: "c1" }),
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }) }));
vi.mock("../assistant/AssistantChat", () => ({
  AssistantFace: () => null,
  CompactionStrip: () => null,
  useAssistantMenu: () => ({ items: [], dialogs: null }),
}));
vi.mock("../assistant/Tasks", () => ({
  DayLine: () => null,
  FeedLineView: () => null,
  QuickResultCard: () => null,
  RouteChip: () => null,
  AssistantActionBar: () => null,
  TaskDock: () => null,
  TaskQuestions: () => null,
}));

// the wait for an ultracode change to go through (#3428): the test says when
const ultra = vi.hoisted(() => ({ release: undefined as ((through: boolean) => void) | undefined }));
vi.mock("../../lib/ultracode", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/ultracode")>(),
  ultracodeQueueDone: () => new Promise<boolean>((resolve) => (ultra.release = resolve)),
}));

// The kept message's status line, Retry and Delete are a chunk of their own (#10357): held back until a test lets it in,
// to see what is drawn meanwhile.
const keptChunk = vi.hoisted(() => {
  let open!: () => void;
  const gate = new Promise<void>((resolve) => (open = resolve));
  return { gate, open };
});
vi.mock("./UndeliveredBubbles", async (importOriginal) => {
  await keptChunk.gate;
  return importOriginal<typeof import("./UndeliveredBubbles")>();
});
// lets the chunk in and waits until it is loaded, so the next flush draws it
const letKeptChunkIn = async () => {
  keptChunk.open();
  await import("./UndeliveredBubbles");
};

// what Delete's toast offers (#4626): no toast stack is drawn here
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../ui/atoms")>(),
  toast: vi.fn(),
}));

const { ChatView } = await import("./ChatView");
const { toast } = await import("../../ui/atoms");
const { t } = await import("../../i18n");
const { applyCompleteSessions, getSession, getState, onHubEvent, send, setOutbox, subscribe } = await import("../../lib/store");
const { setRoute } = await import("../../lib/feed");
const { keepOutgoing, outboxKey, resetOutboxClaimsForTests, resetRestoredForTests, SENT_GRACE_MS } = await import("../../lib/outbox");
const { configureMessageFeedback, receiveMessageSend, resetMessageFeedback } = await import("../../lib/message-send");
configureMessageFeedback({ getSession, send, onHubEvent, subscribe, receiptsAllowed: () => true }); // injected trusted transport
const { SESSIONS_LOOKUP_FEATURE } = await import("../../lib/runtime-list");
// ChatView's own React.lazy notice must be ready before the missing-chat assertions, not only lazyPage's registry.
await import("./NotFoundNotice");
const { preloadQueuePanel } = await import("./QueueStrip");
await preloadQueuePanel();
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
let reply: () => Response;
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};

beforeEach(() => {
  searchRenders.count = 0;
  localStorage.clear();
  resetMessageFeedback();
  resetRestoredForTests();
  transcript.reload.mockClear();
  for (const key of Object.keys(getState().outbox)) setOutbox(key, []);
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(String(input).endsWith("/messages") ? reply() : json({}))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the deferred search dialog", () => {
  it.each([false, true])("keeps cold chat unchanged and opens the existing search dialog on intent (assistant=%s)", async assistant => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
    expect(searchRenders.count).toBe(0);
    expect(document.querySelector("[data-chat-search]")).toBeNull();
    const more = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!;
    expect(more).toBeTruthy(); act(() => more.click()); await flush();
    const search = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(item => item.textContent === t("chat-chatview.search_menu"))!;
    expect(search).toBeTruthy();
    await act(async () => { search.click(); await import("./ChatSearch"); }); await flush();
    expect(searchRenders.count).toBeGreaterThan(0);
    expect(document.querySelector('[role="dialog"] [data-chat-search]')).not.toBeNull();
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain(t("chat-chatsearch.title"));
  });
  it("opens the search, picks a hit and keeps the return label in the primary namespace", async () => {
    const previous = transcript.items;
    transcript.items = [{ id: "search-user", seq: 1, role: "user", kind: "text", text: "Synthetic search target", ts: "2026-11-12T09:00:00Z" }];
    vi.stubGlobal("CSS", { ...globalThis.CSS, escape: (value: string) => value });
    try {
      captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
      await act(async () => root.render(<ChatView mid="hub" sid="c1" />)); await flush();
      const more = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("chat-chatview.more_actions")}"]`)!;
      await act(async () => more.click()); await flush();
      const search = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].find(item => item.textContent === t("chat-chatview.search_menu"))!;
      await act(async () => { search.click(); await import("./ChatSearch"); }); await flush();
      const input = document.querySelector<HTMLInputElement>("[data-chat-search] input")!;
      await act(async () => {
        Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Synthetic");
        input.dispatchEvent(new Event("input", { bubbles: true }));
      });
      await act(async () => { await new Promise(resolve => setTimeout(resolve, 250)); }); await flush();
      const hit = [...document.querySelectorAll<HTMLButtonElement>("[data-chat-search] button")].find(button => button.textContent?.includes("Synthetic search target"))!;
      expect(hit).toBeTruthy(); await act(async () => hit.click()); await flush();
      const back = host.querySelector<HTMLButtonElement>("[data-search-return]")!;
      expect(back.textContent).toBe(t("chat-chatview.search_return"));
      expect(document.querySelector("[data-chat-search]")).toBeNull();
      await act(async () => back.click()); await flush();
      expect(host.querySelector("[data-search-return]")).toBeNull();
    } finally { transcript.items = previous; }
  });
});

describe("the header's title", () => {
  it("calls a leader conversation by its direction, as its sidebar row does, not by the hub's raw title", async () => {
    const { applyLeadersEvent, resetDirectionNames } = await import("../../lib/direction-names");
    captured.session = { machine_id: "hub", session_id: "lead1", title: "1109 | 探索 | 你是「增长」方向的主管", cwd: "/code", state: "idle", status: "idle", assistant: true } as Session;
    try {
      act(() => root.render(<ChatView mid="hub" sid="lead1" assistant />)); await flush();
      expect(host.querySelector("[data-chat-title]")?.textContent).toBe("1109 | 探索 | 你是「增长」方向的主管"); // no names yet
      act(() => host.querySelector("[data-chat-title]")!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
      expect(host.querySelector("[data-chat-title-input]")).not.toBeNull(); // renamable in place while not a direction
      act(() => host.querySelector("[data-chat-title-input]")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true })));
      expect(host.querySelector("[data-chat-title-input]")).toBeNull();
      act(() => void applyLeadersEvent([{ id: "lead1", name: "增长" }])); await flush();
      expect(host.querySelector("[data-chat-title]")?.textContent).toBe("增长");
      // the direction's name is not the conversation's title: no in-place rename of it (⋯ Rename, as on the sidebar row)
      act(() => host.querySelector("[data-chat-title]")!.dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
      expect(host.querySelector("[data-chat-title-input]")).toBeNull();
      // an Agent's page of the same conversation says it the same way
      act(() => root.render(<ChatView mid="hub" sid="lead1" />)); await flush();
      expect(host.querySelector("[data-chat-title]")?.textContent).toBe("增长");
    } finally {
      resetDirectionNames();
    }
  });

  it("reads a conversation titled by its <persona-context> tag", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: '<persona-context kind="context" title="飞书 · 发布群 &quot;周五&quot;" url="">', cwd: "/code", state: "idle", status: "idle" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
    expect(host.querySelector("[data-chat-title]")?.textContent).toBe('飞书 · 发布群 "周五"');
  });
});

describe("message feedback before runtime execution", () => {
  it.each([false, true])("shows the receipt through delayed HTTP without manufacturing execution (assistant=%s)", async (assistant) => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    let accept!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).endsWith("/messages") ? new Promise<Response>(resolve => { accept = resolve; }) : Promise.resolve(json({}))));
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "Synthetic receipt input", images: [] }); }); await flush();
    // local Sending at once, on the chat's status line (its words here are the offline test page's "waiting for the hub")
    expect(host.querySelector('[data-message-feedback="sending"]')).not.toBeNull();
    const call = vi.mocked(fetch).mock.calls.find(([input]) => String(input).endsWith("/messages"))!;
    const id = JSON.parse(String(call[1]?.body)).client_msg_id;
    act(() => receiveMessageSend({ machine_id: "hub", session_id: "c1", client_msg_id: id, state: "processing", at: "2026-11-12T10:00:00Z" }));
    expect(host.querySelector('[data-message-feedback="processing"]')).not.toBeNull();
    expect(captured.session.state).toBe("idle");
    expect(host.querySelector('[data-running-status="on"]')).toBeNull();
    await act(async () => { accept(json({})); await sent; }); await flush();
    expect(host.querySelector('[data-message-feedback="processing"]')).not.toBeNull();
    expect(captured.session.state).toBe("idle");
  });
  it.each([false, true])("says Sending, then Processing, once: on the chat's status line, not under the bubble as well (assistant=%s)", async (assistant) => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    let accept!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).endsWith("/messages") ? new Promise<Response>(resolve => { accept = resolve; }) : Promise.resolve(json({}))));
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "two lines\nof input", images: [] }); }); await flush();
    const said = (words: string) => [...host.querySelectorAll("*")].filter((el) => el.childElementCount === 0 && el.textContent === words).length;
    // the status line says it (here "waiting for the hub", the test page being offline); the bubble only dims
    expect(host.querySelectorAll("[data-message-feedback]")).toHaveLength(1);
    expect(host.querySelector("[data-user-bubble]")?.textContent).not.toContain(t("chat-segmentview.sending"));
    const call = vi.mocked(fetch).mock.calls.find(([input]) => String(input).endsWith("/messages"))!;
    act(() => receiveMessageSend({ machine_id: "hub", session_id: "c1", client_msg_id: JSON.parse(String(call[1]?.body)).client_msg_id, state: "processing", at: "2026-11-12T10:00:00Z" }));
    expect(said(t("chat-segmentview.processing"))).toBe(1);
    expect(host.querySelector("[data-message-feedback]")?.textContent).toContain(t("chat-segmentview.processing"));
    await act(async () => { accept(json({})); await sent; }); await flush();
  });
  it.each(["working", "done"] as const)("does not revive Processing when %s precedes the final HTTP without an echo", async (result) => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    applyCompleteSessions([captured.session]);
    let accept!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).endsWith("/messages") ? new Promise<Response>(resolve => { accept = resolve; }) : Promise.resolve(json({}))));
    act(() => root.render(<ChatView mid="hub" sid="c1" />)); await flush();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "Legacy input without echo", images: [] }); }); await flush();
    captured.session = { ...captured.session, ...(result === "working" ? { state: "working", state_since: "2026-11-12T10:00:01Z" } : { work: { phase: "done" } }) } as Session;
    act(() => applyCompleteSessions([captured.session!])); await flush();
    await act(async () => { accept(json({})); await sent; }); await flush();
    expect(host.querySelector('[data-message-feedback="processing"]')).toBeNull();
    expect(captured.session.state).toBe(result === "working" ? "working" : "idle");
  });
  it("shows a peer receipt without a local outbox and clears a peer failure", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" />)); await flush();
    act(() => receiveMessageSend({ machine_id: "hub", session_id: "c1", client_msg_id: "peer-input", state: "processing", at: "2026-11-12T10:00:00Z" }));
    expect(host.querySelector('[data-message-feedback="processing"]')).not.toBeNull();
    expect(host.querySelector("[data-outbox]")).toBeNull();
    act(() => receiveMessageSend({ machine_id: "hub", session_id: "c1", client_msg_id: "peer-input", state: "failed", at: "2026-11-12T10:00:01Z" }));
    expect(host.querySelector("[data-message-feedback]")).toBeNull();
  });
});

describe("the working chat's outgoing queue", () => {
  const working = { machine_id: "hub", session_id: "c1", agent: "claude", state: "working", status: "working" } as Session;
  const photos = ["iVBORw0KGgo=", "aW1hZ2U="].map((data) => ({ mime: "image/png", data }));
  const rows = () => [...host.querySelectorAll("[data-queue-panel] [data-queue-row]")];
  const sending = (row: Element) => row.querySelector('[role="status"]');
  const previews = (row: Element) => [...row.querySelectorAll("[data-queue-photo-thumb]")].map((img) => img.getAttribute("src"));
  const preview = (i: number) => `data:image/png;base64,${photos[i].data}`;
  const bodies: { text: string; client_msg_id: string }[] = [];
  const replies: ((r: Response) => void)[] = [];
  const editQueue = vi.fn<(keep: number[]) => Response | Promise<Response>>();
  let previousSession: Session | undefined;
  const pushQueue = async (queued: string[]) => {
    captured.session = { ...captured.session!, queued };
    act(() => applyCompleteSessions([captured.session!])); await flush();
  };
  const open = async (session = working, assistant = false) => {
    captured.session = session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
  };
  const sendFromBox = (out: Outgoing) => {
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!(out); });
    return sent;
  };
  const finish = async (i: number, sent: Promise<boolean>, response = json({})) => {
    await act(async () => { replies[i](response); await sent; }); await flush();
  };

  beforeEach(() => {
    previousSession = captured.session;
    bodies.length = 0;
    replies.length = 0;
    editQueue.mockReset();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/messages")) {
        bodies.push(JSON.parse(String(init?.body)));
        return new Promise<Response>((resolve) => replies.push(resolve));
      }
      if (String(input).endsWith("/queue")) return Promise.resolve(editQueue(JSON.parse(String(init?.body)).keep));
      return Promise.resolve(json({}));
    }));
  });
  afterEach(() => {
    captured.session = previousSession;
  });

  it.each(["push", "HTTP"] as const)("keeps one row when the %s arrives first", async (first) => {
    await open();
    const text = "Check the screenshot";
    const sent = sendFromBox({ text, images: [] });
    await flush();
    const [row] = rows();
    expect(row).toBeDefined();
    expect(row.textContent).toContain(text);
    expect(sending(row)?.textContent).toBe(t("chat-segmentview.sending"));
    expect(host.querySelector('[data-user-bubble]')).toBeNull();
    expect(host.querySelector('[data-message-feedback]')).toBeNull();
    act(() => receiveMessageSend({ machine_id: "hub", session_id: "c1", client_msg_id: bodies[0].client_msg_id, state: "processing", at: "2026-11-12T10:00:00Z" }));
    expect(sending(row)?.textContent).toBe(t("chat-segmentview.processing"));

    if (first === "HTTP") {
      await finish(0, sent);
      expect(rows()).toEqual([row]);
      expect(sending(row)).not.toBeNull();
    }
    await pushQueue([text]);
    expect(rows()).toEqual([row]);
    expect(sending(row)).toBeNull();
    expect(host.querySelector('[data-user-bubble]')).toBeNull();
    if (first === "push") await finish(0, sent);
    await pushQueue([]);
    expect(rows()).toEqual([]);
    expect(host.querySelector('[data-user-bubble]')).toBeNull();
  });

  it("reveals a new send past the queue's fold and keeps it visible after confirmation", async () => {
    const queued = ["First", "Second", "Third", "Fourth", "Fifth"];
    await open({ ...working, queued });
    expect(rows()).toHaveLength(3);
    const sent = sendFromBox({ text: "Latest follow-up", images: [] });
    await flush();
    const row = rows().at(-1)!;
    expect(rows()).toHaveLength(6);
    expect(row.textContent).toContain("Latest follow-up");
    expect(sending(row)).not.toBeNull();
    await pushQueue([...queued, "Latest follow-up"]);
    expect(rows().at(-1)).toBe(row);
    expect(sending(row)).toBeNull();
    await finish(0, sent);
  });

  const savedPhoto = (name: string) => `\n<attached_images>\npath: /tmp/${name}.png\n</attached_images>`;
  const attached = '<persona-context kind="context" title="Excerpt">\nEarlier words\n</persona-context>\n> Quoted words\n<persona-file path="/tmp/notes.txt" kind="file" />';
  it.each([
    ["photo with text", "Please check this screen", "[Image #1]\n\nPlease check this screen" + savedPhoto("screen")],
    ["photo only with a path", "", "[Image #1]" + savedPhoto("screen")],
    ["photo only with stand-in words", "", "[Image #1] Please look at the image" + savedPhoto("screen")],
    ["photo only with no paths in the queue", "", ""],
    ["photo, file, quote and context", attached, "[Image #1]\n" + attached + savedPhoto("screen")],
  ])("keeps the local preview and one row for a %s", async (_, text, raw) => {
    await open();
    const sent = sendFromBox({ text, images: [photos[0]] });
    await flush();
    const [row] = rows();
    expect(previews(row)).toEqual([preview(0)]);
    const thumb = row.querySelector("[data-queue-photo-thumb]");
    if (text === attached) expect(row.querySelectorAll('[data-queue-mark="file"], [data-queue-mark="context"]')).toHaveLength(2);
    await pushQueue([raw]);
    expect(rows()).toEqual([row]);
    expect(sending(row)).toBeNull();
    expect(row.querySelector("[data-queue-photo-thumb]")).toBe(thumb);
    expect(previews(row)).toEqual([preview(0)]);
    expect(host.querySelector("[data-user-bubble]")).toBeNull();
    await finish(0, sent);
  });

  it("does not borrow the local preview for queued entries with other attachments", async () => {
    await open();
    const sent = sendFromBox({ text: attached, images: [photos[0]] });
    await flush();
    const [local] = rows();
    const other = "[Image #1]\n" + attached.replace("notes.txt", "other.txt") + savedPhoto("other");
    await pushQueue([other]);
    expect(rows()).toHaveLength(2);
    expect(rows()[1]).toBe(local);
    expect(sending(local)).not.toBeNull();
    const actual = "[Image #1]\n" + attached + savedPhoto("screen");
    await pushQueue([other, actual]);
    expect(rows()).toHaveLength(2);
    expect(rows()[1]).toBe(local);
    expect(sending(local)).toBeNull();
    expect(previews(local)).toEqual([preview(0)]);
    await finish(0, sent);
  });

  it.each(["", "Same words"])("pairs one occurrence at a time for rapid identical sends (%j) and keeps the surviving photo", async (text) => {
    await open({ ...working, queued: [text, text], queued_relayed: 1 });
    const first = sendFromBox({ text, images: [photos[0]] });
    const second = sendFromBox({ text, images: [photos[1]] });
    await flush();
    const [already, a, b] = rows();
    expect(rows()).toHaveLength(3);
    expect(bodies).toHaveLength(1);
    expect(previews(already)).toEqual([]);
    expect(previews(a)).toEqual([preview(0)]);
    expect(previews(b)).toEqual([preview(1)]);
    await pushQueue([text, text, text]);
    expect(rows()).toEqual([already, a, b]);
    expect(sending(a)).toBeNull();
    expect(sending(b)).not.toBeNull();
    await finish(0, first);
    await pushQueue([text, text, text, text]);
    await finish(1, second);
    expect(rows()).toEqual([already, a, b]);
    expect(sending(b)).toBeNull();

    let editReply!: (r: Response) => void;
    editQueue.mockImplementation(() => new Promise((resolve) => { editReply = resolve; }));
    act(() => a.querySelector<HTMLButtonElement>("[data-queue-delete]")!.click()); await flush();
    expect(editQueue).toHaveBeenCalledWith([0, 2]);
    await pushQueue([text, text, text]);
    await act(async () => editReply(json({ queue: [text, text, text] }))); await flush();
    expect(rows()).toEqual([already, b]);
    expect(previews(b)).toEqual([preview(1)]);
    expect(host.querySelector("[data-user-bubble]")).toBeNull();
  });

  it("pulls a local follow-up back to the draft before its POST starts", async () => {
    await open();
    const { onDraftEvent } = await import("../../lib/drafts");
    const restore = vi.fn();
    const off = onDraftEvent("hub/c1", restore);
    const first = sendFromBox({ text: "First", images: [] });
    const second = sendFromBox({ text: "Pull this back", images: [photos[0]] });
    try {
      await flush();
      act(() => rows()[1].querySelector<HTMLButtonElement>("[data-queue-edit]")!.click()); await flush();
      expect(rows()).toHaveLength(1);
      expect(restore.mock.calls.map(([event]) => event)).toEqual([
        { type: "append", text: "Pull this back" },
        { type: "image", image: { name: "photo-1.png", ...photos[0] } },
      ]);
      await finish(0, first);
      await act(async () => { await second; });
      expect(bodies.map((b) => b.text)).toEqual(["First"]);
      expect(editQueue).not.toHaveBeenCalled();
    } finally {
      off();
    }
  });

  it("honors Delete pressed during a POST once the queue push arrives, even if the HTTP reply fails late", async () => {
    await open();
    editQueue.mockReturnValue(json({ queue: ["Remove this"] }));
    const sent = sendFromBox({ text: "Remove this", images: [] });
    await flush();
    act(() => rows()[0].querySelector<HTMLButtonElement>("[data-queue-delete]")!.click()); await flush();
    expect(editQueue).not.toHaveBeenCalled();
    await pushQueue(["Remove this"]);
    await finish(0, sent, json({ error: "Late refusal" }, 400));
    expect(editQueue).toHaveBeenCalledExactlyOnceWith([]);
    await pushQueue([]);
    expect(rows()).toEqual([]);
    expect(host.querySelector("[data-user-bubble], [data-outbox]")).toBeNull();
    expect(localStorage.getItem(`outbox-msg:hub/c1/${bodies[0].client_msg_id}`)).toBeNull();
  });

  it.each(["idle", "send now", "Codex steer"])("keeps direct sends in the transcript (%s)", async (mode) => {
    await open(mode === "idle" ? { ...working, state: "idle" } : mode === "Codex steer" ? { ...working, agent: "codex" } : working);
    const sent = sendFromBox({ text: "Direct follow-up", images: [], sendNow: mode === "send now" });
    await flush();
    expect(host.querySelector('[data-user-bubble][data-send-state="sending"]')?.textContent).toContain("Direct follow-up");
    expect(rows()).toEqual([]);
    await finish(0, sent);
  });

  it("hands a refused send back to the failed outbox with its payload", async () => {
    await open();
    const sent = sendFromBox({ text: "Follow-up while offline", images: [photos[0]] });
    await flush();
    expect(rows()).toHaveLength(1);
    await finish(0, sent, json({ delivered: "none", error: "Computer offline" }, 503));
    expect(rows()).toEqual([]);
    expect(host.querySelector('[data-outbox] [data-send-state="failed"]')?.textContent).toContain("Follow-up while offline");
    expect(getState().outbox["hub/c1"]).toMatchObject([{ images: [photos[0]], state: "failed" }]);
  });

  it("hands a kept follow-up to the session's undelivered copy", async () => {
    await open({ ...working, assistant: true }, true);
    const text = "Try this when it comes back";
    const sent = sendFromBox({ text, images: [] });
    await flush();
    expect(rows()).toHaveLength(1);
    const kept = { id: bodies[0].client_msg_id, text, at: "2026-11-17T10:00:00Z", state: "waiting" as const, attempts: 1 };
    await finish(0, sent, json({ delivered: "later", undelivered: kept }, 202));
    await act(async () => {
      captured.session = { ...captured.session!, undelivered: [kept] };
      applyCompleteSessions([captured.session]);
    }); await flush();
    expect(rows()).toEqual([]);
    expect(host.querySelector("[data-outbox]")).toBeNull();
    expect(host.querySelector('[data-user-bubble][data-send-state="failed"]')?.textContent).toContain(text);
  });
});

describe("a refused send's card", () => {
  it("is said once, by the failed bubble with its one Retry, when the send failed with nothing to set up (the hub away, HTTP 500)", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    reply = () => json({}, 500);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    expect(host.querySelector('[data-outbox] [data-user-bubble][data-send-state="failed"]')).not.toBeNull();
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("HTTP 500");
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull();
    const retries = [...host.querySelectorAll("button")].filter((b) => b.textContent === t("chat-outboxbubbles.retry"));
    expect(retries).toHaveLength(1);
  });

  it("keeps a coded refusal's card for what to set up, with no second Retry beside the bubble's", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    reply = () => json({ error: "模型没接好", code: "model_missing", action: "connect_model" }, 409);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    const card = host.querySelector('[data-testid="readiness-failure"]');
    expect(card?.textContent).toContain("模型没接好");
    // its button is the setup step; its old "重新发送" went: the bubble's 重试 is the one way to send again
    expect([...card!.querySelectorAll("button")].map((b) => b.textContent)).not.toContain("重新发送");
    expect(card?.textContent).not.toContain(t("chat-outboxbubbles.retry"));
    expect([...host.querySelectorAll("[data-outbox] button")].filter((b) => b.textContent === t("chat-outboxbubbles.retry"))).toHaveLength(1);
  });

  it("goes away once a later send goes through", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    reply = () => json({ error: "模型没接好", code: "model_missing", action: "connect_model" }, 409);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    expect(host.querySelector('[data-testid="readiness-failure"]')).not.toBeNull();
    reply = () => json({});
    await act(async () => void (await captured.onSend!({ text: "再来", images: [] })));
    await flush();
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull();
  });
});

describe("a message that did not go, in an agent's chat (#1122)", () => {
  const failed = () => host.querySelector('[data-user-bubble][data-send-state="failed"]');
  const button = (label: string) => Array.from(host.querySelectorAll("[data-outbox] button")).find((b) => b.textContent === label) as HTMLButtonElement | undefined;
  let bodies: { client_msg_id: string; text: string }[];
  beforeEach(() => {
    bodies = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(reply());
    }));
  });

  it("stays in the chat as a failed bubble with why, Retry and Delete, and the box stays empty", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    let kept: boolean | undefined;
    await act(async () => void (kept = await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    expect(kept).toBe(true); // the composer does not put the words back
    expect(failed()?.textContent).toContain("你好");
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("发送失败：会话已结束");
    expect(button("重试")).toBeDefined();
    expect(button("删除")).toBeDefined();
    expect(localStorage.getItem("draft:hub/c1")).toBeNull();
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull(); // the bubble says why: no card over the box
  });

  it("a Codex writer conflict keeps the follow-up and its recovery with Retry and Delete", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "请在原 Codex 窗口继续，这次没有发送", code: "codex_writer_busy", delivered: "none" }, 409);
    await act(async () => void (await captured.onSend!({ text: "继续上一次修改", images: [] })));
    await flush();
    expect(failed()?.textContent).toContain("继续上一次修改");
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("请在原 Codex 窗口继续，这次没有发送");
    expect(button("重试")).toBeDefined();
    expect(button("删除")).toBeDefined();
    expect(getState().outbox["hub/c1"]?.[0]).toMatchObject({ text: "继续上一次修改", state: "failed" });
  });

  it("Retry sends it again, under a new id (the hub keeps its answer to the first), and the bubble waits for the transcript", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    reply = () => json({});
    await act(async () => button("重试")!.click());
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["你好", "你好"]);
    expect(bodies[1].client_msg_id).not.toBe(bodies[0].client_msg_id);
    expect(failed()).toBeNull();
    expect(host.querySelector("[data-outbox] [data-user-bubble]")?.getAttribute("data-send-state")).toBe("sent");
  });

  it("Delete takes it away", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    act(() => button("删除")!.click());
    await flush();
    expect(host.querySelector("[data-outbox]")).toBeNull();
    expect(bodies).toHaveLength(1);
  });

  it("its message menu ends in Delete too, which sends nothing and offers Undo (#4626)", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json({ error: "会话已结束" }, 400);
    await act(async () => void (await captured.onSend!({ text: "你好", images: [] })));
    await flush();
    const words = [...host.querySelectorAll<HTMLElement>("[data-outbox] [data-user-bubble] *")].filter((el) => el.textContent === "你好").at(-1)!;
    // The message menu is a lazy chunk since #8714: wait for it, as MessagePick.test.tsx does.
    await act(async () => {
      words.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 20, clientY: 20 }));
      await import("./ReplyBar");
      await vi.dynamicImportSettled();
    });
    const last = [...document.querySelectorAll<HTMLElement>('[role="menuitem"]')].at(-1)!;
    expect(last.textContent?.trim()).toBe("删除");
    act(() => last.click());
    await flush();
    expect(host.querySelector("[data-outbox]")).toBeNull();
    expect(bodies).toHaveLength(1);
    expect(toast).toHaveBeenLastCalledWith("消息已删除", "ok", expect.objectContaining({ label: "撤销" }));
    act(() => vi.mocked(toast).mock.lastCall![2]!.run());
    await flush();
    expect(failed()?.textContent).toContain("你好");
    expect(bodies).toHaveLength(1);
  });

  it("a send that never went (nothing shows it) gives the words back", async () => {
    captured.session = { ...captured.session, status: "offline", source_kind: "codex_history" } as Session;
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      let went: boolean | undefined;
      await act(async () => void (went = await captured.onSend!({ text: "你好", images: [] })));
      expect(went).toBe(false);
      expect(host.querySelector("[data-outbox]")).toBeNull();
    } finally {
      captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" } as Session;
    }
  });
});

describe("a send whose answer came back in doubt (the hub stopped waiting, the message went in)", () => {
  const failed = () => host.querySelector('[data-user-bubble][data-send-state="failed"]');
  const action = (label: string) => Array.from(host.querySelectorAll("[data-outbox] button")).find((b) => b.textContent === label) as HTMLButtonElement | undefined;
  let bodies: { client_msg_id: string; text: string }[];
  let answer!: (r: Response) => void;
  beforeEach(() => {
    bodies = [];
    transcript.items = [];
    resetOutboxClaimsForTests();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return new Promise<Response>((resolve) => (answer = resolve));
    }));
  });
  afterEach(() => {
    transcript.items = [];
  });
  const inDoubt = () => json({ error: "The message may have been sent: the computer did not answer in time.", delivered: "unknown" }, 502);

  it("offers read-only Check instead of Retry; an inconclusive answer sends nothing", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "只核对，不重发", images: [] }); });
    await flush();
    answer(inDoubt());
    await act(async () => void (await sending));
    await flush();
    const check = action(t("chat-outboxbubbles.check"));
    expect(check).toBeDefined();
    expect(action(t("chat-outboxbubbles.retry"))).toBeUndefined();
    expect(action(t("chat-outboxbubbles.delete"))).toBeDefined();

    await act(async () => check!.click());
    await flush();
    const id = bodies[0].client_msg_id;
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).endsWith(`/ops/${id}`))).toBe(true);
    expect(transcript.reload).toHaveBeenCalledOnce();
    expect(bodies).toHaveLength(1);
    expect(action(t("chat-outboxbubbles.check"))).toBeDefined();
    expect(action(t("chat-outboxbubbles.retry"))).toBeUndefined();
  });

  it("a late Check answer settles only its original bubble, never the next chat or its draft", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "原会话的消息", images: [] }); });
    await flush();
    answer(inDoubt());
    await act(async () => void (await sending));
    await flush();
    const id = bodies[0].client_msg_id;
    let finish!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).endsWith(`/ops/${id}`)
      ? new Promise<Response>((resolve) => { finish = resolve; })
      : Promise.resolve(json({}))));
    await act(async () => action(t("chat-outboxbubbles.check"))!.click());

    localStorage.setItem("draft:hub/c2", "新会话的草稿");
    act(() => root.render(<ChatView mid="hub" sid="c2" />));
    await act(async () => {
      finish(json({ id, kind: "message", target: { path: "/api/v1/sessions/hub/c1/messages" }, state: "done" }));
      await Promise.resolve();
    });
    await flush();
    expect(getState().outbox["hub/c1"]?.[0]).toMatchObject({ id, state: "sent" });
    expect(getState().outbox["hub/c2"]).toBeUndefined();
    expect(localStorage.getItem("draft:hub/c2")).toBe("新会话的草稿");
    expect(transcript.reload).not.toHaveBeenCalled();
    expect(bodies).toHaveLength(1);
  });

  it("shows as sent once its line reaches the transcript, without the hub tying it, so nothing asks to send it again", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "前端页面上也有多次显示发送失败", images: [] }); });
    await flush();
    answer(inDoubt());
    await act(async () => void (await sending));
    await flush();
    expect(failed()?.textContent).toContain("前端页面上也有多次显示发送失败"); // in doubt, until the transcript says

    transcript.items = [{ id: "line-1", seq: 1, role: "user", kind: "text", text: bodies[0].text, ts: new Date().toISOString() }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(failed()).toBeNull();
    const bubbles = host.querySelectorAll("[data-user-bubble]");
    expect(bubbles).toHaveLength(1);
    expect(bubbles[0].getAttribute("data-send-state")).not.toBe("failed");
    expect(bodies).toHaveLength(1);
  });

  it("shows as sent when its line arrived before the answer did", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "可以，前端页面上也有", images: [] }); });
    await flush();
    transcript.items = [{ id: "line-2", seq: 1, role: "user", kind: "text", text: bodies[0].text, ts: new Date().toISOString() }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();

    answer(inDoubt());
    await act(async () => void (await sending));
    await flush();
    expect(failed()).toBeNull();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
  });

  // A Claude desktop session in bypass permissions mode holds Persona's message for approval on the computer: the node
  // says so (peer_held, delivered unknown). The bubble said "Not sent: not sure it arrived, check this message", and Check
  // changed nothing, so the owner could not tell what had happened or what to do.
  it("a message held for approval on the computer says so, is no failure, and Check says it is still waiting", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "等电脑上批准的消息", images: [] }); });
    await flush();
    answer(json({ code: "peer_held", error: "held there", detail: "the node's words", delivered: "unknown" }, 409));
    await act(async () => void (await sending));
    await flush();
    const line = host.querySelector("[data-outbox] [data-in-doubt]");
    expect(line?.getAttribute("data-in-doubt")).toBe("held");
    expect(line?.getAttribute("role")).toBe("status");
    expect(line?.textContent).toContain(t("lib-outbox.held_on_computer"));
    expect(line?.textContent).not.toContain(t("chat-outboxbubbles.failed", { error: "" }));
    expect(line?.textContent).not.toContain(t("lib-outbox.delivery_unknown"));
    expect(action(t("chat-outboxbubbles.retry"))).toBeUndefined();

    vi.mocked(toast).mockClear();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("/ops/")
      ? Promise.resolve(json({ id: bodies[0].client_msg_id, kind: "message", target: { path: "/api/v1/sessions/hub/c1/messages" }, state: "unknown" }))
      : Promise.resolve(json({}))));
    await act(async () => action(t("chat-outboxbubbles.check"))!.click());
    await flush();
    expect(toast).toHaveBeenCalledWith(t("lib-outbox.still_held"), "info");
    expect(bodies).toHaveLength(1);

    // approved there: its line reaches the transcript and the bubble goes, nothing sent again
    transcript.items = [{ id: "line-held", seq: 1, role: "user", kind: "text", text: bodies[0].text, ts: new Date().toISOString() }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(failed()).toBeNull();
    expect(bodies).toHaveLength(1);
  });

  it("an unconfirmed peer send names the computer, and a bubble kept with the generic words says it in the code's", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "没确认收下的消息", images: [] }); });
    await flush();
    answer(json({ code: "peer_unconfirmed", error: "not taken", delivered: "unknown" }, 409));
    await act(async () => void (await sending));
    await flush();
    const line = () => host.querySelector("[data-outbox] [data-in-doubt]");
    expect(line()?.getAttribute("data-in-doubt")).toBe("unknown");
    expect(line()?.textContent).toContain(t("lib-outbox.peer_unconfirmed"));
    // an entry an older page kept with the generic words (before this fix) shows the code's words too
    const kept = getState().outbox["hub/c1"]!;
    act(() => setOutbox("hub/c1", kept.map((e) => ({ ...e, error: t("lib-outbox.delivery_unknown") }))));
    await flush();
    expect(line()?.textContent).toContain(t("lib-outbox.peer_unconfirmed"));
  });

  // The screenshot case (#9025 follow-up): a busy Claude whose Persona plugin is from before busy delivery got the
  // message through its inbox, which did not take it. The bubble says why and what fixes it, across a reload too.
  it("an unconfirmed relay to a session with an older Persona plugin says to reopen it on the computer", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "可以派 subagent 分头处理", images: [] }); });
    await flush();
    answer(json({ code: "peer_unconfirmed", error: "not taken", delivered: "unknown", delivered_as: "relayed", relay_reason: "plugin_outdated" }, 409));
    await act(async () => void (await sending));
    await flush();
    const line = () => host.querySelector("[data-outbox] [data-in-doubt]");
    expect(line()?.getAttribute("data-in-doubt")).toBe("unknown");
    expect(line()?.textContent).toContain(t("lib-outbox.peer_unconfirmed_old_plugin"));
    expect(getState().outbox["hub/c1"]![0].relay).toBe("plugin_outdated");
  });

  it("a refusal stays failed even with the same words in the chat", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "继续", images: [] }); });
    await flush();
    answer(json({ error: "会话已结束", delivered: "none" }, 400));
    await act(async () => void (await sending));
    await flush();
    transcript.items = [{ id: "line-3", seq: 1, role: "user", kind: "text", text: "继续", ts: new Date().toISOString() }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(failed()?.textContent).toContain("继续");
  });
});

describe("a message the node held back for an ultracode change (#3428)", () => {
  const HELD = { code: "ultracode_pending", error: "ultracode 等这一轮结束后生效，请稍后再发送消息" };
  let bodies: { client_msg_id: string; text: string }[];
  beforeEach(() => {
    bodies = [];
    ultra.release = undefined;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(reply());
    }));
  });

  it("waits in its bubble saying so, leaves the box free, and goes again with a new id once the change is through", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json(HELD, 409);
    let went: boolean | undefined;
    await act(async () => void (went = await captured.onSend!({ text: "再看看测试", images: [] })));
    await flush();
    expect(went).toBe(true); // the box is free: its stop button too, while the turn runs
    expect(host.textContent).toContain("此消息将在本轮结束、ultracode 切换生效后自动发送");
    expect(host.textContent).toContain("再看看测试");
    expect(host.querySelector('[data-testid="readiness-failure"]')).toBeNull();
    reply = () => json({});
    await act(async () => ultra.release!(true));
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["再看看测试", "再看看测试"]);
    expect(bodies[1].client_msg_id).not.toBe(bodies[0].client_msg_id);
    expect(host.textContent).not.toContain("ultracode 切换生效后自动发送");
  });

  it("not through in time: it stays as a failed bubble that says why, the box empty", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    reply = () => json(HELD, 409);
    await act(async () => void (await captured.onSend!({ text: "再看看测试", images: [] })));
    await flush();
    await act(async () => ultra.release!(false));
    await flush();
    expect(bodies).toHaveLength(1);
    expect(localStorage.getItem("draft:hub/c1")).toBeNull();
    expect(host.querySelector('[data-user-bubble][data-send-state="failed"]')?.textContent).toContain("再看看测试");
    expect(host.querySelector("[data-outbox] [role=alert]")?.textContent).toContain("ultracode 等这一轮结束后生效");
  });
});

describe("accepted messages waiting for their transcript copy (#2792)", () => {
  let bodies: { client_msg_id: string; text: string }[];
  beforeEach(() => {
    bodies = [];
    resetOutboxClaimsForTests();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/messages")) bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(json({}));
    }));
  });
  afterEach(() => { transcript.items = []; });

  it.each(["reload", "navigation"])("keeps one pending copy after %s without sending again, then replaces it with the echo", async (reopen) => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    await act(async () => void (await captured.onSend!({ text: "Is the museum open on Sunday?", images: [] })));
    await flush();
    const id = bodies[0].client_msg_id;
    const key = `outbox-msg:hub/c1/${id}`;
    expect(JSON.parse(localStorage.getItem(key)!)).toMatchObject({ id, state: "sent" });
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);

    act(() => root.render(null));
    if (reopen === "reload") {
      for (const chat of Object.keys(getState().outbox)) setOutbox(chat, []);
      resetOutboxClaimsForTests();
      resetRestoredForTests();
    }
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(host.querySelector(`[data-outbox="${id}"] [data-user-bubble]`)?.getAttribute("data-send-state")).toBe("sent");
    expect(host.querySelector("[data-outbox] [role=alert]")).toBeNull();
    expect([...host.querySelectorAll("[data-outbox] button")].some((b) => b.textContent === t("chat-outboxbubbles.retry"))).toBe(false);
    expect(bodies).toHaveLength(1);

    transcript.items = [{ id: "accepted-copy", seq: 1, role: "user", kind: "text", text: bodies[0].text, client_msg_id: id }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(host.querySelector("[data-user-bubble]")?.hasAttribute("data-send-state")).toBe(false);
    expect(localStorage.getItem(key)).toBeNull();
    expect(bodies).toHaveLength(1);
  });

  it("does not save an accepted bubble again when its echo arrived before HTTP 200", async () => {
    let accept!: () => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return new Promise<Response>((resolve) => (accept = () => resolve(json({}))));
    }));
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "Please check the opening hours", images: [] }); });
    await flush();
    const id = bodies[0].client_msg_id;
    const key = `outbox-msg:hub/c1/${id}`;
    expect(localStorage.getItem(key)).not.toBeNull();
    transcript.items = [{ id: "early-copy", seq: 1, role: "user", kind: "text", text: bodies[0].text, client_msg_id: id }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(localStorage.getItem(key)).toBeNull();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);

    accept();
    await act(async () => void (await sending));
    await flush();
    expect(localStorage.getItem(key)).toBeNull();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(bodies).toHaveLength(1);
  });

  it("updates a bubble restored on a fresh page when its original POST is accepted", async () => {
    let accept!: () => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return new Promise<Response>((resolve) => (accept = () => resolve(json({}))));
    }));
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let sending!: Promise<boolean>;
    act(() => { sending = captured.onSend!({ text: "Please check the visiting hours", images: [] }); });
    await flush();
    const id = bodies[0].client_msg_id;
    const key = `outbox-msg:hub/c1/${id}`;

    act(() => root.render(null));
    resetRestoredForTests(); // a newly opened page has not checked this conversation yet
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelector(`[data-outbox="${id}"] [data-user-bubble]`)?.getAttribute("data-send-state")).toBe("failed");
    accept();
    await act(async () => void (await sending));
    await flush();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(host.querySelector(`[data-outbox="${id}"] [data-user-bubble]`)?.getAttribute("data-send-state")).toBe("sent");
    expect(host.querySelector("[data-outbox] [role=alert]")).toBeNull();
    expect(JSON.parse(localStorage.getItem(key)!)).toMatchObject({ id, state: "sent" });
    expect(bodies).toHaveLength(1);

    transcript.items = [{ id: "navigated-copy", seq: 1, role: "user", kind: "text", text: bodies[0].text, client_msg_id: id }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(host.querySelector("[data-user-bubble]")?.hasAttribute("data-send-state")).toBe(false);
    expect(localStorage.getItem(key)).toBeNull();
    expect(bodies).toHaveLength(1);
  });

  it("does not turn another tab's active send into a failure on transcript updates, but restores it once accepted", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    const key = outboxKey("hub", "c1");
    const sending = { id: "other-tab", text: "Sent from another tab", images: [], at: Date.now() };
    keepOutgoing(key, { ...sending, state: "sending" });

    transcript.items = [{ id: "unrelated-update", seq: 1, role: "notice", kind: "notice", text: "The session changed" }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelector('[data-outbox="other-tab"]')).toBeNull();
    expect(host.querySelector('[data-user-bubble][data-send-state="failed"]')).toBeNull();
    expect(JSON.parse(localStorage.getItem(`outbox-msg:${key}/other-tab`)!)).toMatchObject({ state: "sending" });

    keepOutgoing(key, { ...sending, state: "sent", acceptedAt: Date.now() });
    transcript.items = [...transcript.items, { id: "another-update", seq: 2, role: "notice", kind: "notice", text: "The session changed again" }];
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelector('[data-outbox="other-tab"] [data-user-bubble]')?.getAttribute("data-send-state")).toBe("sent");
    expect(host.querySelector("[data-outbox] [role=alert]")).toBeNull();
    expect(bodies).toEqual([]);
  });

  it("expires a restored accepted bubble even when no further transcript arrives", async () => {
    vi.useFakeTimers();
    const key = "outbox-msg:hub/c1/accepted";
    keepOutgoing(outboxKey("hub", "c1"), { id: "accepted", text: "Waiting for a copy", images: [], at: Date.now(), state: "sent" });
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await act(async () => { await vi.advanceTimersByTimeAsync(100); });
      expect(host.querySelector('[data-outbox="accepted"] [data-user-bubble]')?.getAttribute("data-send-state")).toBe("sent");
      expect(localStorage.getItem(key)).not.toBeNull();

      await act(async () => { await vi.advanceTimersByTimeAsync(SENT_GRACE_MS); });
      await act(async () => { await vi.advanceTimersByTimeAsync(100); });
      expect(host.querySelector('[data-outbox="accepted"]')).toBeNull();
      expect(localStorage.getItem(key)).toBeNull();
      expect(bodies).toEqual([]);
    } finally {
      act(() => root.render(null));
      vi.useRealTimers();
    }
  });

  it("keeps attached pictures on a sent message after switching to another conversation and back (#13830)", async () => {
    const images = [
      { mime: "image/jpeg", data: "AAAA" },
      { mime: "image/png", data: "BBBB" },
    ];
    act(() => root.render(<ChatView key="c1" mid="hub" sid="c1" />));
    await flush();
    await act(async () => void (await captured.onSend!({ text: "Look at these two pictures", images })));
    await flush();
    const id = bodies[0].client_msg_id;
    const key = `outbox-msg:hub/c1/${id}`;
    expect(host.querySelectorAll("[data-user-bubble] img")).toHaveLength(2);
    expect(localStorage.getItem(key)).not.toContain("AAAA");

    // Switch to another conversation and back in the same tab (hosts key ChatView by conversation).
    act(() => root.render(<ChatView key="c2" mid="hub" sid="c2" />));
    await flush();
    act(() => root.render(<ChatView key="c1" mid="hub" sid="c1" />));
    await flush();
    const restoredImgs = [...host.querySelectorAll(`[data-outbox="${id}"] [data-user-bubble] img`)].map((img) => img.getAttribute("src"));
    expect(restoredImgs).toEqual(["data:image/jpeg;base64,AAAA", "data:image/png;base64,BBBB"]);
    expect(host.querySelector(`[data-outbox="${id}"] [data-user-bubble]`)?.getAttribute("data-send-state")).toBe("sent");

    // When the transcript echo arrives, the outbox entry settles and its images render from the transcript.
    transcript.items = [
      { id: "u1#0", seq: 1, role: "user", kind: "text", text: "Look at these two pictures", client_msg_id: id },
      { id: "u1#1", seq: 1, role: "user", kind: "image", image: { mime: "image/jpeg", lazy: true }, client_msg_id: id },
      { id: "u1#2", seq: 1, role: "user", kind: "image", image: { mime: "image/png", lazy: true }, client_msg_id: id },
    ];
    act(() => root.render(<ChatView key="c1" mid="hub" sid="c1" />));
    await flush();
    expect(host.querySelector(`[data-outbox="${id}"]`)).toBeNull();
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(host.querySelectorAll("[data-user-bubble] img")).toHaveLength(2);
    expect(localStorage.getItem(key)).toBeNull();
  });
});

describe("sending in order, and with no row yet", () => {
  const known = captured.session;
  afterEach(() => void (captured.session = known));
  let bodies: { text: string }[];
  let release: (() => void)[];
  beforeEach(() => {
    bodies = [];
    release = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/messages")) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init?.body)));
      return new Promise<Response>((resolve) => release.push(() => resolve(json({}))));
    }));
  });

  it("posts one message at a time, in the order they were sent", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let first!: Promise<boolean>, second!: Promise<boolean>;
    act(() => {
      first = captured.onSend!({ text: "one", images: [] });
      second = captured.onSend!({ text: "two", images: [] });
    });
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["one"]);
    release[0]();
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["one", "two"]);
    release[1]();
    await act(async () => void (await Promise.all([first, second])));
  });

  it("keeps a message waiting behind a slow one on screen and in this browser, past the grace", async () => {
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    let first!: Promise<boolean>, second!: Promise<boolean>;
    const sentAt = Date.now();
    act(() => {
      first = captured.onSend!({ text: "one", images: [] });
      second = captured.onSend!({ text: "two", images: [] });
    });
    await flush();
    const later = Date.now() + 3 * SENT_GRACE_MS;
    const clock = vi.spyOn(Date, "now").mockImplementation(() => later);
    transcript.items = [{ id: "n1", seq: 1, role: "notice", kind: "notice", text: "something else" }];
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.textContent).toContain("two");
      expect(Object.keys(localStorage).filter((k) => k.startsWith("outbox-msg:"))).toHaveLength(2);
      release[0]();
      await flush();
      release[1]();
      await act(async () => void (await Promise.all([first, second])));
      expect(Object.keys(localStorage).filter((k) => k.startsWith("outbox-msg:"))).toHaveLength(2);
      // Untagged echoes use the original send time, even when acceptance took longer than the clock slack.
      transcript.items = bodies.map((body, i) => ({ id: `slow-copy-${i}`, seq: i + 1, role: "user", kind: "text", text: body.text, ts: new Date(sentAt).toISOString() }));
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(2);
      expect(host.querySelector("[data-user-bubble][data-send-state]")).toBeNull();
      expect(Object.keys(localStorage).filter((k) => k.startsWith("outbox-msg:"))).toHaveLength(0);
    } finally {
      clock.mockRestore();
      transcript.items = [];
    }
  });

  it("sends a message after a routed one only once that one has used the route", async () => {
    const posts: string[] = [];
    const open: (() => void)[] = [];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const path = String(input);
      if (!path.endsWith("/route") && !path.endsWith("/messages")) return Promise.resolve(json({}));
      posts.push(path.slice(path.lastIndexOf("/") + 1));
      return new Promise<Response>((resolve) => open.push(() => resolve(json({}))));
    }));
    setRoute("c1", "todo1");
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    let first!: Promise<boolean>, second!: Promise<boolean>;
    act(() => {
      first = captured.onSend!({ text: "for the task", images: [] });
      second = captured.onSend!({ text: "for persona", images: [] });
    });
    await flush();
    expect(posts).toEqual(["route"]);
    open[0]();
    await flush();
    expect(posts).toEqual(["route", "messages"]);
    open[1]();
    await act(async () => void (await Promise.all([first, second])));
  });

  it("keeps an agent's box open but does not send before its row is here", async () => {
    captured.session = undefined;
    act(() => root.render(<ChatView mid="hub" sid="c1" />));
    await flush();
    expect(captured.composer).toMatchObject({ disabled: false, blocked: { label: expect.any(String) } });
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.connecting_placeholder"));
    let sent: boolean | undefined;
    await act(async () => void (sent = await captured.onSend!({ text: "too early", images: [] })));
    expect(sent).toBe(false);
    expect(bodies).toEqual([]);
  });

  it("lets only the main conversation send before its row is here", async () => {
    captured.session = undefined;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(captured.composer).toMatchObject({ disabled: false, blocked: { label: expect.any(String) } });
    let refused: boolean | undefined;
    await act(async () => void (refused = await captured.onSend!({ text: "too early", images: [] })));
    expect(refused).toBe(false);
    expect(bodies).toEqual([]);

    act(() => root.render(<ChatView mid="hub" sid="c1" assistant mainCache={{ scope: "test" }} />));
    await flush();
    expect(captured.composer?.blocked).toBeUndefined();
    let sending!: Promise<boolean>;
    act(() => {
      sending = captured.onSend!({ text: "already here", images: [] });
    });
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["already here"]);
    release[0]();
    await act(async () => void (await sending));
  });

  it("says a chat the hub reports missing is gone: typing works, sending stays refused, Retry is hidden", async () => {
    captured.session = undefined;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/messages")) bodies.push(JSON.parse(String(init?.body)));
      return Promise.resolve(json(String(input).includes("/sessions?") ? { sessions: [], remove: [], has_more: false, revision: "r", total: 0 } : {}));
    }));
    const key = outboxKey("hub", "c1");
    setOutbox(key, [{ id: "live", text: "did not go", images: [], at: Date.now(), state: "failed", error: "no" }]);
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(captured.composer).toMatchObject({ disabled: false, blocked: { label: t("chat-chatview.session_gone") }, placeholder: t("chat-chatview.session_gone_placeholder") });
      expect(host.querySelector('[data-outbox="live"]')?.textContent).not.toContain(t("chat-outboxbubbles.retry"));
      expect(host.querySelector('[data-outbox="live"]')?.textContent).toContain(t("chat-outboxbubbles.delete"));
      let sent: boolean | undefined;
      await act(async () => void (sent = await captured.onSend!({ text: "too late", images: [] })));
      expect(sent).toBe(false);
      expect(bodies).toEqual([]);
    } finally {
      captured.features = [];
    }
  });

  it("keeps a Retry on the page for a gone chat, and it reads the chat once more (#11237)", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Runtime chat", cwd: "/code", status: "idle" } as Session;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(json(String(input).includes("/sessions?") ? { sessions: [], remove: [], has_more: false, revision: "r", total: 0 } : {}))));
    const lookups = () => vi.mocked(fetch).mock.calls.filter(([input]) => String(input).includes("/sessions?")).length;
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      // the box says why nothing goes and looks as it did; the notice above it has the way on
      expect(captured.composer?.blocked).toEqual({ label: t("chat-chatview.session_gone") });
      const retry = [...host.querySelectorAll("button")].find((b) => b.textContent === t("sidebar.retry"));
      expect(retry).toBeTruthy();
      const before = lookups();
      act(() => retry!.click());
      await flush();
      expect(lookups()).toBe(before + 1);
    } finally {
      captured.features = [];
    }
  });

  it("keeps a runtime's list row typeable while its full detail loads, and holds Send until it answers (#11237)", async () => {
    const summary = { machine_id: "hub", session_id: "c1", title: "Runtime chat", cwd: "/code", status: "idle", state: "idle", source_kind: "antigravity" } as Session;
    captured.session = summary;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    let answer!: (r: Response) => void;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("/sessions?") ? new Promise<Response>((r) => (answer = r)) : Promise.resolve(json({}))));
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(captured.composer).toMatchObject({ disabled: false, blocked: { label: t("chat-chatview.connecting") } });
      let sent: boolean | undefined;
      await act(async () => void (sent = await captured.onSend!({ text: "too early", images: [] })));
      expect(sent).toBe(false);
      expect(bodies).toEqual([]);
      // the full row says what it can do: Send is open
      const full = { ...summary, capabilities: { read: true, send: true } };
      captured.session = full;
      await act(async () => answer(json({ sessions: [full], remove: [], has_more: false, revision: "r", total: 1 })));
      await flush();
      expect(captured.composer).toMatchObject({ disabled: false });
      expect(captured.composer?.blocked).toBeUndefined();
    } finally {
      captured.features = [];
    }
  });

  it("holds a runtime's box to its full row once that row says it takes no messages", async () => {
    const full = { machine_id: "hub", session_id: "c1", title: "Runtime chat", cwd: "/code", status: "idle", state: "idle", source_kind: "antigravity", capabilities: { read: true } } as Session;
    captured.session = full;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(json(String(input).includes("/sessions?") ? { sessions: [full], remove: [], has_more: false, revision: "r", total: 1 } : {}))));
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(captured.composer).toMatchObject({ disabled: true });
    } finally {
      captured.features = [];
    }
  });

  it.each([false, true])("calls a chat the hub has no such conversation for, with nothing of it here, not found, never by its id (assistant=%s)", async (assistant) => {
    captured.session = undefined;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(json(String(input).includes("/sessions?") ? { sessions: [], remove: [], has_more: false, revision: "r", total: 0 } : {}))));
    try {
      history.replaceState(null, "", "/persona/nope-0000");
      act(() => root.render(<ChatView mid="hub" sid="nope-0000" assistant={assistant} />));
      await flush();
      // The exact-session read and React.lazy resolution are asynchronous: wait for the visible notice, not a fixed
      // number of event-loop turns (which still loses under a loaded full suite).
      await vi.waitFor(async () => {
        await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
        expect(host.querySelector("[data-chat-not-found]")?.textContent).toContain(t("chat-notfoundnotice.body"));
      });
      expect(host.querySelector("[data-chat-title]")?.textContent).toBe(t("chat-chatview.not_found_title"));
      expect(host.textContent).not.toContain("nope-0000");
      // The notice is a React.lazy chunk: it suspends once on its first render even when the module is in, so it is
      // drawn a little after flush() returns, as late as the machine is slow. Each poll runs in act so React's retry can flush.
      await vi.waitFor(async () => {
        await act(async () => { await new Promise((r) => setTimeout(r, 20)); });
        expect(host.querySelector("[data-chat-not-found]")).not.toBeNull();
      }, { timeout: 5000, interval: 20 });
      const notice = host.querySelector("[data-chat-not-found]");
      expect(notice?.textContent).toContain(t("chat-notfoundnotice.body"));
      expect(host.textContent).not.toContain(t("runtime-session.missing"));
      const back = [...notice!.querySelectorAll("button")].find((b) => b.textContent === (assistant ? t("assistant-assistantchat.back_to_main") : t("chat-notfoundnotice.back_home")));
      expect(back).toBeTruthy();
      act(() => back!.click());
      // the main chat (`/`), or Overview for an Agent's
      expect(location.pathname).toBe(assistant ? "/" : "/overview");
    } finally {
      captured.features = [];
      history.replaceState(null, "", "/");
    }
  });

  it("keeps a missing chat's own title and the still-readable notice while something of it is here", async () => {
    captured.session = undefined;
    captured.features = [SESSIONS_LOOKUP_FEATURE];
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => Promise.resolve(json(String(input).includes("/sessions?") ? { sessions: [], remove: [], has_more: false, revision: "r", total: 0 } : {}))));
    const previous = transcript.items;
    transcript.items = [{ id: "u1", seq: 1, role: "user", kind: "text", text: "cached line", ts: "2026-11-12T09:00:00Z" }];
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelector("[data-chat-not-found]")).toBeNull();
      expect(host.textContent).toContain(t("runtime-session.missing"));
    } finally {
      transcript.items = previous;
      captured.features = [];
    }
  });

  it("asks in a space's own words in an idle Persona chat (Health's), and the box's other states still say theirs", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", state: "idle", status: "idle" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant placeholder="Ask about your health" />));
    await flush();
    expect(captured.composer?.placeholder).toBe("Ask about your health");
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />));
    await flush();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.ask_placeholder", { name: "Persona" }));
    captured.session = { ...captured.session, state: "working" } as Session;
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant placeholder="Ask about your health" />));
    await flush();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.queue_placeholder"));
  });

  it("the main conversation with no row is not connecting, and its failed bubble keeps Retry", async () => {
    captured.session = undefined;
    setOutbox(outboxKey("hub", "c1"), [{ id: "live", text: "did not go", images: [], at: Date.now(), state: "failed", error: "no" }]);
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant mainCache={{ scope: "test" }} />));
    await flush();
    expect(captured.composer?.blocked).toBeUndefined();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.ask_placeholder", { name: "Persona" }));
    expect(host.querySelector('[data-outbox="live"]')?.textContent).toContain(t("chat-outboxbubbles.retry"));
  });

  it("brings a reload's unconfirmed message back only once the chat is known and the hub's first page is in", async () => {
    keepOutgoing(outboxKey("hub", "c1"), { id: "kept", text: "unconfirmed", images: [], at: Date.now(), state: "sending" });
    captured.session = undefined;
    transcript.firstRead = false;
    try {
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelector('[data-outbox="kept"]')).toBeNull(); // no row, no first page

      captured.session = known;
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelector('[data-outbox="kept"]')).toBeNull(); // a known chat, but only a cached page so far

      transcript.firstRead = true;
      act(() => root.render(<ChatView mid="hub" sid="c1" />));
      await flush();
      expect(host.querySelector('[data-outbox="kept"] [data-user-bubble]')?.getAttribute("data-send-state")).toBe("failed");
      expect(host.querySelector('[data-outbox="kept"]')?.textContent).toContain(t("chat-outboxbubbles.retry"));
      expect(bodies).toEqual([]);
    } finally {
      transcript.firstRead = true;
    }
  });
});

it.each([false, true])("wires runtime steering for the composer (assistant=%s)", async (assistant) => {
  const session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "working", source_kind: "runtime", agent: "agy", assistant } as Session;
  captured.session = { ...session, capabilities: { send: true, queue_send_now: false } };
  act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
  expect(captured.composer?.onSendNow).toBeUndefined();
  captured.session = { ...session, capabilities: { send: true, queue_send_now: true } };
  act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
  expect(captured.composer?.onSendNow).toBeTypeOf("function");
  await act(async () => captured.composer!.onSendNow!());
  expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).endsWith("/sessions/hub/c1/queue/send-now"))).toBe(true);
});

// The assistant could not start (#10194): the hub answers 202 delivered "later" and the manager keeps the message. The
// page drops its own sending bubble and draws the row's kept message, on this device as on any other, marked not
// delivered and sent by itself; a bare operation receipt (202 {"state":"unknown"}) is no delivery and stays in doubt.
describe("a message the assistant keeps after it could not go in", () => {
  const kept = { id: "k1", text: "Synthetic kept message", at: "2026-11-17T10:00:00Z", state: "waiting" as const, attempts: 1, error: "The assistant could not start" };
  it("shows as not delivered, sent by itself, with Delete, and leaves nothing in the outbox", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Persona", cwd: "/code", state: "idle", status: "offline", assistant: true } as Session;
    reply = () => json({ delivered: "later", undelivered: kept }, 202);
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "Synthetic kept message", images: [] }); }); await flush();
    await act(async () => void (await sent)); await flush();
    expect(Object.values(getState().outbox).flat()).toHaveLength(0);
    expect(host.querySelector("[data-outbox]")).toBeNull();
    captured.session = { ...captured.session!, undelivered: [kept] };
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
    // until the bubble's chunk is in, the message shows as the owner's failed bubble alone: it never blinks out
    expect(host.querySelector('[data-undelivered="k1"]')).toBeNull();
    expect(host.querySelector('[data-user-bubble][data-send-state="failed"]')?.textContent).toContain("Synthetic kept message");
    await letKeptChunkIn(); await flush();
    const bubble = host.querySelector('[data-undelivered="k1"]');
    expect(bubble?.getAttribute("data-undelivered-state")).toBe("waiting");
    expect(bubble?.textContent).toContain("Synthetic kept message");
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    expect(bubble?.textContent).toContain(t("chat-undeliveredbubbles.waiting", { reason: kept.error }));
    expect(bubble?.querySelector('[role="status"]')).not.toBeNull();
    expect([...bubble!.querySelectorAll('[role="status"] button')].map((b) => b.textContent)).toEqual([t("chat-outboxbubbles.delete")]);
  });
  it("one that gave up offers Retry and Delete, and hides once the transcript has it", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Persona", cwd: "/code", state: "idle", status: "idle", assistant: true,
      undelivered: [{ ...kept, state: "failed" }] } as Session;
    await letKeptChunkIn();
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
    const bubble = host.querySelector('[data-undelivered="k1"]');
    expect(bubble?.querySelector('[role="alert"]')?.textContent).toContain(t("chat-undeliveredbubbles.failed", { reason: kept.error }));
    expect([...bubble!.querySelectorAll('[role="alert"] button')].map((b) => b.textContent)).toEqual([t("chat-outboxbubbles.retry"), t("chat-outboxbubbles.delete")]);
    const previous = transcript.items;
    transcript.items = [{ id: "u1", seq: 1, role: "user", kind: "text", text: "Synthetic kept message", client_msg_id: "k1", ts: "2026-11-17T10:05:00Z" }];
    try {
      act(() => root.render(<ChatView mid="hub" sid="c2" assistant />)); await flush();
      act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
      expect(host.querySelector('[data-undelivered="k1"]')).toBeNull();
    } finally { transcript.items = previous; }
  });
  it("a bare operation receipt is no delivery: the bubble stays, in doubt", async () => {
    captured.session = { machine_id: "hub", session_id: "c1", title: "Persona", cwd: "/code", state: "idle", status: "offline", assistant: true } as Session;
    reply = () => json({ state: "unknown" }, 202);
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant />)); await flush();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "Synthetic replayed message", images: [] }); }); await flush();
    await act(async () => void (await sent)); await flush();
    expect(host.querySelector("[data-outbox] [data-in-doubt]")?.getAttribute("data-in-doubt")).toBe("unknown");
    expect(host.textContent).toContain("Synthetic replayed message");
  });
  it.each([true, false])("shows a signed-out device's reason and only Delete (assistant: %s)", async (assistant) => {
    const error = "Not sent: the device that sent it was signed out";
    captured.session = {
      machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle", assistant,
      undelivered: [{ ...kept, state: "failed", delete_only: true, error, computer: assistant ? undefined : "Synthetic laptop" }],
    } as Session;
    await letKeptChunkIn();
    act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
    const line = host.querySelector('[data-undelivered="k1"] [role="alert"]');
    expect(line?.querySelector("span")?.textContent).toBe(error);
    expect([...line!.querySelectorAll("button")].map((b) => b.textContent)).toEqual([t("chat-outboxbubbles.delete")]);
  });
});

// An Agent whose computer is away, on a hub that holds what is sent meanwhile (#10661): the box stays open and says the
// messages go once it is back; a send the hub holds (202 delivered "later") leaves the page's own bubble for the row's,
// whose line is the manager's own words, with Delete while it waits and Retry and Delete once it stopped, both on the
// session's held routes. Without the feature, a Persona chat, or a computer that is online: the box as before.
describe("a message to an Agent whose computer is away", () => {
  const laptop = { id: "hub", name: "Synthetic laptop", online: false } as Machine;
  const agentRow = { machine_id: "hub", session_id: "c1", title: "Agent", cwd: "/code", status: "unknown", state: "unknown" } as Session;
  const held = { id: "k1", text: "Synthetic held message", at: "2026-11-19T08:00:00Z", state: "waiting" as const, error: "Will send when Synthetic laptop is back", computer: "Synthetic laptop" };
  const awayLine = async () => (await import("./UndeliveredBubbles")).heldPlaceholder("Synthetic laptop");
  const shown = async (assistant = false) => {
    await letKeptChunkIn();
    await act(async () => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />)); await flush();
  };
  const fetched = () => vi.mocked(fetch).mock.calls.map(([input, init]) => ({ url: String(input), method: (init?.method ?? "GET").toUpperCase() }));
  afterEach(() => {
    captured.features = [];
    captured.machines = undefined;
  });

  it("keeps the box open and says the messages go once the computer is back", async () => {
    captured.session = agentRow;
    captured.machines = [laptop];
    captured.features = ["agent_message_hold"];
    await shown();
    const expectedPlaceholder = await awayLine();
    // The placeholder arrives through a lazy import after the generic flush; wait for that rendered value itself.
    await vi.waitFor(async () => {
      await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
      expect(captured.composer?.placeholder).toBe(expectedPlaceholder);
    }, { timeout: 5000, interval: 20 });
    expect(captured.composer?.disabled).toBe(false);
    // back online, in the same open chat: the box says what it said before
    captured.machines = [{ ...laptop, online: true }];
    await shown();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.agent_placeholder"));
  });

  it("leaves the box as before without the feature, for a Persona chat, or with the computer online", async () => {
    captured.session = agentRow;
    captured.machines = [laptop];
    captured.features = ["sessions_delta"];
    await shown();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.agent_placeholder"));
    expect(captured.composer?.disabled).toBe(false);
    captured.features = ["agent_message_hold"];
    captured.machines = [{ ...laptop, online: true }];
    await shown();
    expect(captured.composer?.placeholder).toBe(t("chat-chatview.agent_placeholder"));
    captured.machines = [laptop];
    captured.session = { ...agentRow, title: "Persona", assistant: true };
    await shown(true);
    expect(captured.composer?.placeholder).not.toBe(await awayLine());
  });

  it("a held send shows on the row with the manager's line, Delete while it waits, and nothing left in the outbox", async () => {
    captured.session = agentRow;
    captured.machines = [laptop];
    captured.features = ["agent_message_hold"];
    reply = () => json({ delivered: "later", undelivered: held }, 202);
    await shown();
    let sent!: Promise<boolean>;
    act(() => { sent = captured.onSend!({ text: "Synthetic held message", images: [] }); }); await flush();
    let kept: boolean | undefined;
    await act(async () => void (kept = await sent)); await flush();
    expect(kept).toBe(true);
    // the send declares the feature itself: a page whose socket is down has no client_hello for the hub to go by
    const post = vi.mocked(fetch).mock.calls.find(([input]) => String(input).endsWith("/messages"));
    expect(new Headers(post?.[1]?.headers).get("X-Persona-Features")).toBe("agent_message_hold");
    expect(Object.values(getState().outbox).flat()).toHaveLength(0);
    expect(host.querySelector("[data-outbox]")).toBeNull();
    // the row has it at once, before the hub's next list carries it
    expect(getState().sessions.hub?.find((s) => s.session_id === "c1")?.undelivered).toEqual([held]);
    expect(captured.composer?.disabled).toBe(false);
    captured.session = { ...agentRow, undelivered: [held] };
    await shown();
    const bubble = host.querySelector('[data-undelivered="k1"]');
    expect(bubble?.hasAttribute("data-undelivered-held")).toBe(true);
    expect(bubble?.getAttribute("data-undelivered-state")).toBe("waiting");
    expect(host.querySelectorAll("[data-user-bubble]")).toHaveLength(1);
    const line = bubble?.querySelector('[role="status"] span');
    expect(line?.textContent).toBe(held.error);
    expect(line?.className).toContain("text-orange");
    expect([...bubble!.querySelectorAll('[role="status"] button')].map((b) => b.textContent)).toEqual([t("chat-outboxbubbles.delete")]);
    await act(async () => (bubble!.querySelector('[role="status"] button') as HTMLButtonElement).click()); await flush();
    expect(fetched().at(-1)).toEqual({ url: "/api/v1/sessions/hub/c1/held/k1", method: "DELETE" });
  });

  it("one that stopped says why in red, with Retry and Delete on the held routes", async () => {
    const stopped = { ...held, state: "failed" as const, error: "Might not have arrived. Retry checks the chat first." };
    captured.session = { ...agentRow, undelivered: [stopped] };
    captured.machines = [laptop];
    captured.features = ["agent_message_hold"];
    await shown();
    const bubble = host.querySelector('[data-undelivered="k1"]');
    const line = bubble?.querySelector('[role="alert"] span');
    expect(line?.textContent).toBe(stopped.error);
    expect(line?.className).toContain("text-red");
    const buttons = [...bubble!.querySelectorAll<HTMLButtonElement>('[role="alert"] button')];
    expect(buttons.map((b) => b.textContent)).toEqual([t("chat-outboxbubbles.retry"), t("chat-outboxbubbles.delete")]);
    // The lazy mode-changes watcher may issue its GET after Retry; let it finish before asserting the last request.
    await act(async () => { await vi.dynamicImportSettled(); }); await flush();
    await act(async () => buttons[0].click()); await flush();
    expect(fetched().at(-1)).toEqual({ url: "/api/v1/sessions/hub/c1/held/k1/retry", method: "POST" });
    // without the manager's words: the plain line in the page's language
    captured.session = { ...agentRow, undelivered: [{ ...held, error: undefined }] };
    await shown();
    expect(host.querySelector('[data-undelivered="k1"] [role="status"] span')?.textContent).toBe(t("chat-undeliveredbubbles.held_waiting", { computer: "Synthetic laptop" }));
  });
});
