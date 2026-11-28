// @vitest-environment jsdom
import fixtures from "../../../../spec/fixtures/todo-wait/todo_wait.json";
import type { TodoWait } from "../../lib/todo-wait-wire";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { formatDate, initI18n, t } from "../../i18n";
import { reset } from "../../lib/store";
import { assistantEntries } from "../../lib/assistant-entries";
import { todoStatusLabel, TODO_STATUS_INFO, type Todo } from "../../lib/todos";
import { ParkedTaskRows } from "./ParkedTaskRow";
import { TaskWaitInfo } from "./TaskWaitInfo";

vi.hoisted(() => {
  globalThis.ResizeObserver ??= class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver;
  window.matchMedia ??= ((query: string) => ({ matches: false, media: query, onchange: null, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false })) as typeof window.matchMedia;
});
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const NOW = new Date("2026-11-17T12:00:00Z").getTime();
const contact = { channel: "Email", url: "https://example.test/thread/7", sender: "Riley", sent_at: "2026-11-15T08:00:00Z", request: "Please confirm the revised date", status: "sent" };
const makeTodo = (evidence?: Record<string, unknown>, more: Record<string, unknown> = {}): Todo => ({
  id: "wait-1", title: "Example rollout", created_at: "2026-11-01T00:00:00Z", updated_at: "2026-11-17T11:00:00Z",
  supervision: { supervisor: "conversation-1", state: "parked", parked: { reason: "Waiting on the external team", until: "2026-11-16T12:00:00Z", waiting_on: "someone_else", count: 1, evidence } },
  ...more,
} as Todo);

let host: HTMLDivElement;
let root: Root;
let fetchMock: ReturnType<typeof vi.fn>;
const render = async (todo: Todo) => act(async () => { root.render(<TaskWaitInfo todo={todo} expanded />); });
const text = (selector: string) => host.querySelector<HTMLElement>(selector)?.textContent ?? "";

beforeEach(async () => {
  await initI18n("en");
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
  fetchMock = vi.fn();
  vi.stubGlobal("fetch", fetchMock);
  host = document.createElement("div"); document.body.append(host); root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove(); reset(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks();
  await initI18n("zh-Hans");
});

describe("task wait information", () => {
  it("shows both confirmed handoffs in the tray and the same receiving to-do in the detail", async () => {
    const at = new Date(NOW - 61 * 60_000).toISOString();
    const until = new Date(NOW + 60 * 60_000).toISOString();
    const toDo = (id: string, check: string): Todo => makeTodo(undefined, {
      id, title: id, origin: "conversation-1", updated_at: at,
      supervision: { supervisor: "conversation-1", state: "parked",
        reviews: [{ at, verdict: "parked", waiting_on: "todo_handoff", parks: 1, reason: check }],
        parked: { reason: check, until, waiting_on: "todo_handoff", count: 1,
          handoff: { todo_id: "shared-verification", title: "Release verification", check } },
      },
    });
    const first = toDo("candidate-a", "Check the sign-in screen");
    const second = toDo("candidate-b", "Check saved settings");
    expect(todoStatusLabel(first, "follow_up")).toBe(TODO_STATUS_INFO.waiting.label);
    expect(todoStatusLabel(first, "needs_you")).toBe(TODO_STATUS_INFO.needs_you.label);
    const entries = assistantEntries({ conversation: "conversation-1", main: "conversation-1", todos: [first, second],
      sessions: [], interactions: [], plans: {}, feed: [], now: NOW });
    expect(entries.parked).toHaveLength(2);
    await act(async () => root.render(<ParkedTaskRows items={entries.parked} />));
    const rows = [...host.querySelectorAll('[data-wait-kind="todo_handoff"]')];
    expect(rows.every((row) => row.getAttribute("data-wait-collapsed") === "true")).toBe(true);
    expect(rows.map((row) => row.querySelector("[data-wait-target]")?.getAttribute("href")))
      .toEqual(["/todos?id=shared-verification", "/todos?id=shared-verification"]);
    expect(rows.every((row) => row.querySelector("[data-wait-wake]") === null)).toBe(true);
    expect(rows.every((row) => row.querySelector("[data-wait-age]")?.textContent === "Waiting 1 hr")).toBe(true);
    for (const row of rows) {
      await act(async () => row.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    }
    expect(rows.map((row) => row.querySelector("[data-wait-wake]")?.textContent))
      .toEqual(["Check the sign-in screen", "Check saved settings"]);
    await render(first);
    expect(text("[data-wait-target]")).toBe("Release verification");
    expect(text("[data-wait-wake]")).toBe("Check the sign-in screen");
    expect(host.textContent).toContain(t("task-wait.handoff_deadline", { time: formatDate(until, { dateStyle: "medium", timeStyle: "short" }) }));
    expect(host.querySelector("[data-wait-contact-missing]")).toBeNull();
  });

  it("orders an explicitly unsorted lazy waiting list by first request", async () => {
    const older = { ...makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact }), id: "older", origin: "conversation-1" };
    const newer = { ...makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply",
      contact: { ...contact, sent_at: "2026-11-17T08:00:00Z" } }), id: "newer", origin: "conversation-1" };
    const entries = assistantEntries({ conversation: "conversation-1", main: "conversation-1", todos: [newer, older],
      sessions: [], interactions: [], plans: {}, feed: [], now: NOW }).parked;
    const items = ["newer", "older"].map((id) => entries.find((item) => item.kind === "task" && item.todo.id === id)!);
    expect(items).not.toContain(undefined);
    await act(async () => root.render(<ParkedTaskRows items={items} />));
    expect([...host.querySelectorAll("[data-parked-task-row]")].map((row) => row.getAttribute("data-parked-task-row")))
      .toEqual(["older", "newer"]);
  });

  it("shows the recorded person request, sender, age, wake and source without substituting the recorder", async () => {
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They confirm a date", contact, recorded_by: "recorder-not-sender", url: "https://example.test/evidence/7" });
    await render(todo);
    const info = host.querySelector<HTMLElement>('[data-task-wait-info="wait-1"]')!;
    expect(info.dataset.waitKind).toBe("person_reply");
    expect(text("[data-wait-target]")).toContain("Design team");
    expect(text("[data-wait-age]")).toContain("2 days");
    expect(text("[data-wait-contact]")).toContain("Riley");
    expect(text("[data-wait-request]")).toContain(contact.request);
    expect(text("[data-wait-wake]")).toContain("They confirm a date");
    expect([...host.querySelectorAll<HTMLAnchorElement>("[data-wait-source]")].map(a => a.href)).toEqual(["https://example.test/evidence/7"]);
    expect(info.textContent).not.toContain("recorder-not-sender");
    expect(info.dataset.waitOverdue).toBe("true");
    expect(host.querySelector("[data-wait-due]")).not.toBeNull();
  });

  it("shows a ready review source, while unknown and unsafe URLs are never linked", async () => {
    const review = makeTodo({ kind: "merge_review", target: "Example PR", wake: "Review is approved", ready: true, url: "https://example.test/review/8", contact: { ...contact, url: "javascript:alert(1)" } });
    await render(review);
    expect(host.querySelector('[data-wait-kind="merge_review"]')).not.toBeNull();
    expect(host.querySelector('[data-wait-contact]')).toBeNull();
    expect(host.querySelector('[data-wait-contact-missing]')).not.toBeNull();
    expect(host.querySelectorAll("a[href^='javascript:']")).toHaveLength(0);
    expect(host.querySelectorAll("a[href='https://example.test/review/8']")).toHaveLength(1);
    expect(text("[data-wait-wake]")).toContain("Review is approved");
    await render(makeTodo({ kind: "date", target: "Release window", wake: "The date arrives", until: "2026-11-18T00:00:00Z", url: "https://example.test/calendar/3", recorded_by: "Supervisor" }));
    expect(host.querySelector('[data-wait-kind="date"]')).not.toBeNull();
    expect(host.querySelector('[data-wait-contact-missing]')).toBeNull();
    expect([...host.querySelectorAll<HTMLAnchorElement>("[data-wait-source]")].map(a => a.href)).toEqual(["https://example.test/calendar/3"]);
    await render(makeTodo({ kind: "external_event", target: "Deployment", wake: "Health check passes", url: "javascript:alert(1)" }));
    expect(host.querySelector('[data-wait-contact-missing]')).toBeNull();
    expect(host.querySelectorAll("[data-wait-source]")).toHaveLength(0);
  });

  it("shows an external event's target, source, wake and fallback without a request line it cannot carry", async () => {
    // The recorded target may describe a prompt to the owner in free text: it is shown as written, never read as a sent
    // request or as an owner decision.
    const todo = makeTodo({ kind: "external_event", target: "Read-only permission prompt already sent", wake: "The connector's preflight passes",
      url: "https://example.test/connector/permissions/4", recorded_by: "Supervisor" });
    todo.supervision!.parked!.waiting_on = "external_event";
    await render(todo);
    const info = host.querySelector<HTMLElement>('[data-task-wait-info="wait-1"]')!;
    expect(info.dataset.waitKind).toBe("external_event");
    expect(text("[data-wait-target]")).toBe(`${t("task-wait.kind_external_event")}${t("task-wait.target", { target: "Read-only permission prompt already sent" })}`);
    expect([...host.querySelectorAll<HTMLAnchorElement>("[data-wait-source]")].map(a => a.href)).toEqual(["https://example.test/connector/permissions/4"]);
    expect(text("[data-wait-wake]")).toContain("The connector's preflight passes");
    expect(info.textContent).toContain(t("task-wait.fallback", { time: formatDate("2026-11-16T12:00:00.000Z", { dateStyle: "medium", timeStyle: "short" }) }));
    for (const sel of ["[data-wait-contact]", "[data-wait-contact-missing]", "[data-wait-request]", "[data-testid='wait-reminder']"]) expect(host.querySelector(sel)).toBeNull();
    expect(info.textContent).not.toContain(t("task-wait.contact_unknown"));
  });

  it("says a sent request is not recorded only for waits that can record one", async () => {
    const missing = t("task-wait.contact_unknown");
    for (const evidence of [
      { kind: "person_reply", target: "Design team", wake: "They reply" },
      { kind: "merge_review", target: "Review team", wake: "Approval and merge", ready: true, url: "https://github.com/example/demo/pull/7" },
      undefined,
    ]) {
      await render(makeTodo(evidence));
      expect(host.querySelector("[data-wait-contact]")).toBeNull();
      expect(text("[data-wait-contact-missing]")).toBe(missing);
    }
    for (const kind of ["external_event", "date"]) {
      await render(makeTodo({ kind, target: "Release", wake: "It ships", url: "https://example.test/release/2" }));
      expect(host.querySelector(`[data-wait-kind="${kind}"]`)).not.toBeNull();
      expect(host.querySelector("[data-wait-contact-missing]")).toBeNull();
    }
  });

  it("keeps legacy missing evidence visibly unknown rather than inventing a contact, wake or source", async () => {
    await render(makeTodo(undefined));
    expect(host.querySelector('[data-wait-kind="legacy"]')).not.toBeNull();
    expect(host.querySelector('[data-wait-contact-missing]')).not.toBeNull();
    expect(text("[data-wait-age]")).toContain("Wait start not recorded");
    expect(text("[data-wait-wake]")).toContain("not recorded");
    expect(host.querySelectorAll("[data-wait-source]")).toHaveLength(0);
  });

  it("keeps PR blockers and one source when the request receipt is the PR page", async () => {
    const url = "https://github.com/example/demo/pull/7";
    const todo = makeTodo({ kind: "merge_review", target: "Review team", wake: "Review and merge", ready: true, url, contact: { ...contact, url } });
    await act(async () => root.render(<TaskWaitInfo todo={todo} merges={[{ url, state: "open", draft: false, review: "required", checks: "failure", conflicts: false, checked_at: new Date(NOW).toISOString() }]} />));
    expect(host.querySelector('[data-wait-collapsed="true"]')).not.toBeNull();
    expect(host.querySelector("[data-pr-waiting-url]")).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(host.querySelectorAll(`a[href="${url}"]`)).toHaveLength(1);
    expect(text("[data-wait-contact]")).toContain("Riley");
    expect(host.querySelector("[data-wait-contact-missing]")).toBeNull();
    expect(host.querySelector("[data-pr-waiting-url]")?.textContent).toContain(t("assistant-tasks.pr_blocker_checks_failed"));
    expect(host.querySelector("[data-pr-waiting-url]")?.textContent).toContain(t("assistant-tasks.pr_blocker_review_required"));
  });

  it.each([
    ["https://github.com/example/demo/pull/73", "PR #73"],
    ["https://github.com/example/demo/issues/912", "issue #912"],
    ["https://github.com/example/demo/issues/912#issuecomment-42", "Comment on #912"],
    ["https://github.com/example/demo/pull/73#discussion_r42", "Comment on #73"],
    ["https://github.com/example/demo/pull/73#pullrequestreview-42", "Comment on #73"],
    ["https://github.com/example/demo/pull/73/files#r42", "Comment on #73"],
    ["https://github.com.example.test/example/demo/pull/73", "github.com.example.test/example/demo/pull/73"],
    ["https://example.test/evidence/7", "example.test/evidence/7"],
  ])("names the GitHub source %s rather than numbering it", async (url, label) => {
    await render(makeTodo({ kind: "external_event", target: "A review", wake: "A response arrives", url }));
    const source = host.querySelector<HTMLAnchorElement>("[data-wait-source]")!;
    expect(source.textContent).toBe(label);
    expect(source.href).toBe(url);
    expect(source.title).toBe(url);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    ["en", "Comment on #912"],
    ["zh-Hans", "#912 \u4e0b\u7684\u8bc4\u8bba"],
    ["ja", "#912 \u306e\u30b3\u30e1\u30f3\u30c8"],
  ])("localizes a GitHub comment source in %s", async (locale, label) => {
    await initI18n(locale);
    await render(makeTodo({ kind: "external_event", target: "A reply", wake: "A response arrives",
      url: "https://github.com/example/demo/issues/912#issuecomment-42" }));
    expect(text("[data-wait-source]")).toBe(label);
  });

  it("shows the bound PR, not an older associated PR, in the actual parked row", async () => {
    const url = "https://github.com/example/demo/pull/73";
    const old = "https://github.com/example/demo/pull/7";
    const todo = makeTodo({ kind: "external_event", target: "Review of the new PR", wake: "Review arrives", url }, {
      origin: "conversation-1", pr_url: old,
      pr_status: { url: old, state: "open", draft: false, review: "required", checks: "success", conflicts: false, checked_at: new Date(NOW).toISOString() },
    });
    const items = assistantEntries({ conversation: "conversation-1", main: "conversation-1", todos: [todo],
      sessions: [], interactions: [], plans: {}, feed: [], now: NOW }).parked;
    await act(async () => root.render(<ParkedTaskRows items={items} />));
    expect(host.querySelector('[data-wait-collapsed="true"]')).not.toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect([...host.querySelectorAll<HTMLAnchorElement>("[data-wait-source]")].map(a => [a.href, a.textContent]))
      .toEqual([[url, "PR #73"]]);
    expect(host.querySelector(`a[href="${old}"]`)).toBeNull();
  });

  it.each(["applink.feishu.cn", "applink.larksuite.com"])("names the matching recorded Lark topic on %s, never an unrelated topic", async (host) => {
    const url = `https://${host}/client/thread/open?open_chat_id=oc_example_group&open_thread_id=omt_example_topic`;
    const todo = makeTodo({ kind: "external_event", target: "Support team", wake: "The topic changes", url }, {
      thread: { channel: "feishu", chat_id: "oc_example_group", thread_id: "omt_example_topic", title: "Release questions" },
    });
    await render(todo);
    expect(text("[data-wait-source]")).toBe("Release questions");
    todo.thread = { ...todo.thread!, thread_id: "omt_another_topic", title: "Unrelated topic" };
    await render(todo);
    expect(text("[data-wait-source]")).toBe("Lark conversation");
    expect(text("[data-wait-source]")).not.toContain("Unrelated");
    todo.thread = { ...todo.thread!, thread_id: "omt_example_topic", chat_id: "oc_another_group" };
    await render(todo);
    expect(text("[data-wait-source]")).toBe("Lark conversation");
  });

  it("keeps a recorded Lark group name in the contact link without duplicating the receipt", async () => {
    const url = "https://applink.feishu.cn/client/chat/open?open_chat_id=oc_example_group";
    const todo = makeTodo({ kind: "person_reply", target: "Support team", wake: "They reply", url,
      contact: { ...contact, url, channel: "Release support group" } });
    await render(todo);
    expect(text("[data-wait-contact]")).toContain("Release support group");
    expect(host.querySelectorAll(`a[href="${url}"]`)).toHaveLength(1);
    expect(host.textContent).not.toContain("Source 1");
  });

  it("keeps PR blockers on the recorded object when its URL has a fragment or query", async () => {
    const url = "https://github.com/example/demo/pull/73";
    const receipt = `${url}?from=review#issuecomment-42`;
    const todo = makeTodo({ kind: "merge_review", target: "Review team", wake: "Approval and merge", ready: true, url: receipt,
      contact: { ...contact, url: receipt } });
    await act(async () => root.render(<TaskWaitInfo todo={todo} merges={[
      { url, state: "open", draft: false, review: "required", checks: "failure", conflicts: false, checked_at: new Date(NOW).toISOString() },
    ]} />));
    await act(async () => host.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    expect(host.querySelectorAll(`a[href="${receipt}"]`)).toHaveLength(1);
    expect(host.querySelector(`a[href="${url}"]`)).toBeNull();
    expect(host.querySelector("[data-pr-waiting-url]")?.textContent).toContain(t("assistant-tasks.pr_blocker_checks_failed"));
  });

  it.each([undefined, "", "not a URL", "javascript:alert(1)"])("omits an absent or unsafe source without an ordinal placeholder: %s", async (url) => {
    await render(makeTodo({ kind: "external_event", target: "A version", wake: "Version changes", url }));
    expect(host.querySelectorAll("[data-wait-source]")).toHaveLength(0);
    expect(host.textContent).not.toContain("Source 1");
  });

  it("refreshes elapsed wait age from the current clock, not the task update time", async () => {
    let tick: (() => void) | undefined;
    vi.spyOn(globalThis, "setInterval").mockImplementation((callback) => { tick = callback as () => void; return {} as ReturnType<typeof setInterval>; });
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact });
    await render(todo);
    expect(text("[data-wait-age]")).toContain("2 days");
    vi.setSystemTime(NOW + 21 * 3600_000);
    await act(async () => tick?.());
    expect(text("[data-wait-age]")).toContain("3 days");
  });

  it("edits, copies, and cancels an overdue reminder draft without network or task-state writes", async () => {
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact });
    const clipboard = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: clipboard } });
    const snapshot = JSON.stringify(todo);
    await render(todo);
    expect(host.querySelector("[data-wait-overdue='true']")).not.toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid='wait-reminder']")!.click());
    const field = document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!;
    expect(field.value).toContain("Please confirm the revised date");
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      setter.call(field, "Edited local draft");
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(document.querySelector('[role="dialog"] [data-reminder-source]')?.getAttribute("href")).toBe(contact.url);
    await act(async () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(b => b.textContent === "Copy draft")!.click());
    expect(clipboard).toHaveBeenCalledWith("Edited local draft");
    expect(fetchMock).not.toHaveBeenCalled();
    expect(JSON.stringify(todo)).toBe(snapshot);
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid='wait-reminder']")!.click());
    await act(async () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(b => b.textContent === "Cancel")!.click());
    expect(document.querySelector('[role="dialog"]')).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
    expect(JSON.stringify(todo)).toBe(snapshot);
  });

  it("keeps the draft recoverable when copying fails", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(async () => { throw new Error("Clipboard unavailable"); }) } });
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact });
    await render(todo);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid='wait-reminder']")!.click());
    const before = document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!.value;
    await act(async () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(b => b.textContent === "Copy draft")!.click());
    expect(document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')?.value).toBe(before);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("does not discard an edit made while clipboard permission is pending", async () => {
    let copied!: () => void;
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn(() => new Promise<void>((resolve) => { copied = resolve; })) } });
    await render(makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact }));
    await act(async () => host.querySelector<HTMLButtonElement>("[data-testid='wait-reminder']")!.click());
    await act(async () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find(b => b.textContent === "Copy draft")!.click());
    const field = document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(field, "Newer local draft");
      field.dispatchEvent(new Event("input", { bubbles: true }));
      field.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => copied());
    expect(document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')?.value).toBe("Newer local draft");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("collapses list rows to one summary line by default and expands full evidence on demand", async () => {
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They confirm a date", contact, url: "https://example.test/evidence/7" });
    await act(async () => root.render(<TaskWaitInfo todo={todo} />));
    const info = host.querySelector<HTMLElement>('[data-task-wait-info="wait-1"]')!;
    const toggle = host.querySelector<HTMLButtonElement>("[data-wait-toggle]")!;
    expect(info.dataset.waitCollapsed).toBe("true");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain(t("task-wait.details"));
    expect(text("[data-wait-target]")).toContain("Design team");
    expect(text("[data-wait-age]")).toContain("2 days");
    expect(host.querySelector("[data-wait-due]")).not.toBeNull();
    for (const sel of ["[data-wait-details]", "[data-wait-contact]", "[data-wait-wake]", "[data-wait-source]", "[data-wait-reason]", "[data-wait-request]", '[data-testid="wait-reminder"]']) {
      expect(host.querySelector(sel)).toBeNull();
    }
    await act(async () => toggle.click());
    expect(info.dataset.waitCollapsed).toBe("false");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.textContent).toContain(t("task-wait.hide_details"));
    expect(text("[data-wait-contact]")).toContain("Riley");
    expect(text("[data-wait-wake]")).toContain("They confirm a date");
    expect(text("[data-wait-reason]")).toContain("Waiting on the external team");
    expect(text("[data-wait-request]")).toContain(contact.request);
    expect(host.querySelector('[data-testid="wait-reminder"]')).not.toBeNull();
    await act(async () => toggle.click());
    expect(info.dataset.waitCollapsed).toBe("true");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(host.querySelector("[data-wait-details]")).toBeNull();
  });

  it("lets waiting text select its task row while controls and the portalled draft remain separate", async () => {
    const select = vi.fn();
    const todo = makeTodo({ kind: "person_reply", target: "Design team", wake: "They reply", contact });
    await act(async () => root.render(<div onClick={select}><TaskWaitInfo todo={todo} /></div>));
    await act(async () => host.querySelector<HTMLElement>("[data-wait-target]")!.click());
    expect(select).toHaveBeenCalledTimes(1);
    await act(async () => host.querySelector<HTMLButtonElement>("[data-wait-toggle]")!.click());
    const link = host.querySelector<HTMLAnchorElement>("[data-wait-contact]")!;
    link.addEventListener("click", (event) => event.preventDefault(), { once: true });
    await act(async () => link.click());
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="wait-reminder"]')!.click());
    await act(async () => document.querySelector<HTMLTextAreaElement>('[role="dialog"] textarea')!.click());
    await act(async () => [...document.querySelectorAll<HTMLButtonElement>('[role="dialog"] button')].find((button) => button.textContent === "Cancel")!.click());
    expect(select).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("server-computed wait", () => {
  it("renders a waiting run even without parked evidence, with distinct wake and deadline", async () => {
    const wake_at = "2026-11-18T12:00:00Z", until = "2026-11-19T16:00:00Z";
    await render(makeTodo(undefined, { supervision: undefined, wait: {
      kind: "agent", waiting_on: "Verification", reason: "Checking the export", wake_condition: "The checks finish", wake_at, until,
    } }));
    expect(text("[data-wait-target]")).toContain("Verification");
    expect(text("[data-wait-reason]")).toBe("Checking the export");
    expect(text("[data-wait-wake]")).toContain("The checks finish");
    for (const time of [wake_at, until]) {
      expect(host.textContent).toContain(formatDate(time, { dateStyle: "medium", timeStyle: "short" }));
    }
  });
});

describe("TodoWait wire fixtures", () => {
  it.each(fixtures.cases.filter((c) => c.expected.wait))("$name renders only the projected evidence", async ({ expected }) => {
    const wait = expected.wait as TodoWait;
    // Supply conflicting old evidence and omit the sessions. Each field must stand on its own.
    await render(makeTodo({ kind: "date", target: "Old target", wake: "Old condition" }, { wait }));
    const body = host.textContent!;
    expect(host.querySelector("[data-wait-kind]")?.getAttribute("data-wait-kind")).toBe(wait.kind);
    for (const value of [wait.waiting_on, wait.reason, wait.wake_condition, wait.step, wait.contact?.request,
      wait.contact?.sender, wait.contact?.channel].filter(Boolean)) expect(body).toContain(value);
    for (const at of [wait.wake_at, wait.until, wait.contact?.sent_at]) {
      if (at) expect(body).toContain(formatDate(at, { dateStyle: "medium", timeStyle: "short" }));
    }
    if (wait.since) expect(text("[data-wait-age]")).not.toBe(t("task-wait.age_unknown"));
    if (wait.url) expect([...host.querySelectorAll("a")].map((a) => a.href)).toContain(wait.url);
    if (wait.contact?.url) expect(host.querySelector("[data-wait-contact]")?.getAttribute("href")).toBe(wait.contact.url);
    if (wait.handoff_todo_id) expect(host.querySelector("[data-wait-target]")?.getAttribute("href")).toBe(`/todos?id=${wait.handoff_todo_id}`);
    for (const blocker of wait.pr_blockers ?? []) {
      expect(body).toContain(t(`assistant-tasks.pr_blocker_${blocker === "checks_failing" ? "checks_failed" : blocker}`));
    }
    expect(body).not.toMatch(/Old target|Old condition|Waiting on the external team/);
  });

  it("keeps future wait kinds neutral and preserves their provided fields", async () => {
    await render(makeTodo({ kind: "date", target: "Earlier target" }, { wait: {
      kind: "future_kind", waiting_on: "Upstream event", pr_blockers: ["future_blocker"],
    } }));
    expect(text("[data-wait-target]")).toBe("Waiting: Upstream event");
    expect(host.textContent).not.toMatch(/future_kind|future_blocker|Earlier target/);
  });

  it("shows an incomplete receipt as text and never links an unsafe source", async () => {
    await render(makeTodo(undefined, { wait: {
      kind: "person_reply", contact: { channel: "Email", url: "javascript:alert(1)" },
    } }));
    expect(text("[data-wait-contact]")).toBe("Email");
    expect(host.querySelector("a")).toBeNull();
    expect(host.querySelector("[data-wait-request]")).toBeNull();
  });

  it.each([undefined, null])("keeps recorded evidence when wait is %s", async (wait) => {
    await render(makeTodo({ kind: "external_event", target: "Earlier target" }, { wait }));
    expect(text("[data-wait-target]")).toContain("Earlier target");
  });
});
