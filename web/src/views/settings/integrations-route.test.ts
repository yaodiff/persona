import { describe, expect, it } from "vitest";
import { integrationHref, isIntegrationDetailRoute, isLegacyIntegrationTab, readIntegrationsAddress, type IntegrationView } from "./integrations-route";

const state = ' {"p":"source","id":"a%20b/%2F@x"} ';
const prefill = ' {"setting":"100% + a/b"} ';
const current = { kind: "current" };
const work = { kind: "owned", manager: "work" };
const read = (url: string) => {
  const { search, pathname } = new URL(url, "https://example.com");
  return readIntegrationsAddress(search, pathname)?.route;
};

describe("the Integrations address", () => {
  it("leaves unrelated Settings categories to the host", () => {
    expect(["channels", "connectors", "extensions", "sources"].every(isLegacyIntegrationTab)).toBe(true);
    expect(read("/settings?tab=managers&manager=work")).toBeUndefined();
  });

  it.each([
    "/settings?tab=integrations&manager=work&integration=ext:tools",
    "/settings?tab=integrations&integration=extension:tools@work",
    "/settings?tab=integrations&integration=ext:tools@work&manager=work",
    "/settings/extensions/tools@work",
    "/settings/extensions@work/tools",
    "/settings?tab=plugins&plugin=tools@work",
  ])("reads the same assistant and extension from %s", (url) => {
    expect(read(url)).toEqual({ scope: work, view: { kind: "extension", id: "tools" }, filter: "all" });
  });

  it.each([
    "/settings?tab=integrations&manager=home&integration=ext:tools@work",
    "/settings?tab=integrations&manager=home&manager=work&integration=ext:tools",
    "/settings?tab=integrations&manager=home&integration=-sources@work/x",
    "/settings?tab=sources&manager=home&state=@work/x",
    "/settings/integrations@home/ext:tools@work",
    "/settings/sources@work/x?manager=home",
    "/settings/extensions@work/tools?manager=home",
    "/settings?tab=extensions&manager=home&extension=-channels/chat@work",
  ])("rejects conflicting assistant claims in %s", (url) => {
    expect(read(url)).toEqual({ scope: current, view: { kind: "unavailable" }, filter: "all" });
  });

  it("distinguishes the current assistant from an exact ID spelled current", () => {
    expect(read("/settings?tab=integrations")?.scope).toEqual(current);
    expect(read("/settings?tab=integrations&manager=current")?.scope).toEqual({ kind: "owned", manager: "current" });
  });

  it.each([
    "/settings?tab=sources&manager=work&state=" + encodeURIComponent(state),
    "/settings/sources@work/" + encodeURIComponent(state),
    "/settings/sources/" + encodeURIComponent(state) + "?manager=work",
    "/settings?tab=sources&state=" + encodeURIComponent("@work/" + state),
    "/settings/integrations/-sources@work/" + encodeURIComponent(state),
    "/settings/integrations@work/-sources/" + encodeURIComponent(state),
    "/settings?tab=integrations&integration=" + encodeURIComponent("-sources@work/" + encodeURIComponent(state)),
    "/settings?tab=integrations&manager=work&integration=" + encodeURIComponent("-sources/" + state),
  ])("keeps source state opaque in %s", (url) => {
    expect(read(url)).toEqual({ scope: work, view: { kind: "source", state }, filter: "all" });
  });

  it("distinguishes full-width Data Flow from a source detail", () => {
    expect(read("/settings?tab=sources&manager=work")?.view).toEqual({ kind: "data-flow" });
    expect(read("/settings/sources/")?.view).toEqual({ kind: "data-flow" });
    expect(isIntegrationDetailRoute({ kind: "data-flow" })).toBe(false);
    expect(isIntegrationDetailRoute({ kind: "source", state })).toBe(true);
  });

  const views: IntegrationView[] = [
    { kind: "list" }, { kind: "extension", id: "tools" }, { kind: "page", id: "tools", ui: "status" },
    { kind: "builtin", id: "mail" }, { kind: "builtin", id: "lark" }, { kind: "builtin", id: "feishu-bot" },
    { kind: "builtin", id: "github" }, { kind: "builtin", id: "apps" }, { kind: "channel", id: "feishu" },
    { kind: "connector", id: "larkcli" }, { kind: "catalog" }, { kind: "markets" }, { kind: "item", market: "public", id: "tools" },
    { kind: "data-flow" }, { kind: "source", state }, { kind: "sent" },
  ];
  it.each(views)("round-trips $kind through both Labs modes with its filter and prefill", (view) => {
    const route = { scope: { kind: "owned", manager: "work" } as const, view: { ...view, prefill }, filter: "tools" as const };
    for (const unified of [true, false]) expect(read(integrationHref(route, unified))).toEqual(route);
  });

  it("writes shareable object addresses that both versions can read", () => {
    const route = read("/settings?tab=integrations&manager=work&integration=ext:tools")!;
    const query = new URL(integrationHref(route), "https://example.com").searchParams;
    expect(query.get("manager")).toBe("work");
    expect(query.get("integration")).toBe("ext:tools@work");
    const list = read("/settings?tab=integrations&manager=work")!;
    const listQuery = new URL(integrationHref(list), "https://example.com").searchParams;
    expect(listQuery.get("manager")).toBe("work");
    expect(listQuery.has("integration")).toBe(false);
  });
});
