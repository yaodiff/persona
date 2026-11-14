// Simplified Chinese screens that still read English: the Agents filter's and New work session's "Harness", the rename
// dialog's "标签名（herdr tab）", and the command palette's "Workspace · …" headings, which node-palette.ts wrote by
// hand. (herdr is an engine's name, kept as is; Claude's "(Recommended)" suffix is lib/answered.ts's.)
import { describe, expect, it } from "vitest";
import { t } from "./index";

describe("zh-Hans words that were English", () => {
  it("names an Agent's kind in Chinese wherever it is a filter or a field", () => {
    for (const key of ["lib-agent-filter.filter_harness", "work-session.harness", "work-session-browser.harness"]) expect(t(key), key).toBe("Agent 类型");
    expect(t("work-session-browser.unknown_harness")).toBe("未知 Agent 类型");
    expect(t("work-session.harness_changed")).not.toMatch(/harness/i);
  });

  it("names the rename dialog's tab field in Chinese, herdr kept as the engine's name", () => {
    expect(t("chat-sessiondialogs.tab_name")).toBe("herdr 标签名");
  });

  it("heads the palette's node commands in the menus' words", () => {
    expect(t("lib-node-palette.section_pane", { name: "整理发布说明" })).toBe("窗格 · 整理发布说明");
    expect(t("lib-node-palette.section_tab", { name: "zsh" })).toBe("标签页 · zsh");
    expect(t("lib-node-palette.section_space", { name: "weather" })).toBe("空间 · weather");
  });
});
