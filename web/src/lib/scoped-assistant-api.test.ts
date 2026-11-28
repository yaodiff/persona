// @vitest-environment jsdom
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api, setPairedToken, setToken } from "./api";
import { chooseManager, MANAGER_HEADER, pinManager, resetManagerChoice } from "./manager-choice";
import { resetRoster, setRoster, type ManagerList } from "./managers";
import { AssistantScopeUnverified, bindScopedAssistant, currentAssistant, currentScopedAssistantRoster, ownedAssistant, verifyScopedAssistantRoster } from "./scoped-assistant-api";

const home = { id: "assistant", name: "Home", online: true };
const work = { id: "work", name: "Work", online: true };
const roster: ManagerList = { default: home.id, managers: [home, work] };
const resource = { kind: "extension-trust", id: "notes" };
const path = "/assistant/extension-trust";
const write = { method: "PUT", json: { trust: "owner" } } as const;
const json = (value: unknown, status = 200) => Response.json(value, { status });
const defaultOwner = () => bindScopedAssistant(ownedAssistant(home.id), resource);
const workOwner = () => bindScopedAssistant(ownedAssistant(work.id), resource);
const manager = (init?: RequestInit) => new Headers(init?.headers).get(MANAGER_HEADER);
const targetRequest = ([url]: [string, RequestInit?]) => url.startsWith(`/api/v1${path}`);

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function hub(handler: (url: string, init?: RequestInit) => Response | Promise<Response> = (_, init) => json({ manager: manager(init) })) {
  const fetcher = vi.fn(async (url: string, init?: RequestInit) =>
    targetRequest([url, init]) || url.endsWith("/managers") ? handler(url, init) : json({}));
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}

beforeEach(() => {
  setToken("test-scoped-credential");
  resetManagerChoice();
  resetRoster();
});

afterEach(() => {
  setToken("");
  vi.unstubAllGlobals();
});

it("names an owned default explicitly while current follows the sidebar at binding time", async () => {
  setRoster({ ...roster });
  chooseManager(work.id);
  const fetcher = hub();
  const current = bindScopedAssistant(currentAssistant, resource);
  const owned = defaultOwner();

  await expect(api(path)).resolves.toEqual({ manager: work.id });
  await expect(current.request(path)).resolves.toEqual({ manager: work.id });
  await expect(owned.request(path)).resolves.toEqual({ manager: home.id });

  const queuedSave = () => owned.request(`${path}/notes`, write);
  setRoster({ ...roster, default: work.id });
  await expect(queuedSave()).resolves.toEqual({ manager: home.id });
  expect(fetcher.mock.calls.filter(targetRequest).map(([, init]) => manager(init))).toEqual([work.id, work.id, home.id, home.id]);
});

it("starts headerless with one installed assistant and verifies the same owner before sending queued work after expansion", async () => {
  setRoster({ default: home.id, managers: [home, { ...work, installing: "2030-01-01T00:00:00Z" }] });
  hub();
  const client = bindScopedAssistant(currentAssistant, resource);
  await expect(client.request(path)).resolves.toEqual({ manager: null });
  const queuedSave = () => client.request(`${path}/notes`, write);

  setRoster({ ...roster, default: work.id });
  await expect(queuedSave()).resolves.toEqual({ manager: home.id });
  await expect(bindScopedAssistant(currentAssistant, resource).request(path)).resolves.toEqual({ manager: work.id });
});

it("uses the historical request on a legacy hub and requires reopening an unverified target if the discovered default differs", async () => {
  const fetcher = hub((url, init) => url.endsWith("/managers") ? json({ error: "Missing route" }, 404) : json({ manager: manager(init) }));
  const legacy = bindScopedAssistant(currentAssistant, resource);
  await expect(legacy.request(path)).resolves.toEqual({ manager: null });
  await expect(legacy.request(path)).resolves.toEqual({ manager: null });
  expect(fetcher.mock.calls.filter(([url]) => url.endsWith("/managers"))).toHaveLength(1);

  setRoster({ ...roster, default: work.id });
  await expect(legacy.request(`${path}/notes`, write)).rejects.toBeInstanceOf(AssistantScopeUnverified);
  expect(fetcher.mock.calls.filter(targetRequest)).toHaveLength(2);
  await expect(defaultOwner().request(path)).resolves.toEqual({ manager: home.id });
});

it("keeps a target pending on roster failure and refuses an ambiguous headerless route", async () => {
  const fetcher = hub(() => json({ error: "Unavailable" }, 503));
  const client = defaultOwner();
  await expect(client.request(path)).rejects.toMatchObject({ status: 503 });
  expect(fetcher.mock.calls.map(([url]) => url)).toEqual(["/api/v1/managers"]);

  setRoster({ default: home.id, managers: [home] });
  const unpin = pinManager(path, work.id);
  await expect(client.request(path)).rejects.toBeInstanceOf(AssistantScopeUnverified);
  unpin();
  await expect(client.request(`${path}?manager=work`)).rejects.toBeInstanceOf(AssistantScopeUnverified);
  expect(fetcher.mock.calls.filter(targetRequest)).toHaveLength(0);
  hub();
  await expect(client.request(path)).resolves.toEqual({ manager: null });
});

it("ignores an unheaded response after roster expansion and rereads the original target by name", async () => {
  setRoster({ default: home.id, managers: [home] });
  const late = deferred<Response>();
  const fetcher = hub((_, init) => manager(init) ? json({ manager: manager(init) }) : late.promise);
  const client = defaultOwner();
  const pending = client.request(path);
  setRoster({ ...roster, default: work.id });
  await expect(client.request(path)).resolves.toEqual({ manager: home.id });
  late.resolve(json({ manager: work.id }));
  await expect(pending).rejects.toBeInstanceOf(AssistantScopeUnverified);
  expect(fetcher.mock.calls.filter(targetRequest).map(([, init]) => manager(init))).toEqual([null, home.id]);
});

it("preserves unknown and offline hub errors and retries only the same target on recovery", async () => {
  setRoster({ ...roster, managers: [home, { ...work, online: false }] });
  let online = false;
  const fetcher = hub((_, init) => {
    if (manager(init) === work.id) return online ? json({ manager: work.id }) : json({ code: "assistant_offline" }, 503);
    return json({ code: "no_manager" }, 404);
  });
  const deleted = bindScopedAssistant(ownedAssistant("deleted"), resource);
  const offline = workOwner();
  await expect(deleted.request(path)).rejects.toMatchObject({ status: 404, code: "no_manager" });
  await expect(offline.request(path)).rejects.toMatchObject({ status: 503, code: "assistant_offline" });
  online = true;
  setRoster({ ...roster });
  await expect(offline.request(path)).resolves.toEqual({ manager: work.id });
  expect(fetcher.mock.calls.filter(targetRequest).map(([, init]) => manager(init))).toEqual(["deleted", work.id, work.id]);
});

it("coalesces concurrent reads only within the credential, manager, kind, id, and route", async () => {
  setRoster({ ...roster });
  const release = deferred<void>();
  const fetcher = hub(async (url, init) => { await release.promise; return json({ url, manager: manager(init) }); });
  const differentId = bindScopedAssistant(ownedAssistant(home.id), { ...resource, id: "tasks" });
  const differentKind = bindScopedAssistant(ownedAssistant(home.id), { ...resource, kind: "connector" });
  const pending = [defaultOwner().request(path), defaultOwner().request(path), workOwner().request(path),
    differentId.request(path), differentKind.request(path), defaultOwner().request(`${path}?other=1`)];
  expect(fetcher.mock.calls.filter(targetRequest)).toHaveLength(5);
  release.resolve();
  const results = await Promise.all(pending);
  expect(results[0]).toEqual(results[1]);
  expect(results[2]).toMatchObject({ manager: work.id });
});

it("discards reads from before a write and refreshes subscribers for just that resource", async () => {
  setRoster({ ...roster });
  const late = deferred<Response>();
  const fetcher = hub((_, init) => init?.method === "PUT" ? json({ trust: "owner" }) : late.promise.then((res) => res.clone()));
  const client = defaultOwner();
  const pending = client.request(path);
  const refresh = vi.fn(), other = vi.fn();
  const stop = defaultOwner().subscribe(refresh);
  workOwner().subscribe(other);
  bindScopedAssistant(ownedAssistant(home.id), { ...resource, id: "tasks" }).subscribe(other);
  await client.request(`${path}/notes`, write);
  expect(refresh).toHaveBeenCalledOnce();
  expect(other).not.toHaveBeenCalled();

  const next = client.request(path);
  expect(fetcher.mock.calls.filter(targetRequest)).toHaveLength(3);
  late.resolve(json({ items: [] }));
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  await expect(next).resolves.toEqual({ items: [] });
  stop();
  await client.request(`${path}/notes`, write);
  expect(refresh).toHaveBeenCalledOnce();
});

it("retires reads, subscribers, and queued writes on re-pairing with the same token", async () => {
  setRoster({ ...roster });
  const late = deferred<Response>();
  const fetcher = hub((url, init) => url.endsWith("/managers") ? json(roster) : init?.method === "PUT" ? json({}) : late.promise.then((res) => res.clone()));
  const old = defaultOwner();
  const oldEvents = vi.fn();
  old.subscribe(oldEvents);
  const pending = old.request(path);
  setPairedToken("test-scoped-credential");

  const fresh = defaultOwner();
  const freshEvents = vi.fn();
  fresh.subscribe(freshEvents);
  expect(fresh.key).not.toEqual(old.key);
  expect(fresh.key).not.toContain("test-scoped-credential");
  await expect(old.request(`${path}/notes`, write)).rejects.toMatchObject({ name: "AbortError" });
  const freshRead = fresh.request(path);
  late.resolve(json({ items: [] }));
  await expect(pending).rejects.toMatchObject({ name: "AbortError" });
  await expect(freshRead).resolves.toEqual({ items: [] });
  await fresh.request(`${path}/notes`, write);
  expect(fetcher.mock.calls.filter(([url]) => url.endsWith(path))).toHaveLength(2);
  expect(oldEvents).not.toHaveBeenCalled();
  expect(freshEvents).toHaveBeenCalledOnce();
});

it("verifies capability with the current credential when an earlier roster read is still pending", async () => {
  setRoster({ ...roster });
  // A live editor has used the roster when its credential is replaced.
  currentScopedAssistantRoster();
  const late = deferred<Response>();
  const capable = { ...work, extension_info_access_scoped: true };
  const previous = { ...roster, managers: [home, capable] };
  let rosterReads = 0;
  hub(() => ++rosterReads === 1 ? late.promise : json(roster));

  const oldRead = verifyScopedAssistantRoster();
  const oldResult = expect(oldRead).rejects.toMatchObject({ name: "AbortError" });
  setPairedToken("test-scoped-credential");
  setRoster(previous);
  expect(currentScopedAssistantRoster()).toBeNull();
  await expect(verifyScopedAssistantRoster()).resolves.toEqual(roster);
  late.resolve(json(previous));
  await oldResult;
});
