// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatNumber, t } from "../../i18n";
import { COMPACT_FEATURE } from "../../lib/compaction";
import { setRoute } from "../../lib/feed";
import { clearRateHistories, emptyRateHistory, recordTokens, saveRateHistory } from "../../lib/token-rate";
import type { Item, Session } from "../../lib/types";
import { ContextMeter } from "./ContextMeter";
import { SessionInsightsPopover } from "./SessionInsightsPopover";

const mocks = vi.hoisted(() => ({
  features: [] as string[],
  api: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../../lib/store")>();
  return {
    ...real,
    useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), features: mocks.features }),
  };
});

vi.mock("../../lib/api", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../lib/api")>(),
  api: (...args: unknown[]) => mocks.api(...args),
}));

vi.mock("../../ui/atoms", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../ui/atoms")>(),
  toast: (...args: unknown[]) => mocks.toast(...args),
}));

const baseSession: Session = {
  machine_id: "m1",
  session_id: "s1",
  title: "Session",
  cwd: "/code",
  status: "idle",
  agent: "claude",
  context_tokens: 78_000,
  context_window: 200_000,
};

const sampleItems: Item[] = [
  { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z", text: "hello" },
  {
    id: "a1",
    seq: 2,
    role: "assistant",
    kind: "text",
    ts: "2026-11-20T10:00:14Z",
    usage: [
      { model: "claude-opus-4-5", in: 1000, out: 500, cache_read: 8000, cache_write: 1000 },
      { model: "claude-sonnet-4-5", in: 500, out: 300, cache_read: 2000, cache_write: 200 },
    ],
  },
];

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as Record<string, unknown>).IS_REACT_ACT_ENVIRONMENT = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  mocks.features = [];
  mocks.api.mockReset().mockResolvedValue({});
  mocks.toast.mockReset();
  clearRateHistories();
});

afterEach(async () => {
  await act(async () => {
    root.unmount();
  });
  host.remove();
  setRoute("s1", null);
  clearRateHistories();
});

async function mount(node: React.ReactNode) {
  await act(async () => {
    root.render(node);
  });
}

async function waitForPopover(): Promise<HTMLElement> {
  for (let i = 0; i < 30; i++) {
    const el = document.querySelector<HTMLElement>("[data-session-insights]");
    if (el) return el;
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
    });
  }
  throw new Error("SessionInsightsPopover did not mount");
}

describe("ContextMeter + lazy SessionInsightsPopover", () => {
  it("shows the agy window in the dialog, then just current context when the capacity disappears", async () => {
    const agy = { ...baseSession, agent: "agy", model: "gemini-4-neon-high", context_tokens: 262_144, context_window: 1_048_576 };
    await mount(<ContextMeter session={agy} items={sampleItems} />);
    await act(async () => host.querySelector("button")!.click());
    const dialog = await waitForPopover();
    const section = () => dialog.querySelector("[data-insights-context]");
    expect(section()?.querySelector('[role="progressbar"]')?.getAttribute("aria-valuenow")).toBe("25");
    expect(section()?.textContent).toContain(t("context.summary", { used: 25, left: 75 }));

    await mount(<ContextMeter session={{ ...agy, model: "custom", context_window: 0 }} items={sampleItems} />);
    expect(section()?.textContent).toContain(t("context.current_tokens", { tokens: formatNumber(agy.context_tokens) }));
    expect(section()?.textContent).not.toMatch(/%|\//);
    expect(section()?.querySelector('[role="progressbar"]')).toBeNull();
    expect(dialog.querySelector("[data-insights-tokens]")?.textContent).toContain("13.5k");
  });
  it("opens the lazy popover on click and displays context, tokens, cache hit rate, multi-model, and speed", async () => {
    let h = emptyRateHistory();
    h = recordTokens(h, { turn: "t1", tokens: 40, at: 1000, now: 1000 });
    h = recordTokens(h, { turn: "t1", tokens: 100, at: 3000, now: 3000 });
    h = recordTokens(h, { turn: "t1", tokens: 220, at: 5000, now: 5000 });
    h = recordTokens(h, { turn: "t1", tokens: 340, at: 7000, now: 7000 }); // 240 tok / 4s = 60 tok/s
    saveRateHistory("m1/s1", h);

    const onCompact = vi.fn();
    await mount(<ContextMeter session={baseSession} items={sampleItems} onCompact={onCompact} />);

    // Popover is not in DOM before clicking
    expect(document.querySelector("[data-session-insights]")).toBeNull();

    const trigger = host.querySelector("button")!;
    await act(async () => {
      trigger.click();
    });
    const dialog = await waitForPopover();
    expect(dialog.querySelector("[data-insights-context]")?.textContent).toContain("39%");
    expect(dialog!.querySelector("[data-insights-input]")?.textContent).toBe("1.5k");
    expect(dialog!.querySelector("[data-insights-output]")?.textContent).toBe("800");
    expect(dialog!.querySelector("[data-insights-cache-read]")?.textContent).toBe("10.0k");
    expect(dialog!.querySelector("[data-insights-cache-write]")?.textContent).toBe("1.2k");
    expect(dialog!.querySelector("[data-insights-cache-hit]")?.textContent).toContain("78.7%");
    expect(dialog!.querySelector("[data-insights-models]")?.textContent).toContain("Opus 4.5");
    expect(dialog!.querySelector("[data-insights-models]")?.textContent).toContain("Sonnet 4.5");
    expect(dialog!.querySelector("[data-insights-duration]")?.textContent).toBeTruthy();
    expect(dialog!.querySelector("[data-insights-speed-latest]")?.textContent).toContain("60 tok/s");
    expect(dialog!.querySelector("[data-insights-speed-avg]")?.textContent).toContain("60 tok/s");
    expect(dialog!.querySelector("[data-insights-speed-peak]")?.textContent).toContain("60 tok/s");

    // Clicking /compact calls onCompact and closes the popover
    const compactBtn = dialog!.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    expect(compactBtn).not.toBeNull();
    expect(compactBtn.textContent).toContain("/compact");
    await act(async () => {
      compactBtn.click();
    });
    expect(onCompact).toHaveBeenCalledTimes(1);
    expect(document.querySelector("[data-session-insights]")).toBeNull();
  });

  it("renders token-only pill when context_window is unknown and honestly omits unavailable sections", async () => {
    const noWindow: Session = {
      ...baseSession,
      context_tokens: undefined,
      context_window: undefined,
      source_kind: "antigravity",
    };
    const noCacheItems: Item[] = [
      { id: "a1", seq: 1, role: "assistant", kind: "text", usage: [{ model: "gpt-5.4-codex", in: 3000, out: 1200 }] },
    ];

    await mount(<ContextMeter session={noWindow} items={noCacheItems} />);
    const trigger = host.querySelector("button")!;
    expect(trigger.textContent).toContain("4.2k tok");
    expect(host.querySelector('[role="progressbar"]')).toBeNull();

    await act(async () => {
      trigger.click();
    });
    const dialog = await waitForPopover();
    expect(dialog.querySelector("[data-insights-context]")).toBeNull();
    expect(dialog.querySelector("[data-insights-cache-read]")).toBeNull();
    expect(dialog.querySelector("[data-insights-cache-hit]")).toBeNull();
    expect(dialog.querySelector("[data-insights-performance]")).toBeNull();
    // Runtime session with source_kind does not offer /compact
    expect(dialog.querySelector("[data-insights-compact]")).toBeNull();
    expect(dialog.querySelector("[data-insights-model-single]")?.textContent).toContain("5.4 Codex");
  });

  it("triggers assistant compaction endpoint when hub supports assistant_compact", async () => {
    mocks.features = [COMPACT_FEATURE];
    const events: CustomEvent[] = [];
    const onEvent = (e: Event) => events.push(e as CustomEvent);
    window.addEventListener("persona:compact-requested", onEvent);

    const anchor = { current: host };
    const onClose = vi.fn();
    try {
      await mount(
        <SessionInsightsPopover
          anchor={anchor}
          session={{ ...baseSession, assistant: true }}
          items={sampleItems}
          assistant
          onClose={onClose}
          breakdown={{
            source: "exact",
            system_and_rules: 8000,
            tools_and_mcp: 12000,
            conversation: 28000,
            tool_outputs: 30000,
            free: 122000,
            auto_compact_threshold: 160000,
          }}
          ttftMs={350}
        />,
      );

      const dialog = document.querySelector("[data-session-insights]")!;
      expect(dialog.querySelector("[data-insights-stacked-bar]")).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-segment="system_and_rules"]')).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-segment="tools_and_mcp"]')).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-segment="conversation"]')).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-segment="tool_outputs"]')).not.toBeNull();
      expect(dialog.querySelector("[data-breakdown-compact-marker]")).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-source="exact"]')).not.toBeNull();
      expect(dialog.querySelector('[data-breakdown-top-consumer="tool_outputs"]')).not.toBeNull();
      expect(dialog.querySelector("[data-insights-ttft]")?.textContent).toContain("350 ms");
      expect(dialog.querySelector("[data-insights-speed-latest]")?.textContent).toBe("57.1 tok/s");
      expect(dialog.querySelector("[data-insights-speed-peak]")).toBeNull();

      const compactBtn = dialog.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
      expect(compactBtn).not.toBeNull();
      expect(compactBtn.disabled).toBe(false);

      await act(async () => {
        compactBtn.click();
      });

      expect(mocks.api).toHaveBeenCalledWith("/assistant/conversations/s1/compact", {
        method: "POST",
        json: { machine_id: "m1" },
      });
      expect(events).toHaveLength(1);
      expect(events[0].detail).toEqual({ mid: "m1", sid: "s1" });
      expect(onClose).toHaveBeenCalledTimes(1);
    } finally {
      window.removeEventListener("persona:compact-requested", onEvent);
    }
  });

  it("hides assistant compact action when hub feature is absent and disables while compacting", async () => {
    const anchor = { current: host };
    await mount(
      <SessionInsightsPopover
        anchor={anchor}
        session={{ ...baseSession, assistant: true }}
        assistant
        onClose={() => {}}
      />,
    );
    expect(document.querySelector("[data-insights-compact]")).toBeNull();

    mocks.features = [COMPACT_FEATURE];
    await mount(
      <SessionInsightsPopover
        anchor={anchor}
        session={{ ...baseSession, assistant: true, compaction: { state: "running", started_at: "2026-11-20T10:00:00Z" } }}
        assistant
        onClose={() => {}}
      />,
    );
    const btn = document.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    expect(btn).not.toBeNull();
    expect(btn.disabled).toBe(true);
  });

  it("closes on Escape key and close button", async () => {
    await mount(<ContextMeter session={baseSession} items={sampleItems} />);
    const trigger = host.querySelector("button")!;
    await act(async () => {
      trigger.click();
    });
    await waitForPopover();
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector("[data-session-insights]")).toBeNull();

    await act(async () => {
      trigger.click();
    });
    const dialog = await waitForPopover();
    await act(async () => {
      dialog.querySelector<HTMLButtonElement>("[data-insights-close]")!.click();
    });
    expect(document.querySelector("[data-session-insights]")).toBeNull();
  });

  it("hides /compact when a task route is active or compactBlocked is set", async () => {
    const anchor = { current: host };
    const onCompact = vi.fn();

    setRoute("s1", "todo-1");
    await mount(
      <SessionInsightsPopover anchor={anchor} session={baseSession} items={sampleItems} onCompact={onCompact} onClose={() => {}} />,
    );
    expect(document.querySelector("[data-insights-compact]")).toBeNull();

    await act(async () => setRoute("s1", null));
    await mount(
      <SessionInsightsPopover anchor={anchor} session={baseSession} items={sampleItems} onCompact={onCompact} compactBlocked onClose={() => {}} />,
    );
    expect(document.querySelector("[data-insights-compact]")).toBeNull();
  });

  it("toasts and keeps popover open when onCompact resolves false or assistant compaction fails", async () => {
    const anchor = { current: host };
    const onClose = vi.fn();
    const onCompact = vi.fn().mockResolvedValue(false);

    await mount(
      <SessionInsightsPopover anchor={anchor} session={baseSession} items={sampleItems} onCompact={onCompact} onClose={onClose} />,
    );
    const btn = document.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    await act(async () => {
      btn.click();
    });
    expect(onCompact).toHaveBeenCalledTimes(1);
    expect(mocks.toast).toHaveBeenCalledWith(expect.any(String), "error");
    expect(onClose).not.toHaveBeenCalled();
    expect(document.querySelector("[data-session-insights]")).not.toBeNull();

    mocks.toast.mockReset();
    mocks.features = [COMPACT_FEATURE];
    mocks.api.mockRejectedValueOnce(new Error("busy"));
    await mount(
      <SessionInsightsPopover anchor={anchor} session={{ ...baseSession, assistant: true }} assistant onClose={onClose} />,
    );
    const asstBtn = document.querySelector<HTMLButtonElement>("[data-insights-compact]")!;
    await act(async () => {
      asstBtn.click();
    });
    expect(mocks.toast).toHaveBeenCalledWith(expect.any(String), "error");
    expect(onClose).not.toHaveBeenCalled();
  });

  it("renders separate rows when the same model has both normal and fast speed usage", async () => {
    const anchor = { current: host };
    const dualSpeedItems: Item[] = [
      {
        id: "a1",
        seq: 1,
        role: "assistant",
        kind: "text",
        usage: [
          { model: "claude-opus-4-5", in: 2000, out: 1000 },
          { model: "claude-opus-4-5", fast: true, in: 1000, out: 500 },
        ],
      },
    ];
    await mount(
      <SessionInsightsPopover anchor={anchor} session={baseSession} items={dualSpeedItems} onClose={() => {}} />,
    );
    const rows = [...document.querySelectorAll("[data-insights-models] li")].map((li) => li.textContent ?? "");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toContain("Opus 4.5");
    expect(rows[0]).not.toContain("Fast");
    expect(rows[1]).toContain("Opus 4.5");
    expect(rows[1]).toContain("Fast");
  });

  it("displays identical cumulative totals in the ContextMeter pill and popover when subagent report fields move in opposite directions", async () => {
    const noWindow: Session = { ...baseSession, context_tokens: undefined, context_window: undefined };
    const reportItems: Item[] = [
      {
        id: "1",
        seq: 1,
        role: "assistant",
        kind: "text",
        usage: [{ id: "sub-1", model: "claude-opus-4-5", in: 1200, out: 400 }],
      },
      {
        id: "2",
        seq: 2,
        role: "assistant",
        kind: "text",
        usage: [{ id: "sub-1", model: "claude-opus-4-5", in: 900, out: 700 }],
      },
    ];
    await mount(<ContextMeter session={noWindow} items={reportItems} />);
    const trigger = host.querySelector("button")!;
    expect(trigger.textContent).toContain("1.9k tok");

    await act(async () => {
      trigger.click();
    });
    const dialog = await waitForPopover();
    expect(dialog.querySelector("[data-insights-tokens]")?.textContent).toContain("1.9k");
    expect(dialog.querySelector("[data-insights-input]")?.textContent).toBe("1.2k");
    expect(dialog.querySelector("[data-insights-output]")?.textContent).toBe("700");
  });

  it("renders an estimated agy breakdown with labeled turn rates and observed TTFT", async () => {
    const anchor = { current: host };
    const sessionWithBreakdown: Session = {
      ...baseSession,
      agent: "agy",
      context_tokens: 100_000,
      context_window: 200_000,
      context_breakdown: {
        source: "estimated",
        system_and_rules: 15_000,
        tools_and_mcp: 10_000,
        conversation: 25_000,
        tool_outputs: 50_000,
        free: 100_000,
        auto_compact_threshold: 160_000,
      },
    };
    const itemsWithTtft: Item[] = [
      { id: "u1", seq: 1, role: "user", kind: "text", ts: "2026-11-20T10:00:00Z", text: "go" },
      { id: "a1", seq: 2, role: "assistant", kind: "text", ts: "2026-11-20T10:00:08Z", ttft_ms: 1450, usage: [{ out: 320 }] },
    ];
    await mount(
      <SessionInsightsPopover anchor={anchor} session={sessionWithBreakdown} items={itemsWithTtft} onClose={() => {}} />,
    );
    const dialog = document.querySelector("[data-session-insights]")!;
    expect(dialog.querySelector('[data-insights-stacked-bar] [data-breakdown-segment="tools_and_mcp"]')).not.toBeNull();
    expect(dialog.querySelector('[data-breakdown-source="estimated"]')?.textContent).toContain("~");
    expect(dialog.querySelector('[data-breakdown-row="tool_outputs"]')?.textContent).toContain("~50.0k");
    expect(dialog.querySelector('[data-breakdown-row="free"]')?.textContent).toContain("100.0k");
    expect(dialog.querySelector("[data-breakdown-auto-compact]")?.textContent).toContain("160.0k");
    expect(dialog.querySelector('[data-breakdown-top-consumer="tool_outputs"]')?.textContent).toContain("50%");
    expect(dialog.querySelector("[data-insights-ttft]")?.textContent).toBe("1.45 s");
    expect(dialog.querySelector("[data-insights-speed-latest]")?.textContent).toBe("40 tok/s");
    expect(dialog.querySelector("[data-insights-speed-avg]")?.textContent).toBe("40 tok/s");
    expect(dialog.querySelector("[data-insights-speed-latest]")?.previousElementSibling?.textContent).toBe("最近实测轮次速率");
    expect(dialog.querySelector("[data-insights-speed-peak]")).toBeNull();
  });

  it("preserves observed 0 ms TTFT in the performance section", async () => {
    const anchor = { current: host };
    await mount(
      <SessionInsightsPopover
        anchor={anchor}
        session={baseSession}
        items={[{ id: "a1", seq: 1, role: "assistant", kind: "text", ttft_ms: 0 }]}
        onClose={() => {}}
      />,
    );
    expect(document.querySelector("[data-insights-ttft]")?.textContent).toBe("0 ms");
  });
});
