// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { resetAgentsDisplay } from "../../lib/agents-display";
import { AgentsDisplayRow } from "./AgentsDisplayRow";
import { validBotmuxOwnerUnionID } from "./BotmuxOwnerField";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let show: boolean;
let owner: string | undefined;
let candidates: { union_id: string; bots: number; sole: number; machines: number }[];
let requests: { method: string; path: string; body?: Record<string, unknown> }[];
let host: HTMLDivElement;
let root: Root;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });

beforeEach(() => {
  localStorage.clear(); resetAgentsDisplay();
  show = true; owner = ""; requests = [];
  candidates = [{ union_id: "on_found1", bots: 3, sole: 3, machines: 1 }, { union_id: "on_other", bots: 1, sole: 0, machines: 1 }];
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    const path = new URL(url, "http://hub").pathname;
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    requests.push({ method, path, body });
    if (path.endsWith("/agents/botmux-owner-candidates")) return json({ candidates });
    if (typeof body?.show_botmux === "boolean") show = body.show_botmux;
    if (typeof body?.botmux_owner_union_id === "string") owner = body.botmux_owner_union_id;
    return json({ show_botmux: show, configured: true, use_herdr_tab_name: false, ...(owner === undefined ? {} : { botmux_owner_union_id: owner }) });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); });

const settle = () => act(async () => { await import("./BotmuxOwnerField"); for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)); });
const input = () => host.querySelector<HTMLInputElement>('[data-testid="botmux-owner-input"]');
const button = (id: string) => host.querySelector<HTMLButtonElement>(`[data-testid="${id}"]`);
const type = (value: string) => act(async () => {
  const el = input()!;
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
});
const puts = () => requests.filter((r) => r.method === "PUT");

it("shows the field only while botmux sessions are shown, and only from a hub that has it", async () => {
  await act(async () => root.render(<AgentsDisplayRow />));
  await settle();
  expect(input()).not.toBeNull();
  expect(host.textContent).toContain(t("settings.botmux_owner"));
  expect(host.textContent).toContain("~/.botmux/bots.json");

  const botmuxSwitch = [...host.querySelectorAll<HTMLButtonElement>('[role="switch"]')].find((b) => b.getAttribute("aria-label") === t("settings.show_botmux"))!;
  await act(async () => botmuxSwitch.click());
  await settle();
  expect(input()).toBeNull();

  const asked = () => requests.filter((r) => r.path.endsWith("/agents/botmux-owner-candidates")).length;
  expect(asked()).toBe(1);
  show = true; owner = undefined; // an older hub
  resetAgentsDisplay();
  await act(async () => root.render(<AgentsDisplayRow key="older" />));
  await settle();
  expect(input()).toBeNull();
  expect(asked()).toBe(1); // nothing asked of a hub without the setting
});

it("fills the suggestion without saving, then saves it trimmed, and clears it", async () => {
  await act(async () => root.render(<AgentsDisplayRow />));
  await settle();
  expect(host.querySelector('[data-testid="botmux-owner-suggestion"]')?.textContent).toContain("on_found1");
  await act(async () => button("botmux-owner-fill")!.click());
  expect(input()!.value).toBe("on_found1");
  expect(puts()).toHaveLength(0); // a suggestion is never saved by itself
  expect(button("botmux-owner-fill")).toBeNull(); // it is what the field holds now

  await type("  on_found1  ");
  await act(async () => button("botmux-owner-save")!.click());
  await settle();
  expect(puts().at(-1)?.body).toEqual({ botmux_owner_union_id: "on_found1" });
  expect(input()!.value).toBe("on_found1");
  expect(button("botmux-owner-save")!.disabled).toBe(true);

  await act(async () => button("botmux-owner-clear")!.click());
  await settle();
  expect(puts().at(-1)?.body).toEqual({ botmux_owner_union_id: "" });
  expect(input()!.value).toBe("");
  expect(button("botmux-owner-clear")).toBeNull();
});

it("refuses a value that is not a union_id before sending it", async () => {
  await act(async () => root.render(<AgentsDisplayRow />));
  await settle();
  await type("ou_not_union");
  expect(host.querySelector('[role="alert"]')?.textContent).toBe(t("settings.botmux_owner_invalid"));
  expect(button("botmux-owner-save")!.disabled).toBe(true);
  expect(puts()).toHaveLength(0);
  expect(validBotmuxOwnerUnionID("")).toBe(true);
  expect(validBotmuxOwnerUnionID(" on_A-b_9 ")).toBe(true);
  expect(validBotmuxOwnerUnionID("on_")).toBe(false);
  expect(validBotmuxOwnerUnionID("on_a b")).toBe(false);
});

it("offers no suggestion when none was found or the hub cannot say", async () => {
  candidates = [];
  await act(async () => root.render(<AgentsDisplayRow />));
  await settle();
  expect(input()).not.toBeNull();
  expect(button("botmux-owner-fill")).toBeNull();
});
