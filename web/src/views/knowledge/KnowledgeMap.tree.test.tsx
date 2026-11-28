// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n } from "../../i18n";
import { setToken } from "../../lib/api";
import type { TreeEntry } from "../../lib/kb-tree";
import { setRosterDefault } from "../../lib/manager-choice";
import { Toasts } from "../../ui/atoms";
import { KnowledgeMap } from "./KnowledgeMap";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void; done?: boolean };
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const json = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
const answer = async (match: (r: Request) => boolean, value: unknown, status = 200) => {
  const r = requests.find((x) => !x.done && match(x));
  if (!r) throw new Error(`no request: ${requests.filter((x) => !x.done).map((x) => `${x.init?.method ?? "GET"} ${x.url}`).join(", ")}`);
  r.done = true;
  await act(async () => r.resolve(json(value, status)));
};
const get = (path: string) => (r: Request) => (r.init?.method ?? "GET") === "GET" && r.url.endsWith(path);
const put = (path: string) => (r: Request) => r.init?.method === "PUT" && r.url.endsWith(path);
const body = (r: Request) => JSON.parse(String(r.init.body));

const p = (id: string, name: string, extra: Partial<TreeEntry> = {}): TreeEntry => ({ kind: "projects", id, name, relevance: 0, extra: "short · active", ...extra });
const items = [
  p("garden-app", "Garden app", { tags: ["home"] }),
  p("seeds", "Seeds", { parent: "garden-app", milestones: [{ id: "m1", title: "Beta out", target: "2026-12-15", state: "open" }] }),
  p("docs-site", "Docs site"),
];

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init: init ?? {}, resolve }))));
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

async function openTree() {
  act(() => root.render(<><KnowledgeMap onToggleSidebar={() => {}} /><Toasts /></>));
  const tab = [...host.querySelectorAll('[role="tab"]')].find((b) => b.textContent === "Project tree")!;
  await act(async () => (tab as HTMLElement).click());
  await answer(get("/kb/projects"), { items, tags: ["home"] });
}
const node = (id: string) => host.querySelector(`[data-node="${id}"]`) as SVGGElement;

describe("the project tree view", () => {
  it("draws sub-projects under their parent with milestone flags, and by tag", async () => {
    await openTree();
    expect(node("garden-app")).toBeTruthy();
    expect(node("seeds")).toBeTruthy();
    expect(host.querySelector('[data-node="seeds"] [data-flag="open"]')?.textContent).toBe("12-15");
    const byTag = [...host.querySelectorAll('[role="tab"]')].find((b) => b.textContent === "By tag")!;
    await act(async () => (byTag as HTMLElement).click());
    expect(host.textContent).toContain("#home");
    expect(host.textContent).toContain("No tag");
  });

  it("moves a project with Move to…, offers Undo, and refuses itself and what is below it", async () => {
    await openTree();
    await act(async () => node("garden-app").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 })));
    const move = [...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("Move to…")) as HTMLElement;
    await act(async () => move.click());
    const rows = [...document.querySelectorAll('[role="menuitem"]')] as HTMLElement[];
    const seeds = rows.find((r) => r.textContent?.includes("Seeds"))!;
    expect((seeds as HTMLButtonElement).disabled).toBe(true);
    expect(seeds.textContent).toContain("It’s under this project");
    const docs = rows.find((r) => r.textContent?.includes("Docs site"))!;
    await act(async () => docs.click());
    const sent = requests.find(put("/kb/projects/garden-app"))!;
    expect(body(sent)).toEqual({ parent: "docs-site" });
    await answer(put("/kb/projects/garden-app"), { id: "garden-app", undo: { parent: "" } });
    expect(document.body.textContent).toContain("Moved “Garden app” under “Docs site”");
    const undo = [...document.querySelectorAll("button")].find((b) => b.textContent === "Undo")!;
    await act(async () => undo.click());
    const back = requests.filter(put("/kb/projects/garden-app")).at(-1)!;
    expect(body(back)).toEqual({ parent: "" });
  });

  it("puts a project back where it was when the hub refuses the move", async () => {
    await openTree();
    await act(async () => node("docs-site").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 10, clientY: 10 })));
    await act(async () => ([...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("Move to…")) as HTMLElement).click());
    await act(async () => ([...document.querySelectorAll('[role="menuitem"]')].find((b) => b.textContent?.includes("Seeds")) as HTMLElement).click());
    await answer(put("/kb/projects/docs-site"), { error: "That parent project doesn’t exist.", code: "parent_not_found" }, 400);
    expect(document.body.textContent).toContain("Couldn’t move it");
  });

  it("opens a project's home with its parent, sub-projects, milestones and a Report placeholder", async () => {
    await openTree();
    await act(async () => node("garden-app").dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await answer(get("/kb/projects/garden-app"), { detail: { kind: "projects", id: "garden-app", path: "", log: [] }, tree: { ancestors: [], children: [], milestones: [], depth: 1 } });
    const home = host.querySelector('aside [data-project-home="garden-app"]')!;
    expect(home.textContent).toContain("Top level (no parent)");
    expect(home.textContent).toContain("Seeds");
    expect(home.textContent).toContain("No notes on this project from yesterday.");
    const add = [...home.querySelectorAll("button")].find((b) => b.textContent === "Add")!;
    await act(async () => add.click());
    const form = home.querySelector("[data-milestone-form]") as HTMLFormElement;
    const title = form.querySelector("#ms-title") as HTMLInputElement;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(title, "Launch");
      title.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => form.requestSubmit());
    expect(body(requests.find(put("/kb/projects/garden-app"))!)).toEqual({ milestone: { title: "Launch", target: "", state: "open" } });
    const report = [...home.querySelectorAll('[role="tab"]')].find((b) => b.textContent === "Report")!;
    await act(async () => (report as HTMLElement).click());
    expect(home.querySelector("[data-report-placeholder]")).toBeTruthy();
  });

  it("drags a project onto another: invalid targets dim with the reason, the drop moves it", async () => {
    await openTree();
    const zoom = Number(/(\d+)%/.exec(host.textContent ?? "")![1]) / 100;
    const spot = (id: string) => {
      const m = /translate\(([-\d.]+)px,\s*([-\d.]+)px\)/.exec(node(id).style.transform)!;
      return { clientX: (Number(m[1]) + 6) * zoom, clientY: (Number(m[2]) + 6) * zoom };
    };
    const from = spot("garden-app"), to = spot("docs-site"), below = spot("seeds");
    const fire = (target: EventTarget, type: string, at: { clientX: number; clientY: number }) => act(async () => {
      target.dispatchEvent(Object.assign(new MouseEvent(type, { bubbles: true, button: 0, ...at }), { pointerId: 1, pointerType: "mouse" }));
    });
    await fire(node("garden-app"), "pointerdown", from);
    await fire(window, "pointermove", below);
    expect(node("seeds").getAttribute("data-drop")).toBe("no");
    expect(host.textContent).toContain("It’s under this project");
    expect(host.querySelector("[data-drag-ghost]")).toBeTruthy();
    await fire(window, "pointermove", to);
    expect(node("docs-site").getAttribute("data-drop")).toBe("ok");
    await fire(window, "pointerup", to);
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    expect(body(requests.find(put("/kb/projects/garden-app"))!)).toEqual({ parent: "docs-site" });
  });
});
