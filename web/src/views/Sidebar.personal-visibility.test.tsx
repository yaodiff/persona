// @vitest-environment jsdom
// Exercise the mounted entry through the real REST client and both list stores, including cold and live updates.
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((media: string) => ({ matches: false, media, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
});
import { initI18n, t } from "../i18n";
import { agentsChanged, refreshAgentSessions, resetAgents, setAgentListMode, type AgentSession, type AgentSourceStatus } from "../lib/agents";
import { SESSIONS_LIST_FEATURE } from "../lib/session-list";
import { getState, reset } from "../lib/store";
import { PersonalAgents } from "./SidebarRows";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const example: AgentSession = { id: "example:agent", source: "example", sessionKey: "agent", provider: "example", title: "Example personal agent", canSend: true, unreadCount: 0, rankTimeMs: 1 };
const ready: AgentSourceStatus = { source: "example", enabled: true, accounts: [{ accountId: "example-account", provider: "example" }] };
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let host: HTMLDivElement, root: Root;
let listed: AgentSession[];
let sources: AgentSourceStatus[];
let readSessions: () => Promise<Response>;
let readSources: () => Promise<Response>;
const flush = async () => { for (let i = 0; i < 15; i++) await act(async () => { await Promise.resolve(); }); };
const hidden = () => expect(host.innerHTML).toBe("");
const shown = () => expect(host.textContent).toContain(t("sidebar.personal_agents"));
const update = async () => { await act(async () => agentsChanged([])); await flush(); };

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  resetAgents();
  listed = [example];
  sources = [ready];
  readSessions = async () => response({ sessions: listed });
  readSources = async () => response({ sources });
  vi.stubGlobal("fetch", async (url: string) => {
    const path = new URL(url, "https://example.invalid").pathname;
    if (path === "/api/v1/agents/sessions") return readSessions();
    if (path === "/api/v1/agents") return readSources();
    return response({});
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetAgents();
  vi.unstubAllGlobals();
});

for (const bounded of [false, true]) describe(`${bounded ? "bounded" : "legacy"} Personal agents visibility`, () => {
  const mount = async () => {
    getState().features = bounded ? [SESSIONS_LIST_FEATURE] : [];
    setAgentListMode(bounded);
    await act(async () => root.render(<StrictMode><PersonalAgents reg={() => () => {}} hover={() => {}} activeKey="" go={() => () => {}} /></StrictMode>));
    await flush();
  };
  const load = async () => { await act(async () => { await refreshAgentSessions(); }); await flush(); };

  it.each(["missing", "disabled", "no_accounts"])("hides the entire section for setup %s even with stale sessions", async (setup) => {
    sources = [{ ...ready, setup }];
    await mount();
    await load();
    hidden();
  });

  it.each([
    { status: [] as AgentSourceStatus[] },
    { status: [{ ...ready, accounts: [] }] },
    { status: [{ ...ready, enabled: false }] },
  ])("hides unconfigured sources or empty sessions: %j", async ({ status }) => {
    sources = status;
    await mount();
    await load();
    hidden();
  });

  it("hides a configured source with no sessions", async () => {
    listed = [];
    await mount();
    await load();
    hidden();
  });

  it("shows configured sessions only after setup finishes loading", async () => {
    let answer!: (value: Response) => void;
    readSources = () => new Promise<Response>((resolve) => { answer = resolve; });
    await mount();
    hidden();
    let pull!: Promise<void>;
    await act(async () => { pull = refreshAgentSessions(); });
    await flush();
    hidden();
    await act(async () => { answer(response({ sources })); await pull; });
    await flush();
    shown();
    if (bounded) expect(host.querySelector("[data-paged-sessions=personal]")?.getAttribute("data-loaded-sessions")).toBe("1");
    else expect(host.textContent).toContain(example.title);
  });

  it("waits for sessions when setup arrives first", async () => {
    const answers: ((value: Response) => void)[] = [];
    readSessions = () => new Promise<Response>((resolve) => answers.push(resolve));
    await mount();
    let pull!: Promise<void>;
    await act(async () => { pull = refreshAgentSessions(); });
    await flush();
    hidden();
    await act(async () => { answers.forEach((answer) => answer(response({ sessions: listed }))); await pull; });
    await flush();
    shown();
  });

  it("stays hidden after a cold failure and appears after an agents event recovers", async () => {
    readSessions = async () => response({ error: "Example load failed" }, 503);
    await mount();
    await load();
    hidden();
    readSessions = async () => response({ sessions: listed });
    await update();
    shown();
  });

  it("waits for a failed source read to recover", async () => {
    readSources = async () => response({ error: "Example source failed" }, 503);
    await mount();
    await load();
    hidden();
    readSources = async () => response({ sources });
    await update();
    shown();
  });

  it("reacts to setup and session changes without remounting or a page reload", async () => {
    sources = [{ ...ready, setup: "disabled" }];
    listed = [];
    await mount();
    await load();
    hidden();
    sources = [ready];
    await update();
    hidden();
    listed = [example];
    await update();
    shown();
    sources = [{ ...ready, setup: "no_accounts" }];
    await update();
    hidden();
    sources = [ready];
    await update();
    shown();
    listed = [];
    await update();
    hidden();
  });
});
