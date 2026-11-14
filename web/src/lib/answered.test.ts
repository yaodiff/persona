import { describe, expect, it } from "vitest";
import { answerLines, endedText } from "./answered";
import type { Interaction } from "./types";

const base = { id: "ix_1", machine_id: "m", session_id: "s" };

describe("answerLines", () => {
  it("每个问题一行，带上选了什么", () => {
    const ix: Interaction = {
      ...base,
      kind: "question",
      tool_name: "ask_user",
      questions: [
        { question: "发吗？", options: [{ label: "发" }, { label: "不发" }] },
        { question: "改哪些？", multi_select: true, options: [{ label: "A" }, { label: "B" }] },
      ],
    };
    expect(answerLines(ix, { answers: { "发吗？": "发", "改哪些？": "A, B" } })).toEqual([
      { label: "发吗？", answer: "发" },
      { label: "改哪些？", answer: "A, B" },
    ]);
    expect(answerLines(ix, { answers: { "发吗？": "发" } })[1].answer).toBe("（未回答）");
  });

  it("计划、授权、终端对话框、交回电脑", () => {
    expect(answerLines({ ...base, kind: "plan" }, { approve: true, mode: "acceptEdits" })[0].answer).toBe("批准，允许编辑");
    expect(answerLines({ ...base, kind: "plan" }, { approve: false, feedback: "先写测试" })[0].answer).toBe("打回：先写测试");
    expect(answerLines({ ...base, kind: "permission" }, { allow: true, always: true })[0].answer).toBe("总是允许");
    expect(answerLines({ ...base, kind: "permission" }, { allow: false })[0].answer).toBe("拒绝");
    const screen: Interaction = { ...base, kind: "screen", dialog: { options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }] } };
    expect(answerLines(screen, { option: "2" })[0].answer).toBe("No");
    expect(answerLines(screen, { options: ["1", "2"], text: "还有" })[0].answer).toBe("Yes，No，还有");
    // what the history keeps of typed text is that something was typed
    expect(answerLines(screen, { text: "<redacted>", keys: ["enter"] })[0].answer).toBe("（已输入）");
    expect(answerLines(screen, { dismiss: true })[0].answer).toBe("交回电脑处理");
  });

  // the card said "推荐" with its tag; once answered it read "Swift Testing (Recommended)", English in every language
  it("drops Claude's (Recommended) from the options picked, and leaves words typed as they were", () => {
    const ix: Interaction = {
      ...base,
      kind: "question",
      tool_name: "AskUserQuestion",
      questions: [
        { question: "用哪个框架？", options: [{ label: "Swift Testing (Recommended)" }, { label: "XCTest" }] },
        { question: "发哪些平台？", multi_select: true, options: [{ label: "手机" }, { label: "电脑 (Recommended)" }] },
        { question: "备注？", options: [{ label: "无" }] },
      ],
    };
    expect(answerLines(ix, { answers: { "用哪个框架？": "Swift Testing (Recommended)", "发哪些平台？": "手机, 电脑 (Recommended)", "备注？": "照旧 (Recommended)" } }).map((l) => l.answer)).toEqual([
      "Swift Testing",
      "手机, 电脑",
      "照旧 (Recommended)",
    ]);
    const screen: Interaction = { ...base, kind: "screen", dialog: { options: [{ key: "1", label: "预览 (Recommended)" }, { key: "2", label: "正式" }] } };
    expect(answerLines(screen, { option: "1" })[0].answer).toBe("预览");
  });

  // the label is kept raw and the answer comes trimmed (pickedOption): a label with a space at an edge is still the pick
  it("knows a picked label that has a space at either edge", () => {
    const ix: Interaction = {
      ...base,
      kind: "question",
      tool_name: "AskUserQuestion",
      questions: [
        { question: "用哪个框架？", options: [{ label: "Swift Testing (Recommended) " }, { label: " XCTest" }] },
        { question: "发哪些平台？", multi_select: true, options: [{ label: " 手机" }, { label: "电脑 (Recommended) " }] },
      ],
    };
    expect(answerLines(ix, { answers: { "用哪个框架？": "Swift Testing (Recommended)", "发哪些平台？": "手机, 电脑 (Recommended)" } }).map((l) => l.answer)).toEqual(["Swift Testing", "手机, 电脑"]);
    expect(answerLines(ix, { answers: { "用哪个框架？": "XCTest" } })[0].answer).toBe("XCTest");
  });
});

describe("endedText", () => {
  it("按结局说明回答为什么没送到", () => {
    expect(endedText("expired")).toContain("过期");
    expect(endedText("cancelled")).toContain("撤回");
    expect(endedText(undefined)).toContain("已经结束");
  });
});
