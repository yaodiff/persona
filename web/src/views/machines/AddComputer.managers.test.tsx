// @vitest-environment jsdom
// Add computer with several assistants (docs/assistant/managers.md "Clients", docs/protocol/protocol.md "Computer pairing
// codes"): the new computer is the current assistant's (its real id in add.manager, once the roster said which), the
// waiting screen says where it went when that is another (or the hub didn't say: one from before add.manager), an
// offline current assistant takes none, and the computers in the way of a new one are every assistant's.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const kept = new Map<string, string>();
  const g = globalThis as unknown as { localStorage: Pick<Storage, "getItem" | "setItem" | "removeItem"> };
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { loadRoster, resetRoster, setRoster, type ManagerList } from "../../lib/managers";
import { getState } from "../../lib/store";
import { AddComputer } from "./AddComputer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const ROSTER: ManagerList = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "Persona", online: true },
    { id: "m-work", name: "Work", online: true },
    { id: "m-home", name: "Home", online: false, last_seen: new Date(Date.now() - 3_600_000).toISOString() },
  ],
};
const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { "Content-Type": "application/json" } });

let posts: { path: string; body: { add?: Record<string, unknown>; machine?: string } }[] = [];
// what the hub says the new computer's manager is: the one asked (a hub with add.manager), another, or none (one from before it)
let answer: (asked: string | undefined) => string | undefined;
let refuse = "";
let refuseStatus = 404;
// what GET /managers does: never answers (the tests put the roster in place), or fails with this status
let rosterStatus = 0;

function hub(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const path = String(input).split("?")[0]!.replace(/^.*\/api\/v1/, "");
  if (path === "/managers") return rosterStatus ? Promise.resolve(json({ error: "unavailable" }, rosterStatus)) : new Promise(() => {});
  if (init?.method === "POST") posts.push({ path, body: init.body ? JSON.parse(String(init.body)) : {} });
  if (path === "/auth/pair-codes") {
    if (refuse) return Promise.resolve(json({ error: "That assistant isn't on this hub any more", code: refuse }, refuseStatus));
    const asked = posts.at(-1)?.body.add?.manager as string | undefined;
    const manager = answer(asked);
    return Promise.resolve(json({
      token: "jp_x", expires_at: new Date(Date.now() + 600_000).toISOString(), pair_url: "persona://link?machine=new", machine: "new", name: "New one",
      trust: "pending", added: true, ...(manager ? { manager } : {}),
    }));
  }
  if (path === "/machines") return Promise.resolve(json({ machines: [] }));
  return Promise.resolve(json({ error: "not found" }, 404));
}

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const type = async (value: string) => {
  const input = document.querySelector("input")!;
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const add = async () => {
  await act(async () => root.render(<AddComputer open onClose={() => {}} />));
  await flush();
  await type("New one");
  await act(async () => button("下一步")!.click());
  await flush();
};
const note = () => document.querySelector<HTMLElement>("[data-added-under]");

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
  posts = [];
  refuse = "";
  refuseStatus = 404;
  rosterStatus = 0;
  answer = (asked) => asked;
  resetManagerChoice();
  resetRoster();
  setRoster(ROSTER);
  vi.stubGlobal("fetch", vi.fn(hub));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  resetRoster();
  resetManagerChoice();
  Object.assign(getState(), { machines: [] });
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe("Add computer for the current assistant", () => {
  it("names the current assistant in add.manager, and says nothing when the computer went there", async () => {
    act(() => chooseManager("m-work"));
    await add();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { add: { name: "New one", os: "mac", manager: "m-work" } } });
    expect(document.querySelector('[data-stage="waiting"]')).not.toBeNull();
    expect(note()).toBeNull();
  });

  it("names the default by its real id when it is the current one", async () => {
    await add();
    expect(posts[0].body.add?.manager).toBe("assistant");
  });

  it("says where the computer went when the hub put it under another assistant", async () => {
    act(() => chooseManager("m-work"));
    answer = () => "assistant";
    await add();
    expect(note()?.dataset.addedUnder).toBe("assistant");
    expect(note()?.textContent).toBe("这台电脑已添加到「Persona」下。");
  });

  it("says a hub from before add.manager put the new computer under its own assistant", async () => {
    act(() => chooseManager("m-work"));
    answer = () => undefined;
    await add();
    expect(note()?.textContent).toBe("这台电脑已添加到「Persona」下。");
  });

  it("names no manager on a new code for a computer added before", async () => {
    act(() => chooseManager("m-work"));
    await act(async () => root.render(<AddComputer open onClose={() => {}} again={{ id: "old", name: "Old one" }} />));
    await flush();
    expect(posts[0].body).toEqual({ machine: "old" });
    expect(note()).toBeNull();
  });

  it("adds nothing to an offline current assistant, and says why", async () => {
    act(() => chooseManager("m-home"));
    await act(async () => root.render(<AddComputer open onClose={() => {}} />));
    await flush();
    expect(text()).toContain("「Home」当前离线，暂时无法向其添加电脑。");
    expect(button("下一步")).toBeUndefined();
    expect(document.querySelector("input")).toBeNull();
    expect(posts).toEqual([]);
  });

  it("waits for the roster before adding: no request goes with a placeholder id", async () => {
    resetRoster();
    resetManagerChoice();
    await act(async () => root.render(<AddComputer open onClose={() => {}} />));
    await flush();
    await type("New one");
    expect(button("下一步")?.disabled).toBe(true);
    await act(async () => button("下一步")!.click());
    await flush();
    expect(posts).toEqual([]);
    act(() => setRoster(ROSTER));
    await flush();
    expect(button("下一步")?.disabled).toBe(false);
    await act(async () => button("下一步")!.click());
    await flush();
    expect(posts[0].body.add?.manager).toBe("assistant");
  });

  it.each([
    [404, "no_manager"],
    [409, "installing"],
    [503, "assistant_offline"],
    [503, "owners_unknown"],
  ])("shows the hub's own words for %s %s", async (status, code) => {
    act(() => chooseManager("m-work"));
    refuse = code;
    refuseStatus = status;
    await add();
    const said = "That assistant isn't on this hub any more";
    const shown = [...document.querySelectorAll("*")].filter((n) => n.children.length === 0 && n.textContent === said);
    expect(shown.length).toBeGreaterThan(0);
  });

  it("adds to the kept pick when the roster can't be read, and says nothing is wrong", async () => {
    resetRoster();
    act(() => chooseManager("m-work"));
    rosterStatus = 503;
    await act(async () => void (await loadRoster()));
    await add();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { add: { name: "New one", os: "mac", manager: "m-work" } } });
  });

  it("sends exactly the request it always did to a hub with no roster, and says nothing", async () => {
    resetRoster();
    rosterStatus = 404;
    await act(async () => void (await loadRoster()));
    answer = () => undefined;
    await add();
    expect(posts[0]).toEqual({ path: "/auth/pair-codes", body: { add: { name: "New one", os: "mac" } } });
    expect(note()).toBeNull();
  });

  it("names the only assistant, and says nothing when an old hub's reply has no manager", async () => {
    resetRoster();
    setRoster({ default: "assistant", managers: [{ id: "assistant", name: "Persona", online: true }] });
    answer = () => undefined;
    await add();
    expect(posts[0].body.add?.manager).toBe("assistant");
    expect(note()).toBeNull();
  });

  it("lists every assistant's computers waiting to be confirmed when the hub's limit is reached", async () => {
    act(() => chooseManager("m-work"));
    refuse = "too_many_pending";
    const waiting = [["a", "assistant"], ["b", "m-work"], ["c", "m-home"]].map(([id, manager]) => ({ id, name: `Computer ${id}`, manager, online: false, trust: "pending" as const, error_code: "never_connected" }));
    Object.assign(getState(), { machines: waiting });
    await add();
    const box = document.querySelector("[data-too-many]");
    expect(box?.textContent).toContain("Computer a");
    expect(box?.textContent).toContain("Computer b");
    expect(box?.textContent).toContain("Computer c");
  });
});
