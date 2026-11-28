// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { getToken, setPairedToken } from "../../lib/api";
import { chooseManager, MANAGER_HEADER, resetManagerChoice } from "../../lib/manager-choice";
import { resetRoster, setRoster, type ManagerList } from "../../lib/managers";
import { bindScopedAssistant, ownedAssistant } from "../../lib/scoped-assistant-api";
import type { ExtensionTrust } from "../../lib/tracks";
import { ExtensionInfoAccess } from "./ExtensionInfoAccess";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const managers = [
  { id: "assistant", name: "Home", online: true, extension_info_access_scoped: true },
  { id: "work", name: "Work", online: true, extension_info_access_scoped: true },
];
const extension = "notes";
const grant = (trust: ExtensionTrust["trust"], categories: string[] = []): ExtensionTrust => ({ extension, trust, categories });
const ownerGrant = grant("owner");
const ownerSwitch = () => host.querySelector<HTMLButtonElement>('[data-extension-info-access] [role="switch"]');
const retry = () => [...host.querySelectorAll("button")].find((button) => button.textContent === t("settings-extensions.retry"))!;
const draw = (managerId: string, editable = true) => act(async () => {
  const client = bindScopedAssistant(ownedAssistant(managerId), { kind: "extension", id: extension });
  root.render(<ExtensionInfoAccess extension={extension} client={client} editable={editable} />);
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

let host: HTMLDivElement;
let root: Root;
let servedRoster: ManagerList | null;
let grants: Record<string, ExtensionTrust | undefined>;
let read: (managerId: string) => Response | Promise<Response>;
let write: (managerId: string, update: ExtensionTrust) => Response | Promise<Response>;
const calls: { method: string; manager: string | null }[] = [];

beforeEach(async () => {
  await initI18n("en");
  servedRoster = { default: "assistant", managers };
  setRoster(servedRoster);
  calls.length = 0;
  grants = {};
  read = (managerId) => Response.json({ items: grants[managerId] ? [grants[managerId]] : [] });
  write = (managerId, update) => {
    grants[managerId] = { extension, ...update };
    return Response.json(grants[managerId]);
  };
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    if (url.endsWith("/managers")) return servedRoster
      ? Response.json(servedRoster) : Response.json({ error: "Missing route" }, { status: 404 });
    if (!url.includes("/assistant/extension-trust")) return Response.json({});
    const method = init?.method ?? "GET";
    const manager = new Headers(init?.headers).get(MANAGER_HEADER);
    calls.push({ method, manager });
    const target = manager ?? "assistant";
    return method === "PUT" ? write(target, JSON.parse(String(init?.body))) : read(target);
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetManagerChoice();
  resetRoster();
  vi.unstubAllGlobals();
});

it("targets the named owner while the sidebar shows another assistant and the default changes", async () => {
  chooseManager("work");
  await draw("assistant");
  expect(calls).toContainEqual({ method: "GET", manager: "assistant" });

  await act(async () => setRoster(servedRoster = { default: "work", managers }));
  await act(async () => ownerSwitch()!.click());
  expect(calls).toContainEqual({ method: "PUT", manager: "assistant" });
  expect(calls.every(({ manager }) => manager === "assistant")).toBe(true);
});

it("uses headerless reads and writes on a hub from before the roster", async () => {
  resetRoster();
  servedRoster = null;
  await draw("assistant");
  await act(async () => ownerSwitch()!.click());
  expect(calls).toEqual([{ method: "GET", manager: null }, { method: "PUT", manager: null }, { method: "GET", manager: null }]);
});

it("offers the third-party default only after the target confirms an empty list", async () => {
  const pending = deferred<Response>();
  read = () => pending.promise;
  await draw("work");
  expect(host.querySelector("[data-extension-info-access] [role=status]")).not.toBeNull();
  expect(ownerSwitch()).toBeNull();
  expect(calls).toEqual([{ method: "GET", manager: "work" }]);

  await act(async () => pending.resolve(Response.json({ items: [] })));
  const switches = [...host.querySelectorAll<HTMLButtonElement>("[data-extension-info-access] [role=switch]")];
  expect(switches.length).toBeGreaterThan(1);
  expect(switches.every((control) => !control.disabled && control.getAttribute("aria-checked") === "false")).toBe(true);
});

it.each([
  ["a load error", () => Response.json({ error: "Couldn't read grants" }, { status: 500 }), "load_failed"],
  ["a refused read", () => Response.json({}, { status: 403 }), "unconfirmed"],
  ["a missing list", () => Response.json({}), "unconfirmed"],
  ["an unknown trust value", () => Response.json({ items: [{ extension, trust: "unknown", categories: [] }] }), "unconfirmed"],
] as const)("keeps switches out after %s and retries the same owner", async (_, response, message) => {
  grants.work = ownerGrant;
  const confirmedRead = read;
  read = response;
  await draw("work");
  expect(ownerSwitch()).toBeNull();
  const text = t(`settings-extensioninfoaccess.${message}`, { error: "" }).split(":")[0];
  expect(host.textContent).toContain(text);
  read = confirmedRead;
  await act(async () => retry().click());
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("true");
  expect(calls).toEqual([{ method: "GET", manager: "work" }, { method: "GET", manager: "work" }]);
});

it("shows confirmed grants on a read-only detail and disables each write control", async () => {
  grants.work = grant("third_party", ["finance"]);
  await draw("work", false);
  const switches = [...host.querySelectorAll<HTMLButtonElement>("[data-extension-info-access] [role=switch]")];
  expect(switches.length).toBeGreaterThan(1);
  expect(switches.filter((control) => control.getAttribute("aria-checked") === "true")).toHaveLength(1);
  expect(switches.every((control) => control.disabled)).toBe(true);
  await act(async () => switches.forEach((control) => control.click()));
  expect(calls).toEqual([{ method: "GET", manager: "work" }]);
});

it("ignores a late read for the other assistant's copy of the same extension", async () => {
  const pending = deferred<Response>();
  const confirmedRead = read;
  read = (managerId) => managerId === "assistant" ? pending.promise : confirmedRead(managerId);
  await draw("assistant");
  await draw("work");
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("false");
  await act(async () => pending.resolve(Response.json({ items: [ownerGrant] })));
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("false");
  expect(calls).toEqual([{ method: "GET", manager: "assistant" }, { method: "GET", manager: "work" }]);
});

it("saves each assistant's copy independently while the other copy's save is still pending", async () => {
  grants.assistant = grant("third_party", ["finance"]);
  grants.work = grant("owner", ["health"]);
  const pending = deferred<void>();
  const accepted = write;
  write = async (managerId, update) => {
    if (managerId === "assistant") await pending.promise;
    return accepted(managerId, update);
  };
  await draw("assistant");
  await act(async () => ownerSwitch()!.click());
  expect(ownerSwitch()?.disabled).toBe(true);
  await draw("work");
  await act(async () => ownerSwitch()!.click());
  expect(grants.work).toEqual(grant("third_party", ["health"]));
  expect(grants.assistant).toEqual(grant("third_party", ["finance"]));

  await act(async () => pending.resolve());
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("false");
  expect(grants.assistant).toEqual(grant("owner", ["finance"]));
  expect(calls.filter(({ method }) => method === "PUT").map(({ manager }) => manager)).toEqual(["assistant", "work"]);
  await draw("assistant");
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("true");
});

it("clears the confirmed value on re-pairing and ignores an older write response", async () => {
  const pendingWrite = deferred<Response>();
  write = () => pendingWrite.promise;
  await draw("work");
  await act(async () => ownerSwitch()!.click());
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("true");

  const pendingRead = deferred<Response>();
  read = () => pendingRead.promise;
  await act(async () => setPairedToken(getToken()));
  expect(ownerSwitch()).toBeNull();
  expect(host.querySelector("[data-extension-info-access] [role=status]")).not.toBeNull();
  await act(async () => pendingWrite.resolve(Response.json(ownerGrant)));
  expect(ownerSwitch()).toBeNull();
  await act(async () => pendingRead.resolve(Response.json({ items: [] })));
  expect(ownerSwitch()?.getAttribute("aria-checked")).toBe("false");
  expect(calls.map(({ method }) => method)).toEqual(["GET", "PUT", "GET"]);
});

it("requires current roster capability after re-pairing even when the old roster is still displayed", async () => {
  await draw("work");
  expect(ownerSwitch()).not.toBeNull();
  servedRoster = { default: "assistant", managers: managers.map((m) => ({ ...m, extension_info_access_scoped: false })) };
  await act(async () => setPairedToken(getToken()));
  expect(host.querySelector("[data-extension-info-access]")).toBeNull();
  expect(calls).toEqual([{ method: "GET", manager: "work" }]);
});
