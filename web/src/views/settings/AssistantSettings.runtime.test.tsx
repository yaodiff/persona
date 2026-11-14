// @vitest-environment jsdom
// Where and with what the main chat runs (docs/assistant/assistant.md "Main conversation execution on the web"): the
// Assistant screen's line says only its state in plain words (audit item 80), and Settings › Assistant's Runs on group
// keeps the rest, each part only when the hub reports it.
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
import { initI18n } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { AssistantSettings } from "./AssistantSettings";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
/** What `GET /assistant?view=main-runtime` answers: a report, or a failure (an older hub without the view). */
let execution: (() => Response) | undefined;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
const place = () => host.querySelector("[data-main-runtime-place]");
const tool = () => host.querySelector("[data-main-runtime-tool]");

beforeEach(async () => {
  await initI18n("en");
  history.replaceState(null, "", "/settings?tab=persona");
  resetManagerChoice(); setRosterDefault("assistant");
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
  execution = undefined;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input).split("/api/v1")[1];
    if (url === "/assistant?view=main-runtime") return execution ? execution() : json({ error: "not found" }, 404);
    if (url === "/assistant") return json({ manager: "assistant", notes: "", machine_id: "server", main: "main-1", main_machine_id: "server" });
    if (url === "/assistant/tasks") return json({ tasks: [] });
    if (url === "/assistant/memory") return json({ files: [] });
    throw new Error(`Unexpected path: ${url}`);
  }));
});
afterEach(() => { act(() => root.unmount()); host.remove(); vi.unstubAllGlobals(); });
async function render() { await act(async () => root.render(<AssistantSettings />)); await flush(); }

describe("Settings › Assistant › Runs on (audit item 80)", () => {
  it("says where and with what the main chat runs when the hub reports it", async () => {
    execution = () => json({ session_id: "main-1", machine_id: "server", state: "running", source: "native", harness: "claude", location: { host: "example-host", container: "example-box" } });
    await render();
    expect(place()?.textContent).toBe("Container example-box");
    expect(tool()?.textContent).toBe("Claude Code");
    expect(host.textContent).toContain("Main chat runs in");
    expect(host.textContent).toContain("Main chat runs with");
  });

  it("names a proxy as the way in, and leaves out what is not reported", async () => {
    execution = () => json({ session_id: "main-1", state: "running", source: "runtime", proxy: "Relay" });
    await render();
    expect(place()?.textContent).toBe("Via Relay");
    expect(tool()).toBeNull();
  });

  it("adds nothing when the hub does not report it, or reports another conversation or route", async () => {
    await render();
    expect(place()).toBeNull();
    expect(tool()).toBeNull();
    expect(host.textContent).not.toContain("not reported");
    act(() => root.unmount());
    root = createRoot(host);
    execution = () => json({ session_id: "other", state: "running", source: "native", harness: "codex", location: { host: "example-host" } });
    await render();
    expect(place()).toBeNull();
    expect(tool()).toBeNull();
    act(() => root.unmount());
    root = createRoot(host);
    execution = () => json({ session_id: "main-1", machine_id: "another-node", state: "running", source: "native", harness: "codex", location: { host: "example-host" } });
    await render();
    expect(place()).toBeNull();
    expect(tool()).toBeNull();
  });
});
