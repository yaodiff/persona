// @vitest-environment jsdom
// A command waiting for a line (`command_input`, #14527) on the web: the prompt over the field, the command and the lines
// it printed (a web address in them a link), Not now and Send. Send sends the typed line once, inside the answer, and
// empties the field, as Not now and the card going do; Enter never sends; the line is hidden while typed when the prompt
// reads as secret, with Show; no quick path (a row's answers, a notification's Allow) answers it. The hub is faked (apiOp).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const calls = vi.hoisted(() => [] as { path: string; body: Record<string, unknown> }[]);
vi.mock("../../lib/ops", () => ({
  apiOp: vi.fn(async (path: string, init: { json: Record<string, unknown> }) => {
    calls.push({ path, body: init.json });
    return {};
  }),
}));
import { t } from "../../i18n";
import { quickAnswers } from "../../lib/quick-answers";
import { getState } from "../../lib/store";
import type { Interaction, Session } from "../../lib/types";
import { CommandInputCard } from "./CommandInputCard";
import { InteractionCard } from "./Interactions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 10; i++) await act(async () => { await Promise.resolve(); });
};
const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label) as HTMLButtonElement | undefined;
const field = () => host.querySelector("input") as HTMLInputElement;
const type = (value: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });

const PROMPT = "Enter the authorization code:";
const card = (input: Record<string, unknown> = {}): Interaction => ({
  id: "ci_0123456789abcdef", machine_id: "m", session_id: "s", session_title: "Sign in", kind: "permission", tool_name: "command_input",
  tool_title: "Command input", agent: "agy",
  tool_input: { command: "example-cli login", task: "task-7", prompt: PROMPT, masked: true,
    output: "To sign in, visit this URL in any browser:\n  https://auth.example.com/authorize?client=demo.\nAfter approving, the page shows a code.", ...input },
  summary: `A command is waiting for your input: ${PROMPT}`,
  fields: [{ key: "input", label: PROMPT, secret: true }],
});

beforeEach(() => {
  calls.length = 0;
  Object.assign(getState(), { sessions: { m: [{ machine_id: "m", session_id: "s" } as Session] }, interactions: [], answered: {} });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.restoreAllMocks();
});

describe("CommandInputCard", () => {
  it("shows the prompt over the field, the command and its output with the address as a link", () => {
    act(() => root.render(<CommandInputCard ix={card()} />));
    expect(host.querySelector("label")?.textContent).toBe(PROMPT);
    expect(host.querySelector("[data-command-input-command]")?.textContent).toBe("example-cli login");
    const link = host.querySelector("[data-command-input-output] a") as HTMLAnchorElement;
    // the sentence's full stop after the address is not part of it
    expect(link.getAttribute("href")).toBe("https://auth.example.com/authorize?client=demo");
    expect(link.getAttribute("rel")).toContain("noopener");
    expect(host.querySelector("[data-command-input-output]")?.textContent).toContain("After approving");
    expect(host.querySelectorAll("input, textarea, [contenteditable]").length).toBe(1);
  });

  it("hides a secret prompt's line until Show, never offers it to a password manager", () => {
    act(() => root.render(<CommandInputCard ix={card()} />));
    const input = field();
    expect(input.type).toBe("password");
    expect(input.getAttribute("autocomplete")).toBe("one-time-code");
    expect(input.getAttribute("spellcheck")).toBe("false");
    expect(input.closest("form")).toBeNull();
    expect(input.hasAttribute("name")).toBe(false);
    act(() => button(t("chat-command-input.show"))!.click());
    expect(field().type).toBe("text");
    act(() => button(t("chat-command-input.hide"))!.click());
    expect(field().type).toBe("password");
  });

  it("shows the line of a prompt that is not secret", () => {
    act(() => root.render(<CommandInputCard ix={card({ prompt: "Project name:", masked: false })} />));
    expect(field().type).toBe("text");
  });

  it("sends the typed line once with Send, and empties the field; Enter never sends", async () => {
    act(() => root.render(<CommandInputCard ix={card()} />));
    const send = button(t("chat-command-input.send"))!;
    expect(send.disabled).toBe(true);
    type("test-code-123");
    act(() => field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    await flush();
    expect(calls).toEqual([]);
    await act(async () => send.click());
    await flush();
    expect(calls).toHaveLength(1);
    expect(calls[0].path).toContain("/interactions/ci_0123456789abcdef/resolve");
    expect(calls[0].body).toMatchObject({ allow: true, values: { input: "test-code-123" } });
    expect(field().value).toBe("");
  });

  it("Not now sends a plain no and nothing typed", async () => {
    act(() => root.render(<CommandInputCard ix={card()} />));
    type("test-code-123");
    await act(async () => button(t("chat-command-input.not_now"))!.click());
    await flush();
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toMatchObject({ allow: false });
    expect(JSON.stringify(calls[0].body)).not.toContain("test-code-123");
    expect(field().value).toBe("");
  });

  it("empties its field when the card goes", () => {
    act(() => root.render(<CommandInputCard ix={card()} />));
    type("test-code-123");
    const input = field();
    act(() => root.render(<></>));
    expect(input.value).toBe("");
  });

  it("is the card a command-input interaction gets, and no quick path answers it", async () => {
    act(() => root.render(<InteractionCard ix={card()} />));
    // drawn by the permission card's lazy chunk
    await vi.waitFor(() => expect(host.querySelector("[data-command-input-command]")).not.toBeNull());
    expect(quickAnswers(card())).toBeNull();
    expect(quickAnswers({ ...card(), fields: undefined })).toBeNull();
  });
});
