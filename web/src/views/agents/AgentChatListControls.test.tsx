// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
  g.matchMedia = (q: string) => ({
    matches: false,
    media: q,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
  });
});
import { t } from "../../i18n";
import { getAgentListFilters, filterAgentList, resetAgentListFilters } from "../../lib/agent-list-filters";
import { closePalette, getPalette } from "../../lib/palette";
import { getState, reset, savePins } from "../../lib/store";
import type { Machine, Session } from "../../lib/types";
import { AgentsList, Sidebar } from "../Sidebar";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const machines: Machine[] = [
  { id: "gcp", name: "Cloud Dev", online: true },
  { id: "mac", name: "Mac Studio", online: true },
];

const sessions: Session[] = [
  {
    machine_id: "gcp",
    session_id: "s1",
    title: "Implement Agents Tab Filter Bar",
    cwd: "/workspace/persona",
    repo_root: "/workspace/persona",
    status: "working",
    state: "working",
    agent: "claude",
    model: "claude-opus-5-5",
    updated_at: "2026-11-09T11:30:00Z",
    last_text: "Updated composable filter bar and chat-list rows.",
  },
  {
    machine_id: "mac",
    session_id: "s2",
    title: "Fix WebSocket Reconnect",
    cwd: "/workspace/orca",
    repo_root: "/workspace/orca",
    status: "blocked",
    state: "needs_you",
    agent: "codex",
    model: "gpt-5",
    updated_at: "2026-11-09T11:00:00Z",
    asks: "Should we retry after 5 seconds?",
    turn_error: "Socket closed unexpectedly",
  },
];

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  reset();
  resetAgentListFilters();
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      if (String(url).includes("/pins")) {
        return Promise.resolve(new Response(JSON.stringify({ spaces: [], sessions: ["mac/s2"] })));
      }
      return Promise.resolve(new Response("{}"));
    }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
  resetAgentListFilters();
  closePalette();
  vi.unstubAllGlobals();
});

const menuRow = (label: string) =>
  [...document.querySelectorAll<HTMLElement>('[role="menuitem"], [role="menuitemradio"], [role="menuitemcheckbox"]')].find((el) =>
    el.textContent?.includes(label),
  );

async function openFilterMenu() {
  await act(async () => {
    document.querySelector<HTMLButtonElement>('[data-testid="agent-filter-button"]')!.click();
  });
}

async function chooseRow(label: string) {
  const row = menuRow(label);
  expect(row, `menu row ${label}`).toBeTruthy();
  await act(async () => {
    row!.click();
  });
}

function seed() {
  Object.assign(getState(), {
    loaded: true,
    machines,
    sessions: { gcp: [sessions[0]], mac: [sessions[1]] },
    pins: { spaces: [], sessions: ["mac/s2"] },
    todosLoaded: true,
  });
}

describe("Agents list Filter menu (#4397) on the title row, the search in ⌘K (#6589)", () => {
  it("has no search row: the Filter button sits on the title row, and nothing is between the quick filters and the list", async () => {
    seed();
    await act(async () => {
      root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />);
    });
    expect(host.querySelector("input")).toBeNull();
    expect(host.querySelector('[data-testid="agent-dimension-filter-bar"]')).toBeNull();
    const button = host.querySelector<HTMLElement>('[data-sidebar-agents-controls] [data-testid="agent-filter-button"]')!;
    expect(button).not.toBeNull();
    // beside the title row's other menu button, nothing counted yet
    expect(button.closest(".group\\/sec")).not.toBeNull();
    expect(button.textContent).toBe("");
    expect(host.querySelector('[data-testid="agent-filter-chips"]')).toBeNull();
    expect(host.querySelector('[data-testid="scoped-filter-strip"]')).toBeNull();
    expect(host.querySelector('[data-testid="search-as-code-plan"]')).toBeNull();
    // the grouped list stays the default; chat-list rows only when picked
    expect(host.querySelectorAll('[data-testid="agent-chatlist-row"]')).toHaveLength(0);
  });

  it("filters from the menu, shows removable chips, and holds the chat-list view and Search as Code", async () => {
    seed();
    await act(async () => {
      root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />);
    });

    await openFilterMenu();
    await chooseRow(t("lib-agent-filter.chat_list_view"));
    const rows = [...host.querySelectorAll<HTMLElement>('[data-testid="agent-chatlist-row"]')];
    expect(rows.map((r) => r.getAttribute("data-session-id"))).toEqual(["s2", "s1"]);
    expect(rows[0].getAttribute("data-pinned")).toBe("true");
    expect(rows[0].textContent).toContain("Mac Studio");
    expect(rows[0].textContent).toContain("orca");

    // a facet page: pick one machine, the menu stays open and the chip appears
    await openFilterMenu();
    await chooseRow(t("lib-agent-filter.filter_machine"));
    await chooseRow("Cloud Dev");
    expect(menuRow("Mac Studio")).toBeTruthy();
    expect([...host.querySelectorAll<HTMLElement>('[data-testid="agent-chatlist-row"]')].map((r) => r.getAttribute("data-session-id"))).toEqual(["s1"]);
    const strip = host.querySelector<HTMLElement>('[data-testid="scoped-filter-strip"]')!;
    expect(strip.textContent).toContain(`${t("lib-agent-filter.filter_machine")}: Cloud Dev`);
    expect(host.querySelector('[data-testid="agent-filter-button"]')!.textContent).toBe("1");

    // the chip's × removes it, and the strip goes with the last one
    await act(async () => {
      strip.querySelector<HTMLButtonElement>("button[aria-label]")!.click();
    });
    expect(host.querySelector('[data-testid="scoped-filter-strip"]')).toBeNull();
    expect(host.querySelectorAll('[data-testid="agent-chatlist-row"]')).toHaveLength(2);

    // Search as Code: off the list until turned on in the menu, then its plan bar, which closes it again
    await act(async () => {
      document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    await openFilterMenu();
    await chooseRow(t("lib-agent-filter.search_as_code"));
    expect(host.querySelector('[data-testid="search-as-code-plan"]')).not.toBeNull();
    await act(async () => {
      host.querySelector<HTMLButtonElement>('[data-testid="search-as-code-off"]')!.click();
    });
    expect(host.querySelector('[data-testid="search-as-code-plan"]')).toBeNull();

    await act(async () => {
      savePins({ spaces: [], sessions: ["gcp/s1"] });
    });
    const updatedRows = [...host.querySelectorAll<HTMLElement>('[data-testid="agent-chatlist-row"]')];
    expect(updatedRows[0].getAttribute("data-session-id")).toBe("s1");
    expect(updatedRows[0].getAttribute("data-pinned")).toBe("true");
  });
  it("narrows by the query ⌘K put on the list: a chip that reopens ⌘K, counted on the button, cleared by × or Reset", async () => {
    seed();
    localStorage.setItem("persona.agentChatList", "1");
    await act(async () => {
      root.render(<AgentsList route={{ name: "agent-list" }} onNewAgent={() => {}} />);
    });
    const rows = () => [...host.querySelectorAll<HTMLElement>('[data-testid="agent-chatlist-row"]')].map((r) => r.getAttribute("data-session-id"));
    expect(rows()).toEqual(["s2", "s1"]);

    // the list's operators, as its own field took them
    await act(async () => filterAgentList("machine:gcp"));
    expect(rows()).toEqual(["s1"]);
    const button = () => host.querySelector<HTMLElement>('[data-testid="agent-filter-button"]')!;
    expect(button().textContent).toBe("1");
    const chip = host.querySelector<HTMLElement>('[data-chip="query"]')!;
    expect(chip.textContent).toBe(`${t("lib-agent-filter.query_chip")}: machine:gcp`);

    // its text opens ⌘K on it, to change it
    await act(async () => chip.querySelector<HTMLButtonElement>("button")!.click());
    expect(getPalette()).toEqual({ open: true, query: "machine:gcp" });

    // words match what the list's Full text reads: the error and the question of the other one
    await act(async () => filterAgentList("socket"));
    expect(rows()).toEqual(["s2"]);

    // with a facet too, the count is two; Reset clears both
    await openFilterMenu();
    await chooseRow(t("lib-agent-filter.filter_machine"));
    await chooseRow("Mac Studio");
    expect(button().textContent).toBe("2");
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="scoped-filter-reset"]')!.click());
    expect(getAgentListFilters().query).toBe("");
    expect(getAgentListFilters().machines).toEqual([]);
    expect(rows()).toEqual(["s2", "s1"]);
    expect(button().textContent).toBe("");

    // × on the query's chip clears it alone
    await act(async () => filterAgentList("orca"));
    expect(rows()).toEqual(["s2"]);
    await act(async () => host.querySelector<HTMLButtonElement>('[data-chip="query"] button[aria-label]')!.click());
    expect(rows()).toEqual(["s2", "s1"]);
  });

  it("opens the block folded under its title when ⌘K puts words on the list", async () => {
    seed();
    localStorage.setItem("fold:agents", "1");
    await act(async () => {
      root.render(<Sidebar route={{ name: "home" }} onSearch={() => {}} onNewAgent={() => {}} />);
    });
    const block = () => host.querySelector<HTMLButtonElement>("[data-sidebar-agents-controls] button[aria-expanded]")!;
    expect(block().getAttribute("aria-expanded")).toBe("false");
    await act(async () => filterAgentList("orca"));
    expect(block().getAttribute("aria-expanded")).toBe("true");
  });
});
