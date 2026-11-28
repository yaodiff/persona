import { describe, expect, it } from "vitest";
import { initI18n } from "../i18n";
import {
  AGENT_TEXT,
  applySummary,
  effective,
  isUnset,
  optionText,
  pickMachine,
  settingDetail,
  settingTitle,
  textValue,
  valueText,
  withValue,
  dirMeta,
  looseRules,
  patternError,
  shortPath,
  toggledPatterns,
  withPattern,
  type AgentSetting,
  type ClaudeDirsState,
} from "./agent-settings";

const recap: AgentSetting = { key: "awaySummaryEnabled", title: "离开回顾", detail: "", type: "bool", default: true, value: null };
const effort: AgentSetting = { key: "effortLevel", title: "思考强度", detail: "", type: "enum", options: ["low", "high"], default: "high", value: "low" };

describe("agent settings", () => {
  it("read the value in effect and whether it is set", () => {
    expect(effective(recap)).toBe(true);
    expect(isUnset(recap)).toBe(true);
    expect(effective(effort)).toBe("low");
    expect(isUnset(effort)).toBe(false);
    expect(effective({ ...recap, default: undefined })).toBeNull();
  });

  it("change one key and roll it back", () => {
    const list = [recap, effort];
    const changed = withValue(list, "awaySummaryEnabled", false);
    expect(changed[0].value).toBe(false);
    expect(changed[1]).toBe(effort);
    expect(withValue(changed, "awaySummaryEnabled", null)[0].value).toBeNull();
  });

  it.each([
    ["en", "not applied", "Model configuration › Agents › Reasoning effort"],
    ["zh-Hans", "未生效", "模型配置 › Agents › 思考强度"],
    ["ja", "反映されていません", "モデル構成 › Agents › 思考レベル"],
  ])("explains ignored max in %s while keeping the model default selected", async (lang, warning, breadcrumb) => {
    try {
      await initI18n(lang);
      const ignored: AgentSetting = { ...effort, value: null, default: "auto", ignored_value: "max" };
      expect(effective(ignored)).toBe("auto");
      expect(isUnset(ignored)).toBe(true);
      expect(settingDetail(ignored)).toContain(warning);
      expect(settingDetail(ignored)).toContain(breadcrumb);
      expect(settingDetail(ignored)).toContain("/effort max");
      const changed = withValue([ignored, recap], "effortLevel", "high");
      expect(changed[0].ignored_value).toBeUndefined();
      expect(settingDetail(changed[0])).not.toContain(warning);
      expect(settingDetail(changed[0])).toContain("auto");
      expect(settingDetail(changed[0])).toContain(breadcrumb);
      expect(settingDetail(changed[0])).toContain("/effort max");
      expect(changed[1]).toBe(recap);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("send an empty text field as back to the default", () => {
    expect(textValue("  中文 ")).toBe("中文");
    expect(textValue("   ")).toBeNull();
  });

  it("edit an online machine, the remembered one first", () => {
    const ms = [
      { id: "nas", online: false },
      { id: "server", online: true },
      { id: "macbook", online: true },
    ];
    expect(pickMachine(ms, "macbook")).toBe("macbook");
    expect(pickMachine(ms, "nas")).toBe("server");
    expect(pickMachine(ms, "")).toBe("server");
    expect(pickMachine([{ id: "nas", online: false }], "nas")).toBe("");
  });

  it("report a change sent to several machines", () => {
    expect(applySummary([{ id: "a", name: "A" }])).toEqual({ ok: true, text: "已保存" });
    expect(applySummary([{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "c", name: "C" }])).toEqual({ ok: true, text: "已应用到 3 台机器" });
    expect(applySummary([{ id: "a", name: "A" }, { id: "b", name: "NAS", error: "machine is offline" }])).toEqual({ ok: false, text: "1 台已应用，1 台失败（NAS：machine is offline）" });
    expect(applySummary([{ id: "b", name: "NAS", error: "x" }]).text).toBe("未能应用更改（NAS：x）");
  });

  it("name Claude Code's options in plain words, without the settings-file key (#3599)", () => {
    expect(settingTitle(recap)).toBe("返回时总结进展");
    expect(settingDetail(recap)).toContain("离开 5 分钟");
    expect(optionText("effortLevel", "xhigh")).toBe("XHigh");
    expect(optionText("model_reasoning_effort", "ultra")).toBe("Ultra");
    expect(optionText("autoUpdatesChannel", "latest")).toBe("最新版");
    expect(optionText("permissions.defaultMode", "auto")).toBe("自动审批");
    expect(optionText("permissions.defaultMode", "default")).toBe("默认权限");
    // An option or key the web does not know stays as the node wrote it.
    expect(optionText("effortLevel", "future")).toBe("future");
    expect(optionText("approval_policy", "later")).toBe("later");
    const unknown: AgentSetting = { key: "newThing", title: "新东西", detail: "说明", type: "bool" };
    expect(settingTitle(unknown)).toBe("新东西");
    expect(settingDetail(unknown)).toBe("说明");
  });

  it("name Codex's options in words, not config.toml values (#3855)", () => {
    const options: Record<string, string[]> = {
      model_reasoning_effort: ["minimal", "low", "medium", "high", "xhigh", "max", "ultra"],
      model_reasoning_summary: ["auto", "concise", "detailed", "none"],
      model_verbosity: ["low", "medium", "high"],
      approval_policy: ["untrusted", "on-failure", "on-request", "never"],
      sandbox_mode: ["read-only", "workspace-write", "danger-full-access"],
    };
    for (const [key, list] of Object.entries(options)) {
      for (const o of list) expect(optionText(key, o), `${key}=${o}`).not.toBe(o);
    }
    expect(optionText("approval_policy", "never")).toBe("从不询问");
    expect(optionText("sandbox_mode", "danger-full-access")).toBe("无限制（高风险）");
    const sandbox: AgentSetting = { key: "sandbox_mode", title: "沙箱", detail: "danger-full-access 不设限", type: "enum" };
    expect(settingDetail(sandbox)).not.toMatch(/[a-z]-[a-z]/);
  });

  it("say values in words", () => {
    expect(valueText(true)).toBe("开");
    expect(valueText(false)).toBe("关");
    expect(valueText(null)).toBe("默认");
    expect(valueText("high")).toBe("high");
    expect(valueText("high", "effortLevel")).toBe("High");
    expect(valueText("stable", "autoUpdatesChannel")).toBe("稳定版");
    expect(AGENT_TEXT.codex.path).toBe("codex-settings");
    expect(AGENT_TEXT.claude.footer).toContain("~/.claude/settings.json");
  });

  // #8353: the node sends English titles for Grok's table keys; the page's catalog names every row.
  it("names Grok's rows in the page's language and says which ones approve every tool call", () => {
    expect(AGENT_TEXT.grok).toMatchObject({ name: "Grok", path: "grok-settings", file: "~/.grok/config.toml" });
    expect(AGENT_TEXT.grok.footer).toContain("~/.grok/config.toml");
    expect(AGENT_TEXT.grok.footer).toContain("Grok");
    const row = (key: string): AgentSetting => ({ key, title: "English", detail: "English", type: "string" });
    for (const key of ["models.default", "models.default_reasoning_effort", "ui.permission_mode", "ui.yolo", "ui.compact_mode"]) {
      expect(settingTitle(row(key)), key).not.toBe("English");
      expect(settingDetail(row(key)), key).not.toBe("English");
    }
    expect(settingTitle(row("ui.permission_mode"))).toBe("权限模式");
    expect(settingDetail(row("ui.permission_mode"))).toContain("always-approve");
    expect(settingDetail(row("ui.permission_mode"))).toContain("直接批准所有工具调用");
    expect(settingDetail(row("ui.yolo"))).toContain("始终自动批准工具调用");
  });
});

describe("blocked claude dirs", () => {
  const state: ClaudeDirsState = {
    home: "/home/me",
    live: true,
    blocked: ["~/.automation/*", "/opt/x"],
    dirs: [
      { path: "/home/me/.claude", default: true, source: "default", running: 0, sessions: 40 },
      { path: "/home/me/.automation/a", blocked: true, blocked_by: "~/.automation/*", source: "found", running: 1, sessions: 120 },
      { path: "/home/me/.config/cc/work", source: "config", running: 0 },
    ],
  };
  it("reads paths and rows", () => {
    expect(shortPath("/home/me/.automation/a", "/home/me")).toBe("~/.automation/a");
    expect(shortPath("/home/user/x", "/home/me")).toBe("/home/user/x");
    expect(dirMeta(state.dirs[0])).toBe("默认目录，无法屏蔽 · 40 个会话");
    expect(dirMeta(state.dirs[1])).toBe("自动发现 · 120 个会话 · 1 个正在运行 · 由规则 ~/.automation/* 屏蔽");
    expect(dirMeta(state.dirs[2])).toBe("在 hub.json 中配置");
  });
  it("blocks a directory by its path and lifts the rule that blocks one", () => {
    expect(toggledPatterns(state, state.dirs[2])).toEqual(["~/.automation/*", "/opt/x", "~/.config/cc/work"]);
    expect(toggledPatterns(state, state.dirs[1])).toEqual(["/opt/x"]);
    expect(looseRules(state)).toEqual(["/opt/x"]); // the glob is lifted from the row it blocks
    // A directory blocked by its own path, written with ~: one row, no separate rule, no "blocked by rule".
    const own: ClaudeDirsState = { ...state, blocked: ["~/.automation/a"], dirs: [{ ...state.dirs[1], blocked_by: "~/.automation/a" }] };
    expect(looseRules(own)).toEqual([]);
    expect(dirMeta(own.dirs[0], own.home)).toBe("自动发现 · 120 个会话 · 1 个正在运行");
    expect(toggledPatterns(own, own.dirs[0])).toEqual([]);
  });
  it("checks typed patterns", () => {
    expect(patternError("", [])).not.toBe("");
    expect(patternError("relative", [])).toContain("开头");
    expect(patternError("~", [])).not.toBe("");
    expect(patternError("/opt/x/", ["/opt/x"])).toBe("已屏蔽");
    expect(patternError(" ~/.bots/* ", [])).toBe("");
    expect(withPattern(["/a"], " ~/.bots/ ")).toEqual(["/a", "~/.bots"]);
  });
});
