import { afterEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import {
  modelPurpose, modelProviderTitle, modelSummary, providerName, runtimeFallbackText, runtimeLabel,
  type ModelProvider, type ModelPurpose,
} from "./model-config";
import {
  agentsOverride, catalogLists, chooseProvider, claudeCodeOnly, configDirChoices, draftFor, effortOffered, executionForm, followedCategory, modelConfiguration, modelPatch, overridden,
  ownRecord, parentCategory, permissionModesForProvider, providersFor, purposeDescription, purposeGroups, resetPatch, runtimeChoices, saveModelPurpose, setApartFrom, withPurposeAfter,
  withUsesAfterCategories,
} from "./model-config-execution";

const purpose: ModelPurpose = {
  id: "lark.agent", name: "飞书 /agent", group: "对话", kind: "launcher", revision: 3,
  override: { provider: "strong", model: "main", effort: "high" },
  effective: { provider: "strong", model: "main", effort: "high" }, fields: ["provider", "model", "effort"],
};
const providers: ModelProvider[] = [
  { id: "strong", name: "常用启动器", kind: "launcher", hidden: true },
  { id: "other", name: "其他启动器", kind: "launcher" },
  { id: "hidden", name: "隐藏启动器", kind: "launcher", hidden: true },
  { id: "api", name: "直接 API", kind: "api" },
];

afterEach(() => vi.unstubAllGlobals());

describe("用途配置", () => {
  it("displays the built-in TRAE CLI provider without changing its saved ID or custom names", () => {
    for (const launcher of ["traex", "traecli", "trae-cli"]) {
      const provider = { id: `launcher.${launcher}`, name: launcher, kind: "launcher" as const };
      expect(modelProviderTitle(provider)).toBe("TRAE CLI");
      expect(providerName(provider.id)).toBe("TRAE CLI");
      expect(providerName(provider.id, [{ ...provider, name: "Team relay" }])).toBe("Team relay");
      expect(runtimeLabel("cli", launcher)).toBe("TRAE CLI");
    }
    expect(modelProviderTitle({ id: "api.traecli", name: "traecli" })).toBe("traecli");
  });

  it("uses overrides for editing, never silently fixes a missing provider", () => {
    expect(draftFor(purpose)).toEqual({ provider: "strong", model: "main", effort: "high", runtime: "" });
    expect(draftFor({ ...purpose, override: { provider: "gone" } })).toEqual({ provider: "gone", model: "", effort: "", runtime: "" });
    expect(draftFor({ ...purpose, override: {} })).toEqual({ provider: "", model: "", effort: "", runtime: "" });
  });
  it("clears model and effort when the provider changes", () => {
    const draft = draftFor(purpose);
    expect(chooseProvider(draft, "api")).toEqual({ provider: "api", model: "", effort: "", runtime: "" });
    expect(chooseProvider(draft, "strong")).toBe(draft);
  });
  it("sends revision and explicit null to restore supported fields", () => {
    expect(modelPatch(purpose, { provider: " other ", model: "", effort: " low ", runtime: "" })).toEqual({ revision: 3, provider: "other", model: null, effort: "low" });
    expect(modelPatch({ ...purpose, fields: ["model"] }, { provider: "ignored", model: "", effort: "high", runtime: "" })).toEqual({ revision: 3, model: null });
    expect(modelPatch({ ...purpose, fields: [] }, draftFor(purpose))).toEqual({ revision: 3 });
  });
  it("offers the main chat's profile for new conversations only when the hub does, for Claude Code (#4633)", () => {
    const main: ModelPurpose = { ...purpose, id: "assistant", override: {}, effective: { provider: "launcher.claude" }, config_dirs: ["private", "legacy"] };
    expect(configDirChoices(main).map((c) => c.id)).toEqual(["private", "legacy"]);
    expect(configDirChoices(main, "codex")).toEqual([]);
    expect(configDirChoices({ ...main, config_dirs: undefined })).toEqual([]); // an older hub
    expect(draftFor(main).config_dir).toBe("private");
    expect(draftFor({ ...main, override: { config_dir: "legacy" } }).config_dir).toBe("legacy");
    expect("config_dir" in draftFor(purpose)).toBe(false);
    const draft = { ...draftFor(main), config_dir: "legacy" };
    expect(chooseProvider(draft, "other").config_dir).toBe("legacy"); // a provider change keeps it, as the hub does
    expect(modelPatch(main, draft)).toMatchObject({ revision: 3, config_dir: "legacy" });
    expect(modelPatch(purpose, { ...draftFor(purpose), config_dir: "legacy" })).not.toHaveProperty("config_dir");
    expect(resetPatch(main, "", "legacy")).toMatchObject({ revision: 3, config_dir: "legacy" }); // a reset keeps it, as it keeps the runtime
    expect(resetPatch(purpose, "", "legacy")).not.toHaveProperty("config_dir");
  });
  it("handles permission_mode on launcher purposes only and clears it when switching providers", () => {
    const withPerm: ModelPurpose = {
      ...purpose,
      override: { provider: "strong", permission_mode: "acceptEdits" },
      fields: ["provider", "model", "effort", "permission_mode"],
    };
    const draft = draftFor(withPerm);
    expect(draft.permission_mode).toBe("acceptEdits");
    expect(chooseProvider(draft, "other").permission_mode).toBe("");
    expect(modelPatch(withPerm, draft)).toMatchObject({ revision: 3, permission_mode: "acceptEdits" });
    expect(resetPatch(withPerm)).toMatchObject({ revision: 3, permission_mode: null });
    expect(permissionModesForProvider(withPerm, { id: "launcher.claude", name: "Claude", kind: "launcher", agent: "claude" }, "claude").length).toBeGreaterThan(0);
    expect(permissionModesForProvider(withPerm, { id: "launcher.omp", name: "OMP", kind: "launcher", agent: "omp" }, "omp")).toEqual([]);
    expect(permissionModesForProvider({ ...withPerm, kind: "api" }, { id: "api", name: "API", kind: "api" }, "")).toEqual([]);
    const restrictedAgy = permissionModesForProvider(withPerm, {
      id: "launcher.agy", name: "Antigravity", kind: "launcher", agent: "agy",
      permission_modes: ["default", "acceptEdits", "plan", "bypassPermissions"],
    }, "agy");
    expect(restrictedAgy.map((m) => m.id)).toEqual(["default", "acceptEdits", "plan", "bypassPermissions"]);
    expect(restrictedAgy[0].title).toBe(t("lib-session-actions.mode_default"));
  });
  it("uses server capability candidates and keeps a referenced hidden provider", () => {
    expect(providersFor(purpose, providers).map((p) => p.id)).toEqual(["strong", "other"]);
    expect(providersFor({ ...purpose, provider_ids: ["api"] }, providers).map((p) => p.id)).toEqual(["api"]);
    expect(providersFor({ ...purpose, provider_ids: [] }, providers)).toEqual([]);
  });
  it("groups and searches real purposes without generating skills", () => {
    const triage: ModelPurpose = { ...purpose, id: "inbox.triage", name: "分拣", group: "收件箱", kind: "api" };
    expect(purposeGroups([purpose, triage], "分拣")).toEqual([["后台处理 · 直接 API", [triage]]]);
    expect(purposeGroups([purpose, triage], " MAIN ").length).toBe(2);
    expect(purposeGroups([purpose], "技能")).toEqual([]);
    expect(modelSummary(purpose, providers)).toBe("常用启动器 · main · 思考 high");
  });
  it("sets apart only the uses of Agents the hub gives fields of their own", () => {
    for (const id of ["lark.agent", "inbox.agent"]) {
      expect(agentsOverride({ ...purpose, id, parent: "agent.new" })).toBe(true);
      expect(agentsOverride({ ...purpose, id, parent: "agent.new", fields: [] })).toBe(false); // an older hub: read-only
      expect(agentsOverride({ ...purpose, id, parent: "agent.new", fields: undefined })).toBe(false);
    }
    expect(agentsOverride({ ...purpose, id: "agent.new" })).toBe(false); // the category itself
    expect(agentsOverride({ ...purpose, id: "inbox.judge", parent: "inbox.triage" })).toBe(false);
  });
});

describe("执行形态", () => {
  it("orders by kind and launch, with extension API purposes kept under Extensions", () => {
    const chat: ModelPurpose = { ...purpose, id: "assistant", launch: "headless" };
    const lark: ModelPurpose = { ...purpose, parent: "agent.new", launch: "headless" };
    const agents: ModelPurpose = { ...purpose, id: "agent.new", launch: "engine" };
    const api: ModelPurpose = { ...purpose, id: "kb", kind: "api" };
    const extension: ModelPurpose = { ...api, id: "extension.example", parent: "kb" };
    const voice: ModelPurpose = { ...purpose, id: "plugin.voice", kind: "plugin" };
    expect(purposeGroups([extension, api, agents, voice, chat, lark], "")).toEqual([
      ["Persona 对话 · 后台进程", [chat, lark]], ["Agents · 交互式", [agents]],
      ["后台处理 · 直接 API", [api]], ["扩展", [extension, voice]],
    ]);
    expect(purposeGroups([chat, agents], "后台进程")).toEqual([["Persona 对话 · 后台进程", [chat]]]);
    // An older hub's catalog still renders, without trusting its inheritance-group labels.
    expect(purposeGroups([{ ...chat, launch: undefined }], "")[0][0]).toBe("Persona 对话 · 后台进程");
    expect(purposeGroups([{ ...lark, launch: undefined }], "")[0][0]).toBe("Persona 对话 · 后台进程");
  });

  it("keeps form fixed while the provider, runtime, protocol and machine change its backend", () => {
    const chat: ModelPurpose = { ...purpose, id: "assistant", launch: "headless" };
    expect(executionForm(chat, providers)).toEqual({ label: "后台进程", backend: "claude -p" });
    expect(executionForm({ ...chat, effective: { provider: "launcher.codex", runtime: "agentsdk" } }, providers).backend).toBe("codex app-server");
    expect(executionForm({ ...chat, effective: { runtime: "agentsdk" } }, providers).backend).toBe("Agent SDK");
    const agent: ModelPurpose = { ...chat, id: "agent.new", launch: "engine" };
    expect(executionForm(agent, providers)).toEqual({ label: "交互式", backend: "按机器 · claude" });
    const machine = { id: "box", name: "Computer", online: true, engine_default: "orca" };
    expect(executionForm({ ...agent, machine_id: "box" }, providers, [machine]).backend).toBe("orca · claude");
    expect(executionForm(agent, providers, [machine]).backend).toBe("按机器 · claude");
    for (const [protocol, backend] of [["messages", "Messages"], ["responses", "Responses"], ["chat", "Chat"]] as const) {
      const direct: ModelPurpose = { ...chat, id: "kb", kind: "api", launch: undefined, effective: { provider: "direct" } };
      expect(executionForm(direct, [{ id: "direct", name: "Direct", kind: "api", api: protocol }]))
        .toEqual({ label: "直接 API", backend });
    }
    // A launcher picked for an API purpose runs its CLI (claude -p, codex exec, agy -p), unless it is a managed one
    // speaking Messages (#5823, #12027).
    const kb: ModelPurpose = { ...chat, id: "kb", kind: "api", launch: undefined, effective: { provider: "launcher.x" } };
    expect(executionForm(kb, [{ id: "launcher.x", name: "Claude Code", kind: "launcher", agent: "claude" }]).backend).toBe("claude -p");
    expect(executionForm(kb, [{ id: "launcher.x", name: "Codex", kind: "launcher", agent: "codex" }]).backend).toBe("codex exec");
    expect(executionForm(kb, [{ id: "launcher.x", name: "Codex Managed", kind: "launcher", agent: "codex", api: "responses", has_key: true }]).backend).toBe("codex exec");
    expect(executionForm({ ...kb, effective: { provider: "launcher.codex" } }, [{ id: "launcher.codex", name: "Codex", kind: "launcher" }]).backend).toBe("codex exec");
    expect(executionForm(kb, [{ id: "launcher.x", name: "Antigravity", kind: "launcher", agent: "agy" }]).backend).toBe("agy -p");
    expect(executionForm({ ...kb, effective: { provider: "launcher.agy" } }, [{ id: "launcher.agy", name: "Antigravity", kind: "launcher" }]).backend).toBe("agy -p");
    expect(executionForm(kb, [{ id: "launcher.x", name: "Gateway", kind: "launcher", has_key: true }]).backend).toBe("Messages");
    expect(executionForm(kb, [{ id: "launcher.x", name: "Other", kind: "launcher", api: "chat", has_key: true }]).backend).toBe("claude -p");
  });
});

describe("主对话的运行方式", () => {
  const assistant: ModelPurpose = {
    ...purpose, id: "assistant", name: "主对话", override: { provider: "strong" }, effective: { provider: "strong" }, runtimes: ["cli", "agentsdk"],
    fields: ["provider", "model", "effort", "runtime"],
  };
  it("offers only the runtimes the hub lists, and names non-Claude CLI runtimes accurately", () => {
    expect(runtimeChoices(assistant).map((r) => r.label)).toEqual(["Claude Code CLI", "Agent SDK"]);
    expect(runtimeChoices(assistant, "codex")).toEqual([{ id: "cli", label: "Codex CLI" }]);
    expect(runtimeChoices(assistant, "agy")).toEqual([{ id: "cli", label: "Antigravity ACP" }]);
    expect(runtimeChoices(assistant, "omp")).toEqual([{ id: "cli", label: "omp CLI" }]);
    expect(runtimeChoices({ ...assistant, runtimes: ["agentsdk", "future"] }).map((r) => r.id)).toEqual(["agentsdk"]);
    expect(runtimeChoices(purpose)).toEqual([]);
  });
  it("edits the stored runtime, CLI when none, and keeps it across a provider change", () => {
    expect(draftFor(assistant).runtime).toBe("cli");
    expect(draftFor({ ...assistant, override: { runtime: "agentsdk" } }).runtime).toBe("agentsdk");
    expect(draftFor(purpose).runtime).toBe("");
    expect(chooseProvider(draftFor({ ...assistant, override: { runtime: "agentsdk" } }), "other").runtime).toBe("agentsdk");
  });
  it("saves the runtime only for a purpose that has one", () => {
    expect(modelPatch(assistant, { provider: "strong", model: "", effort: "", runtime: "agentsdk" }))
      .toEqual({ revision: 3, provider: "strong", model: null, effort: null, runtime: "agentsdk" });
    expect(modelPatch(purpose, { provider: "strong", model: "", effort: "", runtime: "agentsdk" })).not.toHaveProperty("runtime");
  });
  it("says Agent SDK in the summary, nothing for the CLI", () => {
    expect(modelSummary({ ...assistant, effective: { provider: "strong", runtime: "agentsdk" } }, providers)).toBe("常用启动器 · Agent SDK");
    expect(modelSummary({ ...assistant, effective: { provider: "strong", runtime: "cli" } }, providers)).toBe("常用启动器");
  });
});

describe("统一 API 契约", () => {
  it("loads catalog/detail and saves a single purpose, without exposing keys", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "mock" });
    const fetcher = vi.fn().mockImplementation(async (_url: string, init: RequestInit) => ({
      ok: true, status: 200,
      text: async () => JSON.stringify(init.method === "PATCH" ? { purpose, providers } : { purpose, purposes: [purpose], providers }),
    }));
    vi.stubGlobal("fetch", fetcher);
    expect((await modelConfiguration()).purposes).toEqual([purpose]);
    expect((await modelPurpose(purpose.id)).purpose).toEqual(purpose);
    expect((await saveModelPurpose(purpose, resetPatch(purpose))).purpose).toEqual(purpose);
    expect(fetcher.mock.calls.map((c) => c[0])).toEqual([
      "/api/v1/model-config", "/api/v1/model-config/purposes/lark.agent", "/api/v1/model-config/purposes/lark.agent",
    ]);
    expect(JSON.parse(fetcher.mock.calls[2][1].body)).toEqual({ revision: 3, provider: null, model: null, effort: null });
  });
  it("returns conflicts instead of retrying and overwriting newer settings", async () => {
    vi.stubGlobal("localStorage", { getItem: () => "mock" });
    const fetcher = vi.fn().mockResolvedValue({ ok: false, status: 409, text: async () => JSON.stringify({ error: "revision conflict" }) });
    vi.stubGlobal("fetch", fetcher);
    const draft = draftFor(purpose);
    await expect(saveModelPurpose(purpose, modelPatch(purpose, draft))).rejects.toMatchObject({ status: 409 });
    expect(draft).toEqual({ provider: "strong", model: "main", effort: "high", runtime: "" });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});

describe("后台督办的用途 (#5082)", () => {
  const main: ModelPurpose = { ...purpose, id: "assistant", name: "主对话", launch: "headless" };
  const agents: ModelPurpose = { ...purpose, id: "agent.new", name: "Agents", launch: "engine" };
  const supervise: ModelPurpose = { ...purpose, id: "assistant.supervise", name: "后台督办", parent: "assistant", category_id: "assistant", launch: "headless", override: {}, inherited_from: "主对话" };

  const ids = (catalog: { purposes: ModelPurpose[] }) => catalog.purposes.map((p) => p.id);

  it("a hub before #5806 answers it by its id: it goes right after Main chat, once, and only when the hub has both", () => {
    const catalog = { purposes: [main, agents], providers };
    expect(catalogLists(catalog, "assistant.supervise")).toBe(false);
    expect(ids(withPurposeAfter(catalog, "assistant.supervise", "assistant", supervise))).toEqual(["assistant", "assistant.supervise", "agent.new"]);
    expect(catalog.purposes).toHaveLength(2); // the hub's answer is left as it was
    expect(withPurposeAfter(catalog, "assistant.supervise", "assistant", undefined)).toBe(catalog);
    const noMain = { purposes: [agents], providers };
    expect(withPurposeAfter(noMain, "assistant.supervise", "assistant", supervise)).toBe(noMain);
    // an answer for another purpose is not put in under this id
    expect(withPurposeAfter(catalog, "assistant.supervise", "assistant", agents)).toBe(catalog);
  });

  it("a hub since #5806 lists it last among its own: it is moved up to right after Main chat, once (#5806)", () => {
    const twin: ModelPurpose = { ...purpose, id: "inbox.agent", name: "AI 分身执行", parent: "agent.new" };
    const listed = { purposes: [main, agents, twin, supervise], providers };
    expect(catalogLists(listed, "assistant.supervise")).toBe(true);
    const placed = withPurposeAfter(listed, "assistant.supervise", "assistant");
    expect(ids(placed)).toEqual(["assistant", "assistant.supervise", "agent.new", "inbox.agent"]);
    expect(placed.purposes[1]).toBe(supervise); // the hub's row, as sent
    expect(ids(listed)).toEqual(["assistant", "agent.new", "inbox.agent", "assistant.supervise"]); // the answer is left as it was
    // the listed row wins over one read apart, and nothing is listed twice
    expect(ids(withPurposeAfter(listed, "assistant.supervise", "assistant", { ...supervise, name: "stale" }))).toEqual(ids(placed));
    expect(withPurposeAfter(listed, "assistant.supervise", "assistant", { ...supervise, name: "stale" }).purposes[1].name).toBe("后台督办");
    // already in place (a hub that lists it after its parent): the answer itself
    expect(withPurposeAfter(placed, "assistant.supervise", "assistant")).toBe(placed);
    // listed before its parent: still right after it
    expect(ids(withPurposeAfter({ purposes: [supervise, main, agents], providers }, "assistant.supervise", "assistant"))).toEqual(["assistant", "assistant.supervise", "agent.new"]);
    // no Main chat row: left where the hub put it
    const noMain = { purposes: [agents, supervise], providers };
    expect(withPurposeAfter(noMain, "assistant.supervise", "assistant")).toBe(noMain);
  });

  it("is in Main chat's group", () => {
    const groups = purposeGroups(withPurposeAfter({ purposes: [main, agents], providers }, "assistant.supervise", "assistant", supervise).purposes, "");
    expect(groups[0][1].map((p) => p.id)).toEqual(["assistant", "assistant.supervise"]);
    // an older hub's shape (no launch): still with Main chat, by its parent
    expect(purposeGroups([main, { ...supervise, launch: undefined }, agents], "")[0][1].map((p) => p.id)).toEqual(["assistant", "assistant.supervise"]);
  });

  it("each purpose says what it is for in the hub's sentence; the background supervisor has this page's on a hub that sends none (#5806)", () => {
    expect(purposeDescription({ ...main, description: "和你对话，把事情拆成任务派出去。" })).toBe("和你对话，把事情拆成任务派出去。");
    expect(purposeDescription({ ...supervise, description: "  hub 的说法  " })).toBe("hub 的说法");
    // a hub before #5086 sends none: nothing for Main chat, this page's own sentence for the background supervisor
    expect(purposeDescription(main)).toBe("");
    expect(purposeDescription({ ...main, description: "   " })).toBe("");
    expect(purposeDescription(supervise)).toContain("跟进并审核");
  });
});

describe("the two meeting uses (#7980)", () => {
  const fields = ["provider", "model", "effort"];
  const category = (id: string, name: string, kind: ModelPurpose["kind"], launch?: ModelPurpose["launch"]): ModelPurpose => ({
    ...purpose, id, name, kind, launch, category_id: id, override: {}, uses: [{ name: "a use" }],
  });
  const main = category("assistant", "主对话", "launcher", "headless");
  const agents = category("agent.new", "Agents", "launcher", "engine");
  const kb = category("kb", "信息处理", "api");
  const triage = category("inbox.triage", "信息筛选", "api");
  const delegate = category("agent.delegate", "提问代答", "api");
  const use = (id: string, parent: ModelPurpose, extra: Partial<ModelPurpose> = {}): ModelPurpose => ({
    ...purpose, id, name: id, kind: parent.kind, launch: parent.launch, parent: parent.id, category_id: parent.id, override: {}, fields, ...extra,
  });
  const twin = use("inbox.agent", agents, { launch: "headless" });
  const lark = use("lark.agent", agents, { launch: "headless" });
  const supervise = use("assistant.supervise", main, { inherited_from: "主对话" });
  const room = use("meeting.room", main, { inherited_from: "主对话", provider_ids: ["strong", "api"] });
  const minutes = use("meeting.minutes", kb, { inherited_from: "信息处理" });
  const extension = use("extension.notes", kb);
  // the hub's order since #7980: the categories, the uses of Agents, the background supervisor, then the two meeting uses
  const hub = { purposes: [main, agents, kb, triage, delegate, twin, lark, supervise, room, minutes, extension], providers };
  const ids = (catalog: { purposes: ModelPurpose[] }) => catalog.purposes.map((p) => p.id);
  const all = hub.purposes;

  it("tells a use set apart from its category by the data, not by its id", () => {
    expect(setApartFrom(room, all)).toBe(main);
    expect(setApartFrom(minutes, all)).toBe(kb);
    expect(setApartFrom(supervise, all)).toBe(main);
    // the same data under another id is the same kind of row
    expect(setApartFrom({ ...minutes, id: "meeting.later" }, all)).toBe(kb);
    // a category, a read-only use, one with uses, one whose parent is no category, one with no such parent
    expect(setApartFrom(main, all)).toBeUndefined();
    expect(setApartFrom({ ...minutes, fields: [] }, all)).toBeUndefined();
    expect(setApartFrom({ ...minutes, fields: undefined }, all)).toBeUndefined();
    expect(setApartFrom({ ...minutes, uses: [{ name: "x" }] }, all)).toBeUndefined();
    expect(setApartFrom({ ...minutes, category_id: "meeting.minutes" }, all)).toBeUndefined();
    expect(setApartFrom(minutes, [main, { ...kb, category_id: "assistant" }])).toBeUndefined();
    expect(setApartFrom(room, [agents, kb])).toBeUndefined();
    // the uses of Agents run in another group than Agents, an extension's purpose is under Extensions: theirs stay as they were
    expect(setApartFrom(twin, all)).toBeUndefined();
    expect(setApartFrom(lark, all)).toBeUndefined();
    expect(setApartFrom(extension, all)).toBeUndefined();
  });

  it("says Follows <the category> for the meeting uses, and leaves the background supervisor its own words", () => {
    expect(followedCategory(room, all)).toBe(main);
    expect(followedCategory(minutes, all)).toBe(kb);
    expect(followedCategory(supervise, all)).toBeUndefined();
    expect(followedCategory(twin, all)).toBeUndefined();
    expect(overridden({})).toBe(false);
    expect(overridden({ effort: "low" })).toBe(true);
    expect(overridden({ runtime: "agentsdk" })).toBe(false);
  });

  it("puts each right after its category, behind the background supervisor for Main chat", () => {
    const placed = withUsesAfterCategories(withPurposeAfter(hub, "assistant.supervise", "assistant"));
    expect(ids(placed)).toEqual([
      "assistant", "assistant.supervise", "meeting.room", "agent.new", "kb", "meeting.minutes", "inbox.triage", "agent.delegate",
      "inbox.agent", "lark.agent", "extension.notes",
    ]);
    expect(placed.purposes[2]).toBe(room); // the hub's rows, as sent
    expect(ids(hub)[8]).toBe("meeting.room"); // the hub's answer is left as it was
    expect(purposeGroups(placed.purposes, "").map(([group, rows]) => [group, rows.map((p) => p.id)])).toEqual([
      ["Persona 对话 · 后台进程", ["assistant", "assistant.supervise", "meeting.room", "inbox.agent", "lark.agent"]],
      ["Agents · 交互式", ["agent.new"]],
      ["后台处理 · 直接 API", ["kb", "meeting.minutes", "inbox.triage", "agent.delegate"]],
      ["扩展", ["extension.notes"]],
    ]);
    // without the background supervisor: right after Main chat; listed before it: still behind it
    expect(ids(withUsesAfterCategories({ purposes: [main, kb, room, minutes], providers }))).toEqual(["assistant", "meeting.room", "kb", "meeting.minutes"]);
    expect(ids(withUsesAfterCategories(withPurposeAfter({ purposes: [main, agents, room, supervise], providers }, "assistant.supervise", "assistant"))))
      .toEqual(["assistant", "assistant.supervise", "meeting.room", "agent.new"]);
    // in place already: the catalog itself
    expect(withUsesAfterCategories(placed)).toBe(placed);
  });

  it("changes nothing against a hub before #7980", () => {
    const older = withPurposeAfter({ purposes: [main, agents, kb, triage, delegate, twin, lark, supervise, extension], providers }, "assistant.supervise", "assistant");
    expect(withUsesAfterCategories(older)).toBe(older);
    const beforeSupervisor = { purposes: [main, agents, kb, twin, lark, extension], providers };
    expect(withUsesAfterCategories(beforeSupervisor)).toBe(beforeSupervisor);
  });

  it("goes back to following with explicit nulls and the revision, even for a field the provider in effect doesn't take", () => {
    expect(resetPatch({ ...minutes, revision: 4, fields: ["provider", "model"] })).toEqual({ revision: 4, provider: null, model: null, effort: null });
    // told by the purpose's own data, whatever group it is listed in: an extension's purpose and the uses of Agents too
    expect(resetPatch({ ...extension, revision: 2, fields: ["provider", "model"] })).toEqual({ revision: 2, provider: null, model: null, effort: null });
    expect(resetPatch({ ...twin, revision: 6 })).toEqual({ revision: 6, provider: null, model: null, effort: null });
    expect([room, minutes, supervise, twin, lark, extension].every(ownRecord)).toBe(true);
    // a category resets the fields it takes, and keeps the runtime the draft has
    expect(ownRecord(kb) || ownRecord({ ...minutes, fields: [] })).toBe(false);
    expect(resetPatch({ ...kb, revision: 9, fields: ["provider", "model"] })).toEqual({ revision: 9, provider: null, model: null });
    expect(resetPatch({ ...main, fields: [...fields, "runtime"], runtimes: ["cli", "agentsdk"] }, "agentsdk")).toEqual({ revision: 3, provider: null, model: null, effort: null, runtime: "agentsdk" });
    // its editor's save sends only what the hub offers, never a runtime
    expect(modelPatch(room, { provider: "strong", model: "", effort: "high", runtime: "agentsdk" })).toEqual({ revision: 3, provider: "strong", model: null, effort: "high" });
    expect(runtimeChoices(room)).toEqual([]);
    expect(providersFor(room, providers).map((p) => p.id)).toEqual(["strong", "api"]);
  });

  it("follows the category's binding from the catalog once its own is cleared", () => {
    expect(parentCategory(room, all)).toBe(main);
    expect(parentCategory(minutes, all)).toBe(kb);
    expect(parentCategory(twin, all)).toBe(agents);
    expect(parentCategory(extension, all)).toBe(kb);
    expect(parentCategory(kb, all)).toBeUndefined(); // a category follows nothing
    expect(parentCategory({ ...minutes, fields: [] }, all)).toBeUndefined(); // a read-only use
    expect(parentCategory(minutes, [main])).toBeUndefined(); // not in the catalog
  });

  it("offers effort by the provider the draft runs on, also where the hub left it out for a Chat Completions one", () => {
    const chat: ModelProvider = { id: "chat", name: "Chat", kind: "api", api: "chat" };
    const messages: ModelProvider = { id: "messages", name: "Messages", kind: "api", api: "messages" };
    const responses: ModelProvider = { id: "responses", name: "Responses", kind: "api", api: "responses" };
    const onChat = { ...minutes, fields: ["provider", "model"] }; // the hub's fields while it runs on a Chat Completions connection
    expect(effortOffered(onChat, chat, chat)).toBe(false);
    expect(effortOffered(onChat, messages, chat)).toBe(true);
    expect(effortOffered(onChat, responses, chat)).toBe(true);
    const currentChat: ModelProvider = { ...chat, chat_effort: true };
    expect(effortOffered(minutes, chat, chat)).toBe(true); // #8779 hub: the effective Chat fields already admit effort
    expect(effortOffered(minutes, currentChat, messages)).toBe(true); // a current hub explicitly marks Chat effort
    expect(effortOffered(onChat, currentChat, undefined)).toBe(true); // the previous effective provider may be gone
    expect(effortOffered(minutes, chat, messages)).toBe(false); // the same DTO on an older hub, after Messages → Chat
    expect(effortOffered(minutes, { ...chat, id: "chat-b" }, chat)).toBe(true); // Chat A's explicit field also covers Chat B
    expect(effortOffered(onChat, responses, undefined)).toBe(false);
    expect(effortOffered(onChat, responses, undefined, false, true)).toBe(true);
    expect(effortOffered(onChat, messages, undefined, false, true)).toBe(true);
    expect(effortOffered(onChat, chat, undefined, false, true)).toBe(false); // an unmarked Chat stays hidden
    expect(effortOffered(minutes, messages, messages)).toBe(true);
    // the hub leaves effort out on a connection that takes one: it takes none here (an older hub), whatever is picked
    expect(effortOffered({ ...minutes, fields: ["provider", "model"] }, messages, messages)).toBe(false);
    // no provider to pick, or no fields sent at all: as the hub says
    expect(effortOffered({ ...minutes, fields: ["model"] }, messages, chat)).toBe(false);
    expect(effortOffered({ ...minutes, fields: undefined }, messages, chat)).toBe(false);
    // a launcher purpose: its fields
    expect(effortOffered(room, undefined, undefined)).toBe(true);
    expect(effortOffered({ ...room, fields: ["provider", "model"] }, undefined, chat)).toBe(false);
    // Save sends effort when the editor offers it, and not otherwise
    const draft = { provider: "messages", model: "", effort: "high", runtime: "" };
    expect(modelPatch(onChat, draft)).toEqual({ revision: 3, provider: "messages", model: null });
    expect(modelPatch(onChat, draft, true)).toEqual({ revision: 3, provider: "messages", model: null, effort: "high" });
  });

  it("only the In-meeting conversation runs on Claude Code only", () => {
    expect(claudeCodeOnly(room)).toBe(true);
    expect(claudeCodeOnly(minutes)).toBe(false);
    expect(claudeCodeOnly(supervise)).toBe(false);
  });
});

describe("退回 CLI", () => {
  it("says why", () => expect(runtimeFallbackText(" 没有 Node.js 22+ ")).toBe("已回退至 CLI：没有 Node.js 22+"));
});
