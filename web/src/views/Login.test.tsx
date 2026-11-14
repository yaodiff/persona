// @vitest-environment jsdom
// The login page (#3356): the master-key form starts folded, each form signs in with its own field, and a rejected
// token says which kind of thing was wrong (a link vs. the hub's master key).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fake = vi.hoisted(() => ({ pair: vi.fn<(tok: string) => Promise<void>>() }));
vi.mock("../lib/auth", () => ({ pair: (tok: string) => fake.pair(tok) }));
vi.mock("../lib/hubs", () => ({ here: () => "", rememberHub: () => {} }));
vi.mock("./Hubs", () => ({ OtherHubs: () => null }));

import { t } from "../i18n";
import { ApiError, errorText } from "../lib/api";
import { Login } from "./Login";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;

beforeEach(() => {
  fake.pair.mockReset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

const input = (label: string) => host.querySelector<HTMLInputElement>(`input[aria-label="${label}"]`)!;
const advancedToggle = () => host.querySelector<HTMLButtonElement>('button[aria-controls="login-advanced"]')!;
// each form's own error line: the link form's under it, the master key's under its button inside its form
const alert = (from: "link" | "master" = "link") => {
  const master = host.querySelector<HTMLInputElement>('input[type="password"]')!.form!;
  const [own] = [...host.querySelectorAll('[role="alert"]')].filter((el) => master.contains(el) === (from === "master"));
  return own.textContent;
};

function type(el: HTMLInputElement, value: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  act(() => {
    setter.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function submit(el: HTMLInputElement) {
  await act(async () => {
    el.form!.requestSubmit();
  });
}

describe("Login", () => {
  it.each(["link", "master"] as const)("explains an exchange 401 for the %s form regardless of the server's wording", async (from) => {
    const onDone = vi.fn();
    fake.pair.mockRejectedValueOnce(new ApiError(401, "Unauthorized", {}));
    act(() => root.render(<Login onDone={onDone} />));
    if (from === "master") act(() => advancedToggle().click());
    const field = input(t(from === "link" ? "login.link_placeholder" : "login.master_placeholder"));
    type(field, from === "link" ? "https://hub.example/#token=jp_example" : "synthetic-master-key");
    await submit(field);
    expect(alert(from)).toBe(t(from === "link" ? "login.invalid_link" : "login.invalid_token"));
    expect(onDone).not.toHaveBeenCalled();
    expect(field.form!.querySelector("button")!.disabled).toBe(false);
  });

  it.each([new ApiError(500, "storage unavailable", {}), new TypeError("Failed to fetch")])("keeps an exchange failure visible and allows retry: %s", async (error) => {
    const onDone = vi.fn();
    fake.pair.mockRejectedValueOnce(error);
    act(() => root.render(<Login onDone={onDone} />));
    const field = input(t("login.link_placeholder"));
    type(field, "https://hub.example/#token=jp_example");
    await submit(field);
    expect(alert()).toBe(errorText(error));
    expect(onDone).not.toHaveBeenCalled();
    expect(field.form!.querySelector("button")!.disabled).toBe(false);
    fake.pair.mockResolvedValueOnce();
    await submit(field);
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(alert()).toBe("");
  });

  it("starts with the master-key section folded", () => {
    act(() => root.render(<Login onDone={() => {}} />));
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("false");
    act(() => advancedToggle().click());
    expect(advancedToggle().getAttribute("aria-expanded")).toBe("true");
  });

  it("signs in with the pasted link and reports a bad link as a bad link", async () => {
    const onDone = vi.fn();
    fake.pair.mockRejectedValueOnce(new Error("invalid token"));
    act(() => root.render(<Login onDone={onDone} />));
    type(input(t("login.link_placeholder")), "  https://hub.example/#token=jp_abc  ");
    await submit(input(t("login.link_placeholder")));
    expect(fake.pair).toHaveBeenCalledWith("https://hub.example/#token=jp_abc");
    expect(alert()).toBe(t("login.invalid_link"));
    expect(onDone).not.toHaveBeenCalled();

    fake.pair.mockResolvedValueOnce();
    await submit(input(t("login.link_placeholder")));
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("signs in with the master key on its own field and reports its own error", async () => {
    fake.pair.mockRejectedValueOnce(new Error("invalid token"));
    act(() => root.render(<Login onDone={() => {}} />));
    type(input(t("login.link_placeholder")), "not-this-one");
    act(() => advancedToggle().click());
    type(input(t("login.master_placeholder")), "master-key");
    await submit(input(t("login.master_placeholder")));
    expect(fake.pair).toHaveBeenCalledWith("master-key");
    expect(alert("master")).toBe(t("login.invalid_token"));
  });

  it("says the master key's error under its own button, not under the link form, and drops it once edited (web usability audit item 41)", async () => {
    fake.pair.mockRejectedValueOnce(new ApiError(401, "Unauthorized", {}));
    act(() => root.render(<Login onDone={() => {}} />));
    act(() => advancedToggle().click());
    const key = input(t("login.master_placeholder"));
    type(key, "wrong-master");
    await submit(key);
    const shown = [...host.querySelectorAll('[role="alert"]')].find((el) => el.textContent === t("login.invalid_token"))!;
    // in the master key's form, after its button
    expect(key.form!.contains(shown)).toBe(true);
    expect(key.form!.querySelector('button[type="submit"]')!.compareDocumentPosition(shown) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(alert("link")).toBe("");
    type(key, "wrong-master-2");
    expect(alert("master")).toBe("");
  });

  it("drops the link form's error once the link is edited, a signed-out note included", async () => {
    fake.pair.mockRejectedValueOnce(new Error("invalid token"));
    act(() => root.render(<Login onDone={() => {}} error="This browser was signed out." />));
    expect(alert("link")).toBe("This browser was signed out.");
    const link = input(t("login.link_placeholder"));
    type(link, "https://hub.example/#token=jp_abc");
    expect(alert("link")).toBe("");
    await submit(link);
    expect(alert("link")).toBe(t("login.invalid_link"));
    type(link, "https://hub.example/#token=jp_abd");
    expect(alert("link")).toBe("");
  });
});
