// @vitest-environment jsdom
// The module host (#13434, docs/web/web.md "Modules"): registration rules, when a module is there (its hub flag, this
// browser's switch, before the hub's hello), its routes and sidebar entries only while it is, and loading its strings,
// then its code, only while it is on.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { parse } from "../lib/router";
import { getState, reset, subscribe } from "../lib/store";

const setState = (patch: Partial<ReturnType<typeof getState>>) => Object.assign(getState(), patch);
import { cardRendererOf, CHAT_SLOTS, chatSlotFillers, connectModuleHub, CORE_CARD_KINDS, enabledSettingsTabs, hubServes, loadedModule, MODULE_API, MODULES_FEATURE, moduleFeature, enabledNavItems, moduleExtensionAt, moduleKBKindAt, loadModule, moduleNavKey, moduleRouteAt, moduleState, moduleTitle, registeredModules, registerModule, resetModulesForTest, settingsRowFillers, standaloneAt, standaloneOn, subscribeModules, type ModuleDescriptor } from "./module-host";
import { forgetModuleSwitches, moduleSwitchedOff, setModuleSwitchedOff } from "./module-switch";

const code = vi.fn(async () => ({ Page: () => null }));
const strings = vi.fn(async () => ({ default: { "garden.title": "Garden" }, fallbacks: {} }));

function garden(over: Partial<ModuleDescriptor> = {}): ModuleDescriptor {
  return {
    id: "garden",
    feature: "module.garden",
    title: () => "Garden",
    description: () => "A synthetic module",
    routes: [{ base: "garden", aliases: [["old", "garden"]], title: () => "Garden page" }],
    navItems: [{ key: "garden", path: "/garden", icon: "heart", label: () => "Garden", after: "inbox" }],
    i18nNamespaces: ["garden"],
    locales: { "zh-Hans": strings, en: strings },
    load: code,
    ...over,
  };
}

beforeEach(() => {
  connectModuleHub(getState, subscribe);
  localStorage.clear();
  forgetModuleSwitches();
  reset();
  resetModulesForTest();
  code.mockClear();
  strings.mockClear();
});

afterEach(() => {
  localStorage.clear();
  forgetModuleSwitches();
  resetModulesForTest();
});

describe("registering", () => {
  it("keeps each module once, by a lower-case hyphenated id, with route bases of its own", () => {
    registerModule(garden());
    expect(registeredModules().map((d) => d.id)).toEqual(["garden"]);
    expect(() => registerModule(garden())).toThrow(/twice/);
    expect(() => registerModule(garden({ id: "Garden" }))).toThrow(/lower-case/);
    expect(() => registerModule(garden({ id: "yard" }))).toThrow(/claims \/garden/);
    registerModule(garden({ id: "yard-two", routes: [{ base: "yard", title: () => "Yard" }] }));
    expect(registeredModules().map((d) => d.id)).toEqual(["garden", "yard-two"]);
  });

  it("refuses a route that would take a core address or another module's base or alias", () => {
    for (const base of ["inbox", "settings", "tracks", "s", "persona", "api"]) {
      expect(() => registerModule(garden({ routes: [{ base, title: () => "x" }] })), base).toThrow(/core address/);
    }
    // a whole core page as an alias, or a path under a core address that takes none
    expect(() => registerModule(garden({ routes: [{ base: "garden", aliases: [["inbox"]], title: () => "x" }] }))).toThrow(/core address/);
    expect(() => registerModule(garden({ routes: [{ base: "garden", aliases: [["settings", "garden"]], title: () => "x" }] }))).toThrow(/under the core address/);
    // a space's address (/tracks/<slug>) may be a module's alias
    registerModule(garden({ routes: [{ base: "garden", aliases: [["tracks", "garden"]], title: () => "x" }] }));
    // another module: its base, its alias, or a path either one starts
    expect(() => registerModule(garden({ id: "yard", routes: [{ base: "yard", aliases: [["tracks", "garden"]], title: () => "x" }] }))).toThrow(/"garden" has/);
    expect(() => registerModule(garden({ id: "yard", routes: [{ base: "yard", aliases: [["garden", "old"]], title: () => "x" }] }))).toThrow(/"garden" has/);
    expect(() => registerModule(garden({ id: "yard", routes: [{ base: "yard", aliases: [["tracks"]], title: () => "x" }] }))).toThrow(/core address/);
    expect(registeredModules().map((d) => d.id)).toEqual(["garden"]);
  });

  it("refuses what the shell does not serve yet, and another module API, rather than dropping it", () => {
    expect(() => registerModule(garden({ wants: ["yard"] }))).toThrow(/wants.*does not serve yet/);
    expect(() => registerModule(garden({ api: 2 }))).toThrow(/module API 2/);
    expect(registeredModules()).toEqual([]);
    // empty lists and the current API are fine
    registerModule(garden({ chatSlots: [], wants: [], api: MODULE_API }));
    expect(registeredModules().map((d) => d.id)).toEqual(["garden"]);
  });

  it("draws a reserved extension-page id while on, answers it as unknown while off, and refuses an unreserved or taken id", () => {
    expect(() => registerModule(garden({ extensionPages: ["garden-ui"] }))).toThrow(/not a reserved one/);
    registerModule(garden({ feature: undefined, extensionPages: ["-garden-ui"] }));
    expect(() => registerModule(garden({ id: "yard", routes: [], extensionPages: ["-garden-ui"] }))).toThrow(/"garden" has/);
    expect(moduleExtensionAt("-garden-ui")).toMatchObject({ id: "garden" });
    expect(moduleExtensionAt("weather")).toBeNull();
    expect(parse("/extension/-garden-ui/pages")).toEqual({ name: "extension", extension: "-garden-ui", ui: "pages" });
    setModuleSwitchedOff("garden", true);
    expect(moduleExtensionAt("-garden-ui")).toBe("absent");
    expect(parse("/extension/-garden-ui/pages?state=x")).toEqual({ name: "not-found", path: "/extension/-garden-ui/pages?state=x" });
    expect(parse("/extension/weather/pages")).toEqual({ name: "extension", extension: "weather", ui: "pages" });
  });

  it("adds parts to a knowledge kind's page while on, answers its entries as unknown while off, and refuses a taken kind", () => {
    registerModule(garden({ feature: undefined, kbKinds: ["plants"] }));
    expect(() => registerModule(garden({ id: "yard", routes: [], kbKinds: ["plants"] }))).toThrow(/knowledge kind "plants", which "garden" has/);
    expect(moduleKBKindAt("plants")).toMatchObject({ id: "garden" });
    expect(moduleKBKindAt("projects")).toBeNull();
    expect(parse("/kb/plants/rose")).toEqual({ name: "kb", kind: "plants", id: "rose" });
    setModuleSwitchedOff("garden", true);
    expect(moduleKBKindAt("plants")).toBe("absent");
    expect(parse("/kb/plants/rose")).toEqual({ name: "not-found", path: "/kb/plants/rose" });
    // the list of a kind stays the Knowledge page's own
    expect(parse("/kb/plants")).toEqual({ name: "kb", kind: "plants", id: undefined });
    expect(parse("/kb/projects/persona")).toEqual({ name: "kb", kind: "projects", id: "persona" });
  });

  it("draws a page of its own while on, answers it as absent while off, and refuses a core, taken or routed address", () => {
    expect(() => registerModule(garden({ routes: [], standalonePages: ["kb"] }))).toThrow(/claims \/kb as a page of its own, a core address/);
    expect(() => registerModule(garden({ routes: [], standalonePages: ["two/parts"] }))).toThrow(/not one address segment/);
    expect(() => registerModule(garden({ standalonePages: ["garden"] }))).toThrow(/which "garden" routes in the app/);
    registerModule(garden({ routes: [], navItems: [], standalonePages: ["live"] }));
    expect(() => registerModule(garden({ id: "yard", routes: [], navItems: [], standalonePages: ["live"] }))).toThrow(/\/live as a page of its own, which "garden" has/);
    expect(() => registerModule(garden({ id: "yard", navItems: [], routes: [{ base: "live", title: () => "" }] }))).toThrow(/which "garden" draws as a page of its own/);
    expect(standaloneAt(["live", "mac"])).toMatchObject({ id: "garden" });
    expect(standaloneAt(["live"])).toMatchObject({ id: "garden" });
    expect(standaloneAt(["kb", "notes"])).toBeNull();
    expect(standaloneAt([])).toBeNull();
    expect(standaloneOn("live")).toBe(true);
    expect(standaloneOn("kb")).toBe(false);
    // the hub flag rule: before the hello it is on; a publishing hub without the flag takes the core's links to it away
    setState({ features: [MODULES_FEATURE], loaded: true });
    expect(standaloneOn("live")).toBe(false);
    setState({ features: [MODULES_FEATURE, "module.garden"], loaded: true });
    expect(standaloneOn("live")).toBe(true);
    setModuleSwitchedOff("garden", true);
    expect(standaloneAt(["live", "mac"])).toBe("absent");
    expect(standaloneOn("live")).toBe(false);
    // and the app routes it as an unknown address, as it does any address no page has
    expect(parse("/live/mac")).toEqual({ name: "not-found", path: "/live/mac" });
  });

  it("shows a sidebar entry with a `shown` condition only while it holds, and tells subscribers when it changes", () => {
    let on = false;
    const listeners = new Set<() => void>();
    const shown = { get: () => on, subscribe: (l: () => void) => (listeners.add(l), () => void listeners.delete(l)) };
    registerModule(garden({ feature: undefined, navItems: [{ key: "garden", path: "/garden", icon: "heart", label: () => "Garden", after: "inbox", shown }] }));
    const seen = vi.fn();
    const off = subscribeModules(seen);
    expect(enabledNavItems()).toEqual([]);
    on = true;
    listeners.forEach((l) => l());
    expect(seen).toHaveBeenCalledTimes(1);
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
    // its page still opens whatever the condition says
    on = false;
    expect(parse("/garden")).toMatchObject({ name: "module", module: "garden" });
    off();
    expect(listeners.size).toBe(0);
  });

  it("adds rows to a core Settings place it names, refuses another, and lists them only while on", async () => {
    expect(() => registerModule(garden({ settingsRows: ["connectors.garden" as never] }))).toThrow(/Settings place "connectors.garden", which the web shell does not have/);
    const Row = () => null;
    code.mockImplementationOnce(async () => ({ Page: () => null, settingsRows: { "connectors.lark": Row } }) as never);
    registerModule(garden({ feature: undefined, settingsRows: ["connectors.lark"] }));
    expect(settingsRowFillers("connectors.lark")).toEqual([{ id: "garden", Component: undefined }]);
    await loadModule("garden");
    expect(settingsRowFillers("connectors.lark")).toEqual([{ id: "garden", Component: Row }]);
    setModuleSwitchedOff("garden", true);
    expect(settingsRowFillers("connectors.lark")).toEqual([]);
    setModuleSwitchedOff("garden", false);
    resetModulesForTest();
    code.mockImplementationOnce(async () => ({ Page: () => null }) as never);
    registerModule(garden({ feature: undefined, settingsRows: ["connectors.lark"] }));
    await expect(loadModule("garden")).rejects.toThrow(/the Settings rows "connectors.lark", which its code does not draw/);
  });

  it("tells subscribers about a registration", () => {
    const seen = vi.fn();
    const stop = subscribeModules(seen);
    registerModule(garden());
    expect(seen).toHaveBeenCalled();
    stop();
  });
});

describe("whether a module is there (the hub flag rule, #13844)", () => {
  it("names its flag module.<id>, and the marker of a hub that publishes them is modules", () => {
    expect(moduleFeature("garden")).toBe("module.garden");
    expect(MODULES_FEATURE).toBe("modules");
  });

  it("is on before the hello: nothing waits on the hub to draw", () => {
    expect(moduleState(garden(), { features: [], loaded: false })).toBe("on");
  });

  it("is on with an older hub, one whose hello has no modules marker", () => {
    const d = garden();
    expect(moduleState(d, { features: [], loaded: true })).toBe("on");
    expect(moduleState(d, { features: ["terminal_stream", "other"], loaded: true })).toBe("on");
  });

  it("follows a hub that publishes module flags: on with its flag, off without it", () => {
    const d = garden();
    expect(moduleState(d, { features: [MODULES_FEATURE, "module.garden"], loaded: true })).toBe("on");
    expect(moduleState(d, { features: [MODULES_FEATURE], loaded: true })).toBe("off");
    expect(moduleState(d, { features: [MODULES_FEATURE, "module.yard"], loaded: true })).toBe("off");
    expect(hubServes(undefined, { features: [MODULES_FEATURE] })).toBe(true);
  });

  it("flips off and back on as later hellos change what the hub publishes", () => {
    let hub = { features: [] as string[], loaded: false };
    const subs = new Set<() => void>();
    connectModuleHub(() => hub, (l) => (subs.add(l), () => subs.delete(l)));
    const hello = (features: string[]) => {
      hub = { features, loaded: true };
      subs.forEach((l) => l());
    };
    registerModule(garden());
    const seen = vi.fn();
    const stop = subscribeModules(seen);
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]); // no hello yet
    hello([MODULES_FEATURE]);
    expect(enabledNavItems()).toEqual([]);
    expect(seen).toHaveBeenCalledTimes(1);
    hello([MODULES_FEATURE, "module.garden"]);
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
    expect(seen).toHaveBeenCalledTimes(2);
    // a hub rolled back to one that publishes no module flags: still on, nothing to tell
    hello(["terminal_stream"]);
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
    expect(seen).toHaveBeenCalledTimes(2);
    stop();
  });

  it("is on without a flag, whatever the hub says", () => {
    const d = garden({ feature: undefined });
    expect(moduleState(d, { features: [], loaded: false })).toBe("on");
    expect(moduleState(d, { features: ["x"], loaded: true })).toBe("on");
  });

  it("is off where this browser switched it off, and back on when switched on", () => {
    const d = garden({ feature: undefined });
    expect(setModuleSwitchedOff("garden", true)).toBe(true);
    expect(moduleSwitchedOff("garden")).toBe(true);
    expect(moduleState(d, { features: [MODULES_FEATURE, "module.garden"], loaded: true })).toBe("off");
    expect(setModuleSwitchedOff("garden", false)).toBe(true);
    expect(localStorage.getItem("persona.modules.off")).toBeNull();
    expect(moduleState(d)).toBe("on");
  });

  it("reads what it can't parse as nothing switched off", () => {
    localStorage.setItem("persona.modules.off", "{not json");
    forgetModuleSwitches();
    expect(moduleSwitchedOff("garden")).toBe(false);
  });
});

describe("routes and sidebar entries", () => {
  it("routes the module's addresses and aliases while it is on, and as unknown ones while it is off", () => {
    registerModule(garden({ feature: undefined }));
    expect(parse("/garden")).toEqual({ name: "module", module: "garden", base: "garden", path: [] });
    expect(parse("/garden/a%20b/c")).toEqual({ name: "module", module: "garden", base: "garden", path: ["a b", "c"] });
    expect(parse("/old/garden/x")).toEqual({ name: "module", module: "garden", base: "garden", path: ["x"] });
    expect(moduleTitle({ module: "garden", base: "garden" })).toBe("Garden page");
    expect(moduleNavKey({ module: "garden", base: "garden" })).toBe("garden");
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);

    setModuleSwitchedOff("garden", true);
    expect(parse("/garden/x")).toEqual({ name: "not-found", path: "/garden/x" });
    expect(parse("/old/garden")).toEqual({ name: "not-found", path: "/old/garden" });
    expect(enabledNavItems()).toEqual([]);
  });

  it("leaves addresses no module has to the router", () => {
    registerModule(garden({ feature: undefined }));
    expect(moduleRouteAt(["inbox"])).toBeNull();
    expect(parse("/inbox")).toEqual({ name: "inbox", id: undefined });
  });

  it("follows the hub's features: no entry on a publishing hub without the flag, the entry once it has it", () => {
    registerModule(garden());
    expect(parse("/garden")).toMatchObject({ name: "module" }); // before the hello
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
    setState({ loaded: true, features: [MODULES_FEATURE, "x"] });
    expect(parse("/garden")).toEqual({ name: "not-found", path: "/garden" });
    expect(enabledNavItems()).toEqual([]);
    setState({ features: [MODULES_FEATURE, "module.garden"] });
    expect(enabledNavItems().map((n) => n.key)).toEqual(["garden"]);
  });
});

describe("loading", () => {
  it("never asks for an absent module's strings or code", async () => {
    registerModule(garden());
    setState({ loaded: true, features: [MODULES_FEATURE] });
    await expect(loadModule("garden")).rejects.toThrow(/not enabled/);
    await expect(loadModule("nothing")).rejects.toThrow(/no module/);
    expect(strings).not.toHaveBeenCalled();
    expect(code).not.toHaveBeenCalled();
  });

  it("skips the chunk if the module is switched off while its catalog is loading", async () => {
    registerModule(garden());
    const pending = loadModule("garden");
    setModuleSwitchedOff("garden", true);
    await expect(pending).rejects.toThrow(/not enabled/);
    expect(strings).toHaveBeenCalledOnce();
    expect(code).not.toHaveBeenCalled();
  });

  it("loads an enabled module's strings, then its code, once", async () => {
    const order: string[] = [];
    strings.mockImplementationOnce(async () => (order.push("strings"), { default: { "garden.title": "Garden" }, fallbacks: {} }));
    code.mockImplementationOnce(async () => (order.push("code"), { Page: () => null }));
    registerModule(garden({ feature: undefined }));
    const first = loadModule("garden");
    expect(loadModule("garden")).toBe(first);
    await first;
    expect(order).toEqual(["strings", "code"]);
    expect(code).toHaveBeenCalledTimes(1);
  });

  it("tries again after a failed load", async () => {
    code.mockRejectedValueOnce(new Error("offline"));
    registerModule(garden({ feature: undefined }));
    await expect(loadModule("garden")).rejects.toThrow("offline");
    await expect(loadModule("garden")).resolves.toHaveProperty("Page");
    expect(code).toHaveBeenCalledTimes(2);
  });
});

describe("chat slots, Settings categories and card renderers", () => {
  const Part = () => null;
  const fills = { chat: { "composer.trailingAction": Part, "chat.tail": Part, "chat.bar": Part, "interaction.asked": Part } };

  it("refuses, by name, a slot the shell does not have, a core or foreign Settings category, and a core or taken card kind", () => {
    expect(() => registerModule(garden({ chatSlots: ["chat.footer" as never] }))).toThrow(/chat slot "chat.footer"/);
    const tab = { key: "garden", label: () => "Garden", hint: () => "Its plots", icon: "heart" as const, group: "device" as const };
    expect(() => registerModule(garden({ settingsTabs: [{ ...tab, key: "yard" }] }))).toThrow(/Settings category "yard", which is not its id/);
    for (const kind of CORE_CARD_KINDS) expect(() => registerModule(garden({ cardRenderers: [kind] }))).toThrow(new RegExp(`card kind "${kind}", which the core draws`));
    registerModule(garden({ feature: undefined, cardRenderers: ["garden-card"], settingsTabs: [tab, { ...tab, key: "garden-plots" }] }));
    expect(() => registerModule(garden({ id: "yard", routes: [], cardRenderers: ["garden-card"] }))).toThrow(/"garden-card", which "garden" has/);
  });

  it("serves each chat slot from the module's code once it is in, and only while the module is on", async () => {
    code.mockImplementationOnce(async () => ({ Page: () => null, ...fills }) as never);
    registerModule(garden({ feature: undefined, chatSlots: [...CHAT_SLOTS] }));
    for (const slot of CHAT_SLOTS) expect(chatSlotFillers(slot)).toEqual([{ id: "garden", Component: undefined }]);
    await loadModule("garden");
    expect(loadedModule("garden")).toBeDefined();
    for (const slot of CHAT_SLOTS) expect(chatSlotFillers(slot)).toEqual([{ id: "garden", Component: Part }]);
    setModuleSwitchedOff("garden", true);
    for (const slot of CHAT_SLOTS) expect(chatSlotFillers(slot)).toEqual([]);
  });

  it("refuses code that leaves out a slot, a category or a card kind its descriptor names, naming it", async () => {
    code.mockImplementationOnce(async () => ({ chat: { "chat.tail": Part } }) as never);
    registerModule(garden({ feature: undefined, chatSlots: ["chat.tail", "chat.bar"] }));
    await expect(loadModule("garden")).rejects.toThrow(/the chat slot "chat.bar", which its code does not draw/);
    expect(loadedModule("garden")).toBeUndefined();
    resetModulesForTest();
    code.mockImplementationOnce(async () => ({}) as never);
    registerModule(garden({ feature: undefined, settingsTabs: [{ key: "garden", label: () => "G", hint: () => "", icon: "heart", group: "labs" }] }));
    await expect(loadModule("garden")).rejects.toThrow(/the Settings category "garden", which its code does not draw/);
    resetModulesForTest();
    code.mockImplementationOnce(async () => ({}) as never);
    registerModule(garden({ feature: undefined, cardRenderers: ["garden-card"] }));
    await expect(loadModule("garden")).rejects.toThrow(/the card kind "garden-card", which its code does not draw/);    resetModulesForTest();
    code.mockImplementationOnce(async () => ({}) as never);
    registerModule(garden({ feature: undefined, routes: [], navItems: [], standalonePages: ["live"] }));
    await expect(loadModule("garden")).rejects.toThrow(/the page of its own \/live, which its code does not draw/);
  });

  it("lists Settings categories and card renderers of enabled modules only", () => {
    registerModule(garden({ cardRenderers: ["garden-card"], settingsTabs: [{ key: "garden", label: () => "Garden", hint: () => "", icon: "heart", group: "device" }] }));
    expect(enabledSettingsTabs().map((x) => [x.module, x.key])).toEqual([["garden", "garden"]]);
    expect(cardRendererOf("garden-card")?.id).toBe("garden");
    expect(cardRendererOf("question")).toBeUndefined();
    setState({ loaded: true, features: [MODULES_FEATURE] });
    expect(enabledSettingsTabs()).toEqual([]);
    expect(cardRendererOf("garden-card")).toBeUndefined();
  });

  it("keeps CORE_CARD_KINDS to the pending-card kinds lib/types.ts names", async () => {
    const { default: types } = await import("../lib/types.ts?raw");
    const line = types.split("\n").find((l) => /^\s*kind: "question" \|/.test(l))!;
    const kinds = [...line.matchAll(/"([a-z_]+)"/g)].map((m) => m[1]!);
    expect([...CORE_CARD_KINDS].sort()).toEqual(kinds.sort());
  });
});
