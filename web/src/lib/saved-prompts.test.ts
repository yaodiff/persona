// @vitest-environment jsdom
// The user's saved prompts (#7329): the "/" rows they make, the checks said before asking the hub, a name suggested from a
// message, and the one list the page follows (read again on hello, on assistant_saved_prompts and when another assistant
// becomes the current one; an assistant from before them answers 404).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";

const hub = vi.hoisted(() => ({
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
  prompts: new Map<string, { id: string; name: string; text: string }[]>(),
  calls: [] as { path: string; method: string; json?: unknown; manager?: string }[],
  missing: false,
  failNext: null as Error | null,
  /** What the composers get: the module hands them their rows. */
  published: [] as string[][],
  rows: [] as readonly { word: string; text: string; source?: string; key?: string }[],
}));
vi.mock("./saved-prompt-rows", () => ({ setSavedPromptRows: (next: typeof hub.rows) => { hub.rows = next; hub.published.push(next.map((row) => row.word)); } }));
vi.mock("./api", async (original) => {
  const real = await original<typeof import("./api")>();
  const { managerHeaderFor } = await import("./manager-choice");
  return {
    ...real,
    api: vi.fn(async (path: string, init?: { method?: string; json?: unknown }) => {
      const method = init?.method ?? "GET";
      const manager = managerHeaderFor(path) ?? "assistant";
      hub.calls.push({ path, method, json: init?.json, manager });
      if (hub.failNext) {
        const e = hub.failNext;
        hub.failNext = null;
        throw e;
      }
      if (hub.missing) throw new real.ApiError(404, "not found", {});
      const list = hub.prompts.get(manager) ?? [];
      if (method === "GET") return { prompts: list };
      if (method === "POST") {
        const p = { id: `sp${list.length + 1}`, ...(init?.json as { name: string; text: string }) };
        hub.prompts.set(manager, [...list, p]);
        return { prompt: p };
      }
      if (method === "PUT" && path.endsWith("/order")) {
        const ids = (init?.json as { ids: string[] }).ids;
        const next = ids.map((id) => list.find((p) => p.id === id)!);
        hub.prompts.set(manager, next);
        return { prompts: next };
      }
      if (method === "PUT") {
        const id = path.split("/").pop()!;
        const next = list.map((p) => (p.id === id ? { id, ...(init?.json as { name: string; text: string }) } : p));
        hub.prompts.set(manager, next);
        return { prompt: next.find((p) => p.id === id) };
      }
      if (method === "DELETE") {
        hub.prompts.set(manager, list.filter((p) => !path.endsWith(`/${p.id}`)));
        return { ok: true };
      }
      throw new Error(`unexpected ${method} ${path}`);
    }),
  };
});
vi.mock("./store", async (original) => ({
  ...(await original<typeof import("./store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
import { api, handleUnauthorized, setPairedToken, setToken } from "./api";
import { chooseManager, resetManagerChoice, setRosterDefault, settleRosterDefault } from "./manager-choice";
import {
  deleteSavedPrompt,
  moveSavedPrompt,
  resetSavedPrompts,
  saveSavedPrompt,
  savedPromptCommands,
  savedPromptNameProblem,
  savedPromptTextProblem,
  startSavedPrompts,
  suggestedSavedPromptName,
  type SavedPrompt,
} from "./saved-prompts";

const settle = () => new Promise((ok) => setTimeout(ok, 0));
const emit = (type: string) => hub.listeners.get(type)?.forEach((l) => l({ type }));
const prompt = (id: string, name: string, text = `${name} text`): SavedPrompt => ({ id, name, text });

const rows = () => hub.rows;

beforeEach(() => {
  localStorage.clear();
  hub.listeners.clear();
  hub.prompts.clear();
  hub.calls.length = 0;
  hub.missing = false;
  hub.failNext = null;
  hub.rows = [];
  hub.published = [];
  resetManagerChoice();
});

afterEach(() => resetSavedPrompts());

describe("the / rows", () => {
  it("are the names after a slash, typing the text, marked as yours", () => {
    expect(savedPromptCommands([prompt("sp1", "review", "Review this diff.")])).toEqual([
      { key: "saved:sp1", word: "/review", desc: "", text: "Review this diff.", source: t("chat-savedprompts.source") },
    ]);
    expect(savedPromptCommands([])).toEqual([]);
  });
});

describe("the checks before asking the hub", () => {
  const list = [prompt("sp1", "review"), prompt("sp2", "发布说明")];

  it("take one word of letters, digits and - _ ., a slash typed before it dropped", () => {
    for (const ok of ["notes", "/notes", " pr-check_v2.1 ", "日本語", "über", "café"]) expect(savedPromptNameProblem(ok, list, null)).toBe("");
    const rule = t("chat-savedprompts.name_rule", { max: 32 });
    for (const bad of ["", "/", "two words", "a/b", "@me", "x".repeat(33), "tab\there"]) expect(savedPromptNameProblem(bad, list, null)).toBe(rule);
  });

  it("refuse a name another prompt has, case aside, but not the edited prompt's own", () => {
    expect(savedPromptNameProblem("REVIEW", list, null)).toBe(t("chat-savedprompts.name_taken", { name: "REVIEW" }));
    expect(savedPromptNameProblem("Review", list, "sp1")).toBe("");
    expect(savedPromptNameProblem("发布说明", list, "sp1")).not.toBe("");
  });

  it("want some text, at most 8000 characters counted as characters", () => {
    const rule = t("chat-savedprompts.text_rule", { max: 8000 });
    expect(savedPromptTextProblem("  \n ")).toBe(rule);
    expect(savedPromptTextProblem("长".repeat(8000))).toBe("");
    expect(savedPromptTextProblem("a".repeat(8001))).toBe(rule);
  });
});

describe("a name suggested from a message", () => {
  it("joins its first words, lower case, without what a name cannot hold", () => {
    expect(suggestedSavedPromptName("Review this diff, for security issues!", [])).toBe("review-this-diff");
    expect(suggestedSavedPromptName("  ship   it  ", [])).toBe("ship-it");
  });

  it("takes the start of a sentence with no spaces, and moves past a name taken", () => {
    expect(suggestedSavedPromptName("帮我审查这次改动里所有的安全问题并列出文件", [])).toBe("帮我审查这次改动里所有的");
    expect(suggestedSavedPromptName("ship it", [prompt("sp1", "Ship-it"), prompt("sp2", "ship-it-2")])).toBe("ship-it-3");
    expect(suggestedSavedPromptName("!!! ???", [])).toBe("prompt");
  });
});

describe("the list the page follows", () => {
  it("is read once started, and again on hello and on another device's change", async () => {
    hub.prompts.set("assistant", [prompt("sp1", "review")]);
    startSavedPrompts();
    startSavedPrompts(); // once a page
    await settle();
    expect(hub.calls.filter((c) => c.method === "GET")).toHaveLength(1);
    expect(rows().map((r) => r.word)).toEqual(["/review"]);
    hub.prompts.set("assistant", [prompt("sp1", "review"), prompt("sp2", "notes")]);
    emit("assistant_saved_prompts");
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/review", "/notes"]);
    emit("hello");
    await settle();
    expect(hub.calls.filter((c) => c.method === "GET")).toHaveLength(3);
  });

  it("is the current assistant's: another one picked reads its own, and shows no rows until it is in", async () => {
    hub.prompts.set("assistant", [prompt("sp1", "review")]);
    hub.prompts.set("m-abc123", [prompt("sp9", "standup")]);
    startSavedPrompts();
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/review"]);
    chooseManager("m-abc123");
    expect(rows()).toEqual([]);
    await settle();
    expect(hub.calls.at(-1)).toMatchObject({ method: "GET", manager: "m-abc123" });
    expect(rows().map((r) => r.word)).toEqual(["/standup"]);
  });

  it("has no rows on an assistant from before saved prompts, and keeps the last list when a read fails", async () => {
    hub.missing = true;
    startSavedPrompts();
    await settle();
    expect(rows()).toEqual([]);
    hub.missing = false;
    hub.prompts.set("assistant", [prompt("sp1", "review")]);
    emit("hello");
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/review"]);
    hub.failNext = new Error("offline");
    emit("hello");
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/review"]);
  });

  it("shows a change at once: added, changed in place, moved, deleted", async () => {
    startSavedPrompts();
    await settle();
    const a = await saveSavedPrompt(null, { name: " /review ", text: "  Review it.  " });
    expect(hub.calls.find((c) => c.method === "POST")?.json).toEqual({ name: "review", text: "Review it." });
    expect(rows().map((r) => r.word)).toEqual(["/review"]);
    const b = await saveSavedPrompt(null, { name: "notes", text: "Notes." });
    await saveSavedPrompt(a.id, { name: "Review", text: "Review it again." });
    await settle();
    expect(rows().map((r) => `${r.word}:${r.text}`)).toEqual(["/Review:Review it again.", "/notes:Notes."]);
    await moveSavedPrompt(b.id, -1);
    expect(rows().map((r) => r.word)).toEqual(["/notes", "/Review"]);
    expect(hub.calls.find((c) => c.path.endsWith("/order"))?.json).toEqual({ ids: [b.id, a.id] });
    await moveSavedPrompt(b.id, -1); // already first: nothing asked
    expect(hub.calls.filter((c) => c.path.endsWith("/order"))).toHaveLength(1);
    await deleteSavedPrompt(a.id);
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/notes"]);
  });

  it("reads the list again when the hub refuses an order made from an old one", async () => {
    hub.prompts.set("assistant", [prompt("sp1", "a"), prompt("sp2", "b")]);
    startSavedPrompts();
    await settle();
    const gets = hub.calls.filter((c) => c.method === "GET").length;
    hub.failNext = new Error("changed on another device");
    await expect(moveSavedPrompt("sp2", -1)).rejects.toThrow("changed on another device");
    await settle();
    expect(hub.calls.filter((c) => c.method === "GET")).toHaveLength(gets + 1);
  });
});


describe("pending requests belong to their original assistant and sign-in", () => {
  const methods = ["create", "edit", "delete", "order"] as const;
  it.each(methods.flatMap((method) => ["switch", "switch-back", "sign-in"].map((change) => ({ method, change }))))(
    "ignores a late $method after $change without reloading the new list",
    async ({ method, change }) => {
      const old = [prompt("sp1", "old-one"), prompt("sp2", "old-two")];
      const fresh = [prompt("sp1", "fresh-one"), prompt("sp2", "fresh-two")];
      hub.prompts.set("assistant", old);
      hub.prompts.set("m-other", fresh);
      startSavedPrompts();
      await settle();
      let finish!: (value: unknown) => void;
      vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
      const pending = method === "create" ? saveSavedPrompt(null, { name: "old-new", text: "Old owner's text" })
        : method === "edit" ? saveSavedPrompt("sp1", { name: "old-edited", text: "Old owner's edit" })
        : method === "delete" ? deleteSavedPrompt("sp1") : moveSavedPrompt("sp2", -1);
      if (change === "sign-in") {
        hub.prompts.set("assistant", fresh);
        setPairedToken("synthetic-replacement-token");
      } else {
        chooseManager("m-other");
        if (change === "switch-back") {
          hub.prompts.set("assistant", fresh);
          chooseManager(null);
        }
      }
      await settle();
      // Read the replacement even on the old implementation, which did not subscribe to sign-in changes.
      if (change === "sign-in") { emit("hello"); await settle(); }
      expect(rows().map((r) => r.word)).toEqual(["/fresh-one", "/fresh-two"]);
      const calls = hub.calls.length;
      finish(method === "order" ? { prompts: [...old].reverse() } : { prompt: prompt("sp1", "old-result") });
      await pending;
      await settle();
      expect(rows().map((r) => r.word)).toEqual(["/fresh-one", "/fresh-two"]);
      expect(hub.calls).toHaveLength(calls);
    },
  );

  it("does not reload the replacement owner after an old order refusal", async () => {
    hub.prompts.set("assistant", [prompt("sp1", "old-one"), prompt("sp2", "old-two")]);
    hub.prompts.set("m-other", [prompt("sp1", "fresh")]);
    startSavedPrompts(); await settle();
    let fail!: (reason: Error) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((_resolve, reject) => { fail = reject; }));
    const pending = moveSavedPrompt("sp2", -1).catch((e: Error) => e);
    chooseManager("m-other"); await settle();
    const calls = hub.calls.length;
    fail(new Error("old order refused"));
    expect((await pending as Error).message).toBe("old order refused");
    await settle();
    expect(hub.calls).toHaveLength(calls);
    expect(rows().map((r) => r.word)).toEqual(["/fresh"]);
  });

  it("clears the old credential's rows and fences its pending read on same-token pairing", async () => {
    hub.prompts.set("assistant", [prompt("sp1", "old")]);
    startSavedPrompts(); await settle();
    let finish!: (value: unknown) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    emit("hello");
    hub.prompts.set("assistant", [prompt("sp2", "replacement")]);
    setPairedToken("");
    expect(rows()).toEqual([]);
    await settle();
    expect(rows().map((r) => r.word)).toEqual(["/replacement"]);
    finish({ prompts: [prompt("sp3", "late-old")] }); await settle();
    expect(rows().map((r) => r.word)).toEqual(["/replacement"]);
  });

  it("retires a mutation when the roster replaces the default assistant", async () => {
    settleRosterDefault(); // this is a known original default, not its initial discovery
    startSavedPrompts(); await settle();
    let finish!: (value: unknown) => void;
    vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    const pending = saveSavedPrompt(null, { name: "old", text: "Old" });
    setRosterDefault("m-replacement"); await settle();
    const calls = hub.calls.length;
    finish({ prompt: prompt("sp1", "old") }); await pending; await settle();
    expect(rows()).toEqual([]);
    expect(hub.calls).toHaveLength(calls);
  });
});


it("does not let a retired assistant's real 401 sign out the current assistant", async () => {
  setToken("synthetic-current-token");
  chooseManager("m-origin");
  hub.prompts.set("m-other", [prompt("sp9", "current")]);
  startSavedPrompts(); await settle();
  const real = await vi.importActual<typeof import("./api")>("./api");
  let finish!: (response: Response) => void;
  const fetcher = vi.fn((_url: RequestInfo | URL, _init?: RequestInit) => new Promise<Response>((resolve) => { finish = resolve; }));
  const unauthorized = vi.fn();
  vi.stubGlobal("fetch", fetcher);
  handleUnauthorized(unauthorized);
  try {
    vi.mocked(api).mockImplementationOnce(real.api);
    const pending = saveSavedPrompt(null, { name: "old", text: "Old assistant" }).catch((e: Error) => e);
    chooseManager("m-other"); await settle();
    const calls = hub.calls.length;
    finish(new Response(JSON.stringify({ error: "old assistant refused", code: "unauthorized" }),
      { status: 401, headers: { "Content-Type": "application/json" } }));
    expect((await pending as Error).message).toBe("old assistant refused");
    expect(unauthorized).not.toHaveBeenCalled();
    expect(rows().map((r) => r.word)).toEqual(["/current"]);
    expect(hub.calls).toHaveLength(calls);
    const headers = new Headers(fetcher.mock.calls[0][1]?.headers);
    expect(headers.get("Authorization")).toBe("Bearer synthetic-current-token");
    expect(headers.get("X-Persona-Manager")).toBe("m-origin");
  } finally {
    handleUnauthorized(() => {});
    vi.unstubAllGlobals();
  }
});


it("keeps a chosen assistant's pending save when only the roster default changes", async () => {
  chooseManager("m-chosen");
  hub.prompts.set("m-chosen", [prompt("sp1", "before")]);
  startSavedPrompts(); await settle();
  let finish!: (value: unknown) => void;
  vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const pending = saveSavedPrompt(null, { name: "after", text: "Chosen assistant" });
  setRosterDefault("m-new-default");
  await settle(); // the roster-triggered GET finishes before the save
  expect(rows().map((r) => r.word)).toEqual(["/before"]);
  hub.prompts.set("m-chosen", [prompt("sp1", "before"), prompt("sp2", "after")]);
  finish({ prompt: prompt("sp2", "after") });
  await pending; await settle();
  expect(rows().map((r) => r.word)).toEqual(["/before", "/after"]);
});

it.each(["cold", "known", "old hub"])("preserves omitted-header default negotiation on a %s roster", async (roster) => {
  const defaultManager = roster === "old hub" ? "assistant" : "m-real-default";
  if (roster === "known") setRosterDefault(defaultManager);
  if (roster === "old hub") settleRosterDefault();
  const real = await vi.importActual<typeof import("./api")>("./api");
  const fetcher = vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
    // The hub uses its actual default only when the request does not name a manager.
    const manager = new Headers(init?.headers).get("X-Persona-Manager") || defaultManager;
    return Promise.resolve(new Response(JSON.stringify({ prompts: [prompt("sp1", manager)] }),
      { status: 200, headers: { "Content-Type": "application/json" } }));
  });
  vi.stubGlobal("fetch", fetcher);
  try {
    vi.mocked(api).mockImplementationOnce(real.api);
    startSavedPrompts(); await settle();
    expect(fetcher).toHaveBeenCalledTimes(1);
    expect(rows().map((r) => r.word)).toEqual([`/${defaultManager}`]);
    expect(new Headers(fetcher.mock.calls[0][1]?.headers).has("X-Persona-Manager")).toBe(false);
  } finally { vi.unstubAllGlobals(); }
});


it.each([
  { method: "create", defaultManager: "m-real-default" },
  { method: "edit", defaultManager: "m-real-default" },
  { method: "delete", defaultManager: "m-real-default" },
  { method: "order", defaultManager: "m-real-default" },
  { method: "create", defaultManager: "assistant" },
])("canonically refreshes a pending cold $method after discovering default $defaultManager", async ({ method, defaultManager }) => {
  const real = await vi.importActual<typeof import("./api")>("./api");
  const mock = vi.mocked(api), original = mock.getMockImplementation()!;
  let serverRows = [prompt("sp1", "before"), prompt("sp2", "second")];
  let finish!: (response: Response) => void;
  const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  const fetcher = vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
    expect(new Headers(init?.headers).has("X-Persona-Manager")).toBe(false);
    return (init?.method ?? "GET") === "GET" ? Promise.resolve(json({ prompts: serverRows }))
      : new Promise<Response>((resolve) => { finish = resolve; });
  });
  vi.stubGlobal("fetch", fetcher);
  mock.mockImplementation(real.api);
  try {
    startSavedPrompts(); await settle();
    const pending = method === "create" ? saveSavedPrompt(null, { name: "after", text: "Saved" })
      : method === "edit" ? saveSavedPrompt("sp1", { name: "after", text: "Edited" })
      : method === "delete" ? deleteSavedPrompt("sp1") : moveSavedPrompt("sp2", -1);
    setRosterDefault(defaultManager);
    await settle(); // discovery read completes before the write; it cannot include that write yet
    expect(rows().map((row) => row.word)).toEqual(["/before", "/second"]);
    const reads = fetcher.mock.calls.filter(([, init]) => (init?.method ?? "GET") === "GET").length;
    serverRows = method === "create" ? [...serverRows, prompt("sp3", "after")]
      : method === "edit" ? [prompt("sp1", "after"), serverRows[1]]
      : method === "delete" ? [serverRows[1]] : [...serverRows].reverse();
    // The original default was not identified at admission: only a new canonical GET may publish its result.
    finish(json(method === "order" ? { prompts: [prompt("sp9", "unproven")] }
      : { prompt: prompt(method === "create" ? "sp3" : "sp1", "unproven") }));
    await pending; await settle();
    expect(rows().map((row) => row.word)).toEqual(serverRows.map((row) => `/${row.name}`));
    expect(hub.published.some((list) => list.includes("/unproven"))).toBe(false);
    expect(fetcher.mock.calls.filter(([, init]) => (init?.method ?? "GET") === "GET")).toHaveLength(reads + 1);
    expect(fetcher.mock.calls.filter(([, init]) => (init?.method ?? "GET") !== "GET")).toHaveLength(1);
  } finally {
    mock.mockImplementation(original);
    vi.unstubAllGlobals();
  }
});


it("retires cold recovery after a discovered default switches away and back", async () => {
  startSavedPrompts(); await settle();
  let finish!: (value: unknown) => void;
  vi.mocked(api).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
  const pending = saveSavedPrompt(null, { name: "old", text: "Old admission" });
  setRosterDefault("m-discovered"); await settle();
  hub.prompts.set("assistant", [prompt("sp1", "newer")]);
  setRosterDefault("m-other-default");
  setRosterDefault("m-discovered"); await settle();
  const calls = hub.calls.length;
  finish({ prompt: prompt("sp2", "unproven") }); await pending; await settle();
  expect(rows().map((row) => row.word)).toEqual(["/newer"]);
  expect(hub.calls).toHaveLength(calls);
});
