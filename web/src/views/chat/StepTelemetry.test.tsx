// @vitest-environment jsdom
// L3 per-step attribution (#13054): expanded tool calls / step cards show per-step token consumption,
// elapsed duration, and optional TTFT badges; collapsed rows stay uncluttered.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { buildTaskIndex } from "../../lib/tasks";
import type { Item } from "../../lib/types";
import type { ToolStep } from "../../lib/work-groups";
import { ActionRow } from "./ActionRow";
import { ChatContext } from "./chat-context";
import { forgetExpanded } from "./expanded";
import { SubagentCard } from "./SubagentCard";
import { ThinkingRow } from "./ThinkingRow";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const T0 = Date.parse("2026-11-22T10:00:00Z");
const atMs = (ms: number) => new Date(T0 + ms).toISOString();

let host: HTMLDivElement;
let root: Root;

beforeEach(async () => {
  await initI18n("en");
  forgetExpanded();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

async function waitForTelemetry(container: ParentNode = host): Promise<HTMLElement> {
  for (let i = 0; i < 30; i++) {
    const el = container.querySelector<HTMLElement>("[data-step-telemetry]");
    if (el) return el;
    await act(async () => {
      await vi.dynamicImportSettled();
      await new Promise((r) => setTimeout(r, 10));
    });
  }
  throw new Error("StepTelemetry did not mount");
}

describe("L3 per-step attribution on expanded step cards (#13054)", () => {
  it("keeps collapsed ActionRow uncluttered and reveals a metadata line on expand", async () => {
    const use: Item = {
      id: "t1#0",
      seq: 1,
      role: "assistant",
      kind: "tool_use",
      tool_name: "Bash",
      tool_use_id: "c1",
      tool_input: { command: "npm test" },
      ts: atMs(0),
      ttft_ms: 280,
      usage: [{ model: "claude-opus-4-5", in: 1200, out: 400, cache_read: 8400 }],
    };
    const result: Item = {
      id: "r1#0",
      seq: 2,
      role: "user",
      kind: "tool_result",
      tool_use_id: "c1",
      text: "all passed",
      ts: atMs(2450),
    };
    const step: ToolStep = { kind: "tool", id: use.id, use, result, images: [] };

    await act(async () => {
      root.render(<ActionRow step={step} mid="m1" sid="s1" />);
    });

    // Collapsed: no L3 telemetry badges in DOM
    expect(host.querySelector("[data-step-telemetry]")).toBeNull();
    expect(host.querySelector("[data-inline-detail]")).toBeNull();

    // Expand row: lazy StepTelemetry mounts inside detail panel
    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-action-row] > button")!.click();
    });
    const bar = await waitForTelemetry();
    expect(bar.querySelector("[data-step-tokens]")?.textContent).toContain("10.0k tok");
    expect(bar.querySelector("[data-step-tokens]")?.getAttribute("title")).toContain("Input 1.2k");
    expect(bar.querySelector("[data-step-tokens]")?.getAttribute("title")).toContain("Cache read 8.4k");
    expect(bar.querySelector("[data-step-duration]")?.textContent).toContain("2.45 s");
    expect(bar.querySelector("[data-step-ttft]")?.textContent).toContain("TTFT 280 ms");
    expect(bar.textContent).toBe("10.0k tok·2.45 s·TTFT 280 ms");
    expect(bar.querySelector("svg")).toBeNull();
    expect(bar.innerHTML).not.toContain("rounded-chip");
  });

  it("shows sub-second elapsed duration badge on expanded ActionRow and omits badges when telemetry is absent", async () => {
    const fastUse: Item = {
      id: "t2#0",
      seq: 1,
      role: "assistant",
      kind: "tool_use",
      tool_name: "Grep",
      tool_use_id: "c2",
      tool_input: { pattern: "foo" },
      ts: atMs(0),
    };
    const fastResult: Item = {
      id: "r2#0",
      seq: 2,
      role: "user",
      kind: "tool_result",
      tool_use_id: "c2",
      text: "match",
      ts: atMs(420),
    };
    await act(async () => {
      root.render(<ActionRow step={{ kind: "tool", id: fastUse.id, use: fastUse, result: fastResult, images: [] }} mid="m1" sid="s1" />);
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-action-row] > button")!.click();
    });
    const bar = await waitForTelemetry();
    expect(bar.querySelector("[data-step-duration]")?.textContent).toBe("420 ms");
    expect(bar.querySelector("[data-step-tokens]")).toBeNull();
    expect(bar.querySelector("[data-step-ttft]")).toBeNull();

    // No telemetry: no badge bar rendered at all
    const bareUse: Item = { id: "t3#0", seq: 3, role: "assistant", kind: "tool_use", tool_name: "Bash", tool_use_id: "c3", tool_input: { command: "ls" } };
    await act(async () => {
      root.render(<ActionRow step={{ kind: "tool", id: bareUse.id, use: bareUse, images: [] }} mid="m1" sid="s1" />);
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>("[data-action-row] > button")!.click();
    });
    expect(host.querySelector("[data-inline-detail]")).not.toBeNull();
    expect(host.querySelector("[data-step-telemetry]")).toBeNull();
  });

  it("shows L3 token & TTFT badges when expanding a multi-line ThinkingRow", async () => {
    const item: Item = {
      id: "th1#0",
      seq: 1,
      role: "assistant",
      kind: "thinking",
      text: "First line of reasoning\nSecond line of reasoning",
      ttft_ms: 190,
      usage: [{ model: "claude-opus-4-5", in: 500, out: 1500 }],
    };
    await act(async () => {
      root.render(<ThinkingRow item={item} />);
    });
    expect(host.querySelector("[data-step-telemetry]")).toBeNull();

    await act(async () => {
      host.querySelector<HTMLElement>('[role="button"]')!.click();
    });
    const bar = host.querySelector<HTMLElement>("[data-step-telemetry]")!;
    expect(bar).not.toBeNull();
    expect(bar.querySelector("[data-step-tokens]")?.textContent).toContain("2.0k tok");
    expect(bar.querySelector("[data-step-ttft]")?.textContent).toContain("TTFT 190 ms");
  });

  it("keeps hook order stable when ThinkingRow transitions between empty/whitespace and non-empty text", async () => {
    const base: Item = {
      id: "th-stream#0",
      seq: 1,
      role: "assistant",
      kind: "thinking",
      text: "",
      ttft_ms: 140,
      usage: [{ model: "claude-opus-4-5", in: 300, out: 700 }],
    };

    // Start empty -> whitespace -> non-empty -> empty -> non-empty again on the same component instance.
    await act(async () => {
      root.render(<ThinkingRow item={base} />);
    });
    expect(host.querySelector('[data-thinking="words"]')).toBeNull();

    await act(async () => {
      root.render(<ThinkingRow item={{ ...base, text: "   \n\t  " }} />);
    });
    expect(host.querySelector('[data-thinking="words"]')).toBeNull();

    await act(async () => {
      root.render(<ThinkingRow item={{ ...base, text: "Streamed thought line 1\nStreamed thought line 2" }} />);
    });
    const rowBtn = host.querySelector<HTMLElement>('[role="button"]')!;
    expect(rowBtn).not.toBeNull();
    await act(async () => {
      rowBtn.click();
    });
    expect(host.querySelector("[data-step-tokens]")?.textContent).toContain("1.0k tok");
    expect(host.querySelector("[data-step-ttft]")?.textContent).toContain("TTFT 140 ms");

    // Transition back to empty/whitespace (e.g. page merge) without violating Rules of Hooks.
    await act(async () => {
      root.render(<ThinkingRow item={{ ...base, text: "   " }} />);
    });
    expect(host.querySelector('[data-thinking="words"]')).toBeNull();

    // Transition back to non-empty again.
    await act(async () => {
      root.render(<ThinkingRow item={{ ...base, text: "Recovered line 1\nRecovered line 2" }} />);
    });
    expect(host.querySelector('[data-thinking="words"]')).not.toBeNull();
    expect(host.querySelector("[data-step-tokens]")?.textContent).toContain("1.0k tok");
  });

  it("shows L3 token and duration badges inside an expanded SubagentCard", async () => {
    const use: Item = {
      id: "ag1#0",
      seq: 1,
      role: "assistant",
      kind: "tool_use",
      tool_name: "Agent",
      tool_use_id: "c-ag1",
      tool_input: { description: "Analyze bundle", prompt: "Inspect chunks" },
      ts: atMs(0),
    };
    const result: Item = {
      id: "ag1-r#0",
      seq: 2,
      role: "user",
      kind: "tool_result",
      tool_use_id: "c-ag1",
      text: "Done inspecting",
      ts: atMs(35_000),
      usage: [{ id: "agent:sub1", model: "claude-sonnet-4-5", in: 2000, out: 1000, cache_read: 9000 }],
    };
    const step: ToolStep = { kind: "tool", id: use.id, use, result, images: [] };

    await act(async () => {
      root.render(<SubagentCard step={step} status="completed" mid="m1" sid="s1" />);
    });
    expect(host.querySelector("[data-step-telemetry]")).toBeNull();

    await act(async () => {
      host.querySelector<HTMLButtonElement>("button")!.click();
    });
    const bar = host.querySelector<HTMLElement>("[data-step-telemetry]")!;
    expect(bar).not.toBeNull();
    expect(bar.querySelector("[data-step-tokens]")?.textContent).toContain("12.0k tok");
    expect(bar.querySelector("[data-step-duration]")?.textContent).toContain("35s");
  });

  it("deduplicates synchronous subagent result present in both step.result and tasks.reports (id-less usage counted once)", async () => {
    const use: Item = {
      id: "ag-sync#0",
      seq: 1,
      role: "assistant",
      kind: "tool_use",
      tool_name: "Agent",
      tool_use_id: "c-ag-sync",
      tool_input: { description: "Sync check", prompt: "Inspect files" },
      ts: atMs(0),
      usage: [{ model: "claude-opus-4-5", in: 500, out: 100 }],
    };
    const result: Item = {
      id: "ag-sync-r#0",
      seq: 2,
      role: "user",
      kind: "tool_result",
      tool_use_id: "c-ag-sync",
      text: "Sync inspection complete",
      ts: atMs(12_000),
      // Id-less usage on a synchronous result that buildTaskIndex also places in tasks.reports
      usage: [{ model: "claude-sonnet-4-5", in: 1000, out: 400, cache_read: 2000 }],
    };
    const tasks = buildTaskIndex([use, result]);
    expect(tasks.reports.get("c-ag-sync")).toEqual([result]);

    const step: ToolStep = { kind: "tool", id: use.id, use, result, images: [] };
    await act(async () => {
      root.render(
        <ChatContext.Provider value={{ tasks, subagents: [], scope: "" }}>
          <SubagentCard step={step} status="completed" mid="m1" sid="s1" />
        </ChatContext.Provider>,
      );
    });
    await act(async () => {
      host.querySelector<HTMLButtonElement>("button")!.click();
    });
    const bar = host.querySelector<HTMLElement>("[data-step-telemetry]")!;
    expect(bar).not.toBeNull();
    // 600 (use) + 3400 (result counted once, NOT 600 + 6800 = 7400) = 4.0k tok
    expect(bar.querySelector("[data-step-tokens]")?.textContent).toContain("4.0k tok");
    expect(bar.querySelector("[data-step-tokens]")?.getAttribute("title")).toContain("Input 1.5k · Output 500");
    expect(bar.querySelector("[data-step-tokens]")?.getAttribute("title")).toContain("Cache read 2.0k");
    expect(bar.querySelector("[data-step-duration]")?.textContent).toContain("12s");
  });
});
