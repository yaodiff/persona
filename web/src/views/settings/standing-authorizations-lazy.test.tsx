/// <reference types="node" />
// @vitest-environment jsdom
import { readFileSync } from "node:fs";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { expect, it, vi } from "vitest";
vi.hoisted(() => { window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia; });
vi.mock("../../ui/Markdown", () => ({ Markdown: () => null }));
vi.mock("../../lib/managers", () => ({
  useRoster: () => ({ default: "assistant", managers: [{ id: "assistant", name: "Personal" }] }),
  managerOrder: (r: { managers: unknown[] }) => r.managers,
}));
vi.mock("../assistant/Persona", () => ({ usePersona: () => ({ name: "Persona" }), PersonaAvatar: () => null }));
vi.mock("./Skills", () => ({ SkillsGroup: () => null }));
vi.mock("../../lib/store", () => ({ useStore: () => [], refreshAssistant: vi.fn() }));
vi.mock("../../lib/error-reports", () => ({ reportBoundary: vi.fn() }));
vi.mock("./StandingAuthorizations", () => { throw new TypeError("Failed to fetch dynamically imported module: StandingAuthorizations"); });
import { initI18n, t } from "../../i18n";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { reportBoundary } from "../../lib/error-reports";
import { PageBoundary } from "../PageBoundary";
import { AssistantSettings } from "./AssistantSettings";

const source = readFileSync("src/views/settings/AssistantSettings.tsx", "utf8");

it("loads standing authorizations only through a dynamic import under PageBoundary and Suspense", () => {
  const imports = [...source.matchAll(/^import[^;]*from\s+["']([^"']+)["']/gm)].map((m) => m[1]);
  expect(imports).not.toContain("./StandingAuthorizations");
  expect(source).toMatch(/import\(\s*["']\.\/StandingAuthorizations["']\s*\)/);
  expect(source).toMatch(/<PageBoundary [^\n]*>\s*<Suspense [^\n]*>\s*<StandingAuthorizations /);
});

it("keeps the other settings groups when the standing authorizations import fails", async () => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  await initI18n("en");
  resetManagerChoice(); setRosterDefault("assistant");
  sessionStorage.setItem("persona.chunkReload", String(Date.now()));
  const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input).split("?")[0];
    if (path.endsWith("/assistant")) return Response.json({ manager: "assistant", notes: "", agent_tasks: false });
    if (path.endsWith("/tasks")) return Response.json({ tasks: [] });
    if (path.endsWith("/memory")) return Response.json({ files: [] });
    if (path.endsWith("/assistant/leaders")) return Response.json({ leaders: [] });
    if (path.endsWith("/assistant/outbound-messages")) return Response.json({ identity: "bot" });
    if (path.endsWith("/assistant/standing-authorizations")) return Response.json({ revision: 0, authorizations: [] });
    throw new Error(`Unexpected path: ${path}`);
  }));
  const host = document.createElement("div"); document.body.append(host);
  const root = createRoot(host);
  try {
    await act(async () => root.render(<PageBoundary page="settings"><AssistantSettings /></PageBoundary>));
    await vi.waitFor(() => {
      expect(host.querySelector('[role="alert"]')).not.toBeNull();
      expect(host.querySelectorAll('[role="alert"]')).toHaveLength(1);
      expect(host.textContent).toContain(t("settings-nav.tab_model_config"));
      expect(host.querySelector(`select[aria-label="${t("settings-assistantsettings.default_permissions")}"]`)).not.toBeNull();
      expect(host.querySelector("textarea")).not.toBeNull();
      expect(host.querySelector<HTMLSelectElement>(`select[aria-label="${t("settings-outbound.identity")}"]`)?.value).toBe("bot");
      expect(reportBoundary).toHaveBeenCalledWith(expect.objectContaining({ cause: expect.any(TypeError) }), "page_boundary", false, expect.any(String));
    }, { timeout: 20_000 });
  } finally {
    await act(async () => root.unmount()); host.remove();
    sessionStorage.removeItem("persona.chunkReload");
    consoleError.mockRestore(); vi.unstubAllGlobals();
  }
});
