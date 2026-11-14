/// <reference types="node" />
// The settings search's index (audit item 18): every setting a page shows by a fixed label is found by that label, in
// every language, and every entry lands on something a page draws.
import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";

const read = (path: string) => readFileSync(new URL(path, import.meta.url), "utf8");

// The pages the search covers, by the files that draw their rows. Lists of things (connectors, channels, extensions,
// assistants, purposes, profiles) are named by what is in them, not by fixed labels, and are left out.
const PAGES = [
  "../Settings.tsx",
  "./LookSettings.tsx",
  "./LanguageRow.tsx",
  "./TranslationSettings.tsx",
  "./AgentOpenSetting.tsx",
  "./TerminalModeSetting.tsx",
  "./SidebarPages.tsx",
  "./AssistantSettings.tsx",
  "./AgentAutoNaming.tsx",
  "./TaskTrust.tsx",
  "./StandingAuthorizations.tsx",
  "./Repositories.tsx",
  "./Skills.tsx",
  "./TriageSettings.tsx",
  "./NotificationSettings.tsx",
  "./WebPushSettings.tsx",
  "./ShortcutSettings.tsx",
  "./HubSettings.tsx",
  "./AgentGroup.tsx",
  "./ExtensionDownloadMirrors.tsx",
  "./KnowledgeTopics.tsx",
  "./KnowledgeSelection.tsx",
];
// The helpers some pages read their words through: name → the catalog key it puts before its argument.
const WRAPPERS: Record<string, string> = { text: "settings-knowledge.", word: "settings-assistantsettings.trust_" };
// Row labels that are not settings, with why.
const NOT_SETTINGS: Record<string, string> = {
  "settings-notifications.load_failed": "the row that says the page could not be read, with its retry",
};

/** From `from` (just after an opening "{" or after "<Row"), the index of the "}" or ">" that closes it, past nested braces and strings. */
function closing(source: string, from: number, end: "}" | ">"): number {
  let depth = 0;
  for (let i = from; i < source.length; i++) {
    const c = source[i];
    if (c === '"' || c === "'" || c === "`") {
      for (i++; i < source.length && source[i] !== c; i++) if (source[i] === "\\") i++;
    } else if (c === "{") depth++;
    else if (c === "}" && depth-- === 0 && end === "}") return i;
    else if (c === ">" && depth === 0 && end === ">") return i;
  }
  return source.length;
}

/** The catalog keys of every `<Row label={…}>` in `source` whose label is a fixed string (no values put in it). */
function rowLabelKeys(source: string): string[] {
  const keys: string[] = [];
  for (const m of source.matchAll(/<Row\b/g)) {
    const tag = source.slice(m.index, closing(source, m.index + m[0].length, ">"));
    const at = tag.search(/\slabel=\{/);
    if (at < 0) continue;
    const start = tag.indexOf("{", at) + 1;
    const label = tag.slice(start, closing(tag, start, "}"));
    for (const k of label.matchAll(/\b(t|text|word)\("([^"]+)"\)/g)) keys.push(k[1] === "t" ? k[2] : WRAPPERS[k[1]] + k[2]);
  }
  return keys;
}

const labels = [...new Set(PAGES.flatMap((p) => rowLabelKeys(read(p))))].filter((k) => !(k in NOT_SETTINGS));

describe("the settings search's index", () => {
  it("reads the pages' row labels (the scan finds what it should)", () => {
    expect(labels).toEqual(expect.arrayContaining([
      "settings-assistantsettings.timezone", "settings-triagesettings.auto_continue", "settings-notifications.quiet",
      "settings-hubsettings.auto_update", "settings-knowledge.topics_switch", "settings-knowledge.include_all",
      "settings-assistantsettings.trust_title", "settings-translationsettings.auto", "settings-hubsettings.registry_mirror",
    ]));
    expect(labels.length).toBeGreaterThan(60);
  });

  it.each(["en", "zh-Hans", "ja"])("has every fixed row label of the pages it covers in %s", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { SEARCH_INDEX } = await import("./search");
    const words = new Set(SEARCH_INDEX.flatMap((g) => [...g.group.split(/ › | \/ /), ...(g.hint?.split(" / ") ?? [])]));
    const missing = labels.filter((k) => !words.has(t(k)));
    expect(missing).toEqual([]);
  });

  it.each(["en", "zh-Hans", "ja"])("finds the words the audit searched for, and each label's own words, in %s", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { searchSettings } = await import("./search");
    const finds = (q: string, tab: string, anchor: string) => expect(searchSettings(q).groups, q).toContainEqual(expect.objectContaining({ tab, anchor }));
    finds(t("lib-agent-settings.setting_effortLevel"), "agents", "settings-claude");
    finds(t("lib-agent-settings.setting_model_reasoning_effort"), "agents", "settings-codex");
    finds(t("settings-modelconfiguration.effort"), "models", "settings-model-config");
    finds(t("settings-assistantsettings.timezone"), "persona", "runs-on");
    finds(t("settings-assistantsettings.default_permissions"), "persona", "default-permissions");
    finds(t("settings-triagesettings.auto_continue"), "persona", "triage-auto-continue");
    finds(t("settings-notifications.quiet"), "notifications", "notify-quiet");
    finds(t("settings-notifications.system_always"), "notifications", "notify-system-always");
    finds(t("settings-notifications.input"), "notifications", "notify-input");
    finds(t("settings-hubsettings.auto_update"), "hub", "hub-auto-update");
    finds(t("settings-hubsettings.channel"), "hub", "hub-channel");
    finds(t("settings-knowledge.topics_switch"), "knowledge", "knowledge-topics-switch");
    finds(t("settings-extensions.secrets_title"), "extensions", "extensions");
    finds(t("settings.revoke"), "devices", "devices");
    finds(t("lib-shortcuts.cmd_jump"), "shortcuts", "shortcut-jump");
    finds(t("settings.device_tokens"), "devices", "devices");
    finds("API Key", "models", "settings-providers");
    if (lang !== "ja") finds("token", "devices", "devices");
  });

  it("finds the words people searched for in Chinese (audit item 18), each under a label that says it", async () => {
    vi.resetModules();
    const { initI18n } = await import("../../i18n");
    await initI18n("zh-Hans");
    await import("./search").then(({ searchSettings }) => {
      const found = (q: string) => searchSettings(q).groups.map((g) => `${g.tab}/${g.anchor}/${g.group}`);
      for (const q of ["主题", "主题色", "深色", "字号", "语言", "思考强度", "思考", "时区", "token", "API Key", "密钥", "撤销", "退出", "免打扰", "自动更新", "默认权限", "出错自动继续"])
        expect(found(q), q).not.toEqual([]);
      expect(found("思考强度")).toEqual(expect.arrayContaining([
        "agents/settings-claude/Claude Code › 思考强度", "agents/settings-codex/Codex › 思考强度", "models/settings-model-config/模型配置 › 思考强度",
      ]));
      expect(found("时区")).toContain("persona/runs-on/时区");
      // Revoke is named Revoke, not Connect a new device; the API key is found as a key and a secret
      expect(found("撤销")).toEqual(["devices/devices/撤销"]);
      expect(found("密钥")).toEqual(expect.arrayContaining(["extensions/extensions/密钥", "models/settings-providers/新增供应商"]));
      expect(found("token")).toEqual(expect.arrayContaining(["devices/devices/设备 token", "extensions/market/管理市场"]));
    });
  });

  it("finds a label typed without its spaces, or with other case", async () => {
    vi.resetModules();
    const { initI18n } = await import("../../i18n");
    await initI18n("en");
    const { searchSettings } = await import("./search");
    expect(searchSettings("timezone").groups).toContainEqual(expect.objectContaining({ anchor: "runs-on" }));
    expect(searchSettings("QUIET HOURS").groups).toContainEqual(expect.objectContaining({ anchor: "notify-quiet" }));
    expect(searchSettings("effort").groups.map((g) => g.anchor)).toEqual(expect.arrayContaining(["settings-claude", "settings-codex", "settings-model-config"]));
  });

  it("lands every entry on something a page draws, and lists each once", async () => {
    const { SEARCH_INDEX } = await import("./search");
    const { LOCAL_SHORTCUTS, SHORTCUTS } = await import("../../lib/shortcuts");
    const views = new URL("../", import.meta.url);
    const sources = (readdirSync(views, { recursive: true }) as string[])
      .filter((f) => f.endsWith(".tsx") && !f.includes(".test."))
      .map((f) => readFileSync(new URL(f, views), "utf8"))
      .join("\n");
    // anchors a page writes from a value: each one's source, and the values it takes
    const made: [RegExp, string, (rest: string) => boolean][] = [
      [/^shortcut-(.+)$/, "anchor={`shortcut-${s.id}`}", (id) => [...SHORTCUTS, ...LOCAL_SHORTCUTS].some((s) => s.id === id)],
      [/^knowledge-(.+)$/, "anchor={`knowledge-${kind}`}", (kind) => kind === "messages" || kind === "docs"],
      [/^settings-(.+)$/, "id={`settings-${id}`}", (id) => ["claude", "codex", "agy", "omp"].includes(id)],
    ];
    const drawn = (anchor: string) =>
      new RegExp(`\\b(anchor|data-settings-row|data-settings-group|id)="${anchor}"`).test(sources) ||
      made.some(([re, code, takes]) => {
        const m = re.exec(anchor);
        return !!m && sources.includes(code) && takes(m[1]);
      });
    expect(SEARCH_INDEX.filter((g) => !drawn(g.anchor)).map((g) => g.anchor)).toEqual([]);
    const keys = SEARCH_INDEX.map((g) => `${g.tab}/${g.anchor}/${g.group}`);
    expect(keys.length).toBe(new Set(keys).size);
  });
});
