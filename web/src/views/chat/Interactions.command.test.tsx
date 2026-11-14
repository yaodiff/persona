// @vitest-environment jsdom
// Claude Code's command prompt on the screen card (#5266): it says what is asked in plain words (the agent's words, the
// command, the warning plainly with Claude Code's own under "Show original", the countdown running) and answers the
// prompt's Yes / No with Allow / Deny, which send the prompt's own options. A hub before #5265 sends none of the new
// fields: the card stays the terminal's question with its numbered options. The hub is faked (apiOp).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
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
// A slow real chunk makes the old fixed 100 ms body wait fail deterministically.
vi.mock("./CommandScreenCard", async (importOriginal) => {
  await new Promise((resolve) => setTimeout(resolve, 1200));
  return await importOriginal<typeof import("./CommandScreenCard")>();
});
import { InteractionCard } from "./Interactions";
import { getState } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => { await Promise.resolve(); });
};

const card = (deadlineIn: number): Interaction => ({
  id: "scr_m_s_44597c89094b", machine_id: "m", session_id: "s", kind: "screen",
  screen: " Bash command\n Do you want to proceed?\n ❯ 1. Yes\n   2. No",
  dialog: {
    question: "Do you want to proceed?", rev: "44597c89094b",
    detail: "Bash command\nCount the example names in first-screen chunks\ncd ./dist && ls",
    warning: "Dangerous rm operation not ruled out — the shell may read this command differently than it was parsed",
    deadline: "Claude Code will automatically block this request in 1:44, to avoid holding up an unattended session",
    command: "cd ./dist && ls", description: "Count the example names in first-screen chunks", risk: "delete",
    deadline_action: "deny", deadline_at: new Date(Date.now() + deadlineIn * 1000).toISOString(),
    options: [{ key: "1", label: "Yes" }, { key: "2", label: "No" }],
  },
});

/** The plain words' own chunk (lazyPage) is in and drawn. */
const body = () => vi.waitFor(async () => {
  await act(async () => {});
  expect(host.querySelector("[data-screen-command]")).not.toBeNull();
}, { timeout: 3000 });

const button = (label: string) => [...host.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement;

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
  vi.useRealTimers();
});

describe("a command's permission prompt", () => {
  beforeAll(async () => {
    await import("./CommandScreenCard"); // before any test installs fake timers
  }, 60_000);

  it("says what is asked in plain words, with Claude Code's own words one tap away", async () => {
    await act(async () => root.render(<InteractionCard ix={card(104)} />));
    await body();
    expect(host.textContent).toContain(t("chat-commandask.title", { agent: "Claude" }));
    expect(host.textContent).not.toContain("Do you want to proceed?");
    expect(host.textContent).toContain("Count the example names in first-screen chunks");
    expect(host.querySelector("[data-screen-command]")?.textContent).toBe("cd ./dist && ls");
    expect(host.querySelector("[data-screen-risk]")?.textContent).toBe(t("chat-commandask.risk_delete"));
    expect(host.textContent).not.toContain("Dangerous rm operation");
    act(() => button(t("chat-commandask.show_original")).click());
    expect(host.textContent).toContain("Dangerous rm operation not ruled out");
    expect(host.textContent).toContain("will automatically block this request in 1:44");
    // No numbered Yes / No rows: Deny and Allow.
    expect(button("Yes")).toBeUndefined();
    expect(button(t("chat-interactions.deny"))).toBeTruthy();
  });

  it("counts down, then says what happens", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "Date"] });
    act(() => root.render(<InteractionCard ix={card(104)} />));
    await flush();
    const line = () => host.querySelector("[data-screen-countdown]")?.textContent;
    expect(line()).toBe(t("chat-commandask.countdown_deny", { time: "1:44" }));
    // A tick a second, each after the redraw the one before made.
    const seconds = async (n: number) => {
      for (let i = 0; i < n; i++) await act(async () => { vi.advanceTimersByTime(1000); });
    };
    await seconds(3);
    expect(line()).toBe(t("chat-commandask.countdown_deny", { time: "1:41" }));
    await seconds(101);
    expect(line()).toBe(t("chat-commandask.countdown_over_deny"));
  });

  it("answers Allow and Deny with the prompt's own options", async () => {
    act(() => root.render(<InteractionCard ix={card(104)} />));
    await body();
    act(() => button(t("chat-interactions.allow")).click());
    await flush();
    expect(calls.at(-1)?.body).toMatchObject({ option: "1" });
    act(() => root.render(<InteractionCard ix={{ ...card(104), id: "scr_m_s_other" }} />));
    act(() => button(t("chat-interactions.deny")).click());
    await flush();
    expect(calls.at(-1)?.body).toMatchObject({ option: "2" });
  });

  it("stays the terminal's question from a hub before #5265", () => {
    const old = card(104);
    old.dialog = { question: "Do you want to proceed?", rev: "r1", detail: "Bash command\nrm -rf build", options: old.dialog!.options };
    act(() => root.render(<InteractionCard ix={old} />));
    expect(host.textContent).toContain("Do you want to proceed?");
    expect(host.textContent).toContain("rm -rf build");
    expect(host.textContent).toContain("Yes");
    expect(host.querySelector("[data-screen-countdown]")).toBeNull();
  });

  it("folds a long command read without its fields, so its choices sit right under it (#6638)", () => {
    const old = card(104);
    const command = Array.from({ length: 52 }, (_, i) => `echo step ${i + 1}`).join("\n");
    old.dialog = { question: "Do you want to proceed?", rev: "r2", detail: `Check every step\n${command}`, options: old.dialog!.options };
    act(() => root.render(<InteractionCard ix={old} />));
    const fold = host.querySelector("[data-screen-detail]") as HTMLElement;
    // the web's fold (ScrollFold): six lines tall, scrolling inside, the whole text still there to scroll to and select
    expect(fold.className).toContain("max-h-[6lh]");
    expect(fold.className).toContain("overflow-y-auto");
    expect(fold.hasAttribute("data-fold-scroll")).toBe(true);
    expect(fold.textContent).toContain("echo step 52");
    // the choices come after the fold, not inside it
    const yes = [...host.querySelectorAll("button")].find((b) => b.textContent?.includes("Yes")) as HTMLButtonElement;
    expect(fold.contains(yes)).toBe(false);
    expect(fold.compareDocumentPosition(yes) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
