// An agent's row in a list of a machine's or a space's agents: its state, title, tab and latest line; opens its chat.
import { t } from "../../i18n";
import { sessionPreview, sessionTitle, activityOf, stateOf } from "../../lib/format";
import { agentMeta, loudState } from "../../lib/machine-detail";
import { plainMarkdown } from "../../lib/plain-markdown";
import { href, onLinkClick } from "../../lib/router";
import type { Session } from "../../lib/types";
import { StatePill } from "../../ui/atoms";
import { StatusMark } from "../../ui/StatusMark";
import { Icon } from "../../ui/icons";

export function AgentRow({ s }: { s: Session }) {
  const todo = s.todo;
  const st = activityOf(s);
  const phase = stateOf(s);
  const to = { name: "session" as const, mid: s.machine_id, sid: s.session_id };
  const meta = agentMeta(s);
  // Markdown on one line, without its marks (#10939)
  const preview = plainMarkdown(sessionPreview(s));
  return (
    <div className="flex flex-col sm:flex-row sm:items-center">
      <a href={href(to)} onClick={(e) => onLinkClick(e, to)} className="group flex min-h-11 min-w-0 flex-1 items-center gap-3 px-4 py-2.5 transition-colors hover:bg-hover">
        <span className="flex size-4 shrink-0 items-center justify-center">
          <StatusMark state={phase} />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-baseline gap-2">
            <span className="truncate text-[13px] font-medium text-ink">{sessionTitle(s)}</span>
            {meta && <span className="hidden shrink-0 font-mono text-[11px] text-ink-3 sm:inline">{meta}</span>}
          </span>
          {(preview || meta) && (
            <span className="mt-0.5 block truncate text-[12px] text-ink-3">
              {meta && <span className="font-mono text-[11px] sm:hidden">{meta}{preview ? " · " : ""}</span>}
              {preview}
            </span>
          )}
        </span>
        {(phase === "error" || loudState(st)) && <StatePill state={phase} />}
        <Icon name="chevronRight" size={13} stroke={2.2} className="shrink-0 text-ink-3 transition-transform duration-150 group-hover:translate-x-0.5" />
      </a>
      {todo?.id && (
        <a href={href({ name: "todos", id: todo.id })} onClick={(e) => onLinkClick(e, { name: "todos", id: todo.id })}
          className="mx-4 inline-flex min-h-11 min-w-0 items-center gap-1 self-start rounded-control px-2 text-xs text-accent-ink hover:bg-hover sm:mx-2 sm:max-w-[35%] sm:self-center"
          aria-label={t("machines-alerts.handoff_todo", { title: todo.title })}
          title={t("machines-alerts.handoff_todo", { title: todo.title })}>
          <Icon name="todo" size={14} className="shrink-0" />
          <span className="truncate">{todo.title || t("space-page.todos")}</span>
        </a>
      )}
    </div>
  );
}
