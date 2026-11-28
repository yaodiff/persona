// @vitest-environment jsdom
// The model error a chat stopped on is cindy's banner over the composer, with Retry and ✕, and is not drawn in the chat
// at the same time (#5313). Dismissed (remembered in this browser), followed by anything, or while the Agent works, it
// is the record in the chat, without Retry. Retry puts the banner away at once; the same error again is a new banner.
import { act, useEffect, useReducer } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LEGACY_SCHEME } from "../../lib/compatibility";
import type { Item, Session } from "../../lib/types";

// a mouse (fine pointer) or a touch screen alone, per test
const media = vi.hoisted(() => ({ fine: false }));
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  // read on each look: the app keeps one list per query
  g.matchMedia = (q: string) => ({ get matches() { return q === "(any-pointer: fine)" && media.fine; }, addEventListener() {}, removeEventListener() {} });
  g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} };
  g.IntersectionObserver = class { observe() {} unobserve() {} disconnect() {} takeRecords() { return []; } };
});

const IDLE: Session = { machine_id: "hub", session_id: "c1", title: "Conversation", cwd: "/code", status: "idle" };
const captured = vi.hoisted(() => ({
  session: undefined as Session | undefined,
  features: [] as string[],
  assistantMain: undefined as string | undefined,
  ownerOffline: undefined as string | undefined,
}));

vi.mock("./Composer", async (importOriginal) => ({
  ...await importOriginal<typeof import("./Composer")>(),
  Composer: () => <textarea aria-label="composer" data-composer-input />,
}));
// one transcript for every render; a test swaps its items and draws again
const transcript = vi.hoisted(() => ({ items: [] as unknown[], loading: false, firstRead: true, error: "", hasMore: false, streaming: "" }));
vi.mock("./useTranscript", () => ({ useTranscript: () => transcript }));
vi.mock("../../lib/store", async (importOriginal) => {
  const store = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...store,
    getSession: () => captured.session,
    useStore: <T,>(select: (s: ReturnType<typeof store.getState>) => T) => {
      const [, changed] = useReducer((n: number) => n + 1, 0);
      useEffect(() => store.subscribe(changed), []);
      const base = store.getState();
      return select({
        ...base,
        features: captured.features,
        assistant: captured.assistantMain ? { ...(base.assistant ?? { machine_id: "hub", conversations: [] }), main: captured.assistantMain } : base.assistant,
        sessions: { hub: captured.session ? [captured.session] : [] },
      });
    },
  };
});
const openPanelSpy = vi.hoisted(() => vi.fn());
vi.mock("../../lib/panel", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/panel")>(),
  usePanel: () => null,
  openPanel: openPanelSpy,
}));
vi.mock("../ManagerParts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../ManagerParts")>(),
  useManagerOffline: () => captured.ownerOffline,
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

// a refused send's toast: no toast stack is drawn here
vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../ui/atoms")>(),
  toast: vi.fn(),
}));

const { ChatView } = await import("./ChatView");
const { toast } = await import("../../ui/atoms");
const { onModelPicker } = await import("../../lib/model-switch");
const { onPermissionPicker } = await import("../../lib/permissions-command");
const { getState, setOutbox, upsertSession } = await import("../../lib/store");
const { resetOutboxClaimsForTests, resetRestoredForTests } = await import("../../lib/outbox");
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const OVERLOADED = "API Error: 529 overloaded";
const msg = (id: string, seq: number, role: "user" | "assistant", text: string, extra: Partial<Item> = {}) =>
  ({ id, seq, role, kind: "text", text, ts: "2026-11-12T08:00:00Z", ...extra }) as Item;
const failedTurn = [msg("u1", 1, "user", "接着把命名改完"), msg("e1", 2, "assistant", OVERLOADED)];

const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });
let bodies: { client_msg_id: string; text: string; images?: { mime: string; data: string }[] }[];
let compactCalls: string[];
let clearCalls: string[];
let reply: () => Response;
let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const draw = async (assistant = false) => {
  act(() => root.render(<ChatView mid="hub" sid="c1" assistant={assistant} />));
  await flush();
};
const banner = () => host.querySelector<HTMLElement>("[data-chat-dock] [data-error-banner]");
const inChat = () => host.querySelector<HTMLElement>("[data-chat-content] [data-error-notice]");
const button = (scope: Element | null, label: string) => [...(scope?.querySelectorAll("button") ?? [])].find((b) => b.textContent === label);

beforeEach(() => {
  localStorage.clear();
  resetRestoredForTests();
  resetOutboxClaimsForTests();
  captured.features = [];
  captured.assistantMain = undefined;
  captured.ownerOffline = undefined;
  openPanelSpy.mockClear();
  for (const key of Object.keys(getState().outbox)) setOutbox(key, []);
  captured.session = IDLE;
  media.fine = false;
  reply = () => json({});
  vi.mocked(toast).mockClear();
  transcript.items = failedTurn;
  bodies = [];
  compactCalls = [];
  clearCalls = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url.endsWith("/compact") && init?.method === "POST") {
      compactCalls.push(url);
      return Promise.resolve(json({ ok: true }));
    }
    if (url.endsWith("/assistant/main/clear") && init?.method === "POST") {
      clearCalls.push(url);
      return Promise.resolve(json({ ok: true }));
    }
    if (!url.endsWith("/messages") || !init?.body) return Promise.resolve(json({}));
    bodies.push(JSON.parse(String(init.body)));
    return Promise.resolve(reply());
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("the error an Agent's chat stopped on", () => {
  it("is the banner over the composer, with Retry and ✕, and not in the chat", async () => {
    await draw();
    expect(banner()).not.toBeNull();
    expect(banner()!.textContent).toContain("重试");
    expect(banner()!.querySelector('[aria-label="关闭错误提示"]')).not.toBeNull();
    expect(inChat()).toBeNull();
    // the question that failed stays in the chat
    expect(host.querySelector("[data-chat-content]")!.textContent).toContain("接着把命名改完");
  });

  it("✕ puts it back in the chat without Retry, and the chat remembers it on this device", async () => {
    await draw();
    act(() => banner()!.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!.click());
    await flush();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
    expect(button(inChat(), "重试")).toBeUndefined();
    act(() => root.unmount());
    root = createRoot(host);
    await draw();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
  });

  it("is a record in the chat once anything comes after it", async () => {
    transcript.items = [...failedTurn, msg("u2", 3, "user", "换个思路试试"), msg("a2", 4, "assistant", "好的，换成按模块改名。")];
    await draw();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
    expect(button(inChat(), "重试")).toBeUndefined();
  });

  it("is not a banner while the Agent works", async () => {
    captured.session = { ...IDLE, status: "working" };
    await draw();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
    expect(button(inChat(), "重试")).toBeUndefined();
  });

  it("Retry sends 继续 and puts the banner away at once; the same error after it is a new banner", async () => {
    await draw();
    await act(async () => button(banner(), "重试")!.click());
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["继续"]);
    expect(banner()).toBeNull();
    // the echo and the same error again: the line folds with a higher count, a failure to say again
    transcript.items = [...failedTurn, msg("u2", 3, "user", "继续", { client_msg_id: bodies[0].client_msg_id }), msg("e2", 4, "assistant", OVERLOADED)];
    await draw();
    expect(banner()).not.toBeNull();
    expect(inChat()).toBeNull();
  });

  it("comes back when Retry did not go, so the chat can still be retried", async () => {
    await draw();
    // the node refused the launcher switch the message was to carry out: nothing went, no bubble is left (#5035)
    reply = () => json({ error: "切换没成功", code: "switch_failed", delivered: "none" }, 409);
    await act(async () => button(banner(), "重试")!.click());
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["继续"]);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("切换没成功"), "error");
    expect(host.querySelector("[data-user-bubble][data-send-state]")).toBeNull();
    expect(banner()).not.toBeNull();
    expect(button(banner(), "重试")).toBeDefined();
    expect(inChat()).toBeNull();
  });

  it("leaves the host's failure note to say why the turn stopped: one card over the composer", async () => {
    captured.session = { ...IDLE, failure_reason: "⚠️ API Error: 529 overloaded" };
    await draw();
    await act(async () => { await import("./BotmuxMessages"); });
    expect(host.querySelector('[data-chat-dock] [data-testid="chat-failure"]')).not.toBeNull();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
  });

  it("stays away after the failure note's Retry: the note goes, 继续 is sent, and no banner takes its place", async () => {
    captured.session = { ...IDLE, failure_reason: "⚠️ API Error: 529 overloaded" };
    await draw();
    await act(async () => { await import("./BotmuxMessages"); });
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="chat-failure-retry"]')!.click());
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["继续"]);
    expect(host.querySelector('[data-testid="chat-failure"]')).toBeNull();
    expect(banner()).toBeNull();
  });

  it("is a status of its own, and its buttons hand the keyboard back to the composer with a mouse only", async () => {
    await draw();
    expect(banner()!.getAttribute("role")).toBe("status");
    // a touch screen alone: no keyboard raised
    act(() => banner()!.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!.click());
    await flush();
    expect(document.activeElement).not.toBe(host.querySelector("textarea"));
    localStorage.clear();
    act(() => root.unmount());
    root = createRoot(host);
    media.fine = true;
    await draw();
    const close = banner()!.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!;
    close.focus();
    act(() => close.click());
    await flush();
    expect(document.activeElement).toBe(host.querySelector("textarea"));
  });
});

describe("the error the Persona conversation stopped on", () => {
  it("reads the same in the chat after ✕: the hub's Fix it stays, without Retry", async () => {
    captured.session = { ...IDLE, turn_error_code: "auth_failed" };
    transcript.items = [msg("u1", 1, "user", "帮我看看今天的会"), msg("e1", 2, "assistant", "API Error: 401 authentication_error")];
    await draw(true);
    expect(banner()!.textContent).toContain("模型未连接：Key 无效");
    expect(banner()!.textContent).toContain("修复");
    act(() => banner()!.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!.click());
    await flush();
    expect(inChat()!.textContent).toContain("模型未连接：Key 无效");
    expect(inChat()!.textContent).toContain("修复");
    expect(button(inChat(), "重试")).toBeUndefined();
  });

  it("is the banner over the composer too, and not in the chat", async () => {
    await draw(true);
    expect(banner()).not.toBeNull();
    expect(banner()!.textContent).toContain("重试");
    expect(inChat()).toBeNull();
    act(() => banner()!.querySelector<HTMLButtonElement>('[aria-label="关闭错误提示"]')!.click());
    await flush();
    expect(banner()).toBeNull();
    expect(inChat()).not.toBeNull();
    expect(button(inChat(), "重试")).toBeUndefined();
  });
});

describe("image-limit Compact and send again (#13045)", () => {
  const TOO_LARGE = "Request too large (max 32MB). Accumulated images and attachments in the conversation pushed the request over the limit.";

  const clickCompactAndResend = async (control = banner()!.querySelector<HTMLButtonElement>('[data-testid="compact-and-resend"]')!) => {
    act(() => control.click());
    await act(async () => vi.dynamicImportSettled());
    await flush();
  };

  it("runs assistant_compact and resends the failed message once compaction succeeds", async () => {
    captured.features = ["assistant_compact"];
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "帮我对比这几张截图"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(true);
    expect(banner()!.textContent).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    const btn = banner()!.querySelector<HTMLButtonElement>('[data-testid="compact-and-resend"]');
    expect(btn?.textContent).toBe("压缩并重发");
    expect(button(banner(), "重试")).toBeUndefined();

    await clickCompactAndResend(btn!);
    expect(compactCalls).toHaveLength(1);
    expect(compactCalls[0]).toContain("/assistant/conversations/c1/compact");
    expect(bodies).toEqual([]);

    // Compaction runs and finishes -> resend fires automatically.
    captured.session = { ...captured.session, compaction: { state: "running", started_at: "2026-11-22T12:00:00Z" } };
    act(() => upsertSession(captured.session!));
    await flush();
    expect(bodies).toEqual([]);

    captured.session = { ...captured.session, compaction: { state: "done", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:05Z" } };
    act(() => upsertSession(captured.session!));
    await flush();
    expect(bodies.map((b) => b.text)).toEqual(["帮我对比这几张截图"]);
  });

  it("does not resend when assistant compaction fails", async () => {
    captured.features = ["assistant_compact"];
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "帮我对比这几张截图"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(true);
    await clickCompactAndResend();
    captured.session = { ...captured.session, compaction: { state: "failed", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:03Z", error: "too short" } };
    act(() => upsertSession(captured.session!));
    await flush();
    expect(bodies).toEqual([]);
  });

  it("sends /compact and resends after boundary + idle on terminal Claude Code", async () => {
    captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "看看这些界面差异"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(false);
    const btn = banner()!.querySelector<HTMLButtonElement>('[data-testid="compact-and-resend"]');
    expect(btn?.textContent).toBe("压缩并重发");
    expect(button(banner(), "重试")).toBeUndefined();

    await clickCompactAndResend(btn!);
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    // Terminal works, writes compact_boundary, then returns idle -> resends original message.
    captured.session = { ...captured.session, status: "working", state: "working" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    transcript.items = [
      ...transcript.items,
      { id: "c1", seq: 3, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
    ];
    captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
    act(() => upsertSession(captured.session!));
    await draw(false);
    expect(bodies.map((b) => b.text)).toEqual(["/compact", "看看这些界面差异"]);
  });

  it("hides Compact and send again when compaction is not supported", async () => {
    // Assistant without assistant_compact hub feature.
    captured.features = [];
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "看看图"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(true);
    expect(banner()!.textContent).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(banner()!.querySelector('[data-testid="compact-and-resend"]')).toBeNull();

    // Non-Claude agent session (Codex).
    captured.session = { ...IDLE, agent: "codex", turn_error_code: "images_too_large" };
    await draw(false);
    expect(banner()!.textContent).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(banner()!.querySelector('[data-testid="compact-and-resend"]')).toBeNull();
    expect(button(banner(), "重试")).toBeUndefined();
  });

  it("preserves inline images and rich context/file tags when compacting and resending", async () => {
    captured.features = ["assistant_compact"];
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    const richPrompt = '<persona-context kind="edit" title="Doc">line 1</persona-context>\n<persona-file kind="file" path="/tmp/a.pdf" name="a.pdf"/>\n[Image #1] 帮我看看这张图';
    transcript.items = [
      msg("u1#0", 1, "user", richPrompt),
      { id: "u1#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", data: "UE5H" }, ts: "2026-11-12T08:00:00Z" } as Item,
      msg("e1", 2, "assistant", TOO_LARGE),
    ];
    await draw(true);
    await clickCompactAndResend();
    expect(compactCalls).toHaveLength(1);

    captured.session = { ...captured.session, compaction: { state: "done", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:05Z" } };
    act(() => upsertSession(captured.session!));
    await flush();
    expect(bodies).toHaveLength(1);
    const tag = `(?:persona|${LEGACY_SCHEME})`;
    expect(bodies[0].text).toMatch(new RegExp(`<${tag}-context kind="edit" title="Doc">line 1</${tag}-context>`));
    expect(bodies[0].text).toMatch(new RegExp(`<${tag}-file kind="file" path="/tmp/a\\.pdf" name="a\\.pdf"/>`));
    expect(bodies[0].text).toContain("帮我看看这张图");
    expect(bodies[0].images).toEqual([{ mime: "image/png", data: "UE5H" }]);
  });

  it("withholds automatic resend when image bytes are unrecoverable and aborts before compaction if lazy image fetch fails", async () => {
    // Statically unrecoverable image-only message (no data, no lazy): button hidden.
    captured.features = ["assistant_compact"];
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    transcript.items = [
      { id: "u1", seq: 1, role: "user", kind: "image", ts: "2026-11-12T08:00:00Z" } as Item,
      msg("e1", 2, "assistant", TOO_LARGE),
    ];
    await draw(true);
    expect(banner()!.querySelector('[data-testid="compact-and-resend"]')).toBeNull();

    // Lazy image fetch returns 404 -> aborts BEFORE calling POST .../compact and shows error toast.
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      if (url.includes("/image")) return Promise.resolve(new Response(null, { status: 404 }));
      if (url.endsWith("/compact") && init?.method === "POST") {
        compactCalls.push(url);
        return Promise.resolve(json({ ok: true }));
      }
      if (!url.endsWith("/messages") || !init?.body) return Promise.resolve(json({}));
      bodies.push(JSON.parse(String(init.body)));
      return Promise.resolve(reply());
    }));
    transcript.items = [
      msg("u2#0", 1, "user", "看看这张截图"),
      { id: "u2#1", seq: 1, role: "user", kind: "image", image: { mime: "image/png", lazy: true }, ts: "2026-11-12T08:00:00Z" } as Item,
      msg("e2", 2, "assistant", TOO_LARGE),
    ];
    await draw(true);
    await clickCompactAndResend();
    expect(compactCalls).toEqual([]);
    expect(bodies).toEqual([]);
    expect(vi.mocked(toast)).toHaveBeenCalled();
  });

  it("resends a failed slash-command user turn verbatim instead of walking to an earlier prompt", async () => {
    captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
    transcript.items = [
      msg("u0", 1, "user", "更早的无关消息"),
      msg("a0", 2, "assistant", "好的"),
      msg("u1", 3, "user", "/clear"),
      msg("e1", 4, "assistant", TOO_LARGE),
    ];
    await draw(false);
    await clickCompactAndResend();
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    captured.session = { ...captured.session, status: "working", state: "working" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    transcript.items = [
      ...transcript.items,
      { id: "c1", seq: 5, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
    ];
    captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
    act(() => upsertSession(captured.session!));
    await draw(false);
    expect(bodies.map((b) => b.text)).toEqual(["/compact", "/clear"]);
  });

  it("does not resend on terminal Claude Code if an older boundary is paged in while idle or if /compact fails with an API error", async () => {
    captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 10, "user", "看看这些界面差异"), msg("e1", 11, "assistant", TOO_LARGE)];
    await draw(false);
    await clickCompactAndResend();
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    // Older boundary paged in while still idle -> must NOT trigger resend.
    transcript.items = [
      { id: "c0", seq: 3, role: "assistant", kind: "notice", compact: { trigger: "auto" }, text: "上下文已压缩", ts: "2026-11-22T11:00:00Z" } as Item,
      ...transcript.items,
    ];
    await draw(false);
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    // Terminal works, then emits both a boundary and an API error -> must fail without resending.
    captured.session = { ...captured.session, status: "working", state: "working" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    transcript.items = [
      ...transcript.items,
      { id: "c1", seq: 12, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
      msg("e2", 13, "assistant", "API Error: 500 compact failed"),
    ];
    captured.session = { ...captured.session, status: "idle", state: "idle" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);
  });

  it("handles main-chat /clear locally via onSend command path instead of posting it to /messages", async () => {
    captured.features = ["assistant_compact", "assistant_main_clear"];
    captured.assistantMain = "c1";
    captured.session = { ...IDLE, assistant: true, turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "/clear"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(true);
    await clickCompactAndResend();
    expect(compactCalls).toHaveLength(1);

    captured.session = { ...captured.session, compaction: { state: "done", started_at: "2026-11-22T12:00:00Z", finished_at: "2026-11-22T12:00:05Z" } };
    act(() => upsertSession(captured.session!));
    await flush();
    expect(clearCalls).toHaveLength(1);
    expect(bodies).toEqual([]);
  });

  it("handles agent /workflows locally via openBackgroundPanel instead of posting it to /messages", async () => {
    captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "/workflows"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(false);
    await clickCompactAndResend();
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    captured.session = { ...captured.session, status: "working", state: "working" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    transcript.items = [
      ...transcript.items,
      { id: "c1", seq: 3, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
    ];
    captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
    act(() => upsertSession(captured.session!));
    await draw(false);
    expect(openPanelSpy).toHaveBeenCalledTimes(1);
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);
  });

  it("hides Compact and send again for assistant conversations when the owning manager is offline", async () => {
    captured.features = ["assistant_compact"];
    captured.ownerOffline = "Manager offline";
    captured.session = { ...IDLE, assistant: true, manager: "m2", turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "帮我看看这张图"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(true);
    expect(banner()!.textContent).toContain("该对话中的图片数量过多或体积过大，超出了模型限制。");
    expect(banner()!.querySelector('[data-testid="compact-and-resend"]')).toBeNull();
  });

  it("opens the local /model picker instead of posting to /messages when registered", async () => {
    const modelSpy = vi.fn();
    const unsubModel = onModelPicker("hub/c1", modelSpy);
    try {
      captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
      transcript.items = [msg("u1", 1, "user", "/model"), msg("e1", 2, "assistant", TOO_LARGE)];
      await draw(false);
      await clickCompactAndResend();
      expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

      captured.session = { ...captured.session, status: "working", state: "working" };
      act(() => upsertSession(captured.session!));
      await draw(false);
      transcript.items = [
        ...transcript.items,
        { id: "c1", seq: 3, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
      ];
      captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
      act(() => upsertSession(captured.session!));
      await draw(false);
      expect(modelSpy).toHaveBeenCalledTimes(1);
      expect(bodies.map((b) => b.text)).toEqual(["/compact"]);
    } finally {
      unsubModel();
    }
  });

  it("opens the local /permissions picker instead of posting to /messages when registered", async () => {
    const permSpy = vi.fn();
    const unsubPerm = onPermissionPicker("hub/c1", permSpy);
    try {
      captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
      transcript.items = [msg("u2", 10, "user", "/permissions"), msg("e2", 11, "assistant", TOO_LARGE)];
      await draw(false);
      await clickCompactAndResend();
      expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

      captured.session = { ...captured.session, status: "working", state: "working" };
      act(() => upsertSession(captured.session!));
      await draw(false);
      transcript.items = [
        ...transcript.items,
        { id: "c2", seq: 12, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:10Z" } as Item,
      ];
      captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
      act(() => upsertSession(captured.session!));
      await draw(false);
      expect(permSpy).toHaveBeenCalledTimes(1);
      expect(bodies.map((b) => b.text)).toEqual(["/compact"]);
    } finally {
      unsubPerm();
    }
  });

  it("falls back to sending /model as text when no local model picker is registered", async () => {
    captured.session = { ...IDLE, agent: "claude", turn_error_code: "images_too_large" };
    transcript.items = [msg("u1", 1, "user", "/model"), msg("e1", 2, "assistant", TOO_LARGE)];
    await draw(false);
    await clickCompactAndResend();
    expect(bodies.map((b) => b.text)).toEqual(["/compact"]);

    captured.session = { ...captured.session, status: "working", state: "working" };
    act(() => upsertSession(captured.session!));
    await draw(false);
    transcript.items = [
      ...transcript.items,
      { id: "c1", seq: 3, role: "assistant", kind: "notice", compact: { trigger: "manual" }, text: "上下文已压缩", ts: "2026-11-22T12:00:05Z" } as Item,
    ];
    captured.session = { ...captured.session, status: "idle", state: "idle", turn_error_code: undefined };
    act(() => upsertSession(captured.session!));
    await draw(false);
    expect(bodies.map((b) => b.text)).toEqual(["/compact", "/model"]);
  });
});
