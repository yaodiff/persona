// @vitest-environment jsdom
// The four dimensions' rules and requests (lib/execution.ts): the main chat's model + effort choices, the "Advanced /
// route" narrowing, what a channel write sends (never the secrets it did not type), last-good, and the blocked store.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../i18n";
import {
  anyBlocked, channelLabel, channelOptions, choosePick, cleanSpec, conversationLastGood, deleteExecutionChannel, effortOptions,
  executionCatalog, modelOptions, narrowSpec, pickEfforts, pickModels, pickOfSpec, recommendedPicks, reportBlocked,
  restoreConversationLastGood, rollbackExecutionChannels, saveExecutionChannel, validateExecution, writableChannel,
  type CombinationVerdict, type ExecutionCatalog,
} from "./execution";

const verdict = (spec: CombinationVerdict["spec"], extra: Partial<CombinationVerdict> = {}): CombinationVerdict => ({
  spec, level: "allowed", tier: "native", recommended: true, launcher: `${spec.runtime}-${spec.channel}`, ...extra,
});

const sampleCatalog: ExecutionCatalog = {
  runtimes: [
    { runtime: "claude", protocols: ["anthropic_messages"], efforts: ["low", "medium", "high"] },
    { runtime: "codex", protocols: ["openai_responses"], efforts: ["minimal", "low", "high", "xhigh"] },
  ],
  channels: [
    { name: "native:claude", kind: "native", runtimes: ["claude"] },
    {
      name: "gw", display_name: "Team gateway", kind: "managed", runtimes: ["claude", "codex"], endpoints: ["anthropic_messages", "openai_responses"],
      models: [
        { canonical: "m1", name: "Model One", efforts: ["low", "high"] },
        { canonical: "m2", efforts: [] },
      ],
      aliases: ["gw-claude", "gw-codex"],
    },
    { name: "other", kind: "managed", runtimes: ["codex"], models: [{ canonical: "m3" }] },
  ],
  machines: [{ machine: "mac", name: "Mac", online: true, known: true, runtimes: ["claude", "codex"], channels: ["native:claude", "gw"] }],
  aliases: [
    { launcher: "claude", spec: { runtime: "claude", channel: "native:claude" } },
    { launcher: "gw-codex", spec: { runtime: "codex", channel: "gw", model: "m1" } },
  ],
  combinations: [
    verdict({ runtime: "claude", channel: "native:claude", model: "opus", effort: "high" }),
    verdict({ runtime: "claude", channel: "gw", model: "opus", effort: "high" }), // same model + effort: once
    verdict({ runtime: "codex", channel: "gw", model: "m1", effort: "high" }),
    verdict({ runtime: "codex", channel: "gw", model: "m1", effort: "low" }),
    verdict({ runtime: "claude", channel: "gw", model: "m2", effort: "xhigh" }, { level: "notice", tier: "bridged_experimental", recommended: false }),
    verdict({ runtime: "codex", channel: "other", model: "m3", effort: "high" }, { level: "blocked", recommended: false }),
    verdict({ runtime: "codex", channel: "gw", model: "m4" }, { recommended: false }),
  ],
};

type Call = { url: string; method: string; body?: unknown };
let calls: Call[];
let answer: (call: Call) => Response;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

beforeEach(async () => {
  await initI18n("en");
  calls = [];
  answer = () => json({ ok: true });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const call = { url: String(input), method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    return answer(call);
  }));
});
afterEach(() => vi.unstubAllGlobals());

describe("the main chat's model + effort", () => {
  it("offers the recommended, unblocked combinations once per model and effort, never an experimental bridge", () => {
    const picks = recommendedPicks(sampleCatalog);
    expect(picks.map((p) => `${p.model}/${p.effort}`)).toEqual(["opus/high", "m1/high", "m1/low"]);
    expect(picks[0].launcher).toBe("claude-native:claude");
    expect(pickModels(picks)).toEqual(["opus", "m1"]);
    expect(pickEfforts(picks, "m1")).toEqual(["high", "low"]);
    expect(choosePick(picks, "m1", "low")?.spec).toEqual({ runtime: "codex", channel: "gw", model: "m1", effort: "low" });
    // an effort the model is not offered at: the model's first combination
    expect(choosePick(picks, "m1", "max")?.effort).toBe("high");
    expect(recommendedPicks(null)).toEqual([]);
    expect(recommendedPicks({ ...sampleCatalog, combinations: undefined })).toEqual([]);
  });
});

describe("Advanced / route", () => {
  it("narrows channels to the computer's that run the engine, models to the channel's, efforts to the model's", () => {
    expect(channelOptions(sampleCatalog, "mac", "codex").map((c) => c.name)).toEqual(["gw"]);
    expect(channelOptions(sampleCatalog, "mac", "claude").map((c) => c.name)).toEqual(["native:claude", "gw"]);
    // a computer the hub has not seen: every channel that runs the engine
    expect(channelOptions(sampleCatalog, "unknown", "codex").map((c) => c.name)).toEqual(["gw", "other"]);
    expect(modelOptions(sampleCatalog, "gw").map((m) => m.canonical)).toEqual(["m1", "m2"]);
    expect(effortOptions(sampleCatalog, "gw", "m1", "codex")).toEqual(["low", "high"]);
    // a model without its own efforts: the engine's
    expect(effortOptions(sampleCatalog, "gw", "m2", "codex")).toEqual(["minimal", "low", "high", "xhigh"]);
  });

  it("clears what no longer fits when an earlier dimension changes", () => {
    const now = { runtime: "claude", channel: "native:claude", model: "opus", effort: "medium" };
    expect(narrowSpec(sampleCatalog, "mac", now, { runtime: "codex" })).toEqual({ runtime: "codex", channel: "", model: "opus", effort: "" });
    expect(narrowSpec(sampleCatalog, "mac", { runtime: "codex", channel: "gw", model: "m3", effort: "high" }, { channel: "gw" })).toEqual({ runtime: "codex", channel: "gw", model: "", effort: "high" });
    expect(narrowSpec(sampleCatalog, "mac", { runtime: "codex", channel: "gw", model: "m1", effort: "xhigh" }, { model: "m1" }).effort).toBe("");
  });

  it("names channels in words and makes a pick of a spec, the hub's alias first", () => {
    expect(channelLabel(sampleCatalog, "gw")).toBe("Team gateway");
    expect(channelLabel(sampleCatalog, "native:claude")).toBe("Claude Code sign-in");
    expect(channelLabel(sampleCatalog, "shell:glm")).toBe("Computer launcher glm");
    expect(pickOfSpec({ runtime: "codex", channel: "gw", model: "m1", effort: " " }, "gw-codex", "claude")).toEqual({ launcher: "gw-codex", runtime: "codex", channel: "gw", model: "m1" });
    expect(pickOfSpec({ model: "opus" }, undefined, "claude")).toEqual({ launcher: "claude", model: "opus" });
    expect(cleanSpec({ runtime: " ", channel: "gw" })).toEqual({ channel: "gw" });
  });
});

describe("requests", () => {
  it("reads the catalog for a computer and checks one combination", async () => {
    answer = () => json(sampleCatalog);
    await executionCatalog("mac");
    expect(calls[0]).toMatchObject({ method: "GET" });
    expect(calls[0].url).toMatch(/\/api\/v1\/execution\/catalog\?machine=mac$/);
    answer = () => json({ spec: {}, level: "allowed", recommended: true });
    await validateExecution({ runtime: "codex", channel: " ", model: "m1" }, "mac");
    expect(calls[1]).toMatchObject({ method: "POST", body: { spec: { runtime: "codex", model: "m1" }, machine: "mac" } });
    expect(calls[1].url).toMatch(/\/api\/v1\/execution\/validate$/);
  });

  it("writes a channel with its revision and never the secrets it did not type nor the read-only fields", async () => {
    const stored = { name: "gw", base_url: "https://gw.example", endpoints: ["openai_responses"], bindings: [{ runtime: "codex", alias: "gw-codex" }], has_key: true, has_headers: true, rev: "r1", updated: "2026-11-27T00:00:00Z" };
    expect(writableChannel({ ...stored, api_key: "", headers: " " })).toEqual({ name: "gw", base_url: "https://gw.example", endpoints: ["openai_responses"], bindings: [{ runtime: "codex", alias: "gw-codex" }] });
    await saveExecutionChannel({ ...stored, api_key: " sk-test " }, 3);
    expect(calls[0]).toMatchObject({ method: "PUT", body: { name: "gw", api_key: "sk-test", revision: 3 } });
    expect(calls[0].url).toMatch(/\/execution\/channels\/gw$/);
    expect(calls[0].body).not.toHaveProperty("has_key");
    expect(calls[0].body).not.toHaveProperty("headers");
    await deleteExecutionChannel("gw", 4);
    expect(calls[1].method).toBe("DELETE");
    expect(calls[1].url).toMatch(/\/execution\/channels\/gw\?revision=4$/);
    await rollbackExecutionChannels(2);
    await rollbackExecutionChannels();
    expect(calls.slice(2).map((c) => c.body)).toEqual([{ revision: 2 }, {}]);
    expect(calls[2].url).toMatch(/\/execution\/channels\/rollback$/);
  });

  it("reads and restores a chat's last working setup, null where the hub keeps none", async () => {
    const lastGood = { spec: { runtime: "claude", model: "opus" }, launcher: "claude", machine: "mac", at: "2026-11-27T00:00:00Z" };
    answer = () => json({ last_good: lastGood });
    await expect(conversationLastGood({ session_id: "c1" })).resolves.toEqual(lastGood);
    expect(calls[0].url).toMatch(/\/assistant\/conversations\/c1\/model\/last-good$/);
    answer = () => json({ error: "none" }, 404);
    await expect(conversationLastGood({ session_id: "c1" })).resolves.toBeNull();
    await expect(restoreConversationLastGood({ session_id: "c1" })).resolves.toBeNull();
    answer = () => json({ ok: true, model_pick: { launcher: "claude", model: "opus" }, last_good: lastGood });
    await expect(restoreConversationLastGood({ session_id: "c1" })).resolves.toMatchObject({ model_pick: { launcher: "claude", model: "opus" } });
    expect(calls.at(-1)).toMatchObject({ method: "POST" });
    expect(calls.at(-1)!.url).toMatch(/\/model\/restore$/);
  });
});

describe("a form that holds a blocked combination", () => {
  it("is blocked while any of its parts is", () => {
    reportBlocked("form", "a", true);
    reportBlocked("form", "b", true);
    reportBlocked("form", "a", false);
    expect(anyBlocked("form")).toBe(true);
    reportBlocked("form", "b", false);
    expect(anyBlocked("form")).toBe(false);
    expect(anyBlocked("other")).toBe(false);
  });
});
