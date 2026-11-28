// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const apiMock = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({}));
vi.mock("../../lib/api", async (orig) => ({
  ...(await orig<typeof import("../../lib/api")>()),
  api: (path: string, init?: unknown) => apiMock(path, init),
}));

vi.mock("../../lib/store", async (orig) => {
  const actual = await orig<typeof import("../../lib/store")>();
  return {
    ...actual,
    patchAssistant: vi.fn(),
    refreshTodos: vi.fn(),
  };
});

const { initI18n } = await import("../../i18n");
const { resetTodosV2StateForTest, todosV2LabsSource } = await import("../../lib/labs-todos-v2");
const { chooseManager } = await import("../../lib/manager-choice");
const { TodosV2AttentionPrefs } = await import("./TodosV2AttentionPrefs");

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

const flush = async () => {
  for (let i = 0; i < 4; i++) {
    await act(async () => {
      await Promise.resolve();
    });
  }
};

function setInputValue(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  setter.call(el, value);
  el.dispatchEvent(new Event("input", { bubbles: true }));
  el.dispatchEvent(new Event("change", { bubbles: true }));
}

beforeEach(async () => {
  await initI18n("en");
  apiMock.mockReset();
  resetTodosV2StateForTest();
  chooseManager("primary");
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetTodosV2StateForTest();
  chooseManager(null);
});

describe("TodosV2AttentionPrefs quiet hours & digest window (#16433)", () => {
  it("renders nothing and fires zero requests when todos_v2 is false", async () => {
    apiMock.mockResolvedValueOnce({ manager: "primary", todos_v2: false });
    await todosV2LabsSource("primary").read();
    apiMock.mockClear();

    await act(async () => {
      root.render(<TodosV2AttentionPrefs manager="primary" />);
    });
    await flush();

    expect(host.querySelector("[data-todos-v2-attention-prefs]")).toBeNull();
    expect(apiMock).not.toHaveBeenCalled();
  });

  it("validates quiet hours client-side, saves valid overnight range, clears it, and updates digest window", async () => {
    apiMock.mockResolvedValueOnce({
      manager: "primary",
      todos_v2: true,
      todos_v2_quiet_hours: "",
      todos_v2_digest_window_minutes: 0,
    });
    await todosV2LabsSource("primary").read();
    apiMock.mockClear();

    await act(async () => {
      root.render(<TodosV2AttentionPrefs manager="primary" />);
    });
    await flush();

    const startInput = host.querySelector<HTMLInputElement>('[data-testid="todos-v2-quiet-hours-start"]')!;
    const endInput = host.querySelector<HTMLInputElement>('[data-testid="todos-v2-quiet-hours-end"]')!;
    const saveBtn = host.querySelector<HTMLButtonElement>('[data-testid="todos-v2-quiet-hours-save"]')!;
    expect(startInput).not.toBeNull();
    expect(endInput).not.toBeNull();

    // Equal start and end is rejected client-side without calling PUT.
    await act(async () => {
      setInputValue(startInput, "22:00");
      setInputValue(endInput, "22:00");
    });
    await act(async () => saveBtn.click());
    await flush();
    expect(host.querySelector('[data-testid="todos-v2-quiet-hours-error"]')).not.toBeNull();
    expect(apiMock).not.toHaveBeenCalled();

    // Valid overnight range saves via PUT /assistant.
    apiMock.mockResolvedValueOnce({
      manager: "primary",
      todos_v2: true,
      todos_v2_quiet_hours: "22:00-08:00",
      todos_v2_digest_window_minutes: 0,
    });
    await act(async () => {
      setInputValue(endInput, "08:00");
    });
    await act(async () => saveBtn.click());
    await flush();
    expect(host.querySelector('[data-testid="todos-v2-quiet-hours-error"]')).toBeNull();
    expect(apiMock).toHaveBeenCalledWith(
      "/assistant",
      expect.objectContaining({
        method: "PUT",
        json: { todos_v2_quiet_hours: "22:00-08:00" },
      }),
    );

    // Clear button resets quiet hours to "".
    apiMock.mockResolvedValueOnce({
      manager: "primary",
      todos_v2: true,
      todos_v2_quiet_hours: "",
      todos_v2_digest_window_minutes: 0,
    });
    const clearBtn = host.querySelector<HTMLButtonElement>('[data-testid="todos-v2-quiet-hours-clear"]')!;
    await act(async () => clearBtn.click());
    await flush();
    expect(apiMock).toHaveBeenLastCalledWith(
      "/assistant",
      expect.objectContaining({
        method: "PUT",
        json: { todos_v2_quiet_hours: "" },
      }),
    );

    // Selecting digest window (60m) saves immediately via PUT /assistant.
    apiMock.mockResolvedValueOnce({
      manager: "primary",
      todos_v2: true,
      todos_v2_quiet_hours: "",
      todos_v2_digest_window_minutes: 60,
    });
    const digestRow = host.querySelector('[data-todos-v2-pref="todos_v2_digest_window_minutes"]')!;
    const btn60 = [...digestRow.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("60m"))!;
    expect(btn60).toBeDefined();
    await act(async () => btn60.click());
    await flush();
    expect(apiMock).toHaveBeenLastCalledWith(
      "/assistant",
      expect.objectContaining({
        method: "PUT",
        json: { todos_v2_digest_window_minutes: 60 },
      }),
    );
  });
});
