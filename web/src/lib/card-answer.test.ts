// answerCard (#4464): the one answer for a card, from its own buttons and from a notification's Allow / Deny. The approve
// check, what it sends, and how each hub answer comes back.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import type { Interaction, Session } from "./types";

let session: Session | undefined;
const apiOp = vi.fn();
const ended: unknown[][] = [];
vi.mock("./ops", () => ({ apiOp: (...a: unknown[]) => apiOp(...a) }));
vi.mock("./store", () => ({ getSession: () => session, endInteraction: (...a: unknown[]) => ended.push(a) }));

const { answerCard } = await import("./card-answer");
const { allowBody, denyBody } = await import("./quick-answers");

const ix: Interaction = { id: "p1", machine_id: "m", session_id: "a", kind: "permission", tool_name: "Bash", created_at: "2026-01-02T03:04:05.000000001Z" };

beforeEach(() => {
  session = { machine_id: "m", session_id: "a" } as Session;
  apiOp.mockReset();
  ended.length = 0;
});

describe("answerCard", () => {
  it("会话不让批：不发，说为什么", async () => {
    session = { machine_id: "m", session_id: "a", source_kind: "codex_history", capabilities: { approve: false } } as Session;
    expect(await answerCard(ix, allowBody())).toEqual({ ok: false, error: "此会话不允许审批" });
    session = undefined;
    expect(await answerCard(ix, allowBody())).toEqual({ ok: false, error: "此会话不允许审批" });
    expect(apiOp).not.toHaveBeenCalled();
  });

  it("a stopped Agent's work card sends only local defer/dismiss answers", async () => {
    session = { machine_id: "m", session_id: "a", status: "offline", capabilities: { read: true, terminal: true, approve: false, send: false } } as Session;
    const work: Interaction = {
      ...ix, kind: "question", tool_name: "persona_triage",
      questions: [{ question: "What next?", options: [{ label: "Continue" }, { label: "Leave it", action: "defer" }] }],
    };
    for (const answer of ["Continue", "A custom reply"]) {
      expect(await answerCard(work, { answers: { "What next?": answer } })).toEqual({ ok: false, error: "此会话不允许审批" });
    }
    expect(await answerCard({ ...ix, tool_name: "persona_triage" }, { dismiss: true })).toEqual({ ok: false, error: "此会话不允许审批" }); // a permission card is unchanged, even with the same tool name
    expect(apiOp).not.toHaveBeenCalled();

    apiOp.mockResolvedValue({});
    expect(await answerCard(work, { answers: { "What next?": "Leave it" } })).toEqual({ ok: true, already: false });
    expect(await answerCard(work, { dismiss: true })).toEqual({ ok: true, already: false });
    expect(apiOp).toHaveBeenCalledTimes(2);
  });

  it("答上：发到卡片的 resolve，卡片记下答了什么", async () => {
    apiOp.mockResolvedValue({});
    expect(await answerCard(ix, denyBody())).toEqual({ ok: true, already: false });
    expect(apiOp).toHaveBeenCalledWith("/interactions/p1/resolve", { method: "POST", json: { allow: false, message: "用户已拒绝" }, headers: { "X-Persona-Interaction-Created-At": ix.created_at } });
    expect(ended).toEqual([["p1", { ix, resolution: { allow: false, message: "用户已拒绝" }, already: false, handedBack: false }]]);
  });

  it("leaves a handed-back receipt for a fresh hand back, as for an answer", async () => {
    apiOp.mockResolvedValue({ ok: true });
    expect(await answerCard(ix, { dismiss: true })).toEqual({ ok: true, already: false });
    expect(apiOp).toHaveBeenCalledWith("/interactions/p1/resolve", { method: "POST", json: { dismiss: true }, headers: { "X-Persona-Interaction-Created-At": ix.created_at } });
    expect(ended).toEqual([["p1", { ix, resolution: { dismiss: true }, already: false, handedBack: true }]]);
  });

  it("别处已经答过：按 hub 说的答案记下，already", async () => {
    apiOp.mockResolvedValue({ already: true, resolution: { allow: true } });
    expect(await answerCard(ix, denyBody())).toEqual({ ok: true, already: true });
    expect(ended).toEqual([["p1", { ix, resolution: { allow: true }, already: true, handedBack: false }]]);
  });

  it("410：卡已经结束，收起，说结束的原因", async () => {
    apiOp.mockRejectedValue(new ApiError(410, "gone", { status: "expired" }));
    const r = await answerCard(ix, allowBody());
    expect(r.ok).toBe(false);
    expect(ended).toEqual([["p1", undefined, ix.created_at]]);
  });

  it("prompt_changed 和别的错误：不收卡，说原因", async () => {
    apiOp.mockRejectedValue(new ApiError(409, "changed", { code: "prompt_changed" }));
    expect(await answerCard(ix, allowBody())).toEqual({ ok: false, error: "电脑上的对话框内容已更新，已根据当前屏幕重新生成卡片。" });
    apiOp.mockRejectedValue(new ApiError(500, "boom", {}));
    expect(await answerCard(ix, allowBody())).toEqual({ ok: false, error: expect.stringMatching(/^未能送达：.+（HTTP 500: boom）$/) });
    expect(ended).toEqual([]);
  });
});

it("keeps one answer per card on its way: the same one shares it, another is refused unsent, both free once it ends", async () => {
  let finish!: (e: Error) => void;
  apiOp.mockReturnValueOnce(new Promise((_, no) => { finish = no; }));
  const first = answerCard(ix, allowBody());
  expect(answerCard(ix, allowBody())).toBe(first);
  expect(await answerCard(ix, denyBody())).toEqual({ ok: false, error: "此卡片的回复正在发送中" });
  expect(apiOp).toHaveBeenCalledOnce();
  finish(new TypeError("Failed to fetch"));
  expect(await first).toEqual({ ok: false, error: expect.stringMatching(/^未能送达：/) });
  apiOp.mockResolvedValueOnce({});
  expect(await answerCard(ix, denyBody())).toEqual({ ok: true, already: false });
  expect(apiOp).toHaveBeenCalledTimes(2);
});

it("keeps same-ID occurrences separate and sends the displayed occurrence", async () => {
  let finishFirst!: (value: Record<string, unknown>) => void;
  let finishReplacement!: (value: Record<string, unknown>) => void;
  apiOp.mockReturnValueOnce(new Promise((resolve) => { finishFirst = resolve; }))
    .mockReturnValueOnce(new Promise((resolve) => { finishReplacement = resolve; }));
  const replacement = { ...ix, created_at: "2026-01-02T03:04:05.000000002Z", summary: "replacement" };
  const firstAnswer = answerCard(ix, allowBody());
  const replacementAnswer = answerCard(replacement, denyBody());
  const calls = [...apiOp.mock.calls];
  finishFirst({});
  if (calls.length === 2) finishReplacement({});
  const firstResult = await firstAnswer;
  const replacementResult = await replacementAnswer;
  expect(replacementResult).not.toBe(firstResult);
  expect(calls).toHaveLength(2);
  expect(calls[0]).toEqual(["/interactions/p1/resolve", {
    method: "POST", json: allowBody(), headers: { "X-Persona-Interaction-Created-At": ix.created_at },
  }]);
  expect(calls[1]).toEqual(["/interactions/p1/resolve", {
    method: "POST", json: denyBody(), headers: { "X-Persona-Interaction-Created-At": replacement.created_at },
  }]);
  expect(firstResult.ok).toBe(true);
  expect(replacementResult.ok).toBe(true);
});

it("sends secret text unchanged and stores only a redacted receipt", async () => {
  const card: Interaction = { ...ix, kind: "screen", secret: true };
  const body = Object.freeze({ text: "synthetic-secret" });
  apiOp.mockResolvedValue({});
  expect(await answerCard(card, body)).toEqual({ ok: true, already: false });
  expect(apiOp).toHaveBeenCalledWith("/interactions/p1/resolve", { method: "POST", json: body, headers: { "X-Persona-Interaction-Created-At": card.created_at } });
  expect(ended[0]?.[1]).toMatchObject({ resolution: { text: "<redacted>" } });
  expect(body.text).toBe("synthetic-secret");
});

it("redacts field and elicitation secrets by schema and name without mutating the payload", async () => {
  const card: Interaction = {
    ...ix, kind: "elicitation", fields: [{ key: "hidden", label: "Hidden", secret: true }],
    elicitation: { mode: "form", requested_schema: { type: "object", properties: {
      hidden: { type: "string", format: "password" }, write: { type: "string", writeOnly: true }, visible: { type: "string" },
    } } },
  };
  const values = Object.freeze({ hidden: "synthetic", api_key: "synthetic", visible: "keep" });
  const content = Object.freeze({ hidden: "synthetic", write: "synthetic", TOKEN: "synthetic", visible: "keep", count: 3, enabled: false });
  const body = Object.freeze({ action: "accept", values, content });
  apiOp.mockResolvedValue({});
  expect(await answerCard(card, body)).toEqual({ ok: true, already: false });
  expect(apiOp).toHaveBeenCalledWith("/interactions/p1/resolve", { method: "POST", json: body, headers: { "X-Persona-Interaction-Created-At": card.created_at } });
  expect(ended[0]?.[1]).toMatchObject({ resolution: {
    action: "accept", values: { hidden: "<redacted>", api_key: "<redacted>", visible: "<redacted>" },
    content: { hidden: "<redacted>", write: "<redacted>", TOKEN: "<redacted>", visible: "keep", count: 3, enabled: false },
  } });
  expect(body.content.hidden).toBe("synthetic");
  expect(body.values.hidden).toBe("synthetic");
});

it("preserves non-secret text and does not record failed secret answers", async () => {
  apiOp.mockResolvedValue({});
  await answerCard({ ...ix, kind: "screen" }, { text: "ordinary answer" });
  expect(ended[0]?.[1]).toMatchObject({ resolution: { text: "ordinary answer" } });
  ended.length = 0;
  apiOp.mockRejectedValue(new Error("offline"));
  expect((await answerCard({ ...ix, kind: "screen", secret: true }, { text: "synthetic" })).ok).toBe(false);
  expect(ended).toEqual([]);
});

it("answers an explicitly sessionless script card without a session lookup result", async () => {
  session = undefined;
  apiOp.mockResolvedValue({});
  const script = { ...ix, id: "script-request", session_id: "", tool_name: "personad_ask", agent: "script" };
  expect(await answerCard(script, allowBody())).toEqual({ ok: true, already: false });
  expect(apiOp).toHaveBeenCalledWith("/interactions/script-request/resolve", { method: "POST", json: { allow: true }, headers: { "X-Persona-Interaction-Created-At": script.created_at } });
});
