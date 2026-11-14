// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { initI18n, t } from "../../i18n";
import { setToken } from "../../lib/api";
import { appArchiveDeps } from "../../lib/agent-archives";
import { getState, reset, upsertSession } from "../../lib/store";
import type { Session } from "../../lib/types";
import { CloseAgentDialog } from "./CloseAgentDialog";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const session: Session = {
  machine_id: "m", session_id: "runtime-session", title: "Example", cwd: "", status: "idle",
  source_kind: "runtime", agent: "agy", capabilities: { read: true, terminal: false, close: true },
};
let host: HTMLDivElement;
let root: Root;
const closed = vi.fn();
const completed = vi.fn();
const fetcher = vi.fn();

beforeEach(async () => {
  await initI18n("en");
  setToken("synthetic-close-owner");
  reset();
  upsertSession(session);
  closed.mockClear();
  completed.mockClear();
  fetcher.mockReset().mockResolvedValue(new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fetcher);
  vi.stubGlobal("matchMedia", (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<CloseAgentDialog session={session} machineName="Example computer" open onClose={closed} onClosed={completed} />));
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.unstubAllGlobals();
});

async function confirm() {
  const button = [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((element) => element.textContent === "Close agent");
  expect(button).toBeDefined();
  await act(async () => button!.click());
}

it("closes a runtime with the close capability through the existing confirmation", async () => {
  expect(document.querySelector("[role=dialog]")?.textContent).toContain("The conversation history stays available.");
  expect(document.querySelector("[role=dialog]")?.textContent).not.toContain("quits Claude");
  await confirm();
  expect(fetcher).toHaveBeenCalledWith(expect.stringContaining("/sessions/m/runtime-session/close"), expect.objectContaining({ method: "POST" }));
  expect(completed).toHaveBeenCalledOnce();
});

it("dismisses the confirmation if the runtime went offline meanwhile", async () => {
  act(() => upsertSession({ ...session, status: "offline" }));
  await confirm();
  expect(fetcher).not.toHaveBeenCalled();
  expect(closed).toHaveBeenCalledOnce();
  expect(completed).not.toHaveBeenCalled();
});

it("asks the engine an agent ran in about its own worktree, and has that engine remove it", async () => {
  const paseo: Session = { ...session, session_id: "paseo-1", host: "paseo", cwd: "/w/trees/fix", agent: "claude" };
  const reply = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  fetcher.mockReset().mockImplementation(async (url: string) => String(url).endsWith("/worktree/status")
    ? reply({ worktree: true, ours: true, path: "/w/trees/fix", branch: "persona/fix", dirty: 0, unmerged: 0 })
    : String(url).endsWith("/worktree/remove") ? reply({ removed: true, branch_deleted: false }) : reply({}));
  act(() => upsertSession(paseo));
  await act(async () => root.render(<CloseAgentDialog session={paseo} machineName="Example computer" open onClose={closed} onClosed={completed} />));
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  const body = (path: string) => JSON.parse(fetcher.mock.calls.find(([url]) => String(url).endsWith(path))![1].body);
  expect(body("/worktree/status")).toEqual({ cwd: "/w/trees/fix", kind: "paseo" });
  const remove = [...document.querySelectorAll<HTMLButtonElement>("[role=radio]")].find((b) => b.textContent === "Remove it");
  await act(async () => remove!.click());
  await confirm();
  await act(async () => { await new Promise((resolve) => setTimeout(resolve, 0)); });
  expect(body("/worktree/remove")).toEqual({ path: "/w/trees/fix", kind: "paseo" });
});

// "Close…" (#7125): one dialog for an agent the hub can archive and close (it does so when the agent is idle) and for a
// busy one (it says what closing interrupts, and closes it through POST …/close).
describe("Close…", () => {
  const claude: Session = { machine_id: "m", session_id: "claude-1", title: "Fix the build", cwd: "", status: "idle", state: "idle", agent: "claude", pane_id: "w1:p1" };
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
  const dialog = () => document.querySelector("[role=dialog]")!;
  const buttonNamed = (label: string) => [...document.querySelectorAll<HTMLButtonElement>("[role=dialog] button")].find((b) => b.textContent === label);
  const posted = () => fetcher.mock.calls.filter(([, init]) => init?.method === "POST").map(([url]) => String(url).replace(/^.*\/api\/v1/, ""));
  async function open(s: Session) {
    act(() => upsertSession(s));
    await act(async () => root.render(<CloseAgentDialog key={s.status} session={s} machineName="Example computer" open onClose={closed} />));
  }
  beforeEach(() => {
    Object.assign(getState(), { features: ["agent_session_archives", "agent_session_archive_close"] });
    vi.spyOn(appArchiveDeps, "refresh").mockResolvedValue(undefined);
    vi.spyOn(appArchiveDeps, "notify").mockImplementation(() => {});
    fetcher.mockReset().mockImplementation(async () => json({ ok: true, close: { closed: true, outcome: "closed" } }));
  });

  it("archives and closes an agent that is not busy", async () => {
    await open(claude);
    expect(dialog().textContent).toContain(t("chat-sessiondialogs.archive_close_detail", { title: "Fix the build", machine: "Example computer" }));
    expect(dialog().querySelector("[data-close-busy]")).toBeNull();
    await act(async () => buttonNamed(t("chat-sessiondialogs.archive_close"))!.click());
    expect(posted()).toEqual(["/sessions/m/claude-1/archive?close=1"]);
    expect(closed).toHaveBeenCalledOnce();
  });

  it("says what closing a busy agent interrupts, and closes it anyway without archiving", async () => {
    await open({ ...claude, status: "working", state: "working" });
    expect(dialog().querySelector("[data-close-busy]")?.textContent).toBe(t("close-agent.busy_working"));
    expect(buttonNamed(t("chat-sessiondialogs.archive_close"))).toBeUndefined();
    await act(async () => buttonNamed(t("close-agent.close_anyway"))!.click());
    expect(posted()).toEqual(["/sessions/m/claude-1/close"]);
    expect(closed).toHaveBeenCalledOnce();
  });

  it.each([
    ["waiting", { status: "blocked", state: "needs_you" }],
    ["working", { queued: ["next"] }],
    ["loop", { loop: { prompt: "check CI", interval: "5m" } }],
    ["server", { ports: [{ port: 3000 }] }],
  ] as const)("names what it would interrupt: %s", async (why, over) => {
    await open({ ...claude, ...over } as Session);
    expect(dialog().querySelector("[data-close-busy]")?.getAttribute("data-close-busy")).toBe(why);
    expect(buttonNamed(t("close-agent.close_anyway"))).toBeDefined();
  });

  it("follows the agent: one that starts working while it is open asks to close anyway", async () => {
    await open(claude);
    act(() => upsertSession({ ...claude, status: "working", state: "working" }));
    expect(buttonNamed(t("close-agent.close_anyway"))).toBeDefined();
    await act(async () => buttonNamed(t("close-agent.close_anyway"))!.click());
    expect(posted()).toEqual(["/sessions/m/claude-1/close"]);
  });

  it("stays open when the hub refuses the archive as busy, and then offers to close anyway", async () => {
    fetcher.mockReset().mockImplementation(async (url: string) => String(url).includes("/archive") ? json({ error: "busy", code: "agent_busy" }, 409) : json({ ok: true }));
    await open(claude);
    await act(async () => buttonNamed(t("chat-sessiondialogs.archive_close"))!.click());
    expect(closed).not.toHaveBeenCalled();
    expect(dialog().querySelector("[data-close-busy]")?.textContent).toBe(t("close-agent.busy_working"));
    await act(async () => buttonNamed(t("close-agent.close_anyway"))!.click());
    expect(posted()).toEqual(["/sessions/m/claude-1/archive?close=1", "/sessions/m/claude-1/close"]);
    expect(closed).toHaveBeenCalledOnce();
  });

  it("only closes an archived chat's agent that is still open", async () => {
    // the chat's ⋯ marks its snapshot archived; the listed row may not say so
    act(() => upsertSession(claude));
    await act(async () => root.render(<CloseAgentDialog session={{ ...claude, archived: true }} machineName="Example computer" open onClose={closed} />));
    expect(dialog().textContent).not.toContain(t("chat-sessiondialogs.archive_close_detail", { title: "Fix the build", machine: "Example computer" }));
    await act(async () => buttonNamed(t("chat-chatview.close_agent"))!.click());
    expect(posted()).toEqual(["/sessions/m/claude-1/close"]);
  });
});
