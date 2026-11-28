// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { loadPages } from "../../lib/lazy-page";
import { getContexts, removeContext } from "../../lib/links";
import { sendDraft } from "../../lib/drafts";
import { parseQueued } from "../../lib/queue";
import { restoreQueued } from "./queue-restore";
import { t } from "../../i18n";
import { skillContext, skillDraft } from "../../lib/skill-mentions";
import { readInlineDraft } from "../../lib/inline-mentions";
import { inlineReferences } from "../../lib/inline-message";
import { parseUserText } from "../../lib/turns";
import { onModelPicker } from "../../lib/model-switch";
import { onPermissionPicker } from "../../lib/permissions-command";
import { Composer, type Outgoing } from "./Composer";
import type { MentionSource } from "./ComposerPanel";
import type { ComposerCommand } from "../../lib/extension-prompts";
import { changeComposerField, composerField, composerFieldText } from "./composer-test-field";

vi.hoisted(() => {
  window.matchMedia = ((query: string) => ({ matches: query === "(any-pointer: fine)", media: query, onchange: null,
    addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
const skills = [
  { id: "a", name: "review", description: "Inspect security boundaries", source: "project · ." },
  { id: "b", name: "review", description: "Check wording", source: "user" },
];
const api = vi.fn(async (_path: string, _init?: unknown): Promise<unknown> => ({ catalog_version: 1, complete: true, skills }));
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: (path: string, init?: unknown) => api(path, init) }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let host: HTMLDivElement;
let root: Root;
const source: MentionSource = { files: () => [], skills: { manager: "helper-a", session: "chat", launcher: "codex", environment: "profile-a" } };
const onSend = vi.fn(async (_m: Outgoing) => true);
const render = (mention = source, commands?: ComposerCommand[], machineId = "computer", draftKey = "skill-test") => act(() => root.render(<Composer draftKey={draftKey} placeholder="" mention={mention} machineId={machineId} commands={commands} onSend={onSend} />));
const box = () => composerField(host);
const text = () => composerFieldText(host);
const tokens = () => [...box().querySelectorAll<HTMLElement>("[data-inline-mention]")];
const picked = () => {
  act(() => window.dispatchEvent(new Event("pagehide")));
  return inlineReferences(readInlineDraft(localStorage.getItem("draft:skill-test") ?? ""));
};
const options = () => [...host.querySelectorAll<HTMLButtonElement>('[role="option"]')];
const waitForOptions = (count: number) => vi.waitFor(async () => {
  await act(async () => {});
  expect(options()).toHaveLength(count);
});
async function type(text: string, options?: Parameters<typeof changeComposerField>[2]) {
  await act(async () => changeComposerField(box(), text, options));
}
const key = (k: string) => act(() => { box().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })); });
async function press(k: string, extra: KeyboardEventInit = {}) {
  const event = new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...extra });
  await act(async () => {
    box().dispatchEvent(event);
    await Promise.resolve();
  });
  return event;
}
beforeAll(async () => {
  await loadPages();
  await loadPages(); // the slash palette registers its skill-aware second stage while the first pass loads
});
beforeEach(() => {
  Element.prototype.scrollIntoView ??= () => {};
  localStorage.clear();
  getContexts("skill-test").forEach((c) => removeContext("skill-test", c.id));
  api.mockClear();
  onSend.mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(() => { act(() => root.unmount()); host.remove(); });

it("searches name/description locally, disambiguates equal names, selects with keys and retains explicit identity on send", async () => {
  render();
  await type("@");
  await waitForOptions(3); // two skills and the existing Add files action
  expect(options()[1].textContent).not.toContain("project · .");
  key("ArrowDown");
  expect(host.querySelector("[data-picker-detail]")?.textContent).toContain("project · .");
  await type("Check @security please", { caret: "Check @security".length });
  expect(options()).toHaveLength(1);
  expect(api).toHaveBeenCalledTimes(1);
  expect(api.mock.calls[0][1]).toMatchObject({ headers: { "X-Persona-Manager": "helper-a" } });
  key("Enter");
  expect(text()).toBe("Check review please");
  expect(tokens()[0].title).toBe("project · .");
  expect(getContexts("skill-test")).toHaveLength(0);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  await vi.waitFor(() => expect(onSend).toHaveBeenCalledOnce());
  expect(onSend.mock.calls[0][0].text).toContain('kind="skill" title="review" url="" ref="a"');
  expect(parseUserText(onSend.mock.calls[0][0].text).text).toBe("Check review please");
  expect(localStorage.getItem("draft:skill-test") ?? "").toBe("");
  key("ArrowUp");
  expect(text()).toBe("Check review please");
  expect(picked()[0].attrs?.ref).toBe("a");
  key("ArrowDown");
  expect(text()).toBe("");
  expect(getContexts("skill-test")).toHaveLength(0);
});

it("sends on Ctrl+Enter with a highlighted @ skill instead of inserting its chip", async () => {
  render();
  await type("@security");
  await waitForOptions(1);
  expect(options()[0].getAttribute("aria-selected")).toBe("true");
  expect((await press("Enter", { ctrlKey: true })).defaultPrevented).toBe(true);
  expect(onSend.mock.calls.map(([message]) => message.text)).toEqual(["@security"]);
  expect(text()).toBe("");
  expect(getContexts("skill-test").filter((row) => row.kind === "skill")).toHaveLength(0);
});

it("restores a mid-sentence selection after switching conversations or remounting", async () => {
  render();
  await type("Use @wording here", { caret: "Use @wording".length });
  await waitForOptions(1);
  await act(async () => options()[0].click());
  render(source, undefined, "computer", "other-chat");
  expect(text()).toBe("");
  render();
  expect(text()).toBe("Use review here");
  expect(picked()[0].attrs?.ref).toBe("b");
  act(() => root.unmount());
  root = createRoot(host);
  render();
  expect(text()).toBe("Use review here");
  expect(tokens()).toHaveLength(1);
});

it.each(["manager", "directory", "machine", "session", "launcher", "environment"])("never offers stale rows across %s switches, ignores late responses, and allows ordinary chat on failure", async (change) => {
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
  render();
  await type("@review");
  await vi.waitFor(() => expect(resolve).toBeTypeOf("function"));
  const next = change === "directory" ? { ...source, cwd: "/example/changed" } :
    { ...source, skills: { ...source.skills!, [change]: "changed" } };
  render(next, undefined, change === "machine" ? "computer-b" : "computer");
  await waitForOptions(2);
  await act(async () => resolve({ catalog_version: 1, complete: true, skills: [{ ...skills[0], name: "stale" }] }));
  expect(host.textContent).not.toContain("stale");
  api.mockImplementationOnce(async () => { throw new Error("unavailable"); });
  key("Escape");
  await type("text");
  await type("@review");
  expect(options()).toHaveLength(0);
  expect(host.querySelector('[role="status"]')?.textContent).toBeTruthy();
  await type("Email me@example.com and literal @review");
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[0][0].text).toBe("Email me@example.com and literal @review");
});

it("retains the exact reference through queue pullback and refused sends", async () => {
  render();
  const raw = skillDraft([skillContext(skills[1])], "Queued words");
  await act(async () => restoreQueued("skill-test", { machine_id: "computer", session_id: "chat" } as import("../../lib/types").Session, parseQueued(raw)));
  expect(text()).toBe("Queued words");
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("b");
  onSend.mockImplementationOnce(async () => false);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(text()).toBe("Queued words");
  expect(getContexts("skill-test")[0].attrs?.ref).toBe("b");
});

it("uses the ordinary session catalog and retains its selection on a refused send", async () => {
  render({ ...source, skills: { ...source.skills!, agent: true } });
  await type("@security");
  await waitForOptions(1);
  expect(api.mock.calls[0][0]).toBe("/sessions/computer/chat/skills");
  key("Enter");
  await type("the change", { append: true });
  onSend.mockImplementationOnce(async () => false);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[0][0].text).toContain('ref="a"');
  expect(picked()[0].attrs?.ref).toBe("a");
  expect(text()).toBe("review the change");
});

it("keeps existing file rows reachable beside a large skill catalog and searches beyond the empty preview", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: true,
    skills: Array.from({ length: 50 }, (_, i) => ({ ...skills[0], id: `skill-${i}`, name: `review-${i}` })),
  }));
  render({ ...source, files: () => ["/example/report.ts"] });
  await type("@");
  await waitForOptions(52); // all 50 skills plus the file and Add files
  expect(options().some((o) => o.textContent?.includes("report.ts"))).toBe(true);
  expect(options().filter((o) => o.textContent?.includes("review-"))).toHaveLength(50);
  await type("@review-49");
  expect(options()).toHaveLength(1);
  expect(options()[0].textContent).toContain("review-49");
  expect(api).toHaveBeenCalledTimes(1);
});

const COMMANDS: ComposerCommand[] = [
  { word: "/alpha", desc: "First command", text: "/alpha" },
  { word: "/beta", desc: "Second command", text: "/beta" },
];
const focused = () => host.querySelector<HTMLButtonElement>('[role="option"][aria-selected="true"]');

it("keeps bare / clean and matches skills on a non-empty keyword with one catalog read", async () => {
  render(source, COMMANDS);
  expect(api).not.toHaveBeenCalled();
  await type("/");
  await waitForOptions(2);
  expect(options().map((o) => o.textContent)).toEqual(["alpha", "beta"]);
  expect(host.querySelectorAll("[data-picker-group]")).toHaveLength(0);
  expect(api).not.toHaveBeenCalled();

  await type("/e");
  await waitForOptions(3); // beta command plus both review skills
  expect(api).toHaveBeenCalledTimes(1);
  expect([...host.querySelectorAll("[data-picker-group]")].map((g) => g.textContent)).toEqual([
    t("chat-slashpalette.label"),
    t("chat-composerpanel.skills"),
  ]);
  expect(options()[0].textContent).toBe("beta");
  expect(options()[1].textContent).toContain("Inspect security boundaries");
  expect(options()[1].textContent).not.toContain("project · .");
  expect(options()[2].textContent).toContain("Check wording");
  act(() => options()[2].dispatchEvent(new MouseEvent("mousemove", { bubbles: true })));
  expect(host.querySelector("[data-picker-detail]")?.textContent).toContain("user");

  // Further keystrokes filter locally without extra network calls.
  await type("/rev");
  await waitForOptions(2);
  await type("/review");
  await waitForOptions(2);
  expect(api).toHaveBeenCalledTimes(1);
});

it("resets the highlight to the first row when moving from bare / into a keyword query", async () => {
  render(source, COMMANDS);
  await type("/");
  await waitForOptions(2);
  key("ArrowDown");
  expect(focused()?.textContent).toBe("beta");
  await type("/e");
  await waitForOptions(3);
  expect(focused()?.textContent).toBe("beta");
  // Backspacing back to bare / restores the clean command-only palette.
  await type("/");
  await waitForOptions(2);
  expect(options().map((o) => o.textContent)).toEqual(["alpha", "beta"]);
  expect(host.querySelectorAll("[data-picker-group]")).toHaveLength(0);
});

it("handles an empty skill catalog cleanly on keyword search while keeping commands usable", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: true, skills: [] }));
  render(source, COMMANDS);
  await type("/alpha");
  await waitForOptions(1);
  expect(options().map((o) => o.textContent)).toEqual(["alpha"]);
  expect(host.querySelector("[role=status]")).toBeNull();
  await type("/zzz");
  await waitForOptions(0);
  expect(host.textContent).toContain(t("chat-slashpalette.no_match_skills"));
});

it("keeps commands usable without a machine or a skill source and never invents a catalog request", async () => {
  render(source, COMMANDS, "");
  await type("/");
  await waitForOptions(2);
  expect(options().map((o) => o.textContent)).toEqual(["alpha", "beta"]);
  expect(api).not.toHaveBeenCalled();
});

it("keeps the slash palette command-only where the chat has no skill source", async () => {
  render({ files: () => [] }, COMMANDS);
  await type("/");
  await waitForOptions(2);
  expect(options().map((o) => o.textContent)).toEqual(["alpha", "beta"]);
  expect(api).not.toHaveBeenCalled();
});

it("ranks slash skills by exact, prefix, contained and description matches after every command", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: true, skills: [
    { id: "description", name: "audit", description: "Review the result", source: "project" },
    { id: "contains", name: "preview", description: "", source: "project" },
    { id: "prefix", name: "review-later", description: "", source: "project" },
    { id: "exact", name: "review", description: "", source: "project" },
  ] }));
  render(source, [...COMMANDS, { word: "/audit", desc: "Review command", text: "/audit" }]);
  await type("/review");
  await waitForOptions(5);
  expect(options().map((o) => o.textContent)).toEqual([
    "audit", // every command, even a description-tier match, precedes every skill
    expect.stringContaining("review"),
    expect.stringContaining("review-later"),
    expect.stringContaining("preview"),
    expect.stringContaining("audit"),
  ]);
});

it("replaces a slash query with the selected skill and restores it from the sent message when editing", async () => {
  render(source, COMMANDS);
  await type("/wording");
  await waitForOptions(1);
  await act(async () => options()[0].click());
  expect(text()).toBe("review ");
  expect(picked()[0]).toMatchObject({ kind: "skill", title: "review", attrs: { ref: "b" } });
  await type("this", { append: true });
  await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=composer-send]")!.click());
  const wire = onSend.mock.calls[0][0].text;
  expect(parseUserText(wire).text).toBe("review this");
  act(() => sendDraft("skill-test", { type: "replace", text: wire, edit: true }));
  expect(text()).toBe("review this");
  expect(picked()[0].attrs?.ref).toBe("b");
});

it("selects a slash skill with the keyboard and retains its identity through retry and send history", async () => {
  render(source, COMMANDS);
  await type("/security");
  await waitForOptions(1);
  key("Enter");
  expect(text()).toBe("review ");
  expect(picked()[0].attrs?.ref).toBe("a");
  await type("Check this", { append: true });
  onSend.mockImplementationOnce(async () => false);
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  const attempted = onSend.mock.calls[0][0].text;
  expect(attempted).toContain('kind="skill" title="review" url="" ref="a"');
  expect(picked()[0].attrs?.ref).toBe("a");
  await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="发送"]')!.click());
  expect(onSend.mock.calls[1][0].text).toBe(attempted);
  key("ArrowUp");
  expect(text()).toBe("review Check this");
  expect(picked()[0].attrs?.ref).toBe("a");
});

it.each([{ name: "Ctrl", modifier: { ctrlKey: true } }, { name: "Cmd", modifier: { metaKey: true } }])("sends the draft on $name+Enter instead of picking the highlighted slash skill", async ({ modifier }) => {
  render(source, COMMANDS);
  await type("/security");
  await waitForOptions(1);
  expect(focused()?.textContent).toContain("Inspect security boundaries");
  expect((await press("Enter", modifier)).defaultPrevented).toBe(true);
  expect(onSend.mock.calls.map(([message]) => message.text)).toEqual(["/security"]);
  expect(text()).toBe("");
  expect(getContexts("skill-test").filter((row) => row.kind === "skill")).toHaveLength(0);
});

it("leaves Mod+Alt+Enter and Mod+Shift+Enter for the window with a slash skill highlighted", async () => {
  render(source, COMMANDS);
  await type("/security");
  await waitForOptions(1);
  const seen: KeyboardEvent[] = [];
  const listener = (event: KeyboardEvent) => seen.push(event);
  window.addEventListener("keydown", listener);
  try {
    for (const modifier of [
      { ctrlKey: true, altKey: true }, { metaKey: true, altKey: true },
      { ctrlKey: true, shiftKey: true }, { metaKey: true, shiftKey: true },
    ]) {
      expect((await press("Enter", modifier)).defaultPrevented).toBe(false);
      expect(onSend).not.toHaveBeenCalled();
      expect(getContexts("skill-test").filter((row) => row.kind === "skill")).toHaveLength(0);
      expect(text()).toBe("/security");
      expect(focused()?.textContent).toContain("Inspect security boundaries");
    }
  } finally {
    window.removeEventListener("keydown", listener);
  }
  expect(seen).toHaveLength(4);
});

it.each([{ name: "model", key: "Enter" }, { name: "permissions", key: "Tab" }])("picks a $name skill with $key instead of running the typed special command", async ({ name, key: pressed }) => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: true, skills: [{ id: `skill-${name}`, name, description: "Skill, not the command", source: "project" }] }));
  const opened = vi.fn();
  const off = name === "model" ? onModelPicker("skill-test", opened) : onPermissionPicker("skill-test", opened);
  try {
    render(source, [{ word: `/${name}`, desc: "Built-in command", text: `/${name}` }, COMMANDS[1]]);
    await type(`/${name}`);
    await waitForOptions(2);
    key("ArrowDown");
    expect(focused()?.textContent).toContain("Skill, not the command");
    key(pressed);
    expect(opened).not.toHaveBeenCalled();
    expect(text()).toBe(`${name} `);
    expect(picked()[0].attrs?.ref).toBe(`skill-${name}`);
  } finally { off(); }
});

it("wraps arrows across the whole command and skill list", async () => {
  render(source, COMMANDS);
  await type("/e");
  await waitForOptions(3);
  expect(focused()?.textContent).toBe("beta");
  key("ArrowUp");
  expect(focused()?.textContent).toContain("Check wording");
  key("ArrowDown");
  expect(focused()?.textContent).toBe("beta");
});

it("resets a stale skill highlight when the execution environment changes", async () => {
  render(source, COMMANDS);
  await type("/e");
  await waitForOptions(3);
  key("ArrowDown");
  key("ArrowDown");
  expect(focused()?.textContent).toContain("Check wording");
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
  render({ ...source, skills: { ...source.skills!, environment: "profile-b" } }, COMMANDS);
  await vi.waitFor(() => expect(api).toHaveBeenCalledTimes(2));
  expect(focused()?.textContent).toBe("beta");
  await act(async () => resolve({ catalog_version: 1, complete: true, skills: [{ id: "new", name: "replacement", description: "New environment", source: "project" }] }));
  await waitForOptions(2);
  expect(focused()?.textContent).toBe("beta");
});

it("keeps commands and the highlighted row stable while the slash catalog loads, then keeps commands on failure", async () => {
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
  const cmds: ComposerCommand[] = [
    { word: "/echo", desc: "First command", text: "/echo" },
    { word: "/beta", desc: "Second command", text: "/beta" },
  ];
  render(source, cmds);
  await type("/e");
  await waitForOptions(2);
  await vi.waitFor(() => expect(api).toHaveBeenCalledTimes(1));
  expect(host.textContent).toContain("正在加载");
  key("ArrowDown");
  expect(focused()?.textContent).toBe("beta");
  await act(async () => resolve({ catalog_version: 1, complete: true, skills }));
  await waitForOptions(4);
  expect(focused()?.textContent).toBe("beta");
  key("ArrowDown");
  expect(focused()?.textContent).toContain("Inspect security boundaries");
  key("ArrowUp");
  expect(focused()?.textContent).toBe("beta");
  act(() => options()[0].dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
  expect(focused()?.textContent).toBe("beta"); // a shifted command under a resting pointer is not a hover either
  act(() => options()[0].dispatchEvent(new MouseEvent("mousemove", { bubbles: true })));
  expect(focused()?.textContent).toBe("echo");
  key("ArrowDown");
  expect(focused()?.textContent).toBe("beta");
  act(() => options()[2].dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
  expect(focused()?.textContent).toBe("beta"); // appearing under a resting pointer does not steal the row
  act(() => options()[2].dispatchEvent(new MouseEvent("mousemove", { bubbles: true })));
  expect(focused()?.textContent).toContain("Inspect security boundaries");
  key("ArrowUp");
  expect(focused()?.textContent).toBe("beta");

  key("Escape");
  await type("text");
  api.mockImplementationOnce(async () => { throw new Error("unavailable"); });
  await type("/e");
  await waitForOptions(2);
  await vi.waitFor(() => expect(host.textContent).toContain("技能暂不可用"));
  expect(options().map((o) => o.textContent)).toEqual(["echo", "beta"]);
});

it("keeps bare / closed when a Persona chat has no commands, opens on keyword, and restores a picked skill through queue pullback", async () => {
  render(source, []);
  await type("/");
  expect(host.querySelector("[data-slash-palette]")).toBeNull();
  expect(api).not.toHaveBeenCalled();
  await type("/wording");
  await waitForOptions(1);
  key("Tab");
  expect(picked()[0].attrs?.ref).toBe("b");
  await type("Queued words", { append: true });
  await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=composer-send]")!.click());
  const raw = onSend.mock.calls[0][0].text;
  await act(async () => restoreQueued("skill-test", { machine_id: "computer", session_id: "chat" } as import("../../lib/types").Session, parseQueued(raw)));
  expect(text()).toBe("review Queued words");
  expect(picked()[0].attrs?.ref).toBe("b");
});

it("names both commands and skills when an unavailable slash catalog has no matching rows", async () => {
  api.mockImplementationOnce(async () => { throw new Error("unavailable"); });
  render(source, []);
  await type("/zzz");
  await vi.waitFor(() => expect(host.textContent).toContain("技能暂不可用"));
  expect(options()).toHaveLength(0);
  expect(host.textContent).toContain(t("chat-slashpalette.no_match_skills"));
});

it("waits for the first skill answer before announcing an empty skills-only palette", async () => {
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise((r) => { resolve = r; }));
  render(source, []);
  await type("/review");
  await vi.waitFor(() => expect(api).toHaveBeenCalledTimes(1));
  expect(options()).toHaveLength(0);
  expect(host.textContent).not.toContain(t("chat-slashpalette.no_match_skills"));
  expect(host.querySelectorAll('[role="status"]')).toHaveLength(1);
  expect(host.textContent).toContain("正在加载");
  await act(async () => resolve({ catalog_version: 1, complete: true, skills }));
  await waitForOptions(2);
  expect(host.textContent).not.toContain(t("chat-slashpalette.no_match_skills"));
});

it("keeps bare / at 25 commands with no skills and lists 25 ranked commands plus matching skills on keyword", async () => {
  const many = Array.from({ length: 30 }, (_, i) => ({ word: `/command-${i}`, desc: "", text: `/command-${i}` }));
  render(source, many);
  await type("/");
  await waitForOptions(25);
  expect(options().map((o) => o.textContent)).toEqual(Array.from({ length: 25 }, (_, i) => `command-${i}`));
  expect(api).not.toHaveBeenCalled();

  await type("/c");
  await waitForOptions(27);
  expect(options().slice(0, 25).map((o) => o.textContent)).toEqual(Array.from({ length: 25 }, (_, i) => `command-${i}`));
  expect(api).toHaveBeenCalledTimes(1);
});

it("keeps commands but no skill rows when the slash catalog uses an unsupported version", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 2, complete: true, skills }));
  render(source, COMMANDS);
  await type("/e");
  await vi.waitFor(() => expect(host.textContent).toContain(t("chat-composerpanel.skills_error")));
  expect(options().map((row) => row.textContent)).toEqual(["beta"]);
  expect(host.textContent).not.toContain("Inspect security boundaries");
});

it("shows the partial-inventory line only for a partial slash catalog", async () => {
  api.mockImplementationOnce(async () => ({ catalog_version: 1, complete: false, skills }));
  render(source, COMMANDS);
  await type("/security");
  await waitForOptions(1);
  expect(options()[0].textContent).toContain("Inspect security boundaries");
  expect(host.textContent).toContain(t("chat-composerpanel.skills_partial"));

  key("Escape");
  await type("text");
  await type("/security");
  await waitForOptions(1);
  await vi.waitFor(() => expect(host.textContent).not.toContain(t("chat-composerpanel.skills_loading")));
  expect(options()[0].textContent).toContain("Inspect security boundaries");
  expect(host.textContent).not.toContain(t("chat-composerpanel.skills_partial"));
  expect(host.querySelector("[role=status]")).toBeNull();
});

it.each((["Enter", "Tab", "ArrowDown", "ArrowUp"] as const).flatMap((pressed) => [
  { composition: "isComposing", pressed },
  { composition: "keyCode 229", pressed },
]))("does not navigate or pick a skill with $pressed during $composition", async ({ composition, pressed }) => {
  render(source, COMMANDS);
  await type("/e");
  await waitForOptions(3);
  key("ArrowUp");
  const selected = focused()?.textContent;
  expect(selected).toContain("Check wording");
  await act(async () => {
    const event = new KeyboardEvent("keydown", { key: pressed, bubbles: true, cancelable: true, isComposing: composition === "isComposing" });
    if (composition === "keyCode 229") Object.defineProperty(event, "keyCode", { value: 229 });
    box().dispatchEvent(event);
    await Promise.resolve();
  });
  expect(focused()?.textContent).toBe(selected);
  expect(text()).toBe("/e");
  expect(getContexts("skill-test").filter((row) => row.kind === "skill")).toHaveLength(0);
});

it("renders warm results across triggers, preserves a selected identity on reorder and rechecks removed skills", async () => {
  render(source, COMMANDS);
  await type("@review");
  await waitForOptions(2);
  key("Escape");
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise(r => { resolve = r; }));
  await type("/review");
  await waitForOptions(2);
  key("ArrowDown");
  expect(focused()?.textContent).toContain("Check wording");
  await act(async () => resolve({ catalog_version: 1, complete: true, skills: [skills[1]] }));
  await waitForOptions(1);
  expect(focused()?.textContent).toContain("Check wording");
  key("Tab");
  expect(picked()[0].attrs?.ref).toBe("b");
});

it("keeps the selected local file when skills arrive after several @ edits", async () => {
  let resolve!: (value: unknown) => void;
  api.mockImplementationOnce(() => new Promise(r => { resolve = r; }));
  render({ ...source, files: () => ["review.md", "review.txt"] });
  await type("@");
  await type("@re");
  await type("@review");
  await waitForOptions(2);
  key("ArrowDown");
  await act(async () => resolve({ catalog_version: 1, complete: true, skills }));
  await waitForOptions(4);
  expect(focused()?.textContent).toContain("review.txt");
  key("Enter");
  expect(text()).toBe("review.txt ");
  expect(picked()[0].attrs?.path).toBe("review.txt");
});
