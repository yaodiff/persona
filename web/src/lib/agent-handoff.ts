// "Hand to Persona" (the Agents list's row menu, iOS AgentHandoff): what goes to Persona's main conversation about one agent —
// where it runs and what it last said as a context card, then the ask.
import { t } from "../i18n";
import { baseName, activityOf } from "./format";
import { contextXML } from "./context-xml";
import type { Session } from "./types";

export function agentHandoffText(s: Session, machineName?: string): string {
  const title = s.title || baseName(s.cwd);
  let text = t("lib-agent-handoff.context", { machine: machineName || s.machine_id, machine_id: s.machine_id, session: s.session_id, cwd: s.cwd, state: activityOf(s) });
  if (s.last_text) text += "\n" + t("lib-agent-handoff.last_said", { text: [...s.last_text].slice(0, 800).join("") });
  return `${contextXML({ kind: "context", title: `Agent · ${title}`, url: "", text })}\n\n${t("lib-agent-handoff.ask")}`;
}
