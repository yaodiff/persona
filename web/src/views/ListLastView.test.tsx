// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { setPairedToken, setToken } from "../lib/api";
import { chooseManager, resetManagerChoice, setRosterDefault } from "../lib/manager-choice";
import { Routines } from "./assistant/Routines";
import { Extensions } from "./settings/Extensions";

vi.hoisted(() => {
  globalThis.matchMedia = vi.fn().mockReturnValue({ matches: false, addEventListener() {}, removeEventListener() {} });
});
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const pending = new Map<string, ((response: Response) => void)[]>();
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
let root: Root;
let host: HTMLElement;

beforeEach(() => {
  setPairedToken("list-test");
  vi.stubGlobal("fetch", async (input: string) => {
    const path = new URL(input, location.origin).pathname.replace("/api/v1", "");
    if (["/plugins", "/extensions", "/assistant/tasks", "/assistant/runs"].includes(path)) {
      const reads = pending.get(path) ?? [];
      pending.set(path, reads);
      return new Promise<Response>((resolve) => reads.push(resolve));
    }
    return json({});
  });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  pending.clear();
  vi.unstubAllGlobals();
  resetManagerChoice();
  setToken(""); // End the credential generation, releasing both pages' snapshots.
});

const takeRead = (path: string) => {
  const done = pending.get(path)?.shift();
  if (!done) throw new Error(`Missing request for ${path}`);
  return done;
};
const tasks = (name: string) => ({ tasks: [{ id: "daily", title: name, repeat: "daily", time: "09:00", enabled: true }] });
const extensions = (name: string) => ({ extensions: [{ id: "example", name, source: "installed", enabled: true }], markets: true });
const runs = (title: string) => ({ runs: [{ id: "run", kind: "task", ref_id: "daily", title, due: "2026-11-05T09:00:00Z", at: "2026-11-05T09:00:00Z", status: "ok" }] });

describe.each([
  { name: "Scheduled tasks", page: <Routines onToggleSidebar={() => {}} />, path: "/assistant/tasks", list: tasks, history: true },
  { name: "Installed extensions", page: <Extensions open={null} onOpen={() => {}} />, path: "/plugins", list: extensions, history: false },
])("$name last view", ({ page, path, list, history }) => {
  const open = () => act(async () => root.render(page));
  const leave = () => act(async () => root.render(null));
  const answer = (name: string) => act(async () => {
    takeRead(path)(json(list(name)));
    if (history) takeRead("/assistant/runs")(json(runs(name + " history")));
  });

  it("shows Loading on the first visit and the last list throughout a background refresh", async () => {
    await open();
    expect(host.textContent).toContain(t("common.loading"));
    await answer("First list");
    await leave();
    await open();
    expect(host.textContent).not.toContain(t("common.loading"));
    expect(host.textContent).toContain("First list");
    if (history) expect(host.textContent).toContain("First list history");
    else expect(host.textContent).toContain(t("settings-extensions.browse_market"));

    await answer("Updated list");
    expect(host.textContent).not.toContain("First list");
    expect(host.textContent).toContain("Updated list");

    await leave();
    await open();
    await act(async () => {
      takeRead(path)(json({ error: "refresh unavailable" }, 500));
      if (history) takeRead("/assistant/runs")(json({ error: "history unavailable" }, 500));
    });
    expect(host.textContent).toContain("Updated list");
    expect(host.textContent).toContain("refresh unavailable");
    if (history) {
      expect(host.textContent).toContain("Updated list history");
      expect(host.textContent).toContain("history unavailable");
    }
  });

  it("restores the selected assistant's own list when switching back", async () => {
    await open();
    await answer("Home list");
    await act(async () => chooseManager("work"));
    await answer("Work list");
    await act(async () => chooseManager(null));
    expect(host.textContent).toContain("Home list");
    expect(host.textContent).not.toContain("Work list");
    expect(host.textContent).not.toContain(t("common.loading"));
  });

  it.each([
    ["hub", () => vi.stubGlobal("location", new URL("https://hub-b.example.com"))],
    ["sign-in", () => setPairedToken("list-test")],
    ["assistant", () => chooseManager("work")],
    ["default manager", () => setRosterDefault("another-default")],
  ] as const)("isolates a new %s from cached data and late responses", async (kind, switchScope) => {
    await open();
    await answer("Previous list");
    await leave();
    await open();
    const previous = takeRead(path);
    const previousRuns = history && takeRead("/assistant/runs");
    // Navigating to another origin remounts the page. Retain the module cache to test its partition.
    if (kind === "hub") await leave();
    await act(async () => { switchScope(); });
    if (kind === "hub") await open();
    expect(host.textContent).not.toContain("Previous list");
    expect(host.textContent).toContain(t("common.loading"));
    await answer("Current list");
    await act(async () => {
      previous(json(list("Late list")));
      if (previousRuns) previousRuns(json(runs("Late history")));
    });
    await leave();
    await open();
    expect(host.textContent).toContain("Current list");
    expect(host.textContent).not.toContain("Late");
    expect(host.textContent).not.toContain("Previous");
  });
});
