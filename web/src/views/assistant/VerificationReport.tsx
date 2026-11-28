import { t } from "../../i18n";
import { openFromPage } from "../../lib/handoff";
import { getState, taskSessionLookup } from "../../lib/store";
import { mostUrgentAgent, type SessionLookup, type Todo } from "../../lib/todos";
import { Button } from "../../ui/atoms";
import { TodoAskPanel } from "../TodoAsk";
import { ReviewChecklist } from "./ReviewChecklist";

function openWorker(mid: string, sid: string) {
  openFromPage({ name: "session", mid, sid });
}

export function VerificationReport({ todo, lookup = taskSessionLookup(getState()), onOpenWorker = openWorker }: {
  todo: Todo;
  lookup?: SessionLookup;
  onOpenWorker?: (mid: string, sid: string) => void;
}) {
  const agents = todo.agents?.filter((a) => a.machine_id && a.session_id && !a.start_error && !a.dismissed_at);
  // Verification can start after the worker is released. Its transcript is still available.
  const worker = mostUrgentAgent({ ...todo, agents }, lookup)?.agent
    ?? agents?.filter((a) => a.released_at).sort((a, b) => b.released_at!.localeCompare(a.released_at!))[0];
  return (
    <section aria-label={t("assistant-taskpanel.report")}>
      <h2 className="text-label font-semibold text-ink-2">{t("assistant-taskbrief.goal")}</h2>
      <p className="mt-1 whitespace-pre-wrap text-sm text-ink select-text [overflow-wrap:anywhere]">{todo.brief?.goal || todo.title}</p>
      <TodoAskPanel todo={todo} />
      <ReviewChecklist todo={todo} lookup={lookup} />
      {worker && <Button className="mt-3" tone="quiet" icon="terminal" onClick={() => onOpenWorker(worker.machine_id, worker.session_id)}>
        {t("assistant-taskpanel.raw_session")}
      </Button>}
    </section>
  );
}
