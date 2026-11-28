// @vitest-environment jsdom
// The personal list by account (#16248): the hub groups personal Agents by account id; each heading and each choice of
// the account picker names the account as /agents lists it, after its provider's mark, with the id as the tooltip.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import type { AgentSourceStatus } from "../../lib/agents";
import { loadPages } from "../../lib/lazy-page";
import { SessionList, type ListCounts } from "../../lib/session-list";
import { PagedSessionList } from "./PagedSessionList";
import { useAccountGroups } from "./account-groups";

vi.mock("../../lib/api", async (original) => ({ ...await original<typeof import("../../lib/api")>(), api: vi.fn() }));
vi.mock("../../lib/store", () => ({
  onHubEvent: () => () => {},
  // the grouped list reads the computers' online state (GroupedSessions, #13522): none here
  useStore: (select: (state: { machines: [] }) => unknown) => select({ machines: [] }),
}));
const agents = vi.hoisted(() => ({ sources: null as AgentSourceStatus[] | null, sessions: [] as { id: string; accountId: string; provider: string }[] }));
vi.mock("../../lib/agents", async (original) => ({
  ...await original<typeof import("../../lib/agents")>(),
  useAgentList: () => ({ sessions: agents.sessions, loaded: true, error: "", hints: [], sources: agents.sources }),
}));
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: ({ count, getItemKey }: { count: number; getItemKey: (i: number) => string }) => ({
    getVirtualItems: () => Array.from({ length: count }, (_, index) => ({ index, key: getItemKey(index), start: index * 32 })),
    getTotalSize: () => count * 32,
    measureElement: () => {},
  }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;

type Row = { id: string; accountId: string; provider: string };
const ROWS: Row[] = [
  { id: "s1", accountId: "acct-grok", provider: "grok_bot" }, { id: "s2", accountId: "acct-dots", provider: "dots" },
  { id: "s3", accountId: "acct-muse", provider: "muse2api" }, { id: "s4", accountId: "acct-gone", provider: "groq" },
  { id: "s5", accountId: "acct-blank", provider: "claude_duet" },
];
const SOURCES: AgentSourceStatus[] = [
  { source: "bridge-a", enabled: true, accounts: [{ accountId: "acct-grok", provider: "grok_bot", displayName: "Grok Bot" }, { accountId: "acct-dots", provider: "dots", displayName: "Dots · Example User" }] },
  { source: "bridge-b", enabled: true, accounts: [{ accountId: "acct-muse", provider: "muse2api", displayName: "Muse" }, { accountId: "acct-blank", provider: "claude_duet", displayName: "  " }] },
];
const identity = (r: Row) => r.id;
const compare = (a: Row, b: Row) => a.id.localeCompare(b.id);
const params = (path: string) => new URL(path, "https://example.com").searchParams;
const tally = (key: (r: Row) => string): Record<string, number> => Object.fromEntries(ROWS.map((r) => [key(r), ROWS.filter((x) => key(x) === key(r)).length]));

let host: HTMLDivElement;
let root: Root;
let list: SessionList<Row>;

beforeAll(() => loadPages(), 60_000);
beforeEach(() => {
  localStorage.clear();
  agents.sources = SOURCES;
  agents.sessions = ROWS;
  vi.mocked(api).mockReset().mockImplementation((path) => {
    const q = params(path as string);
    const rows = ROWS.filter((r) => !q.get("group") || q.get("group") === r.accountId);
    const counts: ListCounts = { total: rows.length, revision: "r", group: tally((r) => r.accountId), state: {}, machine: {}, project: {} };
    return Promise.resolve(q.has("counts") ? counts : { sessions: rows, remove: [], has_more: false, total: rows.length, revision: "r" }) as ReturnType<typeof api>;
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  list = new SessionList<Row>("/agents/sessions", identity, compare);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

function Personal() {
  const accounts = useAccountGroups();
  return <PagedSessionList root={list} path="/agents/sessions" identity={identity} compare={compare} event="agents" personal
    label={accounts.label} accountMark={accounts.accountMark} providerOptions={accounts.providerOptions} renderRow={(r) => <span data-row={r.id}>{r.id}</span>} />;
}
async function byAccount() {
  await list.load();
  await act(async () => root.render(<Personal />));
  const tab = [...host.querySelectorAll<HTMLButtonElement>(`[aria-label="${t("paged-agents.group_by")}"] [role="radio"]`)].find((b) => b.textContent === t("paged-agents.account"))!;
  await act(async () => tab.click());
  await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
}
const sections = () => [...host.querySelectorAll<HTMLElement>("[data-session-group]")];
const heading = (section: HTMLElement) => section.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
/** A heading's name (the text that carries the id as its tooltip) and the mark drawn before it. */
const named = (section: HTMLElement) => {
  const name = heading(section).querySelector<HTMLElement>("[title]")!;
  return { key: section.dataset.sessionGroup, name: name.textContent, tip: name.title, mark: heading(section).querySelector<HTMLElement>("[data-agent-mark]")?.dataset.agentMark };
};

describe("the personal list by account", () => {
  it("names each account and draws its provider's mark; the id is only the tooltip", async () => {
    await byAccount();
    const groups = sections().map(named).sort((a, b) => a.key!.localeCompare(b.key!));
    expect(groups).toEqual([
      { key: "acct-blank", name: "Claude Duet", tip: "acct-blank", mark: "claude" }, // a blank displayName: the provider's label
      { key: "acct-dots", name: "Dots · Example User", tip: "acct-dots", mark: "letter" }, // Dots is not ChatGPT's Blossom
      { key: "acct-gone", name: "Groq", tip: "acct-gone", mark: "letter" }, // not in /agents: its sessions' provider
      { key: "acct-grok", name: "Grok Bot", tip: "acct-grok", mark: "letter" }, // Grok Bot is not xAI's Grok
      { key: "acct-muse", name: "Muse", tip: "acct-muse", mark: "letter" },
    ]);
    for (const s of sections()) expect(heading(s).textContent).not.toContain(s.dataset.sessionGroup!);
  });

  it("names the accounts in the picker too, each with its mark", async () => {
    await byAccount();
    const pill = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.group_filter")}"]`)!;
    await act(async () => pill.click());
    const items = [...document.querySelectorAll<HTMLElement>("[role=menuitemradio]")].map((el) => el.textContent ?? "");
    expect(items.some((x) => x.includes("Dots · Example User"))).toBe(true);
    expect(items.some((x) => x.includes("Grok Bot"))).toBe(true);
    expect(items.every((x) => !/acct-/.test(x))).toBe(true);
  });

  it("falls back to the id only for an account nothing knows", async () => {
    agents.sources = [];
    agents.sessions = [];
    await byAccount();
    const groups = sections().map((s) => heading(s).textContent);
    expect(groups.some((x) => x?.includes("acct-grok"))).toBe(true);
    expect(sections().every((s) => !heading(s).querySelector("[data-agent-mark]"))).toBe(true);
  });

  it("offers the providers with sessions by display name and filters by their id", async () => {
    await byAccount();
    const pill = host.querySelector<HTMLButtonElement>(`button[aria-label="${t("paged-agents.provider")}"]`)!;
    expect(pill.textContent).toContain(t("paged-agents.all_providers"));

    await act(async () => pill.click());
    const choices = [...document.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(choices.map((x) => x.textContent)).toEqual(expect.arrayContaining([
      t("paged-agents.all_providers"), "Claude Duet", "Grok Bot", "Muse", "Groq",
    ]));
    expect(choices.every((x) => !/grok_bot|muse2api|claude_duet/.test(x.textContent ?? ""))).toBe(true);

    const grok = choices.find((x) => x.textContent?.includes("Grok Bot"))!;
    await act(async () => grok.click());
    expect(pill.textContent).toContain("Grok Bot");
    expect(vi.mocked(api).mock.calls.some(([path]) => params(path as string).get("provider") === "grok_bot")).toBe(true);
  });
});
