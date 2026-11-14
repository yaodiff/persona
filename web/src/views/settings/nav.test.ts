import { describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { searchSettings } from "./search";
import { ASSISTANT_TABS, categoryState, FROM_LIST, itemState, NAV_GROUPS, openedFromList, redirectLegacySettings, settingsAt, settingsHref, stepsToList, TAB_INFO, TABS } from "./nav";

describe("settings location", () => {
  it("names no category on plain /settings: the phone's list (通用 on a wide screen)", () => {
    expect(settingsAt("")).toEqual({ tab: null, sub: null, section: null });
    expect(settingsAt("?tab=nope")).toEqual({ tab: null, sub: null, section: null });
  });
  it("reads the category and the item its page has open", () => {
    expect(settingsAt("?tab=models&purpose=agent.triage", "#settings-model-config")).toEqual({ tab: "models", sub: "agent.triage", section: "model-config" });
    expect(settingsAt("?tab=agents", "#settings-codex")).toEqual({ tab: "agents", sub: null, section: "codex" });
    expect(settingsAt("?tab=agents&purpose=x")).toEqual({ tab: "agents", sub: null, section: null });
  });
  it("names 通用 on a phone, where /settings is the list, and keeps /settings for it on a wide screen", () => {
    expect(settingsHref("general", null, false)).toBe("/settings");
    expect(settingsHref("general", null, true)).toBe("/settings?tab=general");
    expect(settingsHref("channels", "wechat", false)).toBe("/settings?tab=channels&channel=wechat");
    expect(settingsHref("hub", "ignored", true)).toBe("/settings?tab=hub");
  });
  it("preserves an explicit assistant owner in configuration links", () => {
    expect(settingsHref("models", "agent.new", false, "work", "model-config")).toBe("/settings?tab=models&purpose=agent.new&manager=work#settings-model-config");
    expect(settingsHref("persona", null, false, "assistant")).toBe("/settings?tab=persona&manager=assistant");
  });
  it("round-trips every category through its URL", () => {
    for (const t of TABS) expect(settingsAt(settingsHref(t, null, true).split("?")[1] ?? "").tab).toBe(t);
  });
  it("lists each category once on the phone's first page", () => {
    const listed = NAV_GROUPS.flatMap((g) => g.tabs);
    expect([...listed].sort()).toEqual([...TABS].sort());
  });
  it("knows a category was opened from the list (Back goes to it)", () => {
    expect(openedFromList({ [FROM_LIST]: true, personaOverlay: "o1" })).toBe(true);
    expect(openedFromList(null)).toBe(false);
    expect(openedFromList({ personaOverlay: "o1" })).toBe(false);
    // an item's page over it is two entries above the list; another category in its place keeps only that way back
    expect(stepsToList(itemState({ [FROM_LIST]: true }))).toBe(2);
    expect(stepsToList(itemState(null))).toBe(0);
    expect(categoryState(itemState({ [FROM_LIST]: true }))).toEqual({ [FROM_LIST]: 2 });
  });
  it("redirects each old combined category to its new section and preserves its destination", () => {
    expect(redirectLegacySettings("?tab=model-config&purpose=agent.triage&manager=work", "")).toBe("/settings?tab=models&purpose=agent.triage&manager=work#settings-model-config");
    expect(redirectLegacySettings("?tab=models&launcher=example&add=1", "")).toBeNull();
    for (const agent of ["claude", "codex", "agy", "omp"]) {
      expect(redirectLegacySettings(`?tab=${agent}`, "")).toBe(`/settings?tab=agents#settings-${agent}`);
    }
    expect(redirectLegacySettings("?tab=translate", "")).toBe("/settings?tab=general#settings-translation");
    expect(redirectLegacySettings("?tab=assistant&manager=work", "")).toBe("/settings?tab=persona&manager=work");
  });
  it("keeps Sent out of top-level navigation and redirects both legacy URL forms", () => {
    expect(TABS).not.toContain("sent");
    expect(settingsAt("?tab=sent")).toEqual({ tab: "connectors", sub: "-sent", section: null });
    expect(settingsHref("connectors", "-sent", false)).toBe("/settings?tab=connectors&connector=-sent");
    expect(redirectLegacySettings("?tab=sent&manager=work", "")).toBe("/settings?tab=connectors&manager=work&connector=-sent");
    for (const path of ["/settings/sent", "/settings/sent/"]) {
      expect(redirectLegacySettings("", "", path)).toBe("/settings?tab=connectors&connector=-sent");
    }
    expect(redirectLegacySettings("?tab=connectors&connector=-sent", "")).toBeNull();
  });
  it("keeps the legacy purpose in the canonical model URL", () => {
    expect(settingsAt("?tab=model-config&purpose=agent.triage&manager=work")).toEqual({ tab: "models", sub: "agent.triage", section: "model-config" });
  });
  it("opens a link from before the rename (?tab=plugins&plugin=…) as 扩展", () => {
    expect(settingsAt("?tab=plugins&plugin=wechat")).toEqual({ tab: "extensions", sub: "wechat", section: null });
    expect(settingsAt("?tab=extensions&extension=x")).toEqual({ tab: "extensions", sub: "x", section: null });
  });
  it("opens legacy channel links in the independent channel category", () => {
    expect(settingsAt("?tab=plugins&plugin=-channels%2Fwechat")).toEqual({ tab: "channels", sub: "wechat", section: null });
    expect(settingsAt("?tab=extensions&extension=-channels%2Fwechat")).toEqual({ tab: "channels", sub: "wechat", section: null });
    expect(settingsAt("?tab=extensions&extension=-channels")).toEqual({ tab: "channels", sub: null, section: null });
    expect(settingsAt("?tab=channels&channel=wechat")).toEqual({ tab: "channels", sub: "wechat", section: null });
  });
  it("puts channels immediately before connectors in both layouts", () => {
    expect(TABS[TABS.indexOf("connectors") - 1]).toBe("channels");
    const listed = NAV_GROUPS.flatMap((g) => g.tabs);
    expect(listed[listed.indexOf("connectors") - 1]).toBe("channels");
  });
  it("gives every category a one-line hint, and no group shares a name with one of its rows (#3636)", () => {
    for (const k of TABS) expect(TAB_INFO[k].hint.trim()).not.toBe("");
    const titles = [t("settings-nav.group_assistant", { name: "助手" }), t("settings-nav.group_device"), t("settings-nav.group_hub")];
    for (const k of TABS) expect(titles).not.toContain(TAB_INFO[k].label);
  });
});

describe("settings sidebar groups", () => {
  it("has the current assistant's, this device's and the hub's, in that order, every category in one of them", () => {
    expect(NAV_GROUPS.map((g) => g.key)).toEqual(["assistant", "device", "hub"]);
    expect(NAV_GROUPS.map((g) => g.tabs)).toEqual([
      ["models", "persona", "prompts", "channels", "connectors", "sources", "extensions", "knowledge", "agents"],
      ["general", "notifications", "shortcuts"],
      ["hub", "managers", "avatar", "devices"],
    ]);
    expect(NAV_GROUPS.flatMap((g) => g.tabs).sort()).toEqual([...TABS].sort());
    expect([...ASSISTANT_TABS]).toEqual(NAV_GROUPS[0].tabs);
  });

  // audit item 66: the hub's own page was a hard-coded lowercase "hub", and Models and Knowledge selection shared a mark
  it.each(["en", "zh-Hans", "ja"])("names every category from the catalog in %s, each with a mark of its own", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { TAB_INFO, TABS } = await import("./nav");
    expect(TAB_INFO.hub.label).toBe(t("settings-nav.tab_hub"));
    expect(TAB_INFO.hub.label).not.toBe("hub");
    expect(TAB_INFO.hub.label).not.toBe(t("settings-nav.group_hub"));
    expect(new Set(TABS.map((k) => TAB_INFO[k].icon)).size).toBe(TABS.length);
  });

  it("names the assistant's profile and the assistant list apart: no row is called like a group (#3365)", () => {
    expect(TAB_INFO.persona.label).toBe("助手资料");
    expect(TAB_INFO.managers.label).toBe("助手");
    expect(t("settings-nav.group_assistant", { name: "工作" })).toBe("工作 的设置");
  });
});

describe("settings search", () => {
  it.each(["en", "zh-Hans", "ja"])("finds standing authorizations by its localized title in %s", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { searchSettings } = await import("./search");
    const title = t("settings-standing.title");
    expect(title).not.toBe("settings-standing.title");
    expect(searchSettings(title).groups).toContainEqual({ tab: "persona", anchor: "standing-authorizations", group: title });
  });
  it("matches a category by label", () => {
    expect(searchSettings("模型").tabs).toContain("models");
    expect(searchSettings("Agent").tabs).toContain("agents");
  });
  it("matches a category by hint", () => {
    expect(searchSettings("邮箱").tabs).toContain("connectors");
  });
  it("matches a page group by its heading", () => {
    const r = searchSettings("外观");
    expect(r.groups.some((g) => g.tab === "general" && g.anchor === "appearance")).toBe(true);
  });
  it("matches a models/agents section", () => {
    expect(searchSettings("Codex").groups.some((g) => g.tab === "agents" && g.anchor === "settings-codex")).toBe(true);
  });
  it("finds the Sent entry under Connectors by its label and existing hint", () => {
    for (const q of ["以你的名义发出", "为什么放行"]) {
      const result = searchSettings(q);
      expect(result.tabs).not.toContain("sent");
      expect(result.groups).toContainEqual(expect.objectContaining({ tab: "connectors", anchor: "sent" }));
    }
  });
  // audit item 18: only the categories and group headings were found, none of the settings on the pages
  it.each(["en", "zh-Hans", "ja"])("finds the settings on a page by their own labels in %s", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { searchSettings } = await import("./search");
    const finds = (q: string, tab: string, anchor: string) => expect(searchSettings(q).groups).toContainEqual(expect.objectContaining({ tab, anchor }));
    finds(t("language.label"), "general", "language");
    finds(t("ui-look.text_size"), "general", "text-size");
    finds(t("ui-look.accent"), "general", "theme-color");
    finds(t("settings.theme_dark"), "general", "theme");
    finds(t("settings.reply_motion"), "general", "reply-motion");
    finds(t("settings.disconnect_browser"), "general", "disconnect");
    finds(t("disconnect.sign_out"), "general", "disconnect");
    finds(t("settings.add_provider"), "models", "settings-providers");
    finds("API Key", "models", "settings-providers");
    finds(t("settings-notifications.sound"), "notifications", "notify-sound");
    finds(t("settings.revoke"), "devices", "devices");
    finds(t("workspace-board.setting_label"), "hub", "workflow-board");
  });
  it("finds the words people search for in Chinese", () => {
    const anchors = (q: string) => searchSettings(q).groups.map((g) => g.anchor);
    expect(anchors("主题")).toContain("theme-color");
    expect(anchors("深色")).toContain("theme");
    expect(anchors("语言")).toContain("language");
    expect(anchors("字号")).toContain("text-size");
    expect(anchors("退出")).toContain("disconnect");
    expect(anchors("撤销")).toContain("devices");
  });
  it("returns nothing for an unknown query", () => {
    expect(searchSettings("zzz-no-such-setting")).toEqual({ tabs: [], groups: [] });
    expect(searchSettings("  ")).toEqual({ tabs: [], groups: [] });
  });
});
