// @vitest-environment node
// One set of state names on the web (#3415): the words live in lib-state-labels.json, and no other namespace defines a
// state name again or brings back one of the old variants (Waiting for you / In progress / To review / Mark all as read…).
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { beforeAll, describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import { markAllSeenLabel, STATE_LABEL, stateCount, stateName, waitName, type StateName, type WaitReason, type LegacyWaitReason } from "./state-labels";

const ZH = join(__dirname, "../i18n/locales/zh-Hans");

/** The names only an agent's or a task's state has (lib-state-labels.json). Done, Unknown, Interrupted… also name other
 *  things (a tool call, a project, a CI check), so they are not checked. */
const NAMES = ["需要你", "工作中", "完成未看", "已看", "未运行", "待唤醒", "待你回复", "刚做完待看", "待跟进", "全部标为已看",
  // what a waiting task waits on (#11090): 等你 is in OLD, as a page's own word for Needs you
  "等前置任务", "等合入 / 部署", "等外部", "等后台任务", "等自动重试", "暂停中（不需要你）"];

/** Names a page used before #3415 for one of those states. */
const OLD = ["等你", "在做", "待查看", "做完", "做完了", "做完了，还没看", "停着", "等你处理", "需要你处理", "正在工作", "待回答", "全部标为已读"];

/** The same word meaning something else: an inbox item's tier, or a task waiting for its owner (#5732).
 *  (A file marked viewed in a code review says 已查看, #6072.) */
const OTHER_MEANING = new Set(["lib-inbox.tier_action", "todos.section_waiting"]);

function others(): [string, string][] {
  const out: [string, string][] = [];
  for (const f of readdirSync(ZH).filter((f) => f.endsWith(".json") && f !== "lib-state-labels.json")) {
    const ns = f.replace(/\.json$/, "");
    for (const [k, v] of Object.entries(JSON.parse(readFileSync(join(ZH, f), "utf8")) as Record<string, string>)) out.push([`${ns}.${k}`, v]);
  }
  return out;
}

describe("state names", () => {
  beforeAll(() => initI18n("zh-Hans"));

  it("are the docs/protocol/i18n.md glossary's", () => {
    expect(STATE_LABEL).toEqual({ error: "出错", done: "已完成", needs_you: "需要你", working: "工作中", unread: "完成未看", idle: "已看", offline: "未运行", unknown: "未知" });
    const names: Record<StateName, string> = {
      error: "出错", needs_you: "需要你", working: "工作中", unread: "完成未看", idle: "已看", offline: "未运行", unknown: "未知",
      waiting: "待唤醒", bg_waiting: "后台等待中", awaits_reply: "待你回复", just_finished: "刚做完待看", done: "已完成", interrupted: "已中断", follow_up: "待跟进", not_started: "未开始", failed: "启动失败",
    };
    for (const [s, name] of Object.entries(names)) expect(stateName(s as StateName)).toBe(name);
    const waits: Record<LegacyWaitReason, string> = {
      owner: "等你", prerequisite: "等前置任务", delivery: "等合入 / 部署", external: "等外部", background: "等后台任务", retry: "等自动重试", unknown: "暂停中（不需要你）",
    };
    for (const [r, name] of Object.entries(waits)) expect(waitName(r as WaitReason)).toBe(name);
    expect(stateCount("needs_you", 2)).toBe("2 个需要你");
    expect(stateCount("unread", 1)).toBe("1 个完成未看");
    expect(stateCount("bg_waiting", 2)).toBe("2 个后台等待中");
    expect(markAllSeenLabel()).toBe("全部标为已看");
    expect(markAllSeenLabel(3)).toBe("全部标为已看（3）");
  });

  it("are defined once: no other text is just a state name, old or new", () => {
    const words = new Set([...NAMES, ...OLD]);
    // the counts too: "3 need you"
    for (const n of NAMES) words.add(`{count} 个${n}`);
    const again = others().filter(([key, v]) => words.has(v) && !OTHER_MEANING.has(key)).map(([key, v]) => `${key}: ${v}`);
    expect(again, "take these from lib/state-labels.ts").toEqual([]);
  });

  it("say 已看 in a sentence about marking seen, never 已读", () => {
    const read = others().filter(([, v]) => /标为已读/.test(v)).map(([key]) => key);
    expect(read).toEqual([]);
  });
});
