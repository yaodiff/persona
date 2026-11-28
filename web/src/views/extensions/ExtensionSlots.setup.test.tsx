// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { loadPages } from "../../lib/lazy-page";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import * as stream from "../../lib/extension-ui-app-stream";
import { LegacyExtensionUI } from "../settings/ExtensionView";
import { NativeExtensionUI } from "../settings/NativeExtensionUI";
import { ExtensionNavPage } from "./ExtensionSlots";

vi.mock("../../lib/extension-slots", () => ({ useExtensionPlace: () => undefined, useExtensionSlot: () => [] }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const extension = "meeting-notes";
const readyView = { view: { sections: [{ items: [
  { type: "text", text: "Your meeting notes" },
  { type: "field", kind: "text", name: "note", label: "Note", value: "" },
] }] } };
const missing = { code: "not_found", error: "No such extension" };
const required = { id: extension, name: "Meeting Notes", enabled: true, status: "needs_setup", needs_setup: ["Calendar token", "Notes folder"] };
const requests: { path: string; manager: string | null; method: string }[] = [];
let host: HTMLDivElement;
let root: Root;
let drawReady: boolean;
let drawErrorStatus: number;
let detail: { status: number; body: unknown };
let pendingDetail: Promise<Response> | undefined;

function respond(status: number, body: unknown) {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

const detailRequests = () => requests.filter((r) => r.method === "GET" && r.path.endsWith(`/${extension}/-/`));
const drawRequests = () => requests.filter((r) => r.method === "POST" && r.path.endsWith(`/${extension}/-/ui/home`));
const settingsControl = () => [...host.querySelectorAll<HTMLElement>("button, a")].find((el) => /settings/i.test(el.textContent ?? ""));

async function openPage(id = extension) {
  await act(async () => root.render(<ExtensionNavPage extension={id} ui="home" onToggleSidebar={() => {}} />));
  await act(async () => { await vi.dynamicImportSettled(); });
}

beforeAll(async () => {
  await initI18n("en");
  await loadPages();
}, 60_000);

beforeEach(() => {
  resetManagerChoice();
  history.replaceState(null, "", `/extension/${extension}/home`);
  drawReady = false;
  drawErrorStatus = 404;
  pendingDetail = undefined;
  detail = { status: 200, body: required };
  requests.length = 0;
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.stubGlobal("fetch", vi.fn(async (input: string, init?: RequestInit) => {
    const path = new URL(input, location.origin).pathname;
    const manager = new Headers(init?.headers).get("X-Persona-Manager");
    requests.push({ path, manager, method: init?.method ?? "GET" });
    if (path.endsWith("/-/ui/home")) return drawReady ? respond(200, readyView) : respond(drawErrorStatus, missing);
    if (path.endsWith(`/${extension}/-/`)) {
      if (manager === "work") return respond(200, { ...required, needs_setup: ["Work account credential"] });
      return pendingDetail ?? respond(detail.status, detail.body);
    }
    return respond(404, missing);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("opening a page for an extension awaiting setup", () => {
  it("explains the default installation's missing settings and opens that same copy", async () => {
    setRosterDefault("home");
    chooseManager("work");
    await openPage();
    expect(host.textContent).toContain("Calendar token");
    expect(host.textContent).toContain("Notes folder");
    expect(host.textContent).not.toContain("Work account credential");
    expect(host.textContent).not.toContain(t("settings-extensionview.draw_failed"));
    expect(drawRequests()).toHaveLength(1);
    expect(detailRequests()).toHaveLength(1);
    expect(detailRequests()[0].manager).not.toBe("work");
    const control = settingsControl();
    expect(control).toBeDefined();
    await act(async () => control!.click());
    expect(location.pathname).toBe("/settings");
    const query = new URLSearchParams(location.search);
    expect(query.get("tab")).toBe("extensions");
    expect(query.get("extension")).toBe(`${extension}@home`);
  });

  it("opens a healthy page directly, with no installation lookup", async () => {
    drawReady = true;
    await openPage();
    expect(host.textContent).toContain("Your meeting notes");
    expect(detailRequests()).toEqual([]);
    expect(settingsControl()).toBeUndefined();
  });

  it("preserves typed fields when the first manager roster names the default", async () => {
    drawReady = true;
    await openPage();
    const input = host.querySelector("input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Unsaved note");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => setRosterDefault("home"));
    expect(host.querySelector("input")).toBe(input);
    expect(input.value).toBe("Unsaved note");
    expect(drawRequests()).toHaveLength(1);
    expect(detailRequests()).toHaveLength(0);
  });

  it("recovers when a native page open falls back to a missing HTTP view", async () => {
    setRosterDefault("home");
    const opens: string[] = [];
    vi.spyOn(stream, "appUIStreamPage").mockImplementation((options, manager, effect, failed) => {
      let receive: (raw: string) => void;
      return new stream.AppUIStreamPage(options, manager, {
        connected: () => true,
        connection: () => () => {},
        frames: (listener) => { receive = listener; return () => {}; },
        send: (request) => {
          if (request.request !== "open") return;
          opens.push(manager);
          queueMicrotask(() => receive(JSON.stringify({
            type: "extension_ui_stream", manager, request_id: request.request_id, kind: "error",
            error: { code: "negotiation", message: "No extension page" },
          })));
        },
      }, effect, failed);
    });
    await act(async () => root.render(<NativeExtensionUI extension={extension} ui="home" setupOwner="home"
      legacy={(props) => <LegacyExtensionUI {...props} />} />));
    await act(async () => { await vi.dynamicImportSettled(); });
    expect(opens).toEqual(["home"]);
    expect(drawRequests()).toHaveLength(1);
    expect(detailRequests()).toHaveLength(1);
    expect(host.textContent).toContain("Calendar token");
  });

  it("preserves transport errors and never mistakes them for missing setup", async () => {
    drawErrorStatus = 502;
    await openPage();
    expect(host.textContent).toContain(t("settings-extensionview.draw_failed"));
    expect(detailRequests()).toHaveLength(0);
    expect(settingsControl()).toBeUndefined();
  });

  it("ignores late installation details after opening another extension", async () => {
    let finish!: (response: Response) => void;
    pendingDetail = new Promise((resolve) => { finish = resolve; });
    await openPage();
    expect(detailRequests()).toHaveLength(1);
    drawReady = true;
    await openPage("daily-report");
    expect(host.textContent).toContain("Your meeting notes");
    await act(async () => finish(respond(200, required)));
    expect(host.textContent).not.toContain("Calendar token");
    expect(settingsControl()).toBeUndefined();
  });

  it("retries after setup and replaces the instructions with the working page", async () => {
    await openPage();
    expect(host.textContent).toContain("Calendar token");
    drawReady = true;
    const redraw = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("extensions-extensionslots.redraw")}"]`);
    expect(redraw).not.toBeNull();
    await act(async () => redraw!.click());
    expect(host.textContent).toContain("Your meeting notes");
    expect(host.textContent).not.toContain("Calendar token");
    expect(detailRequests()).toHaveLength(1);
  });

  it.each([
    { name: "missing installation", status: 404, body: missing },
    { name: "different extension", status: 200, body: { ...required, id: "another" } },
    { name: "different installation owner", status: 200, body: { ...required, manager: "work" } },
    { name: "running installation with a missing page", status: 200, body: { ...required, status: "running" } },
  ])("keeps the page error for a $name", async ({ status, body }) => {
    detail = { status, body };
    setRosterDefault("home");
    await openPage();
    expect(detailRequests()).toHaveLength(1);
    expect(host.textContent).toContain(t("settings-extensionview.draw_failed"));
    expect(settingsControl()).toBeUndefined();
    expect(host.textContent).not.toContain("Calendar token");
  });
});
