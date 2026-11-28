// @vitest-environment jsdom
// Token usage (#10042): the totals, the calendar with only a picked day's line, the last 7 days by the hour, by
// computer (a removed one named so), by agent (marks and names; usage kept before the hub counted agents apart reads
// "not split"; none from an older hub) and by kind; the calendar card's line opens it in a dialog.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const hub = vi.hoisted(() => ({ calls: [] as string[], agents: true, projects: true }));
vi.mock("../../lib/api", async (orig) => {
  const real = await orig<typeof import("../../lib/api")>();
  return {
    ...real,
    api: (path: string) => {
      hub.calls.push(path);
      if (path === "/usage/projects") {
        if (!hub.projects) return Promise.reject(new real.ApiError(404, "not found", {}));
        return Promise.resolve({ usage: { zone: "UTC", stored: true, ranges: PROJECT_RANGES_FIXTURE } });
      }
      if (path.startsWith("/usage/projects/detail")) return Promise.resolve({ usage: PROJECT_DETAIL_FIXTURE });
      if (path === "/launchers") return Promise.resolve({ launchers: [{ name: "claude-max", display_name: "Anthropic 订阅" }, { name: "kimi" }] });
      if (path.startsWith("/usage/days")) {
        const days = [
          { date: "2026-11-16", in: 1, out: 2_000_000, cache_write: 30_000_000, cache_read: 5_800_000_000 },
          { date: "2026-11-17", cache_read: 3_200_000_000 },
        ];
        return Promise.resolve({ usage: { zone: "UTC", stored: true, days } });
      }
      const hours = [
        { t: 0, date: "2026-11-16", hour: 9, cache_read: 900_000_000 },
        { t: 3600, date: "2026-11-17", hour: 0, cache_read: 100_000_000, out: 5_000 },
      ];
      return Promise.resolve({
        usage: {
          zone: "UTC",
          stored: true,
          hours,
          machines: [
            { id: "a", name: "Studio", cache_read: 700_000_000 },
            { id: "gone", name: "", cache_read: 300_000_000 },
          ],
          ...(hub.agents
            ? {
                agents: [
                  { agent: "claude", cache_read: 600_000_000 },
                  { agent: "codex", cache_read: 250_000_000, out: 5_000 },
                  { agent: "", cache_read: 150_000_000 },
                ],
              }
            : {}),
          total: { in: 10, out: 5_000, cache_write: 2_000, cache_read: 1_000_000_000 },
        },
      });
    },
  };
});

import TokenUsage from "./TokenUsagePage";
import TokenCalendar from "./TokenCalendar";

// Today's, the last 3 and the last 7 days' projects (#16530): one repository on two computers, one without a remote,
// a folder, the assistant, and the week's not-split part.
const repo = (tokens: number, share: number, machines = 2) => ({
  key: "git:github.com/acme/app", kind: "repo", group: "development", name: "acme/app", host: "github.com", machines, share, tokens: { cache_read: tokens },
});
const PROJECT_RANGES_FIXTURE = [
  { days: 1, total: { cache_read: 100 }, not_split: {}, projects: [repo(100, 1, 1)] },
  { days: 3, total: { cache_read: 400 }, not_split: {}, projects: [repo(300, 0.75), { key: "assistant", kind: "assistant", group: "other", name: "", machines: 1, share: 0.25, tokens: { cache_read: 100 } }] },
  {
    days: 7,
    total: { cache_read: 1000 },
    not_split: { cache_read: 100 },
    projects: [
      repo(500, 0.5),
      { key: "folder:trip", kind: "folder", group: "other", name: "Trip", machines: 1, share: 0.2, tokens: { cache_read: 200 } },
      { key: "local:0123", kind: "local", group: "development", name: "scratch", host: "", machines: 1, share: 0.1, tokens: { cache_read: 100 } },
      { key: "home", kind: "home", group: "other", name: "", machines: 1, share: 0.1, tokens: { cache_read: 100 } },
    ],
  },
];
const breakdown = (days: number, tokens: number) => ({
  days,
  total: { in: 10, cache_read: tokens, req: 42 },
  share: 0.5,
  cache_hit: 0.93,
  machines: [{ key: "a", name: "Studio", tokens: { cache_read: tokens - 1 } }, { key: "gone", tokens: { cache_read: 1 } }],
  agents: [{ key: "claude", tokens: { cache_read: tokens } }],
  models: [{ key: "claude-opus-5-5", tokens: { cache_read: tokens } }],
  launchers: [{ key: "claude-max", tokens: { cache_read: tokens - 2 } }, { key: "kimi", tokens: { cache_read: 1 } }, { key: "", tokens: { cache_read: 1 } }],
  places: [
    { machine: "a", machine_name: "Studio", place: "~/code/app", tokens: { cache_read: tokens / 2 } },
    { machine: "a", machine_name: "Studio", place: "~/code/app-wt/feature", worktree: true, tokens: { cache_read: tokens / 2 } },
  ],
});
const PROJECT_DETAIL_FIXTURE = {
  zone: "UTC",
  stored: true,
  project: repo(500, 0.5),
  ranges: [breakdown(1, 100), breakdown(3, 300), breakdown(7, 500)],
  days: ["2026-11-11", "2026-11-12", "2026-11-13", "2026-11-14", "2026-11-15", "2026-11-16", "2026-11-17"].map((date, i) => ({ date, tokens: { cache_read: 10 * i } })),
  hours: [0, 1, 2].map((hour) => ({ t: hour * 3600, hour, tokens: { cache_read: hour } })),
};

let root: Root;
let host: HTMLDivElement;

beforeEach(() => {
  hub.calls = [];
  hub.agents = true;
  hub.projects = true;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  globalThis.ResizeObserver ??= class {
    observe() {}
    disconnect() {}
    unobserve() {}
  } as unknown as typeof ResizeObserver;
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

async function show(node: React.ReactNode) {
  await act(async () => root.render(node));
  await act(async () => {});
}

describe("TokenUsagePage", () => {
  it("reads a year of days and the week by the hour, and shows every section", async () => {
    await show(<TokenUsage />);
    expect(hub.calls.sort()).toEqual(["/usage/days?days=371", "/usage/hours?days=7", "/usage/projects"]);
    const page = host.querySelector("[data-token-usage-page]")!;
    const text = page.textContent ?? "";
    // the tests run in Chinese
    for (const s of ["今天", "近 7 天", "近 30 天", "每天", "最近 7 天 · 每小时", "按电脑 · 最近 7 天", "按 Agent · 最近 7 天", "按类别 · 最近 7 天"]) {
      expect(text).toContain(s);
    }
    expect(text).toContain("Studio");
    expect(text).toContain("已移除的电脑");
    expect(text).toContain("Claude Code");
    expect(text).toContain("Codex");
    expect(text).toContain("未区分（更新前）");
    expect(page.querySelectorAll("[data-agent-mark]")).toHaveLength(2); // the not-split row has no mark
    // the calendar on the page has no totals line, only a picked day's
    expect(page.querySelector("[data-token-calendar-line]")).toBeNull();
    expect(text).toContain("与「机器」页中的「今天」统计一致");
  });

  it("has no By agent from an older hub", async () => {
    hub.agents = false;
    await show(<TokenUsage />);
    const text = host.textContent ?? "";
    expect(text).toContain("按电脑 · 最近 7 天");
    expect(text).not.toContain("按 Agent");
  });

  it("an hour clicked says its tokens", async () => {
    await show(<TokenUsage />);
    const busy = [...host.querySelectorAll<HTMLElement>("span[title]")].find((s) => s.title.includes("9亿"))!;
    expect(busy).toBeDefined();
    act(() => busy.click());
    expect(host.textContent).toContain("9亿 token");
  });
});

describe("the calendar card's line", () => {
  it("opens Token usage in a dialog, which reads its own days and hours", async () => {
    await show(<TokenCalendar />);
    expect(hub.calls).toEqual(["/usage/days?days=280"]);
    const line = host.querySelector<HTMLButtonElement>("[data-token-calendar-line]")!;
    expect(line.getAttribute("aria-haspopup")).toBe("dialog");
    await act(async () => line.click());
    await act(async () => {}); // the chunk, then its reads
    await act(async () => {});
    const dialog = document.querySelector<HTMLElement>("[role=dialog]")!;
    expect(dialog).not.toBeNull();
    expect(dialog.textContent).toContain("Token 用量");
    expect(dialog.querySelector("[data-token-usage-page]")).not.toBeNull();
    expect(hub.calls).toContain("/usage/hours?days=7");
  });
});

describe("By project (#16530)", () => {
  const open = async () => {
    const picked: { key: string }[] = [];
    await show(<TokenUsage onProject={(p) => p && picked.push(p)} />);
    return picked;
  };

  it("shows the last 7 days by project under the totals, development and non-development apart", async () => {
    await open();
    const page = host.querySelector("[data-token-usage-page]")!;
    const text = page.textContent ?? "";
    expect(text).toContain("按项目");
    expect(text.indexOf("按项目")).toBeLessThan(text.indexOf("每天")); // under the totals, before the calendar
    for (const s of ["开发", "非开发", "acme/app", "scratch", "Trip", "主目录", "未区分（更新前）", "50%"]) expect(text).toContain(s);
    // the repository ran on two computers: a count with its words, the others none
    expect(page.querySelector('[title="2 台电脑"]')).not.toBeNull();
    // the github.com host is not repeated
    expect(text).not.toContain("github.com");
  });

  it("switches its range without asking the hub again", async () => {
    await open();
    const calls = hub.calls.length;
    const today = [...host.querySelectorAll<HTMLButtonElement>("[role=radio]")].find((b) => b.textContent === "今天")!;
    await act(async () => today.click());
    expect(hub.calls.length).toBe(calls);
    const text = host.querySelector("[data-token-projects]")?.textContent ?? "";
    expect(text).toContain("acme/app");
    expect(text).not.toContain("Trip");
    expect(text).not.toContain("未区分（更新前）");
  });

  it("opens a project with a click", async () => {
    const picked = await open();
    const row = [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("acme/app"))!;
    act(() => row.click());
    expect(picked.map((p) => p.key)).toEqual(["git:github.com/acme/app"]);
  });

  it("is not there from an older hub", async () => {
    hub.projects = false;
    await open();
    expect(host.textContent).toContain("每天");
    expect(host.textContent).not.toContain("按项目");
  });

  it("shows a project's breakdown: totals, by day, its worktrees and folders, computers, Agents, models, launchers", async () => {
    await show(<TokenUsage project={repo(500, 0.5) as never} onProject={() => {}} />);
    expect(hub.calls).toContain("/usage/projects/detail?key=git%3Agithub.com%2Facme%2Fapp");
    const page = host.querySelector("[data-token-project]")!;
    const text = page.textContent ?? "";
    for (const s of ["占全部", "请求", "42", "缓存命中", "93%", "每天", "目录与 worktree", "~/code/app", "~/code/app-wt/feature", "按电脑", "Studio", "已移除的电脑", "按 Agent", "Claude Code", "按模型", "按启动器"]) {
      expect(text).toContain(s);
    }
    expect(page.querySelector('[title="关联的 worktree"]')).not.toBeNull();
    // a model by the name the model pickers use, its id on hover
    const model = page.querySelector<HTMLElement>('[title="claude-opus-5-5"]')!;
    expect(model.textContent).toBe("opus 5.5");
    // a launcher by its display name (its own name on hover), one without by its name, none: opened in a terminal
    expect(hub.calls).toContain("/launchers");
    expect(page.querySelector<HTMLElement>('[title="claude-max"]')?.textContent).toBe("Anthropic 订阅");
    for (const s of ["kimi", "终端直接打开"]) expect(text).toContain(s);
    // the chart's line: the largest day at first (11月17日, today), then the one hovered, then the one tapped
    const line = () => page.querySelector("[data-token-project-line]")!.textContent ?? "";
    expect(line()).toContain("今天");
    expect(line()).toContain("60");
    const bars = [...page.querySelectorAll<HTMLButtonElement>("button[aria-pressed]")];
    expect(bars).toHaveLength(7);
    act(() => bars[2].dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    expect(line()).toContain("20");
    act(() => bars[4].click());
    act(() => bars[4].parentElement!.dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: document.body })));
    expect(line()).toContain("40");
    expect(bars[4].getAttribute("aria-pressed")).toBe("true");
    // today: by the hour
    const today = [...page.querySelectorAll<HTMLButtonElement>("[role=radio]")].find((b) => b.textContent === "今天")!;
    await act(async () => today.click());
    expect(page.textContent).toContain("每小时");
  });
});

describe("a project in the dialog", () => {
  it("opens in the same dialog, whose title goes back to the list", async () => {
    await show(<TokenCalendar />);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-token-calendar-line]")!.click());
    await act(async () => {});
    await act(async () => {});
    const dialog = () => document.querySelector<HTMLElement>("[role=dialog]")!;
    const row = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("acme/app"))!;
    await act(async () => row.click());
    await act(async () => {});
    expect(dialog().querySelector("[data-token-project]")).not.toBeNull();
    const back = dialog().querySelector<HTMLButtonElement>('[aria-label="返回「按项目」"]')!;
    expect(back).not.toBeNull();
    await act(async () => back.click());
    expect(dialog().querySelector("[data-token-project]")).toBeNull();
    expect(dialog().textContent).toContain("按项目");
  });
});
