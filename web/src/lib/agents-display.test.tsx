// @vitest-environment jsdom
import { beforeEach, expect, it, vi } from "vitest";
import { refreshAgentsDisplay, resetAgentsDisplay, retryAgentsDisplay, saveAgentsDisplay, useAgentsDisplay, useBotmuxHidden, useHerdrTabName, useShowFilter, useSortByActivity } from "./agents-display";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { setToken } from "./api";
import { t } from "../i18n";
import { AgentsDisplayRow } from "../views/settings/AgentsDisplayRow";
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let saved: boolean | undefined;
let preferTab: boolean;
let fail = "";
let requests: { method: string; body?: Record<string,unknown> }[];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers:{"Content-Type":"application/json"} });
beforeEach(() => {
 localStorage.clear(); resetAgentsDisplay(); saved = undefined; preferTab = false; fail = ""; requests = [];
 vi.stubGlobal("fetch", vi.fn(async (_url, init) => {
  const method = init?.method ?? "GET";
  const body = init?.body ? JSON.parse(init.body) : undefined;
  requests.push({method,body});
  if (fail === method) return json({error:"offline"},503);
  if (typeof body?.show_botmux === "boolean" && (!body.only_if_unset || saved === undefined)) saved = body.show_botmux;
  if (typeof body?.use_herdr_tab_name === "boolean") preferTab = body.use_herdr_tab_name;
  return json({ show_botmux:saved ?? true,configured:saved !== undefined,use_herdr_tab_name:preferTab });
 }));
});

it("saves the Settings › Agents name switch and follows later hub reads", async () => {
  const host = document.createElement("div");
  const root = createRoot(host);
  const nameSwitch = () => [...host.querySelectorAll<HTMLButtonElement>('[role="switch"]')]
    .find((button) => button.getAttribute("aria-label") === t("settings.use_herdr_tab_name"))!;
  await act(async () => { root.render(<AgentsDisplayRow />); });
  expect(nameSwitch().getAttribute("aria-checked")).toBe("false");
  await act(async () => { nameSwitch().click(); });
  expect(requests.at(-1)).toEqual({ method: "PUT", body: { use_herdr_tab_name: true } });
  expect(nameSwitch().getAttribute("aria-checked")).toBe("true");
  expect(saved).toBeUndefined();
  preferTab = false;
  await act(refreshAgentsDisplay);
  expect(nameSwitch().getAttribute("aria-checked")).toBe("false");
  await act(async () => { root.unmount(); });
});
it("fresh default, conditional legacy migration, explicit choice wins and clearing storage preserves it", async () => {
 await refreshAgentsDisplay(); expect(saved).toBeUndefined(); expect(requests).toHaveLength(1);
 localStorage.setItem("persona.agentHideBotmux","1"); await refreshAgentsDisplay();
 expect(saved).toBe(false); expect(requests.at(-1)?.body).toEqual({show_botmux:false,only_if_unset:true});expect(localStorage.getItem("persona.agentHideBotmux")).toBeNull();
 await saveAgentsDisplay(true);
 localStorage.setItem("persona.agentHideBotmux","1");await refreshAgentsDisplay();expect(saved).toBe(true);expect(requests.at(-1)?.method).toBe("GET");expect(localStorage.getItem("persona.agentHideBotmux")).toBeNull();
 localStorage.clear();resetAgentsDisplay();await refreshAgentsDisplay();expect(saved).toBe(true);
});
it("retains the legacy key on failed reads and migration writes, then retries",async()=>{
 localStorage.setItem("persona.agentHideBotmux","1");fail="GET";await refreshAgentsDisplay();expect(localStorage.getItem("persona.agentHideBotmux")).toBe("1");
 fail="PUT";await refreshAgentsDisplay();expect(localStorage.getItem("persona.agentHideBotmux")).toBe("1");expect(saved).toBeUndefined();
 fail="";await refreshAgentsDisplay();expect(saved).toBe(false);expect(localStorage.getItem("persona.agentHideBotmux")).toBeNull();
});
it("migrates a locally hidden chip only when the hub has no explicit choice", async () => {
  localStorage.setItem("persona.agentBotmuxMode", "hide");
  await refreshAgentsDisplay();
  expect(requests.at(-1)?.body).toEqual({ show_botmux: false, only_if_unset: true });
  expect(localStorage.getItem("persona.agentBotmuxMode")).toBeNull();

  saved = true;
  localStorage.setItem("persona.agentBotmuxMode", "hide");
  await refreshAgentsDisplay();
  expect(requests.at(-1)?.method).toBe("GET");
  expect(saved).toBe(true);
  expect(localStorage.getItem("persona.agentBotmuxMode")).toBeNull();
});
it("a concurrent explicit choice wins at the conditional server boundary",async()=>{
 localStorage.setItem("persona.agentHideBotmux","1");
 const fetcher=vi.mocked(fetch);fetcher.mockImplementationOnce(async()=>{saved=true;return json({show_botmux:true,configured:false});});
 await refreshAgentsDisplay();expect(saved).toBe(true);expect(localStorage.getItem("persona.agentHideBotmux")).toBeNull();
});
it("retries the failed write after a background refresh", async () => {
  await refreshAgentsDisplay();
  fail = "PUT";
  await saveAgentsDisplay(false);
  await refreshAgentsDisplay();
  fail = "";
  await retryAgentsDisplay();
  expect(requests.at(-1)).toEqual({ method: "PUT", body: { show_botmux: false } });
  expect(saved).toBe(false);
});
it("hides botmux rows until the hub acknowledges the display preference", async () => {
  const host = document.createElement("div");
  const root = createRoot(host);
  function Probe() { return <span>{useBotmuxHidden() ? "hidden" : "shown"}</span>; }
  await act(async () => { root.render(<Probe />); });
  expect(host.textContent).toBe("hidden");
  vi.mocked(fetch).mockResolvedValueOnce(new Response(null, { status: 404 }));
  await act(async () => { await retryAgentsDisplay(); });
  expect(host.textContent).toBe("hidden");
  await act(async () => { await retryAgentsDisplay(); });
  expect(host.textContent).toBe("shown");
  await act(async () => { root.unmount(); });
});
it("updates display consumers only when their own acknowledged choice changes", async () => {
  saved = false;
  await refreshAgentsDisplay();
  const host = document.createElement("div");
  const root = createRoot(host);
  let botmuxRenders = 0;
  let titleRenders = 0;
  function Botmux() {
    botmuxRenders++;
    return <span>{useBotmuxHidden() ? "hidden" : "shown"}</span>;
  }
  function Title() {
    titleRenders++;
    return <span>{useHerdrTabName() ? "tab" : "conversation"}</span>;
  }
  await act(async () => { root.render(<><Botmux /><Title /></>); });
  expect(host.textContent).toBe("hiddenconversation");

  await act(refreshAgentsDisplay);
  fail = "GET";
  await act(refreshAgentsDisplay);
  expect([botmuxRenders, titleRenders]).toEqual([1, 1]);

  fail = "";
  preferTab = true;
  await act(refreshAgentsDisplay);
  expect(host.textContent).toBe("hiddentab");
  expect([botmuxRenders, titleRenders]).toEqual([1, 2]);

  saved = true;
  await act(refreshAgentsDisplay);
  expect(host.textContent).toBe("showntab");
  expect([botmuxRenders, titleRenders]).toEqual([2, 2]);
  await act(async () => { root.unmount(); });
});

it("does not apply a stale read over a save or a credential change",async()=>{
 let finish!:(response:Response)=>void;
 vi.mocked(fetch).mockImplementationOnce(()=>new Promise(resolve=>{finish=resolve;}));
 const read=refreshAgentsDisplay();await saveAgentsDisplay(false);finish(json({show_botmux:true,configured:true}));await read;
 expect(saved).toBe(false);
 // Read back through the actual hook and prove failure keeps the acknowledged value.
 const host=document.createElement("div");const root=createRoot(host);
 function Probe(){const s=useAgentsDisplay();return <span>{JSON.stringify(s.value)}</span>;}
 await act(async()=>{root.render(<Probe/>);});expect(host.textContent).toContain('"show_botmux":false');
 fail="PUT";await act(async()=>{await saveAgentsDisplay(true);});expect(host.textContent).toContain('"show_botmux":false');
 let done!:(response:Response)=>void;vi.mocked(fetch).mockImplementationOnce(()=>new Promise(resolve=>{done=resolve;}));
 const old=refreshAgentsDisplay();await act(async()=>{setToken("new-owner");done(json({show_botmux:false,configured:true}));await old;});expect(host.textContent).toBe("null");
 await act(async()=>{root.unmount();});
});

it("saves the Settings › Agents sort and Filter switches, hub-synced, and hides them on a hub without the fields", async () => {
  let sort: boolean | undefined = false, filter: boolean | undefined = false;
  vi.mocked(fetch).mockImplementation(async (_url, init) => {
    const body = init?.body ? JSON.parse(init.body as string) : undefined;
    requests.push({ method: init?.method ?? "GET", body });
    if (typeof body?.sort_by_activity === "boolean") sort = body.sort_by_activity;
    if (typeof body?.show_filter === "boolean") filter = body.show_filter;
    return json({ show_botmux: true, configured: false, use_herdr_tab_name: false, ...(sort === undefined ? {} : { sort_by_activity: sort, show_filter: filter }) });
  });
  const host = document.createElement("div");
  const root = createRoot(host);
  const sw = (label: string) => [...host.querySelectorAll<HTMLButtonElement>('[role="switch"]')].find((b) => b.getAttribute("aria-label") === label);
  function Probe() { return <span data-probe>{String(useSortByActivity())}/{String(useShowFilter())}</span>; }
  await act(async () => { root.render(<><AgentsDisplayRow /><Probe /></>); });
  expect(sw(t("settings.sort_by_activity"))!.getAttribute("aria-checked")).toBe("false");
  expect(sw(t("settings.show_agents_filter"))!.getAttribute("aria-checked")).toBe("false");
  expect(host.querySelector("[data-probe]")!.textContent).toBe("false/false");
  await act(async () => { sw(t("settings.sort_by_activity"))!.click(); });
  expect(requests.at(-1)).toEqual({ method: "PUT", body: { sort_by_activity: true } });
  await act(async () => { sw(t("settings.show_agents_filter"))!.click(); });
  expect(requests.at(-1)).toEqual({ method: "PUT", body: { show_filter: true } });
  expect(host.querySelector("[data-probe]")!.textContent).toBe("true/true");
  // an older hub does not send them: no switches, and the list keeps its stable order with no Filter
  sort = undefined;
  resetAgentsDisplay();
  await act(refreshAgentsDisplay);
  expect(sw(t("settings.sort_by_activity"))).toBeUndefined();
  expect(sw(t("settings.show_agents_filter"))).toBeUndefined();
  expect(host.querySelector("[data-probe]")!.textContent).toBe("false/false");
  await act(async () => { root.unmount(); });
});
