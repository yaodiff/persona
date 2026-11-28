// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TaskExecutionFields } from "./TaskExecutionFields";
import { blankTask, taskInputSettings, type Task } from "../../lib/routines";
import { agyModels, claudeModels, codexModels, neon } from "../../lib/launcher-model-binding.fixture";
import { getState } from "../../lib/store";
import { resetManagerChoice, setRosterDefault } from "../../lib/manager-choice";
import { t } from "../../i18n";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement, root: Root;
const pending: { path: string; resolve: (response: Response) => void }[] = [];
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status });
const flush = async () => { for (let i = 0; i < 8; i++) await act(async () => { await Promise.resolve(); }); };
beforeEach(() => {
  resetManagerChoice(); setRosterDefault("assistant");
  pending.length = 0;
  Object.assign(getState(), { machines: [{ id: "first", name: "First", online: true }, { id: "second", name: "Second", online: true }] });
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.includes("/purposes/assistant")) return json({ purpose: { machine_id: "first", effective: { provider: "launcher.first" } } });
    if (path.includes("/launchers")) return json({ launchers: [{ name: "first", agent: "claude" }, { name: "second", agent: "codex" }, { name: "third", agent: "codex" }] });
    if (path.includes("/models")) return new Promise<Response>((resolve) => pending.push({ path, resolve }));
    throw new Error(`unexpected request ${path}`);
  }));
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => { await act(async () => root.unmount()); host.remove(); resetManagerChoice(); vi.unstubAllGlobals(); });

it.each(["computer", "launcher"])("does not expose another %s's catalog during loading, failure, or a late response", async (kind) => {
  let draft: Task = { ...blankTask(), id: "job", execution_machine_id: "first", launcher: "first" };
  const changed = vi.fn();
  const render = async () => { await act(async () => root.render(<TaskExecutionFields draft={draft} disabled={false} onChange={changed} />)); await flush(); };
  const picker = () => host.querySelector<HTMLButtonElement>('button[aria-expanded]')!;
  const options = () => [...host.querySelectorAll('[role="option"]')].map((el) => el.textContent);
  const reply = async (index: number, model: string) => { await act(async () => pending[index].resolve(json({ agent: "codex", models: [{ id: model, name: model, efforts: ["high"] }] }))); await flush(); };
  await render();
  await act(async () => { const details = host.querySelector("details")!; details.open = true; details.dispatchEvent(new Event("toggle")); });
  await flush();
  await reply(0, "first-model");
  await act(async () => picker().click());
  expect(options()).toContain("first-model");

  draft = kind === "computer" ? { ...draft, execution_machine_id: "second" } : { ...draft, launcher: "second" };
  await render();
  expect(pending).toHaveLength(2);
  expect(picker().closest("fieldset")?.disabled).toBe(true);
  expect(options()).not.toContain("first-model");
  draft = { ...draft, launcher: "third" };
  await render();
  expect(pending).toHaveLength(3);
  // A transport that ignores cancellation must not publish a former context's successful response.
  if (kind === "computer") {
    await reply(1, "stale-model");
    expect(picker().closest("fieldset")?.disabled).toBe(true);
    expect(options()).not.toContain("stale-model");
  }
  await act(async () => pending[2].resolve(json({ error: "Current catalog unavailable" }, 503)));
  await flush();
  if (kind === "launcher") await reply(1, "stale-model");
  await act(async () => picker().click());
  expect(options()).not.toContain("first-model");
  expect(options()).not.toContain("stale-model");
  expect(host.textContent).toContain("Current catalog unavailable");
  expect(changed).not.toHaveBeenCalled();
  // A failed model read must not disable the saved-launcher inheritance recovery control.
  const agent = [...host.querySelectorAll("label")].find((el) => el.textContent?.startsWith(t("assistant-taskeditor.execution_agent")))!.querySelector("select")!;
  expect(agent.disabled).toBe(false);
  expect(agent.options[0].value).toBe("");
});

it("previews the selected computer's inherited provider and its own model defaults", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.includes("/purposes/assistant")) return json({ purpose: { machine_id: "first", effective: { provider: "launcher.main-local", model: "main-only", effort: "max" } } });
    if (path.includes("/launchers")) return json({ launchers: [{ name: "remote-agent", agent: "codex" }], default: { provider: "remote-agent" } });
    if (path.includes("/models")) {
      expect(path).toContain("launcher.remote-agent");
      expect(path).toContain("machine=second");
      return json({ agent: "codex", models: [{ id: "remote-default", default: true, efforts: ["low"], default_effort: "low" }] });
    }
    throw new Error(`unexpected request ${path}`);
  }));
  await act(async () => root.render(<TaskExecutionFields draft={{ ...blankTask(), execution_machine_id: "second" }} disabled={false} onChange={() => {}} />));
  await flush();
  const details = host.querySelector("details")!;
  expect(details.querySelector("summary")!.textContent).toContain("remote-agent · codex");
  expect(details.querySelector("summary")!.textContent).not.toContain("main-only");
  await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
  await flush();
  expect(details.querySelector("summary")!.textContent).toContain("remote-agent · codex · remote-default · low");
});

it("uses a folded saved alias's advertised default effort without changing the choice", async () => {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.includes("/purposes/assistant")) return json({ purpose: { machine_id: "first", effective: { provider: "launcher.first" } } });
    if (path.includes("/launchers")) return json({ launchers: [{ name: "second", agent: "codex" }] });
    if (path.includes("/models")) return json({ agent: "codex", models: [{ id: "test-model", aliases: ["test-alias"], efforts: ["low"], default_effort: "low" }] });
    throw new Error(`unexpected request ${path}`);
  }));
  const changed = vi.fn();
  const draft = { ...blankTask(), execution_machine_id: "first", launcher: "second", model: "test-alias" };
  await act(async () => root.render(<TaskExecutionFields draft={draft} disabled={false} onChange={changed} />));
  await flush();
  const details = host.querySelector("details")!;
  await act(async () => { details.open = true; details.dispatchEvent(new Event("toggle")); });
  await flush();
  expect(details.querySelector("summary")!.textContent).toContain("second · codex · test-alias · low");
  const effort = [...host.querySelectorAll("label")].find((el) => el.textContent?.startsWith(t("settings-modelconfiguration.effort")))!.querySelector("select")!;
  expect([...effort.options].map((option) => option.value)).toEqual(["", "low"]);
  expect(effort.options[0].textContent).toContain("Low");
  expect(changed).not.toHaveBeenCalled();
});

async function editExecution(stored: Partial<Task>, catalog = agyModels) {
  const { agent } = catalog;
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
    const path = String(input);
    if (path.includes("/models")) return json(catalog);
    if (path.includes("/launchers")) return json({ launchers: [{ name: agent, agent }], default: { provider: agent, model: agent === "agy" ? `${neon}-medium` : "" } });
    if (path.includes("/purposes/assistant")) return json({ purpose: { machine_id: "first", effective: { provider: `launcher.${agent}` } } });
    throw new Error(`unexpected request ${path}`);
  }));
  const saved = { ...blankTask(), id: "test", ...stored };
  let draft = saved;
  function Harness() {
    const [current, update] = useState(saved);
    draft = current;
    return <TaskExecutionFields draft={current} saved={saved} disabled={false} onChange={(patch) => update((prev) => ({ ...prev, ...patch }))} />;
  }
  await act(async () => root.render(<Harness />));
  await flush();
  await act(async () => {
    const details = host.querySelector("details")!;
    details.open = true;
    details.dispatchEvent(new Event("toggle"));
  });
  await flush();
  return () => taskInputSettings(draft, saved);
}

const taskPicker = () => host.querySelector<HTMLButtonElement>("button[aria-expanded]")!;
const taskEffort = () => [...host.querySelectorAll("select")]
  .find((node) => node.closest("label")?.querySelector("span")?.textContent === t("settings-modelconfiguration.effort"))!;
async function setEffort(value: string) {
  await act(async () => {
    taskEffort().value = value;
    taskEffort().dispatchEvent(new Event("change", { bubbles: true }));
  });
}
async function pickModel(text: string) {
  await act(async () => taskPicker().click());
  const row = [...host.querySelectorAll<HTMLButtonElement>('[role="option"]')].find((node) => node.textContent?.startsWith(text))!;
  await act(async () => row.click());
}

it("picks a folded task model and sends only the new variant's ID for a new effort", async () => {
  const body = await editExecution({});
  await pickModel("Gemini 4 Neon");
  expect(taskEffort().value).toBe("medium");
  expect([...taskEffort().options].slice(1).map((o) => o.textContent)).toEqual(["Low", "Medium", "High", "XHigh"]);
  await setEffort("xhigh");
  expect(body()).toMatchObject({ model: `${neon}-xhigh` });
  expect(body()).not.toHaveProperty("effort");
  await pickModel("Gemini 4 Flash");
  expect(taskEffort().disabled).toBe(true);
});

it.each([["", "medium"], ["high", "high"]])("hydrates and serializes the saved task variant with standalone effort %j", async (effort, level) => {
  const body = await editExecution({ model: `${neon}-medium`, effort });
  expect(taskPicker().textContent).toBe(`Gemini 4 Neon${neon}-${level}`);
  expect(taskEffort().value).toBe(level);
  if (effort) expect(body()).toMatchObject({ model: `${neon}-high`, effort: "" });
  else expect(body()).not.toHaveProperty("effort");
});

it("pins a sibling variant when only an inherited task's effort changes", async () => {
  const body = await editExecution({});
  expect(taskPicker().textContent).toContain(t("assistant-taskeditor.follow_assistant"));
  expect(taskEffort().value).toBe("");
  expect(taskEffort().options[0].textContent).toContain("Medium");
  expect(body()).not.toHaveProperty("model");
  await setEffort("high");
  expect(body()).toMatchObject({ model: `${neon}-high` });
  expect(body()).not.toHaveProperty("effort");
});

it.each([
  { agent: "Claude", catalog: claudeModels, name: "Sonnet", value: "high" },
  { agent: "Codex", catalog: codexModels, name: "GPT-5.5", value: "xhigh" },
])("writes $agent task model and effort independently", async ({ catalog, name, value }) => {
  const body = await editExecution({}, catalog);
  await pickModel(name);
  await setEffort(value);
  expect(body()).toMatchObject({ model: catalog.models[0].id, effort: value });
});
