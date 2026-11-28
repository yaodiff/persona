// @vitest-environment jsdom
// Settings › Labs (#9877) through the generic configuration form: the terminal renderer module's choice kept in this
// browser (Standard as no key), the old WebGL switch shown as WebGL, a value from another version named and left until a
// choice replaces it, a browser that won't keep the choice saying the save is unconfirmed, and another tab's change.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { registerModule, resetModulesForTest } from "../../core/module-host";
import { t } from "../../i18n";
import { allTypes } from "../../lib/configuration/fixtures/shared";
import type { ConfigurationUpdate } from "../../lib/configuration/contract";
import { chooseManager, MANAGER_HEADER } from "../../lib/manager-choice";
import { resetTodosV2StateForTest } from "../../lib/labs-todos-v2";
import { LabsDialog, LabsSettings, type LabsModule } from "./Labs";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "persona.terminal.webgl";
let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
  resetTodosV2StateForTest();
  localStorage.clear();
});

const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => {});
};
const field = () => host.querySelector<HTMLElement>('[data-configuration-field="renderer"]')!;
const radios = () => [...field().querySelectorAll<HTMLButtonElement>('[role="radio"]')];
const chosen = () => radios().find((r) => r.getAttribute("aria-checked") === "true")?.textContent;
const render = async () => {
  act(() => root.render(<LabsSettings />));
  await settle();
};
const click = async (el: HTMLElement) => {
  act(() => el.click());
  await settle();
};

describe("Settings › Labs", () => {
  it("keeps shipped module switches out of Labs and its dialog", async () => {
    registerModule({ id: "garden", title: () => "Garden", description: () => "A synthetic module", load: async () => ({}) });
    try {
      await render();
      expect(host.querySelector('[data-configuration="features"]')).toBeNull();
      await act(async () => root.render(<LabsDialog open onClose={() => {}} />));
      expect(document.querySelector('[data-labs-dialog] [data-configuration="terminal"]')).not.toBeNull();
      expect(document.querySelector('[data-labs-dialog] [data-configuration="features"]')).toBeNull();
    } finally {
      resetModulesForTest();
    }
  });

  it("explains Labs and that each item states its own scope, and shows the terminal renderer under Agents", async () => {
    await render();
    expect(host.textContent).toContain(t("settings-labs.intro"));
    expect(host.textContent).toContain(t("settings-labs.scope_note"));
    expect(host.querySelector('[data-configuration="terminal"] h2')?.textContent).toBe(t("settings-labs.area_agents"));
    expect(field().textContent).toContain(t("settings-labs.terminal_renderer"));
    expect(field().textContent).toContain(t("settings-labs.terminal_renderer_hint"));
  });

  it("shows the module's default in the footer and its scope, activation and rollback as details", async () => {
    await render();
    expect(field().textContent).toContain(`${t("settings-configuration.default")} ${t("settings-labs.terminal_renderer_standard")}`);
    expect([...field().querySelectorAll("dt")].map((d) => d.textContent)).toEqual([t("settings-labs.scope"), t("settings-labs.activation"), t("settings-labs.rollback")]);
    expect([...field().querySelectorAll("dd")].map((d) => d.textContent)).toEqual([
      t("settings-labs.scope_browser"),
      t("settings-labs.terminal_renderer_activation"),
      t("settings-labs.terminal_renderer_rollback"),
    ]);
  });

  it("offers Standard and WebGL as one named choice, Standard chosen with nothing saved", async () => {
    await render();
    expect(field().querySelector('[role="radiogroup"]')?.getAttribute("aria-label")).toBe(t("settings-labs.terminal_renderer"));
    expect(radios().map((r) => r.textContent)).toEqual([t("settings-labs.terminal_renderer_standard"), t("settings-labs.terminal_renderer_webgl")]);
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("keeps WebGL as the old switch's value, says it is saved, and Reset removes it again", async () => {
    await render();
    await click(radios()[1]);
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
    expect(field().textContent).toContain(t("settings-configuration.saved"));
    const reset = [...field().querySelectorAll("button")].find((b) => b.textContent === t("settings-configuration.reset"))!;
    await click(reset);
    expect(localStorage.getItem(KEY)).toBeNull();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("shows WebGL for the WebGL switch saved before Labs", async () => {
    localStorage.setItem(KEY, "1");
    await render();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
    expect(field().textContent).not.toContain(t("settings-labs.invalid"));
  });

  it("names a saved value it doesn't recognize, leaves it, and replaces it once a choice is made", async () => {
    localStorage.setItem(KEY, "webgl2");
    await render();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
    expect(field().textContent).toContain(t("settings-labs.invalid"));
    expect(localStorage.getItem(KEY)).toBe("webgl2");
    await click(radios()[1]);
    expect(localStorage.getItem(KEY)).toBe("1");
    expect(field().textContent).not.toContain(t("settings-labs.invalid"));
  });

  it("says when the browser won't keep the choice, and shows what is still in effect", async () => {
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new DOMException("full", "QuotaExceededError");
    });
    await render();
    await click(radios()[1]);
    expect(field().querySelector('[role="alert"]')?.textContent).toBe(t("settings-labs.save_failed"));
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
    expect(field().querySelector("[aria-live]")?.textContent).not.toContain(t("settings-configuration.saved"));
  });

  it("says when the browser won't let it read what is saved", async () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new DOMException("denied", "SecurityError");
    });
    await render();
    expect(field().textContent).toContain(t("settings-labs.unreadable"));
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_standard"));
  });

  it("follows a choice made in another tab", async () => {
    await render();
    localStorage.setItem(KEY, "1");
    act(() => void window.dispatchEvent(new StorageEvent("storage", { key: KEY })));
    await settle();
    expect(chosen()).toBe(t("settings-labs.terminal_renderer_webgl"));
  });

  it("mounts the assistant-scoped New To-dos & Tasks toggle for the chosen manager", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body ?? "{}")) as { todos_v2?: boolean };
          return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: body.todos_v2 === true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: false }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });
    chooseManager("example");
    try {
      await render();
      const todosForm = host.querySelector<HTMLElement>('[data-configuration="assistant-todos-v2"]');
      expect(todosForm).not.toBeNull();
      const row = todosForm!.querySelector<HTMLElement>('[data-configuration-field="todos_v2"]')!;
      expect(row.textContent).toContain(t("settings-labs.todos_v2_title"));
      expect(row.textContent).toContain(t("settings-labs.todos_v2_hint"));
      expect([...row.querySelectorAll("dt")].map((d) => d.textContent)).toEqual([
        t("settings-labs.scope"),
        t("settings-labs.activation"),
        t("settings-labs.rollback"),
      ]);
      const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
      expect(toggle.getAttribute("aria-checked")).toBe("false");
      await click(toggle);
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      const putCall = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
      expect(putCall).toBeDefined();
      expect(new Headers(putCall?.[1]?.headers).get(MANAGER_HEADER)).toBe("example");
      expect(JSON.parse(String(putCall?.[1]?.body))).toEqual({ todos_v2: true, expected_todos_v2: false });
    } finally {
      act(() => chooseManager(null));
    }
  });

  it("mounts the assistant-scoped Unified Integrations & Extensions toggle for the chosen manager", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body ?? "{}")) as { integrations_v2?: boolean };
          return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: false, integrations_v2: body.integrations_v2 === true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: false, integrations_v2: false }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });
    chooseManager("example");
    try {
      await render();
      const integrationsForm = host.querySelector<HTMLElement>('[data-configuration="assistant-integrations-v2"]');
      expect(integrationsForm).not.toBeNull();
      const row = integrationsForm!.querySelector<HTMLElement>('[data-configuration-field="integrations_v2"]')!;
      expect(row.textContent).toContain(t("settings-labs.integrations_v2_title"));
      expect(row.textContent).toContain(t("settings-labs.integrations_v2_hint"));
      expect([...row.querySelectorAll("dt")].map((d) => d.textContent)).toEqual([
        t("settings-labs.scope"),
        t("settings-labs.activation"),
        t("settings-labs.rollback"),
      ]);
      const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
      expect(toggle.getAttribute("aria-checked")).toBe("false");
      await click(toggle);
      expect(toggle.getAttribute("aria-checked")).toBe("true");
      const putCall = fetchMock.mock.calls.find(([, init]) => init?.method === "PUT");
      expect(putCall).toBeDefined();
      expect(new Headers(putCall?.[1]?.headers).get(MANAGER_HEADER)).toBe("example");
      expect(JSON.parse(String(putCall?.[1]?.body))).toEqual({ integrations_v2: true, expected_integrations_v2: false });
    } finally {
      act(() => chooseManager(null));
    }
  });

  it("renders Unified Integrations & Extensions as read-only when an older hub omits integrations_v2", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: false }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });
    chooseManager("example");
    try {
      await render();
      const integrationsForm = host.querySelector<HTMLElement>('[data-configuration="assistant-integrations-v2"]');
      expect(integrationsForm).not.toBeNull();
      const row = integrationsForm!.querySelector<HTMLElement>('[data-configuration-field="integrations_v2"]')!;
      expect(row.textContent).toContain(t("settings-labs.integrations_v2_unsupported"));
      const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
      expect(toggle.disabled).toBe(true);
    } finally {
      act(() => chooseManager(null));
    }
  });

  it("hides todos_v2 attention preferences when todos_v2 is false and expands them on toggle", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body ?? "{}")) as { todos_v2?: boolean };
          return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: body.todos_v2 === true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({ manager: "example", work_guide: false, todos_v2: false }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });
    chooseManager("example");
    try {
      await render();
      const todosForm = host.querySelector<HTMLElement>('[data-configuration="assistant-todos-v2"]')!;
      expect(todosForm.querySelector("[data-todos-v2-attention-prefs]")).toBeNull();

      const toggle = todosForm.querySelector<HTMLButtonElement>('[data-configuration-field="todos_v2"] [role="switch"]')!;
      await click(toggle);
      const panel = todosForm.querySelector<HTMLElement>("[data-todos-v2-attention-prefs]");
      expect(panel).not.toBeNull();
      expect(panel!.textContent).toContain(t("settings-labs.todos_v2_prefs_title"));
      // Older hub omitting the 4 fields falls back to defaults: all_autonomous, 24h, 48h, true
      const autoSeenChecked = panel!.querySelector('[data-todos-v2-pref="todos_v2_auto_seen"] [aria-checked="true"]');
      expect(autoSeenChecked?.textContent).toBe(t("settings-labs.todos_v2_auto_seen_all_autonomous"));
      const pendingChecked = panel!.querySelector('[data-todos-v2-pref="todos_v2_pending_review_sla_hours"] [aria-checked="true"]');
      expect(pendingChecked?.textContent).toBe(t("settings-labs.todos_v2_hours", { hours: 24 }));
      const canWaitChecked = panel!.querySelector('[data-todos-v2-pref="todos_v2_can_wait_sla_hours"] [aria-checked="true"]');
      expect(canWaitChecked?.textContent).toBe(t("settings-labs.todos_v2_hours", { hours: 48 }));
      const foldSwitch = panel!.querySelector<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_fold_children"] [role="switch"]')!;
      expect(foldSwitch.getAttribute("aria-checked")).toBe("true");

      // Turning todos_v2 back off hides the sub-panel immediately
      await click(toggle);
      expect(todosForm.querySelector("[data-todos-v2-attention-prefs]")).toBeNull();
    } finally {
      act(() => chooseManager(null));
    }
  });

  it("displays server attention preferences, saves all 4 individually without expected_todos_v2, and refreshes to-dos", async () => {
    const serverState: Record<string, unknown> = {
      manager: "example",
      work_guide: false,
      todos_v2: true,
      todos_v2_auto_seen: "subtasks",
      todos_v2_pending_review_sla_hours: 12,
      todos_v2_can_wait_sla_hours: 72,
      todos_v2_fold_children: false,
    };
    const putPayloads: Record<string, unknown>[] = [];
    let todosRefreshes = 0;

    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        if (init?.method === "PUT") {
          const body = JSON.parse(String(init.body ?? "{}")) as Record<string, unknown>;
          putPayloads.push(body);
          Object.assign(serverState, body);
          return new Response(JSON.stringify(serverState), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify(serverState), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      if (url.split("?")[0].endsWith("/api/v1/todos")) {
        todosRefreshes++;
        return new Response(JSON.stringify({ todos: [] }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });

    chooseManager("example");
    try {
      await render();
      const panel = host.querySelector<HTMLElement>("[data-todos-v2-attention-prefs]")!;
      expect(panel).not.toBeNull();

      // Verify initial server values
      expect(panel.querySelector('[data-todos-v2-pref="todos_v2_auto_seen"] [aria-checked="true"]')?.textContent)
        .toBe(t("settings-labs.todos_v2_auto_seen_subtasks"));
      expect(panel.querySelector('[data-todos-v2-pref="todos_v2_pending_review_sla_hours"] [aria-checked="true"]')?.textContent)
        .toBe(t("settings-labs.todos_v2_hours", { hours: 12 }));
      expect(panel.querySelector('[data-todos-v2-pref="todos_v2_can_wait_sla_hours"] [aria-checked="true"]')?.textContent)
        .toBe(t("settings-labs.todos_v2_hours", { hours: 72 }));
      const foldSwitch = panel.querySelector<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_fold_children"] [role="switch"]')!;
      expect(foldSwitch.getAttribute("aria-checked")).toBe("false");

      // 1. Change auto_seen -> "off"
      const offAutoSeen = [...panel.querySelectorAll<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_auto_seen"] [role="radio"]')]
        .find((b) => b.textContent === t("settings-labs.todos_v2_auto_seen_off"))!;
      await click(offAutoSeen);
      expect(putPayloads.at(-1)).toEqual({ todos_v2_auto_seen: "off" });
      expect(panel.textContent).toContain(t("settings-configuration.saved"));

      // 2. Change pending_review_sla_hours -> 0
      const offPending = [...panel.querySelectorAll<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_pending_review_sla_hours"] [role="radio"]')]
        .find((b) => b.textContent === t("settings-labs.todos_v2_pending_review_sla_off"))!;
      await click(offPending);
      expect(putPayloads.at(-1)).toEqual({ todos_v2_pending_review_sla_hours: 0 });

      // 3. Change can_wait_sla_hours -> 24
      const canWait24 = [...panel.querySelectorAll<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_can_wait_sla_hours"] [role="radio"]')]
        .find((b) => b.textContent === t("settings-labs.todos_v2_hours", { hours: 24 }))!;
      await click(canWait24);
      expect(putPayloads.at(-1)).toEqual({ todos_v2_can_wait_sla_hours: 24 });

      // 4. Toggle fold_children -> true
      await click(foldSwitch);
      expect(putPayloads.at(-1)).toEqual({ todos_v2_fold_children: true });
      expect(foldSwitch.getAttribute("aria-checked")).toBe("true");

      expect(putPayloads).toHaveLength(4);
      expect(todosRefreshes).toBeGreaterThanOrEqual(4);
    } finally {
      act(() => chooseManager(null));
    }
  });

  it("shows an error alert and preserves the previous preference value when saving fails", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      if (url.endsWith("/api/v1/assistant")) {
        if (init?.method === "PUT") {
          return new Response(JSON.stringify({ error: "hub unavailable" }), {
            status: 500,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({
          manager: "example",
          work_guide: false,
          todos_v2: true,
          todos_v2_auto_seen: "all_autonomous",
          todos_v2_pending_review_sla_hours: 24,
          todos_v2_can_wait_sla_hours: 48,
          todos_v2_fold_children: true,
        }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }
      return new Response("{}", { status: 404 });
    });

    chooseManager("example");
    try {
      await render();
      const panel = host.querySelector<HTMLElement>("[data-todos-v2-attention-prefs]")!;
      const offBtn = [...panel.querySelectorAll<HTMLButtonElement>('[data-todos-v2-pref="todos_v2_auto_seen"] [role="radio"]')]
        .find((b) => b.textContent === t("settings-labs.todos_v2_auto_seen_off"))!;
      await click(offBtn);

      expect(panel.querySelector('[role="alert"]')?.textContent).toContain("hub unavailable");
      expect(panel.querySelector('[data-todos-v2-pref="todos_v2_auto_seen"] [aria-checked="true"]')?.textContent)
        .toBe(t("settings-labs.todos_v2_auto_seen_all_autonomous"));
    } finally {
      act(() => chooseManager(null));
    }
  });
});

describe.each(["settings", "feature dialog"])("experiment guidance from %s", (entry) => {
  it("shows notes, asks only for the trial and reports its timing after saving", async () => {
    const update = vi.fn(async (u: ConfigurationUpdate) => ({
      ...allTypes, revision: "next", values: { ...allTypes.values, ...u.changes },
    }));
    const module: LabsModule = {
      source: { key: "example", read: async () => allTypes, update },
      guidance: {
        notes: [{ kind: "info", text: "Try different layouts." }, { kind: "info", text: " " }],
        fields: { compact: {
          notes: [{ kind: "warning", text: "Cards show less detail." }],
          rules: [{
            when: { action: "change", to: true },
            confirm: { title: "Use compact cards?", body: "Cards will show a shorter preview.", scope: "This browser", impact: "Shorter previews", activation: "New views", confirmLabel: "Save compact layout" },
            afterSave: "Saved. New views will use compact cards.",
          }],
        } },
      },
    };
    const outerClose = vi.fn();
    await act(async () => root.render(entry === "settings" ? <LabsSettings modules={[module]} /> : <LabsDialog modules={[module]} open onClose={outerClose} />));
    const form = document.querySelector<HTMLElement>('[data-configuration="example-preferences"]')!;
    const row = form.querySelector<HTMLElement>('[data-configuration-field="compact"]')!;
    expect(form.textContent?.split("Try different layouts.")).toHaveLength(2);
    expect(row.textContent).toContain("Cards show less detail.");
    const toggle = row.querySelector<HTMLButtonElement>('[role="switch"]')!;
    toggle.focus();
    const dialog = () => [...document.querySelectorAll<HTMLElement>('[role="dialog"]')].find(d => d.querySelector("h2")?.textContent === "Use compact cards?")!;
    await click(toggle);
    const modal = dialog();
    expect(document.activeElement).toBe(modal);
    expect(document.getElementById(modal.getAttribute("aria-describedby")!)?.textContent).toBe("Cards will show a shorter preview.");
    expect(update).not.toHaveBeenCalled();
    act(() => window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(dialog()).toBeUndefined();
    expect(document.activeElement).toBe(toggle);
    expect(outerClose).not.toHaveBeenCalled();
    await click(toggle);
    const submit = [...dialog().querySelectorAll("button")].find(b => b.textContent === "Save compact layout")!;
    await click(submit);
    expect(update).toHaveBeenCalledTimes(1);
    expect(row.querySelector("[aria-live]")?.textContent).toBe("Saved. New views will use compact cards.");
    await click(toggle);
    expect(dialog()).toBeUndefined();
    expect(update).toHaveBeenCalledTimes(2);
    expect(row.querySelector("[aria-live]")?.textContent).toBe(t("settings-configuration.saved"));
  });
});
