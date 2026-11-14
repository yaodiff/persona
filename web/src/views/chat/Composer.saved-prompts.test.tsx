// @vitest-environment jsdom
// Saved prompts in the composer's "/" list (#7329): read once a box has the focus, listed after the chat's own commands
// with "Saved" as their source, filtered as the rest are, and picking one puts its text in the box (never sends it); a
// change on another device (assistant_saved_prompts) shows in an open box. A box with no commands of its own still offers
// them.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../../i18n";

const hub = vi.hoisted(() => ({
  prompts: [] as { id: string; name: string; text: string }[],
  listeners: new Map<string, Set<(m: Record<string, unknown>) => void>>(),
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string) => {
      if (path === "/assistant/saved-prompts") return { prompts: hub.prompts };
      throw new real.ApiError(404, "not found", {});
    }),
  };
});
vi.mock("../../lib/store", async (original) => ({
  ...(await original<typeof import("../../lib/store")>()),
  onHubEvent: (type: string, l: (m: Record<string, unknown>) => void) => {
    if (!hub.listeners.has(type)) hub.listeners.set(type, new Set());
    hub.listeners.get(type)!.add(l);
    return () => void hub.listeners.get(type)?.delete(l);
  },
}));
import { resetSavedPrompts } from "../../lib/saved-prompts";
import { Composer, type Outgoing } from "./Composer";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const box = () => host.querySelector("textarea")!;
const typeInto = (text: string) =>
  act(() => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box(), text);
    box().dispatchEvent(new Event("input", { bubbles: true }));
  });
const key = (name: string) => act(() => void box().dispatchEvent(new KeyboardEvent("keydown", { key: name, bubbles: true, cancelable: true })));
const rows = () => [...host.querySelectorAll("[role=option]")].map((o) => o.textContent);
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
const saved = (name: string) => `${name}${t("chat-savedprompts.source")}`;

beforeEach(() => {
  hub.prompts = [
    { id: "sp1", name: "review", text: "Review this diff for security issues." },
    { id: "sp2", name: "standup", text: "Write today's standup from the commits." },
  ];
  hub.listeners.clear();
  vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("fine"), addEventListener() {}, removeEventListener() {} }));
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
  resetSavedPrompts();
});

it("lists them after the chat's own commands, filters them, and picking one fills the box without sending", async () => {
  const onSend = vi.fn(async (_: Outgoing) => true);
  act(() => root.render(<Composer draftKey="sp" placeholder="" onSend={onSend} />));
  act(() => box().focus());
  typeInto("/");
  await until(() => expect(rows().slice(-2)).toEqual([saved("review"), saved("standup")]));
  expect(rows().length).toBeGreaterThan(2);
  typeInto("/stand");
  expect(rows()).toEqual([saved("standup")]);
  key("Enter");
  expect(box().value).toBe("Write today's standup from the commits.");
  expect(onSend).not.toHaveBeenCalled();
});

it("shows another device's change in an open box", async () => {
  act(() => root.render(<Composer draftKey="sp" placeholder="" onSend={async () => true} />));
  act(() => box().focus());
  typeInto("/re");
  await until(() => expect(rows()).toContain(saved("review")));
  hub.prompts = [{ id: "sp1", name: "release", text: "Write the release notes." }];
  act(() => hub.listeners.get("assistant_saved_prompts")?.forEach((l) => l({ type: "assistant_saved_prompts" })));
  await until(() => expect(rows()).toContain(saved("release")));
  expect(rows()).not.toContain(saved("review"));
});

it("are offered where the chat has no commands of its own", async () => {
  act(() => root.render(<Composer draftKey="none" placeholder="" onSend={async () => true} commands={[]} />));
  typeInto("/");
  await until(() => expect(rows()).toEqual([saved("review"), saved("standup")]));
});
