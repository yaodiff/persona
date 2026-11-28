import { t } from "../../i18n";
import { href, onLinkClick, type Route } from "../../lib/router";
import type { Session } from "../../lib/types";

export default function ClosedTaskNotice({ todo, writable }: { todo: NonNullable<Session["completed_todo"]>; writable: boolean }) {
  const route: Route = { name: "todos", id: todo.id };
  return (
    <div role="status" data-completed-todo className="shrink-0 border-b border-line bg-inset px-3 py-2 text-xs text-ink-2">
      {t(todo.cancelled ? "chat-closed.cancelled" : "chat-closed.completed")}{" "}
      <a
        href={href(route)}
        onClick={(event) => onLinkClick(event, route)}
        className="font-medium text-accent-ink underline underline-offset-2"
      >{todo.title}</a>
      {writable && <>{" "}{t("chat-closed.continue")}</>}
    </div>
  );
}
