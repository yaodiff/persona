// @vitest-environment jsdom
// Settings › Models › Engines and Channels: the engines the hub declares and where they are ready; the managed channels
// listed without their secrets (only whether there are any), written with the catalog's revision and never with a
// secret that was not typed again, a 409 read as "changed elsewhere", deleted with the revision, the last ten points
// to go back to; and a launcher that is a channel's alias marked read-only, its refusal pointing to the channel.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { ApiError } from "../../lib/api";
import type { ChannelCatalogView, ExecutionCatalog } from "../../lib/execution";
import { ChannelAliasNote, ChannelAliasPill, launcherWriteFailed } from "./ChannelAliasNote";
import { ExecutionChannelsCard } from "./ExecutionChannelsCard";
import { RuntimesCard } from "./RuntimesCard";

const toast = vi.hoisted(() => vi.fn());
vi.mock("../../ui/atoms", async (orig) => ({ ...(await orig<typeof import("../../ui/atoms")>()), toast }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const points = Array.from({ length: 12 }, (_, i) => ({ revision: 12 - i, at: "2026-11-27T00:00:00Z", action: "save", channel: "gw", channels: ["gw"] }));
let view: ChannelCatalogView;
type Call = { url: string; method: string; body?: Record<string, unknown> };
let calls: Call[];
let writeStatus: number;
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("en");
  toast.mockClear();
  calls = [];
  writeStatus = 200;
  view = {
    revision: 7,
    channels: [
      { name: "gw", display_name: "Team gateway", base_url: "https://gw.example", endpoints: ["anthropic_messages"], bindings: [{ runtime: "claude", alias: "gw-claude" }], has_key: true, has_headers: true, share: "all" },
      { name: "bare", base_url: "https://bare.example", endpoints: ["openai_responses"], bindings: [{ runtime: "codex", alias: "bare-codex" }], share: "none" },
    ],
    rollbacks: points,
  };
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const call: Call = { url, method: init?.method ?? "GET", body: init?.body ? JSON.parse(String(init.body)) : undefined };
    calls.push(call);
    if (call.method === "GET" && url.endsWith("/execution/channels")) return json(view);
    if (writeStatus !== 200) return json({ error: "revision changed" }, writeStatus);
    return json({ ok: true, revision: 8 });
  }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  vi.unstubAllGlobals();
});

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const testId = <T extends Element = HTMLElement>(id: string) => document.querySelector<T>(`[data-testid="${id}"]`);
const click = async (el: HTMLElement | null) => {
  await act(async () => el!.click());
  await flush();
};
async function type(id: string, value: string) {
  await act(async () => {
    const el = testId<HTMLInputElement | HTMLTextAreaElement>(id)!;
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await flush();
}
const writes = () => calls.filter((c) => c.method !== "GET");
async function card() {
  await act(async () => root.render(<ExecutionChannelsCard />));
  await flush();
}

describe("Channels", () => {
  it("lists the channels with only whether a key and headers are kept, and ten points to go back to", async () => {
    await card();
    const gw = document.querySelector('[data-channel="gw"]')!;
    expect(gw.textContent).toContain("Team gateway");
    expect(gw.textContent).toContain(t("execution.key_saved"));
    expect(gw.textContent).toContain(t("execution.headers_saved"));
    expect(gw.textContent).toContain("Claude Code · gw-claude");
    expect(document.querySelector('[data-channel="bare"]')!.textContent).toContain(t("execution.key_none"));
    expect(document.querySelectorAll("[data-rollback]")).toHaveLength(10);
    expect(document.body.textContent).toContain(t("execution.channels_footer"));
  });

  it("saves with the catalog's revision, keeping the secrets it was not given again", async () => {
    await card();
    await click(testId("channel-edit-gw"));
    expect(document.body.textContent).toContain(t("execution.channels_live_note"));
    const key = testId<HTMLInputElement>("channel-key")!;
    expect(key.type).toBe("password");
    expect(key.value).toBe("");
    expect(key.placeholder).toBe(t("execution.secret_kept"));
    expect(testId<HTMLInputElement>("channel-name")!.disabled).toBe(true);
    await type("channel-base-url", "https://gw2.example");
    await click(testId("channel-save"));
    const [put] = writes();
    expect(put.method).toBe("PUT");
    expect(put.url).toMatch(/\/api\/v1\/execution\/channels\/gw$/);
    expect(put.body).toMatchObject({ name: "gw", base_url: "https://gw2.example", revision: 7 });
    for (const k of ["api_key", "headers", "has_key", "has_headers"]) expect(put.body).not.toHaveProperty(k);
    expect(toast).toHaveBeenCalledWith(t("execution.channel_saved", { channel: "gw" }), "ok");
    expect(document.querySelector("[data-channel-editor]")).toBeNull();
  });

  it("sends a typed key once, and checks a new channel before writing", async () => {
    await card();
    await click(testId("channel-add"));
    await click(testId("channel-save"));
    expect(document.querySelector('[data-channel-editor] [role="alert"]')?.textContent).toBe(t("execution.err_name"));
    expect(writes()).toEqual([]);
    await type("channel-name", "new-gw");
    await type("channel-base-url", "https://new.example");
    await type("binding-alias-0", "new-claude");
    await type("channel-key", "sk-example");
    await click(testId("channel-save"));
    expect(writes()[0].body).toMatchObject({ name: "new-gw", api_key: "sk-example", endpoints: ["anthropic_messages"], bindings: [{ runtime: "claude", alias: "new-claude" }], revision: 7 });
  });

  it("says another change came first (409) and reads the catalog again", async () => {
    await card();
    const reads = calls.length;
    await click(testId("channel-edit-bare"));
    writeStatus = 409;
    await click(testId("channel-save"));
    expect(document.querySelector('[data-channel-editor] [role="alert"]')?.textContent).toBe(t("execution.conflict"));
    expect(testId<HTMLButtonElement>("channel-save")!.disabled).toBe(true);
    expect(calls.slice(reads).filter((c) => c.method === "GET")).toHaveLength(1);
  });

  it("deletes with the revision after a confirm, and goes back to a point or undoes the last change", async () => {
    await card();
    await click(testId("channel-delete-bare"));
    expect(writes()).toEqual([]);
    await click(testId("channel-delete-confirm-bare"));
    expect(writes()[0].method).toBe("DELETE");
    expect(writes()[0].url).toMatch(/\/execution\/channels\/bare\?revision=7$/);
    await click(testId("channel-rollback-11"));
    await click(testId("channel-undo"));
    expect(writes().slice(1).map((c) => [c.url.replace(/^.*\/api\/v1/, ""), c.body])).toEqual([
      ["/execution/channels/rollback", { revision: 11 }],
      ["/execution/channels/rollback", {}],
    ]);
    expect(toast).toHaveBeenCalledWith(t("execution.rolled_back"), "ok");
    writeStatus = 409;
    await click(testId("channel-undo"));
    expect(toast).toHaveBeenCalledWith(t("execution.conflict"), "error");
  });

  it("is not there on a hub without channels", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json({ error: "not found" }, 404)));
    await card();
    expect(host.textContent).toBe("");
  });
});

describe("a launcher that is a channel's alias", () => {
  it("is marked read-only and opens the channel's editor", async () => {
    const onLeave = vi.fn();
    await act(async () => root.render(
      <>
        <ChannelAliasPill channel="gw" />
        <ChannelAliasNote channel="gw" onLeave={onLeave} />
        <ExecutionChannelsCard />
      </>,
    ));
    await flush();
    expect(document.querySelector('[data-channel-alias="gw"]')?.textContent).toBe(t("execution.alias_readonly", { channel: "gw" }));
    expect(document.querySelector('[role="note"]')?.textContent).toContain(t("execution.alias_readonly_detail", { channel: "gw" }));
    await click(testId("alias-edit-in-channels"));
    expect(onLeave).toHaveBeenCalled();
    expect(testId<HTMLInputElement>("channel-name")?.value).toBe("gw");
  });

  it("answers the hub's refusal with the way to the channel", () => {
    launcherWriteFailed(new ApiError(400, "alias is read-only", { error: "alias is read-only", code: "err_alias_read_only" }), "gw");
    expect(toast).toHaveBeenLastCalledWith(expect.any(String), "error", { label: t("execution.alias_edit_in_channels"), run: expect.any(Function) });
    launcherWriteFailed(new Error("boom"));
    expect(toast).toHaveBeenLastCalledWith(expect.any(String), "error");
  });
});

describe("Engines", () => {
  it("lists each engine, what it speaks, and where it is ready", async () => {
    const catalog: ExecutionCatalog = {
      runtimes: [{ runtime: "codex", protocols: ["openai_responses"], efforts: ["low", "high"] }, { runtime: "pi", protocols: [] }],
      channels: [],
      aliases: [],
      machines: [
        { machine: "m1", name: "Mac", online: true, known: true, runtimes: ["codex"] },
        { machine: "m2", name: "Linux box", online: false, known: true, runtimes: ["codex"] },
      ],
    };
    await act(async () => root.render(<RuntimesCard catalog={catalog} />));
    const codex = document.querySelector('[data-runtime="codex"]')!;
    expect(codex.textContent).toContain("Codex");
    expect(codex.textContent).toContain("OpenAI Responses");
    expect(codex.textContent).toContain(t("execution.runtime_ready", { online: 1, total: 2 }));
    expect(codex.textContent).toContain(t("execution.machine_offline", { machine: "Linux box" }));
    expect(document.querySelector('[data-runtime="pi"]')!.textContent).toContain(t("execution.runtime_nowhere"));
    await act(async () => root.render(<RuntimesCard catalog={null} />));
    expect(host.textContent).toBe("");
  });
});
