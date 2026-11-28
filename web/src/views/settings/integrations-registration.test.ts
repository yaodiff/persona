import { describe, expect, it, vi } from "vitest";
import { INTEGRATIONS_SETTINGS_REGISTRATION, LEGACY_INTEGRATION_TABS } from "./integrations-registration";
import { NAV_GROUPS, navGroups } from "./nav";

describe("INTEGRATIONS_SETTINGS_REGISTRATION (#13395-ready host metadata)", () => {
  it("declares stable identity, assistant scope, legacy 4-tab mapping, and lazy loader", async () => {
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.id).toBe("integrations");
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.scope).toBe("assistant");
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.icon).toBe("plug");
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.legacyTabs).toEqual(["channels", "connectors", "sources", "extensions"]);
    expect(LEGACY_INTEGRATION_TABS).toEqual(["channels", "connectors", "sources", "extensions"]);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.label().trim()).not.toBe("");
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.hint().trim()).not.toBe("");
    const mod = await INTEGRATIONS_SETTINGS_REGISTRATION.load();
    expect(typeof mod).toBe("function");
  });

  it("governs unified vs legacy tab visibility without disturbing Devices or Assistant Managers", () => {
    const base = { activeTab: "general", hasChannels: true, hasExtensions: true, hasSources: true };
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isVisible({ integrationsV2: false })).toBe(false);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isVisible({ integrationsV2: true })).toBe(true);

    // Labs off: legacy tabs respect feature flags and stay visible when directly opened.
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("connectors", { ...base, integrationsV2: false })).toBe(true);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("channels", { ...base, hasChannels: false, integrationsV2: false })).toBe(false);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("channels", { ...base, hasChannels: false, activeTab: "channels", integrationsV2: false })).toBe(true);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("extensions", { ...base, hasExtensions: false, integrationsV2: false })).toBe(false);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("extensions", { ...base, hasExtensions: false, activeTab: "extensions", integrationsV2: false })).toBe(true);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("sources", { ...base, hasSources: false, integrationsV2: false })).toBe(false);
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible("sources", { ...base, hasSources: false, activeTab: "sources", integrationsV2: false })).toBe(true);

    // Labs on: all 4 legacy tabs collapse cleanly.
    for (const tab of LEGACY_INTEGRATION_TABS) {
      expect(INTEGRATIONS_SETTINGS_REGISTRATION.isLegacyTabVisible(tab, { ...base, integrationsV2: true })).toBe(false);
    }

    // Hard boundary (#13395 / #17716): hub group keeps managers and devices untouched; core top-level counts stay 18 off / 15 on.
    expect(NAV_GROUPS.find((g) => g.key === "hub")?.tabs).toEqual(["hub", "managers", "avatar", "devices"]);
    expect(navGroups(false).flatMap((g) => g.tabs)).toHaveLength(18);
    expect(navGroups(true).flatMap((g) => g.tabs)).toHaveLength(15);
  });

  it("resolves bookmarks, preserves ?set= prefill and assistant scope, and rolls back cleanly when Labs is off", () => {
    const prefill = encodeURIComponent(JSON.stringify({ region: "jp" }));
    const address = INTEGRATIONS_SETTINGS_REGISTRATION.resolveAddress(`?tab=extensions&extension=larkx%40work&set=${prefill}`);
    expect(address?.tab).toBe("extensions");
    expect(address?.route).toEqual({
      scope: { kind: "owned", manager: "work" },
      view: { kind: "extension", id: "larkx", prefill: JSON.stringify({ region: "jp" }) },
      filter: "all",
    });

    // Forward redirect when Labs is on preserves manager and ?set=.
    const unified = INTEGRATIONS_SETTINGS_REGISTRATION.redirectAddress(
      `?tab=extensions&extension=larkx%40work&set=${prefill}`,
      "#section",
      "/settings",
      true,
    );
    expect(unified).toBe(`/settings?tab=integrations&manager=work&integration=ext%3Alarkx%40work&set=${prefill}#section`);

    // Rollback when Labs is off restores the legacy extension tab with manager and ?set=.
    const rollback = INTEGRATIONS_SETTINGS_REGISTRATION.redirectAddress(
      `?tab=integrations&manager=work&integration=ext%3Alarkx%40work&set=${prefill}`,
      "#section",
      "/settings",
      false,
    );
    expect(rollback).toBe(`/settings?tab=extensions&manager=work&extension=larkx%40work&set=${prefill}#section`);

    // Loading state and conflicting owners never redirect prematurely.
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.redirectAddress("?tab=integrations&integration=ext%3Alarkx", "", "/settings", "loading")).toBeNull();
    expect(INTEGRATIONS_SETTINGS_REGISTRATION.redirectAddress("?tab=integrations&manager=work&integration=ext%3Alarkx%40home", "", "/settings", false)).toBeNull();
  });

  it.each(["en", "zh-Hans", "ja"])("exposes localized search keywords and groups in %s", async (lang) => {
    vi.resetModules();
    const { initI18n, t } = await import("../../i18n");
    await initI18n(lang);
    const { INTEGRATIONS_SETTINGS_REGISTRATION: reg } = await import("./integrations-registration");

    const keywords = reg.searchKeywords();
    expect(keywords).toContain(t("settings-nav.tab_channels"));
    expect(keywords).toContain(t("settings-nav.tab_connectors"));
    expect(keywords).toContain(t("settings-nav.tab_extensions"));
    expect(keywords).toContain(t("settings-nav.tab_sources"));

    const unifiedGroups = reg.searchGroups(true);
    const legacyGroups = reg.searchGroups(false);
    expect(unifiedGroups.every((g) => g.tab === "integrations")).toBe(true);
    expect(legacyGroups.every((g) => g.tab !== "integrations")).toBe(true);
  });
});
