// "Hand work to another computer" (#8037, docs/web/web.md "Machines"): the user picks Agents on a busy computer and a
// target; the request goes to the assistant's main chat as one message in the user's language, and the assistant
// carries it out with its task tools (a task whose plan `continues` the earlier one starts a new Agent on the target
// with the earlier brief), stopping each source Agent only after its successor started. Nothing is moved here: a live
// conversation is not migrated. Pure except sendToPersona, which uses the main chat's own send path (lib/outbox.ts).
import { t } from "../i18n";
import { closeBusy } from "./agent-archives";
import { api, errorText, sessionPath } from "./api";
import { pickedLauncher, pickedModel } from "./assistant-chat";
import { conversationPage } from "./conversation-page";
import { sessionMenuActions, sessionTitle, activityOf } from "./format";
import { chosenManager } from "./manager-choice";
import { askPersona, outboxKey, sendToSession } from "./outbox";
import { navigate } from "./router";
import { getState, refreshAssistant } from "./store";
import type { Machine, Session } from "./types";

/** The Agents a hand-off offers by default: working or idle ones that belong to a to-do (never one that needs you:
 *  it waits for an answer here first). */
export function defaultHandoff(agents: readonly Session[]): Session[] {
  return agents.filter((s) => !!s.todo && ["working", "idle", "unread"].includes(activityOf(s)));
}

/** The one message the assistant gets. Names both computers by name and id, each Agent by title and session id, and
 *  each to-do by id and title, and says how: continue on the target (`continues`), then stop the source Agent. */
export function handoffMessage(source: Pick<Machine, "id" | "name">, target: Pick<Machine, "id" | "name">, agents: readonly Session[]): string {
  const lines = agents.map((s) => s.todo
    ? t("lib-machine-handoff.line_todo", { title: sessionTitle(s), sid: s.session_id, todo: s.todo.title, todo_id: s.todo.id })
    : t("lib-machine-handoff.line_agent", { title: sessionTitle(s), sid: s.session_id }));
  return [
    t("lib-machine-handoff.request", { source: source.name, source_id: source.id, target: target.name, target_id: target.id }),
    "",
    ...lines,
    "",
    t("lib-machine-handoff.how", { target: target.name, source: source.name }),
  ].join("\n");
}

/** Sends `text` to the main chat without leaving the page and says so, with a button that opens the chat. With no main
 *  chat yet, `askPersona` starts one with it (and opens it). Resolves whether it went. */
export async function sendToPersona(text: string, notify: (text: string, tone: "ok" | "error", action?: { label: string; run: () => void }) => void): Promise<boolean> {
  let a = getState().assistant;
  if (!a?.main) {
    await refreshAssistant().catch(() => {});
    a = getState().assistant;
  }
  if (!a?.main) {
    await askPersona(text);
    return true;
  }
  const { machine_id: mid, main: sid } = a;
  const open = { label: t("lib-machine-handoff.open_chat"), run: () => navigate(conversationPage(sid, mid, chosenManager() ?? undefined)) };
  const key = outboxKey(mid, sid);
  const ok = await sendToSession(mid, sid, { text }, { launcher: pickedLauncher(key), model: pickedModel(key) });
  notify(ok ? t("lib-machine-handoff.sent") : t("lib-machine-handoff.not_sent"), ok ? "ok" : "error", open);
  return ok;
}

// ── stopping idle Agents ──

/** The Agents "Stop idle Agents" stops: finished or idle, with nothing running (no work, question, loop, server or
 *  queued message: lib/agent-archives.ts closeBusy), and closable on their computer. Never one working or needing you. */
export function stoppableAgents(agents: readonly Session[]): Session[] {
  return agents.filter((s) => !s.assistant && (activityOf(s) === "idle" || activityOf(s) === "unread") && !closeBusy(s) && sessionMenuActions(s).includes("close"));
}

export interface StopOutcome {
  session: Session;
  ok: boolean;
  error?: string;
}

/** Stops each Agent in turn through the plain close the web's "Close…" uses for one it cannot archive (`POST
 *  /sessions/{mid}/{sid}/close`: the Agent quits, its conversation stays and the next message resumes it), and says
 *  how each went. One at a time, so a computer under pressure is not asked for all of them at once. */
export async function stopAgents(list: readonly Session[], close: (s: Session) => Promise<unknown> = (s) => api(`${sessionPath(s.machine_id, s.session_id)}/close`, { method: "POST" })): Promise<StopOutcome[]> {
  const out: StopOutcome[] = [];
  for (const s of list) {
    try {
      await close(s);
      out.push({ session: s, ok: true });
    } catch (e) {
      out.push({ session: s, ok: false, error: errorText(e) });
    }
  }
  return out;
}
