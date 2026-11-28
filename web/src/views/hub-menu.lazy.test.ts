/// <reference types="node" />
// The assistant switcher, a first-screen file, lists the other hubs in its menu. It needs only useHubs and
// hubMenuItems: importing them from Hubs.tsx put Settings' hub list and dialogs, SettingsParts and every hubs.* word on
// every first screen. The idle extension renderer uses SettingsParts' Pill, so Pill lives with the atoms (as Switch
// does) and SettingsParts does not move into the idle tier either.
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import startup from "../i18n/startup-namespaces.json";
import { Pill as AtomPill, Switch as AtomSwitch } from "../ui/atoms";
import { Pill, Switch } from "./settings/SettingsParts";

const read = (file: string) => readFileSync(new URL(file, import.meta.url), "utf8");
const runtimeImport = (from: string) => new RegExp(`^import\\s+(?!type\\b)[^;]*from\\s+["']${from}["']`, "m");

describe("hub menu", () => {
  it("keeps Hubs.tsx, SettingsParts and the hubs words off the first screen", () => {
    expect(read("./AssistantSwitcher.tsx")).not.toMatch(runtimeImport("\\./Hubs"));
    const menu = read("./hub-menu.ts");
    expect(menu).not.toMatch(runtimeImport("\\./Hubs"));
    expect(menu).not.toMatch(runtimeImport("\\./settings/SettingsParts"));
    expect(startup.filter((key) => key === "hubs" || key.startsWith("hubs."))).toEqual(["hubs.switch"]);
  });

  it("serves SettingsParts' Pill and Switch from the atoms", () => {
    expect(Pill).toBe(AtomPill);
    expect(Switch).toBe(AtomSwitch);
  });
});
