// @vitest-environment jsdom
// Unit & integration tests for the lazy-loaded unified IntegrationDetail view (integrations_v2, #14847).
import { installedMailbox, preloadMailbox } from "../../test/installed-mailbox";
import { act, useLayoutEffect } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { connectModuleHub } from "../../core/module-host";
import { initI18n, t } from "../../i18n";
import { setPairedToken } from "../../lib/api";
import { ago } from "../../lib/format";
import { channelStateText, type ChannelDetail } from "../../lib/channels";
import type { HubConnector } from "../../lib/connectors";
import { askSignIn, signInAsked } from "../../lib/extension-auth";
import { track, type ExtensionOperation } from "../../lib/extension-operations";
import type { HubExtensionDetail } from "../../lib/extensions";
import type { FeishuBotStatus } from "../../lib/feishu-bot";
import { chooseManager, chosenManager, managerHeaderFor, pinManagerAt, resetManagerChoice } from "../../lib/manager-choice";
import { setRoster, type ManagerList } from "../../lib/managers";
import { applyCompleteSessions, getState, subscribe } from "../../lib/store";
import type { ExtensionTrust } from "../../lib/tracks";
import { FEATURES } from "./FeishuBot";
import { Extensions } from "./Extensions";
import { IntegrationDetail, isIntegrationsV2Active, partitionExtensionFields } from "./IntegrationDetail";
import { Integrations } from "./Integrations";
import { decodeIntegrationsRoute } from "./integrations-route";

const hubEvents = vi.hoisted(() => ({
  listeners: new Map<string, Set<(msg: Record<string, unknown>) => void>>(),
}));

vi.mock("../../lib/store", async (original) => {
  const real = await original<typeof import("../../lib/store")>();
  return {
    ...real,
    onHubEvent: (type: string, listener: (msg: Record<string, unknown>) => void) => {
      if (!hubEvents.listeners.has(type)) hubEvents.listeners.set(type, new Set());
      hubEvents.listeners.get(type)!.add(listener);
      const off = real.onHubEvent(type, listener);
      return () => {
        hubEvents.listeners.get(type)?.delete(listener);
        off();
      };
    },
  };
});

vi.mock("../../ui/atoms", async (original) => ({
  ...(await original<typeof import("../../ui/atoms")>()),
  toast: vi.fn(),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

interface RecordedRequest {
  manager: string | null;
  url: string;
  method: string;
  body: unknown;
}

const ok = (body: unknown) => ({
  ok: true,
  status: 200,
  text: async () => JSON.stringify(body),
  headers: new Headers(),
});
const notFound = () => ({
  ok: false,
  status: 404,
  text: async () => JSON.stringify({ error: "not found" }),
  headers: new Headers(),
});

const ROSTER = {
  default: "assistant",
  managers: [
    { id: "assistant", name: "Default", builtin: true, online: true },
    { id: "work", name: "Work", builtin: false, online: true },
  ],
};

const COMPOSITE_EXTENSION: HubExtensionDetail = {
  id: "larkx",
  name: "Lark Extended",
  kind: "process",
  source: "installed",
  status: "running",
  version: "2.1.0",
  enabled: true,
  origin: "market:official/larkx",
  auto_upgrade: true,
  description: "Composite connector, channel, and inbox extension.",
  contributes: {
    connectors: ["larkx_conn"],
    channel: ["larkx_chat"],
  },
  auth: {
    title: "Workspace OAuth",
    state: "signed_in",
    account: "owner@example.com",
  },
  form: {
    fields: [
      { name: "endpoint", type: "string", format: "uri", title: "API Endpoint", section: "connection" },
      { name: "mention_prefix", type: "string", title: "Mention Prefix", section: "channel" },
      { name: "triage_rules", type: "string", title: "Triage Rules", section: "inbox" },
      { name: "display_alias", type: "string", title: "Display Alias" },
      { name: "debug_trace", type: "boolean", title: "Debug Trace", section: "advanced" },
    ],
  },
  config: {
    endpoint: "https://api.example.com",
    mention_prefix: "@bot",
    triage_rules: "Only urgent items",
    display_alias: "Team Lark",
    debug_trace: false,
  },
  secrets: [
    { name: "app_secret", title: "App Secret", required: true, set: true, hint: "1234" },
  ],
  permissions: [
    { key: "network", title: "Network access", detail: "Connects to workspace APIs" },
  ],
};

const COMPOSITE_CHANNEL: ChannelDetail = {
  id: "larkx_chat",
  name: "Lark Chat",
  kind: "im",
  source: "plugin",
  plugin: "larkx",
  settings_url: "settings/plugins/larkx",
  installed: true,
  enabled: true,
  state: "connected",
  account: { name: "Owner" },
  capabilities: { push: true, threads: true, format: "markdown" },
  bind: "qr",
  target: { kind: "main" },
  thread_mode: "channel_default",
  thread_idle_hours: 0,
  features: [{ id: "asks", title: "Questions & approvals", on: true }],
  waiting: { answers: 0, asks: 0 },
};

const COMPOSITE_CONNECTOR: HubConnector = {
  id: "larkx_conn",
  name: "Lark Workspace",
  status: "ok",
  account: "owner@example.com",
  enabled: true,
  archive: true,
  inbox: true,
  capabilities: ["login", "archive", "inbox"],
};

const FEISHU_BOT_STATUS: FeishuBotStatus = {
  enabled: true,
  connected: true,
  has_secret: true,
  from_config: false,
  app_id: "cli_example_123456",
  domain: "feishu",
  bot_name: "Persona Feishu",
  owner: { name: "Alice", paired_at: 1700000000 },
  features: { main: true, cards: false, report_steps: true, answer_new_card: true, topic_cot: false, dm_cot: false, cot_answer: false, read_group: false, all_cards: false, pushes: true, sync: true, reports: true, board: false, workbench: false, only_conclusions: false, agents: true },
  nudge_after: 15,
};

const FEISHU_CHANNEL: ChannelDetail = {
  id: "feishu",
  name: "Feishu",
  kind: "im",
  source: "builtin",
  installed: true,
  enabled: true,
  state: "connected",
  account: { name: "Alice" },
  capabilities: { push: true, threads: true, asks: "buttons" },
  bind: "fields",
  target: { kind: "main" },
  settings_url: "settings/connectors/feishu-bot",
  target_override: { mode: "topics" },
  features: FEATURES.map(({ key, title }) => ({ id: key, title, on: !!FEISHU_BOT_STATUS.features?.[key] })),
  waiting: { answers: 0, asks: 0 },
};

let host: HTMLDivElement;
let root: Root;
let requests: RecordedRequest[];
let projectionRead: ((request: RecordedRequest) => unknown | Promise<unknown>) | undefined;
let currentExt: HubExtensionDetail;
let currentChannels: Record<string, ChannelDetail>;
let currentConnectors: HubConnector[];
let currentMailAccounts: { id: string; email: string; provider: string; status: string }[];
let currentFeishuBot: FeishuBotStatus;
let currentRepositories: { id: string; provider?: string }[] | "error" | null;
let servedRoster: ManagerList;
let grants: Record<string, ExtensionTrust>;

const flush = async () => {
  for (let i = 0; i < 14; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const SEED_SESSION = { machine_id: "hub", session_id: "seed-1", status: "idle", updated_at: "2026-01-01T00:00:00Z" } as never;

function setStoreAssistant(integrations_v2: boolean | undefined, manager = "assistant") {
  // Ensure the seed session is present so applyCompleteSessions always hits its `also`-only branch.
  applyCompleteSessions([SEED_SESSION]);
  applyCompleteSessions([SEED_SESSION], [], {
    features: ["channels", "connectors", "extensions"],
    assistant: {
      machine_id: "hub",
      session_id: "main-1",
      manager,
      integrations_v2,
      conversations: [],
    } as never,
  });
}

function section(id: string): HTMLElement | null {
  return host.querySelector<HTMLElement>(`[data-integration-section="${id}"]`);
}

function sectionIds(): string[] {
  return [...host.querySelectorAll<HTMLElement>("[data-integration-section]")].map((el) => el.getAttribute("data-integration-section") || "");
}

function setInputValue(input: HTMLInputElement | HTMLTextAreaElement, value: string) {
  const proto = input instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")!.set!;
  setter.call(input, value);
  input.dispatchEvent(new Event("input", { bubbles: true }));
  input.dispatchEvent(new Event("change", { bubbles: true }));
}

beforeEach(() => {
  requests = [];
  projectionRead = undefined;
  currentExt = structuredClone(COMPOSITE_EXTENSION);
  grants = {};
  currentChannels = {
    larkx_chat: structuredClone(COMPOSITE_CHANNEL),
    feishu: structuredClone(FEISHU_CHANNEL),
  };
  currentConnectors = [
    structuredClone(COMPOSITE_CONNECTOR),
    { id: "mail", name: "Mail", status: "ok", enabled: true, inbox: true, capabilities: ["inbox"] },
    {
      id: "github",
      name: "GitHub",
      status: "ok",
      account: "octocat",
      enabled: true,
      archive: true,
      inbox: true,
      capabilities: ["login", "archive", "inbox"],
      options: { ci: "mine", muted_repos: ["org/noisy"], ignore_branches: ["train/*"] },
    },
  ];
  currentMailAccounts = [{ id: "m1", email: "me@example.com", provider: "imap", status: "ok" }];
  currentFeishuBot = structuredClone(FEISHU_BOT_STATUS);
  currentRepositories = [{ id: "repo-1", provider: "github" }, { id: "repo-2", provider: "github" }];

  resetManagerChoice();
  setPairedToken("");
  servedRoster = ROSTER;
  setRoster(servedRoster);
  setStoreAssistant(true);
  connectModuleHub(getState, subscribe);

  vi.stubGlobal("fetch", vi.fn(async (input: string | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method || "GET").toUpperCase();
    let body: unknown = undefined;
    if (typeof init?.body === "string" && init.body) {
      try { body = JSON.parse(init.body); } catch { body = init.body; }
    }
    const manager = new Headers(init?.headers).get("X-Persona-Manager");
    const request = { url, method, body, manager };
    requests.push(request);

    if (url.includes("/assistant/integrations") && projectionRead) {
      const result = await projectionRead(request);
      return result instanceof Response ? result : ok(result);
    }
    if (url.endsWith("/managers")) return ok(servedRoster);
    if (url.endsWith("/assistant/extension-trust")) {
      const grant = grants[manager ?? "assistant"];
      return grant ? ok({ items: [grant] }) : notFound();
    }
    if (url.endsWith(`/assistant/extension-trust/${currentExt.id}`) && method === "PUT") {
      const grant = { ...(body as ExtensionTrust), extension: currentExt.id };
      grants[manager ?? "assistant"] = grant;
      return ok(grant);
    }
    if (url.includes("/mail/accounts")) {
      return ok({ accounts: currentMailAccounts });
    }
    if (url.includes("/bots/feishu")) {
      if (method === "PUT" && body && typeof body === "object") {
        currentFeishuBot = { ...currentFeishuBot, ...(body as Partial<FeishuBotStatus>) };
      }
      return ok(currentFeishuBot);
    }
    if (url.includes("/connectors/github/auth")) {
      return ok({ source: "device", machines: [], missing: [] });
    }
    if (url.includes("/connectors/") && url.endsWith("/settings") && method === "PUT") {
      const m = /\/connectors\/([^/]+)\/settings/.exec(url);
      const cid = m ? decodeURIComponent(m[1]) : "";
      const found = currentConnectors.find((c) => c.id === cid && (c.manager ?? null) === manager);
      if (found && body && typeof body === "object") {
        Object.assign(found, body);
        return ok(found);
      }
      return notFound();
    }
    if (/\/connectors(\?|$)/.test(url)) {
      return ok({ connectors: currentConnectors });
    }
    if (url.includes("/channels/")) {
      const testMatch = /\/channels\/([^/?]+)\/test/.exec(url);
      if (testMatch && method === "POST") {
        return ok({ ok: true, note: "Test delivered" });
      }
      const chMatch = /\/channels\/([^/?]+)/.exec(url);
      const cid = chMatch ? decodeURIComponent(chMatch[1]) : "";
      const ch = currentChannels[cid];
      if (!ch) return notFound();
      if (method === "PUT" && body && typeof body === "object") {
        const patch = body as Partial<ChannelDetail>;
        const mode = patch.thread_mode ?? ch.thread_mode;
        const next: ChannelDetail = {
          ...ch,
          ...patch,
          target_override: mode === "per_group_and_topic"
            ? { mode: "groups_and_topics" }
            : mode === "per_topic" ? { mode: "topics" } : undefined,
        };
        currentChannels[cid] = next;
        return ok(next);
      }
      return ok(ch);
    }
    if (url.includes("/-/upgrades")) {
      return ok({ auto: true });
    }
    if (url.includes("/-/config") && method === "PUT") {
      const cfg = (body as { config?: Record<string, unknown> })?.config ?? {};
      currentExt = { ...currentExt, config: cfg as HubExtensionDetail["config"] };
      return ok(currentExt);
    }
    if (url.includes("/-/enabled") && method === "PUT") {
      const enabled = (body as { enabled?: boolean })?.enabled ?? true;
      currentExt = { ...currentExt, enabled, status: enabled ? "running" : "disabled" };
      return ok(currentExt);
    }
    if (url.includes("/extensions/") || url.includes("/plugins/")) {
      return ok(currentExt);
    }
    if (url.includes("/subscriptions/summary")) {
      return ok({ lines: [] });
    }
    if (url.includes("/assistant/repositories")) {
      if (currentRepositories === "error") return notFound();
      if (currentRepositories === null) return ok({});
      return ok({ manager: "assistant", repositories: currentRepositories });
    }
    if (url.includes("/lark/status")) {
      return ok({ configured: true, connected: true, name: "Team Lark" });
    }
    if (url.includes("/lark/accounts")) {
      return ok({ accounts: [] });
    }
    return ok({});
  }));

  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.documentElement.classList.remove("dark");
  vi.unstubAllGlobals();
  setRoster(null);
  resetManagerChoice();
});

describe("IntegrationDetail pure helpers & lazy import", () => {
  it("can be loaded via dynamic import() with both named and default exports", async () => {
    const mod = await import("./IntegrationDetail");
    expect(mod.IntegrationDetail).toBe(IntegrationDetail);
    expect(mod.default).toBe(IntegrationDetail);
  });

  it("partitions fields by section and preserves legacy/unknown fields in general", () => {
    const parts = partitionExtensionFields([
      { name: "a", type: "string", section: "connection" },
      { name: "b", type: "string" },
      { name: "c", type: "string", section: "channel" },
      { name: "d", type: "string", section: "inbox" },
      { name: "e", type: "boolean", section: "advanced" },
      { name: "f", type: "string", section: "future_section" as never },
    ]);
    expect(parts.connection.map((x) => x.name)).toEqual(["a"]);
    expect(parts.channel.map((x) => x.name)).toEqual(["c"]);
    expect(parts.inbox.map((x) => x.name)).toEqual(["d"]);
    expect(parts.general.map((x) => x.name)).toEqual(["b", "f"]);
    expect(parts.advanced.map((x) => x.name)).toEqual(["e"]);
  });

  it("gates integrations_v2 fail-closed and respects assistant manager scope", () => {
    expect(isIntegrationsV2Active(null)).toBe(false);
    expect(isIntegrationsV2Active({ manager: "assistant", integrations_v2: false })).toBe(false);
    expect(isIntegrationsV2Active({ manager: "assistant" })).toBe(false);
    expect(isIntegrationsV2Active({ manager: "assistant", integrations_v2: true }, "assistant")).toBe(true);
    expect(isIntegrationsV2Active({ manager: "assistant", integrations_v2: true }, "other")).toBe(false);
  });
});

describe("integrations_v2 gating & legacy isolation", () => {
  it("renders fallback and issues zero API requests when integrations_v2 is false", async () => {
    setStoreAssistant(false);
    act(() => {
      root.render(
        <IntegrationDetail
          target={{ kind: "extension", id: "larkx" }}
          fallback={<div data-legacy-fallback>Legacy view</div>}
        />,
      );
    });
    await flush();

    expect(host.querySelector("[data-legacy-fallback]")?.textContent).toBe("Legacy view");
    expect(host.querySelector("[data-integration-detail]")).toBeNull();
    expect(requests).toHaveLength(0);
  });

  it("renders nothing by default when integrations_v2 is omitted (older hub)", async () => {
    setStoreAssistant(undefined);
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "builtin", id: "mail" }} />);
    });
    await flush();

    expect(host.innerHTML).toBe("");
    expect(requests).toHaveLength(0);
  });
});

describe("Integrations server detail", () => {
  const endpoint = "/api/v1/assistant/integrations";
  const available = { state: "available" };
  const readFailed = { state: "unavailable", reason: "read_failed" };
  const extensionRef = (manager: string) => ({ manager, kind: "extension", id: "larkx" });
  const row = (manager: string, name = "Server integration") => ({
    ref: extensionRef(manager), name, capabilities: ["channels", "tools", "future_capability"],
    origin: "installed_extension", availability: available,
  });

  async function open(sub: string, manager = "assistant") {
    const route = decodeIntegrationsRoute("integrations", sub, new URLSearchParams({ manager }));
    await act(async () => root.render(<Integrations route={route} onOpen={() => {}} onBack={() => {}} twoCol />));
    await act(async () => vi.dynamicImportSettled());
    await flush();
  }

  function announceProjection(work = true) {
    servedRoster = { ...ROSTER, managers: ROSTER.managers.map((manager) => ({
      ...manager, ...(manager.id === "assistant" || work ? { integrations_projection_v1: true } : {}),
    })) };
    setRoster(servedRoster);
  }

  function catalog(item: ReturnType<typeof row>) {
    return { items: [item], counts: { all: 1, channels: 1, data: 0, tools: 1 }, availability: [] };
  }

  it("opens the server-owned channel while a failed configuration section can be retried", async () => {
    announceProjection();
    chooseManager("work");
    currentChannels.projected_chat = { ...COMPOSITE_CHANNEL, id: "projected_chat" };
    let failed = true;
    projectionRead = ({ url }) => {
      const item = row("assistant");
      if (url === endpoint) return catalog(item);
      return {
        item, availability: [], sections: [
          { kind: "conversation_and_push", ref: { manager: "assistant", kind: "channel", id: "projected_chat" }, availability: available, edit: "settings" },
          { kind: "extension_configuration", ref: item.ref, configuration_groups: ["general"],
            availability: failed ? readFailed : available, edit: failed ? "read_only" : "settings" },
          { kind: "future_action", ref: { manager: "assistant", kind: "channel", id: "unknown_chat" }, availability: available, edit: "settings" },
        ],
      };
    };

    await open("ext:larkx");
    expect(host.querySelector('[data-integration-detail] h1')?.textContent).toBe("Server integration");
    expect(section("channel")?.querySelector("[data-channel-routing]")).not.toBeNull();
    expect(section("extension-settings")?.textContent).not.toContain("Display Alias");
    expect(requests.some((r) => r.url.endsWith("/channels/projected_chat") && r.manager === "assistant")).toBe(true);
    expect(requests.some((r) => /channels\/(larkx_chat|unknown_chat)|\/api\/v1\/connectors(\?|$)/.test(r.url))).toBe(false);
    expect(requests.filter((r) => r.url.startsWith(endpoint)).map((r) => r.manager)).toEqual(["assistant", "assistant"]);
    const alert = host.querySelector('[data-integration-detail] [role="alert"]')!;
    expect(alert.textContent).toBe(t("settings-integrations.projection_section_error"));

    failed = false;
    await act(async () => alert.parentElement?.querySelector("button")?.click());
    await flush();
    expect(host.querySelector('[data-integration-detail] [role="alert"]')).toBeNull();
    expect(section("extension-settings")?.textContent).toContain("Display Alias");
    expect(section("channel")?.querySelector("[data-channel-routing]")).not.toBeNull();
  });

  it("keeps a late response from an assistant with the same id out of the newly opened detail", async () => {
    announceProjection();
    let finish!: (answer: unknown) => void;
    projectionRead = ({ url, manager }) => {
      const item = row(manager!, manager === "work" ? "Work settings" : "Home settings");
      if (url === endpoint) return catalog(item);
      const answer = { item, sections: [], availability: [] };
      return manager === "work" ? answer : new Promise<unknown>((resolve) => { finish = resolve; });
    };
    await open("ext:larkx");
    expect(finish).toBeTypeOf("function");
    await open("ext:larkx", "work");
    expect(host.querySelector('[data-integration-detail] h1')?.textContent).toBe("Work settings");
    await act(async () => finish({ item: row("assistant", "Late home settings"), sections: [], availability: [] }));
    expect(host.querySelector('[data-integration-detail] h1')?.textContent).toBe("Work settings");
    expect(host.textContent).not.toContain("Late home settings");
    expect(requests.filter((r) => r.url.endsWith("/extension/larkx")).map((r) => r.manager)).toEqual(["assistant", "work"]);
  });

  it("uses legacy detail for the assistant that does not advertise projection", async () => {
    announceProjection(false);
    await open("ext:larkx", "work");
    expect(host.querySelector('[data-integration-detail] h1')?.textContent).toBe(COMPOSITE_EXTENSION.name);
    expect(requests.some((r) => r.url.startsWith(endpoint))).toBe(false);
    expect(requests.some((r) => /\/(extensions|plugins)\/larkx\//.test(r.url) && r.manager === "work")).toBe(true);
  });

  it("shows the named assistant's confirmed information access as read only when the section says so", async () => {
    announceProjection();
    servedRoster = { ...servedRoster, managers: servedRoster.managers.map((m) => ({ ...m, extension_info_access_scoped: true })) };
    setRoster(servedRoster);
    grants.work = { extension: currentExt.id, trust: "owner", categories: [] };
    projectionRead = ({ url }) => {
      const item = row("work");
      return url === endpoint ? catalog(item) : {
        item, availability: [], sections: [
          { kind: "information_access", ref: item.ref, availability: { state: "read_only" }, edit: "read_only" },
        ],
      };
    };
    await open("ext:larkx", "work");
    const grant = host.querySelector<HTMLButtonElement>("[data-extension-info-access] [role=switch]")!;
    expect(grant?.getAttribute("aria-checked")).toBe("true");
    expect(grant.disabled).toBe(true);
    expect(requests.filter((r) => r.url.endsWith("/assistant/extension-trust")).map((r) => r.manager)).toEqual(["work"]);
  });

  it("renders the server's module-off detail without calling the bot settings", async () => {
    announceProjection();
    const item = {
      ref: { manager: "assistant", kind: "service", id: "feishu-bot" }, name: "Lark Bot",
      capabilities: ["channels"], origin: "builtin_service", availability: { state: "module_off" },
    };
    const availability = [{ source: "service", ref: item.ref, module_feature: "module.lark", availability: item.availability }];
    projectionRead = ({ url }) => url === endpoint
      ? { items: [], counts: { all: 0, channels: 0, data: 0, tools: 0 }, availability }
      : { item, sections: [], availability };
    await open("builtin:feishu-bot");
    expect(host.textContent).toContain(t("settings-integrations.feishu_module_off"));
    expect(sectionIds()).toEqual([]);
    expect(requests.some((r) => r.url.includes("/bots/feishu") || r.url.includes("/channels/feishu"))).toBe(false);
  });

  it("recovers from an advertised detail failure without opening the legacy editor", async () => {
    announceProjection();
    let failed = true;
    projectionRead = ({ url }) => {
      const item = row("assistant");
      if (url === endpoint) return catalog(item);
      return failed ? Response.json({ error: "Temporarily unavailable", code: "unavailable" }, { status: 503 })
        : { item, sections: [], availability: [] };
    };
    await open("ext:larkx");
    const alert = host.querySelector('[role="alert"]')!;
    expect(alert?.textContent).toContain("Temporarily unavailable");
    expect(requests.some((r) => /\/(extensions|plugins)\/larkx\//.test(r.url))).toBe(false);
    failed = false;
    await act(async () => alert.parentElement?.querySelector("button")?.click());
    await flush();
    expect(host.querySelector('[data-integration-detail] h1')?.textContent).toBe("Server integration");
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });
});

describe("Built-in services", () => {
  it.each([
    ["mail", "mail_module_off"],
    ["feishu-bot", "feishu_module_off"],
    ["feishu", "feishu_module_off"],
    ["larkcli", "feishu_module_off"],
  ])("shows a quiet closed state and a way back on a direct link to %s", async (id, message) => {
    applyCompleteSessions([SEED_SESSION], [], { features: ["modules", "lark"] });
    const onBack = vi.fn();
    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "builtin", id }} onBack={onBack} />));

    expect(host.textContent).toContain(t(`settings-integrations.${message}`));
    expect(host.querySelector('[role="alert"], [role="switch"]')).toBeNull();
    expect(requests).toHaveLength(0);
    act(() => host.querySelector("button")!.click());
    expect(onBack).toHaveBeenCalledOnce();
  });

  it("keeps the Lark extension gateway usable while the Feishu bot module is off", async () => {
    applyCompleteSessions([SEED_SESSION], [], { features: ["modules", "lark"] });
    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "builtin", id: "lark" }} />));
    expect(section("connection")?.textContent).toContain("Team Lark");
    expect(requests.some((r) => r.url.endsWith("/lark/status"))).toBe(true);
    expect(requests.some((r) => r.url.includes("/bots/") || r.url.includes("/channels/feishu"))).toBe(false);
  });

  it.skipIf(!installedMailbox)("renders Mail with Account & Connection accounts and Inbox & Sync switch", async () => {
    await preloadMailbox("Settings");
    await preloadMailbox("InboxSwitch");
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "builtin", id: "mail" }} />);
    });
    await flush();

    expect(sectionIds()).toEqual(["connection", "inbox"]);
    expect(section("connection")!.textContent).toContain("me@example.com");
    expect(section("inbox")!.textContent).toContain(t("settings-hubconnector.to_inbox"));
    expect(requests.some((r) => r.url.includes("/bots/feishu"))).toBe(false);
  });

  it("gives Feishu Bot a single set of controls and refreshes its channel after a bot save", async () => {
    currentFeishuBot.agents = [{ title: "Daily review", machine_id: "computer-id", session_id: "review" }];
    applyCompleteSessions([SEED_SESSION], [], { machines: [{ id: "computer-id", name: "Office laptop" } as never] });
    const onSettings = vi.fn();
    await act(async () => {
      root.render(<IntegrationDetail target={{ kind: "builtin", id: "feishu-bot" }} onSettings={onSettings} />);
    });
    await flush();

    expect(sectionIds()).toEqual(["connection", "channel", "inbox"]);
    expect(section("connection")!.textContent).toContain("Alice");
    const delivery = section("channel")!;
    expect(delivery.textContent).toContain("Office laptop");
    expect(delivery.textContent).not.toContain("computer-id");
    expect(delivery.querySelector("[data-channel-routing]")).toBeNull();
    expect(delivery.textContent).toContain(t("settings-channels.target_row"));
    expect(delivery.textContent).toContain(t("settings-channels.target_override_topics"));
    expect(delivery.textContent).not.toContain(t("settings-channels.target_override_settings"));
    expect(delivery.textContent).not.toContain(t("settings-channels.enable", { name: FEISHU_CHANNEL.name }));
    expect(delivery.textContent).not.toContain(t("settings-channels.send_test"));
    const buttons = [...section("connection")!.querySelectorAll<HTMLButtonElement>("button")];
    expect(buttons.filter((b) => b.textContent === t("settings-feishubot.send_test"))).toHaveLength(1);
    for (const { key, title } of FEATURES) {
      const switches = delivery.querySelectorAll<HTMLButtonElement>(`[role="switch"][aria-label="${title}"]`);
      expect(switches, key).toHaveLength(1);
      if (key === "all_cards") expect(switches[0].disabled).toBe(true);
    }
    const enable = buttons.filter((b) => b.getAttribute("aria-label") === t("settings-feishubot.enable"));
    expect(enable).toHaveLength(1);
    requests.length = 0;
    currentChannels.feishu.state = "disabled";
    await act(async () => enable[0].click());
    await flush();
    const writes = requests.filter((r) => r.method === "PUT");
    expect(writes).toHaveLength(1);
    expect(writes[0]).toMatchObject({ url: expect.stringContaining("/bots/feishu"), body: { enabled: false } });
    expect(requests.some((r) => r.method === "GET" && r.url.endsWith("/channels/feishu"))).toBe(true);
    expect(host.querySelector("h1")!.closest("section")!.textContent).toContain(channelStateText(currentChannels.feishu));
    expect(section("channel")!.textContent).toContain(t("settings-feishubot.topic_groups"));
    const kbBtn = section("inbox")!.querySelector<HTMLButtonElement>('[data-settings-row="knowledge-scope-shortcut"]');
    expect(kbBtn).not.toBeNull();
    act(() => kbBtn!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/knowledge#knowledge-items");
  });

  it("renders built-in GitHub connector with Saved repositories shortcut and tolerates repository API failure", async () => {
    const onSettings = vi.fn();
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "builtin", id: "github" }} onSettings={onSettings} />);
    });
    await flush();

    expect(sectionIds()).toEqual(["connection", "inbox"]);
    expect(section("connection")!.textContent).toContain("octocat");
    expect(section("inbox")!.textContent).toContain("org/noisy");
    expect(section("inbox")!.querySelector('[data-settings-row="knowledge-scope-shortcut"]')).toBeNull();
    const repoBtn = section("connection")!.querySelector<HTMLButtonElement>('[data-settings-row="saved-repositories-shortcut"]');
    expect(repoBtn).not.toBeNull();
    expect(repoBtn!.textContent).toContain(t("settings-integrations.saved_repositories_title"));
    expect(repoBtn!.textContent).toContain(t("settings-integrations.saved_repositories_count", { count: 2 }));
    act(() => repoBtn!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/persona#repositories");
    expect(requests.some((r) => r.url.includes("/bots/feishu"))).toBe(false);

    // Repository query failure does not block rendering or clicking the shortcut.
    onSettings.mockClear();
    currentRepositories = "error";
    act(() => root.render(null));
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "builtin", id: "github" }} onSettings={onSettings} />);
    });
    await flush();
    const fallbackBtn = section("connection")!.querySelector<HTMLButtonElement>('[data-settings-row="saved-repositories-shortcut"]');
    expect(fallbackBtn).not.toBeNull();
    act(() => fallbackBtn!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/persona#repositories");
  });

  it("renders Knowledge sync scope shortcut in Inbox & Sync for Mail, Lark gateway, and history-capable connectors", async () => {
    const onSettings = vi.fn();
    currentConnectors.push({
      id: "chat-history",
      name: "History Connector",
      status: "ok",
      enabled: true,
      archive: true,
      inbox: false,
      capabilities: ["history"],
    });

    for (const target of [
      ...(installedMailbox ? [{ kind: "builtin", id: "mail" } as const] : []),
      { kind: "builtin", id: "lark" } as const,
      { kind: "builtin", id: "chat-history", connectorId: "chat-history" } as const,
    ]) {
      onSettings.mockClear();
      act(() => root.render(null));
      act(() => {
        root.render(<IntegrationDetail target={target} onSettings={onSettings} />);
      });
      await flush();

      const kbBtn = section("inbox")?.querySelector<HTMLButtonElement>('[data-settings-row="knowledge-scope-shortcut"]');
      expect(kbBtn, `${target.id} knowledge shortcut`).not.toBeNull();
      expect(kbBtn!.textContent).toContain(t("settings-integrations.knowledge_scope_title"));
      act(() => kbBtn!.click());
      expect(onSettings).toHaveBeenCalledWith("settings/knowledge#knowledge-items");
    }
  });

  it("omits the Back button when onBack is undefined (desktop split-pane / phone) and renders it when provided", async () => {
    const onBack = vi.fn();
    for (const target of [
      ...(installedMailbox ? [{ kind: "builtin", id: "mail" } as const] : []),
      { kind: "builtin", id: "feishu-bot" } as const,
      { kind: "extension", id: "larkx" } as const,
    ]) {
      act(() => {
        root.render(<IntegrationDetail target={target} />);
      });
      await flush();
      const hiddenBack = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("settings-integrations.back")));
      expect(hiddenBack, `${target.id} without onBack`).toBeUndefined();

      act(() => {
        root.render(<IntegrationDetail target={target} onBack={onBack} />);
      });
      await flush();
      const visibleBack = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("settings-integrations.back")));
      expect(visibleBack, `${target.id} with onBack`).toBeDefined();
      act(() => visibleBack!.click());
    }
    expect(onBack).toHaveBeenCalledTimes(installedMailbox ? 3 : 2);
  });
});

describe("Composite extension (connector + channel + sectioned config + lifecycle)", () => {
  it("renders all 5 sections inline, folds advanced fields, and saves unified config atomically", async () => {
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} />);
    });
    await flush();

    expect(sectionIds()).toEqual(["connection", "channel", "inbox", "extension-settings", "management"]);

    // 1. Account & Connection: auth + connector + section="connection" field
    expect(section("connection")!.textContent).toContain("Workspace OAuth");
    expect(section("connection")!.textContent).toContain("owner@example.com");
    expect(section("connection")!.querySelector('[data-field="endpoint"]')).not.toBeNull();

    // 2. Chat & Delivery: channel toggle, Send to target, thread_mode & thread_idle_hours, section="channel" field
    expect(section("channel")!.textContent).toContain(t("settings-channels.target_row"));
    expect(section("channel")!.textContent).toContain(t("settings-integrations.thread_mode_title"));
    expect(section("channel")!.textContent).toContain(t("settings-integrations.thread_idle_hours_title"));
    expect(section("channel")!.querySelector('[data-field="mention_prefix"]')).not.toBeNull();

    // 3. Inbox & Sync: connector feeds + section="inbox" field + knowledge sync scope shortcut for larkx
    expect(section("inbox")!.textContent).toContain(t("settings-hubconnector.to_inbox"));
    expect(section("inbox")!.querySelector('[data-field="triage_rules"]')).not.toBeNull();
    expect(section("inbox")!.querySelector('[data-settings-row="knowledge-scope-shortcut"]')).not.toBeNull();

    // 4. Extension Settings: unsectioned field visible, advanced field folded by default, secret present
    expect(section("extension-settings")!.querySelector('[data-field="display_alias"]')).not.toBeNull();
    expect(section("extension-settings")!.querySelector('[data-field="debug_trace"]')).toBeNull();
    expect(section("extension-settings")!.textContent).toContain("App Secret");

    // Expand Advanced disclosure -> reveals section="advanced" field
    const advButton = section("extension-settings")!.querySelector<HTMLButtonElement>("[data-integration-advanced] button")!;
    expect(advButton.getAttribute("aria-expanded")).toBe("false");
    act(() => advButton.click());
    await flush();
    expect(advButton.getAttribute("aria-expanded")).toBe("true");
    expect(section("extension-settings")!.querySelector('[data-field="debug_trace"]')).not.toBeNull();

    // 5. Plugin Management: version, enable, updates, uninstall
    expect(section("management")!.textContent).toContain("2.1.0");
    expect(section("management")!.textContent).toContain(t("settings-extensions.uninstall"));

    // Edit fields in two different sections (connection + general) -> single unified Save persists all fields
    const endpointInput = section("connection")!.querySelector<HTMLInputElement>("#extension-field-endpoint")!;
    const aliasInput = section("extension-settings")!.querySelector<HTMLInputElement>("#extension-field-display_alias")!;
    act(() => {
      setInputValue(endpointInput, "https://next.example.com");
      setInputValue(aliasInput, "Updated Lark");
    });
    await flush();

    const saveBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("common.save"))!;
    expect(saveBtn).toBeDefined();
    await act(async () => saveBtn.click());
    await flush();

    const putCfg = requests.find((r) => r.method === "PUT" && r.url.includes("/-/config"));
    expect(putCfg).toBeDefined();
    expect(window.dispatchEvent(new Event("beforeunload", { cancelable: true }))).toBe(true);
    expect(putCfg!.body).toEqual({
      config: {
        endpoint: "https://next.example.com",
        mention_prefix: "@bot",
        triage_rules: "Only urgent items",
        display_alias: "Updated Lark",
        debug_trace: false,
      },
    });
  });

  it("waits for a channel's display name on a direct detail link", async () => {
    const originalFetch = fetch;
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    vi.stubGlobal("fetch", (url: RequestInfo | URL, init?: RequestInit) =>
      String(url).endsWith("/channels/internal-channel-id") ? pending : originalFetch(url, init));
    await act(async () => root.render(
      <IntegrationDetail target={{ kind: "builtin", id: "internal-channel-id", channelId: "internal-channel-id", connectorId: null }} />,
    ));
    expect(host.textContent).not.toContain("internal-channel-id");
    await act(async () => finish(new Response(JSON.stringify({ ...COMPOSITE_CHANNEL, id: "internal-channel-id" }))));
    expect(host.querySelector("h1")?.textContent).toBe(COMPOSITE_CHANNEL.name);
  });

  it.each(["settings/plugins/other", "settings/plugins/larkx"])("shows read-only channel activity and routes More settings only to another page (%s)", async (url) => {
    const onSettings = vi.fn();
    const when = new Date().toISOString();
    Object.assign(currentChannels.larkx_chat, { read_only: true, settings_url: url, last_event_at: when });
    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} onSettings={onSettings} />));
    await flush();
    expect(host.querySelector("h1")?.closest("section")?.textContent).toContain(t("settings-channels.last_event", { when: ago(when) }));
    const more = [...section("channel")!.querySelectorAll("button")].find((b) => b.textContent === t("settings-channels.more_settings"));
    if (url.endsWith("other")) {
      expect(more).toBeDefined();
      act(() => more!.click());
      expect(onSettings).toHaveBeenCalledWith(url);
    } else {
      expect(more).toBeUndefined();
    }
  });

  it("keeps the override link when it opens another integration", async () => {
    const onSettings = vi.fn();
    currentChannels.larkx_chat.settings_url = "settings/plugins/other";
    currentChannels.larkx_chat.target_override = { mode: "threads" };
    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} onSettings={onSettings} />));
    await flush();
    const link = [...section("channel")!.querySelectorAll("button")].find(
      (b) => b.textContent === t("settings-channels.target_override_extension_settings"),
    );
    expect(link).toBeDefined();
    act(() => link!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/plugins/other");
  });

  it("acknowledges a routing save and reconciles with a fresh read while an older refresh is pending", async () => {
    const fetchNow = fetch;
    let finishPut!: () => void;
    let finishOldRead!: () => void;
    let finishReconcile!: () => void;
    let delayNextRead = false;
    let delayReconcile = false;
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith("/channels/larkx_chat")) return fetchNow(input, init);
      if (init?.method === "PUT") {
        return new Promise((resolve) => {
          finishPut = () => resolve(ok({ ...COMPOSITE_CHANNEL, thread_mode: "per_topic" }));
        });
      }
      if (delayNextRead) {
        delayNextRead = false;
        const stale = { ...currentChannels.larkx_chat };
        return new Promise((resolve) => { finishOldRead = () => resolve(ok(stale)); });
      }
      if (delayReconcile) {
        delayReconcile = false;
        return new Promise((resolve) => { finishReconcile = () => resolve(ok(currentChannels.larkx_chat)); });
      }
      return fetchNow(input, init);
    });
    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} />));
    await flush();
    const delivery = section("channel")!;
    const option = (label: string) => [...delivery.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find(
      (b) => b.textContent?.includes(label),
    )!;
    const enable = delivery.querySelector<HTMLButtonElement>('[role="switch"]')!;
    const topic = t("settings-integrations.thread_mode_topic_title");
    delayNextRead = true;
    const refresh = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-integrations.refresh")}"]`)!;
    await act(async () => refresh.click());
    await act(async () => option(topic).click());
    expect(enable.disabled).toBe(true);
    expect(option(topic).getAttribute("aria-checked")).toBe("true");

    currentChannels.larkx_chat.thread_mode = "per_group_and_topic";
    const groupTopic = t("settings-integrations.thread_mode_group_topic_title");
    delayReconcile = true;
    await act(async () => finishPut());

    expect(delivery.textContent).toContain(t("settings-integrations.routing_saved"));
    expect(option(topic).getAttribute("aria-checked")).toBe("true");
    await act(async () => finishOldRead());
    expect(option(topic).getAttribute("aria-checked")).toBe("true");
    await act(async () => finishReconcile());
    expect(option(groupTopic).getAttribute("aria-checked")).toBe("true");
    expect(delivery.textContent).not.toContain(t("settings-configuration.error_configuration_conflict"));
    expect(delivery.querySelector<HTMLButtonElement>('[role="switch"]')!.disabled).toBe(false);
  });

  it("keeps an unsaved channel routing field through refresh and switching between one and two columns", async () => {
    const onOpen = vi.fn();
    const draw = (wide: boolean) => act(async () => root.render(<Integrations route={decodeIntegrationsRoute("integrations", "ext:larkx")} twoCol={wide} onOpen={onOpen} onBack={() => {}} />));
    await draw(true);
    await act(async () => vi.dynamicImportSettled());

    const field = () => host.querySelector<HTMLElement>('[data-configuration-field="thread_idle_hours"]')!;
    const input = () => field().querySelector<HTMLInputElement>("input")!;
    await act(async () => setInputValue(input(), "48"));
    currentChannels.larkx_chat = { ...currentChannels.larkx_chat, thread_mode: "per_topic", thread_idle_hours: 24 };
    const refresh = host.querySelector<HTMLButtonElement>(`[data-integration-detail] button[aria-label="${t("settings-integrations.refresh")}"]`)!;
    await act(async () => refresh.click());

    expect(input().value).toBe("48");
    expect(section("channel")?.querySelector('[role="radio"][aria-checked="true"]')?.textContent)
      .toContain(t("settings-integrations.thread_mode_topic_title"));
    // The narrow Settings host can also replace the detail subtree.
    await act(async () => root.render(null));
    for (const wide of [false, true]) {
      await draw(wide);
      await act(async () => vi.dynamicImportSettled());
      expect(input().value).toBe("48");
      expect(!!host.querySelector("[data-integrations-split]")).toBe(wide);
    }
    const save = [...field().querySelectorAll("button")].find((b) => b.textContent === t("settings-configuration.save"))!;
    await act(async () => save.click());
    expect(requests.filter((r) => r.url.endsWith("/channels/larkx_chat") && r.method === "PUT").map((r) => r.body))
      .toEqual([{ thread_idle_hours: 48 }]);
    expect(currentChannels.larkx_chat).toMatchObject({ thread_idle_hours: 48, thread_mode: "per_topic" });
  });

  it("saves thread_mode and thread_idle_hours via PUT /api/v1/channels/{id} and sends test messages", async () => {
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} onSettings={vi.fn()} />);
    });
    await flush();

    // Switch thread_mode to per_group_and_topic
    const groupTopicOption = [...section("channel")!.querySelectorAll<HTMLButtonElement>('[role="radio"]')].find((b) =>
      b.textContent?.includes(t("settings-integrations.thread_mode_group_topic_title")),
    )!;
    expect(groupTopicOption).toBeDefined();
    await act(async () => groupTopicOption.click());
    await flush();

    const modePut = requests.find((r) => r.method === "PUT" && r.url.includes("/channels/larkx_chat"));
    expect(modePut?.body).toEqual({ thread_mode: "per_group_and_topic" });
    // Response updated target_override -> override hint appears immediately
    expect(section("channel")!.textContent).toContain(t("settings-channels.target_override_groups_and_topics"));

    expect(section("channel")!.textContent).not.toContain(t("settings-channels.target_override_extension_settings"));

    // Update thread_idle_hours to 48 and click Save inside the ConfigurationForm field
    const idleField = section("channel")!.querySelector<HTMLElement>('[data-configuration-field="thread_idle_hours"]')!;
    const idleInput = idleField.querySelector<HTMLInputElement>("input")!;
    act(() => setInputValue(idleInput, "48"));
    await flush();

    const idleSave = [...idleField.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("settings-configuration.save"))!;
    await act(async () => idleSave.click());
    await flush();

    const idlePut = requests.filter((r) => r.method === "PUT" && r.url.includes("/channels/larkx_chat")).pop();
    expect(idlePut?.body).toEqual({ thread_idle_hours: 48 });

    // Send test message
    const testBtn = [...section("channel")!.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("settings-channels.send_test")))!;
    await act(async () => testBtn.click());
    await flush();
    expect(requests.some((r) => r.method === "POST" && r.url.includes("/channels/larkx_chat/test"))).toBe(true);
    expect(section("channel")!.querySelector('[role="status"]')?.textContent).toBe("Test delivered");
  });
});

describe("Older Hub and legacy extension graceful degradation", () => {
  it.each([
    ["built-in voice", { id: "voice", kind: "voice", source: "builtin" }, false],
    ["app", { kind: "app" }, false],
    ["market", { source: "market" }, false],
    ["uninstalled plugin", { installed: false }, false],
    ["read-only plugin", { read_only: true }, false],
    ["mention plugin", { kind: "mention" }, true],
  ] as const)("shows the expected routing controls for %s", async (_name, patch, visible) => {
    const channel = "source" in patch && patch.source === "builtin"
      ? { ...FEISHU_CHANNEL, ...patch, id: "voice", name: "Voice" }
      : { ...COMPOSITE_CHANNEL, ...patch };
    currentChannels[channel.id] = channel;
    const target = channel.source === "builtin"
      ? { kind: "builtin" as const, id: channel.id, channelId: channel.id, connectorId: null }
      : { kind: "extension" as const, id: "larkx" };
    act(() => root.render(<IntegrationDetail target={target} />));
    await flush();
    expect(section("channel")).not.toBeNull();
    expect(!!section("channel")!.querySelector("[data-channel-routing]")).toBe(visible);
  });

  it("degrades routing controls to read-only on an older Hub omitting thread_mode/thread_idle_hours", async () => {
    currentChannels.larkx_chat = {
      ...COMPOSITE_CHANNEL,
      thread_mode: undefined,
      thread_idle_hours: undefined,
    };

    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} />);
    });
    await flush();

    const ch = section("channel")!;
    expect(ch.textContent).toContain(t("settings-integrations.routing_hub_unsupported"));
    const radios = [...ch.querySelectorAll<HTMLButtonElement>('[data-configuration-field="thread_mode"] [role="radio"]')];
    expect(radios.length).toBe(3);
    // Fieldset is disabled so clicking does not issue an unsupported PUT
    const fieldset = ch.querySelector<HTMLFieldSetElement>('[data-configuration-field="thread_mode"] fieldset');
    expect(fieldset?.disabled).toBe(true);
    expect(requests.some((r) => r.method === "PUT" && r.url.includes("/channels/"))).toBe(false);
  });

  it("renders legacy extensions without section metadata seamlessly and omits unused sections", async () => {
    currentExt = {
      id: "legacy-tool",
      name: "Legacy Tool",
      kind: "process",
      source: "installed",
      status: "running",
      version: "1.0.0",
      enabled: true,
      origin: "market:official/legacy-tool",
      auto_upgrade: false,
      form: {
        fields: [
          { name: "api_url", type: "string", title: "API URL" },
          { name: "max_items", type: "integer", title: "Max Items", default: 20 },
        ],
      },
      config: { api_url: "https://legacy.example.com" },
    };

    // Verify both light and dark themes render without error or hard-coded theme issues
    document.documentElement.classList.add("dark");
    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "legacy-tool" }} />);
    });
    await flush();

    expect(sectionIds()).toEqual(["extension-settings", "management"]);
    expect(section("extension-settings")!.querySelector('[data-field="api_url"]')).not.toBeNull();
    expect(section("extension-settings")!.querySelector('[data-field="max_items"]')).not.toBeNull();
    expect(section("extension-settings")!.querySelector("[data-integration-advanced]")).toBeNull();
    expect(host.querySelector('[data-settings-row="saved-repositories-shortcut"]')).toBeNull();
    expect(host.querySelector('[data-settings-row="knowledge-scope-shortcut"]')).toBeNull();
  });

  it("shows Knowledge sync scope shortcut for an external extension whose connector advertises history capability", async () => {
    const onSettings = vi.fn();
    currentExt = {
      id: "acme-chat",
      name: "Acme Chat",
      kind: "process",
      source: "installed",
      status: "running",
      version: "1.0.0",
      enabled: true,
      origin: "market:official/acme-chat",
      contributes: { connectors: ["acme_conn"] },
    };
    currentConnectors = [
      { id: "acme_conn", name: "Acme", status: "ok", enabled: true, archive: true, capabilities: ["history"] },
    ];

    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "acme-chat" }} onSettings={onSettings} />);
    });
    await flush();

    const kbBtn = section("inbox")?.querySelector<HTMLButtonElement>('[data-settings-row="knowledge-scope-shortcut"]');
    expect(kbBtn).not.toBeNull();
    act(() => kbBtn!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/knowledge#knowledge-items");
  });

  it("shows Saved repositories shortcut for a GitHub-compatible extension", async () => {
    const onSettings = vi.fn();
    currentExt = {
      id: "github-extra",
      name: "GitHub Extra",
      kind: "process",
      source: "installed",
      status: "running",
      version: "1.0.0",
      enabled: true,
      origin: "market:official/github-extra",
      contributes: { repository_providers: ["github"] },
    };

    act(() => {
      root.render(<IntegrationDetail target={{ kind: "extension", id: "github-extra" }} onSettings={onSettings} />);
    });
    await flush();

    const repoBtn = section("connection")?.querySelector<HTMLButtonElement>('[data-settings-row="saved-repositories-shortcut"]');
    expect(repoBtn).not.toBeNull();
    expect(repoBtn!.textContent).toContain(t("settings-integrations.saved_repositories_count", { count: 2 }));
    act(() => repoBtn!.click());
    expect(onSettings).toHaveBeenCalledWith("settings/persona#repositories");
  });
});

describe("manager-owned integration links", () => {
  it.each([
    ["work", null],
    ["assistant", "work"],
  ] as const)("reads and writes the named %s extension's grants in Labs Integrations", async (manager, sidebar) => {
    servedRoster = { ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, extension_info_access_scoped: true })) };
    setRoster(servedRoster);
    chooseManager(sidebar);
    grants = {
      assistant: { extension: currentExt.id, trust: "owner", categories: [] },
      work: { extension: currentExt.id, trust: "third_party", categories: ["finance"] },
    };

    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "extension", id: currentExt.id, manager }} />));
    const owner = host.querySelector<HTMLButtonElement>("[data-extension-info-access] [role=switch]")!;
    expect(owner?.getAttribute("aria-checked")).toBe(String(manager === "assistant"));
    await act(async () => owner.click());
    const trustRequests = requests.filter(({ url }) => url.includes("/assistant/extension-trust"));
    expect(trustRequests.map((r) => [r.method, r.manager])).toEqual([["GET", manager], ["PUT", manager], ["GET", manager]]);
    expect(grants.assistant.trust).toBe(manager === "assistant" ? "third_party" : "owner");
    expect(grants.work.trust).toBe(manager === "work" ? "owner" : "third_party");
    expect(chosenManager()).toBe(sidebar);
  });

  it("shows the supported assistant's confirmed grants on a read-only integration", async () => {
    servedRoster = { ...ROSTER, managers: ROSTER.managers.map((m) => ({ ...m, extension_info_access_scoped: true })) };
    setRoster(servedRoster);
    currentExt = { ...currentExt, read_only: true };
    grants.work = { extension: currentExt.id, trust: "owner", categories: [] };

    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "extension", id: currentExt.id, manager: "work" }} />));
    const owner = host.querySelector<HTMLButtonElement>("[data-extension-info-access] [role=switch]")!;
    expect(owner?.getAttribute("aria-checked")).toBe("true");
    expect(owner.disabled).toBe(true);
    await act(async () => owner.click());
    expect(requests.filter(({ url }) => url.includes("/assistant/extension-trust")).map(({ method, manager }) => [method, manager]))
      .toEqual([["GET", "work"]]);
  });

  it.each([
    ["feishu-bot", "/bots/feishu"],
    ["lark", "/lark/status"],
    ...(installedMailbox ? [["mail", "/mail/accounts"]] : []),
  ])("keeps an explicit default-assistant %s link scoped inside a secondary assistant's Settings", async (id, path) => {
    if (id === "mail") await preloadMailbox("Settings");
    chooseManager("work");
    function SecondarySettings() {
      useLayoutEffect(() => pinManagerAt(["/bots", "/lark", "/mail", "/channels"], "work"), []);
      return <IntegrationDetail integrationsV2 target={{ kind: "builtin", id, manager: "assistant" }} />;
    }
    await act(async () => root.render(<SecondarySettings />));
    expect(requests).toContainEqual(expect.objectContaining({ url: `/api/v1${path}`, method: "GET", manager: null }));
    if (id === "feishu-bot") {
      const toggle = host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-feishubot.enable")}"]`)!;
      expect(requests).toContainEqual(expect.objectContaining({ url: "/api/v1/channels/feishu", method: "GET", manager: "assistant" }));
      await act(async () => toggle.click());
      expect(requests).toContainEqual(expect.objectContaining({ url: "/api/v1/bots/feishu", method: "PUT", manager: null }));
    }
    expect(chosenManager()).toBe("work");
  });

  it("scopes contributed and target connectors to the extension owner for the lifetime of the detail", async () => {
    currentConnectors.push({ ...COMPOSITE_CONNECTOR, manager: "work" }, { id: "imessage", name: "Messages", status: "ok", manager: "work" });
    await act(async () => root.render(
      <IntegrationDetail integrationsV2 target={{ kind: "extension", id: "larkx", connectorId: "imessage", manager: "work" }} />,
    ));
    await flush();
    const toggle = section("inbox")!.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-hubconnector.to_inbox")}"]`)!;
    await act(async () => toggle.click());

    expect(requests.filter((r) => r.url.endsWith("/connectors/larkx_conn/settings"))).toEqual([
      expect.objectContaining({ method: "PUT", body: { inbox: false }, manager: "work" }),
    ]);
    expect(requests).toContainEqual(expect.objectContaining({ url: "/api/v1/imessage", method: "GET", manager: "work" }));
    expect(currentConnectors.find((c) => c.id === "larkx_conn" && c.manager === "work")?.inbox).toBe(false);
    expect(currentConnectors.find((c) => c.id === "larkx_conn" && !c.manager)?.inbox).toBe(true);
    expect(managerHeaderFor("/connectors/imessage/settings")).toBe("work");
    expect(managerHeaderFor("/connectors/unrelated/settings")).toBeUndefined();
    expect(chosenManager()).toBeNull();
    act(() => root.render(null));
    for (const path of ["/connectors/larkx_conn/settings", "/connectors/imessage/settings", "/imessage"]) {
      expect(managerHeaderFor(path)).toBeUndefined();
    }
  });

  it.each([
    ["github", "saved-repositories-shortcut", "settings/persona#repositories"],
    ...(installedMailbox ? [["mail", "knowledge-scope-shortcut", "settings/knowledge#knowledge-items"]] : []),
  ])("carries the owner to %s settings while keeping the sidebar selection", async (id, row, destination) => {
    const onSettings = vi.fn(() => chosenManager());
    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "builtin", id, manager: "work" }} onSettings={onSettings} />));
    await flush();
    expect(chosenManager()).toBeNull();
    if (id === "github") {
      expect(requests).toContainEqual(expect.objectContaining({ url: "/api/v1/assistant/repositories", manager: "work" }));
    }
    act(() => host.querySelector<HTMLButtonElement>(`[data-settings-row="${row}"]`)!.click());
    expect(onSettings).toHaveBeenCalledWith(destination, "work");
    expect(chosenManager()).toBeNull();
  });
});

describe("integration detail parity", () => {
  const extension = { kind: "extension", id: "larkx" } as const;
  const refresh = () => host.querySelector<HTMLButtonElement>(`button[aria-label="${t("settings-integrations.refresh")}"]`)!.click();
  const unloadWarns = () => !window.dispatchEvent(new Event("beforeunload", { cancelable: true }));
  const input = (name: string) => host.querySelector<HTMLInputElement>(`#extension-field-${name}`)!;

  it.each(["classic", "unified"])("counts fields that need Save in the %s editor", async (editor) => {
    currentExt.form!.fields!.push(
      { name: "computers", type: "array", items: { name: "", type: "string", format: "machine" } },
      { name: "minutes", type: "integer" },
    );
    currentExt.config = { ...currentExt.config, computers: ["demo"], minutes: 5 };
    await act(async () => root.render(editor === "classic"
      ? <Extensions open={extension.id} onOpen={() => {}} />
      : <IntegrationDetail target={extension} />));
    const save = () => [...host.querySelectorAll("button")].find((b) => b.textContent === t("common.save")
      && b.parentElement?.textContent?.includes(t("settings-extensions.revert")));
    const editField = (name: string, value: string) => act(() => setInputValue(input(name), value));

    await act(async () => host.querySelector<HTMLButtonElement>('#extension-field-computers [role="switch"][aria-label="demo"]')!.click());
    expect(requests.some((r) => r.method === "PUT" && r.url.includes("/-/config"))).toBe(true);
    expect(save()).toBeUndefined();

    editField("display_alias", "First edit");
    editField("display_alias", "Second edit");
    editField("minutes", "10");
    expect(host.textContent).toContain("还有 2 项待保存");
    editField("minutes", "5");
    expect(host.textContent).toContain("还有 1 项待保存");

    editField("minutes", "invalid");
    expect(host.textContent).toContain(t("settings-extensions.not_a_number"));
    expect(save()?.disabled).toBe(true);
    editField("minutes", "5");
    editField("display_alias", "Team Lark");
    expect(save()).toBeUndefined();

    editField("display_alias", "Final edit");
    await act(async () => save()!.click());
    expect(currentExt.config?.display_alias).toBe("Final edit");
    expect(save()).toBeUndefined();
  });

  it("excludes an in-flight machine switch while other fields are edited", async () => {
    currentExt.form!.fields!.push({ name: "computers", type: "array", items: { name: "", type: "string", format: "machine" } });
    currentExt.config = { ...currentExt.config, computers: ["demo"] };
    const fetchNow = fetch;
    let release!: () => void;
    const pending = new Promise<void>((resolve) => { release = resolve; });
    vi.stubGlobal("fetch", async (url: string, init?: RequestInit) => {
      if (init?.method === "PUT" && url.includes("/-/config")) await pending;
      return fetchNow(url, init);
    });
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    const bar = () => host.querySelector('[data-settings-group="integration-save-bar"]');
    await act(async () => host.querySelector<HTMLButtonElement>('#extension-field-computers [role="switch"][aria-label="demo"]')!.click());
    expect(bar()).toBeNull();
    act(() => setInputValue(input("display_alias"), "Edited while saving"));
    expect(bar()?.textContent).toContain("还有 1 项待保存");
    await act(async () => release());
    expect(bar()?.textContent).toContain("还有 1 项待保存");
    expect(currentExt.config?.computers).toBeUndefined();
  });

  it.each([
    ["en", "1 field to save", "2 fields to save"],
    ["zh-Hans", "还有 1 项待保存", "还有 2 项待保存"],
    ["ja", "未保存の項目：1 件", "未保存の項目：2 件"],
  ])("names one and two pending fields in %s", async (language, one, two) => {
    await initI18n(language);
    try {
      expect(t("settings-extensions.fields_to_save", { count: 1 })).toBe(one);
      expect(t("settings-extensions.fields_to_save", { count: 2 })).toBe(two);
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("keeps a draft after settings_moved and retries on the refreshed configuration", async () => {
    const fetchNow = fetch;
    let fail!: (answer: ReturnType<typeof ok>) => void;
    let first = true;
    const writes: unknown[] = [];
    vi.stubGlobal("fetch", (url: string, init?: RequestInit) => {
      if (init?.method === "PUT" && url.includes("/-/config")) {
        writes.push(JSON.parse(String(init.body)));
        if (first) {
          first = false;
          return new Promise((resolve) => { fail = resolve; });
        }
      }
      return fetchNow(url, init);
    });
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    act(() => setInputValue(input("endpoint"), "https://edited.example.com"));
    const save = [...host.querySelectorAll<HTMLButtonElement>('[data-settings-group="integration-save-bar"] button')]
      .find((b) => b.textContent === t("common.save"))!;
    await act(async () => { save.click(); save.click(); });
    expect(writes).toHaveLength(1);
    act(() => setInputValue(input("endpoint"), "https://newer.example.com"));
    currentExt = { ...currentExt, config: { ...currentExt.config, display_alias: "Changed elsewhere" } };
    await act(async () => fail({
      ...ok({ code: "settings_moved", error: "Settings moved. Try again." }), ok: false, status: 409,
    }));
    expect(host.textContent).toContain("Settings moved. Try again.");
    expect(host.textContent).toContain("还有 1 项待保存");
    expect(input("endpoint").value).toBe("https://newer.example.com");
    expect(input("display_alias").value).toBe("Changed elsewhere");
    await act(async () => save.click());
    expect(writes).toHaveLength(2);
    expect(currentExt.config).toMatchObject({ endpoint: "https://newer.example.com", display_alias: "Changed elsewhere" });
    expect(unloadWarns()).toBe(false);
  });

  it("keeps both resource drafts across the measured 740 px Settings breakpoint", async () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
    const { Settings } = await import("../Settings");
    let width = 740;
    let resize!: () => void;
    const measure = vi.spyOn(Element.prototype, "clientWidth", "get").mockImplementation(() => width);
    vi.stubGlobal("ResizeObserver", class {
      constructor(private notify: () => void) {}
      observe(element: Element) { if (element.matches('[role="region"]')) resize = this.notify; }
      disconnect() {}
    });
    const original = location.href;
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    try {
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      await act(async () => vi.dynamicImportSettled());
      const hours = () => host.querySelector<HTMLInputElement>('[data-configuration-field="thread_idle_hours"] input')!;
      await act(async () => {
        setInputValue(input("endpoint"), "https://edited.example.com");
        setInputValue(hours(), "48");
      });
      for (width of [739, 740, 739, 740]) {
        await act(async () => resize());
        expect(!!host.querySelector("[data-integrations-split]")).toBe(width >= 740);
        expect(hours().value).toBe("48");
        expect(input("endpoint").value).toBe("https://edited.example.com");
      }
      expect(requests.filter(({ method }) => method === "PUT")).toEqual([]);
    } finally {
      measure.mockRestore();
      history.replaceState(null, "", original);
    }
  }, 30000);

  it("shares an extension draft with the legacy page and clears it on re-pairing", async () => {
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    await act(async () => setInputValue(input("endpoint"), "https://edited.example.com"));
    await act(async () => root.render(<Extensions open="larkx" onOpen={() => {}} />));
    expect(input("endpoint").value).toBe("https://edited.example.com");
    expect(unloadWarns()).toBe(true);
    await act(async () => {
      setPairedToken("");
      setRoster(structuredClone(ROSTER));
    });
    expect(input("endpoint").value).toBe(currentExt.config?.endpoint);
    expect(unloadWarns()).toBe(false);
  });

  it("acknowledges a lost configuration reply only after reading the edited resource", async () => {
    const fetchNow = fetch;
    const calls: { method: string; manager: string | null }[] = [];
    vi.stubGlobal("fetch", (url: string, init?: RequestInit) => {
      if (!url.includes("/larkx/-/")) return fetchNow(url, init);
      const method = init?.method ?? "GET";
      if (!url.includes("entry_lists=1")) return fetchNow(url, init);
      calls.push({ method, manager: new Headers(init?.headers).get("X-Persona-Manager") });
      if (method !== "PUT") return fetchNow(url, init);
      currentExt = { ...currentExt, config: JSON.parse(String(init?.body)).config };
      return Promise.reject(new TypeError("Failed to fetch"));
    });
    await act(async () => root.render(<IntegrationDetail target={{ ...extension, manager: "assistant" }} integrationsV2 />));
    await act(async () => setInputValue(input("endpoint"), "https://saved.example.com"));
    const save = [...host.querySelectorAll<HTMLButtonElement>('[data-settings-group="integration-save-bar"] button')]
      .find((b) => b.textContent === t("common.save"))!;
    await act(async () => save.click());
    expect(calls).toEqual(["GET", "GET", "PUT", "GET"].map((method) => ({ method, manager: "assistant" })));
    expect(input("endpoint").value).toBe("https://saved.example.com");
    expect(unloadWarns()).toBe(false);
  });

  it.each([false, true])("loads a fresh full-config baseline before saving (overlap=%s)", async (overlap) => {
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    await act(async () => setInputValue(input("endpoint"), "https://edited.example.com"));
    currentExt = {
      ...currentExt,
      config: {
        ...currentExt.config, display_alias: "Updated elsewhere",
        ...(overlap && { endpoint: "https://remote.example.com" }),
      },
    };
    const save = [...host.querySelectorAll<HTMLButtonElement>('[data-settings-group="integration-save-bar"] button')]
      .find((b) => b.textContent === t("common.save"))!;
    await act(async () => save.click());
    expect(input("display_alias").value).toBe("Updated elsewhere");
    expect(input("endpoint").value).toBe("https://edited.example.com");
    expect(requests.filter(({ url, method }) => url.includes("/-/config") && method === "PUT")).toHaveLength(overlap ? 0 : 1);
    expect(unloadWarns()).toBe(overlap);
    if (overlap) expect(host.textContent).toContain(t("settings-configuration.error_configuration_conflict"));
    else expect(currentExt.config).toMatchObject({ endpoint: "https://edited.example.com", display_alias: "Updated elsewhere" });
  });

  it("keeps a late PUT on its original assistant while editing another copy", async () => {
    const fetchNow = fetch;
    const copies = { assistant: structuredClone(currentExt), work: structuredClone(currentExt) };
    copies.work.config = { ...copies.work.config, endpoint: "https://work.example.com" };
    const writes: (string | null)[] = [];
    let finish!: () => void;
    vi.stubGlobal("fetch", (url: string, init?: RequestInit) => {
      if (!url.includes("/larkx/-/") || !url.includes("entry_lists=1")) return fetchNow(url, init);
      const owner = new Headers(init?.headers).get("X-Persona-Manager");
      const copy = owner === "work" ? copies.work : copies.assistant;
      if (init?.method !== "PUT") return Promise.resolve(ok(copy));
      writes.push(owner);
      return new Promise((resolve) => {
        finish = () => {
          copy.config = JSON.parse(String(init.body)).config;
          resolve(ok(copy));
        };
      });
    });
    const draw = (manager: string) =>
      act(async () => root.render(<IntegrationDetail target={{ ...extension, manager }} integrationsV2 />));
    await act(async () => chooseManager("work"));
    await draw("assistant");
    await act(async () => setInputValue(input("endpoint"), "https://home-edit.example.com"));
    const save = [...host.querySelectorAll<HTMLButtonElement>('[data-settings-group="integration-save-bar"] button')]
      .find((b) => b.textContent === t("common.save"))!;
    await act(async () => save.click());
    expect(writes).toEqual(["assistant"]);
    await draw("work");
    expect(input("endpoint").value).toBe("https://work.example.com");
    await act(async () => setInputValue(input("endpoint"), "https://work-edit.example.com"));
    await act(async () => finish());
    expect(input("endpoint").value).toBe("https://work-edit.example.com");
    await draw("assistant");
    expect(input("endpoint").value).toBe("https://home-edit.example.com");
    expect(writes).toEqual(["assistant"]);
  });

  it("puts link-prefilled values in an editable draft once and saves only recognized fields on request", async () => {
    const page = <IntegrationDetail target={extension} prefill={{ display_alias: "From a link", unknown_key: "ignored", app_secret: "ignored" }} />;
    await act(async () => root.render(page));
    expect(input("display_alias").value).toBe("From a link");
    expect(requests.some((r) => r.method === "PUT")).toBe(false);

    await act(async () => setInputValue(input("display_alias"), "My edit"));
    await act(async () => refresh());
    expect(input("display_alias").value).toBe("My edit");
    const save = [...host.querySelectorAll<HTMLButtonElement>('[data-settings-group="integration-save-bar"] button')]
      .find((b) => b.textContent === t("common.save"))!;
    await act(async () => save.click());
    expect(currentExt.config).toEqual({ ...COMPOSITE_EXTENSION.config, display_alias: "My edit" });
  });

  it.each(["feishu-bot", "github", "larkx"])("marks retained connector and channel details when a refresh fails for %s", async (id) => {
    currentConnectors.push({ id: "larkcli", name: "Lark CLI", status: "ok", capabilities: ["inbox", "login"] });
    const target = id === "larkx" ? extension : { kind: "builtin", id } as const;
    await act(async () => root.render(<IntegrationDetail target={target} />));
    await flush();
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    mockFetch.mockImplementation((url, init) => {
      const path = String(url);
      if (/\/connectors(\?|$)/.test(path)) {
        return Promise.resolve(new Response(JSON.stringify({ error: "Connector read failed" }), { status: 500 }));
      }
      if (path === "/api/v1/channels/larkx_chat" || path === "/api/v1/channels/feishu") {
        return Promise.resolve(new Response(JSON.stringify({ error: "Channel read failed" }), { status: 500 }));
      }
      return fallback(url, init);
    });
    await act(async () => refresh());
    expect(section("connection")?.querySelector('[role="alert"]')?.textContent).toContain("Connector read failed");
    if (id !== "github") {
      expect(section("channel")?.querySelector('[role="alert"]')?.textContent).toContain("Channel read failed");
    }
    if (id === "larkx") expect(section("channel")?.querySelector("[data-channel-routing]")).not.toBeNull();
    mockFetch.mockImplementation(fallback);
    await act(async () => refresh());
    expect(section("connection")?.querySelector('[role="alert"]')).toBeNull();
    expect(section("channel")?.querySelector('[role="alert"]') ?? null).toBeNull();
  });

  function finish(kind: ExtensionOperation["kind"], manager?: string) {
    const now = new Date().toISOString();
    track({ id: `operation-${kind}`, kind, extension: extension.id, state: "succeeded", started_at: now,
      updated_at: now, steps: [], retryable: false }, manager);
  }

  it("starts the requested market sign-in once and consumes the marker", async () => {
    askSignIn(extension.id);
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    expect(signInAsked(extension.id)).toBe(false);
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    expect(requests.filter((r) => r.url.endsWith("/auth/step"))).toHaveLength(1);
    act(() => root.render(null));
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    expect(document.querySelector('[role="dialog"]')).toBeNull();
  });

  it("keeps upper-section edits through navigation, refresh and external upgrades until reverted", async () => {
    const onUninstalled = vi.fn();
    const page = <IntegrationDetail target={extension} onUninstalled={onUninstalled} />;
    await act(async () => root.render(page));
    act(() => setInputValue(input("endpoint"), "https://edited.example.com"));
    const bar = host.querySelector('[data-settings-group="integration-save-bar"]')!;
    expect(bar.closest("[data-integration-section]")).toBeNull();
    expect(unloadWarns()).toBe(true);
    await act(async () => root.render(<IntegrationDetail target={{ kind: "builtin", id: "mail" }} />));
    expect(unloadWarns()).toBe(true);
    currentExt.config = { ...currentExt.config, display_alias: "Updated elsewhere" };
    await act(async () => root.render(page));
    expect(input("endpoint").value).toBe("https://edited.example.com");
    expect(input("display_alias").value).toBe("Updated elsewhere");
    currentExt.config.display_alias = "Refreshed";
    await act(async () => refresh());
    expect(input("endpoint").value).toBe("https://edited.example.com");
    expect(input("display_alias").value).toBe("Refreshed");
    currentExt.name = "Upgraded";
    currentExt.config.display_alias = "After upgrade";
    await act(async () => finish("upgrade", "work"));
    expect(host.querySelector("h1")!.textContent).not.toBe("Upgraded");
    await act(async () => finish("upgrade"));
    expect(host.querySelector("h1")!.textContent).toBe("Upgraded");
    expect(input("display_alias").value).toBe("After upgrade");
    expect(input("endpoint").value).toBe("https://edited.example.com");
    const revert = [...host.querySelectorAll("button")].find((b) => b.textContent === t("settings-extensions.revert"))!;
    act(() => revert.click());
    expect(input("endpoint").value).toBe(currentExt.config.endpoint);
    expect(unloadWarns()).toBe(false);
    act(() => setInputValue(input("endpoint"), "https://other.example.com"));
    await act(async () => finish("uninstall"));
    expect(onUninstalled).toHaveBeenCalledTimes(1);
    expect(unloadWarns()).toBe(false);
  });

  it.each(["builtin", "extension"] as const)("polls a starting %s connector until sign-in is available, for at most 15s", async (kind) => {
    vi.useFakeTimers();
    try {
      const target = kind === "builtin" ? { kind, id: "larkx_conn" } : extension;
      const c = { ...COMPOSITE_CONNECTOR, account: "", status: "not_signed_in", capabilities: [] as string[] };
      currentConnectors = [c];
      const draw = () => act(async () => root.render(<IntegrationDetail target={target} />));
      const reads = () => requests.filter((r) => r.url.endsWith("/connectors")).length;
      const tick = () => act(async () => { await vi.advanceTimersByTimeAsync(1000); });
      await draw();
      expect(host.textContent).toContain(t("settings-hubconnector.starting", { name: c.name }));
      const initial = reads();
      for (let second = 0; second < 17; second++) await tick();
      expect(reads() - initial).toBe(15);
      act(() => root.render(null));
      await draw();
      c.capabilities = ["login"];
      await tick();
      expect(host.textContent).not.toContain(t("settings-hubconnector.starting", { name: c.name }));
      expect(section("connection")!.textContent).toContain(t("settings-hubconnector.sign_in_to", { name: c.name }));
      const ready = reads();
      await tick();
      expect(reads()).toBe(ready);
    } finally {
      vi.useRealTimers();
    }
  });

  it.each(["apps", "voice", "feishu-mention"])("omits the empty connection section on read-only %s", async (id) => {
    currentChannels[id] = { ...COMPOSITE_CHANNEL, id, source: "builtin", kind: id === "apps" ? "app" : "mention", read_only: true };
    await act(async () => root.render(<IntegrationDetail target={{ kind: "builtin", id, channelId: id, connectorId: null }} />));
    await act(async () => vi.dynamicImportSettled());
    expect(section("channel")).not.toBeNull();
    expect(section("connection")).toBeNull();
    if (id === "apps") expect(requests.some((r) => r.url.includes("provider=apps"))).toBe(false);
  });

  it.each(["builtin", "extension"] as const)("omits the inbox heading for a %s connector with no feeds or subscriptions", async (kind) => {
    currentConnectors = [{ id: "plain", name: "Plain", status: "ok", capabilities: ["read"] }];
    currentExt = { ...currentExt, id: "plain", contributes: { connectors: ["plain"] }, form: undefined };
    await act(async () => root.render(<IntegrationDetail target={{ kind, id: "plain" }} />));
    expect(section("connection")).not.toBeNull();
    expect(section("inbox")).toBeNull();
  });

  it.each(["builtin", "extension"] as const)("keeps a subscription-only %s section actionable with a single summary read", async (kind) => {
    applyCompleteSessions([SEED_SESSION], [], { features: ["connectors", "source_subscriptions"] });
    currentConnectors = [{ id: "plain", name: "Plain", status: "ok", capabilities: ["read"] }];
    currentExt = { ...currentExt, id: "plain", contributes: { connectors: ["plain"] }, form: undefined };
    const fetch = vi.mocked(window.fetch);
    const fallback = fetch.getMockImplementation()!;
    fetch.mockImplementation((url, init) => String(url).endsWith("/subscriptions/summary")
      ? Promise.resolve(ok({ connectors: [{ connector: "plain", extension: "plain", on: 1, failing: 0 }] }) as Response)
      : fallback(url, init));
    const onOpen = vi.fn();
    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind, id: "plain" }} onOpenSources={onOpen} />));
    expect(fetch.mock.calls.filter(([url]) => String(url).endsWith("/subscriptions/summary"))).toHaveLength(1);
    const button = section("inbox")!.querySelector("button")!;
    expect(button.textContent).toContain(t("subscriptions.row"));
    act(() => button.click());
    expect(onOpen).toHaveBeenCalledWith(JSON.stringify(kind === "builtin" ? { c: "plain" } : { ext: "plain" }), undefined);
  });

  it.each(["builtin", "extension"] as const)("shows the named assistant's subscriptions in %s detail", async (kind) => {
    applyCompleteSessions([SEED_SESSION], [], { features: ["connectors", "source_subscriptions"] });
    currentConnectors = [{ id: "plain", name: "Plain", status: "ok", capabilities: ["read"], manager: "work" }];
    currentExt = { ...currentExt, id: "plain", contributes: { connectors: ["plain"] }, form: undefined };
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    mockFetch.mockImplementation((url, init) => {
      if (!String(url).endsWith("/subscriptions/summary")) return fallback(url, init);
      const owner = new Headers(init?.headers).get("X-Persona-Manager");
      return Promise.resolve(ok({ connectors: owner === "work" ? [{ connector: "plain", extension: "plain", on: 3, failing: 1 }] : [] }) as Response);
    });
    const onOpen = vi.fn();
    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind, id: "plain", manager: "work" }} onOpenSources={onOpen} />));
    const button = section("inbox")?.querySelector("button");
    expect(button?.textContent ?? "").toContain(t("subscriptions.counts_failing", { on: 3, failing: 1 }));
    act(() => button!.click());
    expect(onOpen).toHaveBeenCalledWith(JSON.stringify(kind === "builtin" ? { c: "plain" } : { ext: "plain" }), "work");
  });

  it.skipIf(!installedMailbox)("refreshes mail accounts and only groups an available inbox switch", async () => {
    await preloadMailbox("Settings");
    await preloadMailbox("InboxSwitch");
    currentMailAccounts = [];
    await act(async () => root.render(<IntegrationDetail target={{ kind: "builtin", id: "mail" }} />));
    expect(requests.some((r) => r.url.includes("/connectors?all=1"))).toBe(false);
    expect(section("inbox")!.querySelectorAll("section")).toHaveLength(1); // Knowledge scope shortcut
    currentMailAccounts.push({ id: "added", email: "added@example.com", provider: "imap", status: "ok" });
    currentConnectors = [];
    await act(async () => refresh());
    expect(section("connection")!.textContent).toContain("added@example.com");
    expect(section("inbox")!.querySelectorAll("section")).toHaveLength(1); // Older hub lacks the switch
    currentConnectors.push({ id: "mail", name: "Mail", status: "ok", inbox: true });
    await act(async () => refresh());
    expect(section("inbox")!.querySelector('[role="switch"]')).not.toBeNull();
  });

  it("refreshes both Lark gateway reads from the detail header", async () => {
    await act(async () => root.render(<IntegrationDetail target={{ kind: "builtin", id: "lark" }} />));
    requests.length = 0;
    await act(async () => refresh());
    expect(requests.some((r) => r.url.endsWith("/lark/status"))).toBe(true);
    expect(requests.some((r) => r.url.endsWith("/lark/accounts"))).toBe(true);
  });

  it("loads external settings for every contributed channel", async () => {
    currentExt.contributes!.channel = ["larkx_chat", "second_chat"];
    await act(async () => root.render(<IntegrationDetail target={extension} />));
    await act(async () => vi.dynamicImportSettled());
    for (const id of ["larkx_chat", "second_chat", "larkx_conn"]) {
      expect(requests.some((r) => r.url.includes(`provider=${id}`)), id).toBe(true);
    }
  });
});

describe("extension auth-to-connector sync and host-auth contract (#17803)", () => {
  const emitHubEvent = (type: string, msg: Record<string, unknown>) => {
    hubEvents.listeners.get(type)?.forEach((listener) => listener({ type, ...msg }));
  };

  function setupHostAuthExtension(manager?: string) {
    currentExt = {
      id: "storefront",
      name: "Storefront",
      kind: "process",
      source: "installed",
      status: "running",
      version: "0.3.1",
      enabled: true,
      origin: "market:official/storefront",
      contributes: { connectors: ["storefront_conn"] },
      auth: {
        title: "Storefront Sign-in",
        state: "signed_out",
      },
      form: {
        fields: [{ name: "region", type: "string", title: "Region" }],
      },
      config: { region: "us" },
    };
    currentConnectors = [
      {
        id: "storefront_conn",
        name: "Storefront Library",
        status: "not_signed_in",
        enabled: true,
        capabilities: ["read"],
        ...(manager ? { manager } : {}),
      },
    ];
  }

  it("refreshes associated connector status after AuthGroup sign-in, preserves unsaved drafts, and hides hub_side_note", async () => {
    setupHostAuthExtension();
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    let extAuth = structuredClone(currentExt.auth!);
    mockFetch.mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/-/auth/step") && method === "POST") {
        extAuth = { title: "Storefront Sign-in", state: "signed_in", account: "player1" };
        currentConnectors = [
          {
            id: "storefront_conn",
            name: "Storefront Library",
            status: "ok",
            account: "player1",
            enabled: true,
            capabilities: ["read"],
          },
        ];
        return ok({ done: true, account: "player1" }) as Response;
      }
      if (url.endsWith("/-/auth") && method === "GET") {
        return ok(extAuth) as Response;
      }
      return fallback(input, init);
    });

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "storefront" }} />));
    await flush();

    const connSection = () => section("connection")!;
    expect(connSection().textContent).toContain(t("lib-extension-auth.signed_out"));
    expect(connSection().textContent).toContain(t("lib-connectors.state_not_signed_in"));
    // Top-level interactive AuthGroup is rendered -> suppress misleading manager.json note.
    expect(connSection().textContent).not.toContain(t("settings-hubconnector.hub_side_note"));

    // Create an unsaved config draft before signing in.
    const regionInput = host.querySelector<HTMLInputElement>("#extension-field-region")!;
    act(() => setInputValue(regionInput, "jp"));
    await flush();
    expect(regionInput.value).toBe("jp");

    const signInBtn = [...connSection().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("lib-extension-auth.sign_in"),
    )!;
    expect(signInBtn).toBeDefined();
    await act(async () => signInBtn.click());
    await flush();

    // Both top AuthGroup and lower Connection status card reflect signed-in state.
    expect(connSection().textContent).toContain(t("lib-extension-auth.signed_in_as", { account: "player1" }));
    expect(connSection().textContent).toContain(t("lib-connectors.state_ok"));
    expect(connSection().textContent).not.toContain(t("lib-connectors.state_not_signed_in"));
    expect(connSection().textContent).not.toContain(t("settings-hubconnector.hub_side_note"));
    // Unsaved draft stays intact.
    expect(host.querySelector<HTMLInputElement>("#extension-field-region")!.value).toBe("jp");
  });

  it("syncs connector status on sign-out under secondary assistant scope and ignores other assistant auth pushes", async () => {
    setupHostAuthExtension("work");
    currentExt.auth = { title: "Storefront Sign-in", state: "signed_in", account: "work-player" };
    currentConnectors = [
      { id: "storefront_conn", name: "Storefront Library", status: "ok", account: "default-player", enabled: true, capabilities: ["read"] },
      { id: "storefront_conn", name: "Storefront Library", status: "ok", account: "work-player", enabled: true, capabilities: ["read"], manager: "work" },
    ];
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    mockFetch.mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/-/auth/logout") && method === "POST") {
        const manager = new Headers(init?.headers).get("X-Persona-Manager");
        requests.push({ url, method, body: undefined, manager });
        currentConnectors = [
          currentConnectors[0],
          { id: "storefront_conn", name: "Storefront Library", status: "not_signed_in", enabled: true, capabilities: ["read"], manager: "work" },
        ];
        return ok({ title: "Storefront Sign-in", state: "signed_out" }) as Response;
      }
      return fallback(input, init);
    });

    await act(async () => root.render(<IntegrationDetail integrationsV2 target={{ kind: "extension", id: "storefront", manager: "work" }} />));
    await flush();

    const connSection = () => section("connection")!;
    expect(connSection().textContent).toContain("work-player");
    expect(connSection().textContent).toContain(t("lib-connectors.state_ok"));

    // An extension_auth event from another assistant must not touch work's auth or reload connectors.
    const connectorReadsBefore = requests.filter((r) => /\/connectors(\?|$)/.test(r.url)).length;
    await act(async () => {
      emitHubEvent("extension_auth", {
        extension: "storefront",
        manager: "assistant",
        auth: { title: "Storefront Sign-in", state: "signed_out" },
      });
    });
    await flush();
    expect(connSection().textContent).toContain("work-player");
    expect(requests.filter((r) => /\/connectors(\?|$)/.test(r.url)).length).toBe(connectorReadsBefore);

    // Signing out routes with X-Persona-Manager: work and refreshes work's connector snapshot.
    const signOutBtn = [...connSection().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("settings-extensionauth.sign_out"),
    )!;
    expect(signOutBtn).toBeDefined();
    await act(async () => signOutBtn.click());
    await flush();

    expect(requests).toContainEqual(expect.objectContaining({ url: expect.stringContaining("/-/auth/logout"), method: "POST", manager: "work" }));
    expect(connSection().textContent).toContain(t("lib-extension-auth.signed_out"));
    expect(connSection().textContent).toContain(t("lib-connectors.state_not_signed_in"));
    expect(connSection().textContent).not.toContain("default-player");
  });

  it("guards against stale out-of-order connector responses across rapid auth transitions", async () => {
    setupHostAuthExtension();
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    let intercept = false;
    let resolveFirst!: (r: Response) => void;
    let resolveSecond!: (r: Response) => void;
    let call = 0;

    mockFetch.mockImplementation((input, init) => {
      const url = String(input);
      if (intercept && /\/connectors(\?|$)/.test(url)) {
        call++;
        if (call === 1) return new Promise<Response>((resolve) => { resolveFirst = resolve; });
        if (call === 2) return new Promise<Response>((resolve) => { resolveSecond = resolve; });
      }
      return fallback(input, init);
    });

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "storefront" }} />));
    await flush();
    intercept = true;

    // Transition 1: signed_in (slow response #1).
    await act(async () => {
      emitHubEvent("extension_auth", {
        extension: "storefront",
        auth: { title: "Storefront Sign-in", state: "signed_in", account: "player1" },
      });
    });
    // Transition 2: expired (faster response #2).
    await act(async () => {
      emitHubEvent("extension_auth", {
        extension: "storefront",
        auth: { title: "Storefront Sign-in", state: "expired" },
      });
    });

    expect(call).toBe(2);
    // Newer response #2 lands first -> not_signed_in.
    await act(async () => {
      resolveSecond(ok({
        connectors: [{ id: "storefront_conn", name: "Storefront Library", status: "not_signed_in", enabled: true, capabilities: ["read"] }],
      }) as Response);
    });
    await flush();
    expect(section("connection")!.textContent).toContain(t("lib-connectors.state_not_signed_in"));

    // Stale response #1 resolves later -> must be ignored.
    await act(async () => {
      resolveFirst(ok({
        connectors: [{ id: "storefront_conn", name: "Storefront Library", status: "ok", account: "player1", enabled: true, capabilities: ["read"] }],
      }) as Response);
    });
    await flush();

    expect(section("connection")!.textContent).toContain(t("lib-extension-auth.expired"));
    expect(section("connection")!.textContent).toContain(t("lib-connectors.state_not_signed_in"));
    expect(section("connection")!.textContent).not.toContain("player1");
  });

  it("drops a stale getAuth response when a newer auth state arrives first", async () => {
    setupHostAuthExtension();
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    let resolveStaleAuth!: (r: Response) => void;

    mockFetch.mockImplementation((input, init) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/-/auth/step") && method === "POST") {
        return Promise.resolve(ok({ done: true, account: "player1" }) as Response);
      }
      if (url.endsWith("/-/auth") && method === "GET") {
        return new Promise<Response>((resolve) => { resolveStaleAuth = resolve; });
      }
      return fallback(input, init);
    });

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "storefront" }} />));
    await flush();

    // Start sign-in (triggers deferred GET /-/auth).
    const signInBtn = [...section("connection")!.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("lib-extension-auth.sign_in"),
    )!;
    await act(async () => signInBtn.click());
    await flush();

    // A newer pushed status arrives before the deferred GET /-/auth resolves.
    await act(async () => {
      emitHubEvent("extension_auth", {
        extension: "storefront",
        auth: { title: "Storefront Sign-in", state: "expired" },
      });
    });
    await flush();
    expect(section("connection")!.textContent).toContain(t("lib-extension-auth.expired"));

    // Resolve the older GET /-/auth -> must be ignored.
    await act(async () => {
      resolveStaleAuth(ok({ title: "Storefront Sign-in", state: "signed_in", account: "player1" }) as Response);
    });
    await flush();
    expect(section("connection")!.textContent).toContain(t("lib-extension-auth.expired"));
    expect(section("connection")!.textContent).not.toContain("player1");
  });

  it("preserves updated detail.auth on transient connector reload failure and recovers via retry", async () => {
    setupHostAuthExtension();
    const mockFetch = vi.mocked(window.fetch);
    const fallback = mockFetch.getMockImplementation()!;
    let failConnectors = false;
    let extAuth = structuredClone(currentExt.auth!);

    mockFetch.mockImplementation(async (input, init) => {
      const url = String(input);
      const method = (init?.method || "GET").toUpperCase();
      if (url.endsWith("/-/auth/step") && method === "POST") {
        extAuth = { title: "Storefront Sign-in", state: "signed_in", account: "player1" };
        currentConnectors = [
          { id: "storefront_conn", name: "Storefront Library", status: "ok", account: "player1", enabled: true, capabilities: ["read"] },
        ];
        return ok({ done: true, account: "player1" }) as Response;
      }
      if (url.endsWith("/-/auth") && method === "GET") {
        return ok(extAuth) as Response;
      }
      if (failConnectors && /\/connectors(\?|$)/.test(url)) {
        return new Response(JSON.stringify({ error: "Connector reload timed out" }), { status: 503 });
      }
      return fallback(input, init);
    });

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "storefront" }} />));
    await flush();

    failConnectors = true;
    const signInBtn = [...section("connection")!.querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("lib-extension-auth.sign_in"),
    )!;
    await act(async () => signInBtn.click());
    await flush();

    // Auth card stays updated even though connector reload failed.
    const connSection = () => section("connection")!;
    expect(connSection().textContent).toContain(t("lib-extension-auth.signed_in_as", { account: "player1" }));
    expect(connSection().querySelector('[role="alert"]')?.textContent).toContain("Connector reload timed out");

    // Retry button inside the error card recovers the connector status cleanly.
    failConnectors = false;
    const retryBtn = [...connSection().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === t("settings-hubconnector.recheck"),
    )!;
    expect(retryBtn).toBeDefined();
    await act(async () => retryBtn.click());
    await flush();

    expect(connSection().querySelector('[role="alert"]')).toBeNull();
    expect(connSection().textContent).toContain(t("lib-connectors.state_ok"));
    expect(connSection().textContent).toContain("player1");
  });

  it("keeps hub_side_note visible for hub-configured connectors when no top-level interactive auth is present", async () => {
    currentExt = {
      id: "hub-managed-ext",
      name: "Hub Managed Extension",
      kind: "process",
      source: "installed",
      status: "running",
      version: "1.0.0",
      enabled: true,
      origin: "market:official/hub-managed-ext",
      contributes: { connectors: ["hub_managed_conn"] },
    };
    currentConnectors = [
      { id: "hub_managed_conn", name: "Hub Managed", status: "not_signed_in", enabled: true, capabilities: ["read"] },
    ];

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "hub-managed-ext" }} />));
    await flush();
    expect(section("connection")!.textContent).toContain(t("settings-hubconnector.hub_side_note"));

    // Standalone built-in hub-configured connector also keeps hub_side_note.
    act(() => root.render(null));
    await act(async () => root.render(
      <IntegrationDetail target={{ kind: "builtin", id: "hub_managed_conn", connectorId: "hub_managed_conn" }} />,
    ));
    await flush();
    expect(section("connection")!.textContent).toContain(t("settings-hubconnector.hub_side_note"));
  });
});

describe("unified capability detail shell (#17762)", () => {
  const slotIds = () =>
    [...host.querySelectorAll<HTMLElement>("[data-integration-slot]")].map((el) => el.getAttribute("data-integration-slot") || "");

  it("mounts Feishu Bot external sources strictly after Inbox & Sync in both legacy and projected views", async () => {
    currentConnectors.push({
      id: "larkcli",
      name: "Lark CLI",
      status: "ok",
      enabled: true,
      archive: true,
      inbox: true,
      capabilities: ["login", "inbox", "archive"],
    });

    await act(async () => root.render(<IntegrationDetail target={{ kind: "builtin", id: "feishu-bot" }} />));
    await flush();

    expect(slotIds()).toEqual(["connection", "conversation_and_push", "inbox_and_sync", "external_sources"]);
    const inboxEl = section("inbox")!;
    const externalSlot = host.querySelector<HTMLElement>('[data-integration-slot="external_sources"]')!;
    expect(inboxEl.compareDocumentPosition(externalSlot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

    // Projected Feishu Bot also mounts external_sources immediately after inbox_and_sync.
    const ref = { manager: "assistant", kind: "service" as const, id: "feishu-bot" };
    const projection = {
      detail: {
        item: {
          ref,
          name: "Feishu Bot",
          origin: "builtin_service" as const,
          capabilities: ["channels" as const, "data" as const],
          availability: { state: "available" as const },
        },
        availability: [],
        sections: [
          { kind: "connection" as const, ref, availability: { state: "available" as const }, edit: "settings" as const },
          { kind: "conversation_and_push" as const, ref: { manager: "assistant", kind: "channel" as const, id: "feishu" }, availability: { state: "available" as const }, edit: "settings" as const },
          { kind: "inbox_and_sync" as const, ref: { manager: "assistant", kind: "connector" as const, id: "larkcli" }, availability: { state: "available" as const }, edit: "settings" as const },
          { kind: "external_sources" as const, ref: { manager: "assistant", kind: "channel" as const, id: "feishu" }, availability: { state: "available" as const }, edit: "settings" as const },
        ],
      },
      refresh: vi.fn(),
      loading: false,
    };

    act(() => root.render(null));
    await act(async () => root.render(
      <IntegrationDetail
        target={{ kind: "builtin", id: "feishu-bot", channelId: "feishu", connectorId: "larkcli", manager: "assistant" }}
        projection={projection as never}
      />,
    ));
    await flush();

    expect(slotIds()).toEqual(["connection", "conversation_and_push", "inbox_and_sync", "external_sources"]);
    const projInbox = section("inbox")!;
    const projExternal = host.querySelector<HTMLElement>('[data-integration-slot="external_sources"]')!;
    expect(projInbox.compareDocumentPosition(projExternal) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("mounts all seven canonical capability slots in order and places unknown configuration.section fields in general settings", async () => {
    currentExt = {
      ...structuredClone(COMPOSITE_EXTENSION),
      form: {
        fields: [
          ...(COMPOSITE_EXTENSION.form?.fields ?? []),
          { name: "future_option", type: "string", title: "Future Option", section: "future_slot" as never },
        ],
      },
      config: {
        ...COMPOSITE_EXTENSION.config,
        future_option: "fallback-val",
      },
    };

    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} />));
    await flush();

    expect(slotIds()).toEqual([
      "connection",
      "conversation_and_push",
      "inbox_and_sync",
      "external_sources",
      "extension_configuration",
      "information_access",
      "lifecycle",
    ]);
    expect(section("extension-settings")!.querySelector('[data-field="future_option"]')).not.toBeNull();
    expect(host.querySelector('[data-integration-detail="larkx"]')?.getAttribute("data-integration-resource")).toBe(
      JSON.stringify(["", "extension", "larkx"]),
    );
  });

  it("renders consistent origin badges for built-in extensions, standalone channels, and standalone connectors", async () => {
    currentExt = { ...structuredClone(COMPOSITE_EXTENSION), source: "builtin" };
    await act(async () => root.render(<IntegrationDetail target={{ kind: "extension", id: "larkx" }} />));
    await flush();
    expect(host.querySelector("h1")?.closest("section")?.textContent).toContain(t("settings-integrations.builtin_badge"));

    const standaloneChannelProjection = {
      detail: {
        item: {
          ref: { manager: "assistant", kind: "channel", id: "voice" },
          name: "Voice",
          origin: "standalone_channel",
          capabilities: ["channels"],
          availability: { state: "available" },
        },
        availability: [],
        sections: [
          { kind: "conversation_and_push", ref: { manager: "assistant", kind: "channel", id: "voice" }, availability: { state: "available" }, edit: "settings" },
        ],
      },
      refresh: vi.fn(),
      loading: false,
    };
    currentChannels.voice = { ...FEISHU_CHANNEL, id: "voice", name: "Voice", kind: "voice", read_only: true };
    act(() => root.render(null));
    await act(async () => root.render(
      <IntegrationDetail
        target={{ kind: "builtin", id: "voice", channelId: "voice", connectorId: null, manager: "assistant" }}
        projection={standaloneChannelProjection as never}
      />,
    ));
    await flush();
    expect(host.querySelector("h1")?.closest("section")?.textContent).toContain(t("settings-integrations.cap_channels"));
    expect(host.querySelector('[data-integration-detail="voice"]')?.getAttribute("data-integration-resource")).toBe(
      JSON.stringify(["assistant", "channel", "voice"]),
    );
  });

  it("preserves the mounted detail instance, open Advanced disclosure, and unsaved drafts across the 740px breakpoint", async () => {
    vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
    const { Settings } = await import("../Settings");
    let width = 820;
    let resize!: () => void;
    const measure = vi.spyOn(Element.prototype, "clientWidth", "get").mockImplementation(() => width);
    vi.stubGlobal("ResizeObserver", class {
      constructor(private notify: () => void) {}
      observe(element: Element) { if (element.matches('[role="region"]')) resize = this.notify; }
      disconnect() {}
    });
    const original = location.href;
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    try {
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      await act(async () => vi.dynamicImportSettled());
      await flush();

      const detailEl = host.querySelector<HTMLElement>('[data-integration-detail="larkx"]')!;
      expect(detailEl).not.toBeNull();
      const resourceBefore = detailEl.getAttribute("data-integration-resource");

      // Open Advanced disclosure and edit fields before crossing the 740px breakpoint.
      const advButton = section("extension-settings")!.querySelector<HTMLButtonElement>("[data-integration-advanced] button")!;
      await act(async () => advButton.click());
      expect(advButton.getAttribute("aria-expanded")).toBe("true");
      const endpointInput = host.querySelector<HTMLInputElement>("#extension-field-endpoint")!;
      await act(async () => setInputValue(endpointInput, "https://stable-across-breakpoint.example.com"));

      const readsBefore = requests.filter((r) => r.method === "GET" && r.url.includes("/larkx/")).length;
      for (width of [739, 740, 680, 900]) {
        await act(async () => resize());
        expect(!!host.querySelector("[data-integrations-split]")).toBe(width >= 740);
        expect(host.querySelector('[data-integration-detail="larkx"]')).toBe(detailEl);
        expect(detailEl.getAttribute("data-integration-resource")).toBe(resourceBefore);
        expect(section("extension-settings")!.querySelector<HTMLButtonElement>("[data-integration-advanced] button")?.getAttribute("aria-expanded")).toBe("true");
        expect(host.querySelector<HTMLInputElement>("#extension-field-endpoint")?.value).toBe("https://stable-across-breakpoint.example.com");
      }
      expect(requests.filter((r) => r.method === "GET" && r.url.includes("/larkx/")).length).toBe(readsBefore);
    } finally {
      measure.mockRestore();
      history.replaceState(null, "", original);
    }
  }, 30000);

  it("exposes an explicit Switch assistant button on cross-assistant details without silently switching", async () => {
    const onSwitchAssistant = vi.fn();
    await act(async () => root.render(
      <IntegrationDetail
        integrationsV2
        target={{ kind: "extension", id: "larkx", manager: "work" }}
        onSwitchAssistant={onSwitchAssistant}
      />,
    ));
    await flush();

    expect(host.querySelector('[data-owner-mark="work"]')).not.toBeNull();
    const switchBtn = host.querySelector<HTMLButtonElement>('[data-testid="integration-detail-switch-assistant"]')!;
    expect(switchBtn).not.toBeNull();
    expect(switchBtn.textContent).toContain(t("settings-integrations.switch_assistant_integrations", { name: "Work" }));
    expect(chosenManager()).toBeNull();
    act(() => switchBtn.click());
    expect(onSwitchAssistant).toHaveBeenCalledOnce();
  });
});
