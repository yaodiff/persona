// @vitest-environment jsdom
// "Save as Prompt…" (#7329): your own message's menu opens the saved prompt editor over the page, filled with the message's
// words and a name from its first ones (past one a prompt already has); a reply's menu does not offer it.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";

const hub = vi.hoisted(() => ({ prompts: [] as { id: string; name: string; text: string }[], posted: [] as unknown[] }));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, init?: { method?: string; json?: unknown }) => {
      if (path !== "/assistant/saved-prompts") throw new real.ApiError(404, "not found", {});
      if (init?.method === "POST") {
        hub.posted.push(init.json);
        return { prompt: { id: "sp2", ...(init.json as object) } };
      }
      return { prompts: hub.prompts };
    }),
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { resetSavedPrompts } from "../../lib/saved-prompts";
import { ChatActionsContext, type ChatActions } from "./chat-actions";
import { MessageActions } from "./MessageActions";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const chat: ChatActions = { draftKey: "m/s", canCompose: true, speaker: "Persona", title: "Persona", images: () => [] };
let host: HTMLDivElement;
let root: Root;
const until = async (check: () => void) => {
  for (let i = 0; i < 200; i++) {
    await act(async () => { await new Promise((ok) => setTimeout(ok, 5)); });
    try {
      return check();
    } catch {
      // not yet
    }
  }
  check();
};
const render = (isUser: boolean) =>
  act(() => root.render(
    <ChatActionsContext.Provider value={chat}>
      <MessageActions text="Review this diff for security issues." isUser={isUser} side={isUser}>
        <p data-testid="bubble">Review this diff for security issues.</p>
      </MessageActions>
    </ChatActionsContext.Provider>,
  ));
const openMenu = () =>
  act(() => void document.querySelector('[data-testid="bubble"]')!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 10, clientY: 10 })));
const items = () => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")];
const save = () => items().find((i) => i.textContent?.includes(t("chat-messageactions.save_as_prompt")));

beforeEach(() => {
  hub.prompts = [{ id: "sp1", name: "review-this-diff", text: "Old." }];
  hub.posted.length = 0;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetSavedPrompts();
});

it("opens the editor filled with your message, named from its first words", async () => {
  render(true);
  openMenu();
  expect(save()).toBeTruthy();
  act(() => save()!.click());
  const name = () => document.querySelector<HTMLInputElement>('[data-testid="saved-prompt-name"]');
  await until(() => expect(name()?.value).toBe("review-this-diff-2"));
  expect(document.querySelector("[role=dialog]")?.textContent).toContain(t("chat-savedprompts.title_from_message"));
  expect(document.querySelector<HTMLTextAreaElement>('[data-testid="saved-prompt-text"]')!.value).toBe("Review this diff for security issues.");
  act(() => document.querySelector<HTMLButtonElement>('[data-testid="saved-prompt-save"]')!.click());
  await until(() => expect(hub.posted).toEqual([{ name: "review-this-diff-2", text: "Review this diff for security issues." }]));
});

it("is not on a reply's menu", () => {
  render(false);
  openMenu();
  expect(items().length).toBeGreaterThan(0);
  expect(save()).toBeUndefined();
});
