// @vitest-environment jsdom
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { MANAGER_HEADER } from "../../lib/manager-choice";
import type { ModelDraft, ModelProvider, ModelPurpose } from "../../lib/model-config";
import { DirectAPIEditor, expectedDirectProbeEffort } from "./DirectAPIEditor";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const purpose: ModelPurpose = {
  id: "inbox.triage",
  name: "Triage",
  group: "Direct API",
  kind: "api",
  revision: 1,
  override: {},
  effective: {},
  fields: ["model", "effort"],
};
const provider: ModelProvider = {
  id: "launcher.gateway",
  name: "Gateway",
  kind: "api",
  api: "responses",
  model: "raw-default",
};
const models = [
  { id: "raw-a", name: "Alias A", efforts: ["low", "high"], default_effort: "low" },
  { id: "raw-b", name: "raw-b", efforts: [] },
  { id: "raw-none", name: "raw-none", efforts: [], no_effort: true },
  { id: "raw-off", name: "raw-off", efforts: ["off", "low", "high"] },
  { id: "raw-efforts-only", name: "raw-efforts-only", efforts: ["low"] },
  { id: "raw-minimal", name: "raw-minimal", efforts: ["low"], all_efforts: ["minimal"] },
  { id: "raw-direct", name: "raw-direct", efforts: ["low", "high", "max"], all_efforts: ["minimal", "none", "low", "high", "ultra"] },
];
const response = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

type Call = { path: string; init?: RequestInit; body: Record<string, unknown> };
let host: HTMLDivElement;
let root: Root;
let calls: Call[];
let draft: ModelDraft;
let patch: Partial<ModelDraft> | undefined;
let manager: string;
let currentProvider: ModelProvider;

function jsonFetch(answer: (call: Call) => Promise<Response> | Response) {
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input).split("?")[0];
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {};
    const call = { path, init, body };
    calls.push(call);
    return answer(call);
  }));
}

function Harness() {
  const [, redraw] = useState(0);
  return (
    <DirectAPIEditor
      manager={manager}
      purpose={purpose}
      provider={currentProvider}
      draft={draft}
      disabled={false}
      effortAllowed
      onChange={(change) => {
        patch = change;
        draft = { ...draft, ...change };
        redraw((n) => n + 1);
      }}
    />
  );
}

const flush = async () => {
  for (let i = 0; i < 6; i++) await act(async () => { await Promise.resolve(); });
};
const input = () => host.querySelector<HTMLInputElement>("input[placeholder]")!;
const selects = () => [...host.querySelectorAll<HTMLSelectElement>("select")];
const effortSelect = () => selects()[1];
const testButton = () => host.querySelector<HTMLButtonElement>("button[aria-busy], button:not([aria-busy])")!;
const headerValue = (init: RequestInit | undefined, name: string) => new Headers(init?.headers).get(name);

async function render() {
  await act(async () => root.render(<Harness />));
  await flush();
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

async function typeModel(value: string) {
  await act(async () => {
    const element = input();
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(element, value);
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function changeDraft(change: Partial<ModelDraft>) {
  draft = { ...draft, ...change };
  await render();
}

async function testProbe() {
  await act(async () => testButton().click());
}

beforeEach(async () => {
  await initI18n("en");
  calls = [];
  patch = undefined;
  manager = "manager-one";
  currentProvider = { ...provider, models };
  draft = { provider: provider.id, model: "raw-a", effort: "low", runtime: "" };
  jsonFetch((call) => call.path.endsWith("/launchers/discover")
    ? response({ models })
    : response({ ok: true, ms: 12, effort: call.body.effort }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  await initI18n("en");
});

it("leaves Use default to the saved connection instead of explicitly clearing its effort", async () => {
  draft = { ...draft, effort: "" };
  jsonFetch((call) => call.path.endsWith("/launchers/discover")
    ? response({ models })
    : response({ ok: true, ms: 12, effort: "high" }));
  await act(async () => root.render(
    <DirectAPIEditor
      manager={manager}
      purpose={{ ...purpose, id: "kb" }}
      provider={currentProvider}
      draft={draft}
      disabled={false}
      effortAllowed
      onChange={() => {}}
    />,
  ));
  await flush();
  await testProbe();
  const probe = calls.find((call) => call.path.endsWith("/launchers/test"))!;
  expect(probe.body).toEqual({ name: "gateway", model: "raw-a" });
  expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  expect(draft.effort).toBe("");
});

describe("DirectAPIEditor", () => {
  it.each([
    ["raw-none", "low", ""],
    ["raw-b", "low", "low"],
    ["raw-efforts-only", "off", "off"],
    ["raw-efforts-only", "high", ""],
    ["raw-direct", "off", "off"],
    ["raw-direct", "none", "none"],
    ["raw-direct", "disabled", "disabled"],
    ["raw-direct", "medium", ""],
    ["raw-a", "LOW", ""],
    ["unlisted-model", "high", "high"],
  ])("expects the stored Chat capability result for %s effort %s", (model, requested, expected) => {
    currentProvider = { ...provider, api: "chat", models };
    expect(expectedDirectProbeEffort(currentProvider, model, requested)).toBe(expected);
  });

  it("discovers aliases but submits the raw model ID, requested effort and manager", async () => {
    await render();

    expect(calls[0]).toMatchObject({
      path: "/api/v1/launchers/discover",
      body: { name: "gateway" },
    });
    expect(headerValue(calls[0].init, MANAGER_HEADER)).toBe(manager);
    expect(selects()[0].textContent).toContain("Alias A (raw-a)");

    await act(async () => {
      selects()[0].value = "raw-a";
      selects()[0].dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(patch).toEqual({ model: "raw-a" });

    await testProbe();
    await flush();
    expect(calls.at(-1)).toMatchObject({
      path: "/api/v1/launchers/test",
      body: { name: "gateway", model: "raw-a", effort: "low" },
    });
    expect(headerValue(calls.at(-1)!.init, MANAGER_HEADER)).toBe(manager);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("updates effort capability with model choice and preserves unsupported current values", async () => {
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "low", "high"]);

    await act(async () => {
      selects()[0].value = "raw-b";
      selects()[0].dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unsupported"));

    draft = { ...draft, model: "raw-b", effort: "legacy-level" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toContain("legacy-level");
    expect(effortSelect().disabled).toBe(false);

    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: "legacy-level" };
    await render();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unsupported"));
    expect([...effortSelect().options].map((option) => option.value)).toContain("legacy-level");

    draft = { ...draft, model: "raw-a" };
    await render();
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_effort_unsupported"));
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "legacy-level", "off", "low", "high"]);
    expect(effortSelect().disabled).toBe(false);
  });

  it("treats an empty Chat effort list as unknown instead of unsupported", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-b", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low", "medium", "high", "xhigh", "max"]);
    expect(effortSelect().disabled).toBe(false);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unknown"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_effort_unsupported"));
  });

  it("uses stored Chat capabilities when discovery has no model entry", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: "" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models: [] })
      : response({ ok: true, ms: 12, effort: "" }));
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual([""]);
    expect(effortSelect().disabled).toBe(true);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unsupported"));
  });

  it("uses Chat's complete capability list without offering levels purpose Save rejects", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-direct", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low", "high"]);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_hint"));
  });

  it("resolves legacy Chat tier aliases before reading model capabilities", async () => {
    const aliased = [{ id: "gw/sonnet", efforts: ["low"], all_efforts: ["none", "low"] }];
    currentProvider = { ...provider, api: "chat", models: aliased, model_aliases: { sonnet: "gw/sonnet" } };
    draft = { ...draft, model: "Sonnet[1m]", effort: "" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models: aliased })
      : response({ ok: true, ms: 12, effort: call.body.effort }));
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low"]);
    expect(effortSelect().disabled).toBe(false);
    await act(async () => {
      effortSelect().value = "low";
      effortSelect().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "Sonnet[1m]", effort: "low" });
  });

  it("does not trust an inherited constructor property as a Chat model alias", async () => {
    const exact = [{ id: "constructor", efforts: ["low"], all_efforts: ["none", "low"] }];
    currentProvider = { ...provider, api: "chat", models: exact, model_aliases: { sonnet: "gw/sonnet" } };
    draft = { ...draft, model: "constructor", effort: "" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models: [] })
      : response({ ok: true, ms: 12, effort: call.body.effort }));
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low"]);
  });

  it("keeps Off available for Chat efforts-only metadata", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-efforts-only", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low"]);
  });

  it("keeps projected Responses choices instead of applying Chat's exact list", async () => {
    draft = { ...draft, model: "raw-minimal", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "low"]);
    expect(effortSelect().disabled).toBe(false);

    await changeDraft({ model: "raw-direct" });
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "low", "high", "max"]);
  });

  it("keeps a reported Off option when another effort is current", async () => {
    draft = { ...draft, model: "raw-off", effort: "low" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low", "high"]);
  });

  it("preserves a current none value with one Off option", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-direct", effort: "none" };
    await render();
    const options = [...effortSelect().options];
    expect(options.map((option) => option.value)).toEqual(["", "none", "low", "high"]);
    expect(effortSelect().selectedOptions[0].textContent).toBe(t("settings-modelconfiguration.model_current", {
      id: t("settings-modelconfiguration.api_effort_off"),
    }));
  });

  it("explains when exact reported levels cannot be saved for this purpose", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-minimal", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual([""]);
    expect(effortSelect().disabled).toBe(true);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unavailable"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_effort_unknown"));

    await changeDraft({ effort: "legacy-level" });
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "legacy-level"]);
    expect(effortSelect().disabled).toBe(false);
  });

  it.each(["", "off", "none"])("accepts stored no_effort suppression for draft effort %s when discovery omits the flag", async (value) => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: value };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models: models.map((item) => item.id === "raw-none" ? { id: item.id, name: item.name, efforts: [] } : item) })
      : response({ ok: true, ms: 12, effort: "" }));
    await render();
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "raw-none", effort: value || "off" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_effort"));
  });

  it("does not use discovered no_effort to verify an older hub's empty echo", async () => {
    currentProvider = { ...provider, api: "chat", models: undefined };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 12, effort: "" }));
    await render();
    await testProbe();
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("rejects an empty echo when fresh discovery reports no_effort but the stored model does not", async () => {
    currentProvider = {
      ...provider,
      api: "chat",
      models: models.map((item) => item.id === "raw-none" ? { ...item, no_effort: false } : item),
    };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 12, effort: "" }));
    await render();
    await testProbe();
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("rejects a degraded empty echo when the selected Chat provider has a saved default effort", async () => {
    currentProvider = { ...provider, api: "chat", effort: "high", models };
    draft = { provider: provider.id, model: "raw-a", effort: "", runtime: "" };
    const switchedPurpose = { ...purpose, id: "kb", effective: { provider: "launcher.other", model: "other", effort: "low" } };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 12, effort: "" }));
    await act(async () => root.render(
      <DirectAPIEditor manager={manager} purpose={switchedPurpose} provider={currentProvider} draft={draft}
        disabled={false} effortAllowed onChange={() => {}} />,
    ));
    await flush();
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "raw-a" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("accepts stored model suppression of the provider's inherited default effort", async () => {
    currentProvider = { ...provider, api: "chat", effort: "high", models };
    draft = { provider: provider.id, model: "raw-efforts-only", effort: "", runtime: "" };
    const switchedPurpose = { ...purpose, id: "kb", effective: { provider: "launcher.other", model: "other", effort: "low" } };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 12, effort: "" }));
    await act(async () => root.render(
      <DirectAPIEditor manager={manager} purpose={switchedPurpose} provider={currentProvider} draft={draft}
        disabled={false} effortAllowed onChange={() => {}} />,
    ));
    await flush();
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "raw-efforts-only" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_effort"));
  });

  it("rejects a different nonempty effort for a no_effort Chat model", async () => {
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 12, effort: "high" }));
    await render();
    await testProbe();
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_effort"));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("waits for model discovery before testing effort", async () => {
    const discovered = deferred<Response>();
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? discovered.promise
      : response({ ok: true, ms: 12, effort: "" }));
    await render();
    expect(testButton().disabled).toBe(true);
    await testProbe();
    expect(calls.filter((call) => call.path.endsWith("/launchers/test"))).toHaveLength(0);

    await act(async () => discovered.resolve(response({ models })));
    await flush();
    expect(testButton().disabled).toBe(false);
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "raw-none", effort: "low" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it.each(["responses", "messages"] as const)("does not block a %s probe on model discovery", async (api) => {
    const discovered = deferred<Response>();
    currentProvider = { ...provider, api, models };
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? discovered.promise
      : response({ ok: true, ms: 12, effort: call.body.effort }));
    await render();
    expect(testButton().disabled).toBe(false);
    await testProbe();
    await flush();
    expect(calls.filter((call) => call.path.endsWith("/launchers/test"))).toHaveLength(1);
    expect(calls.at(-1)!.body).toEqual({ name: "gateway", model: "raw-a", effort: "low" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("retires a probe when the effective no_effort capability changes", async () => {
    const pending = deferred<Response>();
    const refreshed = deferred<Response>();
    let discoveries = 0;
    currentProvider = { ...provider, api: "chat", models: undefined };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => {
      if (call.path.endsWith("/launchers/discover")) {
        return ++discoveries === 1 ? response({ error: "retry", models: [] }) : refreshed.promise;
      }
      return pending.promise;
    });
    await render();
    const connection = () => [...host.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent === t("settings-modelconfiguration.api_test"))!;
    await act(async () => connection().click());
    const old = calls.find((call) => call.path.endsWith("/launchers/test"))!;
    const retry = [...host.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent === t("settings-modelconfiguration.retry"))!;
    await act(async () => retry.click());
    await act(async () => refreshed.resolve(response({ models })));
    await flush();
    expect(old.init?.signal?.aborted).toBe(true);
    expect(testButton().getAttribute("aria-busy")).not.toBe("true");

    await act(async () => pending.resolve(response({ ok: true, ms: 99, effort: "low" })));
    await flush();
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 99 }));
  });

  it("keeps a probe when only redundant discovery no_effort metadata changes", async () => {
    const pending = deferred<Response>();
    const refreshed = deferred<Response>();
    let discoveries = 0;
    currentProvider = { ...provider, api: "chat", models };
    draft = { ...draft, model: "raw-none", effort: "low" };
    jsonFetch((call) => {
      if (call.path.endsWith("/launchers/discover")) {
        return ++discoveries === 1 ? response({ error: "retry", models: [] }) : refreshed.promise;
      }
      return pending.promise;
    });
    await render();
    const connection = () => [...host.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent === t("settings-modelconfiguration.api_test"))!;
    await act(async () => connection().click());
    const old = calls.find((call) => call.path.endsWith("/launchers/test"))!;
    const retry = [...host.querySelectorAll<HTMLButtonElement>("button")]
      .find((button) => button.textContent === t("settings-modelconfiguration.retry"))!;
    await act(async () => retry.click());
    await act(async () => refreshed.resolve(response({ models })));
    await flush();
    expect(old.init?.signal?.aborted).toBe(false);
    expect(connection().getAttribute("aria-busy")).toBe("true");

    await act(async () => pending.resolve(response({ ok: true, ms: 99, effort: "" })));
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 99 }));
  });

  it.each([
    ["responses", ["", "off", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"]],
    ["chat", ["", "off", "low", "medium", "high", "xhigh", "max"]],
  ] as const)("offers testable %s protocol efforts when discovery has no model-specific metadata", async (api, expected) => {
    currentProvider = { ...provider, api, models };
    draft = { ...draft, model: "unlisted-model", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(expected);
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_effort_unknown"));
    await act(async () => {
      effortSelect().value = "high";
      effortSelect().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await testProbe();
    await flush();
    expect(calls.at(-1)!.body).toMatchObject({ model: "unlisted-model", effort: "high" });
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it.each([["messages", "messages" as const], ["legacy", undefined]])("keeps the Messages fallback for %s providers without model metadata", async (_, api) => {
    currentProvider = { ...provider, api, models };
    draft = { ...draft, model: "unlisted-model", effort: "" };
    await render();
    expect([...effortSelect().options].map((option) => option.value)).toEqual(["", "off", "low", "medium", "high", "xhigh", "max"]);
  });

  it("keeps manual model entry usable after discovery failure", async () => {
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ error: "list unavailable", models: [] })
      : response({ ok: true, ms: 1, effort: "low" }));
    await render();

    expect(host.textContent).toContain("Couldn't load models: list unavailable");
    await typeModel("typed-model");
    expect(input().value).toBe("typed-model");
    expect(patch).toEqual({ model: "typed-model" });
  });

  it("invalidates a verified result when the draft changes", async () => {
    await render();
    await testProbe();
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));

    await typeModel("changed-model");
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 12 }));
  });

  it("ignores A to B to A stale probe completion without clearing the newer probe", async () => {
    const old = deferred<Response>();
    const fresh = deferred<Response>();
    let probes = 0;
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : (++probes === 1 ? old.promise : fresh.promise));

    await render();
    await testProbe();
    await changeDraft({ model: "raw-b" });
    await changeDraft({ model: "raw-a" });
    await testProbe();
    expect(testButton().getAttribute("aria-busy")).toBe("true");

    await act(async () => old.resolve(response({ ok: true, ms: 1, effort: "low" })));
    await flush();
    expect(testButton().getAttribute("aria-busy")).toBe("true");
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 1 }));

    await act(async () => fresh.resolve(response({ ok: true, ms: 2, effort: "low" })));
    await flush();
    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 2 }));
  });

  it("does not start duplicate probes on a rapid double click", async () => {
    const pending = deferred<Response>();
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : pending.promise);
    await render();

    await act(async () => {
      testButton().click();
      testButton().click();
    });
    expect(calls.filter((call) => call.path.endsWith("/launchers/test"))).toHaveLength(1);

    await act(async () => pending.resolve(response({ ok: true, ms: 4, effort: "low" })));
    await flush();
  });

  it("fails closed when an inherited provider, model or effort baseline is unresolved", async () => {
    const inheritedPurpose: ModelPurpose = {
      ...purpose,
      effective: { provider: provider.id, model: "inherited-model", effort: "medium" },
      override: { provider: provider.id, model: "saved-model", effort: "high" },
    };
    await act(async () => root.render(
      <DirectAPIEditor
        manager={manager}
        purpose={inheritedPurpose}
        provider={currentProvider}
        draft={{ provider: "", model: "", effort: "", runtime: "" }}
        disabled={false}
        onChange={() => {}}
      />,
    ));
    await flush();

    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_inheritance"));
    expect(testButton().disabled).toBe(true);
    expect(calls.filter((call) => call.path.endsWith("/launchers/discover"))).toHaveLength(0);
    await testProbe();
    expect(calls.filter((call) => call.path.endsWith("/launchers/test"))).toHaveLength(0);
  });

  it("ignores discovery from the previous provider after a provider remount", async () => {
    const first = deferred<Response>();
    const second = deferred<Response>();
    let discoveries = 0;
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? (++discoveries === 1 ? first.promise : second.promise)
      : response({ ok: true, ms: 1 }));

    await render();
    currentProvider = { ...provider, id: "launcher.other" };
    await render();
    await act(async () => second.resolve(response({ models: [{ id: "new", name: "New" }] })));
    await flush();
    await act(async () => first.resolve(response({ models: [{ id: "stale", name: "Stale" }] })));
    await flush();

    expect(selects()[0].textContent).toContain("New (new)");
    expect(selects()[0].textContent).not.toContain("Stale");
  });

  it("ignores probe responses after unmount and reopen", async () => {
    const oldProbe = deferred<Response>();
    let probes = 0;
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : (++probes === 1 ? oldProbe.promise : response({ ok: true, ms: 3, effort: "low" })));

    await render();
    await testProbe();
    await act(async () => root.render(null));
    await render();
    await testProbe();
    await flush();
    await act(async () => oldProbe.resolve(response({ ok: true, ms: 99, effort: "low" })));
    await flush();

    expect(host.textContent).toContain(t("settings-modelconfiguration.api_test_ok", { ms: 3 }));
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 99 }));
  });

  it.each(["en", "zh-Hans", "ja"] as const)("localizes auth, model, timeout and other errors in %s", async (language) => {
    const cases = [
      ["auth", "401 unauthorized", "auth"],
      ["model_not_found", "model not found", "model"],
      ["timeout", "deadline exceeded", "timeout"],
      ["other", "gateway broke", "other"],
    ] as const;
    await initI18n(language);

    for (const [label, detail, kind] of cases) {
      jsonFetch((call) => call.path.endsWith("/launchers/discover")
        ? response({ models })
        : response({ ok: false, error: detail }));
      await render();
      await testProbe();
      await flush();
      expect(host.querySelector('[role="alert"]')?.textContent, `${language} ${label}`)
        .toContain(t(`settings-modelconfiguration.api_test_${kind}`));
    }
  });

  it.each([
    ["en", "This hub cannot verify"],
    ["zh-Hans", "当前 hub 无法验证"],
    ["ja", "このハブでは"],
  ])("does not verify a successful old-hub response without effort echo (%s)", async (language, expected) => {
    await initI18n(language);
    jsonFetch((call) => call.path.endsWith("/launchers/discover")
      ? response({ models })
      : response({ ok: true, ms: 5 }));
    await render();
    await testProbe();
    await flush();

    expect(host.querySelector('[role="alert"]')?.textContent).toContain(expected);
    expect(host.textContent).not.toContain(t("settings-modelconfiguration.api_test_ok", { ms: 5 }));
  });
});
