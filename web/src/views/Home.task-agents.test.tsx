// The overview's task cards say who works on each task (#10904): the lead agent's computer, model and effort as its
// model pill names them, "+N" for its other agents, a held delivery first, and today's "N agents" while no row is read.
import { describe, expect, it, vi } from "vitest";
import { renderToStaticMarkup } from "react-dom/server";
import type { LauncherModels } from "../lib/launcher-models";
import type { ModelList } from "../lib/session-models";
import type { Todo, TodoAgent } from "../lib/todos";
import type { Machine, Session } from "../lib/types";

vi.hoisted(() => {
  const g = globalThis as Record<string, unknown>;
  g.matchMedia = () => ({ matches: false, addEventListener() {}, removeEventListener() {} });
  const kept = new Map<string, string>();
  g.localStorage = { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v), removeItem: (k: string) => void kept.delete(k) };
});

const fake = vi.hoisted(() => ({ sessions: [] as Session[], machines: [] as Machine[], todos: [] as Todo[], lists: new Map<string, ModelList>() }));

vi.mock("../lib/store", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/store")>();
  return { ...real, useStore: <T,>(select: (s: ReturnType<typeof real.getState>) => T) => select({ ...real.getState(), loaded: true, machines: fake.machines, interactions: [], todos: fake.todos, answered: {} }) };
});
vi.mock("../lib/sessions", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/sessions")>();
  return { ...real, useAgentSessions: () => fake.sessions, useCounts: () => ({ ...real.stateCounts(fake.sessions), interactions: 0 }), useAgentListStatus: () => ({ status: "loaded", whole: true }) };
});
// the model lists the pill has read, by computer and launcher (none unless a test puts one in)
vi.mock("../lib/session-models", async (importOriginal) => {
  const real = await importOriginal<typeof import("../lib/session-models")>();
  return { ...real, modelList: (mid: string, launcher: string) => fake.lists.get(`${mid}/${launcher}`) ?? {} };
});
vi.mock("./extensions/ExtensionSlots", () => ({ OverviewCards: () => null, OverviewArrangeButton: () => null }));

const { Home } = await import("./Home");
const { t } = await import("../i18n");

const machines: Machine[] = [{ id: "mac", name: "MacBook", online: true }, { id: "studio", name: "Studio", online: true }];
const row = (sid: string, o: Partial<Session> = {}): Session => ({
  machine_id: "mac", session_id: sid, title: sid, cwd: "/code", status: "idle", state: "idle", agent: "claude", updated_at: "2026-11-19T08:00:00Z", ...o,
});
const on = (sid: string, o: Partial<TodoAgent> = {}): TodoAgent => ({ machine_id: "mac", session_id: sid, linked_at: "2026-11-19T07:00:00Z", ...o });
const todo = (id: string, o: Partial<Todo> = {}): Todo => ({ id, title: `task ${id}`, created_at: "2026-11-19T07:00:00Z", updated_at: "2026-11-19T07:00:00Z", ...o });

/** The card's agent line: its text and its "+N" (empty without one). */
function agentLine(todos: Todo[], sessions: Session[], id = todos[0]!.id): { who: string; more: string; held: boolean } | null {
  Object.assign(fake, { todos, sessions, machines });
  const html = renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
  Object.assign(fake, { todos: [], sessions: [] });
  const card = html.slice(html.indexOf(`data-task="${id}"`));
  const m = /<p data-task-agents="true" class="([^"]*)"><span class="truncate">([^<]*)<\/span>(?:<span class="shrink-0 tabular-nums">([^<]*)<\/span>)?<\/p>/.exec(card);
  return m ? { who: m[2]!, more: m[3] ?? "", held: m[1]!.includes("text-orange") } : null;
}
const effort = (id: string) => t(`lib-session-actions.effort_${id}`);

describe("a task card's agent line", () => {
  it("one agent: its computer, model and effort", () => {
    const line = agentLine([todo("a", { agents: [on("s1")] })], [row("s1", { model: "claude-opus-5-5", effort: "xhigh" })]);
    expect(line).toEqual({ who: `MacBook · opus 5.5 · ${effort("xhigh")}`, more: "", held: false });
  });

  it("names the model as its model list does once the pill has read it", () => {
    const list: LauncherModels = { agent: "claude", models: [{ id: "opus", name: "Opus 5.5", resolved: "claude-opus-5-5" }] };
    fake.lists.set("mac/claude", { list });
    const line = agentLine([todo("a", { agents: [on("s1")] })], [row("s1", { model: "claude-opus-5-5", effort: "high" })]);
    fake.lists.clear();
    expect(line?.who).toBe(`MacBook · Opus 5.5 · ${effort("high")}`);
  });

  it("several agents: the one working, though another moved later, and +N for the rest", () => {
    const agents = [on("idle"), on("working", { machine_id: "studio" }), on("asks"), on("released", { released_at: "2026-11-19T07:30:00Z" })];
    const sessions = [
      row("idle", { model: "claude-sonnet-5-5", effort: "low", updated_at: "2026-11-19T09:00:00Z" }),
      row("working", { machine_id: "studio", status: "working", state: "working", model: "claude-opus-5-5", effort: "max", updated_at: "2026-11-19T08:00:00Z" }),
      row("asks", { state: "needs_you", model: "claude-haiku-5-5", updated_at: "2026-11-19T09:30:00Z" }),
    ];
    expect(agentLine([todo("a", { agents })], sessions)).toEqual({ who: `Studio · opus 5.5 · ${effort("max")}`, more: "+2", held: false });
  });

  it("several agents, none working: the one that moved last", () => {
    const sessions = [
      row("old", { model: "claude-sonnet-5-5", updated_at: "2026-11-19T08:00:00Z" }),
      row("new", { model: "claude-opus-5-5", state: "unread", updated_at: "2026-11-19T08:00:00Z", state_since: "2026-11-19T09:00:00Z" }),
    ];
    expect(agentLine([todo("a", { agents: [on("old"), on("new")] })], sessions)).toEqual({ who: "MacBook · opus 5.5", more: "+1", held: false });
  });

  it("says no effort when the agent reports none", () => {
    const line = agentLine([todo("a", { agents: [on("cx")] })], [row("cx", { agent: "codex", model: "gpt-5.5" })]);
    expect(line?.who).toBe("MacBook · GPT-5.5");
  });

  it("keeps the gateway channel in an unnamed model label as the model pill does (#10125)", () => {
    const work = [todo("a", { agents: [on("s1")] })];
    for (const channel of ["acme", "relay"]) {
      const line = agentLine(work, [row("s1", { model: `${channel}/orbit-7`, effort: "medium" })]);
      expect(line?.who).toBe(`MacBook · ${channel}/orbit-7 · ${effort("medium")}`);
    }
  });

  it("counts the agents as before while none of their rows is read", () => {
    expect(agentLine([todo("a", { agents: [on("gone")] })], [])).toEqual({ who: t("todos.agent_count", { count: 1 }), more: "", held: false });
    expect(agentLine([todo("a", { agents: [on("x"), on("y"), on("z")] })], [])).toEqual({ who: t("todos.agent_count", { count: 3 }), more: "", held: false });
    // one row read of two: that agent, and the other counted
    expect(agentLine([todo("a", { agents: [on("x"), on("gone")] })], [row("x", { model: "claude-opus-5-5" })])).toEqual({ who: "MacBook · opus 5.5", more: "+1", held: false });
  });

  it("puts a held delivery first, in its colour", () => {
    const line = agentLine([todo("a", { delivery: { state: "merged" }, agents: [on("s1"), on("s2")] })], [row("s1", { model: "claude-opus-5-5", effort: "high" }), row("s2")]);
    expect(line).toEqual({ who: `${t("todos.delivery_merged")} · MacBook · opus 5.5 · ${effort("high")}`, more: "+1", held: true });
    // a task with no agents keeps its delivery alone
    expect(agentLine([todo("b", { delivery: { state: "merged" } })], [])).toEqual({ who: t("todos.delivery_merged"), more: "", held: true });
  });
});

/** The card's status pill: its word and class. */
function statusPill(todos: Todo[], sessions: Session[], id: string): { label: string; className: string } | null {
  Object.assign(fake, { todos, sessions, machines });
  const html = renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
  Object.assign(fake, { todos: [], sessions: [] });
  const card = html.slice(html.indexOf(`data-task="${id}"`));
  const m = /<span class="([^"]*)" data-testid="task-status">([^<]*)<\/span>/.exec(card);
  return m ? { label: m[2]!, className: m[1]! } : null;
}

describe("a task card's status (#11090)", () => {
  const waiting = (sid: string, wait: string) => row(sid, { work: { phase: "blocked", on: "external", line: wait, wait, wake_at: "2099-01-01T00:00:00Z" } });

  it("a waiting task says what it waits on, neutral", () => {
    const pill = statusPill([todo("a", { agents: [on("s1")], delivery: { state: "pr", unblock: "other" } })], [waiting("s1", "CI")], "a");
    expect(pill?.label).toBe(t("lib-state-labels.waits_delivery"));
    expect(pill?.className).toContain("text-ink-3");
    expect(pill?.className).not.toContain("text-attention");
  });

  it("Waiting on you, in the attention color, for one held for the owner's OK", () => {
    const pill = statusPill([todo("a", { agents: [on("s1")], delivery: { state: "merged", unblock: "user" } })], [waiting("s1", "deploy")], "a");
    expect(pill).toEqual({ label: t("lib-state-labels.waits_owner"), className: expect.stringContaining("text-attention") });
  });

  it("other statuses keep their words and colors", () => {
    const pill = statusPill([todo("a", { agents: [on("s1")] })], [row("s1", { state: "working", status: "working" })], "a");
    expect(pill).toEqual({ label: t("lib-state-labels.working"), className: expect.stringContaining("text-orange") });
  });
});

/** A task card's own classes and its markup from there on. */
function card(todos: Todo[], id: string): { box: string[]; inner: string } {
  Object.assign(fake, { todos, sessions: [], machines });
  const html = renderToStaticMarkup(<Home onNewAgent={() => {}} onToggleSidebar={() => {}} />);
  Object.assign(fake, { todos: [] });
  const at = html.indexOf(`data-task="${id}"`);
  return { box: /class="([^"]*)"/.exec(html.slice(html.lastIndexOf("<div ", at), at))![1]!.split(" "), inner: html.slice(at) };
}
const classOf = (html: string, tag: RegExp) => tag.exec(html)?.[1]?.split(" ") ?? [];
const zOf = (classes: string[], prefix: string) => Number(/(\d+)\]?$/.exec(classes.find((c) => c.startsWith(prefix)) ?? "")?.[1] ?? NaN);

describe("a task card on a touch screen (#11442)", () => {
  it("opens from anywhere on it: the title link's overlay spans the card, the PR link above it", () => {
    const { box, inner } = card([todo("a", { stage: "develop", pr_url: "https://example.com/pull/7", agents: [on("s1")] })], "a");
    expect(box).toEqual(expect.arrayContaining(["pointer-coarse:relative", "pointer-coarse:isolate"]));
    const title = classOf(inner, /<a href="\/todos\?id=a" class="([^"]*)"/);
    expect(title).toEqual(expect.arrayContaining(["pointer-coarse:after:absolute", "pointer-coarse:after:inset-0"]));
    // the stage bar's dots are positioned too: the overlay is above them, so a tap on a dot opens the task as well
    expect(zOf(title, "pointer-coarse:after:z-")).toBeGreaterThan(0);
    // the PR link keeps its own tap
    const pr = classOf(inner, /<a href="https:\/\/example\.com\/pull\/7"[^>]* class="([^"]*)"/);
    expect(pr).toContain("pointer-coarse:relative");
    expect(zOf(pr, "pointer-coarse:z-")).toBeGreaterThan(zOf(title, "pointer-coarse:after:z-"));
  });

  it("is as it was under a mouse: only the title is the link", () => {
    const { box, inner } = card([todo("a", { agents: [on("s1")] })], "a");
    expect(box).not.toContain("relative");
    expect(classOf(inner, /<a href="\/todos\?id=a" class="([^"]*)"/).some((c) => c.startsWith("after:"))).toBe(false);
  });
});
