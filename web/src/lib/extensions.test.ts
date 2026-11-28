import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "./api";
import { EXTENSIONS_CHANGED } from "./extension-prompts";
import {
  checkError, clearedSecrets, clearExtensionSecret, compact, configError, fieldTitle, isDirty, listItems, listExtensions, listSummary, numberProblems, placeholder, prefillDraft, extensionDetail,
  machineStatus, extensionState, extensionTone, readNumber, rebaseDraft, runNote, same, secretState, setupSecret, setAt, setExtensionConfig, setExtensionConfigPreservingEntries, setExtensionEnabled, setExtensionSecret, sourceText,
  stateLine, valueAt, morePermissions, extensionUpgrades, setExtensionAutoUpgrade, setExtensionUpgrades, upgradeAction, upgradeDetail, upgradeNote,
  type HubExtension, type ExtensionField, type ExtensionUpgrade, type JsonObject, nodeProgramStatus, exitText, saveFooter, channelOf, sandboxState,
  entryID, entryItems, entryKey, fieldPath, isEntryList, extensionTypeText, permissionTitle, permissionDetail,
} from "./extensions";

const extension = (status: string, extra: Partial<HubExtension> = {}): HubExtension => ({ id: `p-${status}`, name: status, kind: "connectors", status, ...extra });

// The hub's features as the extension routes read them (lib/extension-api.ts): a hub with the new routes, unless a test
// says otherwise.
const hub = vi.hoisted(() => ({ features: ["extensions"] as string[] }));
vi.mock("./store", async (importOriginal) => {
  const m = await importOriginal<typeof import("./store")>();
  return { ...m, getState: () => ({ ...m.getState(), features: hub.features }) };
});

// The places refresh a state-changing call asks for (lib/extension-slots.ts), counted instead of fetched.
const slots = vi.hoisted(() => ({ refresh: vi.fn() }));
vi.mock("./extension-slots", async (importOriginal) => {
  const m = await importOriginal<typeof import("./extension-slots")>();
  return { ...m, refreshExtensionPlaces: slots.refresh };
});

afterEach(() => {
  vi.unstubAllGlobals();
  slots.refresh.mockReset();
  hub.features = ["extensions"];
});

describe("扩展的文字", () => {
  it("says each state, its tone, and the list's summary", () => {
    // Running or idle until something needs it: both are just on to the user.
    expect(["running", "stopped", "disabled", "starting", "needs_setup"].map((s) => extensionState(extension(s)))).toEqual([
      "启动", "启动", "停用", "正在启动", "需要设置",
    ]);
    expect(extensionState(extension("weird"))).toBe("状态未知");
    expect(["running", "needs_setup", "error", "incompatible", "disabled", "stopped"].map((s) => extensionTone(extension(s)))).toEqual([
      "green", "orange", "red", "red", "idle", "green",
    ]);
    expect(["running", "stopped", "disabled", "error"].map((s) => runNote(extension(s)))).toEqual(["正在运行", "当前未运行，将在需要时自动启动", "", ""]);
    expect(extensionTone(null)).toBe("idle");
    expect(listSummary(null)).toBe("");
    expect(listSummary([])).toBe("无");
    expect(listSummary([extension("running"), extension("needs_setup"), extension("error"), extension("disabled")])).toBe("4 个 · 2 个待确认");
    expect(listSummary([extension("running")])).toBe("1 个");
  });
  it("says the node program's aggregate, not the idle hub process", () => {
    const summary: HubExtension["node_status"] = { total: 2, running: 2, starting: 0, stopped: 0, offline: 0, abnormal: 0 };
    const running = extension("running", { node_status: summary });
    expect(extensionState(running)).toBe("在 2 台机器上运行");
    expect(runNote(running)).toBe(""); // the node line already says where it runs

    const mixed = extension("running", { node_status: { total: 2, running: 1, starting: 0, stopped: 0, offline: 0, abnormal: 1 } });
    expect(extensionState(mixed)).toBe("在 1 台机器上运行 · 1 台机器异常");

    const abnormalOnly = extension("error", { node_status: { total: 2, running: 0, starting: 0, stopped: 0, offline: 1, abnormal: 1 } });
    expect(extensionState(abnormalOnly)).toBe("1 台机器异常 · 1 台机器离线");

    const starting = extension("starting", { node_status: { total: 1, running: 0, starting: 1, stopped: 0, offline: 0, abnormal: 0 } });
    expect(extensionState(starting)).toBe("正在 1 台机器上启动");

    const allStopped = extension("stopped", { node_status: { total: 2, running: 0, starting: 0, stopped: 2, offline: 0, abnormal: 0 } });
    expect(extensionState(allStopped)).toBe("2 台机器已停止");

    const allOffline = extension("stopped", { node_status: { total: 1, running: 0, starting: 0, stopped: 0, offline: 1, abnormal: 0 } });
    expect(extensionState(allOffline)).toBe("1 台机器离线");

    // A stopped machine and an offline one: the stopped headline plus the offline count, never "Stopped on 0".
    const stoppedAndOffline = extension("stopped", { node_status: { total: 2, running: 0, starting: 0, stopped: 1, offline: 1, abnormal: 0 } });
    expect(extensionState(stoppedAndOffline)).toBe("1 台机器已停止 · 1 台机器离线");

    // Running machines and one away: the running headline plus the offline count.
    const runningAndOffline = extension("running", { node_status: { total: 3, running: 2, starting: 0, stopped: 0, offline: 1, abnormal: 0 } });
    expect(extensionState(runningAndOffline)).toBe("在 2 台机器上运行 · 1 台机器离线");

    // No node summary: the hub process's own status wording stands.
    expect(extensionState(extension("stopped"))).toBe("启动");
  });
  it("puts the state, version and source on one line", () => {
    expect(stateLine(extension("running", { version: "1.2.0", source: "installed" }))).toBe("启动 · 1.2.0 · 已安装");
    expect(stateLine(extension("running", { source: "hub.json" }))).toBe("启动 · manager.json");
    expect(sourceText("other")).toBeUndefined();
  });
  it("names extension kinds and permissions in words, without hub keys or socket filenames", () => {
    expect(extensionTypeText("connectors")).toBe("连接器");
    expect(extensionTypeText("agent_sources")).toBe("Agent 来源");
    expect(extensionTypeText("a_new_point")).toBe("其他扩展");
    expect(extensionTypeText()).toBeUndefined();
    expect(permissionTitle({ key: "network", title: "访问网络" })).toBe("可访问的外部网站");
    expect(permissionTitle({ key: "agent_runtimes", title: "接入节点" })).toBe("在 Agents 中显示其管理的 Agent");
    expect(permissionTitle({ key: "secrets", title: "使用密钥" })).toBe("使用密钥");
    const runtime = permissionDetail({ key: "agent_runtimes", detail: "only runtime.sock" });
    expect(runtime).toContain("本地 Agent 服务");
    expect(runtime).not.toContain("runtime.sock");
    expect(permissionDetail({ key: "network", detail: "notes.example.com" })).toBe("notes.example.com");
  });
  it("says whether an attached extension is online, since when, and the version its program says", () => {
    const at = new Date();
    at.setHours(10, 42, 0, 0);
    const since = at.toISOString();
    const attached = (status: string, a: HubExtension["attached"]) => extension(status, { version: "1.0.0", attached: a });
    expect(runNote(attached("running", { online: true, since }))).toBe("独立运行 · 10:42 起在线");
    // within the hub's 30 seconds it is still starting, with no error of its own: the note says why
    expect(runNote(attached("starting", { online: false, since, error: "连不上" }))).toBe("独立运行 · 正在重连，10:42 起离线：连不上");
    expect(runNote(attached("starting", { online: false, since }))).toBe("独立运行 · 正在重连，10:42 起离线");
    // past them it is an error, which says why itself
    expect(runNote(attached("error", { online: false, since, error: "连不上" }))).toBe("独立运行 · 10:42 起离线");
    expect(runNote(attached("stopped", { online: false }))).toBe("独立运行，需要时自动连接");
    expect(runNote(attached("disabled", { online: false }))).toBe("");
    expect(["running", "starting", "error"].map((s) => extensionTone(attached(s, { online: s === "running", since })))).toEqual(["green", "idle", "red"]);
    expect(stateLine(attached("running", { online: true, since }))).toBe("启动 · 1.0.0");
    expect(stateLine(attached("error", { online: false, since, version: "1.1.0" }))).toBe("出错 · 已批准版本为 1.0.0，当前程序版本为 1.1.0");
  });
  it("marks required fields without a default, and shows the default as a placeholder", () => {
    expect(fieldTitle({ name: "url", type: "string", required: true })).toBe("url（必填）");
    expect(fieldTitle({ name: "url", type: "string", title: "地址", required: true, default: "https://x" })).toBe("地址");
    expect(placeholder({ name: "n", type: "integer", default: 30 })).toBe("默认：30");
    expect(placeholder({ name: "m", type: "string", default: "safe", enum: [{ value: "safe", title: "全部核对" }] })).toBe("默认：全部核对");
    expect(placeholder({ name: "s", type: "string" })).toBe("未设置");
    expect(secretState({ name: "k", title: "K", set: false, required: true })).toBe("未设置（必填）");
    expect(secretState({ name: "k", title: "K", set: true, required: true })).toBe("已设置");
  });
  it("shows a set secret's last four characters when the hub gives them, and nothing more without", () => {
    expect(secretState({ name: "k", title: "K", set: true, hint: "7f3a" })).toBe("已设置 · 末 4 位 7f3a");
    expect(secretState({ name: "k", title: "K", set: true, hint: "" })).toBe("已设置");
    expect(secretState({ name: "k", title: "K", set: true })).toBe("已设置");
    // a hint on one that is not set (it should never come) does not make it read as set
    expect(secretState({ name: "k", title: "K", set: false, required: true, hint: "7f3a" })).toBe("未设置（必填）");
  });
  // Same cases as ExtensionsModelTests' theStillNeedsLineOpensTheSecretItNames on the iPhone (#8853).
  it("opens from Still needs the first unset secret that is required or that the line names", () => {
    const key = { name: "key", title: "API Key", set: false };
    const token = { name: "token", title: "Token", set: false, required: true };
    // an optional secret the line names (the hub's own words for the extension)
    expect(setupSecret(["火山引擎的 API Key"], [key])).toBe(key);
    // a required one, first among those not set
    expect(setupSecret(["密钥「Token」"], [{ ...key, set: true }, token])?.name).toBe("token");
    expect(setupSecret(["密钥「API Key」"], [token, key])?.name).toBe("token");
    // nothing it names is a secret here: plain text
    expect(setupSecret(["依赖「Example」还没准备好"], [key])).toBeUndefined();
    expect(setupSecret(["密钥「API Key」"], [{ ...key, set: true }])).toBeUndefined();
    // nothing needed: no target, whatever is unset
    expect(setupSecret([], [token])).toBeUndefined();
    expect(setupSecret(undefined, [token])).toBeUndefined();
    expect(setupSecret(["密钥「API Key」"], undefined)).toBeUndefined();
  });
  it("reads a save the extension turned down in the hub's words, and nothing else", () => {
    const words = "网关不认这把 Key（401）：invalid api key";
    expect(checkError(new ApiError(400, words, { code: "check_failed", error: words }))).toBe(words);
    expect(checkError(new ApiError(429, "检查太频繁了", { code: "check_rate_limited" }))).toBe("检查太频繁了");
    expect(checkError(new ApiError(409, "设置刚被改过", { code: "settings_moved" }))).toBe("设置刚被改过");
    expect(checkError(new ApiError(400, "bad", { code: "invalid_config" }))).toBeNull();
    expect(checkError(new Error("check_failed"))).toBeNull();
  });
});

describe("a proposed settings prefill", () => {
  it("fills only form fields, keeps groups nested and never enters ordinary entry lists", () => {
    const fields: ExtensionField[] = [
      { name: "machine", type: "string" },
      { name: "repos", type: "array", items: { name: "items", type: "string" } },
      { name: "limits", type: "object", fields: [{ name: "max", type: "integer" }, { name: "label", type: "string" }] },
      { name: "accounts", type: "array", secret: { name: "entry_key", title: "Key" }, items: { name: "items", type: "object", fields: [{ name: "name", type: "string" }] } },
    ];
    expect(prefillDraft(
      { machine: "old", limits: { label: "Saved" }, accounts: [{ _id: "one", name: "One" }] },
      { machine: "new", repos: ["~/code"], limits: { max: 4, unknown: "no" }, accounts: [{ name: "Other" }], secret: "no", unknown: "no" },
      fields,
    )).toEqual({ machine: "new", repos: ["~/code"], limits: { label: "Saved", max: 4 }, accounts: [{ _id: "one", name: "One" }] });
  });
});

describe("草稿", () => {
  it("reads and sets values by path, and drops a group left empty", () => {
    const d = setAt(setAt({}, ["proxy", "host"], "h.example.com"), ["proxy", "port"], 8080);
    expect(d).toEqual({ proxy: { host: "h.example.com", port: 8080 } });
    expect(valueAt(d, ["proxy", "port"])).toBe(8080);
    expect(valueAt(d, ["proxy", "host", "x"])).toBeUndefined();
    expect(valueAt({ a: null }, ["a"])).toBeUndefined();
    const less = setAt(setAt(d, ["proxy", "host"], undefined), ["proxy", "port"], undefined);
    expect(less).toEqual({});
    expect(d).toEqual({ proxy: { host: "h.example.com", port: 8080 } }); // not mutated
  });
  it("compacts what the user cleared, and compares what would be sent", () => {
    expect(compact({ a: "", b: [], c: { d: "" }, e: false, f: 0, g: ["x"], h: null })).toEqual({ e: false, f: 0, g: ["x"] });
    const config = { folders: ["工作"], mode: "safe" };
    expect(isDirty({ mode: "safe", folders: ["工作"] }, config)).toBe(false);
    expect(isDirty({ mode: "safe", folders: ["工作"], base_url: "" }, config)).toBe(false);
    expect(isDirty({ mode: "fast", folders: ["工作"] }, config)).toBe(true);
    expect(isDirty({}, undefined)).toBe(false);
    expect(same([1, { a: 2 }], [1, { a: 2 }])).toBe(true);
    expect(same([1], { 0: 1 })).toBe(false);
  });
  it("preserves only required entry lists as explicit empty arrays", () => {
    const required = new Set(["accounts"]);
    expect(compact({ accounts: [], tags: [] }, required)).toEqual({ accounts: [] });
    const base: JsonObject = { accounts: [{ _id: "entry_a", name: "One" }], include_shared: false };
    const draft: JsonObject = { accounts: [], include_shared: false };
    expect(isDirty(draft, base, required)).toBe(true);
    expect(rebaseDraft({ base, draft, typed: {} }, { accounts: base.accounts, include_shared: true }, required).draft).toEqual({ accounts: [], include_shared: true });
  });
  it("puts only the edits back on a newer configuration, field by field inside a group", () => {
    const base = { mode: "safe", proxy: { host: "a.example.com", port: 80 }, every: 30, tags: ["x"] };
    const draft = { mode: "safe", proxy: { host: "b.example.com", port: 80 }, every: 30, tags: [] };
    const typed = { every: { text: "30", integer: true }, limit: { text: "1a", integer: true } };
    const fresh = { mode: "fast", proxy: { host: "a.example.com", port: 8080 }, every: 120, tags: ["x", "y"] };
    // host and the cleared tags are the user's; mode, port and every (typed, but as it was) follow the hub; "1a" is still being typed
    expect(rebaseDraft({ base, draft, typed }, fresh)).toEqual({
      draft: { mode: "fast", proxy: { host: "b.example.com", port: 8080 }, every: 120 },
      typed: { limit: { text: "1a", integer: true } },
    });
    expect(rebaseDraft({ base, draft: base, typed: {} }, fresh).draft).toBe(fresh);
  });
  it("keeps an unsaved entry's browser key across immutable edits without sending it", () => {
    const entry = {};
    const key = entryKey(entry);
    const edited = setAt({ accounts: [entry] }, ["accounts", "0", "name"], "One");
    const saved = entryItems(edited, ["accounts"])[0];
    expect(entryKey(saved)).toBe(key);
    expect(entryID(saved)).toBeUndefined();
    expect(Object.keys(saved)).toEqual(["name"]);
    expect(JSON.stringify(edited)).toBe('{"accounts":[{"name":"One"}]}');
    expect(fieldPath(["accounts", "0", "address"])).toBe("accounts[0].address");
    expect(isEntryList({ name: "accounts", type: "array", secret: { name: "key", title: "Key" }, items: { name: "", type: "object", fields: [{ name: "name", type: "string" }] } })).toBe(true);
    expect(isEntryList({ name: "tags", type: "array", items: { name: "", type: "string" } })).toBe(false);
  });
  it("keeps entry arrays atomic when unsaved edits meet a fresh configuration", () => {
    const base: JsonObject = { accounts: [{ _id: "entry_a", name: "One" }] };
    const draft: JsonObject = { accounts: [{ _id: "entry_a", name: "One" }, { name: "Two" }] };
    const fresh: JsonObject = { accounts: [{ _id: "entry_a", name: "Changed elsewhere" }, { _id: "entry_c", name: "Three" }] };
    expect(rebaseDraft({ base, draft, typed: {} }, fresh).draft).toEqual(draft);
  });
  it("reads numbers as typed", () => {
    expect(readNumber(" 30 ", true)).toEqual({ value: 30 });
    expect(readNumber("", true)).toEqual({});
    expect(readNumber("1.5", true)).toEqual({ error: "必须为整数" });
    expect(readNumber("1.5", false)).toEqual({ value: 1.5 });
    expect(readNumber("abc", false)).toEqual({ error: "必须为数字" });
    expect(numberProblems({ every: { text: "x", integer: true }, port: { text: "80", integer: true } })).toEqual({ every: "必须为数字" });
  });
  it("lists a list field's items, falling back to the default", () => {
    const f: ExtensionField = { name: "tags", type: "array", default: ["a"] };
    expect(listItems({}, ["tags"], f)).toEqual(["a"]);
    expect(listItems({ tags: ["b", 3] }, ["tags"], f)).toEqual(["b"]);
  });
});

describe("扩展的接口", () => {
  it("calls the hub's reserved paths", async () => {
    const calls: { url: string; method: string; body?: string }[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, method: init.method ?? "GET", body: init.body as string | undefined });
      return new Response(JSON.stringify(url.endsWith("/extensions") ? { extensions: [extension("running")] } : { id: "a b", name: "x", kind: "k", status: "running" }));
    });
    expect((await listExtensions()).map((p) => p.status)).toEqual(["running"]);
    await extensionDetail("a b");
    await setExtensionEnabled("a b", false);
    await setExtensionConfig("a b", { x: 1 });
    await setExtensionConfigPreservingEntries("a b", { x: 2 });
    await setExtensionSecret("a b", "api_key", "v");
    await clearExtensionSecret("a b", "api_key");
    expect(calls.map((c) => `${c.method} ${c.url} ${c.body ?? ""}`.trim())).toEqual([
      "GET /api/v1/extensions",
      "GET /api/v1/extensions/a%20b/-/?entry_lists=1",
      'PUT /api/v1/extensions/a%20b/-/enabled?entry_lists=1 {"enabled":false}',
      'PUT /api/v1/extensions/a%20b/-/config?entry_lists=1 {"config":{"x":1}}',
      'PUT /api/v1/extensions/a%20b/-/config?entry_lists=1&preserve_entries=1 {"config":{"x":2}}',
      'PUT /api/v1/extensions/a%20b/-/secrets/api_key?entry_lists=1 {"value":"v"}',
      "DELETE /api/v1/extensions/a%20b/-/secrets/api_key?entry_lists=1",
    ]);
  });
  it("calls a hub from before #1844 on its old routes, and reads its old keys", async () => {
    hub.features = ["plugins", "plugin_manager"];
    const calls: string[] = [];
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push(`${init.method ?? "GET"} ${url}`);
      if (url.endsWith("/plugins")) return new Response(JSON.stringify({ plugins: [extension("running")], can_install: true }));
      return new Response(JSON.stringify({ id: "a", name: "x", kind: "k", status: "running", ui: [{ plugin: "a", plugin_name: "A", id: "usage", title: "用量", placement: "plugin_page" }] }));
    });
    expect((await listExtensions()).map((p) => p.status)).toEqual(["running"]);
    const d = await extensionDetail("a");
    expect(d.ui).toEqual([{ extension: "a", extension_name: "A", id: "usage", title: "用量", placement: "plugin_page" }]);
    expect(calls).toEqual(["GET /api/v1/plugins", "GET /api/v1/plugins/a/-/?entry_lists=1"]);
  });
  it("refreshes the extension's places after a secret is set or cleared, not after a refused one", async () => {
    // A secret change restarts the extension (registry SetSecret / DeleteSecret), so its sidebar entries, cards and
    // quick prompts can appear or go away.
    const events = new EventTarget();
    let fired = 0;
    events.addEventListener(EXTENSIONS_CHANGED, () => fired++);
    vi.stubGlobal("window", events);
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    let fail = false;
    vi.stubGlobal("fetch", async () =>
      fail
        ? new Response(JSON.stringify({ code: "invalid_secret", message: "bad" }), { status: 400 })
        : new Response(JSON.stringify({ id: "a", name: "x", kind: "k", status: "running" })));
    await setExtensionSecret("a", "api_key", "v");
    expect([slots.refresh.mock.calls.length, fired]).toEqual([1, 1]);
    await clearExtensionSecret("a", "api_key");
    expect([slots.refresh.mock.calls.length, fired]).toEqual([2, 2]);
    fail = true;
    await expect(setExtensionSecret("a", "api_key", "v")).rejects.toBeInstanceOf(ApiError);
    await expect(clearExtensionSecret("a", "api_key")).rejects.toBeInstanceOf(ApiError);
    expect([slots.refresh.mock.calls.length, fired]).toEqual([2, 2]);
  });
  it("reads the field the hub refused", () => {
    expect(configError(new ApiError(400, "base_url: 要是一个网址", { code: "invalid_config", field: "base_url", message: "要是一个网址" }))).toEqual({
      field: "base_url", message: "要是一个网址",
    });
    expect(configError(new ApiError(400, "bad", { code: "invalid_config" }))).toEqual({ field: "", message: "bad" });
    expect(configError(new ApiError(500, "x", {}))).toBeNull();
    expect(configError(new Error("x"))).toBeNull();
  });
  it("reads the secrets a refused configuration save cleared anyway", () => {
    expect(clearedSecrets(new ApiError(500, "x", { code: "secret_not_cleared", secrets_cleared: ["api_key", 3, ""] }))).toEqual(["api_key"]);
    expect(clearedSecrets(new ApiError(409, "x", { code: "read_only", secrets_cleared: ["token"] }))).toEqual(["token"]);
    expect(clearedSecrets(new ApiError(409, "x", { code: "read_only" }))).toEqual([]);
    expect(clearedSecrets(new ApiError(500, "x", { secrets_cleared: "api_key" }))).toEqual([]);
    expect(clearedSecrets(new Error("x"))).toEqual([]);
  });
});

describe("an extension's machine switches", () => {
  const mac = { id: "macbook", name: "MacBook", online: true, os: "darwin" };
  it("say online or not, and for a computer extension how Cua Driver is", () => {
    const note = "…";
    expect(machineStatus({ ...mac, online: false, computer: { enabled: true, installed: false, note } }, true)).toBe("离线");
    expect(machineStatus({ ...mac, computer: { enabled: false, installed: false, note } }, true)).toBe("在线 · 未安装 Cua");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: false, note } }, true)).toBe("在线 · Cua 未运行");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: true, screen_recording: "missing", note } }, true)).toBe("在线 · Cua 缺少权限");
    expect(machineStatus({ ...mac, computer: { enabled: true, installed: true, daemon: true, ready: true } }, true)).toBe("在线 · Cua 已就绪");
    expect(machineStatus({ ...mac, computer: { enabled: false } }, true)).toBe("在线");
    expect(machineStatus({ ...mac, computer: { enabled: true, ready: true } }, false)).toBe("在线");
  });
});

describe("auto-upgrade (docs/extensions/extensions.md「Auto-upgrade」)", () => {
  const up = (state: string, extra: Partial<ExtensionUpgrade> = {}): ExtensionUpgrade => ({ id: "p", market: "m1", from: "1.0.0", to: "1.1.0", state, ...extra });

  it("says what happens to a newer version, and what the user does about it", () => {
    const ask = up("needs_permission", { added_permissions: [{ key: "network", title: "访问更多网络", detail: "status.example.com" }] });
    expect(upgradeDetail(ask)).toBe("需新增一项权限：访问更多网络（status.example.com）");
    expect(upgradeAction(ask)).toBe("去确认");
    expect(upgradeDetail(up("waiting_idle"))).toBe("扩展空闲时自动升级");
    expect(upgradeAction(up("waiting_idle"))).toBeNull();
    expect([upgradeDetail(up("off")), upgradeAction(up("off"))]).toEqual(["自动升级已关闭", "升级"]);
    expect(upgradeDetail(up("skipped"))).toBe("上次升级后未能启动，已回退至 1.0.0，将不再自动升级到此版本");
    // A version the user rolled back from by hand (#1826): not "didn't start".
    expect(upgradeDetail(up("skipped", { skipped_by: "user" }))).toBe("你已将该版本回滚至 1.0.0，因此不会再自动升级到此版本");
    expect(upgradeAction(up("skipped", { skipped_by: "user" }))).toBe("升级");
    expect(upgradeDetail(up("failed", { detail: "下载失败" }))).toBe("安装失败：下载失败");
    expect(morePermissions([])).toBe("所需权限已变更");
    expect(morePermissions([{ key: "a", title: "甲" }, { key: "b", title: "乙", detail: "x" }])).toBe("需新增两项权限：甲、乙（x）");
  });

  it("tells of the last upgrade only when it is news of its own", () => {
    const at = "2026-11-04T14:34:21.439310005Z";
    expect(upgradeNote({ kind: "upgraded", from: "0.9.0", to: "1.0.0", at })).toBe("已自动升级至 1.0.0（原版本为 0.9.0）");
    expect(upgradeNote({ kind: "rolled_back", from: "1.0.0", to: "1.1.0", at })).toBe("1.1.0 无法启动，已回退至 1.0.0");
    expect(upgradeNote({ kind: "needs_permission", from: "1.0.0", to: "1.1.0", at })).toBeNull();
  });

  it("puts the switches to the hub; a hub without the route has no global switch", async () => {
    const calls: { url: string; init?: RequestInit }[] = [];
    let has = false;
    vi.stubGlobal("localStorage", { getItem: () => "t", setItem: () => {}, removeItem: () => {} });
    vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      if (url.endsWith("/extensions/-/upgrades")) {
        if (!has) return new Response(JSON.stringify({ error: "no such extension", code: "not_found" }), { status: 404, headers: { "Content-Type": "application/json" } });
        return new Response(JSON.stringify({ auto: init?.method === "PUT" ? JSON.parse(String(init.body)).auto : true, upgrades: [] }), { headers: { "Content-Type": "application/json" } });
      }
      return new Response(JSON.stringify({ id: "p", name: "P", kind: "ui", status: "running", auto_upgrade: false }), { headers: { "Content-Type": "application/json" } });
    }));
    expect(await extensionUpgrades()).toBeNull();
    has = true;
    expect(await extensionUpgrades()).toBe(true);
    expect(await setExtensionUpgrades(false)).toBe(false);
    expect((await setExtensionAutoUpgrade("p", false)).auto_upgrade).toBe(false);
    const last = calls[calls.length - 1];
    expect(last.url).toContain("/extensions/p/-/auto-upgrade");
    expect(last.init?.method).toBe("PUT");
    expect(JSON.parse(String(last.init?.body))).toEqual({ auto: false });
  });
});

describe("saveFooter", () => {
  it("says nothing when every field is a list of machines (they say so themselves)", () => {
    const machines = { name: "computers", type: "array", items: { name: "items", type: "string", format: "machine" } } as ExtensionField;
    expect(saveFooter([machines])).toBeUndefined();
    expect(saveFooter([machines, { name: "url", type: "string" } as ExtensionField])).toContain("立即生效；其余配置修改完成后请点击「保存」");
    expect(saveFooter([{ name: "url", type: "string" } as ExtensionField])).toMatch(/^修改完成后请点击「保存」/);
  });
});

describe("nodeProgramStatus", () => {
  it("says how a plugin's program is on a machine switched on for it, in words the user can act on", () => {
    expect(nodeProgramStatus(undefined)).toBe("等待 hub 下发程序");
    expect(nodeProgramStatus({ machine_id: "m", state: "running", version: "1.2.0" })).toBe("程序运行中 · 1.2.0");
    expect(nodeProgramStatus({ machine_id: "m", state: "error", error: "exit status 3" })).toBe("程序出错：退出码 3");
    expect(exitText("1.0.0 没有起来：exit status 3")).toBe("1.0.0 没有起来：退出码 3");
    expect(exitText("signal: killed")).toBe("被信号终止（killed）");
    expect(nodeProgramStatus({ machine_id: "m", state: "outdated" })).toContain("personad 版本过旧");
    expect(nodeProgramStatus({ machine_id: "m", state: "lease_expired" })).toContain("重新连接后将自动启动");
  });
});

describe("sandbox and channel", () => {
  it("says whether the sandbox is in effect and what went through the egress proxy", () => {
    expect(sandboxState({ enabled: true, active: true })).toBe("沙箱已生效");
    expect(sandboxState({ enabled: true, active: false, why: "no bwrap" })).toBe("已请求启用沙箱，但当前未生效");
    expect(sandboxState({ enabled: false, active: false })).toBe("未启用沙箱");
  });

  it("finds the channel an extension contributes", () => {
    expect(channelOf({ contributes: { channel: ["", "wechat"] } })).toBe("wechat");
    expect(channelOf({ contributes: { connector: ["x"] } })).toBeNull();
    expect(channelOf({})).toBeNull();
  });
});
