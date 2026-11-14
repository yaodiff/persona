// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import { formatAt, formatSpan, minutesKeptUntil, type KBDetailResponse } from "../../lib/kb";
import { setRosterDefault } from "../../lib/manager-choice";
import { toast } from "../../ui/atoms";
import overviewCases from "../../../../spec/fixtures/minutes-overview/cases.json";
import { EntryDetail } from "./KBDetail";

vi.mock("../../ui/atoms", async (original) => ({ ...await original<typeof import("../../ui/atoms")>(), toast: vi.fn() }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
type Request = { url: string; init: RequestInit; resolve: (value: Response) => void };
const id = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa";
const detailPath = `/api/v1/kb/recordings/${id}`;
const minutesPath = `/api/v1/recordings/${id}/minutes`;
let requests: Request[];
let host: HTMLDivElement;
let root: Root;
const saved = vi.fn();
const scroll = vi.fn();
const clipboard = vi.fn().mockResolvedValue(undefined);
const fixture = (): KBDetailResponse => ({ detail: {
  kind: "recordings", id, path: `recordings/${id}`,
  meta: { title: "Design review recording", started_at: new Date(2026, 10, 10, 14).toISOString(), complete: true, kind: "meeting", revision: 4, minutes_state: "ready", minutes_asked: true, bookmarks: [{ at: 12, label: "Review" }] },
  summary: "A short profile.",
  segments: [{ start: 0, end: 15, text: "Review the export limit." }, { start: 20.5, end: 50, text: "Keep the cap. Add coverage." }, { start: 60, end: 80, text: "Discuss remaining questions." }],
  minutes: {
    version: 2, revision: 4, generated_at: "2026-11-10T15:00:00Z", title: "Design review", when: "2026-11-10 14:00–14:30", where: "Room 3", attendees: ["Ada", "Bo"],
    points: [{ text: "Export limit", who: "Ada", at: 12 }], decisions: [{ text: "Keep the cap", at: 20.5 }],
    actions: [{ id: "a1", who: "Bo", what: "Add coverage", by: "Friday", due_date: "2026-11-13", at: 25 }, { id: "a2", what: "Share notes", at: 40, todo_id: "todo-example" }],
    open: [{ text: "Per-entry limit?", at: 60 }], markdown: "# Design review\n\n- Add coverage",
  },
} });
const render = (recording = id) => act(() => root.render(<EntryDetail kind="recordings" id={recording} reloadKey={0} onToggleSidebar={() => {}} onBack={() => {}} onOpen={() => {}} onSaved={saved} />));
const reply = async (request: Request, body: unknown, status = 200) => act(async () => {
  request.resolve(new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } }));
});
const load = async (body = fixture()) => { render(); await reply(requests.at(-1)!, body); };
const section = () => host.querySelector<HTMLElement>("[data-recording-minutes]")!;
const button = (text: string) => [...(section()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === text)!;
const click = (text: string) => act(() => button(text).click());
const writes = () => requests.filter((r) => r.init.method === "POST");

beforeEach(async () => {
  await initI18n("en");
  localStorage.clear();
  setToken("synthetic-owner-a");
  setRosterDefault("assistant");
  vi.clearAllMocks();
  requests = [];
  vi.stubGlobal("fetch", vi.fn((url: string, init: RequestInit) => new Promise<Response>((resolve) => requests.push({ url, init, resolve }))));
  vi.stubGlobal("navigator", { clipboard: { writeText: clipboard } });
  Object.defineProperty(HTMLElement.prototype, "scrollTo", { value: scroll, configurable: true });
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  delete (HTMLElement.prototype as { scrollTo?: unknown }).scrollTo;
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("recording minutes in Knowledge detail", () => {
  it("renders the metadata and sections in wire order before the profile", async () => {
    await load();
    expect([...section().querySelectorAll("h3")].map((h) => h.textContent)).toEqual(["Design review", "Key points", "Decisions", "Action items", "Open questions"]);
    for (const text of ["2026-11-10 14:00–14:30", "Location: Room 3", "Attendees: Ada · Bo", "Ada · Export limit", "Bo · Add coverage · Friday · Due 2026-11-13", "Per-entry limit?"]) expect(section().textContent).toContain(text);
    expect(section().textContent).not.toContain("A short profile.");
    expect(host.textContent!.indexOf("Design review")).toBeLessThan(host.textContent!.indexOf("A short profile."));
    const added = [...section().querySelectorAll("li")].find((li) => li.textContent?.includes("Share notes"))!;
    expect(added.textContent).toContain("Added");
    expect(added.textContent).not.toContain("Add to to-dos");
  });

  it("says when in the app's own dates from start / end, and the hub's when only without them (#4713)", async () => {
    const body = fixture();
    const start = new Date(2026, 10, 10, 14).toISOString();
    const end = new Date(2026, 10, 10, 14, 30).toISOString();
    Object.assign(body.detail.minutes!, { start, end });
    await load(body);
    const when = section().querySelector("[data-minutes-when]")!.textContent!;
    expect(when).toBe(formatSpan(start, end));
    expect(when).toContain(formatAt(start));
    expect(when).toMatch(/14:00–14:30$/);
    expect(section().textContent).not.toContain("2026-11-10 14:00–14:30");
  });

  it("targets the spoken transcript line, scrolls only its scroller and briefly highlights it", async () => {
    await load();
    vi.useFakeTimers();
    const time = section().querySelector<HTMLButtonElement>('[aria-label="Go to transcript at 14:00:12"]')!;
    expect(time.getAttribute("aria-controls")).toBe("recording-line-0");
    const target = document.getElementById("recording-line-0")!;
    act(() => time.click());
    expect(scroll).toHaveBeenCalledOnce();
    expect(scroll.mock.contexts[0]).toBe(host.querySelector(".overflow-y-auto"));
    expect(document.activeElement).toBe(target);
    expect(target.classList.contains("bg-accent-tint")).toBe(true);
    expect(document.getElementById("recording-bookmark-12")?.classList.contains("bg-accent-tint")).toBe(false);
    act(() => vi.advanceTimersByTime(2000));
    expect(target.classList.contains("bg-accent-tint")).toBe(false);
    expect(section().querySelector('[aria-label="Go to transcript at 14:00:20"]')?.getAttribute("aria-controls")).toBe("recording-line-20.5");
  });

  it("copies the exact stored markdown", async () => {
    await load();
    await act(async () => button("Copy").click());
    expect(clipboard).toHaveBeenCalledWith(fixture().detail.minutes!.markdown);
  });

  it("adds one action with the minutes version, owner and manager, then refreshes", async () => {
    await load();
    act(() => { button("Add to to-dos").click(); button("Add to to-dos").click(); });
    expect(writes()).toHaveLength(1);
    const write = writes()[0];
    expect(write.url).toBe(`${minutesPath}/todos`);
    expect(JSON.parse(write.init.body as string)).toEqual({ version: 2, actions: ["a1"] });
    expect(new Headers(write.init.headers).get("Authorization")).toBe("Bearer synthetic-owner-a");
    expect(new Headers(write.init.headers).get("X-Persona-Manager")).toBe("assistant");
    expect(button("Regenerate").disabled).toBe(true);
    expect(button("Add all").disabled).toBe(true);
    const next = fixture(); next.detail.minutes!.actions![0].todo_id = "todo-new";
    await reply(write, { todos: [{ id: "todo-new" }], minutes: next.detail.minutes });
    expect(requests.at(-1)!.url).toBe(detailPath);
    expect(button("Add to to-dos").disabled).toBe(true);
    await reply(requests.at(-1)!, next);
    expect(button("Add to to-dos")).toBeUndefined();
    expect(button("Add all")).toBeUndefined();
    expect(saved).toHaveBeenCalledOnce();
  });

  it("adds only the remaining actions in their existing order", async () => {
    const data = fixture(); data.detail.minutes!.actions!.push({ id: "a3", what: "Review tests", at: 50 });
    await load(data); click("Add all");
    expect(JSON.parse(writes()[0].init.body as string)).toEqual({ version: 2, actions: ["a1", "a3"] });
  });

  it("shows the writing hint without minutes", async () => {
    const data = fixture(); delete data.detail.minutes; data.detail.meta!.minutes_state = "pending";
    await load(data);
    expect(section().textContent).toContain("Writing the minutes…");
    expect(button("Regenerate")).toBeUndefined();
    expect(button("Make minutes")).toBeUndefined();
  });

  it("refreshes pending minutes only while visible and stops once ready", async () => {
    vi.useFakeTimers();
    const visibility = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const data = fixture(); delete data.detail.minutes; data.detail.meta!.minutes_state = "pending";
    await load(data);
    await reply(requests.at(-1)!, data);
    const before = requests.length;
    act(() => vi.advanceTimersByTime(5000));
    expect(requests).toHaveLength(before + 1);
    await reply(requests.at(-1)!, data);
    visibility.mockReturnValue("hidden");
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    act(() => vi.advanceTimersByTime(10000));
    expect(requests).toHaveLength(before + 1);
    visibility.mockReturnValue("visible");
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    expect(requests).toHaveLength(before + 2);
    await reply(requests.at(-1)!, fixture());
    expect(section().textContent).toContain("Keep the cap");
    act(() => vi.advanceTimersByTime(10000));
    expect(requests).toHaveLength(before + 2);
  });

  it("retains older minutes while pending and shows the partial-transcript note", async () => {
    const data = fixture(); data.detail.meta!.minutes_state = "pending"; data.detail.meta!.revision = 5; data.detail.minutes!.partial = true;
    await load(data);
    expect(section().textContent).toContain("Updating…");
    expect(section().textContent).toContain("Keep the cap");
    expect(section().textContent).toContain("The end of this long transcript was not read");
    expect(button("Regenerate").disabled).toBe(true);
  });

  it.each([false, true])("regenerates failed minutes (retained: %s) with a bodyless POST", async (retained) => {
    const data = fixture(); data.detail.meta!.minutes_state = "failed"; if (!retained) delete data.detail.minutes;
    await load(data);
    expect(section().textContent).toContain("Couldn’t write the minutes");
    if (retained) expect(section().textContent).toContain("Keep the cap");
    click("Regenerate");
    expect(writes()[0].url).toBe(minutesPath);
    expect(writes()[0].init.body).toBeUndefined();
    await reply(writes()[0], { state: "pending" }, 202);
    data.detail.meta!.minutes_state = "pending";
    await reply(requests.at(-1)!, data);
    expect(section().textContent).toContain(retained ? "Updating…" : "Writing the minutes…");
  });

  it("offers Make minutes for a completed ordinary recording", async () => {
    const data = fixture(); delete data.detail.minutes; delete data.detail.meta!.minutes_state; data.detail.meta!.kind = "note";
    await load(data); click("Make minutes");
    expect(writes()[0].url).toBe(minutesPath);
    expect(writes()[0].init.body).toBeUndefined();
  });

  it("hides minutes on an incomplete non-meeting recording", async () => {
    const data = fixture(); data.detail.meta!.kind = "note"; data.detail.meta!.complete = false;
    await load(data); expect(section()).toBeNull();
  });

  it("disables regeneration while a meeting is incomplete", async () => {
    const data = fixture(); data.detail.meta!.complete = false;
    await load(data); expect(button("Regenerate").disabled).toBe(true);
  });

  it("omits optional metadata and empty sections and disables missing transcript targets", async () => {
    const data = fixture(); data.detail.segments = []; delete data.detail.meta!.bookmarks;
    const minutes = data.detail.minutes!;
    delete minutes.where; delete minutes.attendees; delete minutes.points; minutes.decisions = []; delete minutes.open;
    await load(data);
    expect([...section().querySelectorAll("h3")].map((h) => h.textContent)).toEqual(["Design review", "Action items"]);
    expect(section().textContent).not.toContain("Location:");
    expect(section().textContent).not.toContain("Attendees:");
    expect(section().querySelector<HTMLButtonElement>("[aria-label^='Go to transcript']")!.disabled).toBe(true);
  });

  it("refetches a stale version without silently resubmitting selected actions", async () => {
    await load(); click("Add to to-dos");
    await reply(writes()[0], { code: "minutes_stale", error: "Changed" }, 409);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("The minutes changed"), "error");
    expect(requests.at(-1)!.url).toBe(detailPath);
    const next = fixture(); next.detail.minutes!.version = 3; next.detail.minutes!.actions![0].what = "Review the new plan";
    await reply(requests.at(-1)!, next);
    expect(section().textContent).toContain("Review the new plan");
    expect(writes()).toHaveLength(1);
    click("Add to to-dos");
    expect(JSON.parse(writes()[1].init.body as string)).toEqual({ version: 3, actions: ["a1"] });
  });

  it("reports a rejected generation request without refreshing or erasing minutes", async () => {
    await load(); click("Regenerate");
    await reply(writes()[0], { error: "Transcript incomplete" }, 409);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Transcript incomplete"), "error");
    expect(requests).toHaveLength(2);
    expect(section().textContent).toContain("Keep the cap");
    expect(button("Regenerate").disabled).toBe(false);
  });

  it("retries only the read after a successful write and failed refresh", async () => {
    await load(); click("Add to to-dos");
    await reply(writes()[0], { todos: [] });
    await reply(requests.at(-1)!, { error: "Synthetic read failure" }, 503);
    expect(toast).toHaveBeenCalledWith(expect.stringContaining("Synthetic read failure"), "error");
    expect(button("Add to to-dos").disabled).toBe(true);
    click("Retry refresh");
    expect(requests.at(-1)!.url).toBe(detailPath);
    await reply(requests.at(-1)!, fixture());
    expect(writes()).toHaveLength(1);
    expect(button("Retry refresh")).toBeUndefined();
  });

  // #3780: past recording_days only the minutes are kept; they are read and confirmed as before, never made again.
  const trimmed = () => {
    const data = fixture();
    data.detail.meta!.trimmed_at = "2026-11-25T02:00:00Z"; delete data.detail.segments; delete data.detail.summary;
    return data;
  };
  const assistantAsk = () => requests.find((r) => r.url === "/api/v1/assistant")!;

  it("keeps a trimmed recording's minutes and to-dos, says until when, and offers no regenerate", async () => {
    const data = trimmed();
    await load(data);
    expect(new Headers(assistantAsk().init.headers).get("X-Persona-Manager")).toBe("assistant");
    expect(section().textContent).toContain(t("knowledge-kbdetail.minutes_transcript_expired"));
    await reply(assistantAsk(), { manager: "assistant", recording_days: 14, minutes_days: 365 });
    const until = minutesKeptUntil(data.detail.meta!.started_at as string, 365)!;
    expect(until).toContain("2027");
    expect(section().textContent).toContain(t("knowledge-kbdetail.minutes_kept_until", { date: until }));
    expect(section().textContent).toContain("Keep the cap");
    expect(button("Regenerate")).toBeUndefined();
    expect(button("Make minutes")).toBeUndefined();
    expect(button("Copy")).toBeDefined();
    expect(section().querySelector<HTMLButtonElement>('[aria-label="Go to transcript at 14:00:12"]')!.disabled).toBe(true);
    expect(host.textContent).toContain(t("knowledge-kbdetail.transcript_expired"));
    expect(host.textContent).not.toContain(t("knowledge-kbdetail.transcript_empty"));
    click("Add to to-dos");
    expect(writes().map((w) => w.url)).toEqual([`${minutesPath}/todos`]);
  });

  it("says minutes are kept until deleted when retention is forever", async () => {
    await load(trimmed());
    await reply(assistantAsk(), { manager: "assistant", recording_days: 14, minutes_days: 0 });
    expect(section().textContent).toContain(t("knowledge-kbdetail.minutes_kept_forever"));
    expect(section().textContent).not.toContain("1970");
    expect(button("Regenerate")).toBeUndefined();
  });

  it.each([
    ["has no minutes_days", { manager: "assistant" }, 200],
    ["cannot be read", { error: "Synthetic failure" }, 503],
  ])("says only that the transcript expired when the assistant %s", async (_, body, status) => {
    await load(trimmed());
    await reply(assistantAsk(), body, status);
    expect(section().textContent).toContain(t("knowledge-kbdetail.minutes_transcript_expired"));
    expect(toast).not.toHaveBeenCalled();
  });

  it("asks nothing more of a whole recording", async () => {
    await load();
    expect(requests.some((r) => r.url === "/api/v1/assistant")).toBe(false);
    expect(section().querySelector("[data-minutes-kept]")).toBeNull();
  });

  it.each(["route", "credential", "manager"])("ignores late writes after a %s change", async (change) => {
    await load(); click("Regenerate"); const old = writes()[0];
    act(() => {
      if (change === "route") render("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb");
      else if (change === "credential") setToken("synthetic-owner-b");
      else setRosterDefault("other-manager");
    });
    const count = requests.length;
    await reply(old, { state: "pending" }, 202);
    expect(requests).toHaveLength(count);
    expect(saved).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });
});

// #6563: minutes with an overview read as one summary card (docs/assistant/meeting-minutes.md "Summary card (#6563)").
describe("the minutes' summary card", () => {
  const bullets = (n: number) => Array.from({ length: n }, (_, i) => ({ text: `Line ${i + 1}`, at: 12 + i }));
  type Overview = { headline?: string; conclusions: object[]; groups: { title: string; topics: { title: string; icon: string; bullets: object[] }[] }[] };
  const overview = (): Overview => ({
    headline: "Export ships with the cap; the date waits on the cache fix",
    conclusions: [
      { text: "Export ships with the 4 MB cap", strong: ["4 MB"], at: 20.5 },
      { text: "The date hangs on the fix by Wednesday", strong: ["Wednesday", "missing"], at: 60 },
    ],
    groups: [
      { title: "Project progress", topics: [
        { title: "Export", icon: "progress", bullets: [
          { text: "Export limit is 4 MB, not 4 MB per file", strong: ["4 MB", "4 MB"], at: 12, who: "Ada" },
          { text: "Keep the cap", strong: ["the cap"], at: 20.5, decision: true },
        ] },
        { title: "Release date", icon: "schedule", bullets: [{ text: "The release is at risk", strong: ["at risk"], at: 60 }] },
      ] },
      { title: "Tooling", topics: [{ title: "Cache bug", icon: "rocket", bullets: [{ text: "Reproduced once", strong: ["once"], at: 60 }] }] },
    ],
  });
  const withOverview = (o: unknown = overview()) => { const data = fixture(); data.detail.minutes!.overview = o; return data; };
  const headings = () => [...section().querySelectorAll("h3, h4, h5")].map((h) => `${h.tagName} ${h.textContent}`);
  const legacy = ["H3 Design review", "H3 Key points", "H3 Decisions", "H3 Action items", "H3 Open questions"];
  const card = () => section().querySelector<HTMLElement>("[data-minutes-overview]");
  const labelOf = (b: { who: string | null; decision: boolean }) => (b.decision ? "Decision" : b.who ?? "");

  it("shows the headline, the conclusions, the topic cards by group, the action items, the open questions and the AI note, in that order", async () => {
    await load(withOverview());
    expect(headings()).toEqual([
      "H3 Export ships with the cap; the date waits on the cache fix",
      "H4 Core conclusions",
      "H4 Project progress", "H5 Export", "H5 Release date",
      "H4 Tooling", "H5 Cache bug",
      "H4 Action items", "H4 Open questions",
    ]);
    // Points and decisions are not shown again in cards of their own.
    expect(section().textContent).not.toContain("Key points");
    expect(section().textContent).not.toContain("Export limit ·");
    // When, where and who on one line under the headline.
    expect(card()!.querySelector("p")!.textContent).toBe("2026-11-10 14:00–14:30 · Room 3 · Ada · Bo");
    // A screen reader hears the separators too (nothing in the line is hidden from it), so the facts stay apart.
    const heard = (el: Element) => { const copy = el.cloneNode(true) as Element; copy.querySelectorAll("[aria-hidden]").forEach((n) => n.remove()); return copy.textContent; };
    expect(heard(card()!.querySelector("p")!)).toBe("2026-11-10 14:00–14:30 · Room 3 · Ada · Bo");
    // The bold places as the strong rule finds them (a repeated figure twice, one not found skipped).
    expect([...card()!.querySelectorAll("[data-minutes-conclusion] strong")].map((s) => s.textContent)).toEqual(["4 MB", "Wednesday"]);
    const lines = [...card()!.querySelectorAll<HTMLElement>("[data-minutes-bullet]")];
    expect(lines.map((li) => li.getAttribute("data-minutes-bullet"))).toEqual(["point", "decision", "point", "point"]);
    expect([...lines[0].querySelectorAll("strong")].map((s) => s.textContent)).toEqual(["4 MB", "4 MB"]);
    expect(lines[0].querySelector("p")!.textContent).toBe("Export limit is 4 MB, not 4 MB per file Ada");
    // A point shows its speaker, a decision the word Decision in that place.
    expect(lines[0].textContent).toContain("Ada");
    expect(lines[1].textContent).toContain("Decision");
    expect(lines[1].textContent).not.toContain("Ada");
    // Each group a section labelled by its heading, each topic an article labelled by its title; tints by position.
    const groups = [...card()!.querySelectorAll<HTMLElement>("section[aria-labelledby]")];
    expect(groups.map((g) => document.getElementById(g.getAttribute("aria-labelledby")!)!.textContent)).toEqual(["Project progress", "Tooling"]);
    const topics = [...card()!.querySelectorAll<HTMLElement>("article[data-minutes-topic]")];
    expect(topics.map((a) => document.getElementById(a.getAttribute("aria-labelledby")!)!.textContent)).toEqual(["Export", "Release date", "Cache bug"]);
    expect(topics.map((a) => a.closest("[data-minutes-tint]")!.getAttribute("data-minutes-tint"))).toEqual(["1", "1", "2"]);
    // Times jump to the transcript as the items' do; the AI note ends the card.
    expect(lines[1].querySelector("[aria-label^='Go to transcript']")!.getAttribute("aria-controls")).toBe("recording-line-20.5");
    const parts = [...card()!.parentElement!.children];
    expect(parts.at(-1)!.textContent).toBe("AI-generated from the recording. Check anything important.");
    expect(parts.indexOf(card()!)).toBe(0);
    // The suggested to-dos keep their Added and Add to to-dos.
    const added = [...section().querySelectorAll("li")].find((li) => li.textContent?.includes("Share notes"))!;
    expect(added.textContent).toContain("Added");
    expect(host.textContent!.indexOf("Export ships with the cap")).toBeLessThan(host.textContent!.indexOf("A short profile."));
  });

  it("has no group heading for the single untitled group: a plain grid, each topic an h4, the title when there is no headline", async () => {
    const o = overview();
    delete o.headline;
    o.conclusions = [];
    o.groups = [{ title: "", topics: [...o.groups[0].topics, ...o.groups[1].topics] }];
    await load(withOverview(o));
    expect(headings()).toEqual(["H3 Design review", "H4 Export", "H4 Release date", "H4 Cache bug", "H4 Action items", "H4 Open questions"]);
    expect(card()!.querySelector("section")).toBeNull();
    expect(card()!.querySelector("[data-minutes-conclusions]")).toBeNull();
    expect([...card()!.querySelectorAll<HTMLElement>("article")].map((a) => a.closest("[data-minutes-tint]")!.getAttribute("data-minutes-tint"))).toEqual(["1", "1", "1"]);
    expect(card()!.querySelector("[data-minutes-group]")!.tagName).toBe("DIV");
  });

  it.each([
    ["a string", "x"],
    ["null", null],
    ["an empty object", {}],
    ["an array", [overview()]],
    ["groups that are not a list", { headline: "Never shown", groups: "x" }],
    ["no bullet left", { headline: "Never shown", groups: [{ title: "A", topics: [{ title: "T", bullets: [{ text: " ", at: 1 }, "x"] }, { bullets: [{ text: "No title" }] }] }] }],
  ])("falls back to the minutes as before for %s", async (_, o) => {
    await load(withOverview(o));
    expect(headings()).toEqual(legacy);
    expect(card()).toBeNull();
    expect(section().querySelector("[data-minutes-ai-note]")).toBeNull();
    expect(section().textContent).not.toContain("Never shown");
    for (const text of ["Ada · Export limit", "Keep the cap", "Per-entry limit?"]) expect(section().textContent).toContain(text);
  });

  // The cases the hub and the iPhone check too (spec/fixtures/minutes-overview): the layout each gets, and the bold places.
  it.each(overviewCases.cases.map((c) => [c.name, c] as const))("lays out the shared fixture's %s as it says", async (_, c) => {
    const data = fixture();
    data.detail.minutes = { ...(c.input.minutes as object), markdown: c.expected.markdown ?? "" } as typeof data.detail.minutes;
    await load(data);
    expect(card() ? "overview" : "legacy").toBe(c.expected.layout);
    const o = c.expected.overview as { conclusions: { segments: { text: string; bold: boolean }[] }[]; groups: { topics: { bullets: { segments: { text: string; bold: boolean }[] }[] }[] }[] } | null;
    if (!o) return expect(headings()[0]).toBe(`H3 ${(c.input.minutes as { title: string }).title}`);
    const lines = [...o.conclusions, ...o.groups.flatMap((g) => g.topics.flatMap((t) => t.bullets.slice(0, 4)))];
    expect([...card()!.querySelectorAll("strong")].map((b) => b.textContent)).toEqual(lines.flatMap((l) => l.segments.filter((p) => p.bold).map((p) => p.text)));
    expect([...card()!.querySelectorAll("[data-minutes-conclusion] p, [data-minutes-bullet] p")].map((p) => p.textContent))
      .toEqual(lines.map((l) => [l.segments.map((p) => p.text).join(""), "decision" in l ? labelOf(l as never) : ""].filter(Boolean).join(" ")));
  });

  it("folds a topic after 4 bullets and opens the rest in place", async () => {
    const o = overview();
    o.groups[0].topics[0].bullets = bullets(6);
    o.groups[0].topics[1].bullets = bullets(5);
    await load(withOverview(o));
    const topic = () => card()!.querySelector<HTMLElement>("article")!;
    const shown = () => [...topic().querySelectorAll("[data-minutes-bullet] p")].map((p) => p.textContent);
    const toggle = () => topic().querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    expect(shown()).toEqual(["Line 1", "Line 2", "Line 3", "Line 4"]);
    expect(toggle().textContent).toBe("Show 2 more");
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
    // Its name starts with its words and names its topic; it controls the topic's list.
    expect(toggle().getAttribute("aria-label")).toBe("Show 2 more in Export");
    const list = document.getElementById(toggle().getAttribute("aria-controls")!)!;
    expect(list.tagName).toBe("UL");
    expect(topic().contains(list)).toBe(true);
    expect(list.querySelectorAll("[data-minutes-bullet]")).toHaveLength(4);
    // Two folded topics' buttons have different names.
    expect(card()!.querySelectorAll("article")[1].querySelector("button[aria-expanded]")!.getAttribute("aria-label")).toBe("Show 1 more in Release date");
    act(() => toggle().click());
    expect(shown()).toEqual(["Line 1", "Line 2", "Line 3", "Line 4", "Line 5", "Line 6"]);
    expect(list.querySelectorAll("[data-minutes-bullet]")).toHaveLength(6);
    expect(toggle().textContent).toBe("Show less");
    expect(toggle().getAttribute("aria-label")).toBe("Show less of Export");
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    act(() => toggle().click());
    expect(shown()).toHaveLength(4);
    // A topic of 4 or fewer has no toggle.
    expect(card()!.querySelectorAll("article")[2].querySelector("button[aria-expanded]")).toBeNull();
  });

  it("keeps a topic open while the minutes keep their version, and closed once they are rewritten", async () => {
    vi.useFakeTimers();
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const o = overview();
    o.groups[0].topics[0].bullets = bullets(6);
    const data = withOverview(o); data.detail.minutes!.actions!.push({ id: "a3", what: "Review tests", at: 50 });
    await load(data);
    const first = () => card()!.querySelector<HTMLElement>("article")!;
    const toggle = () => first().querySelector<HTMLButtonElement>("button[aria-expanded]")!;
    act(() => toggle().click());
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    // A confirmed to-do keeps the version: the topic stays open.
    click("Add to to-dos");
    await reply(writes()[0], { todos: [{ id: "todo-new" }] });
    data.detail.minutes!.actions![0].todo_id = "todo-new";
    await reply(requests.at(-1)!, data);
    expect(first().querySelectorAll("[data-minutes-bullet]")).toHaveLength(6);
    expect(toggle().getAttribute("aria-expanded")).toBe("true");
    // Regenerate writes new minutes (a new version) whose first topic is another one: it starts folded.
    click("Regenerate");
    await reply(writes()[1], { state: "pending" }, 202);
    data.detail.meta!.minutes_state = "pending";
    await reply(requests.at(-1)!, data);
    const rewritten = overview();
    rewritten.groups[0].topics[0] = { title: "Rollout", icon: "release", bullets: bullets(6) };
    const next = withOverview(rewritten); next.detail.minutes!.version = 3;
    act(() => vi.advanceTimersByTime(5000));
    await reply(requests.at(-1)!, next);
    expect(first().querySelector("h5")!.textContent).toBe("Rollout");
    expect(first().querySelectorAll("[data-minutes-bullet]")).toHaveLength(4);
    expect(toggle().getAttribute("aria-expanded")).toBe("false");
  });

  it("adds one action and all the remaining ones with the minutes version, as before", async () => {
    const data = withOverview(); data.detail.minutes!.actions!.push({ id: "a3", what: "Review tests", at: 50 });
    await load(data);
    click("Add to to-dos");
    expect(writes()[0].url).toBe(`${minutesPath}/todos`);
    expect(JSON.parse(writes()[0].init.body as string)).toEqual({ version: 2, actions: ["a1"] });
    await reply(writes()[0], { todos: [{ id: "todo-new" }] });
    await reply(requests.at(-1)!, data);
    click("Add all");
    expect(JSON.parse(writes()[1].init.body as string)).toEqual({ version: 2, actions: ["a1", "a3"] });
  });

  it("copies the stored markdown", async () => {
    const data = withOverview(); data.detail.minutes!.markdown = "# Design review\n\n## Core conclusions\n\n- [14:00:20] Export ships with the **4 MB** cap";
    await load(data);
    await act(async () => button("Copy").click());
    expect(clipboard).toHaveBeenCalledWith(data.detail.minutes!.markdown);
  });

  it("disables every time on a trimmed recording, keeps the to-dos, and shows no time for a line whose time did not read", async () => {
    const data = withOverview();
    data.detail.meta!.trimmed_at = "2026-11-25T02:00:00Z"; delete data.detail.segments; delete data.detail.summary;
    const o = overview();
    o.groups[1].topics[0].bullets.push({ text: "No time", strong: [], at: -3 });
    data.detail.minutes!.overview = o;
    await load(data);
    const times = [...card()!.querySelectorAll<HTMLButtonElement>("[aria-label^='Go to transcript']")];
    expect(times).toHaveLength(6);
    expect(times.every((b) => b.disabled)).toBe(true);
    const last = [...card()!.querySelectorAll<HTMLElement>("[data-minutes-bullet]")].at(-1)!;
    expect(last.textContent).toBe("No time");
    expect(section().querySelector<HTMLButtonElement>("[aria-label='Go to transcript at 14:00:25']")!.disabled).toBe(true);
    click("Add to to-dos");
    expect(writes().map((w) => w.url)).toEqual([`${minutesPath}/todos`]);
  });
});
