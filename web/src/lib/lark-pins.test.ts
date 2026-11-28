import { describe, expect, it } from "vitest";
import { ApiError } from "./api";
import type { LarkMessage } from "./inbox";
import {
  canAddPin, canOpenPin, canRemovePin, ChatPinsModel, mergePins, needsRecheck, pinProblemText, pinRouteError, pinSummary,
  type ChatPin, type ChatPinsDeps, type ChatPinsPage, type ChatPinTarget,
} from "./lark-pins";

const msg = (id: string, extra: Partial<LarkMessage> = {}): LarkMessage => ({ id, position: id.replace(/\D/g, "") || "1", text: `示例 ${id}`, ...extra });
const pin = (id: string, m: LarkMessage | null = msg(`m${id}`, { can_unpin: true })): ChatPin => ({ id, message_id: m?.id ?? "missing", message: m });

describe("pin helpers", () => {
  it("offers 添加 Pin only for a settled message the hub allows", () => {
    expect(canAddPin(msg("m1", { can_pin: true }))).toBe(true);
    expect(canAddPin(msg("m1"))).toBe(false);
    expect(canAddPin(msg("m1", { can_pin: true, recalled: true }))).toBe(false);
    expect(canAddPin(msg("m1", { can_pin: true, deleted: true }))).toBe(false);
    expect(canAddPin(msg("m1", { can_pin: true, local_state: "pending" }))).toBe(false);
    expect(canAddPin(msg("m1", { can_pin: true, sending: true }))).toBe(false);
    expect(canAddPin(msg("m1", { can_pin: true, type: "SYSTEM" }))).toBe(false);
  });

  it("removes and opens by what the hub says", () => {
    expect(canRemovePin(pin("1"))).toBe(true);
    expect(canRemovePin(pin("1", msg("m1", { can_unpin: false })))).toBe(false);
    expect(canOpenPin(pin("1", null))).toBe(false);
    expect(canOpenPin(pin("1", msg("m1", { deleted: true })))).toBe(false);
  });

  it("summarises without showing a recalled message", () => {
    expect(pinSummary(pin("1", msg("m1", { text: "  评审记录  " })))).toBe("评审记录");
    expect(pinSummary(pin("1", msg("m1", { recalled: true, text: "不应显示" })))).toBe("消息已撤回");
    expect(pinSummary(pin("1", null))).toBe("原消息已不可用");
    expect(pinSummary(pin("1", msg("m1", { text: "", type: "IMAGE" })))).toBe("[图片]");
    expect(pinSummary(pin("1", msg("m1", { text: "", parts: [{ kind: "text", text: "分段" }] })))).toBe("分段");
  });

  it("reads a bare 404 / 405 as a hub from before Pins, not a missing message", () => {
    const old = pinRouteError(new ApiError(404, "not found", {})) as ApiError;
    expect(old.status).toBe(426);
    expect(old.code).toBe("lark_hub_upgrade_required");
    expect(pinProblemText(old)).toContain("先更新 hub");
    const gone = new ApiError(404, "原消息已不可用，请重新加载 Pin 列表", { code: "lark_pin_not_found" });
    expect(pinRouteError(gone)).toBe(gone);
    expect(pinProblemText(new ApiError(426, "升级飞书网关后可使用 Pin 消息。", { code: "lark_gateway_upgrade_required" }))).toBe("升级飞书网关后可使用 Pin 消息。");
    expect(pinProblemText(new ApiError(500, "boom", {}))).toMatch(/^无法加载 Pin 消息：.*HTTP 500: boom/);
  });

  it("asks for a fresh list after an unknown write", () => {
    expect(needsRecheck(new ApiError(502, "x", { code: "lark_pin_result_unknown" }))).toBe(true);
    expect(needsRecheck(new TypeError("fetch failed"))).toBe(true);
    expect(needsRecheck(new ApiError(403, "x", { code: "lark_pin_forbidden" }))).toBe(false);
  });

  it("merges pages one copy per pin", () => {
    expect(mergePins([pin("1"), pin("2")], [pin("2"), pin("3")]).map((p) => p.id)).toEqual(["1", "2", "3"]);
  });
});

function fake(rows: ChatPin[], { account = "acc", per = 2 } = {}) {
  const state = { rows, account, reads: 0, writes: [] as { message: string; pin?: string; account: string }[], fail: null as Error | null, writeFail: null as Error | null };
  const deps: ChatPinsDeps = {
    page: async (owner, cursor) => {
      state.reads += 1;
      if (state.fail) throw state.fail;
      if (owner && owner !== state.account) throw new ApiError(409, "飞书账号已切换，请重新打开会话", { code: "lark_account_changed" });
      const at = Number(cursor ?? 0);
      const next = at + per < state.rows.length ? String(at + per) : "";
      return { account_id: state.account, items: state.rows.slice(at, at + per), total: state.rows.length, next_cursor: next } satisfies ChatPinsPage;
    },
    change: async (message, p, owner) => {
      state.writes.push({ message, pin: p, account: owner });
      if (state.writeFail) throw state.writeFail;
      state.rows = p ? state.rows.filter((r) => r.id !== p) : [pin("new"), ...state.rows];
    },
    locate: async (message) => ({ message: msg(message), page: { messages: [msg(message)] } }) satisfies ChatPinTarget,
  };
  return { state, deps };
}

describe("ChatPinsModel", () => {
  it("pages through the list and keeps the pages on refresh", async () => {
    const { state, deps } = fake([pin("1"), pin("2"), pin("3"), pin("4"), pin("5")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    expect(m.state.items.map((p) => p.id)).toEqual(["1", "2"]);
    expect(m.state.total).toBe(5);
    expect(m.state.account).toBe("acc");
    await m.more();
    expect(m.state.items.map((p) => p.id)).toEqual(["1", "2", "3", "4"]);
    state.reads = 0;
    await m.refresh();
    expect(state.reads).toBe(2);
    expect(m.state.items).toHaveLength(4);
  });

  it("removes with the account it read, then reads the list again", async () => {
    const { state, deps } = fake([pin("1"), pin("2")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    expect(await m.change("m1", "1")).toBe(true);
    expect(state.writes).toEqual([{ message: "m1", pin: "1", account: "acc" }]);
    expect(m.state.items.map((p) => p.id)).toEqual(["2"]);
    expect(m.state.requiresRecheck).toBe(false);
  });

  it("does not write again after an unknown result until the list was read", async () => {
    const { state, deps } = fake([pin("1")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    state.writeFail = new ApiError(502, "尚未确认操作结果", { code: "lark_pin_result_unknown" });
    expect(await m.change("m1", "1")).toBe(false);
    expect(m.state.requiresRecheck).toBe(true);
    expect(m.state.problem).toBe("尚未确认操作结果");
    expect(await m.change("m1", "1")).toBe(false);
    expect(state.writes).toHaveLength(1);
    await m.refresh();
    expect(m.state.requiresRecheck).toBe(false);
  });

  it("clears and blocks when the account changed", async () => {
    const { state, deps } = fake([pin("1")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    state.account = "other";
    await m.refresh();
    expect(m.state.items).toEqual([]);
    expect(m.state.blocked).toBe(true);
    expect(m.state.problem).toBe("飞书账号已切换，请重新打开会话");
    state.reads = 0;
    await m.refresh();
    expect(state.reads).toBe(0);
  });

  it("keeps the last list when a refresh fails, and says so", async () => {
    const { state, deps } = fake([pin("1")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    state.fail = new ApiError(503, "暂时无法加载 Pin 消息，请稍后重试", { code: "lark_pins_unavailable" });
    await m.refresh();
    expect(m.state.items.map((p) => p.id)).toEqual(["1"]);
    expect(m.state.problem).toBe("暂时无法加载 Pin 消息，请稍后重试");
  });

  it("finds a pin's message, and drops replies after it stopped", async () => {
    const { deps } = fake([pin("1")]);
    const m = new ChatPinsModel(deps);
    await m.refresh();
    expect((await m.locate(m.state.items[0]))?.message.id).toBe("m1");
    m.stop();
    expect(await m.locate(m.state.items[0])).toBeUndefined();
  });

  it("reads nothing once stopped", async () => {
    const { state, deps } = fake([pin("1")]);
    const m = new ChatPinsModel(deps);
    m.stop();
    await m.refresh();
    expect(state.reads).toBe(0);
  });
});
