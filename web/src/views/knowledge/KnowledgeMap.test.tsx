// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import type { KBDetailResponse, KBList } from "../../lib/kb";
import { setRosterDefault } from "../../lib/manager-choice";
import { KnowledgeTopicsSwitch } from "../settings/KnowledgeTopics";
import { EntryDetail } from "./KBDetail";
import { KnowledgeMap } from "./KnowledgeMap";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const reply = async (request: Request, value: unknown, status = 200) => act(async () => {
  request.resolve(new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } }));
});
const pending = (path: string) => requests.find((r) => r.url.endsWith(path) && !(r as { done?: boolean }).done)!;
const answer = async (path: string, value: unknown) => {
  const r = pending(path);
  (r as { done?: boolean }).done = true;
  await reply(r, value);
};

const topic: KBDetailResponse = {
  detail: {
    kind: "topics", id: "search", path: "", meta: { name: "Search", note: "How search works", points: 3 },
    links: [{ kind: "projects", id: "alpha", name: "Project Alpha", relevance: 0 }],
    points: [
      { id: "p1", kind: "decision", text: "Results are grouped by entry", why: "Lines alone could not be found again", sources: [{ kind: "projects", id: "alpha" }], when: "2026-11-02" },
      { id: "p2", path: ["Cache"], kind: "pitfall", text: "Files changed within 2 seconds are not cached", why: "Same-timestamp rewrites", sources: [{ kind: "chats", id: "unknown-chat" }] },
      { id: "p3", path: ["Cache", "Limits"], kind: "fact", text: "The cache holds at most 64 MB" },
    ],
  },
};

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  // jsdom lays nothing out and has no element scrolling; the map's first fit only scrolls.
  Element.prototype.scrollTo = vi.fn() as unknown as Element["scrollTo"];
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

describe("a topic's page", () => {
  it("shows its points grouped by subtopic, with kind, reason, sources by name and day", async () => {
    act(() => root.render(<EntryDetail kind="topics" id="search" reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={() => {}} />));
    await reply(requests.at(-1)!, topic);
    const text = host.textContent ?? "";
    expect(text).toContain("How search works");
    expect(text).toContain("3 points");
    const sections = [...host.querySelectorAll("section")].map((s) => s.textContent ?? "");
    const loose = sections.find((s) => s.startsWith("Knowledge points"))!;
    expect(loose).toContain("Decision");
    expect(loose).toContain("Results are grouped by entry");
    expect(loose).toContain("Why: Lines alone could not be found again");
    expect(loose).toContain("Project Alpha");
    const cache = sections.find((s) => s.startsWith("Cache"))!;
    expect(cache).toContain("Pitfall");
    expect(cache).toContain("Cause: Same-timestamp rewrites");
    expect(cache).toContain("Limits");
    expect(cache).toContain("The cache holds at most 64 MB");
    // a source the detail does not name is left out rather than shown as an id
    expect(text).not.toContain("unknown-chat");
    // the summary rendered from the points is not shown a second time
    expect(sections.some((s) => s.startsWith("Profile"))).toBe(false);
    const link = [...host.querySelectorAll("a")].find((a) => a.textContent === "Project Alpha")!;
    expect(link.getAttribute("href")).toContain("/kb/projects/alpha");
  });
});

describe("the knowledge map", () => {
  const empty: KBList = { items: [] };

  it("says curation fills it in when there are no topics yet", async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", empty);
    await answer("/api/v1/assistant", { kb_topics: true });
    expect(host.textContent).toContain("No topics yet");
    expect(host.textContent).not.toContain("Open Settings");
  });

  it("says where to turn curation on when it is off", async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", empty);
    await answer("/api/v1/assistant", { kb_topics: false });
    expect(host.textContent).toContain("Knowledge points are off");
    expect(host.textContent).toContain("Open Settings");
  });

  it("draws the topics and opens a point in the panel", async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", { items: [{ kind: "topics", id: "search", name: "Search", relevance: 0, extra: "3" }] });
    await answer("/api/v1/assistant", {});
    await answer("/api/v1/kb/topics/search", topic);
    const svg = host.querySelector("svg[data-map]")!;
    expect(svg.getAttribute("aria-label")).toBe("Mind map: Knowledge");
    const nodes = [...svg.querySelectorAll<SVGGElement>("g[role=button][tabindex]")];
    const decision = nodes.find((g) => g.getAttribute("aria-label") === "Decision: Results are grouped by entry")!;
    act(() => decision.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    const panel = host.querySelector("aside")!.textContent ?? "";
    expect(panel).toContain("Results are grouped by entry");
    expect(panel).toContain("Lines alone could not be found again");
    expect(panel).toContain("Project Alpha");
    // the subtopic opens with its toggle, which says what it does
    const toggle = [...svg.querySelectorAll("g[role=button]")].find((g) => g.getAttribute("aria-label") === "Expand Cache")!;
    act(() => toggle.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect([...svg.querySelectorAll("g[role=button]")].some((g) => g.getAttribute("aria-label") === "Collapse Cache")).toBe(true);
  });

  it("wraps a point's English text between words and its Chinese text between any two characters", async () => {
    const english = "Files changed within two seconds of each other are not cached until the next scan";
    const chinese = "缓存在两秒内被改写的文件不会被缓存因为时间戳相同需要等到下一次扫描才会更新到索引里面"; // i18n-ignore: a point's text as curation writes it
    const words: KBDetailResponse = { detail: { kind: "topics", id: "cache", path: "", meta: { name: "Cache" }, points: [{ id: "e", kind: "fact", text: english }, { id: "c", kind: "fact", text: chinese }] } };
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", { items: [{ kind: "topics", id: "cache", name: "Cache", relevance: 0 }] });
    await answer("/api/v1/assistant", {});
    await answer("/api/v1/kb/topics/cache", words);
    // the card's lines, after its kind tag
    const lines = (text: string) => [...host.querySelector(`svg[data-map] g[aria-label="Fact: ${text}"]`)!.querySelectorAll("text")].slice(1).map((t) => t.textContent!);
    const en = lines(english);
    expect(en.length).toBeGreaterThan(1);
    expect(en.join(" ")).toBe(english); // every break falls between two words
    const zh = lines(chinese);
    expect(zh.length).toBeGreaterThan(1);
    expect(zh.join("")).toBe(chinese);
    expect(zh[0].length).toBeGreaterThan(en[0].length); // Chinese fills its lines to the edge, as before
  });

  it("switches to the projects view", async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", empty);
    await answer("/api/v1/assistant", {});
    const tab = [...host.querySelectorAll<HTMLButtonElement>("[role=tab]")].find((b) => b.textContent === "Projects")!;
    act(() => tab.click());
    await answer("/api/v1/kb/projects", { items: [{ kind: "projects", id: "alpha", name: "Project Alpha", relevance: 0, importance: "P1", extra: "short · active" }] });
    await answer("/api/v1/kb/projects/alpha", { detail: { kind: "projects", id: "alpha", path: "", meta: { name: "Project Alpha", goal: "Ship it" } } });
    expect(host.querySelector("svg[data-map]")?.getAttribute("aria-label")).toBe("Mind map: Projects");
    expect(tab.getAttribute("aria-selected")).toBe("true");
  });
});

describe("zooming and panning the map", () => {
  const draw = async () => {
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", { items: [{ kind: "topics", id: "search", name: "Search", relevance: 0, extra: "3" }] });
    await answer("/api/v1/assistant", {});
    await answer("/api/v1/kb/topics/search", topic);
    const svg = host.querySelector<SVGSVGElement>("svg[data-map]")!;
    const view = svg.parentElement!.parentElement as HTMLDivElement;
    const [, , width, height] = svg.getAttribute("viewBox")!.split(" ").map(Number);
    return { svg, view, width, height };
  };
  const zoom = () => host.querySelector("span[aria-live=polite]")!.textContent;
  const button = (name: string) => host.querySelector<HTMLButtonElement>(`button[aria-label="${name}"]`)!;
  // jsdom lays nothing out: give the scroll view a size
  const size = (el: HTMLElement, w: number, h: number) => {
    Object.defineProperty(el, "clientWidth", { configurable: true, value: w });
    Object.defineProperty(el, "clientHeight", { configurable: true, value: h });
  };
  const wheel = (el: Element, deltaY: number) => {
    const e = new WheelEvent("wheel", { deltaY, ctrlKey: true, bubbles: true, cancelable: true });
    act(() => el.dispatchEvent(e));
    return e.defaultPrevented;
  };
  const gesture = (el: Element, type: string, scale: number) => act(() => el.dispatchEvent(Object.assign(new Event(type, { bubbles: true, cancelable: true }), { scale, clientX: 0, clientY: 0 })));
  const pointer = (el: Element, type: string, x: number, y: number) => act(() => el.dispatchEvent(new PointerEvent(type, { pointerType: "mouse", pointerId: 1, button: 0, clientX: x, clientY: y, bubbles: true, cancelable: true })));
  const panel = () => host.querySelector("aside")!.textContent ?? "";

  it("zooms by a trackpad pinch (ctrl+wheel, Safari's gestures), and a mouse notch by one step", async () => {
    const { svg } = await draw();
    expect(zoom()).toBe("65%");
    // Chrome sends a pinch as small ctrl+wheel deltas: deltaY = -100 ln(scale)
    for (let i = 0; i < 4; i++) expect(wheel(svg, (-100 * Math.log(1.5)) / 4)).toBe(true);
    expect(zoom()).toBe("98%");
    gesture(svg, "gesturestart", 1);
    gesture(svg, "gesturechange", 1 / 1.5);
    expect(zoom()).toBe("65%");
    // one notch of a mouse wheel (100 px) is one moderate step, not straight to the floor
    wheel(svg, 100);
    expect(zoom()).toBe("51%");
  });

  it("zooms out until the whole map fits, and Fit to view shows it whole", async () => {
    const { view, width, height } = await draw();
    size(view, 120, 90);
    const fit = Math.max(0.05, Math.min((120 - 24) / width, (90 - 24) / height));
    expect(fit).toBeLessThan(0.3);
    for (let i = 0; i < 20; i++) act(() => button("Zoom out").click());
    expect(zoom()).toBe(`${Math.round(fit * 100)}%`);
    act(() => button("Zoom in").click());
    expect(zoom()).toBe(`${Math.round(fit * 1.25 * 100)}%`);
    act(() => button("Fit to view").click());
    expect(zoom()).toBe(`${Math.round(fit * 100)}%`);
  });

  it("zooms with + / = / - / _ / 0 while a node has focus, leaves ⌘ / Ctrl / Alt to the page zoom, and ignores typing", async () => {
    const { svg, view, width, height } = await draw();
    size(view, 800, 600);
    const node = svg.querySelector<SVGGElement>("g[role=button][tabindex]")!;
    node.focus();
    const press = (el: Element, key: string, mods: KeyboardEventInit = {}) => {
      const e = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true, ...mods });
      act(() => el.dispatchEvent(e));
      return e.defaultPrevented;
    };
    expect(zoom()).toBe("65%");
    // the zoom bar's steps
    expect(press(node, "=")).toBe(true);
    expect(zoom()).toBe("81%");
    press(node, "+", { shiftKey: true });
    expect(zoom()).toBe("102%");
    press(node, "-");
    expect(zoom()).toBe("81%");
    press(node, "_", { shiftKey: true });
    expect(zoom()).toBe("65%");
    // with ⌘, Ctrl or Alt they are the browser's or the desktop app's page zoom: not handled, not prevented
    for (const mods of [{ metaKey: true }, { ctrlKey: true }, { altKey: true }]) for (const key of ["=", "+", "-", "_", "0"]) expect(press(node, key, mods)).toBe(false);
    expect(zoom()).toBe("65%");
    // typing in a field, inside the map's view or elsewhere on the page, zooms nothing
    for (const parent of [view, document.body]) {
      const input = parent.appendChild(document.createElement("input"));
      input.focus();
      for (const key of ["=", "+", "-", "_", "0"]) expect(press(input, key)).toBe(false);
      input.remove();
    }
    expect(zoom()).toBe("65%");
    // 0 is Fit to view
    size(view, 120, 90);
    expect(press(node, "0")).toBe(true);
    expect(zoom()).toBe(`${Math.round(Math.max(0.05, Math.min((120 - 24) / width, (90 - 24) / height)) * 100)}%`);
  });

  it("pans on a mouse drag without selecting where it started; a press that hardly moves still selects", async () => {
    const { svg, view } = await draw();
    size(view, 800, 600);
    Element.prototype.setPointerCapture = vi.fn();
    view.scrollLeft = 500;
    view.scrollTop = 400;
    const decision = [...svg.querySelectorAll("g[role=button][tabindex]")].find((g) => g.getAttribute("aria-label") === "Decision: Results are grouped by entry")!;
    pointer(decision, "pointerdown", 100, 100);
    pointer(decision, "pointermove", 160, 130);
    pointer(decision, "pointerup", 160, 130);
    act(() => decision.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect([view.scrollLeft, view.scrollTop]).toEqual([440, 370]);
    expect(panel()).not.toContain("Lines alone could not be found again");
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    pointer(decision, "pointerdown", 100, 100);
    pointer(decision, "pointermove", 102, 101);
    pointer(decision, "pointerup", 102, 101);
    act(() => decision.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect([view.scrollLeft, view.scrollTop]).toEqual([440, 370]);
    expect(panel()).toContain("Lines alone could not be found again");
  });

  it("keeps the node it opens or closes where it was on screen, by toggle, double click and arrow keys", async () => {
    // two topics, one a side; opening a subtopic of long points widens the left side, which moves the whole map in its box
    const facts = (path: string, n: number, text: string) => Array.from({ length: n }, (_, i) => ({ id: `${path}${i}`, path: [path], kind: "fact", text: `${text} ${i + 1}` }));
    const search: KBDetailResponse = { detail: { kind: "topics", id: "search", path: "", meta: { name: "Search" }, points: [...facts("Cache", 12, "Cache fact"), ...facts("Index", 12, "Index fact")] } };
    const sync: KBDetailResponse = { detail: { kind: "topics", id: "sync", path: "", meta: { name: "Sync" }, points: facts("Conflicts", 8, "A conflict between two devices is settled by the newest write, kept short") } };
    act(() => root.render(<KnowledgeMap onToggleSidebar={() => {}} />));
    await answer("/api/v1/kb/topics", { items: [{ kind: "topics", id: "search", name: "Search", relevance: 0 }, { kind: "topics", id: "sync", name: "Sync", relevance: 0 }] });
    await answer("/api/v1/assistant", {});
    await answer("/api/v1/kb/topics/search", search);
    await answer("/api/v1/kb/topics/sync", sync);
    const svg = host.querySelector<SVGSVGElement>("svg[data-map]")!;
    const view = svg.parentElement!.parentElement as HTMLDivElement;
    view.scrollLeft = 3000;
    view.scrollTop = 3000;
    const node = (label: string) => [...svg.querySelectorAll<SVGGElement>("g[role=button][tabindex]")].find((g) => g.getAttribute("aria-label")!.startsWith(label))!;
    const toggle = (name: string) => [...svg.querySelectorAll("g[role=button]")].find((g) => g.getAttribute("aria-label") === name)!;
    // where a node is in the map's box (its place in the drawing less the viewBox's start), and on screen up to where the
    // view itself sits (through the zoom, less the scroll)
    const inBox = (label: string) => {
      const [, x, y] = /translate\(\s*([-\d.e]+)px,\s*([-\d.e]+)px\)/.exec(node(label).style.transform)!.map(Number);
      const [vx, vy] = svg.getAttribute("viewBox")!.split(" ").map(Number);
      return [x - vx, y - vy];
    };
    const onScreen = (label: string) => {
      const [x, y] = inBox(label), z = Number(svg.getAttribute("width")) / Number(svg.getAttribute("viewBox")!.split(" ")[2]);
      return [x * z - view.scrollLeft, y * z - view.scrollTop];
    };
    const apart = (a: number[], b: number[]) => Math.hypot(a[0] - b[0], a[1] - b[1]);
    const check = (label: string, act_: () => void) => {
      const screen = onScreen(label), laid = inBox(label);
      act(act_);
      expect(apart(inBox(label), laid)).toBeGreaterThan(20); // the new layout moved it in the map…
      expect(apart(onScreen(label), screen)).toBeLessThan(0.5); // …and it stayed put on screen
    };
    check("Cache", () => toggle("Expand Cache").dispatchEvent(new MouseEvent("click", { bubbles: true })));
    check("Cache", () => node("Cache").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })));
    check("Conflicts", () => node("Conflicts").dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true })));
    check("Conflicts", () => node("Conflicts").dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
    expect(toggle("Expand Conflicts")).toBeTruthy();
  });

  it("brings what the side panel opens to the middle, straight there under reduced motion", async () => {
    await draw();
    const scrollTo = vi.mocked(Element.prototype.scrollTo);
    const open = () => [...host.querySelectorAll<HTMLButtonElement>("aside button")].find((b) => b.textContent?.includes("Search"))!;
    scrollTo.mockClear();
    act(() => open().click());
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ behavior: "smooth" }));
    vi.stubGlobal("matchMedia", (q: string) => ({ matches: q.includes("reduce"), addEventListener() {}, removeEventListener() {} }));
    act(() => host.querySelector<SVGGElement>("svg[data-map] g[tabindex]")!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    scrollTo.mockClear();
    act(() => open().click());
    expect(scrollTo).toHaveBeenLastCalledWith(expect.objectContaining({ behavior: "auto" }));
  });
});

describe("the knowledge points switch", () => {
  it("reads the setting as on when unset, and writes the change", async () => {
    act(() => root.render(<KnowledgeTopicsSwitch manager="assistant" />));
    await answer("/api/v1/assistant", {});
    const sw = host.querySelector<HTMLButtonElement>("[role=switch]")!;
    expect(sw.getAttribute("aria-checked")).toBe("true");
    act(() => sw.click());
    const put = requests.find((r) => r.init.method === "PUT")!;
    expect(JSON.parse(put.init.body as string)).toEqual({ kb_topics: false });
    expect(new Headers(put.init.headers).get("X-Persona-Manager")).toBe("assistant");
    await reply(put, { kb_topics: false });
    expect(sw.getAttribute("aria-checked")).toBe("false");
  });

  it("puts the switch back when the write fails", async () => {
    act(() => root.render(<KnowledgeTopicsSwitch manager="assistant" />));
    await answer("/api/v1/assistant", { kb_topics: false });
    const sw = host.querySelector<HTMLButtonElement>("[role=switch]")!;
    act(() => sw.click());
    await reply(requests.find((r) => r.init.method === "PUT")!, { error: "nope" }, 500);
    expect(sw.getAttribute("aria-checked")).toBe("false");
  });
});
