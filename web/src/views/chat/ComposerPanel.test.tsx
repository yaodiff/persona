// @vitest-environment jsdom
// The composer's toolbar as cindy's (#5324): the + opens the + / @ panel with a search box that has the keys; a typed @
// opens the same panel above the box. "Add" first (files or images, Plan mode where the session has modes), then a few of
// each resource; picking a file or session inserts an inline reference. One round button on the right: Stop
// while the agent works and nothing is typed, Send otherwise (Queue message while it works, with a second Stop).
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { onOverlayEntry } from "../../lib/back-close";
import { loadPages } from "../../lib/lazy-page";
import { getContexts } from "../../lib/links";
import { applyCompleteSessions } from "../../lib/store";
import type { Session } from "../../lib/types";
import { Composer } from "./Composer";
import type { MentionSource } from "./ComposerPanel";
import type { PlanMode } from "./PermissionPicker";
import { composerField, composerFieldText } from "./composer-test-field";
import { t } from "../../i18n";

// the session folder's first level: a test may put other entries in it
const DEFAULT_ENTRIES = [{ name: "src", dir: true }, { name: "README.md", dir: false }];
let entries = DEFAULT_ENTRIES;
const api = vi.fn(async (path: string, _init?: unknown): Promise<unknown> => {
  if (path.startsWith("/machines/m1/fs")) return { path: "/code/app", entries };
  throw new Error(`unexpected ${path}`);
});
vi.mock("../../lib/api", async (orig) => ({ ...(await orig<typeof import("../../lib/api")>()), api: (path: string, init?: unknown) => api(path, init) }));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const KEY = "m1/s1";
const source: MentionSource = { cwd: "/code/app", self: KEY, files: () => ["/code/app/web/Composer.tsx", "/code/app/lib/links.ts"] };
let host: HTMLDivElement;
let root: Root;

beforeAll(async () => {
  await loadPages();
  act(() => applyCompleteSessions([{ machine_id: "m2", session_id: "other", title: "Fix the login page", agent: "claude", cwd: "/code/site", updated_at: "2026-11-12T10:00:00Z" } as Session]));
});

// a precise pointer on a wide screen unless a test changes that (lib/viewport.ts keeps one list per query, so the
// switches are read live)
let fine = true;
let narrow = false;
beforeEach(() => {
  fine = true;
  narrow = false;
  vi.stubGlobal("matchMedia", (q: string) => ({ get matches() { return q === "(max-width: 1023px)" ? narrow : q.includes("fine") && fine; }, media: q, addEventListener() {}, removeEventListener() {} }));
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
  Element.prototype.scrollIntoView ??= () => {};
  api.mockClear();
  entries = DEFAULT_ENTRIES;
  localStorage.clear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  getContexts(KEY).length = 0;
});

const render = (props: Partial<React.ComponentProps<typeof Composer>> = {}) =>
  act(() => root.render(<Composer draftKey={KEY} placeholder="" machineId="m1" mention={source} onSend={async () => true} {...props} />));
const box = () => host.querySelector("textarea")!;
const plus = () => host.querySelector<HTMLButtonElement>("button[data-composer-plus]")!;
const panel = () => host.querySelector<HTMLElement>("[data-composer-panel]");
const search = () => host.querySelector<HTMLInputElement>("[data-composer-panel-search]");
const options = () => [...host.querySelectorAll<HTMLElement>('[data-composer-panel] [role="option"]')];
const option = (text: string) => options().find((o) => o.textContent?.includes(text))!;
const settle = () => act(async () => void vi.advanceTimersByTime(200)); // the folder's listing, read after 120 ms
const setValue = (el: HTMLInputElement | HTMLTextAreaElement, text: string) =>
  act(() => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, text);
    if (el instanceof HTMLTextAreaElement) el.setSelectionRange(text.length, text.length);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const press = (el: Element, key: string) => act(() => void el.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));
const click = (el: Element) => act(() => el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the + (#5324)", () => {
  it("is a bare round button that says what it does, with no @ or / button beside it", () => {
    render();
    expect(plus().getAttribute("aria-label")).toBe(t("chat-composer.add_more"));
    expect(plus().title).toBe(t("chat-composer.more_actions"));
    expect(plus().className).toContain("rounded-full");
    expect(plus().className).toContain("border-transparent");
    const labels = [...host.querySelectorAll("button")].map((b) => b.textContent);
    expect(labels).not.toContain("/");
    expect(host.querySelectorAll("button").length).toBe(2); // the + and Send
  });

  it("opens the panel over the box with its search box focused: Add, then a few of each resource", async () => {
    const plan: PlanMode = { on: false, toggle: vi.fn() };
    render({ planMode: plan });
    click(plus());
    await settle();
    expect(panel()?.dataset.composerPanel).toBe("plus");
    expect(plus().getAttribute("aria-expanded")).toBe("true");
    expect(document.activeElement).toBe(search());
    expect(panel()!.textContent).toContain(t("chat-composerpanel.add"));
    const texts = options().map((o) => o.textContent);
    expect(texts[0]).toBe(t("chat-composerpanel.add_files"));
    expect(texts[1]).toBe(t("lib-session-actions.mode_plan"));
    expect(texts.slice(2).join("|")).toContain("Composer.tsx");
    expect(texts.join("|")).toContain("src/");
    expect(texts.join("|")).toContain("Fix the login page");
    // Compact pointer rows keep a full touch target.
    expect(option("Composer.tsx").className).toContain("[@media(pointer:coarse)]:h-11");
  });

  it("closes on a touch outside it, not only a mouse press (#11237)", async () => {
    render();
    click(plus());
    await settle();
    expect(panel()).not.toBeNull();
    // inside it, or on the + (which toggles it), it stays
    act(() => void panel()!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" })));
    expect(panel()).not.toBeNull();
    const outside = document.createElement("div");
    document.body.append(outside);
    act(() => void outside.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" })));
    expect(panel()).toBeNull();
    outside.remove();
  });

  it("filters as the search is typed, and Backspace in an empty search or Esc closes it, the caret back in the box", async () => {
    render();
    click(plus());
    await settle();
    setValue(search()!, "links");
    expect(options().map((o) => o.textContent?.replace(/lib$/, ""))).toEqual(["links.ts"]);
    setValue(search()!, "");
    press(search()!, "Backspace");
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(box());
    click(plus());
    press(search()!, "Escape");
    expect(panel()).toBeNull();
    expect(document.activeElement).toBe(box());
  });

  it("adds a picked file at the last caret while the search has focus", async () => {
    render();
    setValue(box(), "look at please");
    act(() => {
      box().focus();
      box().setSelectionRange(7, 7);
    });
    click(plus());
    await settle();
    setValue(search()!, "Composer");
    press(search()!, "Enter");
    expect(panel()).toBeNull();
    expect(composerFieldText(host)).toBe("look at Composer.tsx please");
    expect(composerField(host).querySelector("[data-inline-mention]")?.getAttribute("title")).toBe("/code/app/web/Composer.tsx");
  });

  it("browses a folder with its disclosure and inserts a session in the prompt", async () => {
    render();
    click(plus());
    await settle();
    click(option("src/").querySelector("[data-browse-folder]")!);
    expect(search()!.value).toBe("src/");
    click(plus()); // the + toggles it shut
    expect(panel()).toBeNull();
    click(plus());
    await settle();
    click(option("Fix the login page"));
    expect(panel()).toBeNull();
    expect(composerFieldText(host)).toBe("Fix the login page ");
    expect(getContexts(KEY)).toEqual([]);
  });

  it("checks Plan mode while the session plans, and its row switches it", async () => {
    const plan: PlanMode = { on: true, toggle: vi.fn() };
    render({ planMode: plan });
    click(plus());
    const row = option(t("lib-session-actions.mode_plan"));
    expect(row.getAttribute("aria-checked")).toBe("true");
    expect(row.querySelectorAll("svg")).toHaveLength(2); // its symbol and the check
    click(row);
    expect(plan.toggle).toHaveBeenCalledOnce();
    expect(panel()).toBeNull();
  });

  it("keeps an IME's Escape in its search box from the window (ChatView's interrupt)", async () => {
    const seen = vi.fn();
    window.addEventListener("keydown", seen);
    render();
    click(plus());
    act(() => void search()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", isComposing: true, bubbles: true, cancelable: true })));
    expect(seen).not.toHaveBeenCalled();
    expect(panel()).not.toBeNull(); // the IME's Escape cancels its candidate, not the panel
    window.removeEventListener("keydown", seen);
  });

  it("closes the / palette: one panel at a time", async () => {
    render({ commands: [{ word: "/compact", desc: "Compact", text: "/compact" }] });
    act(() => box().focus());
    setValue(box(), "/");
    await settle();
    expect(host.querySelector("[data-slash-palette]")).not.toBeNull();
    click(plus());
    expect(host.querySelector("[data-slash-palette]")).toBeNull();
    expect(panel()?.dataset.composerPanel).toBe("plus");
  });

  it("goes when the box is turned off, and does not come back with it", async () => {
    render();
    click(plus());
    expect(panel()).not.toBeNull();
    render({ disabled: true });
    expect(panel()).toBeNull();
    render();
    expect(panel()).toBeNull();
  });

  it("on a touch screen alone, takes the keyboard down and leaves the search box for a tap", async () => {
    fine = false;
    render();
    act(() => box().focus());
    click(plus());
    expect(document.activeElement).not.toBe(box());
    expect(document.activeElement).not.toBe(search());
  });

  it("opens the file picker from its Add row, images only where there is no computer", async () => {
    const picker = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
    render({ machineId: undefined });
    click(plus());
    click(option(t("chat-composerpanel.add_images")));
    expect(picker).toHaveBeenCalledOnce();
    picker.mockRestore();
    // the caret back in the box, as after any other pick
    await act(async () => void (await new Promise((r) => requestAnimationFrame(r))));
    expect(document.activeElement).toBe(box());
  });
});

describe("Back with the + panel open (#11442)", () => {
  // jsdom's Back lands as a popstate on a real timer, after back-close's own 0 ms one
  beforeEach(() => void vi.useRealTimers());
  const settleBack = () => act(async () => void (await vi.waitFor(() => expect(onOverlayEntry()).toBe(false), { timeout: 1000, interval: 10 })));
  const goBack = () => act(async () => {
    const popped = new Promise((r) => addEventListener("popstate", r, { once: true }));
    history.back();
    await popped;
  });

  it("closes it on a narrow screen and stays on the page", async () => {
    narrow = true;
    const at = location.href;
    render();
    click(plus());
    expect(panel()?.dataset.composerPanel).toBe("plus");
    expect(onOverlayEntry()).toBe(true);
    await goBack();
    expect(panel()).toBeNull();
    expect(location.href).toBe(at);
    expect(onOverlayEntry()).toBe(false);
  });

  it("closed from the page (the + again, Esc), takes its entry back off, leaving no Back that does nothing", async () => {
    narrow = true;
    render();
    click(plus());
    expect(onOverlayEntry()).toBe(true);
    click(plus());
    expect(panel()).toBeNull();
    await settleBack();
    expect(onOverlayEntry()).toBe(false);
    click(plus());
    expect(onOverlayEntry()).toBe(true);
    press(search()!, "Escape");
    expect(panel()).toBeNull();
    await settleBack();
    expect(onOverlayEntry()).toBe(false);
  });

  it("adds no entry on a wide screen", async () => {
    render();
    const before = history.state;
    click(plus());
    expect(panel()).not.toBeNull();
    expect(history.state).toBe(before);
  });
});

describe("a typed @ (#5324)", () => {
  it("opens the same panel above the box, without a search box, and the keys stay in the box", async () => {
    const plan: PlanMode = { on: false, toggle: vi.fn() };
    render({ planMode: plan });
    act(() => box().focus());
    setValue(box(), "@");
    await settle();
    expect(panel()?.dataset.composerPanel).toBe("at");
    expect(search()).toBeNull();
    expect(options()[0].textContent).toBe(t("chat-composerpanel.add_files"));
    expect(panel()!.textContent).toContain(t("chat-composerpanel.search")); // the hint under the rows
    // Plan mode from the keyboard: the @ goes, the row runs
    press(box(), "ArrowDown");
    press(box(), "Enter");
    expect(plan.toggle).toHaveBeenCalledOnce();
    expect(box().value).toBe("");
  });

  it("filters by the text after @ and picks a file in its place", async () => {
    render();
    act(() => box().focus());
    setValue(box(), "see @links");
    await settle();
    expect(options().map((o) => o.textContent?.replace(/lib$/, ""))).toEqual(["links.ts"]);
    press(box(), "Enter");
    expect(composerFieldText(host)).toBe("see links.ts ");
    expect(panel()).toBeNull();
  });
});

describe("a typed query picks a resource before an Add row (#5324)", () => {
  const at = async (text: string, plan?: PlanMode) => {
    render(plan ? { planMode: plan } : {});
    act(() => box().focus());
    setValue(box(), text);
    await settle();
  };

  it("`@a` + Enter picks api.ts, not the file picker", async () => {
    entries = [{ name: "api.ts", dir: false }];
    const picker = vi.spyOn(HTMLInputElement.prototype, "click").mockImplementation(() => {});
    await at("@a");
    expect(options()[0].textContent).toContain("api.ts");
    press(box(), "Enter");
    expect(picker).not.toHaveBeenCalled();
    expect(composerFieldText(host)).toBe("api.ts ");
    picker.mockRestore();
  });

  it("`@pl` + Enter inserts a folder, and Right Arrow can browse it", async () => {
    entries = [{ name: "plans", dir: true }];
    const plan: PlanMode = { on: false, toggle: vi.fn() };
    await at("@pl", plan);
    expect(options()[0].textContent).toContain("plans/");
    press(box(), "ArrowRight");
    expect(plan.toggle).not.toHaveBeenCalled();
    expect(box().value).toBe("@plans/");
    await at("@pl", plan);
    press(box(), "Enter");
    expect(composerFieldText(host)).toBe("plans/ ");
    expect(composerField(host).querySelector("[data-inline-mention]")?.getAttribute("title")).toBe("/code/app/plans/");
  });

  it("`@plan` with nothing else matching still offers Plan mode", async () => {
    entries = [{ name: "api.ts", dir: false }];
    const plan: PlanMode = { on: false, toggle: vi.fn() };
    await at(`@${t("lib-session-actions.mode_plan").slice(0, 2)}`, plan); // "Pl", "计划", "計画"
    expect(options().map((o) => o.textContent)).toEqual([t("lib-session-actions.mode_plan")]);
    press(box(), "Enter");
    expect(plan.toggle).toHaveBeenCalledOnce();
    expect(box().value).toBe("");
  });

  it("matches an Add row by a word's start, not anywhere in it", async () => {
    entries = [];
    await at(`@${t("chat-composerpanel.add_files").slice(1, 3)}`); // inside the label's first word ("dd", "加文")
    expect(options().some((o) => o.textContent === t("chat-composerpanel.add_files"))).toBe(false);
  });
});

describe("the round button (#5324)", () => {
  const send = () => host.querySelector<HTMLButtonElement>('[data-testid="composer-send"]');
  const stops = () => host.querySelectorAll<HTMLButtonElement>('[data-testid="composer-stop"]');

  it("is Send when something is typed and the agent is idle", () => {
    localStorage.setItem(`draft:${KEY}`, "hello");
    render({ onStop: () => {} });
    expect(send()!.getAttribute("aria-label")).toBe(t("chat-composer.send"));
    expect(send()!.className).toContain("rounded-full");
    expect(stops()).toHaveLength(0);
  });

  it("is Stop while the agent works and nothing is typed: the same round shell, not red", () => {
    const onStop = vi.fn();
    render({ working: true, onStop });
    expect(send()).toBeNull();
    expect(stops()).toHaveLength(1);
    expect(stops()[0].getAttribute("aria-label")).toBe(t("chat-composer.interrupt"));
    expect(stops()[0].className).toContain("bg-ink");
    expect(stops()[0].className).not.toContain("danger");
    click(stops()[0]);
    expect(onStop).toHaveBeenCalledOnce();
  });

  it("queues what is typed while the agent works, a second Stop left of voice", () => {
    localStorage.setItem(`draft:${KEY}`, "and also this");
    render({ working: true, onStop: () => {}, action: { icon: "voice", name: "Voice", label: "voice", primary: true, onClick: () => {}, attributes: { "data-testid": "composer-voice" } } });
    expect(send()!.getAttribute("aria-label")).toBe(t("chat-composer.queue"));
    expect(send()!.title).toBe(t("chat-composer.queue_hint"));
    expect(stops()).toHaveLength(1);
    const row = send()!.parentElement!;
    const order = [...row.children].map((c) => c.getAttribute("data-testid")).filter(Boolean);
    expect(order).toEqual(["composer-stop", "composer-voice", "composer-send"]);
  });
});
