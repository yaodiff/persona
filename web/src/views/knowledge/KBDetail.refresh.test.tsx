// @vitest-environment jsdom
// An open entry read again after its tags were saved (#5048): a failed read keeps the last good detail but says so,
// with Retry; Retry only reads again. Errors go through the real REST decoder.
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import { kindTitle, type KBDetailResponse } from "../../lib/kb";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { EntryDetail } from "./KBDetail";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const entry = (tags: string[]): KBDetailResponse => ({ detail: { kind: "people", id: "example", path: "", meta: { name: "Example entry", tags }, summary: "# Example entry" } });

const back = vi.fn();
// The page's owner bumps reloadKey after a save, as Knowledge does.
function Page() {
  const [reloadKey, setReloadKey] = useState(0);
  return <EntryDetail kind="people" id="example" reloadKey={reloadKey} onToggleSidebar={() => {}} onBack={back} onOpen={() => {}} onSaved={() => setReloadKey((k) => k + 1)} />;
}
const render = () => act(() => root.render(<Page />));
const json = (r: Request, value: unknown, status = 200) => act(async () => r.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } })));
const refuse = (r: Request) => json(r, { code: "kb_unavailable", error: "Request failed" }, 503);
const button = (label: string) => [...document.body.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === label);
const reads = () => requests.filter((r) => !r.init.method && r.url === "/api/v1/kb/people/example");
const writes = () => requests.filter((r) => r.init.method === "PUT");
const unavailable = () => t("lib-api.knowledge_unavailable");
const retry = () => button(t("knowledge-kblist.retry"));

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetManagerChoice();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  requests = [];
  back.mockReset();
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  resetManagerChoice();
  vi.unstubAllGlobals();
});

it("shows a failed first read with Retry, as before", async () => {
  render();
  await refuse(reads()[0]);
  expect(host.textContent).toContain(unavailable());
  expect(retry()).toBeDefined();
  act(() => retry()!.click());
  expect(reads()).toHaveLength(2);
  await json(reads()[1], entry(["old"]));
  expect(host.textContent).not.toContain(unavailable());
  expect(host.textContent).toContain("#old");
});

it("keeps the detail but shows a failed read after saving tags, and Retry reads it again without saving again", async () => {
  render();
  await json(reads()[0], entry(["old"]));
  expect(host.textContent).not.toContain(unavailable());
  act(() => button("Tag")!.click());
  await act(async () => button("Save")!.click());
  expect(writes()).toHaveLength(1);
  await json(writes()[0], { id: "example" });
  expect(reads()).toHaveLength(2);
  await refuse(reads()[1]);
  // the last good detail stays, and the page says its read failed
  expect(host.textContent).toContain("Example entry");
  expect(host.textContent).toContain("#old");
  expect(host.textContent).toContain(unavailable());
  expect(retry()).toBeDefined();

  act(() => retry()!.click());
  expect(reads()).toHaveLength(3);
  expect(writes()).toHaveLength(1);
  await json(reads()[2], entry(["new"]));
  expect(host.textContent).not.toContain(unavailable());
  expect(host.textContent).toContain("#new");
  expect(host.textContent).not.toContain("#old");
});

it("shows nothing extra after a successful read following the save", async () => {
  render();
  await json(reads()[0], entry(["old"]));
  act(() => button("Tag")!.click());
  await act(async () => button("Save")!.click());
  await json(writes()[0], { id: "example" });
  await json(reads()[1], entry(["new"]));
  expect(host.textContent).toContain("#new");
  expect(host.textContent).not.toContain(unavailable());
  expect(retry()).toBeUndefined();
});

it("says an entry the hub doesn't have isn't there, in the shared empty state with the way back (web usability audit item 42)", async () => {
  render();
  await json(reads()[0], { code: "not_found", error: "not found" }, 404);
  const status = host.querySelector("[role=status]");
  expect(status?.textContent).toContain(t("not-found.kb_title"));
  expect(status?.textContent).toContain(t("not-found.kb_detail"));
  expect(host.textContent).not.toContain("404");
  expect(host.textContent).not.toContain(t("not-found.kb_gone"));
  // not the entry's page: no title made of its id, no buttons that can't be used, nothing to retry
  expect(host.querySelector("h1")).toBeNull();
  expect(button("Tag")).toBeUndefined();
  expect(button(t("knowledge-kbdetail.ask"))).toBeUndefined();
  expect(host.querySelector("button:disabled")).toBeNull();
  expect(retry()).toBeUndefined();
  // the header names the list it would be in
  expect(host.querySelector("header")?.textContent).toContain(kindTitle("people"));
  const toList = [...status!.querySelectorAll("button")].find((b) => b.textContent?.trim() === t("knowledge-kbdetail.back"));
  act(() => toList!.click());
  expect(back).toHaveBeenCalledTimes(1);
});

it("keeps the detail and says so when a read after a save finds the entry gone", async () => {
  render();
  await json(reads()[0], entry(["old"]));
  act(() => button("Tag")!.click());
  await act(async () => button("Save")!.click());
  await json(writes()[0], { id: "example" });
  await json(reads()[1], { code: "not_found", error: "not found" }, 404);
  expect(host.textContent).toContain("Example entry");
  expect(host.textContent).toContain(t("not-found.kb_gone"));
  expect(host.querySelector("[role=status]")?.textContent ?? "").not.toContain(t("not-found.kb_title"));
});
