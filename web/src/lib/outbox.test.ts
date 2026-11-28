import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./store", () => {
  const state = {
    outbox: {} as Record<string, import("./outbox").OutboxEntry[]>,
    assistant: null as null | { machine_id: string; main?: string; conversations: unknown[] },
    features: [] as string[], // a hub without start receipts: the start goes as it always did (lib/assistant-start.ts)
  };
  return {
    getState: () => state,
    setOutbox: (key: string, list: import("./outbox").OutboxEntry[]) => {
      if (list.length) state.outbox[key] = list;
      else delete state.outbox[key];
    },
    refreshAssistant: vi.fn(async () => {}),
  };
});
vi.mock("./router", () => ({ navigate: vi.fn() }));
vi.mock("../ui/atoms", async (actual) => ({ ...(await actual<typeof import("../ui/atoms")>()), toast: vi.fn() }));

import { acceptOutgoing, arrived, checkOutbox, entryReason, failureText, forgetOutgoing, askPersona, discardOutbox, inSendOrder, keepFailed, keepOutgoing, noteSent, noteWritten, outboxMessage, placeFailed, queueToSession, registerOutboxRecheck, resetOutboxClaimsForTests, resetRestoredForTests, restoreOutbox, retryOutbox, sendToSession, settle, settleOutbox, SENT_GRACE_MS, type OutboxEntry } from "./outbox";
import { messageFeedback, resetMessageFeedback } from "./message-send";
import type { ChatLine } from "./turns";
import { ApiError } from "./api";
import { pickedLauncher, pickedModel, pickLauncher, pickModel } from "./assistant-chat";
import { navigate } from "./router";
import { getState } from "./store";
import { toast } from "../ui/atoms";
import { t } from "../i18n";
import { skillContext, skillDraft, splitSkillDraft } from "./skill-mentions";

function response(body: unknown, status = 200) {
  return { ok: status < 400, status, text: async () => JSON.stringify(body) };
}
const outbox = () => getState().outbox as Record<string, OutboxEntry[]>;
const entry = (patch: Partial<OutboxEntry> = {}): OutboxEntry => ({ id: "e1", text: "你好", images: [], at: 1000, state: "sent", ...patch });
const recheckRegistrations: (() => void)[] = [];
const mountedRecheck = (mid: string, sid: string, recheck: () => void) => {
  const cleanup = registerOutboxRecheck(mid, sid, recheck);
  recheckRegistrations.push(cleanup);
  return cleanup;
};
const retryPrompts = [
  { kind: "plain text", text: "Please check this." },
  { kind: "selected skill", text: skillDraft([skillContext({ id: "scoped-reference", name: "review", source: "Project" })], "Please check this.") },
];

beforeEach(() => {
  vi.stubGlobal("localStorage", { getItem: () => "test" });
  for (const k of Object.keys(outbox())) delete outbox()[k];
  (getState() as { assistant: unknown }).assistant = null;
  vi.clearAllMocks();
});
afterEach(() => {
  for (const cleanup of recheckRegistrations.splice(0)) cleanup();
  resetMessageFeedback();
  vi.unstubAllGlobals();
});

describe("which bubbles stay", () => {
  it("goes once the transcript says the same, with as many images", () => {
    expect(arrived(entry(), [{ text: "你好", images: [] }])).toBe(true);
    expect(arrived(entry({ images: [{ mime: "image/png", data: "x" }] }), [{ text: "你好", images: [] }])).toBe(false);
    expect(arrived(entry({ text: '<persona-context kind="context" title="t" url="">x</persona-context>\n\n说说' }), [{ text: "说说", images: [] }])).toBe(true);
  });

  it("keeps failed ones, and sent ones only for a while", () => {
    const users = [{ text: "别的", images: [] }];
    const list = [entry({ id: "a" }), entry({ id: "b", state: "failed" }), entry({ id: "c", state: "sending" }), entry({ id: "d", at: 1000 - SENT_GRACE_MS })];
    expect(settle(list, users, 1000).map((e) => e.id)).toEqual(["a", "b", "c"]);
    const copies = ["u1", "u2", "u3"].map((id) => ({ id, text: "你好", images: [] }));
    expect(settle(list, copies, 1000).map((e) => e.id)).toEqual(["b"]);
  });

  it("drops a failed one the hub tied a copy to (it went in after all), and only that one", () => {
    const list = [entry({ id: "a", state: "failed" }), entry({ id: "b", state: "failed" }), entry({ id: "w", state: "failed", wordsOnly: true })];
    expect(settle(list, [{ id: "u1", client_msg_id: "a", text: "改过的字", images: [] }], 1000).map((e) => e.id)).toEqual(["b", "w"]);
    expect(settle(list, [{ id: "u1", text: "你好", images: [] }], 1000).map((e) => e.id)).toEqual(["a", "b", "w"]); // words alone: not it
  });

  it("a send that ended in doubt goes once its words reach the transcript; a refusal does not", () => {
    const doubt = entry({ id: "d", state: "failed", inDoubt: true, at: 1_000_000 });
    const refused = entry({ id: "r", state: "failed", at: 1_000_000 });
    const copy = { id: "u1", text: "你好", images: [], ts: new Date(1_030_000).toISOString() };
    expect(settle([doubt], [copy], 1_040_000).map((e) => e.id)).toEqual([]);
    expect(settle([refused], [copy], 1_040_000).map((e) => e.id)).toEqual(["r"]);
    // an older message with the same words is not its copy, also within the clocks' usual slack
    const older = { id: "u0", text: "你好", images: [], ts: new Date(1_000_000 - 600_000).toISOString() };
    expect(settle([doubt], [older], 1_040_000).map((e) => e.id)).toEqual(["d"]);
    const justBefore = { id: "u2", text: "你好", images: [], ts: new Date(1_000_000 - 60_000).toISOString() };
    expect(settle([doubt], [justBefore], 1_040_000).map((e) => e.id)).toEqual(["d"]);
  });

  it("takes one bubble per copy, never an older message's copy, and a copy only once", () => {
    const list = [entry({ id: "a", at: 1_000_000 }), entry({ id: "b", at: 1_001_000 })];
    const older = { id: "u0", text: "你好", images: [], ts: new Date(1_000_000 - 600_000).toISOString() };
    const copy = { id: "u1", text: "你好", images: [], ts: new Date(1_000_500).toISOString() };
    expect(settle(list, [older], 1_002_000).map((e) => e.id)).toEqual(["a", "b"]);
    const claimed = new Set<string>();
    expect(settle(list, [older, copy], 1_002_000, claimed).map((e) => e.id)).toEqual(["b"]);
    expect(settle(list.slice(1), [older, copy], 1_003_000, claimed).map((e) => e.id)).toEqual(["b"]);
  });

  it("goes when the hub ties an item to its id, whatever the item says; a tie to another bubble leaves it", () => {
    expect(arrived(entry({ id: "a", text: "/compact" }), [{ id: "n1", client_msg_id: "a", text: "压缩上下文", images: [] }])).toBe(true);
    expect(arrived(entry({ id: "a" }), [{ id: "u1", client_msg_id: "b", text: "你好", images: [] }])).toBe(false);
    const list = [entry({ id: "a", text: "继续" }), entry({ id: "b", text: "继续", at: 2000 })];
    const claimed = new Set<string>();
    expect(settle(list, [{ id: "u2", client_msg_id: "b", text: "继续", images: [] }], 3000, claimed).map((e) => e.id)).toEqual(["a"]);
    expect(settle(list, [{ id: "u1", text: "继续", images: [] }], 3000, claimed).map((e) => e.id)).toEqual(["b"]);
  });

  it("finds the prompt that started a conversation by its words (it went in without the bubble's id)", () => {
    noteSent("m", "s", { text: "帮我看看" });
    settleOutbox("m", "s", [{ id: "u1", client_msg_id: "hub-made", text: "帮我看看", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it("shows contexts as cards and images as images", () => {
    const m = outboxMessage(entry({ text: '<persona-context kind="context" title="项目" url="">档案</persona-context>\n\n问题', images: [{ mime: "image/png", data: "AQID" }] }));
    expect(m.text).toBe("问题");
    expect(m.contexts).toEqual([{ kind: "context", title: "项目", url: "", text: "档案" }]);
    expect(m.images[0]).toMatchObject({ kind: "image", role: "user", image: { mime: "image/png", data: "AQID" } });
  });
});

describe("sending from another page", () => {
  it.each([{ status: 200, state: "sent", sent: true }, { status: 409, state: "failed", sent: false }])("dispatches all 257 queued sends and retains the evicted first HTTP outcome ($state)", async ({ status, state, sent }) => {
    vi.useFakeTimers();
    try {
      let release!: () => void;
      const gate = inSendOrder("m/s", () => new Promise<void>(resolve => { release = resolve; }));
      const fetcher = vi.fn().mockResolvedValue(response({})).mockResolvedValueOnce(response(status === 200 ? {} : { error: "Synthetic send refusal", delivered: "none" }, status));
      vi.stubGlobal("fetch", fetcher);
      const jobs = Array.from({ length: 257 }, (_, i) => queueToSession("m", "s", { text: `Synthetic queued input ${i}` }));
      expect(fetcher).not.toHaveBeenCalled();
      expect(messageFeedback("m", "s", jobs[0].id)).toBeUndefined(); // the UI cache still has its original 256-entry limit
      release(); await gate;
      const results = await Promise.all(jobs.map(job => job.sent));
      expect(fetcher).toHaveBeenCalledTimes(257);
      expect(JSON.parse(fetcher.mock.calls[0][1].body).client_msg_id).toBe(jobs[0].id);
      expect(results[0]).toBe(sent); expect(results.slice(1).every(Boolean)).toBe(true);
      expect(outbox()["m/s"][0]).toMatchObject({ id: jobs[0].id, state });
      expect(messageFeedback("m", "s", jobs[0].id)).toBeUndefined();
    } finally { vi.useRealTimers(); }
  });
  it("shows the message at once and marks it sent", async () => {
    let resolve!: (v: unknown) => void;
    const fetcher = vi.fn().mockReturnValueOnce(new Promise((r) => (resolve = r)));
    vi.stubGlobal("fetch", fetcher);
    const job = sendToSession("m", "s", { text: "看一下" }, { launcher: "codex" });
    expect(outbox()["m/s"]).toHaveLength(1);
    expect(outbox()["m/s"][0].state).toBe("sending");
    const body = JSON.parse(fetcher.mock.calls[0][1].body);
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/sessions/m/s/messages");
    expect(body).toMatchObject({ text: "看一下", launcher: "codex", client_msg_id: outbox()["m/s"][0].id });
    resolve(response({ ok: true }));
    expect(await job).toBe(true);
    expect(outbox()["m/s"][0].state).toBe("sent");
    settleOutbox("m", "s", [{ text: "看一下", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it("says when the Agent reads it only after the question on the computer is answered (#5890)", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(response({ ok: true, waits_on_computer: true })).mockResolvedValueOnce(response({ ok: true })));
    expect(await sendToSession("m", "s", { text: "为什么没得点" })).toBe(true);
    expect(outbox()["m/s"][0].state).toBe("sent");
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("电脑上的问题回复后"), "info");
    vi.mocked(toast).mockClear();
    expect(await sendToSession("m", "s", { text: "还有一条" })).toBe(true);
    expect(toast).not.toHaveBeenCalled();
  });

  it("says when your own message went through a Claude's inbox although Persona's plugin is there", async () => {
    vi.stubGlobal("fetch", vi.fn()
      .mockResolvedValueOnce(response({ ok: true, delivered_as: "relayed", relay_reason: "busy" }))
      .mockResolvedValueOnce(response({ ok: true, delivered_as: "relayed", relay_reason: "plugin_unconfirmed" }))
      .mockResolvedValueOnce(response({ ok: true, delivered_as: "user_input" }))
      .mockResolvedValueOnce(response({ ok: true, delivered_as: "relayed", relay_reason: "no_plugin" }))
      .mockResolvedValueOnce(response({ ok: true, delivered_as: "relayed", relay_reason: "plugin_outdated" })));
    expect(await sendToSession("m", "s", { text: "可以合并" })).toBe(true);
    expect(toast).toHaveBeenLastCalledWith(t("lib-outbox.relayed_busy"), "info");
    expect(await sendToSession("m", "s", { text: "合并吧" })).toBe(true);
    expect(toast).toHaveBeenLastCalledWith(t("lib-outbox.relayed_plugin"), "info");
    vi.mocked(toast).mockClear();
    expect(await sendToSession("m", "s", { text: "合并" })).toBe(true); // in as your input: nothing to say
    expect(await sendToSession("m", "s", { text: "好" })).toBe(true); // no plugin there: it always used the inbox
    expect(toast).not.toHaveBeenCalled();
    // busy, and the session's plugin is from before busy delivery: reopening the session fixes it
    expect(await sendToSession("m", "s", { text: "继续" })).toBe(true);
    expect(toast).toHaveBeenLastCalledWith(t("lib-outbox.relayed_old_plugin"), "info");
  });

  it.each(retryPrompts)("keeps $kind after a refusal and retries as another operation without changing its intent", async ({ text }) => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ code: "input_draft", error: "draft", clearable: "old" }, 409))
      .mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    expect(await sendToSession("m", "s", { text }, { launcher: "claude" })).toBe(false);
    const failed = outbox()["m/s"][0];
    expect(failed).toMatchObject({ state: "failed", error: "电脑上的输入框中还有未发送的内容", clearable: "old" });
    expect(await retryOutbox("m", "s", failed.id, failed.clearable)).toBe(true);
    const again = JSON.parse(fetcher.mock.calls[1][1].body);
    expect(again.launcher).toBe("claude");
    expect(splitSkillDraft(again.text)).toEqual(splitSkillDraft(text));
    expect(again.client_msg_id).not.toBe(failed.id);
    expect(fetcher.mock.calls[1][1].headers.get("X-Persona-Op-Id")).toBe(again.client_msg_id);
    expect(again.clear_draft).toBe("old");
    discardOutbox("m", "s", again.client_msg_id);
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it("keeps an existing conversation's launcher and model on sends and Retry, and drops both on Default", async () => {
    const key = "m/s";
    const saved = { machine_id: "m", main: "s", launcher: "saved-launcher", model: "saved-model", conversations: [] };
    const originalConfig = structuredClone(saved);
    (getState() as { assistant: unknown }).assistant = saved;
    pickLauncher(key, undefined);
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ ok: true }))
      .mockResolvedValueOnce(response({ code: "input_draft", error: "draft", delivered: "none" }, 409))
      .mockResolvedValue(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    const send = (text: string) => sendToSession("m", "s", { text }, { launcher: pickedLauncher(key), model: pickedModel(key) });
    try {
      pickLauncher(key, "team-api");
      pickModel(key, "claude-sonnet-5-5");
      expect(await send("first")).toBe(true);
      expect(await send("second")).toBe(false);
      const failed = outbox()[key].find((e) => e.text === "second")!;
      expect(failed).toMatchObject({ state: "failed", launcher: "team-api", model: "claude-sonnet-5-5" });
      expect(await retryOutbox("m", "s", failed.id)).toBe(true);

      pickLauncher(key, undefined); // AssistantModelPanel.back: Default clears its model with the launcher.
      expect(pickedModel(key)).toBeUndefined();
      expect(await send("default")).toBe(true);

      const bodies = fetcher.mock.calls.map((call) => JSON.parse(call[1].body));
      expect(fetcher.mock.calls.map((call) => call[0])).toEqual(Array(4).fill("/api/v1/sessions/m/s/messages"));
      for (const [i, text] of ["first", "second", "second"].entries()) {
        expect(bodies[i]).toMatchObject({ text, launcher: "team-api", model: "claude-sonnet-5-5" });
      }
      expect(bodies[2].client_msg_id).not.toBe(bodies[1].client_msg_id);
      expect(bodies[3]).toMatchObject({ text: "default" });
      expect(bodies[3]).not.toHaveProperty("launcher");
      expect(bodies[3]).not.toHaveProperty("model");
      expect((getState() as { assistant: unknown }).assistant).toEqual(originalConfig);
    } finally {
      pickLauncher(key, undefined);
    }
  });

  it("checks an uncertain send under its original id instead of posting another message", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ error: "timeout", delivered: "unknown" }, 504));
    vi.stubGlobal("fetch", fetcher);
    await sendToSession("m", "s", { text: "x" });
    const id = outbox()["m/s"][0].id;
    fetcher.mockResolvedValueOnce(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/s/messages" }, state: "pending" }));
    expect(await retryOutbox("m", "s", id)).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(fetcher.mock.calls[1][0]).toBe(`/api/v1/ops/${id}`);
    expect(fetcher.mock.calls[1][1].method).toBeUndefined(); // GET, never another message POST
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: true, newId: false });
  });

  it("settles an applied check under the original id and keeps ordinary echo reconciliation", async () => {
    const id = "123e4567-e89b-42d3-a456-426614174000";
    keepFailed("m", "s", { id, text: "再发一次", images: [], at: Date.now() - 100_000 }, new ApiError(502, "x", { delivered: "unknown" }));
    const fetcher = vi.fn().mockResolvedValue(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/s/messages" }, state: "done" }));
    vi.stubGlobal("fetch", fetcher);
    const recheck = vi.fn();
    mountedRecheck("m", "s", recheck);
    expect(await checkOutbox("m", "s", id)).toBe("applied");
    expect(recheck).toHaveBeenCalledOnce();
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "sent", inDoubt: undefined });
    expect(fetcher).toHaveBeenCalledOnce();
    settleOutbox("m", "s", [{ id: "line-1", client_msg_id: id, text: "原消息已到", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it("notes a prompt that started a conversation as sent", () => {
    noteSent("m", "new", { text: "开始", images: [{ mime: "image/png", data: "x" }] });
    expect(outbox()["m/new"][0]).toMatchObject({ state: "sent", text: "开始" });
  });
});

describe("checking an uncertain send", () => {
  const id = "123e4567-e89b-42d3-a456-426614174001";
  const failed = () => keepFailed("m", "s", { id, text: "到了吗", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
  const receipt = (state: string, extra: Record<string, unknown> = {}) => response({ id, kind: "message", target: { path: "/api/v1/sessions/m/s/messages" }, state, ...extra });

  it.each([
    { name: "pending", state: "pending", status: 200 },
    { name: "unknown", state: "unknown", status: 200 },
    { name: "404", state: "missing", status: 404 },
    { name: "network failure", state: "network", status: 0 },
  ])("$name keeps the original id uncertain and performs no write", async ({ state, status }) => {
    failed();
    const fetcher = vi.fn();
    if (status === 0) fetcher.mockRejectedValue(new TypeError("offline"));
    else fetcher.mockResolvedValue(status === 404 ? response({ error: "missing" }, status) : receipt(state));
    vi.stubGlobal("fetch", fetcher);
    const recheck = vi.fn();
    mountedRecheck("m", "s", recheck);
    expect(await checkOutbox("m", "s", id)).toBe("unknown");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: true, newId: false });
    expect(recheck).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    expect(fetcher.mock.calls[0][1].method).toBeUndefined();
  });

  it("does not accept a receipt for another target", async () => {
    failed();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(receipt("done", { target: { path: "/api/v1/sessions/m/other/messages" } })));
    expect(await checkOutbox("m", "s", id)).toBe("unknown");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: true });
  });

  it.each([
    { state: "done", wanted: "applied", entry: { state: "sent", inDoubt: undefined } },
    { state: "failed", wanted: "not_applied", entry: { state: "failed", inDoubt: false, newId: true } },
  ])("accepts the hub's decoded target for an escaped session id: $state", async ({ state, wanted, entry }) => {
    const sid = "row/with/slashes", key = `m/${sid}`;
    keepFailed("m", sid, { id, text: "escaped target", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/row/with/slashes/messages" }, state })));
    expect(await checkOutbox("m", sid, id)).toBe(wanted);
    expect(outbox()[key][0]).toMatchObject({ id, ...entry });
  });

  it("rejects another decoded target for an escaped session id", async () => {
    const sid = "row/with/slashes", key = `m/${sid}`;
    keepFailed("m", sid, { id, text: "escaped target", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/row/with/other/messages" }, state: "done" })));
    expect(await checkOutbox("m", sid, id)).toBe("unknown");
    expect(outbox()[key][0]).toMatchObject({ id, state: "failed", inDoubt: true, newId: false });
  });

  it("accepts the hub's decoded target when the session id contains a literal percent escape", async () => {
    const sid = "row%2Fwith", key = `m/${sid}`;
    keepFailed("m", sid, { id, text: "literal percent", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/row%2Fwith/messages" }, state: "done" })));
    expect(await checkOutbox("m", sid, id)).toBe("applied");
    expect(outbox()[key][0]).toMatchObject({ id, state: "sent", inDoubt: undefined });
  });

  it("keeps literal percent escapes intact and still accepts the encoded compatibility target", async () => {
    const sid = "row%2Fwith", key = `m/${sid}`;
    keepFailed("m", sid, { id, text: "literal percent", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/row/with/messages" }, state: "done" }))
      .mockResolvedValueOnce(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/row%252Fwith/messages" }, state: "done" }));
    vi.stubGlobal("fetch", fetcher);
    expect(await checkOutbox("m", sid, id)).toBe("unknown");
    expect(outbox()[key][0]).toMatchObject({ id, state: "failed", inDoubt: true });
    expect(await checkOutbox("m", sid, id)).toBe("applied");
    expect(outbox()[key][0]).toMatchObject({ id, state: "sent", inDoubt: undefined });
    expect(fetcher).toHaveBeenCalledTimes(2);
  });

  // An uncertain send the receipt says a dialog refused (#7343): the failed bubble carries dialog_open, as a live
  // refusal does, so the terminal button shows the dialog waits; a receipt without a code leaves none.
  it("keeps the code of a definite failure the receipt saved", async () => {
    failed();
    const body = (b: Record<string, unknown>) => Buffer.from(JSON.stringify(b)).toString("base64");
    const fetcher = vi.fn().mockResolvedValueOnce(receipt("failed", { result: { status: 409, body: body({ error: "a dialog is open", code: "dialog_open", delivered: "none" }) } }));
    vi.stubGlobal("fetch", fetcher);
    expect(await checkOutbox("m", "s", id)).toBe("not_applied");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: false, newId: true, code: "dialog_open" });
    delete outbox()["m/s"];
    failed();
    fetcher.mockResolvedValueOnce(receipt("failed", { result: { status: 400, body: body({ error: "会话已结束", delivered: "none" }) } }));
    expect(await checkOutbox("m", "s", id)).toBe("not_applied");
    expect(outbox()["m/s"][0].code).toBeUndefined();
  });

  it("a definite failure restores ordinary Retry, which then uses a new id", async () => {
    failed();
    const body = Buffer.from(JSON.stringify({ error: "会话已结束", delivered: "none" })).toString("base64");
    const fetcher = vi.fn().mockResolvedValueOnce(receipt("failed", { result: { status: 400, body } }));
    vi.stubGlobal("fetch", fetcher);
    expect(await checkOutbox("m", "s", id)).toBe("not_applied");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: false, newId: true, error: "会话已结束" });

    fetcher.mockResolvedValueOnce(response({ ok: true }));
    expect(await retryOutbox("m", "s", id)).toBe(true);
    expect(fetcher.mock.calls[1][0]).toBe("/api/v1/sessions/m/s/messages");
    expect(JSON.parse(fetcher.mock.calls[1][1].body).client_msg_id).not.toBe(id);
  });

  it("a replacement view receives an in-flight Check without a second click", async () => {
    failed();
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const unmounted = vi.fn(), remounted = vi.fn();
    const leave = mountedRecheck("m", "s", unmounted);
    const checking = checkOutbox("m", "s", id);
    mountedRecheck("m", "s", remounted);
    leave(); // an old view's cleanup cannot remove the replacement's registration
    answer(receipt("pending"));
    expect(await checking).toBe("unknown");
    expect(unmounted).not.toHaveBeenCalled();
    expect(remounted).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: true, newId: false });
  });

  it("all mounted views receive the same in-flight Check", async () => {
    failed();
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const first = vi.fn(), second = vi.fn();
    mountedRecheck("m", "s", first);
    mountedRecheck("m", "s", second);
    const checking = checkOutbox("m", "s", id);
    answer(receipt("pending"));
    expect(await checking).toBe("unknown");
    expect(first).toHaveBeenCalledOnce();
    expect(second).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("unmounting one of two views keeps the other registered for its in-flight Check", async () => {
    failed();
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const remaining = vi.fn(), closed = vi.fn();
    mountedRecheck("m", "s", remaining);
    const unmount = mountedRecheck("m", "s", closed);
    const checking = checkOutbox("m", "s", id);
    unmount();
    answer(receipt("pending"));
    expect(await checking).toBe("unknown");
    expect(remaining).toHaveBeenCalledOnce();
    expect(closed).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("a second click joins the same in-flight read", async () => {
    failed();
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const recheck = vi.fn();
    mountedRecheck("m", "s", recheck);
    const checking = checkOutbox("m", "s", id);
    expect(checkOutbox("m", "s", id)).toBe(checking);
    answer(receipt("pending"));
    expect(await checking).toBe("unknown");
    expect(recheck).toHaveBeenCalledOnce();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("an unmounted view is not called when its in-flight Check completes", async () => {
    failed();
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const recheck = vi.fn();
    const unmount = mountedRecheck("m", "s", recheck);
    const checking = checkOutbox("m", "s", id);
    unmount();
    answer(receipt("pending"));
    expect(await checking).toBe("unknown");
    expect(recheck).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledOnce();
  });

  it("a late applied answer cannot overwrite a replacement or another conversation", async () => {
    failed();
    const other = "123e4567-e89b-42d3-a456-426614174002";
    keepFailed("m", "other", { id: other, text: "别的会话", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    let answer!: (value: unknown) => void;
    const fetcher = vi.fn().mockReturnValue(new Promise((resolve) => { answer = resolve; }));
    vi.stubGlobal("fetch", fetcher);
    const recheck = vi.fn();
    mountedRecheck("m", "s", recheck);
    const checking = checkOutbox("m", "s", id);
    discardOutbox("m", "s", id);
    keepFailed("m", "s", { id, text: "后来重新写的", images: [], at: 2000 }, new ApiError(502, "x", { delivered: "unknown" }));
    answer(receipt("done"));
    expect(await checking).toBe("stale");
    expect(outbox()["m/s"][0]).toMatchObject({ id, text: "后来重新写的", state: "failed", inDoubt: true });
    expect(outbox()["m/other"][0]).toMatchObject({ id: other, text: "别的会话", state: "failed", inDoubt: true });
    expect(recheck).not.toHaveBeenCalled();
    expect(fetcher).toHaveBeenCalledOnce();
  });
});

describe("问 Persona", () => {
  it("opens the main conversation at once and sends there, with its picked launcher", async () => {
    (getState() as { assistant: unknown }).assistant = { machine_id: "hub", main: "main1", conversations: [] };
    pickLauncher("hub/main1", "codex");
    const fetcher = vi.fn().mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    await askPersona("说说进展");
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "main1" });
    expect(outbox()["hub/main1"][0].text).toBe("说说进展");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toMatchObject({ text: "说说进展", launcher: "codex" });
    pickLauncher("hub/main1", undefined);
  });

  it("starts the main conversation when there is none, and shows the prompt there", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ session: { machine_id: "hub", session_id: "new1" } }));
    vi.stubGlobal("fetch", fetcher);
    await askPersona("说说进展");
    expect(fetcher.mock.calls[0][0]).toBe("/api/v1/assistant/conversations");
    expect(JSON.parse(fetcher.mock.calls[0][1].body)).toEqual({ prompt: "说说进展", main: true });
    expect(outbox()["hub/new1"][0]).toMatchObject({ state: "sent", text: "说说进展" });
    expect(navigate).toHaveBeenCalledWith({ name: "persona", sid: "new1" });
  });
});

describe("where a failed message goes (#1122)", () => {
  // an exchange whose message was sent from here (`cmid`, written at `at`) or typed elsewhere (`ts` only)
  const line = (id: string, user: { cmid?: string; ts?: number }): ChatLine =>
    ({ kind: "segment", id, live: false, seg: { id, steps: [], tools: 0, thoughts: 0, replies: 0, folds: [], user: { id: `${id}u`, text: "x", images: [], contexts: [], client_msg_id: user.cmid, ts: user.ts === undefined ? undefined : new Date(user.ts).toISOString() } } }) as ChatLine;

  it("goes right before the first message written after it; with none after, and while sending, at the end", () => {
    noteWritten("later", 5_000_000);
    const lines = [line("l1", { ts: 1_000_000 }), line("l2", { cmid: "later" }), line("l3", { cmid: "later2" })];
    const failed = entry({ id: "f", state: "failed", at: 4_000_000 });
    const last = entry({ id: "g", state: "failed", at: 9_000_000 });
    const sending = entry({ id: "s", state: "sending", at: 2_000_000 });
    const { before, rest } = placeFailed(lines, [last, failed, sending]);
    expect([...before.entries()].map(([id, es]) => [id, es.map((e) => e.id)])).toEqual([["l2", ["f"]]]);
    expect(rest.map((e) => e.id)).toEqual(["s", "g"]);
  });

  it("goes by the computer's time for a message typed there, with two minutes' slack for its clock", () => {
    const failed = entry({ id: "f", state: "failed", at: 10_000_000 });
    expect(placeFailed([line("l1", { ts: 10_000_000 + 60_000 })], [failed]).rest.map((e) => e.id)).toEqual(["f"]);
    expect([...placeFailed([line("l1", { ts: 10_000_000 + 180_000 })], [failed]).before.keys()]).toEqual(["l1"]);
  });

  it("keeps failed real messages outside a quiet supervision wrapper at its original input time", () => {
    const quiet: ChatLine = { kind: "supervision", id: "quiet", live: false, lines: [line("child", { ts: 10_000_000 + 180_000 })] };
    const failed = entry({ id: "f", state: "failed", at: 10_000_000 });
    const placed = placeFailed([quiet], [failed]);
    expect([...placed.before.keys()]).toEqual(["quiet"]);
    expect(placed.rest).toEqual([]);
  });

  it("keepFailed puts the composer's message in the outbox as failed, with why", () => {
    keepFailed("m", "s", { id: "k1", text: "你好", images: [], at: 1000 }, new Error("断了"));
    expect(outbox()["m/s"]).toEqual([expect.objectContaining({ id: "k1", state: "failed", error: "断了", text: "你好" })]);
  });
});

describe("messages kept across a reload", () => {
  const store = new Map<string, string>();
  const fakeStorage = {
    get length() { return store.size; },
    key: (i: number) => [...store.keys()][i] ?? null,
    getItem: (k: string) => store.get(k) ?? null,
    setItem: (k: string, v: string) => void store.set(k, v),
    removeItem: (k: string) => void store.delete(k),
  };
  const reload = () => {
    delete outbox()["m/s"];
    resetOutboxClaimsForTests();
    resetRestoredForTests();
  };
  beforeEach(() => {
    store.clear();
    delete outbox()["m/s"];
    resetOutboxClaimsForTests();
    resetRestoredForTests();
    vi.stubGlobal("localStorage", fakeStorage);
  });

  it.each(retryPrompts)("restores $kind after a reload, and Retry preserves the same identity and payload", async ({ text }) => {
    keepFailed("m", "s", { id: "a1", text, images: [], at: 1000, launcher: "codex" }, new TypeError("Failed to fetch"));
    expect(store.size).toBe(1);
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "a1", text, launcher: "codex", state: "failed", restored: true }]);
    const fetcher = vi.fn().mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    expect(await retryOutbox("m", "s", "a1")).toBe(true);
    const retried = JSON.parse(fetcher.mock.calls[0][1].body);
    expect(retried).toMatchObject({ client_msg_id: "a1", launcher: "codex" });
    expect(splitSkillDraft(retried.text)).toEqual(splitSkillDraft(text));
    expect(store.size).toBe(1); // accepted, still waiting for its transcript copy
    settleOutbox("m", "s", [{ id: "accepted-retry", client_msg_id: "a1", text: "accepted", images: [] }]);
    expect(store.size).toBe(0);
  });

  const dialog = "Choose a workspace";
  const refusal = { code: "dialog_open", delivered: "none", dialog, machine_id: "other-box", session_id: "blocked-session" };
  const blocker = { dialog, dialogMachine: "other-box", dialogSession: "blocked-session" };

  it("keeps the dialog and its terminal across a reload and clears them on successful retry", async () => {
    const refused = new ApiError(409, "a dialog is open", refusal);
    keepFailed("m", "s", { id: "d1", text: "go on", images: [], at: 1000 }, refused);
    expect(outbox()["m/s"]).toMatchObject([{ id: "d1", state: "failed", code: "dialog_open", ...blocker }]);
    expect(failureText(refused)).toContain(dialog);
    reload();
    restoreOutbox("m", "s");
    const [restored] = outbox()["m/s"];
    expect(restored).toMatchObject({ id: "d1", state: "failed", code: "dialog_open", restored: true, ...blocker });
    expect(entryReason(restored)).toContain(dialog);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(response({ ok: true })));
    expect(await retryOutbox("m", "s", "d1")).toBe(true);
    const sent = outbox()["m/s"].find((e) => e.text === "go on")!;
    expect(sent).toMatchObject({ state: "sent" });
    const kept = [...store.values()].map((v) => JSON.parse(v) as Partial<OutboxEntry>);
    expect(kept.length).toBe(1);
    for (const key of ["code", "dialog", "dialogMachine", "dialogSession"] as const) {
      expect(sent[key]).toBeUndefined();
      expect(kept[0]).not.toHaveProperty(key);
    }
  });

  it("restores the dialog and its terminal when Check finds a definite refusal", async () => {
    const id = "123e4567-e89b-42d3-a456-426614174001";
    keepFailed("m", "s", { id, text: "go on", images: [], at: 1000 }, new ApiError(502, "no response", { delivered: "unknown" }));
    const body = Buffer.from(JSON.stringify({ error: "a dialog is open", ...refusal })).toString("base64");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(response({
      id, kind: "message", target: { path: "/api/v1/sessions/m/s/messages" }, state: "failed", result: { status: 423, body },
    })));
    expect(await checkOutbox("m", "s", id)).toBe("not_applied");
    reload();
    restoreOutbox("m", "s");
    const [restored] = outbox()["m/s"];
    expect(restored).toMatchObject({ state: "failed", inDoubt: false, code: "dialog_open", ...blocker });
    expect(entryReason(restored)).toContain(dialog);
  });

  it.each([undefined, "", "  ", 42])("keeps the existing fallback for dialog title %s", (title) => {
    const refused = new ApiError(409, "a dialog is open", { code: "dialog_open", delivered: "none", dialog: title });
    keepFailed("m", "s", { id: "d1", text: "go on", images: [], at: 1000 }, refused);
    reload();
    restoreOutbox("m", "s");
    expect(entryReason(outbox()["m/s"][0])).toBe(t("lib-outbox.dialog_open"));
  });

  it("shows the title and the missing-images warning when only the message text survived a reload", () => {
    keepFailed("m", "s", { id: "d1", text: "go on", images: [{ mime: "image/png", data: "AA==" }], at: 1000 }, new ApiError(409, "a dialog is open", refusal));
    reload();
    restoreOutbox("m", "s");
    const [restored] = outbox()["m/s"];
    expect(entryReason(restored)).toContain(dialog);
    expect(entryReason(restored)).toContain(t("lib-outbox.not_confirmed_images"));
  });

  it("restores an accepted message as pending with its original id, never resends, and settles once", async () => {
    const fetcher = vi.fn().mockResolvedValue(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    await sendToSession("m", "s", { text: "Is the museum open?" });
    const { id, at } = outbox()["m/s"][0];
    expect(store.size).toBe(1);
    reload();
    restoreOutbox("m", "s");
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id, at, state: "sent", restored: true, error: undefined }]);
    expect(await retryOutbox("m", "s", id)).toBe(false);
    expect(fetcher).toHaveBeenCalledTimes(1);
    settleOutbox("m", "s", [{ id: "accepted-copy", client_msg_id: id, text: "Is the museum open?", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.size).toBe(0);
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it.each(["sending", "failed", undefined] as const)("restores another tab's %s entry only on the first visit per page", (state) => {
    restoreOutbox("m", "s"); // this page opened before the other tab started its send
    keepOutgoing("m/s", entry({ id: "other-tab", text: "Still in the other tab", state, at: Date.now() }));
    restoreOutbox("m", "s");
    settleOutbox("m", "s", [{ id: "unrelated-copy", text: "A different message", images: [] }]);
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.has("outbox-msg:m/s/other-tab")).toBe(true);

    // Another conversation still gets its own first restoration.
    keepOutgoing("m/other", entry({ id: "first-visit", state, at: Date.now() }));
    restoreOutbox("m", "other");
    expect(outbox()["m/other"]).toMatchObject([{ id: "first-visit", state: "failed", restored: true }]);

    reload();
    restoreOutbox("m", "s");
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "other-tab", state: "failed", restored: true }]);
  });

  it("restores a newly accepted entry after the first visit and again on navigation without resending", async () => {
    const fetcher = vi.fn();
    vi.stubGlobal("fetch", fetcher);
    restoreOutbox("m", "s");
    keepOutgoing("m/s", entry({ id: "later-accepted", text: "Accepted in another tab", state: "sending", at: Date.now() }));
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toBeUndefined();
    acceptOutgoing("m/s", "later-accepted");
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "later-accepted", state: "sent" }]);
    delete outbox()["m/s"]; // revisit in the same page: the first-visit guard stays set
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "later-accepted", state: "sent" }]);
    expect(await retryOutbox("m", "s", "later-accepted")).toBe(false);
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("matches an untagged copy after a slow acceptance and reload using the original send time", () => {
    const at = Date.now() - 180_000;
    keepOutgoing("m/s", entry({ id: "slow", text: "A slow reply", at, state: "sending" }));
    acceptOutgoing("m/s", "slow");
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "slow", at, state: "sent" }]);
    settleOutbox("m", "s", [{ id: "slow-copy", text: "A slow reply", images: [], ts: new Date(at).toISOString() }]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.size).toBe(0);
  });

  it("expires accepted copies without extending their grace on reload, leaving unconfirmed sends alone", () => {
    const clock = vi.spyOn(Date, "now").mockReturnValue(1000);
    try {
      keepOutgoing("m/s", entry({ id: "accepted", state: "sending" }));
      acceptOutgoing("m/s", "accepted");
      keepOutgoing("m/s", entry({ id: "unconfirmed", state: "sending" }));
      clock.mockReturnValue(1000 + SENT_GRACE_MS - 1);
      restoreOutbox("m", "s");
      expect(outbox()["m/s"]).toMatchObject([{ id: "accepted", at: 1000, state: "sent" }, { id: "unconfirmed", state: "failed" }]);
      reload();
      clock.mockReturnValue(1000 + SENT_GRACE_MS);
      restoreOutbox("m", "s");
      expect(outbox()["m/s"]).toMatchObject([{ id: "unconfirmed", state: "failed" }]);
      expect(store.has("outbox-msg:m/s/accepted")).toBe(false);
      expect(store.has("outbox-msg:m/s/unconfirmed")).toBe(true);
    } finally {
      clock.mockRestore();
    }
  });

  it("does not revive an echo settled before acceptance or overwrite another tab's cleanup", () => {
    keepOutgoing("m/s", entry({ id: "early", state: "sending" }));
    restoreOutbox("m", "s");
    settleOutbox("m", "s", [{ id: "early-copy", client_msg_id: "early", text: "copy", images: [] }]);
    acceptOutgoing("m/s", "early");
    expect(store.size).toBe(0);

    noteSent("m", "s", { text: "Accepted in this tab" });
    const accepted = outbox()["m/s"][0];
    forgetOutgoing("m/s", accepted.id); // another tab saw the transcript first
    keepFailed("m", "s", { id: "failed", text: "Another message", images: [], at: Date.now() }, new TypeError("Failed to fetch"));
    expect(store.has(`outbox-msg:m/s/${accepted.id}`)).toBe(false);
    expect(store.has("outbox-msg:m/s/failed")).toBe(true);
  });

  it("keeps sessions separate and restores prompts that must match by words", () => {
    const at = Date.now();
    keepOutgoing("m/other", entry({ id: "same", at, text: "Other conversation" }));
    keepOutgoing("m/s", entry({ id: "same", at, text: "This conversation" }));
    restoreOutbox("m", "s");
    settleOutbox("m", "s", [{ id: "session-copy", client_msg_id: "same", text: "This conversation", images: [] }]);
    expect(store.has("outbox-msg:m/other/same")).toBe(true);
    expect(store.has("outbox-msg:m/s/same")).toBe(false);

    noteSent("m", "s", { text: "Start a conversation" });
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ state: "sent", wordsOnly: true }]);
    settleOutbox("m", "s", [{ id: "start-copy", client_msg_id: "hub-generated", text: "Start a conversation", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.size).toBe(1); // only the other conversation
  });

  it("a restored uncertain send keeps its reason and Check, never a new id", () => {
    keepFailed("m", "s", { id: "a6", text: "到了吗", images: [], at: 1000 }, new ApiError(502, "x", { delivered: "unknown" }));
    const reason = outbox()["m/s"][0].error;
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "a6", error: reason, inDoubt: true, newId: false }]);
  });

  it("persists a real unknown failure as Check and its definite check result as Retry across reloads", async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(response({ error: "lost answer", delivered: "unknown" }, 502));
    vi.stubGlobal("fetch", fetcher);
    expect(await sendToSession("m", "s", { text: "keep this uncertainty" })).toBe(false);
    const { id, at } = outbox()["m/s"][0], reason = outbox()["m/s"][0].error;
    const saved = () => JSON.parse(store.get(`outbox-msg:m/s/${id}`)!);
    expect(saved()).toMatchObject({ id, state: "failed", inDoubt: true, newId: false, error: reason });

    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", restored: true, inDoubt: true, newId: false, error: reason });

    const body = Buffer.from(JSON.stringify({ error: "会话已结束", delivered: "none" })).toString("base64");
    fetcher.mockResolvedValueOnce(response({ id, kind: "message", target: { path: "/api/v1/sessions/m/s/messages" }, state: "failed", result: { status: 400, body } }));
    expect(await checkOutbox("m", "s", id)).toBe("not_applied");
    expect(saved()).toMatchObject({ id, state: "failed", inDoubt: false, newId: true, error: "会话已结束" });
    const older = { id: "older-identical", text: "keep this uncertainty", images: [], ts: new Date(at - 60_000).toISOString() };
    settleOutbox("m", "s", [older]);
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", inDoubt: false, newId: true, error: "会话已结束" });
    expect(saved()).toMatchObject({ id, state: "failed", inDoubt: false, newId: true });

    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", restored: true, inDoubt: false, newId: true, error: "会话已结束" });
    settleOutbox("m", "s", [older]);
    expect(outbox()["m/s"][0]).toMatchObject({ id, state: "failed", restored: true, inDoubt: false, newId: true, error: "会话已结束" });
    expect(saved()).toMatchObject({ id, state: "failed", inDoubt: false, newId: true });
    fetcher.mockResolvedValueOnce(response({ ok: true }));
    expect(await retryOutbox("m", "s", id)).toBe(true);
    const retry = fetcher.mock.calls.at(-1)!;
    expect(retry[0]).toBe("/api/v1/sessions/m/s/messages");
    expect(JSON.parse(retry[1].body)).toMatchObject({ text: "keep this uncertainty", client_msg_id: expect.not.stringMatching(id) });
    expect(store.has(`outbox-msg:m/s/${id}`)).toBe(false);
    const retried = outbox()["m/s"][0];
    expect(retried).toMatchObject({ state: "sent", text: "keep this uncertainty" });
    expect(retried.id).not.toBe(id);
    const persisted = JSON.parse(store.get(`outbox-msg:m/s/${retried.id}`)!);
    expect(persisted).toMatchObject({ id: retried.id, state: "sent" });
    expect(persisted).not.toHaveProperty("newId");
    expect(persisted).not.toHaveProperty("inDoubt");
  });

  it("a restored same-id unconfirmed send still settles on a valid current echo", () => {
    const at = Date.now();
    keepOutgoing("m/s", entry({ id: "same-id-restored", text: "Continue", at, state: "sending" }));
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"][0]).toMatchObject({ id: "same-id-restored", state: "failed", restored: true });
    expect(outbox()["m/s"][0].newId).not.toBe(true);
    settleOutbox("m", "s", [{ id: "current-copy", text: "Continue", images: [], ts: new Date(at + 1000).toISOString() }]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.has("outbox-msg:m/s/same-id-restored")).toBe(false);
  });

  it("a message whose send timed out but went in shows as sent, not as failed to send again", () => {
    // The hub answered 502 delivered "unknown" after its 90 s (the node wrote the message after it stopped waiting).
    keepFailed("m", "s", { id: "a7", text: "前端页面上也有多次显示发送失败", images: [], at: Date.now() }, new ApiError(502, "x", { delivered: "unknown" }));
    expect(outbox()["m/s"]).toMatchObject([{ id: "a7", state: "failed", inDoubt: true }]);
    // Its line reaches the transcript a little later, with no tie from the hub.
    settleOutbox("m", "s", [{ id: "line-9", text: "前端页面上也有多次显示发送失败", images: [], ts: new Date(Date.now() + 30_000).toISOString() }]);
    expect(outbox()["m/s"]).toBeUndefined();
  });

  it("a message still sending when the page closed comes back too", () => {
    keepOutgoing("m/s", { id: "a2", text: "发出去了吗", images: [], at: 2000 });
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "a2", state: "failed", restored: true }]);
  });

  it("does not show twice when the transcript has it, by its id or by its words", () => {
    keepOutgoing("m/s", { id: "a3", text: "第一条消息内容", images: [], at: 2000 });
    keepOutgoing("m/s", { id: "a4", text: "第二条消息内容", images: [], at: 2000 });
    restoreOutbox("m", "s");
    settleOutbox("m", "s", [
      { id: "u1", client_msg_id: "a3", text: "第一条消息内容", images: [] },
      { id: "u2", text: "第二条消息内容", images: [], ts: new Date(3000).toISOString() },
    ]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.size).toBe(0);
  });

  it("keeps Clear and send available after a reload", async () => {
    const fetcher = vi.fn()
      .mockResolvedValueOnce(response({ code: "input_draft", error: "draft", clearable: "old terminal draft" }, 409))
      .mockResolvedValueOnce(response({ ok: true }));
    vi.stubGlobal("fetch", fetcher);
    expect(await sendToSession("m", "s", { text: "message from the web" })).toBe(false);
    const failed = outbox()["m/s"][0];
    expect(failed).toMatchObject({ state: "failed", clearable: "old terminal draft", newId: true });
    reload();
    restoreOutbox("m", "s");
    const restored = outbox()["m/s"][0];
    expect(await retryOutbox("m", "s", restored.id, restored.clearable)).toBe(true);
    const again = JSON.parse(fetcher.mock.calls[1][1].body);
    expect(again).toMatchObject({ text: "message from the web", clear_draft: "old terminal draft" });
    expect(again.client_msg_id).not.toBe(failed.id);
    expect(restored.clearable).toBe("old terminal draft");
  });

  it("forgets a deleted message", () => {
    keepFailed("m", "s", { id: "a5", text: "别发了", images: [], at: 1000 }, new TypeError("Failed to fetch"));
    discardOutbox("m", "s", "a5");
    expect(store.size).toBe(0);
  });

  it("puts a deleted message back where it was on Undo, kept for reloads again (#4626)", () => {
    keepFailed("m", "s", { id: "a6", text: "第一条", images: [], at: 1000 }, new TypeError("Failed to fetch"));
    keepFailed("m", "s", { id: "a7", text: "第二条", images: [], at: 2000 }, new TypeError("Failed to fetch"));
    const undo = discardOutbox("m", "s", "a6")!;
    expect(outbox()["m/s"].map((e) => e.id)).toEqual(["a7"]);
    expect(store.size).toBe(1);
    undo();
    expect(outbox()["m/s"].map((e) => e.id)).toEqual(["a6", "a7"]);
    expect(outbox()["m/s"][0]).toMatchObject({ state: "failed", text: "第一条" });
    expect(store.size).toBe(2);
    undo(); // there already: no second copy
    expect(outbox()["m/s"]).toHaveLength(2);
    expect(discardOutbox("m", "s", "gone")).toBeUndefined();
  });

  it("keeps attached images in memory across same-tab navigation without persisting image bytes to localStorage", () => {
    const imgs = [
      { mime: "image/jpeg", data: "AAAA" },
      { mime: "image/png", data: "BBBB" },
    ];
    const at = Date.now();
    restoreOutbox("m", "s");
    keepOutgoing("m/s", { id: "img-msg", text: "Check these two screenshots", images: imgs, at });
    acceptOutgoing("m/s", "img-msg", at);
    const saved = JSON.parse(store.get("outbox-msg:m/s/img-msg")!);
    expect(saved.images).toBe(2);
    expect(store.get("outbox-msg:m/s/img-msg")).not.toContain("AAAA");
    expect(store.get("outbox-msg:m/s/img-msg")).not.toContain("BBBB");

    // Client-side switch away and back (same tab: in-memory images survive).
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "img-msg", text: "Check these two screenshots", state: "sent", images: imgs }]);

    // Once settled by the transcript echo, both localStorage and in-memory images are cleaned up.
    settleOutbox("m", "s", [{ id: "echo-1", client_msg_id: "img-msg", text: "Check these two screenshots", images: [] }]);
    expect(outbox()["m/s"]).toBeUndefined();
    expect(store.size).toBe(0);

    // After a full page reload (in-memory cleared), an unconfirmed message with images still reports lost images.
    keepOutgoing("m/s", { id: "img-reload", text: "Lost after reload", images: imgs, at, state: "sending" });
    reload();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "img-reload", images: [], state: "failed", restored: true, error: t("lib-outbox.not_confirmed_images") }]);
  });

  it("preserves an image-only message across same-tab navigation without writing to localStorage", () => {
    const imgs = [{ mime: "image/jpeg", data: "CCCC" }];
    const at = Date.now();
    restoreOutbox("m", "s");
    keepOutgoing("m/s", { id: "img-only", text: "", images: imgs, at });
    acceptOutgoing("m/s", "img-only", at);
    expect(store.size).toBe(0);

    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toMatchObject([{ id: "img-only", text: "", state: "sent", images: imgs }]);

    settleOutbox("m", "s", [{ id: "echo-only", client_msg_id: "img-only", text: "", images: [{}] }]);
    expect(outbox()["m/s"]).toBeUndefined();
    restoreOutbox("m", "s");
    expect(outbox()["m/s"]).toBeUndefined();
  });
});

describe("inSendOrder", () => {
  it("stops waiting for a POST that hung", async () => {
    vi.useFakeTimers();
    try {
      void inSendOrder("hung", () => new Promise(() => {}));
      const next = vi.fn(async () => {});
      void inSendOrder("hung", next);
      await vi.advanceTimersByTimeAsync(5 * 60_000);
      expect(next).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(60_000);
      expect(next).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("runs a conversation's sends one at a time, past a failure", async () => {
    let fail!: (e: Error) => void;
    const first = inSendOrder("k", () => new Promise((_, reject) => (fail = reject)));
    const second = vi.fn(async () => "two");
    const after = inSendOrder("k", second);
    await Promise.resolve();
    expect(second).not.toHaveBeenCalled();
    fail(new Error("no"));
    await expect(first).rejects.toThrow("no");
    await expect(after).resolves.toBe("two");
  });
});
