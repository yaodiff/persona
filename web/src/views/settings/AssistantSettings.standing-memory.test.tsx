// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }, { id: "work", name: "Work" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Assistant › "Memory kept in every chat" (assistant.memory_extension_standing_bytes): 4 KB by default,
// 8 KB or 16 KB, saved in bytes and confirmed by the hub's echo.

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let stored: Record<string, number | undefined>;
let writes: { manager: string; body: Record<string, unknown> }[];
let answer: ((manager: string, body: Record<string, unknown>) => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const picker = () => host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-assistantsettings.standing_memory")}"]`);
async function choose(node: HTMLSelectElement, value: string) {
  await act(async () => { node.value = value; node.dispatchEvent(new Event("change", { bubbles: true })); });
  await flush();
}
const state = (manager: string) => ({ manager, notes: "", ...(stored[manager] === undefined ? {} : { memory_extension_standing_bytes: stored[manager] }) });
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  stored = { assistant: 4096, work: undefined }; writes = []; answer = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const manager = new Headers(init?.headers).get("X-Persona-Manager")!;
    if (path.endsWith("/assistant")) {
      if (init?.method === "PUT") {
        const body = JSON.parse(String(init.body)); writes.push({ manager, body });
        if (answer) return answer(manager, body);
        if (![4096, 8192, 16384].includes(body.memory_extension_standing_bytes)) return json({ error: "memory_extension_standing_bytes must be 4096, 8192 or 16384" }, 400);
        stored[manager] = body.memory_extension_standing_bytes;
      }
      return json(state(manager));
    }
    if (path.endsWith("/tasks")) return json({ tasks: [] });
    if (path.endsWith("/memory")) return json({ files: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
});
afterEach(async () => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }

it("shows 4 KB by default with the units, and saves 8 KB and 16 KB in bytes", async () => {
  await render();
  const select = picker()!;
  expect(select.value).toBe("4096");
  expect([...select.options].map((o) => [o.value, o.textContent])).toEqual([["4096", "4 KB (default)"], ["8192", "8 KB"], ["16384", "16 KB"]]);
  expect(host.textContent).toContain("4,096 bytes");
  expect(host.textContent).toContain("16,384");
  await choose(select, "8192");
  expect(picker()!.value).toBe("8192");
  await choose(picker()!, "16384");
  expect(picker()!.value).toBe("16384");
  expect(writes).toEqual([
    { manager: "assistant", body: { memory_extension_standing_bytes: 8192 } },
    { manager: "assistant", body: { memory_extension_standing_bytes: 16384 } },
  ]);
  expect(toast).not.toHaveBeenCalled();
});

it("has no picker on a hub that does not offer the choice", async () => {
  stored.assistant = undefined;
  await render();
  expect(picker()).toBeNull();
});

it("keeps the shown size and says why when the hub refuses or does not confirm", async () => {
  answer = () => json({ error: "memory_extension_standing_bytes must be 4096, 8192 or 16384" }, 400);
  await render();
  await choose(picker()!, "16384");
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("4096, 8192 or 16384"), "error");
  expect(picker()!.value).toBe("4096");
  vi.mocked(toast).mockClear();
  answer = (manager) => json({ manager, memory_extension_standing_bytes: 4096 });
  await choose(picker()!, "8192");
  expect(toast).toHaveBeenCalledWith(t("settings-assistantsettings.save_failed", { error: t("settings-assistantsettings.standing_memory_unconfirmed") }), "error");
  expect(picker()!.value).toBe("4096");
});

it("shows a size it does not know as it is, without offering it", async () => {
  stored.assistant = 12288;
  await render();
  expect(picker()!.value).toBe("12288");
  expect(picker()!.options[0].disabled).toBe(true);
  expect(writes).toEqual([]);
});
