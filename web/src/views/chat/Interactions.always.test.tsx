// @vitest-environment jsdom
// A permission card offers "Always allow" only where it is kept (#3590): the manager's own cards say it is not
// (always_honored false) and are titled by their tool's name for people; a card without the field keeps the old rule.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
import { InteractionCard } from "./Interactions";
import { getState, reset, upsertSession } from "../../lib/store";
import { t } from "../../i18n";
import type { Interaction, Session } from "../../lib/types";
import { loadPages } from "../../lib/lazy-page";

await loadPages();

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const card = { id: "p_1", machine_id: "m", session_id: "s", kind: "permission", tool_name: "iphone_do", tool_input: { steps: [] } } as Interaction;

let host: HTMLDivElement;
let root: Root;
const labels = () => [...host.querySelectorAll<HTMLButtonElement>("[data-card] button")].map((b) => b.textContent);
const show = (ix: Interaction) => {
  Object.assign(getState(), { interactions: [ix], answered: {} });
  act(() => root.render(<InteractionCard ix={ix} />));
};

beforeEach(() => {
  upsertSession({ machine_id: "m", session_id: "s", title: "Demo", state: "needs_you" } as Session);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  reset();
});

describe("Always allow on a permission card", () => {
  it("is not offered where the manager says nothing keeps it, and the tool reads by its name", () => {
    show({ ...card, always_honored: false, tool_title: "iPhone control" });
    expect(labels()).not.toContain(t("chat-interactions.always_allow"));
    expect(labels()).toContain(t("chat-interactions.allow"));
    expect(host.textContent).not.toContain(t("chat-interactions.always_scope_tool", { tool: "iphone_do" }));
    expect(host.textContent).toContain(t("chat-interactions.permission_title", { tool: "iPhone control" }));
  });

  it("is offered where the manager keeps it, and as before where it does not say", () => {
    show({ ...card, tool_name: "Read", tool_input: { file_path: "/project/notes.txt" }, always_honored: true });
    expect(labels()).toContain(t("chat-interactions.always_allow"));
    act(() => root.render(null));
    show({ ...card, tool_name: "Bash", tool_input: { command: "git status" } });
    expect(labels()).toContain(t("chat-interactions.always_allow"));
    expect(host.textContent).toContain(t("chat-interactions.permission_title", { tool: "Bash" }));
  });
});
