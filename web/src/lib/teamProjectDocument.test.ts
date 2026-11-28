import { createHash } from "node:crypto";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { listProjectDocumentApprovals, listProjectDocuments, loadApprovedProjectDocument } from "./teamProjectDocument";

const digest = (content: string) => createHash("sha256").update(content, "utf8").digest("hex");
const content = "设计稿 📚\r\nCafe\u0301\n";
const doc = { project: "studio", document: "design_2", revision: 3, content, sha256: digest(content), author: "alice", approved_by: "bob" };
const read = (signal = new AbortController().signal) => loadApprovedProjectDocument("studio", "design_2", "fixture-reader", signal);
let fetcher: ReturnType<typeof vi.fn<typeof fetch>>;

beforeEach(() => {
  vi.stubGlobal("location", new URL("https://teams.example/hub/"));
  fetcher = vi.fn<typeof fetch>().mockImplementation(async () => Response.json(doc));
  vi.stubGlobal("fetch", fetcher);
});
afterEach(() => vi.unstubAllGlobals());

describe("reading an approved project revision", () => {
  it("sends only the member credential to the fixed same-origin endpoint and verifies exact UTF-8 bytes", async () => {
    const signal = new AbortController().signal;
    await expect(read(signal)).resolves.toEqual(doc);
    expect(fetcher).toHaveBeenCalledWith(new URL("https://teams.example/v1/projects/studio/documents/design_2/approved"), {
      method: "GET", headers: { Authorization: "Bearer fixture-reader", Accept: "application/json" },
      credentials: "omit", mode: "same-origin", cache: "no-store", redirect: "error", referrerPolicy: "no-referrer", signal,
    });
  });
  it.each(["http://localhost:1812", "http://127.0.0.2", "http://[::1]"])("allows loopback development at %s", async url => {
    vi.stubGlobal("location", new URL(url));
    await expect(read()).resolves.toEqual(doc);
  });
  it.each(["http://projects.example", "http://127.example.test", "file:///test"])("declines %s before contacting it", async url => {
    vi.stubGlobal("location", new URL(url));
    await expect(read()).rejects.toMatchObject({ code: "unavailable" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it.each([["bad/path", "design_2"], ["studio", "?"], ["a".repeat(101), "design_2"]])("rejects invalid identifiers", async (project, id) => {
    await expect(loadApprovedProjectDocument(project, id, "fixture-reader", new AbortController().signal)).rejects.toMatchObject({ code: "invalid" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it.each([[401,"access"],[403,"access"],[404,"missing"],[500,"unavailable"]] as const)("maps %i without surfacing the body", async (status, code) => {
    fetcher.mockResolvedValue(new Response("fixture-reader", { status }));
    await expect(read()).rejects.toMatchObject({ code, message: code });
  });
  it.each([
    { project: "other" }, { document: "other" }, { approved_by: "" }, { approved_by: undefined },
    { approved_by: "bad/id" }, { author: "" }, { revision: 0 }, { revision: 2.5 }, { revision: Number.MAX_SAFE_INTEGER + 1 },
    { content: "" }, { content: "a\0b" }, { content: "\ud800" }, { sha256: digest(content.normalize("NFC")) },
  ])("rejects a mismatched or malformed revision", async patch => {
    fetcher.mockResolvedValue(Response.json({ ...doc, ...patch }));
    await expect(read()).rejects.toMatchObject({ code: "invalid" });
  });
  it("checks the exact byte limit after UTF-8 encoding", async () => {
    const exact = "é".repeat(128 * 1024);
    for (const text of [exact, exact + "a"]) {
      fetcher.mockResolvedValue(Response.json({ ...doc, content: text, sha256: digest(text) }));
      if (text === exact) expect((await read()).content).toEqual(exact);
      else await expect(read()).rejects.toMatchObject({ code: "invalid" });
    }
  });
  it("bounds each streamed response without requiring Content-Length", async () => {
    const cancel = vi.fn();
    const body = new ReadableStream({ pull(controller) { controller.enqueue(new Uint8Array(256 * 1024)); }, cancel });
    fetcher.mockResolvedValue(new Response(body));
    await expect(read()).rejects.toMatchObject({ code: "invalid" });
    expect(cancel).toHaveBeenCalledOnce();
  });
  it("skips an already cancelled request", async () => {
    await expect(read(AbortSignal.abort("fixture-reader"))).rejects.toMatchObject({ name: "AbortError", message: "Aborted" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it("returns a safe cancellation when a request is aborted", async () => {
    const controller = new AbortController();
    fetcher.mockImplementation(async () => { controller.abort("fixture-reader"); throw Error("fixture-reader"); });
    await expect(read(controller.signal)).rejects.toMatchObject({ name: "AbortError", message: "Aborted" });
  });
  it("does not surface upstream errors or parse their text as a document", async () => {
    fetcher.mockRejectedValueOnce(Error("fixture-reader")).mockResolvedValueOnce(new Response("fixture-reader"));
    await expect(read()).rejects.toMatchObject({ code: "unavailable", message: "unavailable" });
    await expect(read()).rejects.toMatchObject({ code: "invalid", message: "invalid" });
  });
});

describe("browsing approved project documents", () => {
  const summary = {
    project: doc.project, document: doc.document, revision: doc.revision, author: doc.author,
    approved_by: doc.approved_by, sha256: doc.sha256, approved_at: "2026-05-10T12:00:00Z",
  };
  const signal = new AbortController().signal;
  const catalog = (after?: string) => listProjectDocuments("studio", "fixture-reader", signal, after);
  const history = (before?: string) => listProjectDocumentApprovals("studio", doc.document, "fixture-reader", signal, before);
  const pinned = () => loadApprovedProjectDocument("studio", doc.document, "fixture-reader", signal, summary);

  it("loads metadata only until a particular revision is requested", async () => {
    fetcher.mockResolvedValueOnce(Response.json({ items: [summary], next: doc.document }))
      .mockResolvedValueOnce(Response.json({ items: [summary], next: String(doc.revision) }))
      .mockResolvedValueOnce(Response.json(doc));
    expect(await catalog("a")).toEqual({ items: [summary], next: doc.document });
    expect(await history("5")).toEqual({ items: [summary], next: String(doc.revision) });
    expect(await pinned()).toEqual(doc);
    expect(fetcher.mock.calls.map(([url]) => String(url))).toEqual([
      "https://teams.example/v1/projects/studio/documents?after=a",
      "https://teams.example/v1/projects/studio/documents/design_2/approvals?before=5",
      "https://teams.example/v1/projects/studio/documents/design_2/revisions/3",
    ]);
  });
  it.each([
    { items: [summary, summary] }, { items: [{ ...summary, project: "other" }] },
    { items: [{ ...summary, content: doc.content }] }, { items: [{ ...summary, approved_at: "" }] },
    { items: [{ ...summary, approved_by: "" }] }, { items: [summary], next: "elsewhere" },
    { items: [], next: doc.document },
  ])("rejects inconsistent catalog pages", async page => {
    fetcher.mockResolvedValueOnce(Response.json(page));
    await expect(catalog()).rejects.toMatchObject({ code: "invalid" });
  });
  it("checks that history stays within the document and moves to older approvals", async () => {
    for (const row of [{ ...summary, document: "other" }, { ...summary, revision: 5 }]) {
      fetcher.mockResolvedValueOnce(Response.json({ items: [row] }));
      await expect(history("5")).rejects.toMatchObject({ code: "invalid" });
    }
    fetcher.mockResolvedValueOnce(Response.json({ items: [summary] }));
    await expect(catalog(doc.document)).rejects.toMatchObject({ code: "invalid" });
    fetcher.mockResolvedValueOnce(Response.json({ items: [] }));
    await expect(history("1")).resolves.toEqual({ items: [] });
  });
  it("rejects invalid cursors before sending the credential", async () => {
    await expect(catalog("../elsewhere")).rejects.toMatchObject({ code: "invalid" });
    await expect(history("1&limit=100")).rejects.toMatchObject({ code: "invalid" });
    expect(fetcher).not.toHaveBeenCalled();
  });
  it.each([
    { revision: doc.revision + 1 }, { author: "another-author" }, { approved_by: "another-approver" },
    { content: "changed", sha256: digest("changed") },
  ])("refuses a verified body that differs from the chosen approval", async patch => {
    fetcher.mockResolvedValueOnce(Response.json({ ...doc, ...patch }));
    await expect(pinned()).rejects.toMatchObject({ code: "invalid" });
  });
});
