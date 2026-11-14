// @vitest-environment jsdom
// Settings › Saved Prompts (#7329): an empty page says what they are for and adds the first; the editor says a rule before
// asking the hub (a name another prompt has at once), saves, and the row shows; a row is changed, moved and deleted
// (confirmed in the row); an assistant from before saved prompts says so.
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";

const hub = vi.hoisted(() => ({
  prompts: [] as { id: string; name: string; text: string }[],
  calls: [] as { path: string; method: string; json?: unknown }[],
  missing: false,
  next: 1,
}));
vi.mock("../../lib/api", async (original) => {
  const real = await original<typeof import("../../lib/api")>();
  return {
    ...real,
    api: vi.fn(async (path: string, init?: { method?: string; json?: unknown }) => {
      const method = init?.method ?? "GET";
      hub.calls.push({ path, method, json: init?.json });
      if (hub.missing) throw new real.ApiError(404, "not found", {});
      if (method === "GET") return { prompts: hub.prompts };
      if (method === "POST") {
        const body = init!.json as { name: string; text: string };
        if (hub.prompts.some((p) => p.name.toLowerCase() === body.name.toLowerCase())) throw new real.ApiError(409, "taken on the hub", {});
        const p = { id: `sp${hub.next++}`, ...body };
        hub.prompts = [...hub.prompts, p];
        return { prompt: p };
      }
      if (method === "PUT" && path.endsWith("/order")) {
        const ids = (init!.json as { ids: string[] }).ids;
        hub.prompts = ids.map((id) => hub.prompts.find((p) => p.id === id)!);
        return { prompts: hub.prompts };
      }
      if (method === "PUT") {
        const id = decodeURIComponent(path.split("/").pop()!);
        hub.prompts = hub.prompts.map((p) => (p.id === id ? { id, ...(init!.json as { name: string; text: string }) } : p));
        return { prompt: hub.prompts.find((p) => p.id === id) };
      }
      if (method === "DELETE") {
        hub.prompts = hub.prompts.filter((p) => !path.endsWith(`/${p.id}`));
        return { ok: true };
      }
      throw new Error(`unexpected ${method} ${path}`);
    }),
  };
});
vi.mock("../../ui/atoms", async (original) => ({ ...(await original<typeof import("../../ui/atoms")>()), toast: vi.fn() }));
import { api } from "../../lib/api";
import { chooseManager, resetManagerChoice } from "../../lib/manager-choice";
import { resetSavedPrompts } from "../../lib/saved-prompts";
import { toast } from "../../ui/atoms";
import { SavedPrompts } from "./SavedPrompts";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
const flush = async () => {
  for (let i = 0; i < 4; i++) await act(async () => { await new Promise((ok) => setTimeout(ok, 0)); });
};
const open = async () => {
  act(() => root.render(<SavedPrompts />));
  await flush();
};
const byTestId = (id: string) => document.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const buttons = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("button")].filter((b) => b.textContent === label || b.getAttribute("aria-label") === label);
const button = (label: string) => buttons(label)[0];
const type = (el: HTMLElement | null, value: string) =>
  act(() => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el!.dispatchEvent(new Event("input", { bubbles: true }));
  });
const click = async (el: HTMLElement | undefined | null) => {
  expect(el).toBeTruthy();
  act(() => el!.click());
  await flush();
};
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-saved-prompt]")].map((r) => r.dataset.savedPrompt);
const nameNote = () => document.getElementById(byTestId("saved-prompt-name")!.getAttribute("aria-describedby")!)!.textContent;

beforeEach(() => {
  localStorage.clear();
  resetManagerChoice();
  hub.prompts = [];
  hub.calls.length = 0;
  hub.missing = false;
  hub.next = 1;
  vi.mocked(toast).mockClear();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
  resetSavedPrompts();
  vi.unstubAllGlobals();
});

describe("an empty page", () => {
  it("says what saved prompts are for, and adds the first one", async () => {
    await open();
    const empty = byTestId("saved-prompts-empty");
    expect(empty?.textContent).toContain(t("settings-savedprompts.empty_title"));
    expect(empty?.textContent).toContain(t("settings-savedprompts.empty_body"));
    await click(byTestId("saved-prompt-add"));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain(t("chat-savedprompts.title_new"));
    type(byTestId("saved-prompt-name"), "/review");
    expect(nameNote()).toBe(t("chat-savedprompts.name_hint", { name: "review" }));
    type(byTestId("saved-prompt-text"), "  Review this diff.\nList each finding.  ");
    await click(byTestId("saved-prompt-save"));
    expect(hub.calls.find((c) => c.method === "POST")?.json).toEqual({ name: "review", text: "Review this diff.\nList each finding." });
    expect(toast).toHaveBeenCalledWith(t("chat-savedprompts.saved", { name: "review" }), "ok");
    expect(rows()).toEqual(["review"]);
    expect(document.querySelector("[data-saved-prompt]")?.textContent).toContain("Review this diff.");
  });
});

describe("the editor", () => {
  it("says a rule before asking the hub, and a name another prompt has at once", async () => {
    hub.prompts = [{ id: "sp9", name: "review", text: "Review." }];
    await open();
    await click(byTestId("saved-prompt-add"));
    await click(byTestId("saved-prompt-save"));
    expect(hub.calls.some((c) => c.method === "POST")).toBe(false);
    expect(nameNote()).toBe(t("chat-savedprompts.name_rule", { max: 32 }));
    type(byTestId("saved-prompt-name"), "two words");
    expect(nameNote()).toBe(t("chat-savedprompts.name_rule", { max: 32 }));
    type(byTestId("saved-prompt-name"), "Review");
    expect(nameNote()).toBe(t("chat-savedprompts.name_taken", { name: "Review" }));
    expect(byTestId("saved-prompt-name")!.getAttribute("aria-invalid")).toBe("true");
    type(byTestId("saved-prompt-name"), "notes");
    type(byTestId("saved-prompt-text"), "Notes.");
    act(() => byTestId("saved-prompt-text")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", metaKey: true, bubbles: true, cancelable: true })));
    await flush();
    expect(hub.calls.filter((c) => c.method === "POST")).toHaveLength(1);
    expect(rows()).toEqual(["review", "notes"]);
  });

  it("keeps what was typed and shows the hub's words when it refuses", async () => {
    await open();
    await click(byTestId("saved-prompt-add"));
    type(byTestId("saved-prompt-name"), "fresh");
    type(byTestId("saved-prompt-text"), "Text.");
    hub.prompts = [{ id: "sp7", name: "fresh", text: "added on another device" }]; // the page has not read it yet
    await click(byTestId("saved-prompt-save"));
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toContain("taken on the hub");
    expect((byTestId("saved-prompt-name") as HTMLInputElement).value).toBe("fresh");
  });
});

describe("a saved prompt's row", () => {
  beforeEach(() => {
    hub.prompts = [{ id: "sp1", name: "review", text: "Review." }, { id: "sp2", name: "notes", text: "Notes." }];
  });

  it("changes it in its place", async () => {
    await open();
    await click(button(`${t("settings-savedprompts.edit")} /review`));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain(t("chat-savedprompts.title_edit"));
    expect((byTestId("saved-prompt-name") as HTMLInputElement).value).toBe("review");
    expect((byTestId("saved-prompt-text") as HTMLTextAreaElement).value).toBe("Review.");
    type(byTestId("saved-prompt-text"), "Review again.");
    await click(byTestId("saved-prompt-save"));
    expect(hub.calls.find((c) => c.method === "PUT")).toMatchObject({ path: "/assistant/saved-prompts/sp1", json: { name: "review", text: "Review again." } });
    expect(rows()).toEqual(["review", "notes"]);
  });

  it("moves it, the first not up and the last not down", async () => {
    await open();
    expect(button(t("settings-savedprompts.move_up", { name: "review" })).disabled).toBe(true);
    expect(button(t("settings-savedprompts.move_down", { name: "notes" })).disabled).toBe(true);
    await click(button(t("settings-savedprompts.move_down", { name: "review" })));
    expect(hub.calls.find((c) => c.path.endsWith("/order"))?.json).toEqual({ ids: ["sp2", "sp1"] });
    expect(rows()).toEqual(["notes", "review"]);
  });

  it("deletes it once confirmed in the row", async () => {
    await open();
    const row = () => document.querySelector<HTMLElement>('[data-saved-prompt="review"]')!;
    const inRow = (label: string) => [...row().querySelectorAll("button")].find((b) => b.textContent?.startsWith(label));
    await click(inRow(t("settings-savedprompts.delete")));
    expect(hub.calls.some((c) => c.method === "DELETE")).toBe(false);
    await click(inRow(t("common.cancel")));
    expect(rows()).toEqual(["review", "notes"]);
    await click(inRow(t("settings-savedprompts.delete")));
    await click([...row().querySelectorAll("button")].find((b) => b.textContent === t("settings-savedprompts.delete")));
    expect(hub.calls.find((c) => c.method === "DELETE")?.path).toBe("/assistant/saved-prompts/sp1");
    expect(rows()).toEqual(["notes"]);
  });
});

it("says so on an assistant from before saved prompts", async () => {
  hub.missing = true;
  await open();
  expect(host.textContent).toContain(t("settings-savedprompts.unsupported"));
  expect(byTestId("saved-prompt-add")).toBeNull();
});


it("keeps the selected assistant's settings rows when an earlier assistant's real POST completes", async () => {
  hub.prompts = [{ id: "sp1", name: "alpha", text: "Assistant A" }];
  await open();
  await click(byTestId("saved-prompt-add"));
  type(byTestId("saved-prompt-name"), "alpha-new");
  type(byTestId("saved-prompt-text"), "Assistant A's new prompt");
  const real = await vi.importActual<typeof import("../../lib/api")>("../../lib/api");
  let finish!: (response: Response) => void;
  const fetcher = vi.fn(() => new Promise<Response>((resolve) => { finish = resolve; }));
  vi.stubGlobal("fetch", fetcher);
  vi.mocked(api).mockImplementationOnce(real.api);
  await click(byTestId("saved-prompt-save"));
  expect(fetcher).toHaveBeenCalledTimes(1);
  const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
  expect(url).toBe("/api/v1/assistant/saved-prompts");
  expect(init.method).toBe("POST");
  expect(JSON.parse(init.body as string)).toEqual({ name: "alpha-new", text: "Assistant A's new prompt" });
  hub.prompts = [{ id: "sp9", name: "beta", text: "Assistant B" }];
  act(() => chooseManager("m-other"));
  await flush();
  expect(rows()).toEqual(["beta"]);
  const reads = hub.calls.filter((c) => c.method === "GET").length;
  act(() => finish(new Response(JSON.stringify({ prompt: { id: "sp2", name: "alpha-new", text: "Assistant A's new prompt" } }),
    { status: 200, headers: { "Content-Type": "application/json" } })));
  await flush();
  expect(rows()).toEqual(["beta"]);
  expect(hub.calls.filter((c) => c.method === "GET")).toHaveLength(reads);
});
