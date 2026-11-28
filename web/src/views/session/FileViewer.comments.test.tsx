// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { t } from "../../i18n";
import { parseCommentBlock } from "../../lib/comments";
import { sendDraft, splitQuotes, setQuotes } from "../../lib/drafts";
import { closePanel, getPanel } from "../../lib/panel";
import { preloadCommentBar } from "../chat/CommentBarOverlay";
import { Composer, type Outgoing } from "../chat/Composer";
import { QuoteStack } from "../chat/Quote";
import { SidePanel } from "../SidePanel";
import { FileViewerPanel } from "./FileViewer";

vi.hoisted(() => {
  window.matchMedia ??= ((query: string) => ({
    matches: query === "(any-pointer: fine)", media: query, addEventListener() {}, removeEventListener() {},
  })) as typeof window.matchMedia;
});

const MID = "test-machine";
const SID = "file-comment-session";
const KEY = `${MID}/${SID}`;
const CWD = "/work";
const VISIBLE_TAIL = "endOfVisibleCode";
const DISPLAYED_LINE = "x".repeat(4000 - VISIBLE_TAIL.length) + VISIBLE_TAIL;
const HIDDEN_TAIL = "hiddenSourceBeyondTheDisplayedLine";
const FILES = {
  markdown: { name: "notes.md", mime: "text/markdown", text: "# Review notes\n\nCheck the example." },
  source: { name: "example.ts", mime: "text/plain", text: `const before = 0;\n  const first = 42;\n  const second = 7;\n${DISPLAYED_LINE}${HIDDEN_TAIL}` },
  html: { name: "page.html", mime: "text/html", text: "<!doctype html>\n<h2>Rendered chart</h2>" },
  paged: { name: "paged.ts", mime: "text/plain", text: "const loaded = 1;\n" },
};
const NEXT_PAGE = "const next = 2;";
type Kind = Exclude<keyof typeof FILES, "paged">;

vi.mock("../../lib/api", async (original) => ({
  ...(await original<typeof import("../../lib/api")>()),
  api: vi.fn(async (route: string) => {
    const params = new URL(route, "https://hub.invalid").searchParams;
    const path = params.get("path");
    const file = Object.values(FILES).find(({ name }) => path === `${CWD}/${name}`);
    if (!file) throw new Error(`Unexpected request ${route}`);
    if (file === FILES.paged) {
      const size = file.text.length + NEXT_PAGE.length;
      const base = { ...file, path, size, is_dir: false };
      return params.has("offset")
        ? { ...base, text: NEXT_PAGE, truncated: false }
        : { ...base, next: file.text.length, truncated: true };
    }
    return { ...file, path, size: file.text.length, is_dir: false, truncated: false };
  }),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const sent = vi.fn(async (_message: Outgoing) => true);
const bar = () => document.querySelector<HTMLElement>("[data-comment-bar]");
const frame = () => host.querySelector<HTMLIFrameElement>("iframe");
const rect = { left: 50, top: 80, right: 150, bottom: 100, width: 100, height: 20 } as DOMRect;

beforeAll(async () => {
  await preloadCommentBar();
});

beforeEach(() => {
  sent.mockClear();
  localStorage.clear();
  setQuotes(KEY, []);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({
    left: 0, top: 0, right: 600, bottom: 600, width: 600, height: 600,
  } as DOMRect);
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => window.setTimeout(() => callback(0), 0));
  vi.stubGlobal("cancelAnimationFrame", (id: number) => window.clearTimeout(id));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  closePanel();
  document.getSelection()?.removeAllRanges();
  host.remove();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

async function show(kind: keyof typeof FILES) {
  const path = `${CWD}/${FILES[kind].name}`;
  await act(async () => root.render(
    <>
      <Composer draftKey={KEY} placeholder="" onSend={sent} />
      <FileViewerPanel key={path} path={path} cwd={CWD} mid={MID} sid={SID} onClose={() => {}} />
    </>,
  ));
}

function select(start: Node, from: number, end: Node, to: number) {
  const doc = start.ownerDocument!;
  const range = doc.createRange();
  range.setStart(start, from);
  range.setEnd(end, to);
  Object.defineProperty(range, "getClientRects", { value: () => [rect] });
  act(() => {
    doc.getSelection()!.removeAllRanges();
    doc.getSelection()!.addRange(range);
    doc.dispatchEvent(new doc.defaultView!.Event("selectionchange"));
  });
}

function textAt(el: Element, offset: number) {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const size = (node as Text).length;
    if (offset <= size) return { node, offset };
    offset -= size;
  }
  throw new Error("Missing text in source row");
}

const sourceText = (line: number) => host.querySelector(`[data-line="${line}"] [data-file-code-text]`)!;
const buttonNamed = (name: string) => [...host.querySelectorAll("button")].find((button) => button.textContent === name)!;

async function waitForBar() {
  for (let i = 0; i < 8 && !bar(); i++) await act(async () => { await new Promise((done) => setTimeout(done, 5)); });
}

async function pick(kind: Kind) {
  await show(kind);
  if (kind === "source") {
    const a = textAt(sourceText(2), 2);
    const b = textAt(sourceText(3), 14);
    select(a.node, a.offset, b.node, b.offset);
  } else if (kind === "html") {
    act(() => buttonNamed(t("session-files-fileview.mode_source")).click());
    expect(frame()).toBeNull();
    const a = textAt(sourceText(2), 4);
    const b = textAt(sourceText(2), 18);
    select(a.node, a.offset, b.node, b.offset);
  } else {
    const heading = host.querySelector("[data-testid=file-markdown] h1")!.firstChild!;
    select(heading, 0, heading, (heading as Text).length);
  }
  await waitForBar();
  expect(bar(), `Comment bar for ${kind}`).not.toBeNull();
}

async function writeNote(note: string) {
  await vi.waitFor(() => expect(document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")).not.toBeNull());
  const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
  act(() => {
    input.focus();
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(input, note);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  act(() => input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  expect(bar()).toBeNull();
}

async function send() {
  expect(sent).not.toHaveBeenCalled();
  await act(async () => host.querySelector<HTMLButtonElement>("[data-testid=composer-send]")!.click());
  expect(sent).toHaveBeenCalledTimes(1);
  return sent.mock.calls[0][0].text;
}

const QUOTED_LINES: Record<Kind, string[]> = {
  markdown: ["> Review notes", "File: /work/notes.md"],
  source: ["> const first = 42;", ">   const second", "File: /work/example.ts:2-3"],
  html: ["> Rendered chart", "File: /work/page.html:2"],
};

function expected(kind: Kind, n: number, note: string) {
  return [`Comment ${n}`, "Selected text:", ...QUOTED_LINES[kind], "", note].map((line) => `> ${line}`).join("\n");
}

describe("selection comments from the Agent's file panel", () => {
  it("reopens the selected HTML source line from a sent comment, even after switching back to Preview", async () => {
    await pick("html");
    await writeNote("Check this heading.");
    const text = await send();
    await act(async () => root.render(<><QuoteStack blocks={splitQuotes(text)!.quotes} mid={MID} sid={SID} /><SidePanel /></>));
    const jump = host.querySelector<HTMLButtonElement>('[data-comment-block="1"] button')!;
    expect(jump).not.toBeNull();
    await act(async () => jump.click());
    await vi.waitFor(() => expect(sourceText(2)).not.toBeNull());
    expect(getPanel()).toMatchObject({ kind: "file", path: "/work/page.html", mid: MID, sid: SID, line: 2, to: 2 });
    expect(sourceText(2).parentElement?.classList.contains("bg-accent-tint")).toBe(true);
    await act(async () => buttonNamed(t("session-files-fileview.mode_preview")).click());
    await vi.waitFor(() => expect(frame()).not.toBeNull());
    await act(async () => jump.click());
    await vi.waitFor(() => expect(sourceText(2)).not.toBeNull());
    expect(frame()).toBeNull();
  });

  it("restores a sent file comment's line range in the edit composer", async () => {
    await pick("source");
    await writeNote("Review these lines.");
    const text = await send();
    await act(async () => { sendDraft(KEY, { type: "replace", text }); });
    await vi.waitFor(() => expect(host.querySelector<HTMLButtonElement>("[data-comment-chip]")).not.toBeNull());
    act(() => host.querySelector<HTMLButtonElement>("[data-comment-chip]")!.click());
    await vi.waitFor(() => expect(document.querySelector<HTMLButtonElement>('[data-comment-jump="1"]')).not.toBeNull());
    act(() => document.querySelector<HTMLButtonElement>('[data-comment-jump="1"]')!.click());
    expect(getPanel()).toMatchObject({ kind: "file", path: "/work/example.ts", mid: MID, sid: SID, line: 2, to: 3 });
  });

  it.each(["markdown", "source", "html"] as const)("sends the selection and location from the %s preview", async (kind) => {
    await pick(kind);
    await writeNote("Clarify this passage.");
    expect(bar()).toBeNull();
    expect(await send()).toBe(expected(kind, 1, "Clarify this passage."));
  });

  it("focuses the floating comment bar on pressing C", async () => {
    await pick("markdown");
    const input = document.querySelector<HTMLTextAreaElement>("[data-comment-bar] textarea")!;
    expect(document.activeElement).not.toBe(input);
    act(() => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "c", bubbles: true })); });
    expect(document.activeElement).toBe(input);
  });

  it("numbers comments across files in one message and restores their locations on readback", async () => {
    const kinds = ["markdown", "source", "html"] as const;
    for (const [i, kind] of kinds.entries()) {
      await pick(kind);
      await writeNote(`Note ${i + 1}`);
    }
    const text = await send();
    expect(text).toBe(kinds.map((kind, i) => expected(kind, i + 1, `Note ${i + 1}`)).join("\n\n"));
    expect(splitQuotes(text)!.quotes.map((block) => parseCommentBlock(block)?.comment?.file)).toEqual([
      { location: "/work/notes.md" },
      { location: "/work/example.ts:2-3" },
      { location: "/work/page.html:2" },
    ]);
  });

  it("keeps rendered HTML isolated and does not offer a comment for a frame selection", async () => {
    await show("html");
    const before = frame()!;
    expect(before.getAttribute("sandbox")).not.toMatch(/allow-same-origin|allow-scripts/);
    expect(before.srcdoc).toContain("script-src 'none'");
    // jsdom does not enforce the iframe sandbox or populate srcdoc.
    const doc = before.contentDocument!;
    Object.defineProperty(doc, "URL", { value: "about:srcdoc" });
    const heading = doc.body.appendChild(doc.createTextNode("Rendered chart"));
    act(() => before.dispatchEvent(new Event("load")));
    select(heading, 0, heading, heading.length);
    await waitForBar();
    expect(bar()).toBeNull();
    act(() => buttonNamed(t("session-files-htmlpreview.scripts")).click());
    expect(bar()).toBeNull();
    expect(frame()).not.toBe(before);
    expect(frame()!.getAttribute("sandbox")).toContain("allow-scripts");
    expect(frame()!.getAttribute("sandbox")).not.toContain("allow-same-origin");
  });

  it.each(["load button", "after the loaded rows"] as const)("rejects paged source selection ending at %s", async (boundary) => {
    await show("paged");
    const load = buttonNamed(t("session-files-fileview.load_more"));
    const start = textAt(sourceText(1), 0);
    const rowsAndControls = load.parentElement!.parentElement!;
    const end = boundary === "load button" ? textAt(load, 2) : { node: rowsAndControls, offset: rowsAndControls.childNodes.length };
    select(start.node, start.offset, end.node, end.offset);
    await waitForBar();
    expect(bar()).toBeNull();
    expect(sourceText(2)).toBeNull();
    expect(sent).not.toHaveBeenCalled();

    await act(async () => load.click());
    const first = textAt(sourceText(1), 0);
    const last = textAt(sourceText(2), NEXT_PAGE.length);
    select(first.node, first.offset, last.node, last.offset);
    await waitForBar();
    expect(bar()).not.toBeNull();
    await writeNote("Check both lines.");
    expect(splitQuotes(await send())!.quotes.map(parseCommentBlock)).toMatchObject([
      { text: FILES.paged.text + NEXT_PAGE, comment: { file: { location: "/work/paged.ts:1-2" } } },
    ]);
  });

  it.each([
    { description: "one line ending inside the marker", first: 4, endpoint: "marker" },
    { description: "one line ending after the marker", first: 4, endpoint: "text wrapper" },
    { description: "two lines ending inside the marker", first: 3, endpoint: "marker" },
    { description: "two lines ending at the boundary after the truncated row", first: 3, endpoint: "row" },
  ])("sends only visible source for $description", async ({ first, endpoint }) => {
    await show("source");
    const text = sourceText(4);
    const marker = text.lastElementChild!.firstChild!;
    const start = textAt(sourceText(first), first === 4 ? DISPLAYED_LINE.length - VISIBLE_TAIL.length : 2);
    const end = endpoint === "marker" ? marker : endpoint === "row" ? text.parentElement! : text;
    const endOffset = endpoint === "marker" ? 6 : end.childNodes.length;
    select(start.node, start.offset, end, endOffset);
    await waitForBar();
    expect(bar()).not.toBeNull();
    await writeNote("Check this range.");
    const selected = first === 4 ? [VISIBLE_TAIL] : ["const second = 7;", DISPLAYED_LINE];
    const location = first === 4 ? "4" : "3-4";
    expect(await send()).toBe([
      "> Comment 1", "> Selected text:", ...selected.map((line) => `> > ${line}`),
      `> File: /work/example.ts:${location}`, "> ", "> Check this range.",
    ].join("\n"));
  });

  it("does not offer a source comment for the truncation marker alone", async () => {
    await show("source");
    const marker = sourceText(4).lastElementChild!.firstChild!;
    select(marker, 2, marker, 6);
    await waitForBar();
    expect(bar()).toBeNull();
  });

  it.each(["start", "end"] as const)("rejects a source selection with its %s inside the gutter", async (edge) => {
    await show("source");
    const gutter = host.querySelector(edge === "start" ? '[data-gutter="1"] span' : '[data-gutter="3"] span')!.firstChild!;
    const code = textAt(sourceText(2), 7);
    if (edge === "start") select(gutter, 0, code.node, code.offset);
    else select(code.node, code.offset, gutter, 1);
    await waitForBar();
    expect(bar()).toBeNull();
  });

  it.each(["row", "text"] as const)("omits the first row when selection starts after its code (%s boundary)", async (boundary) => {
    await show("source");
    const row = sourceText(1).parentElement!;
    const first = boundary === "row" ? { node: row, offset: row.childNodes.length } : textAt(sourceText(1), "const before = 0;".length);
    const last = textAt(sourceText(2), "  const first = 42;".length);
    select(first.node, first.offset, last.node, last.offset);
    await waitForBar();
    expect(bar()).not.toBeNull();
    await writeNote("Check this line.");
    expect(splitQuotes(await send())!.quotes.map(parseCommentBlock)).toMatchObject([
      { text: "  const first = 42;", comment: { file: { location: "/work/example.ts:2" } } },
    ]);
  });
});
