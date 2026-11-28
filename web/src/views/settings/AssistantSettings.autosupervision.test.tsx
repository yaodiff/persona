// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
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
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import { AssistantSettings } from "./AssistantSettings";

// Settings › Assistant › Agent follow-up: "Automatic supervision messages", one picker of all (default), outcomes
// and off (#16417); a hub that has only the older on/off switch gets all and off.

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
const picker = () => host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-assistantsettings.auto_supervision_mode")}"]`);
const options = () => [...picker()!.options].filter((o) => !o.disabled).map((o) => o.value);
beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  vi.mocked(toast).mockClear();
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  stored = { manager: "assistant", notes: "", agent_tasks: false, inbox_signals: false, auto_supervision_messages: true, auto_supervision_mode: "all" }; writes = []; answer = undefined;
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

async function choose(value: string) {
  await act(async () => {
    const select = picker()!;
    select.value = value;
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await flush();
}

it("shows the hub's mode as one picker with three choices, with the sentence saying what it does", async () => {
  await render();
  expect(picker()!.value).toBe("all");
  expect(options()).toEqual(["all", "outcomes", "off"]);
  expect(host.textContent).toContain(t("settings-assistantsettings.auto_supervision_mode_hint"));
  expect(host.querySelector(`[role="switch"][aria-label="${t("settings-assistantsettings.auto_supervision_mode")}"]`)).toBeNull();
  act(() => root.unmount()); root = createRoot(host);
  stored = { ...stored, auto_supervision_mode: "outcomes" };
  await render();
  expect(picker()!.value).toBe("outcomes");
});

it("saves only the mode, every way, and shows what the hub answered", async () => {
  await render();
  for (const mode of ["outcomes", "off", "all"]) {
    stored = { ...stored, auto_supervision_messages: mode !== "off" }; // the older switch kept in step, as the hub does
    await choose(mode);
    expect(picker()!.value).toBe(mode);
  }
  expect(writes).toEqual([{ auto_supervision_mode: "outcomes" }, { auto_supervision_mode: "off" }, { auto_supervision_mode: "all" }]);
  expect(toast).not.toHaveBeenCalled();
});

it("keeps the hub's value and says so when the hub does not confirm the choice", async () => {
  answer = () => json({ ...stored, auto_supervision_mode: "all" });
  await render();
  await choose("outcomes");
  expect(writes).toEqual([{ auto_supervision_mode: "outcomes" }]);
  expect(picker()!.value).toBe("all");
  expect(toast).toHaveBeenCalledWith(expect.stringContaining(t("settings-assistantsettings.auto_supervision_mode_unconfirmed")), "error");
});

it("goes back with the hub's reason when the change is refused", async () => {
  answer = () => json({ error: "Synthetic refusal" }, 403);
  await render();
  await choose("off");
  expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic refusal"), "error");
  expect(picker()!.value).toBe("all");
});

it("offers all and off on a hub that has only the older switch, and writes the switch", async () => {
  delete stored.auto_supervision_mode;
  stored.auto_supervision_messages = false;
  await render();
  expect(picker()!.value).toBe("off");
  expect(options()).toEqual(["all", "off"]);
  await choose("all");
  expect(writes).toEqual([{ auto_supervision_messages: true }]);
  expect(picker()!.value).toBe("all");
  expect(toast).not.toHaveBeenCalled();
});

it("has no row on a hub before it", async () => {
  delete stored.auto_supervision_messages;
  delete stored.auto_supervision_mode;
  await render();
  expect(picker()).toBeNull();
  expect(host.textContent).toContain("Agent follow-up"); // the group's other rows stay
});
