// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { setToken } from "../lib/api";
import { clearAssistantSnapshot, readAssistantSnapshot, writeAssistantSnapshot } from "../lib/assistant-snapshot";
import { loadPages } from "../lib/lazy-page";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { acceptLabsSeen, getState, patchAssistant, refresh, refreshAssistant, reset } from "../lib/store";
import { Settings } from "./Settings";
import type { IntegrationsRoute } from "./settings/integrations-route";

vi.hoisted(() => {
  window.matchMedia = (media) => ({
    media, matches: false, onchange: null, addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent: () => false,
  });
});
vi.mock("./settings/Integrations", async () => {
  const { integrationQuery } = await import("./settings/integrations-route");
  return {
    Integrations: ({ route }: { route: IntegrationsRoute }) => <div data-integration={integrationQuery(route, true).get("integration") ?? ""} />,
  };
});
vi.mock("./settings/Extensions", () => ({ Extensions: () => null }));

const detail = "?tab=integrations&integration=builtin%3Amail";
const profile = (manager: string | undefined, enabled: boolean) => Response.json({
  machine_id: "example", manager, conversations: [], integrations_v2: enabled, labs_seen: [],
});
let read: () => Promise<Response>;
let root: Root;
let host: HTMLDivElement;
const mount = async (query: string) => {
  history.replaceState(null, "", "/settings" + query);
  await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
};

beforeAll(async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await loadPages();
});
beforeEach(() => {
  setToken("example-browser");
  setRosterDefault("home");
  read = async () => profile("home", false);
  vi.stubGlobal("fetch", async (url: string) => {
    if (url.endsWith("/api/v1/assistant")) return read();
    return Response.json(url.endsWith("/managers") ? { default: "home", managers: [] } : {});
  });
  host = document.createElement("div");
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  resetManagerChoice();
  reset();
  setToken("");
  localStorage.clear();
  vi.unstubAllGlobals();
});

it.each([true, false])("restores a saved Labs choice (%s) from the reload snapshot", async (enabled) => {
  read = async () => profile("home", !enabled);
  await refreshAssistant();
  acceptLabsSeen("home", ["terminal"]);
  patchAssistant({ integrations_v2: enabled });
  expect(readAssistantSnapshot("")).toMatchObject({
    manager: "home", assistant: { integrations_v2: enabled, labs_seen: ["terminal"] },
  });

  // A cold store, with the next hub read still pending, draws the persisted profile.
  reset();
  let reply!: (response: Response) => void;
  read = () => new Promise((resolve) => { reply = resolve; });
  const pending = refreshAssistant();
  expect(getState().assistant).toMatchObject({ integrations_v2: enabled, manager: "home" });
  await mount(enabled ? detail : "?tab=extensions");
  expect(location.search).toBe(enabled ? detail : "?tab=extensions");
  if (enabled) expect(host.querySelector("[data-integration]")?.getAttribute("data-integration")).toBe("builtin:mail");
  await act(async () => {
    reply(profile("home", enabled));
    await pending;
  });
  expect(location.search).toBe(enabled ? detail : "?tab=extensions");
});

it.each([true, false])("restores the named assistant's patched flag (%s) before its next read", async (enabled) => {
  await refreshAssistant();
  read = async () => profile("work", !enabled);
  chooseManager("work");
  await vi.waitFor(() => expect(getState().assistant?.manager).toBe("work"));
  patchAssistant({ integrations_v2: enabled });
  expect(readAssistantSnapshot("work")?.assistant.integrations_v2).toBe(enabled);
  expect(readAssistantSnapshot("")?.assistant.integrations_v2).toBe(false);
  clearAssistantSnapshot("work");
  read = () => new Promise(() => {});
  chooseManager(null);
  chooseManager("work");
  expect(getState().assistant?.integrations_v2).toBe(enabled);
});

it("waits for an uncached assistant and follows each cached assistant's own mode on return", async () => {
  await refresh();
  expect(getState().loaded).toBe(true);
  let reply!: (response: Response) => void;
  read = () => new Promise((resolve) => { reply = resolve; });
  chooseManager("work");
  expect(getState().assistant).toBeNull();
  await mount("?tab=integrations");
  expect(location.search).toBe("?tab=integrations");
  expect(host.querySelector("[data-integration]")).toBeNull();

  // Older hubs omit the owner from the profile returned for an explicit manager request.
  await act(async () => reply(profile(undefined, true)));
  expect(host.querySelector("[data-integration]")).toBeTruthy();
  expect(location.search).toBe("?tab=integrations");
  await act(async () => chooseManager(null));
  expect(location.search).toBe("?tab=extensions");
  await act(async () => chooseManager("work"));
  expect(location.search).toBe("?tab=integrations");
});

it("keeps the legacy route while an enabled snapshot belongs to a different default", async () => {
  resetManagerChoice(); // The roster has not identified the default on this reload yet.
  writeAssistantSnapshot("", "work", { machine_id: "example", conversations: [], integrations_v2: true });
  let reply!: (response: Response) => void;
  read = () => new Promise((resolve) => { reply = resolve; });
  void refreshAssistant();
  expect(getState().assistant?.integrations_v2).toBe(true);
  await mount("?tab=extensions");
  expect(location.search).toBe("?tab=extensions");
  await act(async () => {
    setRosterDefault("home");
    reply(profile("home", false));
  });
  expect(getState().assistant?.manager).toBe("home");
  expect(location.search).toBe("?tab=extensions");
});
