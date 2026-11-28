// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
const hub = vi.hoisted(() => ({ features: [] as string[] }));
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }, { id: "m-work", name: "Work" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({
  useStore: (select: (s: { features: string[]; machines: unknown[] }) => unknown) => select({ features: hub.features, machines: [] }),
  refreshAssistant: vi.fn(),
}));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Assistant › "Let Persona make dashboards" (#15017): shown only when the hub announces assistant_pages, and
// only for the default manager; off unless the hub says on (absent is off); saved alone, taken back when refused.

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let stored: Record<string, unknown>;
let writes: Record<string, unknown>[];
let answer: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const toggle = () => host.querySelector<HTMLButtonElement>(`[role="switch"][aria-label="${t("settings-assistantsettings.dashboards")}"]`);
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  hub.features = ["assistant_pages"];
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  stored = { manager: "assistant", notes: "", dashboards: false }; writes = []; answer = undefined;
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

it("is hidden when the hub does not announce assistant_pages", async () => {
  hub.features = [];
  stored = { ...stored, dashboards: true }; // even if the hub sent the field
  await render();
  expect(toggle()).toBeNull();
  expect(host.textContent).not.toContain(t("settings-assistantsettings.dashboards"));
  expect(host.textContent).not.toContain(t("settings-assistantsettings.dashboards_footer"));
});

it("shows the hub's value, off by default and when absent, with its one footnote", async () => {
  await render();
  expect(toggle()!.getAttribute("aria-checked")).toBe("false");
  expect(host.textContent).toContain("Let Persona make dashboards");
  expect(host.textContent).toContain("When on, the assistant can draft pages you come back to; tap Keep in the chat and they appear in the sidebar.");
  act(() => root.unmount()); root = createRoot(host);
  delete stored.dashboards;
  await render();
  expect(toggle()!.getAttribute("aria-checked")).toBe("false");
});

it("saves only its own field and shows what the hub saved", async () => {
  await render();
  await click(toggle()!);
  expect(writes).toEqual([{ dashboards: true }]);
  expect(toggle()!.getAttribute("aria-checked")).toBe("true");
  await click(toggle()!);
  expect(writes).toEqual([{ dashboards: true }, { dashboards: false }]);
  expect(toggle()!.getAttribute("aria-checked")).toBe("false");
  expect(toast).not.toHaveBeenCalled();
});

it("goes back with the hub's reason when the change is refused (a device without full access)", async () => {
  answer = () => json({ error: "Synthetic refusal", code: "forbidden" }, 403);
  await render();
  await click(toggle()!);
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic refusal"), "error");
  expect(toggle()!.getAttribute("aria-checked")).toBe("false");
});

it("is not on another manager's settings: only the default manager has dashboards", async () => {
  chooseManager("m-work");
  stored = { ...stored, manager: "m-work" };
  await render();
  expect(toggle()).toBeNull();
});

it("has its words in every language", async () => {
  for (const [lang, label, footer] of [
    ["zh-Hans", "允许 Persona 创建仪表盘", "开启后，助手可以为你起草需要经常查看的页面；在对话中点击「保留」后，页面就会显示在侧边栏中。"],
    ["ja", "Persona にダッシュボードを作らせる", "オンにすると、アシスタントが何度も見返すページを下書きします。チャットで「保存」をタップするとサイドバーに表示されます。"],
  ] as const) {
    await initI18n(lang);
    expect(t("settings-assistantsettings.dashboards")).toBe(label);
    expect(t("settings-assistantsettings.dashboards_footer")).toBe(footer);
  }
  await initI18n("en");
});

it("is found by the settings search", async () => {
  const { searchSettings } = await import("./search");
  const { groups } = searchSettings("make dashboards");
  expect(groups.some((g) => g.tab === "persona" && g.anchor === "dashboards")).toBe(true);
});
