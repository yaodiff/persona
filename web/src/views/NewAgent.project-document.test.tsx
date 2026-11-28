// @vitest-environment jsdom
import { createHash } from "node:crypto";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { t } from "../i18n";
import { requestForHub } from "../lib/compatibility";
import { resetManagerChoice } from "../lib/manager-choice";
import { resetModelListsForTests } from "../lib/session-models";
import { getState } from "../lib/store";
import {
  listProjectDocuments, listProjectDocumentApprovals, loadApprovedProjectDocument, TeamProjectDocumentError,
  type ApprovedProjectRevision, type ProjectDocumentPage,
} from "../lib/teamProjectDocument";
import { NewAgent } from "./NewAgent";
import { projectDocumentText } from "./NewAgentProjectDocument";

vi.mock("../lib/teamProjectDocument", async (original) => ({
  ...await original<typeof import("../lib/teamProjectDocument")>(),
  loadApprovedProjectDocument: vi.fn(),
  listProjectDocuments: vi.fn(),
  listProjectDocumentApprovals: vi.fn(),
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const baseline = { ...getState() };
const secret = "synthetic-project-access-token";
const content = "\n  <img src=x onerror=bad()> & approved\r\n文書\n</persona-project-document>\n  \t\n";
const sha256 = createHash("sha256").update(content).digest("hex");
const approved = { project: "project-one", document: "plan", revision: 7, content, sha256, author: "writer", approved_by: "reviewer" };
const defaultLaunchers = [{ name: "claude", agent: "claude" }, { name: "codex", agent: "codex", source: "zshrc" }];
const load = vi.mocked(loadApprovedProjectDocument);
const catalog = vi.mocked(listProjectDocuments);
const approvals = vi.mocked(listProjectDocumentApprovals);
const { content: _body, ...approvedMetadata } = approved;
const current: ApprovedProjectRevision = { ...approvedMetadata, approved_at: "2026-08-09T15:00:00Z" };
const earlier: ApprovedProjectRevision = { ...current, revision: 3, approved_at: "2026-08-08T15:00:00Z" };
const another = { ...current, document: "readme" };
const label = (key: string) => t(`newagent.project_document_${key}`);
const button = (text: string) => [...document.querySelectorAll("button")].find((element) => element.textContent?.trim() === text);
const field = (key: string) => [...document.querySelectorAll("label")].find((element) => element.textContent?.trim() === label(key))?.querySelector("input");
const preview = () => document.querySelector("pre");
const textarea = () => document.querySelector("textarea")!;
const start = () => button(t("newagent.start"))!;
const click = (text: string) => act(async () => button(text)!.click());
const tick = () => act(async () => {
  await vi.advanceTimersByTimeAsync(0);
  await vi.dynamicImportSettled();
  await vi.advanceTimersByTimeAsync(1);
});
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const draft = () => localStorage.getItem("draft:new-agent");
const stored = () => JSON.stringify([Object.entries(localStorage), Object.entries(sessionStorage)]);
let root: Root;
let mount: HTMLDivElement;
let mounted: boolean;
let followClaude: boolean;
let launchers: { name: string; agent?: string; source?: string }[];
let modelCatalog: () => Response | Promise<Response>;
let posts: { path: string; body: Record<string, unknown>; headers: Headers }[];

function DialogHarness() {
  const [open, setOpen] = useState(true);
  return <><button onClick={() => setOpen(false)}>Close outside dialog</button><button onClick={() => setOpen(true)}>Reopen dialog</button>
    <NewAgent open={open} onClose={() => setOpen(false)} /></>;
}

async function show(launcher = "claude") {
  localStorage.setItem("new.launcher.computer", launcher);
  await act(async () => root.render(<DialogHarness />));
  await tick();
}

async function type(element: HTMLInputElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(element, value);
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function enterIds() {
  await click(label("use"));
  await type(field("project")!, approved.project);
  await type(field("id")!, approved.document);
  await type(field("token")!, secret);
}

async function attach() {
  await enterIds();
  await click(label("load"));
  await click(label("attach"));
}

function renderExpectedSource() {
  const { content: body, ...metadata } = approved;
  return `<persona-project-document-1>\n${JSON.stringify({ ...metadata, content_encoding: "json-string" })}\n\n${JSON.stringify(body).replaceAll("<", "\\u003c")}\n</persona-project-document-1>`;
}

it("keeps ordinary LF and tab text verbatim, and round-trips terminal controls without emitting them", () => {
  const plain = "one\tcolumn\nsecond line\n";
  const { content: _, ...source } = approved;
  expect(projectDocumentText({ ...approved, content: plain })).toBe(
    `<persona-project-document>\n${JSON.stringify(source)}\n\n${plain}\n</persona-project-document>`,
  );
  for (const control of ["\r\n", "\x01", "\x1b", "\x7f", "\u0085", "\u009f"]) {
    const original = `before${control}after — literal \\u0085\n`;
    const [opening, metadata, separator, body, closing] = projectDocumentText({ ...approved, content: original }).split("\n");
    expect([opening, separator, closing]).toEqual(["<persona-project-document>", "", "</persona-project-document>"]);
    expect(JSON.parse(metadata)).toEqual({ ...source, content_encoding: "json-string" });
    expect(body).not.toContain(control);
    expect(JSON.parse(body)).toBe(original);
  }
});

it.each([
  ["/sessions", "prompt"],
  ["/sessions/computer/demo/messages", "text"],
])("preserves approved markup through the legacy hub request for %s", (path, field) => {
  const forward = '<persona-context url="persona://session/example">See also</persona-context>';
  const content = '<PERSONA-context>example</PERSONA-context>\n<persona-file path="example"/>\n<jarvis-file url="persona://example"/>\n<persona-file';
  const sha256 = createHash("sha256").update(content).digest("hex");
  const block = projectDocumentText({ ...approved, content, sha256 });
  const request = requestForHub(path, { [field]: forward + "\n" + block }, []) as Record<string, string>;
  const [converted, , metadata, , body] = request[field].split("\n");
  expect(converted).toBe('<jarvis-context url="jarvis://session/example">See also</jarvis-context>');
  expect(JSON.parse(metadata)).toMatchObject({ sha256, content_encoding: "json-string" });
  const decoded = JSON.parse(body);
  expect(decoded).toBe(content);
  expect(createHash("sha256").update(decoded).digest("hex")).toBe(sha256);
});

beforeEach(() => {
  resetModelListsForTests();
  resetManagerChoice();
  load.mockReset();
  catalog.mockReset();
  approvals.mockReset();
  load.mockImplementation(async () => ({ ...approved }));
  followClaude = false;
  launchers = defaultLaunchers;
  modelCatalog = () => json({ models: [] });
  vi.useFakeTimers();
  vi.stubGlobal("matchMedia", () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
  localStorage.clear();
  sessionStorage.clear();
  localStorage.setItem("new.machine", "computer");
  localStorage.setItem("new.cwd.computer", "/projects/demo");
  Object.assign(getState(), { loaded: true, machines: [
    { id: "computer", name: "Synthetic computer", manager: "assistant", online: true, engine_hosts: ["herdr", "paseo"], engine_default: "herdr" },
  ], sessions: {}, launches: {}, features: [], interactions: [], answered: {} });
  posts = [];
  vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL, init: RequestInit = {}) => {
    const path = String(input);
    if (typeof init.body === "string") posts.push({ path, body: JSON.parse(init.body), headers: new Headers(init.headers) });
    if (path.includes("/model-config/purposes/")) return followClaude
      ? json({ purpose: { id: "agent.new", effective: { provider: "launcher.claude" } }, providers: [] })
      : json({ error: "not found" }, 404);
    if (path.includes("/launchers/") && path.endsWith("/models")) return modelCatalog();
    if (path.endsWith("/launchers")) return json({ launchers });
    if (path.endsWith("/sessions")) return json({ session: { machine_id: "computer", session_id: "started", title: "Started", status: "idle" } });
    if (path.includes("/dirs?")) return json({ path: "/projects", entries: [] });
    return json({});
  }));
  mount = document.createElement("div");
  document.body.append(mount);
  root = createRoot(mount);
  mounted = true;
});

afterEach(async () => {
  if (mounted) await act(async () => root.unmount());
  mount.remove();
  Object.assign(getState(), baseline);
  resetManagerChoice();
  localStorage.clear();
  sessionStorage.clear();
  history.replaceState({}, "", "/");
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

it("previews plaintext, attaches an immutable source, and sends the exact body and digest on Start only", async () => {
  const returned = { ...approved };
  load.mockResolvedValue(returned);
  followClaude = true;
  await show();
  expect(button(label("use"))?.getAttribute("aria-expanded")).toBe("false");
  expect(field("token")).toBeUndefined();
  await type(textarea(), "Initial draft");
  await enterIds();
  expect(field("token")?.type).toBe("password");
  expect(stored()).not.toContain(secret);
  await click(label("load"));
  expect(load).toHaveBeenCalledExactlyOnceWith(approved.project, approved.document, secret, expect.any(AbortSignal));
  expect(field("token")?.value).toBe("");
  expect(preview()?.textContent).toBe(content);
  expect(preview()?.querySelector("img")).toBeNull();
  for (const value of [approved.author, approved.approved_by, sha256, String(approved.revision)]) {
    expect(document.querySelector("dl")?.textContent).toContain(value);
  }
  expect(draft()).toBe("Initial draft");
  expect(stored()).not.toContain(sha256);
  await act(async () => { const control = button(label("attach"))!; control.click(); control.click(); });
  expect(document.body.textContent).toContain(t("newagent.project_document_source", approved));
  expect(preview()).toBeNull();
  expect(textarea().value).toBe("Initial draft");
  returned.content = "Not the reviewed revision";
  returned.revision = 8;
  await type(textarea(), "Edited task");
  expect(draft()).toBe("Edited task");
  expect(stored()).not.toContain(sha256);
  expect(posts).toHaveLength(0);
  await act(async () => { const control = start(); control.click(); control.click(); });
  await tick();
  expect(posts.filter((item) => item.path.endsWith("/sessions"))).toHaveLength(1);
  expect(posts.find((item) => item.path.endsWith("/sessions"))?.body).toMatchObject({ launcher: "claude", host: "herdr", name_hint: "project-one/plan r7" });
  const sends = posts.filter((item) => item.path.endsWith("/messages"));
  expect(sends).toHaveLength(1);
  expect(sends[0].body.text).toBe(`Edited task\n\n${renderExpectedSource()}`);
  expect(JSON.stringify(posts.map((item) => [item.path, item.body, [...item.headers]]))).not.toContain(secret);
  expect(stored()).not.toContain(secret);
});

it("allows a document-only inline Codex start", async () => {
  await show("codex");
  expect(start().disabled).toBe(true);
  await enterIds();
  await click(label("load"));
  expect(start().disabled).toBe(true);
  await click(label("attach"));
  expect(start().disabled).toBe(false);
  await click(t("newagent.start"));
  await tick();
  expect(posts).toHaveLength(1);
  expect(posts[0].body).toMatchObject({ launcher: "codex", prompt: renderExpectedSource(), name_hint: "project-one/plan r7" });
});

it("starts with only the user's draft when a loaded preview was not attached", async () => {
  await show();
  await type(textarea(), "Plain task");
  await enterIds();
  await click(label("load"));
  await click(t("newagent.start"));
  await tick();
  expect(posts.find((item) => item.path.endsWith("/messages"))?.body.text).toBe("Plain task");
  expect(JSON.stringify(posts)).not.toContain(sha256);
});

// #17053: Orca's terminals take the document as the first message, as herdr's do.
it("starts with the document in an Orca terminal", async () => {
  Object.assign(getState(), { machines: [
    { id: "computer", name: "Synthetic computer", manager: "assistant", online: true, engine_hosts: ["herdr", "orca"], engine_default: "herdr" },
  ] });
  await show();
  await attach();
  await type(textarea(), "Plain task");
  await click("Orca");
  expect(document.body.textContent).not.toContain(label("unsupported"));
  expect(start().disabled).toBe(false);
  await click(t("newagent.start"));
  await tick();
  expect(posts.find((item) => item.path.endsWith("/sessions"))?.body).toMatchObject({ launcher: "claude", host: "orca", name_hint: "project-one/plan r7" });
  expect(posts.find((item) => item.path.endsWith("/messages"))?.body.text).toBe(`Plain task\n\n${renderExpectedSource()}`);
});

it("keeps the source visible and blocks an unsupported start until it is removed", async () => {
  await show();
  await attach();
  await type(textarea(), "Plain task");
  await click("Paseo");
  expect(button(label("use"))).toBeUndefined();
  expect(button(label("remove"))).toBeDefined();
  expect(document.body.textContent).toContain(label("unsupported"));
  expect(start().disabled).toBe(true);
  await click(label("remove"));
  expect(start().disabled).toBe(false);
  await click(t("newagent.start"));
  await tick();
  expect(posts.find((item) => item.path.endsWith("/messages"))?.body.text).toBe("Plain task");
  expect(JSON.stringify(posts)).not.toContain(sha256);
});

it("fences a double load and an abort-ignoring result after an identifier edit", async () => {
  let respond!: (value: typeof approved) => void;
  load.mockImplementationOnce(() => new Promise((resolve) => { respond = resolve; }));
  await show();
  await enterIds();
  await act(async () => { const control = button(label("load"))!; control.click(); control.click(); });
  expect(load).toHaveBeenCalledTimes(1);
  await type(field("id")!, "second-plan");
  expect(load.mock.calls[0][3].aborted).toBe(true);
  load.mockResolvedValueOnce({ ...approved, document: "second-plan", revision: 8 });
  await click(label("load"));
  await act(async () => respond(approved));
  expect(document.querySelector("dd")?.textContent).toBe("8");
  await type(field("project")!, "another-project");
  expect(preview()).toBeNull();
  expect(button(label("attach"))).toBeUndefined();
});

it.each(["cancel", "parent close", "parent unmount"])("drops all lookup state on %s", async (closing) => {
  let respond!: (value: typeof approved) => void;
  load.mockImplementationOnce(() => new Promise((resolve) => { respond = resolve; }));
  await show();
  await enterIds();
  await click(label("load"));
  if (closing === "cancel") await click(t("common.cancel"));
  if (closing === "parent close") await click("Close outside dialog");
  if (closing === "parent unmount") {
    await act(async () => root.unmount());
    mounted = false;
  }
  expect(load.mock.calls[0][3].aborted).toBe(true);
  await act(async () => respond(approved));
  expect(field("token")).toBeUndefined();
  expect(preview()).toBeNull();
  expect(posts).toHaveLength(0);
  if (mounted) {
    await click("Reopen dialog");
    await click(label("use"));
    expect(field("token")?.value).toBe("");
    expect(field("project")?.value).toBe("");
    expect(preview()).toBeNull();
  }
});

it("uses a localized load failure instead of exposing an error's raw details", async () => {
  const failure = new TeamProjectDocumentError("access");
  failure.message = secret;
  load.mockRejectedValueOnce(failure);
  await show();
  await enterIds();
  await click(label("load"));
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(label("error_access"));
  expect(document.body.textContent).not.toContain(secret);
});

it("starts ordinary Claude with the approved document after its model catalog fails", async () => {
  launchers = [{ name: "local-wrapper" }];
  let finishCatalog!: (reply: Response) => void;
  modelCatalog = () => new Promise((resolve) => { finishCatalog = resolve; });
  await show("local-wrapper");
  expect(button(label("use"))).toBeDefined();
  await act(async () => finishCatalog(json({ error: "catalog unavailable" }, 503)));
  expect(button(label("use"))).toBeDefined();
  await attach();
  await click(t("newagent.start"));
  await tick();
  expect(posts.find((item) => item.path.endsWith("/sessions"))?.body).toMatchObject({ launcher: "local-wrapper", async: true });
  expect(posts.find((item) => item.path.endsWith("/messages"))?.body.text).toBe(renderExpectedSource());
});

it.each([
  ["pi", "claude"],
  ["claude", "pi"],
  [undefined, "pi"],
])("keeps incompatible launcher and catalog families out (%s, %s)", async (agent, catalogAgent) => {
  launchers = [{ name: "local-wrapper", agent }];
  modelCatalog = () => json({ agent: catalogAgent, models: [] });
  await show("local-wrapper");
  expect(button(label("use"))).toBeUndefined();
  expect(load).not.toHaveBeenCalled();
});

const namedButton = (name: string) => [...document.querySelectorAll("button")].find(el => el.getAttribute("aria-label") === name);
const historyLabel = (row: ApprovedProjectRevision) => t("newagent.project_document_versions_of", row);
const previewLabel = (row: ApprovedProjectRevision) => t("newagent.project_document_preview_version", row);
async function enterProject() {
  await click(label("use"));
  await type(field("project")!, approved.project);
  await type(field("token")!, secret);
}

it("browses documents and approval pages without reading content, then attaches exactly the previewed version", async () => {
  catalog.mockResolvedValueOnce({ items: [current], next: current.document }).mockResolvedValueOnce({ items: [another] });
  approvals.mockResolvedValueOnce({ items: [current], next: String(current.revision) }).mockResolvedValueOnce({ items: [earlier] });
  await show("codex");
  await enterProject();
  expect(catalog).not.toHaveBeenCalled();
  await click(label("browse"));
  expect(catalog).toHaveBeenCalledWith(current.project, secret, expect.any(AbortSignal), undefined);
  expect(namedButton(previewLabel(current))).toBeDefined();
  await click(label("more"));
  expect(catalog).toHaveBeenLastCalledWith(current.project, secret, expect.any(AbortSignal), current.document);
  expect(namedButton(previewLabel(another))).toBeDefined();
  await act(async () => namedButton(historyLabel(current))!.click());
  expect(approvals).toHaveBeenCalledWith(current.project, current.document, secret, expect.any(AbortSignal), undefined);
  await click(label("older"));
  expect(approvals).toHaveBeenLastCalledWith(current.project, current.document, secret, expect.any(AbortSignal), String(current.revision));
  expect(namedButton(previewLabel(earlier))).toBeDefined();
  expect(load).not.toHaveBeenCalled();
  expect(start().disabled).toBe(true);
  const returned = { ...approved, revision: earlier.revision };
  load.mockResolvedValueOnce(returned);
  await act(async () => namedButton(previewLabel(earlier))!.click());
  expect(load).toHaveBeenCalledExactlyOnceWith(current.project, current.document, secret, expect.any(AbortSignal), earlier);
  expect(field("token")?.value).toBe("");
  expect(document.querySelector("dd")?.textContent).toBe(String(earlier.revision));
  expect(preview()?.textContent).toBe(content);
  expect(start().disabled).toBe(true);
  expect(stored()).not.toContain(sha256);
  await click(label("attach"));
  expect(posts).toHaveLength(0);
  expect(start().disabled).toBe(false);
  await click(t("newagent.start"));
  await tick();
  expect(posts[0].body.prompt).toBe(projectDocumentText(returned));
  expect(JSON.stringify(posts)).not.toContain(secret);
});

it.each(["project", "token", "collapse"])("drops a late catalog after changing the %s", async change => {
  let resolvePage!: (page: ProjectDocumentPage) => void;
  catalog.mockImplementationOnce(() => new Promise(resolve => { resolvePage = resolve; }));
  await show();
  await enterProject();
  await act(async () => { const control = button(label("browse"))!; control.click(); control.click(); });
  expect(catalog).toHaveBeenCalledTimes(1);
  if (change === "collapse") {
    await click(label("use"));
    await click(label("use"));
    expect(field("token")?.value).toBe("");
  } else {
    await type(field(change)!, "changed");
  }
  expect(catalog.mock.calls[0][2].aborted).toBe(true);
  await act(async () => resolvePage({ items: [current] }));
  expect(namedButton(historyLabel(current))).toBeUndefined();
  expect(load).not.toHaveBeenCalled();
});

it("clears lists when the service refuses a subsequent read after membership changes", async () => {
  catalog.mockResolvedValueOnce({ items: [current] });
  approvals.mockRejectedValueOnce(new TeamProjectDocumentError("access"));
  await show();
  await enterProject();
  await click(label("browse"));
  await act(async () => namedButton(historyLabel(current))!.click());
  expect(namedButton(historyLabel(current))).toBeUndefined();
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(label("error_access"));
  expect(button(label("attach"))).toBeUndefined();
});

it("keeps direct lookup available on a project service without the catalog route", async () => {
  catalog.mockRejectedValueOnce(new TeamProjectDocumentError("missing"));
  await show();
  await enterProject();
  await click(label("browse"));
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(label("catalog_unavailable"));
  await type(field("id")!, current.document);
  await click(label("load"));
  expect(load).toHaveBeenCalledExactlyOnceWith(current.project, current.document, secret, expect.any(AbortSignal));
  expect(preview()?.textContent).toBe(content);
});

it("returns to previously listed documents from empty history without another request", async () => {
  catalog.mockResolvedValueOnce({ items: [current] });
  approvals.mockResolvedValueOnce({ items: [] });
  await show();
  await enterProject();
  await click(label("browse"));
  await act(async () => namedButton(historyLabel(current))!.click());
  expect(document.body.textContent).toContain(label("no_versions"));
  await click(label("back"));
  expect(namedButton(historyLabel(current))).toBeDefined();
  expect(catalog).toHaveBeenCalledTimes(1);
  expect(approvals).toHaveBeenCalledTimes(1);
});

it("shows an empty catalog without offering a preview or another page", async () => {
  catalog.mockResolvedValueOnce({ items: [] });
  await show();
  await enterProject();
  await click(label("browse"));
  expect(document.body.textContent).toContain(label("no_documents"));
  expect(button(label("more"))).toBeUndefined();
  expect(button(label("attach"))).toBeUndefined();
});
