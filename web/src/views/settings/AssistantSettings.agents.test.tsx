// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
const mockStore = vi.hoisted(() => ({ features: [] as string[] }));
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }, { id: "work", name: "Work" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({
  useStore: (select?: (s: { machines: unknown[]; features: string[] }) => unknown) =>
    select ? select({ machines: [], features: mockStore.features }) : [],
  refreshAssistant: vi.fn(),
  onHubEvent: () => () => {},
}));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Assistant › Agent follow-up: "Tell the supervisor about new messages on a task" (#4293).

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
/** The hub's assistant settings; a field left out is one an older hub does not have. */
let stored: Record<string, unknown>;
let writes: Record<string, unknown>[];
/** What a PUT answers instead of saving, when set. */
let answer: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const signals = () => host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.inbox_signals")}"]`);
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  mockStore.features = [];
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  stored = { manager: "assistant", notes: "", agent_tasks: false, inbox_signals: false, archive_closed_days: 3 }; writes = []; answer = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); writes.push(body);
        if (answer) return answer();
        stored = { ...stored, ...body };
      }
      return json(stored);
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }
async function click(node: HTMLElement) { await act(async () => { node.click(); }); await flush(); }

it("shows the hub's value, off by default, beside the other follow-up switches", async () => {
  await render();
  const row = signals()!;
  expect(row.getAttribute("aria-checked")).toBe("false");
  expect(host.textContent).toContain("Agent follow-up");
  expect(host.textContent).toContain("Needs the AI twin");
});

it("saves only its own field and shows what the hub saved", async () => {
  await render();
  await click(signals()!);
  expect(writes).toEqual([{ inbox_signals: true }]);
  expect(signals()!.getAttribute("aria-checked")).toBe("true");
  await click(signals()!);
  expect(writes).toEqual([{ inbox_signals: true }, { inbox_signals: false }]);
  expect(signals()!.getAttribute("aria-checked")).toBe("false");
  expect(toast).not.toHaveBeenCalled();
});

it("goes back with the hub's reason when the change is refused", async () => {
  answer = () => json({ error: "Synthetic refusal" }, 403);
  await render();
  await click(signals()!);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic refusal"), "error");
  expect(signals()!.getAttribute("aria-checked")).toBe("false");
});

it("has no row on a hub before it", async () => {
  delete stored.inbox_signals;
  await render();
  expect(signals()).toBeNull();
  expect(host.textContent).toContain("Agent follow-up"); // the group's other rows stay
});

it("deduplicates inbox_todo_forward into a status link to Integrations Data flow under integrations_v2 + inbox_sources", async () => {
  mockStore.features = ["inbox_sources"];
  stored = { ...stored, integrations_v2: true, inbox_todo_forward: true };
  await render();

  const row = host.querySelector<HTMLAnchorElement>('a[data-settings-row="inbox-todo-forward"]');
  expect(row).not.toBeNull();
  expect(host.querySelector(`[role="switch"][aria-label="${t("settings-assistantsettings.inbox_todo_forward")}"]`)).toBeNull();
  expect(row!.textContent).toContain(t("settings-assistantsettings.inbox_todo_forward"));
  expect(row!.textContent).toContain(t("settings-assistantsettings.inbox_todo_forward_on"));
  expect(row!.getAttribute("href")).toBe("/settings?tab=integrations&manager=assistant&integration=-sources%40assistant");

  await click(row!);
  expect(location.pathname + location.search).toBe("/settings?tab=integrations&manager=assistant&integration=-sources%40assistant");

  // Off state reflects accurately too.
  stored = { ...stored, inbox_todo_forward: false };
  act(() => root.render(null));
  await render();
  const offRow = host.querySelector<HTMLAnchorElement>('a[data-settings-row="inbox-todo-forward"]')!;
  expect(offRow.textContent).toContain(t("settings-assistantsettings.inbox_todo_forward_off"));
});

it("carries the manager parameter and legacy owner on a non-default assistant's Data flow link", async () => {
  mockStore.features = ["inbox_sources"];
  chooseManager("work");
  stored = { ...stored, manager: "work", integrations_v2: true, inbox_todo_forward: false };
  await render();

  const row = host.querySelector<HTMLAnchorElement>('a[data-settings-row="inbox-todo-forward"]');
  expect(row).not.toBeNull();
  expect(row!.getAttribute("href")).toBe("/settings?tab=integrations&manager=work&integration=-sources%40work");
  await click(row!);
  expect(location.pathname + location.search).toBe("/settings?tab=integrations&manager=work&integration=-sources%40work");
});

it("keeps the original inbox_todo_forward switch when integrations_v2 is off or hub lacks inbox_sources", async () => {
  // 1. integrations_v2 is false even when hub supports inbox_sources.
  mockStore.features = ["inbox_sources"];
  stored = { ...stored, integrations_v2: false, inbox_todo_forward: false };
  await render();
  expect(host.querySelector('a[data-settings-row="inbox-todo-forward"]')).toBeNull();
  const sw1 = host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.inbox_todo_forward")}"]`);
  expect(sw1).not.toBeNull();
  expect(sw1!.getAttribute("aria-checked")).toBe("false");
  await click(sw1!);
  expect(writes).toEqual([{ inbox_todo_forward: true }]);

  // 2. integrations_v2 is true, but hub does not expose inbox_sources.
  mockStore.features = [];
  stored = { ...stored, integrations_v2: true, inbox_todo_forward: true };
  act(() => root.render(null));
  await render();
  expect(host.querySelector('a[data-settings-row="inbox-todo-forward"]')).toBeNull();
  const sw2 = host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.inbox_todo_forward")}"]`);
  expect(sw2).not.toBeNull();
  expect(sw2!.getAttribute("aria-checked")).toBe("true");
});
