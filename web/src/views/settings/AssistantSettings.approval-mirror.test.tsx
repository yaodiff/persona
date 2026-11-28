// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
import { initI18n } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
let assistant: Record<string, unknown>;
let puts: Record<string, unknown>[];
let rejectWrite = false;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };

beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  assistant = { manager: "assistant", machine_id: "server", main: "main", main_machine_id: "server", approval_mirror_main: true };
  puts = []; rejectWrite = false;
  vi.mocked(toast).mockClear();
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input).split("/api/v1")[1];
    if (url === "/assistant?view=main-runtime") return json({ error: "not found" }, 404);
    if (url === "/assistant" && init?.method === "PUT") {
      const body = JSON.parse(String(init.body)); puts.push(body);
      if (rejectWrite) return json({ error: "Denied" }, 403);
      assistant = { ...assistant, ...body };
      return json(assistant);
    }
    if (url === "/assistant") return json(assistant);
    if (url === "/assistant/tasks") return json({ tasks: [] });
    if (url === "/assistant/memory") return json({ files: [] });
    throw new Error(`Unexpected path: ${url}`);
  }));
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }
const toggle = () => [...host.querySelectorAll<HTMLElement>("[role='switch']")].find((s) => s.getAttribute("aria-label") === "Show approvals in the main chat");

describe("Settings › Assistant › approval mirroring", () => {
  it("defaults on, explains the shared request and saves the reversible choice", async () => {
    await render();
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
    expect(host.textContent).toContain("answers the same request");
    expect(host.textContent).toContain("Private chats stay private");
    await act(async () => { toggle()!.click(); });
    await flush();
    expect(puts).toEqual([{ approval_mirror_main: false }]);
    expect(toggle()?.getAttribute("aria-checked")).toBe("false");
    expect(toast).not.toHaveBeenCalled();
  });

  it("is hidden by a hub without the setting", async () => {
    delete assistant.approval_mirror_main;
    await render();
    expect(toggle()).toBeUndefined();
  });

  it("restores the confirmed value when the save fails", async () => {
    rejectWrite = true;
    await render();
    await act(async () => { toggle()!.click(); });
    await flush();
    expect(toggle()?.getAttribute("aria-checked")).toBe("true");
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Denied"), "error");
  });
});
