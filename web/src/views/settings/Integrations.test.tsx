// @vitest-environment jsdom
// Unit & integration tests for the lazy-loaded unified Integrations & Extensions list view,
// object deduplication, capability filter chips, Extension Market entry, IntegrationDetail mounting,
// bidirectional legacy route redirects, and Settings navigation collapse under integrations_v2 (#14874).
import { registerTestMailbox } from "../../test/mailbox-fixture";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setPairedToken } from "../../lib/api";

const state = vi.hoisted(() => ({
  phone: false,
  twoCol: true,
  integrationsV2: true,
  features: ["channels", "extension_manager", "connectors", "lark"] as string[],
  loaded: true,
  assistant: "assistant",
}));

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({
    get matches() {
      return /min-width/.test(query) ? state.twoCol && !state.phone : state.phone;
    },
    media: query,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as typeof window.matchMedia;
});

vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  useStore: (select: (s: Record<string, unknown>) => unknown) =>
    select({
      features: state.features,
      todos: [],
      machines: [],
      sessions: {},
      interactions: [],
      mutes: { spaces: [], sessions: [] },
      conn: "online",
      link: { networkUp: true, restarting: false },
      hubName: "",
      mesh: [],
      loaded: state.loaded,
      assistant: state.loaded ? { manager: state.assistant, integrations_v2: state.integrationsV2 } : null,
    }),
  useLarkOn: () => state.features.includes("lark"),
  onHubEvent: () => () => {},
}));

vi.mock("./IntegrationDetail", () => ({
  IntegrationDetail: ({
    target,
    prefill,
    onBack,
    onUpgrade,
    onPage,
    onSettings,
    onOpenSources,
  }: {
    target: { kind: string; id: string; channelId?: string; connectorId?: string | null; manager?: string };
    prefill?: Record<string, unknown>;
    onBack?: () => void;
    onUpgrade?: (market: string, id: string, manager?: string) => void;
    onPage?: (extId: string, uiId: string, manager?: string) => void;
    onSettings?: (url: string) => void;
    onOpenSources?: (state?: string, manager?: string) => void;
  }) => (
    <div
      data-mock-integration-detail
      data-kind={target.kind}
      data-id={target.id}
      data-channel-id={target.channelId ?? ""}
      data-connector-id={target.connectorId ?? ""}
      data-manager={target.manager ?? ""}
      data-prefill={prefill && JSON.stringify(prefill)}
    >
      {onBack && (
        <button type="button" data-mock-detail-back onClick={onBack}>
          Back
        </button>
      )}
      {onUpgrade && (
        <button type="button" data-mock-detail-upgrade onClick={() => onUpgrade("official", target.id, target.manager)}>
          Upgrade
        </button>
      )}
      {onPage && (
        <button type="button" data-mock-detail-page onClick={() => onPage(target.id, "dashboard", target.manager)}>
          Open page
        </button>
      )}
      {onOpenSources && (
        <button
          type="button"
          data-mock-detail-sources
          onClick={() => onOpenSources(JSON.stringify({ p: "source", id: "sub_push_iphone" }), target.manager)}
        >
          Sources
        </button>
      )}
      {onSettings && (
        <>
          <button type="button" data-mock-detail-repos onClick={() => onSettings("settings/persona#repositories")}>
            Repos
          </button>
          <button type="button" data-mock-detail-kb onClick={() => onSettings("settings/knowledge#knowledge-items")}>
            Knowledge
          </button>
        </>
      )}
    </div>
  ),
}));

vi.mock("./ExtensionMarket", () => ({
  MarketCatalog: ({
    manager,
    onBack,
    backLabel,
    onOpen,
    onMarkets,
    onManageMarkets,
  }: {
    manager?: string;
    onBack?: () => void;
    backLabel?: string;
    onOpen: (it: { market: string; id: string }) => void;
    onMarkets?: () => void;
    onManageMarkets?: () => void;
  }) => {
    const manage = onManageMarkets ?? onMarkets;
    return (
      <div data-mock-market-catalog data-back-label={backLabel ?? ""} data-manager={manager}>
        {onBack && <button type="button" data-mock-market-back onClick={onBack}>Back</button>}
        <button type="button" data-mock-market-open onClick={() => onOpen({ market: "official", id: "demo-ext" })}>Open item</button>
        {manage && <button type="button" data-mock-markets-open onClick={manage}>Manage markets</button>}
      </div>
    );
  },
  MarketItem: ({ market, id, onBack, onInstalled }: { market: string; id: string; onBack?: () => void; onInstalled: (id: string) => void }) => (
    <div data-mock-market-item data-market={market} data-id={id}>
      {onBack && <button type="button" data-mock-item-back onClick={onBack}>Back</button>}
      <button type="button" data-mock-item-install onClick={() => onInstalled(id)}>Install</button>
    </div>
  ),
  Markets: ({ onBack }: { onBack?: () => void }) => (
    <div data-mock-markets>
      {onBack && <button type="button" data-mock-markets-back onClick={onBack}>Back</button>}
    </div>
  ),
}));

vi.mock("./SentSettings", () => ({
  SentSettings: () => <div data-mock-sent-settings />,
}));

const extViewMock = vi.hoisted(() => ({ onWide: undefined as ((wide: boolean) => void) | undefined }));
vi.mock("./ExtensionView", async (original) => {
  const { ExtensionUI } = await original<typeof import("./ExtensionView")>();
  return {
    ExtensionUI: (props: Parameters<typeof ExtensionUI>[0]) => {
      const { extension, ui, state: uiState, onWide } = props;
      extViewMock.onWide = onWide;
      return (
        <div data-mock-extension-ui data-extension={extension} data-ui={ui} data-state={uiState ?? ""}>
          {extension === "-subscriptions" && <ExtensionUI {...props} />}
        </div>
      );
    },
    askAssistant: vi.fn(),
    LauncherExtensionSections: () => null,
  };
});

vi.mock("./ShortcutSettings", () => ({ ShortcutSettings: () => <div data-page="shortcuts" /> }));
vi.mock("./NotificationSettings", () => ({ NotificationSettings: () => <div data-page="notifications" /> }));
vi.mock("./ModelConfiguration", () => ({ ModelConfiguration: () => <div data-page="model-config" /> }));
vi.mock("./KnowledgeSelection", () => ({ KnowledgeSelection: () => <div data-page="knowledge-selection" /> }));
vi.mock("./TranslationSettings", () => ({ TranslationSettings: () => <div data-page="translation" /> }));
vi.mock("./AssistantSettings", () => ({ AssistantSettings: () => <div data-page="persona" /> }));
vi.mock("./SidebarPages", () => ({ SidebarPagesGroup: () => <div data-page="sidebar-pages" /> }));
vi.mock("./StartPage", () => ({ StartPageGroup: () => <div data-page="start-page" /> }));
vi.mock("./AgentSettings", () => ({ AgentSettings: () => <div data-page="agent" /> }));
vi.mock("./Extensions", () => ({
  Extensions: ({ open }: { open: string | null }) => <div data-page="legacy-extensions" data-open={open ?? ""} />,
}));
vi.mock("./Connectors", () => ({
  Connectors: ({ open }: { open: string | null }) => <div data-page="legacy-connectors" data-open={open ?? ""} />,
}));
vi.mock("./Channels", () => ({
  ChannelList: () => <div data-page="legacy-channels-list" />,
  ChannelPage: ({ id, manager }: { id: string; manager?: string }) => (
    <div data-page="legacy-channel-detail" data-id={id} data-manager={manager ?? ""} />
  ),
}));
vi.mock("./HubSettings", () => ({ HubSettings: () => <div data-page="hub" /> }));
vi.mock("./AgentGroup", () => ({ AgentGroup: () => <div data-page="agent-group" /> }));
vi.mock("../../lib/pwa", async (original) => ({ ...(await original<typeof import("../../lib/pwa")>()), useInstall: () => "installed" }));

import { initI18n, t } from "../../i18n";
import type { Channel } from "../../lib/channels";
import type { HubConnector } from "../../lib/connectors";
import type { HubExtension } from "../../lib/extensions";
import type { SourcesGraph } from "../../lib/sources-graph";
import { subscriptionSourceState, type HubSubscription } from "../../lib/subscriptions";
import { navigate } from "../../lib/router";
import { chooseManager, chosenManager, managerHeaderFor, resetManagerChoice } from "../../lib/manager-choice";
import { loadRoster, resetRoster } from "../../lib/managers";
import { Settings } from "../Settings";
import { Integrations, type IntegrationsProps } from "./Integrations";
import {
  buildIntegrationCatalog,
  filterIntegrationItems,
  isIntegrationRowSelected,
  resolveIntegrationTarget,
} from "./integrations/catalog";
import { decodeIntegrationsRoute, integrationHref, integrationScope, type IntegrationsRoute } from "./integrations-route";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const SAMPLE_EXTENSIONS: HubExtension[] = [
  {
    id: "larkx",
    name: "LarkX Suite",
    kind: "plugin",
    status: "running",
    enabled: true,
    source: "installed",
    description: "Unified Lark bot, inbox connector, and doc tools.",
    contributes: { connectors: ["larkx"], channel: ["larkx-bot"], tools: ["lark_docs"] },
  },
  {
    id: "wechat",
    name: "WeChat Bot",
    kind: "plugin",
    status: "running",
    enabled: true,
    source: "installed",
    description: "Chat with Persona on WeChat.",
    contributes: { channel: ["wechat"] },
  },
  {
    id: "nord-theme",
    name: "Nord Theme",
    kind: "plugin",
    status: "running",
    enabled: true,
    source: "installed",
    description: "Calm polar color palette.",
    contributes: { themes: ["nord"] },
  },
];

const SAMPLE_CHANNELS: Channel[] = [
  {
    id: "feishu",
    name: "Feishu Bot",
    kind: "im",
    source: "builtin",
    installed: true,
    enabled: true,
    state: "connected",
    capabilities: {},
    bind: "code",
  },
  {
    id: "apps",
    name: "Persona Apps",
    kind: "app",
    source: "builtin",
    installed: true,
    enabled: true,
    state: "connected",
    capabilities: {},
    bind: "none",
    surfaces: ["iPhone", "Web"],
  },
  {
    id: "larkx-bot",
    name: "LarkX Bot",
    kind: "im",
    source: "plugin",
    plugin: "larkx",
    installed: true,
    enabled: true,
    state: "connected",
    capabilities: {},
    bind: "config",
  },
  {
    id: "wechat",
    name: "WeChat",
    kind: "im",
    source: "plugin",
    plugin: "wechat",
    installed: true,
    enabled: true,
    state: "connected",
    capabilities: {},
    bind: "qr",
  },
  {
    id: "market-only-im",
    name: "Market IM",
    kind: "im",
    source: "market",
    market: "official",
    plugin: "market-im",
    installed: false,
    enabled: false,
    state: "not_installed",
    capabilities: {},
    bind: "qr",
  },
];

const SAMPLE_CONNECTORS: HubConnector[] = [
  { id: "mail", name: "Mail", status: "ok", capabilities: ["inbox", "archive"] },
  { id: "larkcli", name: "Lark CLI", status: "ok", account: "alice", capabilities: ["login", "inbox"] },
  { id: "github", name: "GitHub", status: "ok", account: "@octocat", capabilities: ["login", "inbox"] },
  { id: "larkx", name: "LarkX Connector", status: "ok", account: "corp-tenant", capabilities: ["inbox", "archive"] },
];

const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  await initI18n("zh-Hans");
});

beforeEach(() => {
  state.phone = false;
  state.twoCol = true;
  state.integrationsV2 = true;
  state.features = ["channels", "extension_manager", "connectors", "lark"];
  state.loaded = true;
  state.assistant = "assistant";
  history.replaceState(null, "", "/settings?tab=integrations");
  localStorage.clear();
  setPairedToken("");
  resetManagerChoice();
  resetRoster();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

const flush = async () => {
  await act(async () => vi.dynamicImportSettled());
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

const parseRoute = (sub: string | null) => decodeIntegrationsRoute("integrations", sub);
const subOf = (route: IntegrationsRoute) => new URL(integrationHref(route), location.origin).searchParams.get("integration");
const show = (sub: string | null, props: Partial<IntegrationsProps> = {}) =>
  <Integrations route={parseRoute(sub)} onOpen={() => {}} onBack={() => {}} {...props} />;

const SAMPLE_SUBSCRIPTIONS: HubSubscription[] = [
  {
    id: "sub_gh_fail",
    connector: "github",
    extension: "",
    source: "issues",
    source_name: "GitHub Issues",
    label: "Assigned Issues",
    recipe_origin: "extension",
    to_inbox: true,
    to_kb: false,
    state: "failing",
    error: { code: "call_failed", message: "API 502 Bad Gateway" },
    fail_count: 3,
  },
  {
    id: "sub_larkx_rate",
    connector: "larkx",
    extension: "larkx",
    source: "docs",
    source_name: "LarkX Docs",
    label: "Team Wiki Updates",
    recipe_origin: "extension",
    to_inbox: true,
    to_kb: true,
    state: "rate_limited",
  },
  {
    id: "sub_mail_ok",
    connector: "mail",
    extension: "",
    source: "inbox",
    source_name: "Mail",
    label: "Important Mail",
    recipe_origin: "persona",
    to_inbox: true,
    to_kb: false,
    state: "ok",
  },
  {
    id: "sub_push_iphone",
    connector: "-shortcuts",
    extension: "",
    source: "dev_iphone_1",
    source_name: "My iPhone",
    icon: "bell",
    label: "My iPhone Notifications",
    recipe_origin: "push",
    to_inbox: true,
    to_kb: false,
    state: "ok",
  },
];

const SAMPLE_GRAPH: SourcesGraph = {
  extensions: [
    { id: "persona", name: "Persona", line: "1 source" },
    { id: "larkx", extension: "larkx", name: "LarkX Suite", line: "1 source" },
  ],
  reads: [
    { id: "push", extension: "persona", title: "Pushed", push: true },
    { id: "t.larkx/docs/list", extension: "larkx", title: "List docs" },
  ],
  sources: [
    {
      id: "s.sub_push_iphone",
      read: "push",
      kind: "subscription",
      name: "My iPhone Notifications",
      icon: "bell",
      color: "orange",
      line: "Receiving",
      reading: true,
      to: ["inbox"],
      state: subscriptionSourceState("sub_push_iphone"),
    },
    {
      id: "s.sub_larkx_rate",
      read: "t.larkx/docs/list",
      kind: "subscription",
      name: "Team Wiki Updates",
      icon: "doc",
      color: "blue",
      line: "Rate limited",
      badge: { text: "Slowed down", tone: "caution" },
      reading: true,
      to: ["inbox", "kb"],
      state: subscriptionSourceState("sub_larkx_rate"),
    },
  ],
  destinations: [
    { id: "inbox", name: "Inbox", line: "2 sources · 2 reading", sources: 2, reading: 2 },
    { id: "kb", name: "Knowledge", line: "1 source · 1 reading", sources: 1, reading: 1 },
  ],
};

function stubFullHubFetch(overrides?: {
  markets?: boolean;
  failMail?: boolean;
  failBot?: boolean;
  extensions?: HubExtension[];
  channels?: Channel[];
  connectors?: HubConnector[];
  subscriptions?: HubSubscription[];
  graph?: SourcesGraph;
  inboxTodoForward?: boolean;
}) {
  const forwarding = new Map<string | null, boolean>();
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), "http://hub.example.com").pathname;
      if (path === "/api/v1/extensions" || path === "/api/v1/plugins") {
        return Promise.resolve(json({ extensions: overrides?.extensions ?? SAMPLE_EXTENSIONS, markets: overrides?.markets ?? true }));
      }
      if (path === "/api/v1/channels") {
        return Promise.resolve(json({ channels: overrides?.channels ?? SAMPLE_CHANNELS }));
      }
      if (path === "/api/v1/connectors") {
        return Promise.resolve(json({ connectors: overrides?.connectors ?? SAMPLE_CONNECTORS }));
      }
      if (path === "/api/v1/subscriptions") {
        return Promise.resolve(json({ subscriptions: overrides?.subscriptions ?? [], features: { deliver: true, generate: true } }));
      }
      if (path === "/api/v1/subscriptions/-/graph") {
        return Promise.resolve(json(overrides?.graph ?? SAMPLE_GRAPH));
      }
      if (path === "/api/v1/subscriptions/-/ui") {
        const owner = new Headers(init?.headers).get("X-Persona-Manager") ?? "default";
        return Promise.resolve(json({ view: { sections: [{ items: [{ type: "text", text: `Sources for ${owner}` }] }] } }));
      }
      if (path === "/api/v1/assistant") {
        const manager = new Headers(init?.headers).get("X-Persona-Manager");
        if (init?.method === "PUT" && init.body) {
          const parsed = JSON.parse(String(init.body)) as { inbox_todo_forward?: boolean };
          if (typeof parsed.inbox_todo_forward === "boolean") forwarding.set(manager, parsed.inbox_todo_forward);
        }
        return Promise.resolve(json({
          manager: manager ?? "assistant",
          integrations_v2: state.integrationsV2,
          inbox_todo_forward: forwarding.get(manager) ?? overrides?.inboxTodoForward ?? false,
        }));
      }
      if (path === "/api/v1/mail/accounts") {
        return overrides?.failMail
          ? Promise.resolve(json({ error: "not found" }, 404))
          : Promise.resolve(json({ accounts: [{ id: "m1", email: "me@example.com", status: "ok" }] }));
      }
      if (path === "/api/v1/lark/status") {
        return Promise.resolve(json({ configured: true, connected: true, name: "Workspace" }));
      }
      if (path === "/api/v1/bots/feishu") {
        return overrides?.failBot
          ? Promise.resolve(json({ error: "not found" }, 404))
          : Promise.resolve(json({ enabled: true, has_secret: true, connected: true, bot_name: "Feishu Bot" }));
      }
      if (path === "/api/v1/managers") {
        return Promise.resolve(json({ default: "assistant", managers: [{ id: "assistant", name: "助手", online: true, builtin: true }, { id: "work", name: "Work", online: true }] }));
      }
      return Promise.resolve(json({}));
    }),
  );
}

registerTestMailbox();

describe("Integrations server catalog", () => {
  const route = { scope: { kind: "current" }, view: { kind: "list" }, filter: "all" } as const;
  const feishu = { manager: "assistant", kind: "service", id: "feishu-bot" };
  const gateway = {
    ref: { manager: "assistant", kind: "extension", id: "gateway" },
    name: "Projected chat gateway", origin: "builtin_extension",
    capabilities: ["channels", "tools", "future_capability"], availability: { state: "available" },
  };

  function serveProjection(catalog: () => Response, detail?: (path: string) => Response) {
    const fetcher = vi.fn(async (input: string) => {
      const path = new URL(input, "http://hub.example.com").pathname;
      if (path === "/api/v1/managers") {
        return json({ default: "assistant", managers: [
          { id: "assistant", name: "Home", online: true, integrations_projection_v1: true },
          { id: "work", name: "Work", online: true },
        ] });
      }
      if (path === "/api/v1/assistant/integrations") return catalog();
      if (path.startsWith("/api/v1/assistant/integrations/") && detail) return detail(path);
      if (path.endsWith("/-/markets")) return json({ markets: [{ id: "official" }] });
      return json({});
    });
    vi.stubGlobal("fetch", fetcher);
    return fetcher;
  }

  it("shows server counts, supported capability pills, and an independent gateway with the bot module off", async () => {
    state.features = ["extension_manager", "channels", "connectors", "lark", "module.mail"];
    const onOpen = vi.fn();
    const fetcher = serveProjection(() => json({
      items: [gateway, { ...gateway, ref: { ...gateway.ref, kind: "future_resource", id: "other" } }],
      counts: { all: 2, channels: 1, data: 0, tools: 1 },
      availability: [{ source: "service", ref: feishu, module_feature: "module.lark", availability: { state: "module_off" } }],
    }));
    await act(async () => root.render(<Integrations route={route} onOpen={onOpen} onBack={() => {}} />));
    await flush();
    expect(host.querySelectorAll("[data-integration-row]")).toHaveLength(1);
    const row = host.querySelector<HTMLButtonElement>('[data-integration-id="gateway"]')!;
    expect(row.textContent).toContain(t("settings-integrations.cap_channels"));
    expect(row.textContent).toContain(t("settings-integrations.cap_tools"));
    expect(row.textContent).not.toContain("future_capability");
    expect(row.dataset.integrationSource).toBe("builtin_extension");
    expect(host.querySelector('[data-integration-filter="all"]')?.textContent).toContain("2");
    expect(host.querySelector('[data-integration-filter="channels"]')?.textContent).toContain("1");
    expect(host.querySelector('[data-testid="integrations-market-entry"]')).not.toBeNull();
    expect(fetcher.mock.calls.every(([url]) => /\/managers$|\/integrations$|\/-\/markets$/.test(url))).toBe(true);
    await act(async () => row.click());
    expect(onOpen).toHaveBeenCalledWith({ scope: { kind: "owned", manager: "assistant" }, view: { kind: "extension", id: "gateway" }, filter: "all" }, "push");
  });

  it("keeps successful rows and lets the user retry a partial capability failure", async () => {
    let failed = true;
    serveProjection(() => json({
      items: [{ ...gateway, capability_availability: failed ? [{ capability: "channels", availability: { state: "unavailable", reason: "read_failed" } }] : [] }],
      counts: { all: 1, channels: 1, data: 0, tools: 1 },
      availability: [{ source: "channels", availability: failed ? { state: "unavailable", reason: "read_failed" } : { state: "available" } }],
    }));
    await act(async () => root.render(<Integrations route={route} onOpen={() => {}} onBack={() => {}} />));
    await flush();
    const alert = host.querySelector('[role="alert"]')!;
    expect(alert.textContent).toContain(t("settings-channels.title"));
    expect(host.querySelector('[data-integration-id="gateway"]')?.textContent).toContain(t("settings-integrations.cap_tools"));
    failed = false;
    await act(async () => alert.parentElement?.querySelector("button")?.click());
    await flush();
    expect(host.querySelector('[role="alert"]')).toBeNull();
  });

  it("retries a projection transport failure without falling back and only shows empty after success", async () => {
    let failed = true;
    const fetcher = serveProjection(() => failed ? json({ error: "temporarily unavailable", code: "unavailable" }, 503)
      : json({ items: [], counts: { all: 0, channels: 0, data: 0, tools: 0 }, availability: [] }));
    await act(async () => root.render(<Integrations route={route} onOpen={() => {}} onBack={() => {}} />));
    await flush();
    expect(host.querySelector("[data-integrations-empty]")).toBeNull();
    const alert = host.querySelector('[role="alert"]')!;
    expect(alert).not.toBeNull();
    failed = false;
    await act(async () => alert.parentElement?.querySelector("button")?.click());
    await flush();
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector("[data-integrations-empty]")).not.toBeNull();
    expect(fetcher.mock.calls.every(([url]) => /\/managers$|\/integrations$|\/-\/markets$/.test(url))).toBe(true);
  });

  it("keeps the legacy catalog on a hub from before the roster", async () => {
    const fetcher = vi.fn(async (input: string) => {
      if (input.endsWith("/managers")) return json({ error: "Not found" }, 404);
      if (input.endsWith("/plugins") || input.endsWith("/extensions")) return json({ extensions: SAMPLE_EXTENSIONS });
      return json({});
    });
    vi.stubGlobal("fetch", fetcher);
    await act(async () => root.render(<Integrations route={route} onOpen={() => {}} onBack={() => {}} />));
    await flush();
    expect(host.querySelector('[data-integration-id="larkx"]')).not.toBeNull();
    expect(fetcher.mock.calls.some(([url]) => url.includes("/assistant/integrations"))).toBe(false);
  });

  it("opens standalone subscription settings with the subscription ref", async () => {
    const ref = { manager: "assistant", kind: "subscription", id: "standalone" };
    const item = { ref, name: "Projected subscription", origin: "standalone_subscription", capabilities: ["data"], availability: { state: "available" } };
    const fetcher = serveProjection(() => json({ items: [item], counts: { all: 1, channels: 0, data: 1, tools: 0 }, availability: [] }),
      (path) => path.endsWith("/subscription/standalone") ? json({ item, sections: [], availability: [] }) : json({}, 404));
    const onOpen = vi.fn();
    await act(async () => root.render(<Integrations route={route} onOpen={onOpen} onBack={() => {}} />));
    await flush();
    const row = host.querySelector<HTMLButtonElement>('[data-integration-id="standalone"]')!;
    expect(row.closest('section')?.querySelector('h2')?.textContent).toContain(t("subscriptions.title"));
    await act(async () => row.click());
    const opened = onOpen.mock.calls[0]![0];
    expect(opened.scope).toEqual({ kind: "owned", manager: "assistant" });
    await act(async () => root.render(<Integrations route={opened} onOpen={onOpen} onBack={() => {}} />));
    await flush();
    expect(host.querySelector('[data-integration-detail="standalone"] h1')?.textContent).toBe(item.name);
    expect(fetcher.mock.calls.some(([url]) => url.endsWith("/push_source/standalone"))).toBe(false);
  });
});

describe("buildIntegrationCatalog deduplication and capability classification", () => {
  it("deduplicates multi-capability extensions and built-in services into single canonical rows", () => {
    const items = buildIntegrationCatalog({
      extensions: SAMPLE_EXTENSIONS,
      channels: SAMPLE_CHANNELS,
      connectors: SAMPLE_CONNECTORS,
      hasMail: true,
      hasLark: true,
      hasFeishuBot: true,
      mailAccounts: [{ id: "m1", email: "me@example.com", status: "ok" }],
      larkStatus: { configured: true, connected: true, name: "Workspace" },
      feishuBotStatus: { enabled: true, has_secret: true, connected: true, bot_name: "Feishu Bot" } as never,
    });

    // larkx (extension + channel larkx-bot + connector larkx) appears exactly once with all 3 capabilities.
    const larkxMatches = items.filter((it) => it.id === "larkx" || it.channel?.id === "larkx-bot" || it.connector?.id === "larkx");
    expect(larkxMatches).toHaveLength(1);
    expect(larkxMatches[0]!.kind).toBe("extension");
    expect(larkxMatches[0]!.capabilities).toEqual(["channels", "data", "tools"]);
    expect(larkxMatches[0]!.target).toEqual({
      kind: "extension",
      id: "larkx",
      name: "LarkX Suite",
      channelId: "larkx-bot",
      connectorId: "larkx",
    });

    // Feishu Bot merges feishu channel + larkcli connector into one built-in item.
    const feishuMatches = items.filter((it) => it.id === "feishu-bot" || it.id === "feishu" || it.id === "larkcli");
    expect(feishuMatches).toHaveLength(1);
    expect(feishuMatches[0]!.capabilities).toEqual(["channels", "data"]);

    // Market-only uninstalled channels are excluded from the installed list.
    expect(items.find((it) => it.id === "market-only-im")).toBeUndefined();

    // Built-in channel-only item (`apps`) sets connectorId: null so IntegrationDetail skips connector lookup.
    const apps = items.find((it) => it.id === "apps");
    expect(apps?.target).toEqual(expect.objectContaining({ kind: "builtin", id: "apps", channelId: "apps", connectorId: null }));

    // Filtering by each capability returns the exact matching subset.
    expect(filterIntegrationItems(items, "channels").map((it) => it.id)).toEqual(["feishu-bot", "apps", "larkx", "wechat"]);
    expect(filterIntegrationItems(items, "data").map((it) => it.id)).toEqual(["mail", "feishu-bot", "lark", "github", "larkx"]);
    expect(filterIntegrationItems(items, "tools").map((it) => it.id)).toEqual(["larkx", "nord-theme"]);

    // Legacy channel/connector deep links resolve to the merged larkx extension target and highlight its row.
    expect(resolveIntegrationTarget(parseRoute("channel:larkx-bot"), items, null)).toEqual(larkxMatches[0]!.target);
    expect(resolveIntegrationTarget(parseRoute("connector:larkx"), items, null)).toEqual(larkxMatches[0]!.target);
    expect(isIntegrationRowSelected(parseRoute("channel:larkx-bot"), larkxMatches[0]!, null)).toBe(true);
    expect(isIntegrationRowSelected(parseRoute("connector:larkx"), larkxMatches[0]!, null)).toBe(true);
  });

  it("attaches deduplicated subscription alerts to owning integrations and adds standalone push sources under data", () => {
    const extraSubs: HubSubscription[] = [
      ...SAMPLE_SUBSCRIPTIONS,
      {
        id: "sub_larkx_recipe",
        connector: "larkx",
        extension: "larkx",
        source: "tasks",
        source_name: "LarkX Tasks",
        label: "Team Tasks",
        recipe_origin: "model",
        to_inbox: true,
        to_kb: false,
        state: "needs_recipe",
      },
      {
        id: "sub_lark_disc",
        connector: "lark",
        extension: "",
        source: "messages",
        source_name: "Lark Messages",
        label: "Lark Messages",
        recipe_origin: "persona",
        to_inbox: true,
        to_kb: false,
        state: "not_connected",
      },
    ];
    const items = buildIntegrationCatalog({
      extensions: SAMPLE_EXTENSIONS,
      channels: SAMPLE_CHANNELS,
      connectors: SAMPLE_CONNECTORS,
      subscriptions: extraSubs,
      hasMail: true,
      hasLark: true,
      hasFeishuBot: true,
      mailAccounts: [{ id: "m1", email: "me@example.com", status: "ok" }],
      larkStatus: { configured: true, connected: true, name: "Workspace" },
      feishuBotStatus: { enabled: true, has_secret: true, connected: true, bot_name: "Feishu Bot" } as never,
    });

    // No duplicate rows for github, larkx, mail, or lark.
    expect(items.filter((it) => it.id === "github")).toHaveLength(1);
    expect(items.filter((it) => it.id === "larkx")).toHaveLength(1);
    expect(items.filter((it) => it.id === "mail")).toHaveLength(1);
    expect(items.filter((it) => it.id === "lark")).toHaveLength(1);

    // GitHub carries red failing alert with error detail.
    const github = items.find((it) => it.id === "github")!;
    expect(github.tone).toBe("red");
    expect(github.alertTone).toBe("red");
    expect(github.subscriptionAlert).toMatchObject({
      count: 1,
      tone: "red",
      state: "failing",
      badgeText: t("settings-integrations.sub_alert_failing"),
    });
    expect(github.subtitle).toContain("API 502 Bad Gateway");

    // LarkX carries orange alert prioritizing needs_recipe over rate_limited and reporting count 2.
    const larkx = items.find((it) => it.id === "larkx")!;
    expect(larkx.tone).toBe("orange");
    expect(larkx.alertTone).toBe("orange");
    expect(larkx.subscriptionAlert).toMatchObject({
      count: 2,
      tone: "orange",
      state: "needs_recipe",
      badgeText: t("settings-integrations.sub_alert_count", { count: 2 }),
    });

    // Lark carries orange not_connected alert.
    const lark = items.find((it) => it.id === "lark")!;
    expect(lark.tone).toBe("orange");
    expect(lark.subscriptionAlert?.state).toBe("not_connected");

    // Mail has only an "ok" subscription: normal visual untouched (no alert).
    const mail = items.find((it) => it.id === "mail")!;
    expect(mail.tone).toBe("green");
    expect(mail.alertTone).toBeUndefined();
    expect(mail.subscriptionAlert).toBeUndefined();

    // Standalone push source appears once under "data" with canonical -sources/<state> route.
    const pushItem = items.find((it) => it.id === "sub_push_iphone")!;
    expect(pushItem).toBeDefined();
    expect(pushItem.kind).toBe("source");
    expect(pushItem.capabilities).toEqual(["data"]);
    expect(pushItem.tone).toBe("green");
    expect(pushItem.sourceState).toBe(subscriptionSourceState("sub_push_iphone"));
    expect(filterIntegrationItems(items, "data").map((it) => it.id)).toContain("sub_push_iphone");
    expect(filterIntegrationItems(items, "channels").map((it) => it.id)).not.toContain("sub_push_iphone");
    expect(isIntegrationRowSelected({ scope: integrationScope(), view: pushItem.view, filter: "all" }, pushItem, null)).toBe(true);
  });

  it("distinguishes source=builtin extensions from user-installed extensions while preserving extension target boundaries", () => {
    const builtinExtensions: HubExtension[] = [
      {
        id: "imessage",
        name: "信息（iMessage）",
        kind: "plugin",
        status: "running",
        enabled: true,
        source: "builtin",
        description: "Read Messages on your Mac.",
        contributes: { connectors: ["imessage"] },
      },
      {
        id: "voice",
        name: "Voice",
        kind: "plugin",
        status: "running",
        enabled: true,
        source: "builtin",
        description: "Built-in voice capabilities.",
        contributes: { voice: ["voice"] },
      },
    ];
    const items = buildIntegrationCatalog({
      extensions: [...SAMPLE_EXTENSIONS, ...builtinExtensions],
      channels: SAMPLE_CHANNELS,
      connectors: [
        ...SAMPLE_CONNECTORS,
        { id: "imessage", name: "信息", status: "ok", capabilities: ["inbox", "archive"] },
      ],
      hasMail: true,
      hasLark: false,
      hasFeishuBot: false,
      mailAccounts: [{ id: "m1", email: "me@example.com", status: "ok" }],
    });

    const imessage = items.find((it) => it.id === "imessage")!;
    const voice = items.find((it) => it.id === "voice")!;
    const larkx = items.find((it) => it.id === "larkx")!;

    // Built-in extensions keep kind="extension" and target.kind="extension" (do not collapse into kind="builtin").
    expect(imessage.kind).toBe("extension");
    expect(imessage.builtinExtension).toBe(true);
    expect(imessage.target).toEqual({
      kind: "extension",
      id: "imessage",
      name: "信息（iMessage）",
      connectorId: "imessage",
    });
    expect(voice.kind).toBe("extension");
    expect(voice.builtinExtension).toBe(true);
    expect(voice.target?.kind).toBe("extension");
    expect(larkx.builtinExtension).toBeUndefined();
    // Deduplicated: imessage connector was claimed by the imessage built-in extension.
    expect(items.filter((it) => it.id === "imessage")).toHaveLength(1);
    // Ordering places built-in extensions before user-installed extensions.
    expect(items.indexOf(imessage)).toBeLessThan(items.indexOf(larkx));
  });
});

describe("Integrations unified list view", () => {
  it("renders deduplicated items, filters by all 4 capability chips, opens Extension Market, and mounts IntegrationDetail", async () => {
    stubFullHubFetch();
    let currentSub: string | null = null;
    const render = async (sub: string | null, twoCol = true) => {
      const mount = (next: IntegrationsRoute) => {
        currentSub = subOf(next);
        root.render(<Integrations route={next} twoCol={twoCol} parentView={{ kind: "list" }} onOpen={mount} onBack={() => {}} />);
      };
      await act(async () => {
        mount(parseRoute(sub));
      });
      await flush();
    };

    await render(null, true);

    // Single persistent header Market button in single-column list view; no duplicate bottom Extension Market group (#14958).
    const singleHeaderMarket = host.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
    expect(singleHeaderMarket).not.toBeNull();
    expect(singleHeaderMarket.textContent).toContain(t("settings-integrations.browse_market_action"));
    expect(host.querySelector('[data-settings-group="market"]')).toBeNull();
    expect(host.textContent).not.toContain(t("settings-extensions.browse_market"));
    expect(host.textContent).not.toContain(t("settings-extensions.manage_markets"));

    // Deduplicated rows: larkx appears once, feishu-bot once, mail/lark/github/apps/wechat/nord-theme once each.
    const rows = () => [...host.querySelectorAll<HTMLButtonElement>("[data-integration-row]")];
    expect(rows().map((r) => r.dataset.integrationId)).toEqual([
      "mail", "feishu-bot", "lark", "github", "apps", "larkx", "wechat", "nord-theme",
    ]);

    // Filter chips: All / Chat channels / Data sources / Tools & appearance.
    const chip = (id: string) => host.querySelector<HTMLButtonElement>(`[data-integration-filter="${id}"]`)!;
    expect(chip("all").getAttribute("aria-pressed")).toBe("true");

    await act(async () => chip("channels").click());
    expect(chip("channels").getAttribute("aria-pressed")).toBe("true");
    expect(rows().map((r) => r.dataset.integrationId)).toEqual(["feishu-bot", "apps", "larkx", "wechat"]);

    await act(async () => chip("data").click());
    expect(rows().map((r) => r.dataset.integrationId)).toEqual(["mail", "feishu-bot", "lark", "github", "larkx"]);

    await act(async () => chip("tools").click());
    expect(rows().map((r) => r.dataset.integrationId)).toEqual(["larkx", "nord-theme"]);

    await act(async () => chip("all").click());
    expect(rows()).toHaveLength(8);

    // Clicking larkx opens IntegrationDetail in desktop two-column split pane with larkx highlighted,
    // independent left/right scroll containers, single-row filter chips, and no redundant Back button.
    const larkxRow = rows().find((r) => r.dataset.integrationId === "larkx")!;
    await act(async () => larkxRow.click());
    await flush();
    expect(currentSub).toBe("ext:larkx@assistant");
    const split = host.querySelector<HTMLElement>("[data-integrations-split]")!;
    expect(split).not.toBeNull();
    expect(split.classList.contains("overflow-hidden")).toBe(true);
    expect(split.classList.contains("h-full")).toBe(true);
    const listHeader = host.querySelector<HTMLElement>("[data-integrations-list-header]")!;
    expect(listHeader).not.toBeNull();
    expect(listHeader.classList.contains("shrink-0")).toBe(true);
    // Split-pane narrow header keeps the single persistent compact Market button and no bottom market group (#14958).
    const compactHeaderMarket = listHeader.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
    expect(compactHeaderMarket).not.toBeNull();
    expect(compactHeaderMarket.textContent).toContain(t("settings-integrations.browse_market_compact"));
    expect(compactHeaderMarket.getAttribute("title")).toBe(t("settings-integrations.browse_market_action"));
    expect(compactHeaderMarket.classList.contains("whitespace-nowrap")).toBe(true);
    expect(compactHeaderMarket.classList.contains("shrink-0")).toBe(true);
    expect(host.querySelector('[data-settings-group="market"]')).toBeNull();
    const listScroll = host.querySelector<HTMLElement>("[data-integrations-list-scroll]")!;
    expect(listScroll.classList.contains("overflow-y-auto")).toBe(true);
    expect(listScroll.classList.contains("overscroll-contain")).toBe(true);
    expect(listScroll.classList.contains("pb-8")).toBe(true);
    const detailScroll = host.querySelector<HTMLElement>("[data-integrations-detail-scroll]")!;
    expect(detailScroll.classList.contains("overflow-y-auto")).toBe(true);
    expect(detailScroll.classList.contains("overscroll-contain")).toBe(true);
    // Filter chips stay single-row non-wrapping in the 300px compact list header.
    const compactFilters = listHeader.querySelector<HTMLElement>("[data-integrations-filters]")!;
    expect(compactFilters).not.toBeNull();
    expect(compactFilters.classList.contains("flex-nowrap")).toBe(true);
    expect(compactFilters.classList.contains("flex-wrap")).toBe(false);
    expect(compactFilters.classList.contains("overflow-x-auto")).toBe(true);
    expect(compactFilters.classList.contains("integrations-chip-scroll")).toBe(true);
    for (const btn of compactFilters.querySelectorAll<HTMLButtonElement>("button")) {
      expect(btn.classList.contains("shrink-0")).toBe(true);
      expect(btn.classList.contains("whitespace-nowrap")).toBe(true);
    }
    const detail = host.querySelector<HTMLElement>("[data-mock-integration-detail]")!;
    expect(detail.dataset.kind).toBe("extension");
    expect(detail.dataset.id).toBe("larkx");
    expect(detail.dataset.channelId).toBe("larkx-bot");
    expect(detail.dataset.connectorId).toBe("larkx");
    // Desktop split-pane hides redundant Back button in right pane.
    expect(host.querySelector("[data-mock-detail-back]")).toBeNull();
    expect(host.querySelector('[data-integration-id="larkx"]')?.getAttribute("aria-current")).toBe("true");

    // Switching items in split-pane resets the right detail scroll without touching the left list scroll.
    listScroll.scrollTop = 180;
    detailScroll.scrollTop = 320;
    await render("builtin:mail", true);
    expect(host.querySelector<HTMLElement>("[data-integrations-list-scroll]")?.scrollTop).toBe(180);
    expect(host.querySelector<HTMLElement>("[data-integrations-detail-scroll]")?.scrollTop).toBe(0);

    // Narrow single-column layout (twoCol=false, headerBack=false) drills down without nested scrollers and keeps Back.
    await render("ext:larkx", false);
    expect(host.querySelector("[data-integrations-split]")).toBeNull();
    expect(host.querySelector("[data-integrations-list]")).toBeNull();
    expect(host.querySelector("[data-integrations-list-scroll]")).toBeNull();
    expect(host.querySelector("[data-integrations-detail-scroll]")).toBeNull();
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("larkx");
    expect(host.querySelector("[data-mock-detail-back]")).not.toBeNull();

    // Phone layout (twoCol=false, headerBack=true) omits inner Back because the top bar provides Back.
    await act(async () => {
      root.render(show("ext:larkx", { twoCol: false, headerBack: true }));
    });
    await flush();
    expect(host.querySelector("[data-integrations-split]")).toBeNull();
    expect(host.querySelector("[data-mock-detail-back]")).toBeNull();

    // Clicking the compact header Market button in split-pane opens MarketCatalog and exposes Manage Markets inside it.
    await render("ext:larkx", true);
    const splitMarketBtn = host.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
    await act(async () => splitMarketBtn.click());
    await flush();
    expect(currentSub).toBe("-market");
    const catalog = host.querySelector<HTMLElement>("[data-mock-market-catalog]")!;
    expect(catalog).not.toBeNull();
    expect(catalog.dataset.backLabel).toBe(t("settings-integrations.back"));

    const manageMarketsBtn = host.querySelector<HTMLButtonElement>("[data-mock-markets-open]")!;
    expect(manageMarketsBtn).not.toBeNull();
    await act(async () => manageMarketsBtn.click());
    await flush();
    expect(currentSub).toBe("-markets");
    expect(host.querySelector("[data-mock-markets]")).not.toBeNull();

    // Clicking the header Market button in single-column mode also opens MarketCatalog.
    await render(null, false);
    const singleMarketBtn = host.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
    expect(singleMarketBtn.textContent).toContain(t("settings-integrations.browse_market_action"));
    await act(async () => singleMarketBtn.click());
    await flush();
    expect(currentSub).toBe("-market");
    expect(host.querySelector("[data-mock-market-catalog]")).not.toBeNull();
  });

  it("keeps empty-state CTA to browse Extension Market while omitting the duplicate bottom market group", async () => {
    state.features = ["extension_manager"];
    stubFullHubFetch({ markets: true, extensions: [], channels: [], connectors: [], failMail: true, failBot: true });
    let currentSub: string | null = null;
    const render = async (sub: string | null) => {
      const mount = (next: IntegrationsRoute) => {
        currentSub = subOf(next);
        root.render(<Integrations route={next} onOpen={mount} onBack={() => {}} />);
      };
      await act(async () => mount(parseRoute(sub)));
      await flush();
    };

    await render(null);
    // Filter to tools (empty) and verify empty state CTA navigates to MarketCatalog without any bottom market group.
    const toolsChip = host.querySelector<HTMLButtonElement>('[data-integration-filter="tools"]')!;
    await act(async () => toolsChip.click());
    await flush();
    const empty = host.querySelector<HTMLElement>("[data-integrations-empty]")!;
    expect(empty).not.toBeNull();
    expect(host.querySelector('[data-settings-group="market"]')).toBeNull();
    const emptyCta = [...empty.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
      b.textContent?.includes(t("settings-integrations.browse_market_action")),
    )!;
    expect(emptyCta).not.toBeNull();
    await act(async () => emptyCta.click());
    await flush();
    expect(currentSub).toBe("-market");
    expect(host.querySelector("[data-mock-market-catalog]")).not.toBeNull();
  });

  it("shows extensions while channels are pending and ignores a previous assistant's late response", async () => {
    stubFullHubFetch();
    const fetchNow = fetch;
    let finish!: (response: Response) => void;
    const pending = new Promise<Response>((resolve) => { finish = resolve; });
    vi.stubGlobal("fetch", (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith("/channels")) {
        return new Headers(init?.headers).has("X-Persona-Manager") ? Promise.resolve(json({ channels: [] })) : pending;
      }
      return fetchNow(input, init);
    });
    await act(async () => root.render(show(null)));
    expect(host.querySelector('[data-integration-id="apps"]')).toBeNull();
    expect(host.querySelector('[data-integration-id="nord-theme"]')).not.toBeNull();
    expect(host.querySelector('[data-integrations-empty]')).toBeNull();
    await act(async () => chooseManager("work"));
    await act(async () => finish(json({ channels: SAMPLE_CHANNELS })));
    expect(host.querySelector('[data-integration-id="apps"]')).toBeNull();
  });

  it("degrades gracefully on older hubs without extension_manager, channels, or connectors", async () => {
    state.features = [];
    stubFullHubFetch({ markets: false, failMail: true, failBot: true });
    await act(async () => {
      root.render(show(null));
    });
    await flush();
    // Still renders Mail built-in without crashing, hides Extension Market header button, and shows filter chips.
    expect(host.querySelector("[data-integrations-filters]")).not.toBeNull();
    expect(host.querySelector('[data-testid="integrations-market-entry"]')).toBeNull();
    expect(host.querySelector('[data-settings-group="market"]')).toBeNull();
    expect(host.querySelector('[data-integration-id="mail"]')).not.toBeNull();
  });

  it("hides the Feishu bot and its channel when the hub turns off module.lark, with the gateway still available", async () => {
    stubFullHubFetch();
    state.features.push("modules", "module.mail");
    const draw = () => act(async () => root.render(show(null)));
    await draw();
    await flush();

    expect(host.querySelector('[data-integration-id="feishu-bot"], [data-integration-id="feishu"], [data-integration-id="larkcli"]')).toBeNull();
    expect(host.querySelector('[data-integration-id="lark"]')?.textContent).toContain(t("settings-integrations.status_connected"));
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url).endsWith("/bots/feishu"))).toBe(false);

    state.features.push("module.lark");
    await draw();
    expect(host.querySelector('[data-integration-id="feishu-bot"]')).not.toBeNull();
  });

  it("reports each partial catalog failure, keeps the bot entry, and clears the warning after a successful reload", async () => {
    state.features.push("source_subscriptions");
    stubFullHubFetch({ channels: [], connectors: [] });
    const mockFetch = vi.mocked(fetch);
    const fallback = mockFetch.getMockImplementation()!;
    const errors: Record<string, string> = {
      "/api/v1/mail/accounts": "Mail account read failed",
      "/api/v1/lark/status": "Gateway status read failed",
      "/api/v1/bots/feishu": "Bot status read failed",
      "/api/v1/subscriptions": "Subscription list read failed",
    };
    let failing = false;
    mockFetch.mockImplementation((url, init) => failing && errors[String(url)]
      ? Promise.resolve(json({ error: errors[String(url)] }, 500))
      : fallback(url, init));
    await act(async () => root.render(show(null)));
    await flush();

    const refresh = host.querySelector<HTMLButtonElement>(`[aria-label="${t("settings-integrations.refresh")}"]`)!;
    failing = true;
    await act(async () => refresh.click());
    const alert = host.querySelector('[role="alert"]')?.textContent ?? "";
    for (const error of Object.values(errors)) expect(alert).toContain(error);
    expect(host.querySelector('[data-integration-id="feishu-bot"]')).not.toBeNull();
    expect(host.querySelector('[data-integration-id="mail"]')?.textContent).toContain(t("settings-connectors.status_unknown"));

    failing = false;
    await act(async () => refresh.click());
    expect(host.querySelector('[role="alert"]')).toBeNull();
    expect(host.querySelector('[data-integration-id="mail"]')?.textContent).not.toContain(t("settings-connectors.status_unknown"));
  });

  it.each(["en", "zh-Hans", "ja"])("renders localized labels, compact Market entry, and single-row narrow capability chips in %s", async (lang) => {
    stubFullHubFetch();
    try {
      await initI18n(lang);
      await act(async () => {
        root.render(show("ext:larkx", { twoCol: true }));
      });
      await flush();
      expect(host.textContent).toContain(t("settings-integrations.list_title"));
      expect(host.textContent).toContain(t("settings-integrations.filter_all"));
      expect(host.textContent).toContain(t("settings-integrations.filter_channels"));
      expect(host.textContent).toContain(t("settings-integrations.filter_data"));
      expect(host.textContent).toContain(t("settings-integrations.filter_tools"));
      const compactMarket = host.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
      expect(compactMarket.textContent).toContain(t("settings-integrations.browse_market_compact"));
      expect(host.querySelector('[data-settings-group="market"]')).toBeNull();
      const filters = host.querySelector<HTMLElement>("[data-integrations-filters]")!;
      expect(filters.classList.contains("flex-nowrap")).toBe(true);
      expect(filters.classList.contains("overflow-x-auto")).toBe(true);

      await act(async () => {
        root.render(show(null, { twoCol: true }));
      });
      await flush();
      const singleMarket = host.querySelector<HTMLButtonElement>('[data-testid="integrations-market-entry"]')!;
      expect(singleMarket.textContent).toContain(t("settings-integrations.browse_market_action"));
    } finally {
      await initI18n("zh-Hans");
    }
  });

  it("renders source=builtin extensions in their own Built-in extensions group with Built-in pill, separate from Installed extensions", async () => {
    const extensionsWithBuiltins: HubExtension[] = [
      ...SAMPLE_EXTENSIONS,
      {
        id: "imessage",
        name: "信息（iMessage）",
        kind: "plugin",
        status: "running",
        enabled: true,
        source: "builtin",
        description: "Read Messages on your Mac.",
        contributes: { connectors: ["imessage"] },
      },
      {
        id: "cua",
        name: "Computer Use",
        kind: "plugin",
        status: "stopped",
        enabled: false,
        source: "builtin",
        description: "Operate a desktop.",
        contributes: { tools: ["cua"] },
      },
    ];
    stubFullHubFetch({
      extensions: extensionsWithBuiltins,
      connectors: [
        ...SAMPLE_CONNECTORS,
        { id: "imessage", name: "信息", status: "ok", capabilities: ["inbox", "archive"] },
      ],
    });
    for (const lang of ["en", "zh-Hans", "ja"]) {
      await initI18n(lang);
      await act(async () => {
        root.render(show(null));
      });
      await flush();

      const builtinExtGroup = host.querySelector<HTMLElement>('[data-settings-group="builtin-extensions"]')!;
      const installedExtGroup = host.querySelector<HTMLElement>('[data-settings-group="extensions"]')!;
      expect(builtinExtGroup).not.toBeNull();
      expect(installedExtGroup).not.toBeNull();
      expect(builtinExtGroup.querySelector("h2")?.textContent).toContain(t("settings-integrations.group_builtin_extensions"));
      expect(installedExtGroup.querySelector("h2")?.textContent).toContain(t("settings-integrations.group_extensions"));

      const imessageRow = builtinExtGroup.querySelector<HTMLButtonElement>('[data-integration-id="imessage"]')!;
      const cuaRow = builtinExtGroup.querySelector<HTMLButtonElement>('[data-integration-id="cua"]')!;
      expect(imessageRow).not.toBeNull();
      expect(imessageRow.dataset.integrationKind).toBe("extension");
      expect(imessageRow.dataset.integrationSource).toBe("builtin");
      expect(imessageRow.textContent).toContain(t("settings-integrations.builtin_badge"));
      expect(cuaRow).not.toBeNull();
      expect(cuaRow.dataset.integrationSource).toBe("builtin");

      // Built-in extensions are NOT placed under "Installed extensions", while user-installed ones remain there.
      expect(installedExtGroup.querySelector('[data-integration-id="imessage"]')).toBeNull();
      expect(installedExtGroup.querySelector('[data-integration-id="cua"]')).toBeNull();
      const larkxRow = installedExtGroup.querySelector<HTMLButtonElement>('[data-integration-id="larkx"]')!;
      expect(larkxRow).not.toBeNull();
      expect(larkxRow.dataset.integrationSource).toBe("installed");
      expect(larkxRow.textContent).not.toContain(t("settings-integrations.builtin_badge"));
    }
    await initI18n("zh-Hans");
  });
});

describe("Settings navigation collapse and bidirectional redirects under integrations_v2", () => {
  it("passes the extension prefill through the legacy URL redirect to Integrations", async () => {
    stubFullHubFetch();
    const proposal = JSON.stringify({ display_alias: "From a link" });
    history.replaceState(null, "", "/settings/extensions/larkx?set=" + encodeURIComponent(proposal));
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();
    const search = new URLSearchParams(location.search);
    expect(search.get("tab")).toBe("integrations");
    expect(search.get("set")).toBe(proposal);
    expect(host.querySelector('[data-mock-integration-detail][data-id="larkx"]')?.getAttribute("data-prefill")).toBe(proposal);
  });

  it("keeps legacy Channels/Connectors/Extensions tabs untouched when integrations_v2 is off and redirects /settings/integrations/*", async () => {
    state.integrationsV2 = false;
    stubFullHubFetch();
    history.replaceState(null, "", "/settings/integrations/ext:larkx@work");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    // Redirected cleanly back to legacy Extensions with @work preserved, zero 404/white screen.
    expect(location.pathname + location.search).toBe("/settings?tab=extensions&manager=work&extension=larkx%40work");
    const opens = [...host.querySelectorAll('[data-page="legacy-extensions"]')].map((el) => el.getAttribute("data-open"));
    expect(opens).toContain("larkx@work");

    const navText = host.querySelector("nav")?.textContent ?? "";
    expect(navText).toContain(t("settings-nav.tab_channels"));
    expect(navText).toContain(t("settings-nav.tab_connectors"));
    expect(navText).toContain(t("settings-nav.tab_extensions"));
    expect(navText).not.toContain(t("settings-nav.tab_integrations"));
  });

  it("collapses Channels/Connectors/Extensions into one Integrations entry when integrations_v2 is on and redirects legacy deep links", async () => {
    state.integrationsV2 = true;
    stubFullHubFetch();
    history.replaceState(null, "", "/settings/connectors/github@work");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    expect(location.pathname + location.search).toBe("/settings?tab=integrations&manager=work&integration=connector%3Agithub%40work");
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("github");
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-manager")).toBe("work");

    const navButtons = [...host.querySelectorAll<HTMLButtonElement>("nav button")].map((b) => b.textContent ?? "");
    expect(navButtons).toContain(t("settings-nav.tab_integrations"));
    expect(navButtons).not.toContain(t("settings-nav.tab_channels"));
    expect(navButtons).not.toContain(t("settings-nav.tab_connectors"));
    expect(navButtons).not.toContain(t("settings-nav.tab_extensions"));
  });

  it("reconciles seamlessly when integrations_v2 toggles off while viewing an integration detail", async () => {
    state.integrationsV2 = true;
    stubFullHubFetch();
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("larkx");

    // Flip integrations_v2 off and re-render: smoothly redirects to legacy Extensions without white screen.
    state.integrationsV2 = false;
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(location.pathname + location.search).toBe("/settings?tab=extensions&extension=larkx");
    const opens = [...host.querySelectorAll('[data-page="legacy-extensions"]')].map((el) => el.getAttribute("data-open"));
    expect(opens).toContain("larkx");
    // Legacy Extensions stays on the original single outer scroller (zero split-host change).
    const region = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(region.hasAttribute("data-integrations-split-host")).toBe(false);
    expect(region.classList.contains("overflow-y-auto")).toBe(true);
  });

  it("keeps chip selection, reloads and legacy in-app navigation in sync with the URL", async () => {
    stubFullHubFetch();
    history.replaceState({ example: true }, "", "/settings?tab=connectors&manager=work");
    const render = () => act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    const chip = (id: string) => host.querySelector<HTMLButtonElement>(`[data-integration-filter="${id}"]`)!;
    await render();
    expect(chip("data").getAttribute("aria-pressed")).toBe("true");
    const length = history.length;
    await act(async () => chip("all").click());
    expect(location.search).toContain("manager=work");
    expect(location.search).not.toContain("filter=");
    expect(history.state).toEqual({ example: true });
    expect(history.length).toBe(length);
    await act(async () => root.render(null));
    await render();
    expect(chip("all").getAttribute("aria-pressed")).toBe("true");
    await act(async () => chip("tools").click());
    expect(location.search).toContain("filter=tools");
    await act(async () => navigate("/settings?tab=connectors"));
    expect(chip("data").getAttribute("aria-pressed")).toBe("true");
    await act(async () => navigate("/settings?tab=extensions"));
    expect(chip("all").getAttribute("aria-pressed")).toBe("true");
    expect(location.search).toBe("?tab=integrations");
  });

  it("uses one view picker on phone and returns from a graph source through the Settings header", async () => {
    state.phone = true;
    vi.spyOn(Element.prototype, "clientWidth", "get").mockReturnValue(350);
    state.features.push("source_subscriptions", "inbox_sources", "inbox_sources_graph");
    stubFullHubFetch({ subscriptions: SAMPLE_SUBSCRIPTIONS });
    history.replaceState(null, "", "/settings?tab=integrations&integration=-sources");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();
    const byText = (text: string) => [...document.querySelectorAll("button")].find(b => b.textContent?.includes(text))!;
    expect(host.querySelector("[data-integrations-view-switch]")).toBeNull();
    expect(host.querySelectorAll(`[role="radiogroup"][aria-label="${t("subscriptions.view")}"]`)).toHaveLength(1);
    await act(async () => vi.dynamicImportSettled());
    await act(async () => byText("Team Wiki Updates").click());
    await act(async () => byText(t("subscriptions.graph_open")).click());
    expect(host.querySelector("[data-mock-extension-ui]")?.getAttribute("data-state")).toBe(subscriptionSourceState("sub_larkx_rate"));
    expect(byText(t("subscriptions.view_graph"))).toBeUndefined();
    await act(async () => {
      const back = new Promise(resolve => addEventListener("popstate", resolve, { once: true }));
      host.querySelector<HTMLButtonElement>("header button")!.click();
      await back;
    });
    expect(location.search).toBe("?tab=integrations&integration=-sources");
    expect(byText("Team Wiki Updates")).toBeDefined();
  });

  it("locks the Settings content host to viewport height only in desktop integrations_v2 split-pane detail state", async () => {
    state.integrationsV2 = true;
    state.twoCol = true;
    state.phone = false;
    stubFullHubFetch();

    let hostWidth = 920;
    let resized!: () => void;
    vi.spyOn(Element.prototype, "clientWidth", "get").mockImplementation(() => hostWidth);
    vi.stubGlobal("ResizeObserver", class {
      constructor(private notify: () => void) {}
      observe(el: Element) { if (el.matches('[role="region"]')) resized = this.notify; }
      disconnect() {}
    });

    // 1. Desktop list-only view: single natural scroll container (not viewport-locked split host).
    history.replaceState(null, "", "/settings?tab=integrations");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    const listRegion = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(listRegion.hasAttribute("data-integrations-split-host")).toBe(false);
    expect(listRegion.classList.contains("overflow-y-auto")).toBe(true);
    expect(listRegion.classList.contains("overflow-hidden")).toBe(false);
    expect(host.querySelector("[data-integrations-list-scroll]")).toBeNull();
    expect(host.querySelector("[data-integrations-detail-scroll]")).toBeNull();

    // 2. Clicking a row enters desktop split-pane: outer region locks height, two independent scrollers mount, no redundant Back.
    const larkxRow = host.querySelector<HTMLButtonElement>('[data-integration-id="larkx"]')!;
    await act(async () => larkxRow.click());
    await flush();
    const splitRegion = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(splitRegion.hasAttribute("data-integrations-split-host")).toBe(true);
    expect(splitRegion.classList.contains("flex")).toBe(true);
    expect(splitRegion.classList.contains("h-full")).toBe(true);
    expect(splitRegion.classList.contains("min-h-0")).toBe(true);
    expect(splitRegion.classList.contains("overflow-hidden")).toBe(true);
    expect(splitRegion.classList.contains("overflow-y-auto")).toBe(false);
    expect(host.querySelector("[data-integrations-list-scroll]")).not.toBeNull();
    expect(host.querySelector("[data-integrations-detail-scroll]")).not.toBeNull();
    expect(host.querySelector("[data-mock-detail-back]")).toBeNull();

    // Both sidebars are outside the measured host. The 1080px, 1200px and 1280px layouts leave 552px, 672px and 752px.
    const detail = host.querySelector("[data-mock-integration-detail]");
    expect(detail).not.toBeNull();
    for (const width of [552, 672, 739, 740, 752, 912]) {
      hostWidth = width;
      await act(async () => resized());
      expect(splitRegion.hasAttribute("data-integrations-split-host")).toBe(width >= 740);
      expect(!!host.querySelector("[data-integrations-detail-scroll]")).toBe(width >= 740);
      expect(host.querySelector("[data-mock-integration-detail]")).toBe(detail);
    }
    hostWidth = 560;

    // 3. Narrow desktop (< 900px, twoCol=false, phone=false): single natural scroller, Back button kept.
    state.twoCol = false;
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    const narrowRegion = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(narrowRegion.hasAttribute("data-integrations-split-host")).toBe(false);
    expect(narrowRegion.classList.contains("overflow-y-auto")).toBe(true);
    expect(host.querySelector("[data-integrations-list-scroll]")).toBeNull();
    expect(host.querySelector("[data-integrations-detail-scroll]")).toBeNull();
    expect(host.querySelector("[data-mock-detail-back]")).not.toBeNull();

    // 4. Phone (twoCol=false, phone=true): single natural scroller, top-bar Back present, no inner Back.
    state.phone = true;
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    const phoneRegion = host.querySelector<HTMLElement>('[role="region"]')!;
    expect(phoneRegion.hasAttribute("data-integrations-split-host")).toBe(false);
    expect(phoneRegion.classList.contains("overflow-y-auto")).toBe(true);
    expect(host.querySelector("[data-integrations-list-scroll]")).toBeNull();
    expect(host.querySelector("[data-integrations-detail-scroll]")).toBeNull();
    expect(host.querySelector("[data-mock-detail-back]")).toBeNull();
    expect(host.querySelector("header button:first-child")).not.toBeNull();
  });

  it("surfaces subscription alerts, standalone push sources, and the lazy Data Flow view with SourcesGraph + InboxForwardSetting", async () => {
    state.integrationsV2 = true;
    state.twoCol = true;
    state.phone = false;
    state.features = ["channels", "extension_manager", "connectors", "lark", "source_subscriptions", "inbox_sources", "inbox_sources_graph"];
    stubFullHubFetch({ subscriptions: SAMPLE_SUBSCRIPTIONS, inboxTodoForward: false });

    const origClientWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientWidth");
    Object.defineProperty(HTMLElement.prototype, "clientWidth", { configurable: true, get: () => 960 });

    try {
      history.replaceState(null, "", "/settings?tab=integrations");
      await act(async () => {
        root.render(<Settings onToggleSidebar={() => {}} />);
      });
      await flush();

      // Sidebar folds channels/connectors/extensions/sources 4-in-1 into Integrations & Extensions (#15424).
      const navButtons = [...host.querySelectorAll<HTMLButtonElement>("nav button")].map((b) => b.textContent ?? "");
      expect(navButtons).toContain(t("settings-nav.tab_integrations"));
      expect(navButtons).not.toContain(t("settings-nav.tab_channels"));
      expect(navButtons).not.toContain(t("settings-nav.tab_connectors"));
      expect(navButtons).not.toContain(t("settings-nav.tab_extensions"));
      expect(navButtons).not.toContain(t("settings-nav.tab_sources"));

      // Top view switch ("Integrations / Data flow") and persistent Market button both visible.
      const viewSwitch = host.querySelector<HTMLElement>("[data-integrations-view-switch]")!;
      expect(viewSwitch).not.toBeNull();
      expect(host.querySelector('[data-testid="integrations-market-entry"]')).not.toBeNull();

      // Deduplicated subscription alerts shown on github (red failing) and larkx (orange rate_limited); mail (ok) untouched.
      const githubRow = host.querySelector<HTMLButtonElement>('[data-integration-id="github"]')!;
      expect(githubRow.getAttribute("data-integration-sub-alert")).toBe("failing");
      expect(githubRow.querySelector('[data-subscription-alert-badge="failing"]')).not.toBeNull();
      expect(githubRow.textContent).toContain("API 502 Bad Gateway");

      const larkxRow = host.querySelector<HTMLButtonElement>('[data-integration-id="larkx"]')!;
      expect(larkxRow.getAttribute("data-integration-sub-alert")).toBe("rate_limited");
      expect(larkxRow.querySelector('[data-subscription-alert-badge="rate_limited"]')).not.toBeNull();

      const mailRow = host.querySelector<HTMLButtonElement>('[data-integration-id="mail"]')!;
      expect(mailRow.getAttribute("data-integration-sub-alert")).toBeNull();
      expect(mailRow.querySelector("[data-subscription-alert-badge]")).toBeNull();

      // Standalone push source is listed under Push sources and included in the Data sources filter.
      const pushRow = host.querySelector<HTMLButtonElement>('[data-integration-id="sub_push_iphone"]')!;
      expect(pushRow).not.toBeNull();
      expect(pushRow.dataset.integrationKind).toBe("source");

      await act(async () => host.querySelector<HTMLButtonElement>('[data-integration-filter="data"]')!.click());
      await flush();
      expect(host.querySelector('[data-integration-id="sub_push_iphone"]')).not.toBeNull();

      // Clicking the standalone push source opens its detail pane in desktop split-pane via lazy SourcesUI with exact state.
      await act(async () => host.querySelector<HTMLButtonElement>('[data-integration-id="sub_push_iphone"]')!.click());
      await flush();
      expect(host.querySelector("[data-integrations-split]")).not.toBeNull();
      const pushDetail = host.querySelector<HTMLElement>('[data-integration-detail="sub_push_iphone"]')!;
      expect(pushDetail).not.toBeNull();
      const pushExtUI = pushDetail.querySelector<HTMLElement>('[data-mock-extension-ui][data-extension="-subscriptions"]')!;
      expect(pushExtUI).not.toBeNull();
      expect(pushExtUI.dataset.state).toBe(subscriptionSourceState("sub_push_iphone"));
      await act(async () => vi.dynamicImportSettled());
      expect(pushDetail.querySelector("[data-external-source-settings]")).toBeNull();
      expect(host.querySelector('[data-integration-id="sub_push_iphone"]')?.getAttribute("aria-current")).toBe("true");
      // Split-pane compact header still keeps both view switch and Market button.
      expect(host.querySelector("[data-integrations-list-header] [data-integrations-view-switch]")).not.toBeNull();
      expect(host.querySelector('[data-integrations-list-header] [data-testid="integrations-market-entry"]')).not.toBeNull();

      // Switching to the Data Flow view exits split-pane into wide single scroll, keeps Market button,
      // and lazily renders SourcesGraph + untouched InboxForwardSetting.
      const dataFlowBtn = [...host.querySelectorAll<HTMLButtonElement>('[data-integrations-view-switch] [role="radio"]')].find(
        (b) => b.textContent?.includes(t("settings-integrations.view_sources")),
      )!;
      await act(async () => dataFlowBtn.click());
      await flush();
      expect(location.search).toContain("integration=-sources");
      expect(host.querySelector("[data-integrations-sources-view]")).not.toBeNull();
      const dataFlowRegion = host.querySelector<HTMLElement>('[role="region"]')!;
      expect(dataFlowRegion.hasAttribute("data-integrations-split-host")).toBe(false);
      expect(dataFlowRegion.classList.contains("overflow-y-auto")).toBe(true);
      expect(host.querySelector('[data-testid="integrations-market-entry"]')).not.toBeNull();

      // SourcesGraph renders source nodes AND InboxForwardSetting switch is mounted untouched.
      expect(host.textContent).toContain("My iPhone Notifications");
      expect(host.textContent).toContain("Team Wiki Updates");
      expect(host.querySelector("[data-integrations-sources-view] [data-external-source-settings]")).not.toBeNull();
      const forwardSwitch = host.querySelector<HTMLButtonElement>(
        `[role="switch"][aria-label="${t("settings-assistantsettings.inbox_todo_forward")}"]`,
      )!;
      expect(forwardSwitch).not.toBeNull();
      expect(forwardSwitch.getAttribute("aria-checked")).toBe("false");
      await act(async () => forwardSwitch.click());
      await flush();
      expect(forwardSwitch.getAttribute("aria-checked")).toBe("true");

      // Clicking a source node in SourcesGraph -> "Open Settings" drills into that source's page and returns smoothly.
      const sourceNodeBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
        b.textContent?.includes("Team Wiki Updates"),
      )!;
      await act(async () => sourceNodeBtn.click());
      await flush();
      const openSettingsBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
        b.textContent?.includes(t("subscriptions.graph_open")),
      )!;
      expect(openSettingsBtn).not.toBeNull();
      await act(async () => openSettingsBtn.click());
      await flush();
      const drilledExtUI = host.querySelector<HTMLElement>('[data-mock-extension-ui][data-extension="-subscriptions"]')!;
      expect(drilledExtUI.dataset.state).toBe(subscriptionSourceState("sub_larkx_rate"));

      const backToGraphBtn = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) =>
        b.textContent?.includes(t("subscriptions.view_graph")),
      )!;
      await act(async () => {
        const returned = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
        backToGraphBtn.click();
        await returned;
      });
      await flush();
      expect(host.textContent).toContain("My iPhone Notifications");

      // Switching back to "Integrations" returns to the list view cleanly.
      const listViewBtn = [...host.querySelectorAll<HTMLButtonElement>('[data-integrations-view-switch] [role="radio"]')].find(
        (b) => b.textContent?.includes(t("settings-integrations.view_list")),
      )!;
      await act(async () => listViewBtn.click());
      await flush();
      expect(host.querySelector("[data-integrations-list]")).not.toBeNull();
      expect(host.querySelector("[data-integrations-sources-view]")).toBeNull();
    } finally {
      if (origClientWidth) Object.defineProperty(HTMLElement.prototype, "clientWidth", origClientWidth);
      else delete (HTMLElement.prototype as { clientWidth?: number }).clientWidth;
    }
  });

  it("hides the Data Flow view switcher when hub lacks inbox_sources and redirects -sources to legacy sources when integrations_v2 is off", async () => {
    state.integrationsV2 = true;
    state.features = ["channels", "extension_manager", "connectors", "lark"];
    stubFullHubFetch();
    history.replaceState(null, "", "/settings?tab=integrations");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(host.querySelector("[data-integrations-view-switch]")).toBeNull();

    // Flip integrations_v2 off while at -sources: redirects cleanly to legacy ?tab=sources and restores standalone nav.
    state.integrationsV2 = false;
    state.features = ["channels", "extension_manager", "connectors", "lark", "inbox_sources"];
    history.replaceState(null, "", "/settings?tab=integrations&integration=-sources");
    await act(async () => root.render(null));
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(location.pathname + location.search).toBe("/settings?tab=sources");
    const restoredNav = [...host.querySelectorAll<HTMLButtonElement>("nav button")].map((b) => b.textContent ?? "");
    expect(restoredNav).toContain(t("settings-nav.tab_sources"));
    expect(restoredNav).not.toContain(t("settings-nav.tab_integrations"));

    // Flip back to integrations_v2=true from /settings?tab=sources: redirects straight into Data Flow view.
    state.integrationsV2 = true;
    state.features = ["channels", "extension_manager", "connectors", "lark", "source_subscriptions", "inbox_sources", "inbox_sources_graph"];
    stubFullHubFetch({ subscriptions: SAMPLE_SUBSCRIPTIONS, inboxTodoForward: false });
    history.replaceState(null, "", "/settings?tab=sources");
    await act(async () => root.render(null));
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(location.pathname + location.search).toBe("/settings?tab=integrations&integration=-sources");
    expect(host.querySelector("[data-integrations-sources-view]")).not.toBeNull();
    const foldedNav = [...host.querySelectorAll<HTMLButtonElement>("nav button")].map((b) => b.textContent ?? "");
    expect(foldedNav).toContain(t("settings-nav.tab_integrations"));
    expect(foldedNav).not.toContain(t("settings-nav.tab_sources"));

    // Deep link /settings/sources/<encodedState> redirects into push-source detail inside Integrations.
    const pushState = subscriptionSourceState("sub_push_iphone");
    const encodedPushState = encodeURIComponent(pushState);
    history.replaceState(null, "", `/settings/sources/${encodedPushState}`);
    await act(async () => root.render(null));
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(location.pathname + location.search).toBe(
      `/settings?tab=integrations&integration=${encodeURIComponent(`-sources/${encodedPushState}`)}`,
    );
    const pushExtUI = host.querySelector<HTMLElement>('[data-mock-extension-ui][data-extension="-subscriptions"]')!;
    expect(pushExtUI).not.toBeNull();
    expect(pushExtUI.dataset.state).toBe(pushState);
  });

  it("widens the Settings container to 1680px when an extension plugin_page requests wide mode and restores 1120px on exit", async () => {
    state.integrationsV2 = true;
    state.features = ["channels", "extension_manager", "connectors", "lark"];
    stubFullHubFetch();
    extViewMock.onWide = undefined;
    history.replaceState(null, "", "/settings?tab=integrations&integration=demo-ext%2Fui%2Ftopology");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    const extUI = host.querySelector<HTMLElement>('[data-mock-extension-ui][data-extension="demo-ext"][data-ui="topology"]')!;
    expect(extUI).not.toBeNull();
    const container = () => host.querySelector<HTMLElement>('[role="region"] > div')!;
    expect(container().className).toContain("max-w-[1120px]");

    await act(async () => extViewMock.onWide?.(true));
    expect(container().className).toContain("max-w-[1680px]");

    await act(async () => extViewMock.onWide?.(false));
    expect(container().className).toContain("max-w-[1120px]");
  });

  it("forwards IntegrationDetail crosslinks through Settings to Assistant repositories and Knowledge items", async () => {
    state.integrationsV2 = true;
    state.twoCol = true;
    state.phone = false;
    state.features = ["channels", "extension_manager", "connectors", "lark"];
    stubFullHubFetch();

    history.replaceState(null, "", "/settings?tab=integrations&integration=builtin%3Agithub");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    const reposBtn = host.querySelector<HTMLButtonElement>("[data-mock-detail-repos]")!;
    expect(reposBtn).not.toBeNull();
    await act(async () => reposBtn.click());
    await flush();
    expect(location.pathname + location.search).toBe("/settings?tab=persona");
    expect(host.querySelector('[data-page="persona"]')).not.toBeNull();

    history.replaceState(null, "", "/settings?tab=integrations&integration=builtin%3Amail");
    await act(async () => root.render(null));
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    const kbBtn = host.querySelector<HTMLButtonElement>("[data-mock-detail-kb]")!;
    expect(kbBtn).not.toBeNull();
    await act(async () => kbBtn.click());
    await flush();
    expect(location.pathname + location.search).toBe("/settings?tab=knowledge");
    expect(host.querySelector('[data-page="knowledge-selection"]')).not.toBeNull();
  });

  it("returns from MarketItem to extension detail after default-assistant Upgrade, while catalog-opened items return to MarketCatalog", async () => {
    state.integrationsV2 = true;
    state.twoCol = true;
    state.phone = false;
    stubFullHubFetch();

    await act(async () => loadRoster());

    const waitPop = (go: () => void) => act(async () => {
      const done = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
      go();
      await done;
    });

    // 1. Default assistant: open ext:larkx -> click Upgrade -> MarketItem Back returns to ext:larkx (not -market).
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("larkx");

    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-detail-upgrade]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("-market/official/larkx@assistant");
    expect(host.querySelector("[data-mock-market-item]")?.getAttribute("data-id")).toBe("larkx");

    await waitPop(() => host.querySelector<HTMLButtonElement>("[data-mock-item-back]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("ext:larkx");
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("larkx");

    // 2. Non-default assistant: ext:larkx@work -> Upgrade -> Back returns to ext:larkx@work.
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx%40work");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-detail-upgrade]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("-market/official/larkx@work");

    await waitPop(() => host.querySelector<HTMLButtonElement>("[data-mock-item-back]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("ext:larkx@work");

    // 3. Phone: default assistant ext:larkx -> Upgrade -> header Back returns to ext:larkx.
    state.phone = true;
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=ext%3Alarkx");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-detail-upgrade]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("-market/official/larkx@assistant");
    await waitPop(() => host.querySelector<HTMLButtonElement>("header button")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("ext:larkx");

    // 4. MarketCatalog -> open item -> Back still returns to MarketCatalog.
    state.phone = false;
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=-market");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-market-open]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("-market/official/demo-ext@assistant");
    await waitPop(() => host.querySelector<HTMLButtonElement>("[data-mock-item-back]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("-market");
    expect(host.querySelector("[data-mock-market-catalog]")).not.toBeNull();
  });

  it("returns to the parent integration detail when backing out of a drilled push source on narrow desktop (<740px)", async () => {
    state.integrationsV2 = true;
    state.twoCol = false;
    state.phone = false;
    state.features = ["channels", "extension_manager", "connectors", "lark", "source_subscriptions", "inbox_sources"];
    stubFullHubFetch({ subscriptions: SAMPLE_SUBSCRIPTIONS });
    vi.spyOn(Element.prototype, "clientWidth", "get").mockReturnValue(600);

    await act(async () => loadRoster());

    const waitPop = (go: () => void) => act(async () => {
      const done = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
      go();
      await done;
    });

    // Narrow desktop: list → extension → source → Back returns to the same scoped extension.
    history.replaceState(null, "", "/settings?tab=integrations");
    await act(async () => {
      root.render(<Settings onToggleSidebar={() => {}} />);
    });
    await flush();

    await act(async () => host.querySelector<HTMLButtonElement>('[data-integration-id="larkx"]')!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("ext:larkx@assistant");

    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-detail-sources]")!.click());
    await flush();
    const pushPane = host.querySelector<HTMLElement>('[data-integration-kind="source"]')!;
    expect(pushPane).not.toBeNull();
    const pushBackBtn = pushPane.querySelector<HTMLButtonElement>("button")!;
    expect(pushBackBtn.textContent).toContain(t("settings-extensionview.back"));

    await waitPop(() => pushBackBtn.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBe("ext:larkx@assistant");
    expect(host.querySelector("[data-mock-integration-detail]")?.getAttribute("data-id")).toBe("larkx");

    // Back from ext:larkx returns cleanly to the Integrations list.
    await waitPop(() => host.querySelector<HTMLButtonElement>("[data-mock-detail-back]")!.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBeNull();
    expect(host.querySelector("[data-integrations-list]")).not.toBeNull();

    // Direct push-source open from the list on narrow desktop still returns to the list.
    await act(async () => host.querySelector<HTMLButtonElement>('[data-integration-id="sub_push_iphone"]')!.click());
    await flush();
    const directPushPane = host.querySelector<HTMLElement>('[data-integration-kind="source"]')!;
    const directBackBtn = directPushPane.querySelector<HTMLButtonElement>("button")!;
    expect(directBackBtn.textContent).toContain(t("settings-integrations.back"));
    await waitPop(() => directBackBtn.click());
    await flush();
    expect(new URLSearchParams(location.search).get("integration")).toBeNull();
    expect(host.querySelector("[data-integrations-list]")).not.toBeNull();
  });
});

describe("assistant isolation across integration routes", () => {
  function headersFor(path: string, method: string) {
    return vi.mocked(fetch).mock.calls
      .filter(([url, init]) => String(url).split("?")[0] === `/api/v1${path}` && (init?.method ?? "GET") === method)
      .map(([, init]) => new Headers(init?.headers).get("X-Persona-Manager"));
  }

  async function clickOption(group: string, label: string) {
    await flush();
    const button = [...host.querySelectorAll<HTMLButtonElement>(`${group} [role="radio"]`)].find((el) => el.textContent === label);
    expect(button).toBeDefined();
    await act(async () => button!.click());
    await flush();
  }

  beforeEach(() => {
    state.features.push("source_subscriptions", "inbox_sources", "inbox_sources_graph");
    stubFullHubFetch();
  });

  it("retains the chosen assistant when switching list → data flow → list", async () => {
    chooseManager("work");
    state.assistant = "work";
    history.replaceState(null, "", "/settings?tab=integrations");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await clickOption("[data-integrations-view-switch]", t("settings-integrations.view_sources"));
    expect(new URLSearchParams(location.search).get("integration")).toBe("-sources@work");
    expect(headersFor("/subscriptions/-/graph", "GET")).toEqual(["work"]);
    const returned = new Promise<void>((resolve) => addEventListener("popstate", () => resolve(), { once: true }));
    await clickOption("[data-integrations-view-switch]", t("settings-integrations.view_list"));
    await act(async () => returned);
    expect(host.querySelector("[data-integrations-list]")).not.toBeNull();
    expect(chosenManager()).toBe("work");
    expect(managerHeaderFor("/subscriptions/-/graph")).toBeUndefined();
  });

  it("uses the chosen assistant for an unqualified Data flow link and a market install", async () => {
    chooseManager("work");
    const onOpen = vi.fn();
    await act(async () => root.render(show("-sources", { onOpen })));
    await act(async () => vi.dynamicImportSettled());
    await flush();
    expect(headersFor("/subscriptions/-/graph", "GET")).toEqual(["work"]);
    await act(async () => root.render(show("-market", { onOpen })));
    await flush();
    expect(host.querySelector("[data-mock-market-catalog]")?.getAttribute("data-manager")).toBe("work");
    await act(async () => root.render(show("-market/official/demo-ext", { onOpen })));
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-mock-item-install]")!.click());
    expect(onOpen).toHaveBeenCalledWith({
      scope: integrationScope("work"), view: { kind: "extension", id: "demo-ext" }, filter: "all",
    }, "replace");
  });

  it.each([true, false])("reads and saves forwarding for the linked assistant with Labs enabled=%s", async (v2) => {
    state.integrationsV2 = v2;
    history.replaceState(null, "", v2 ? "/settings?tab=integrations&integration=-sources%40work" : "/settings?tab=sources&manager=work");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();
    expect(headersFor("/assistant", "GET").length).toBeGreaterThan(0);
    expect(new Set(headersFor("/assistant", "GET"))).toEqual(new Set(["work"]));
    const toggle = host.querySelector<HTMLButtonElement>('[data-settings-group="inbox-todo-forward"] [role="switch"]')!;
    await act(async () => toggle.click());
    expect(headersFor("/assistant", "PUT")).toEqual(["work"]);
    expect(toggle.getAttribute("aria-checked")).toBe("true");
    if (v2) await clickOption('[data-settings-group="inbox-sources"]', t("subscriptions.view_list"));
    expect(headersFor("/subscriptions/-/ui", "POST")).toEqual(["work"]);
    expect(host.textContent).toContain("Sources for work");
    if (!v2) await clickOption('[data-settings-group="inbox-sources"]', t("subscriptions.view_graph"));
    expect(headersFor("/subscriptions/-/graph", "GET")).toEqual(["work"]);
    expect(chosenManager()).toBeNull();
  });

  it("reloads the same push source page when only the route owner changes", async () => {
    const onOpen = vi.fn();
    await act(async () => root.render(show("-sources@work/source", { onOpen })));
    await act(async () => root.render(show("-sources/source", { onOpen })));
    expect(headersFor("/subscriptions/-/ui", "POST")).toEqual(["work", null]);
  });

  it("hides the ambiguous view switcher on cross-assistant -sources@other deep links and never silently switches the global assistant", async () => {
    // Sidebar is on default assistant (chosenManager() === null), deep-linked to -sources@work.
    await act(async () => loadRoster());
    history.replaceState(null, "", "/settings?tab=integrations&integration=-sources%40work");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();

    // Ambiguous "Integrations | Data flow" segmented toggle is hidden on cross-assistant deep links.
    expect(host.querySelector("[data-integrations-view-switch]")).toBeNull();
    const crossNav = host.querySelector<HTMLElement>("[data-integrations-cross-assistant-nav]")!;
    expect(crossNav).not.toBeNull();
    const backBtn = crossNav.querySelector<HTMLButtonElement>('[data-testid="integrations-cross-assistant-back"]')!;
    const switchBtn = crossNav.querySelector<HTMLButtonElement>('[data-testid="integrations-switch-assistant"]')!;
    expect(backBtn).not.toBeNull();
    expect(switchBtn).not.toBeNull();
    expect(switchBtn.textContent).toContain(t("settings-integrations.switch_assistant_integrations", { name: "Work" }));

    // Back opens the linked assistant’s directory while the sidebar keeps its selection.
    await act(async () => backBtn.click());
    await flush();
    expect(chosenManager()).toBeNull();
    expect(host.querySelector("[data-integrations-list]")).not.toBeNull();
    expect(new URLSearchParams(location.search).get("manager")).toBe("work");

    // Re-open -sources@work and explicitly click "Switch to Work": now global assistant switches intentionally.
    await act(async () => root.render(null));
    history.replaceState(null, "", "/settings?tab=integrations&integration=-sources%40work");
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();
    const explicitSwitch = host.querySelector<HTMLButtonElement>('[data-testid="integrations-switch-assistant"]')!;
    await act(async () => explicitSwitch.click());
    await flush();
    expect(chosenManager()).toBe("work");
    expect(new URLSearchParams(location.search).get("integration")).toBeNull();
    // Once the switched assistant's state arrives, Settings renders Work's Integrations list.
    state.assistant = "work";
    await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
    await flush();
    expect(host.querySelector("[data-integrations-list]")).not.toBeNull();
  });

  it("adds a same-assistant Data Flow entry in single-pane standalone source detail without leaking global defaults or switching sidebar assistant", async () => {
    state.integrationsV2 = true;
    state.twoCol = false;
    state.phone = false;
    stubFullHubFetch({ subscriptions: SAMPLE_SUBSCRIPTIONS });
    const measure = vi.spyOn(Element.prototype, "clientWidth", "get").mockReturnValue(600);
    await act(async () => loadRoster());

    try {
      const pushState = subscriptionSourceState("sub_push_iphone");
      history.replaceState(null, "", `/settings?tab=integrations&manager=work&integration=${encodeURIComponent(`-sources@work/${pushState}`)}`);
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      await flush();

      const pushPane = host.querySelector<HTMLElement>('[data-integration-kind="source"]')!;
      expect(pushPane).not.toBeNull();
      // Single-source scope preserves #17677 boundary (no provider global defaults).
      expect(pushPane.querySelector("[data-external-source-settings]")).toBeNull();

      const dataFlowRow = pushPane.querySelector<HTMLButtonElement>('[data-settings-row="source-data-flow-entry"]')!;
      expect(dataFlowRow).not.toBeNull();
      expect(dataFlowRow.textContent).toContain(t("settings-integrations.view_sources"));

      // Clicking opens the same assistant's Data Flow view without changing the sidebar's selected assistant.
      await act(async () => dataFlowRow.click());
      await flush();
      expect(location.search).toContain("manager=work");
      expect(location.search).toContain("integration=-sources%40work");
      expect(host.querySelector("[data-integrations-sources-view]")).not.toBeNull();
      expect(chosenManager()).toBeNull();

      // In desktop split-pane (>=740px), the list header already owns the view switcher so the duplicate row is omitted.
      measure.mockReturnValue(920);
      state.twoCol = true;
      await act(async () => root.render(null));
      history.replaceState(null, "", `/settings?tab=integrations&integration=${encodeURIComponent(`-sources/${pushState}`)}`);
      await act(async () => root.render(<Settings onToggleSidebar={() => {}} />));
      await flush();
      expect(host.querySelector("[data-integrations-split]")).not.toBeNull();
      expect(host.querySelector('[data-settings-row="source-data-flow-entry"]')).toBeNull();
    } finally {
      measure.mockRestore();
    }
  });
});
