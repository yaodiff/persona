// Dialog to link an agent session or project space to an uncompleted todo
import { useEffect, useMemo, useRef, useState } from "react";
import { errorText } from "../lib/api";
import { ago } from "../lib/format";
import { composing } from "../lib/send-history";
import { useAgentSessions } from "../lib/sessions";
import { refresh, refreshTodos, setSpacesSupported, useStore } from "../lib/store";
import { projectNames, useTodoProjects } from "../lib/todo-projects";
import {
  createTodo,
  isLegacySpacesRouteError,
  linkAgentToTodo,
  linkSpaceToTodo,
  makeSessionLookup,
  resolveLinkCurrentTodoId,
  shownStatus,
  todoStatus,
  todoStatusLabel,
  TODO_STATUS_INFO,
  TODO_TONE_CLASS,
  unlinkAgentFromTodo,
  unlinkSpaceFromTodo,
  type Todo,
} from "../lib/todos";
import type { Session } from "../lib/types";
import { hasFinePointer } from "../lib/viewport";
import { Button, cx, Dialog, toast, useNow } from "../ui/atoms";
import { Icon } from "../ui/icons";
import { SearchField } from "../ui/SearchField";
import { t } from "../i18n";

/** From this many open to-dos the list gets a search field: its box shows five two-line rows whole (and a peek of the
 *  sixth, so a longer list is seen to scroll), so from six one has to be scrolled to. */
export const LINK_SEARCH_FROM = 6;

/** The open to-dos whose title, notes or project name hold `query` (all of them for an empty one). */
export function matchTodos(todos: readonly Todo[], query: string, project: (slug: string) => string = (slug) => slug): Todo[] {
  const q = query.trim().toLocaleLowerCase();
  if (!q) return [...todos];
  return todos.filter((todo) => [todo.title, todo.notes ?? "", todo.project ? project(todo.project) : ""].some((text) => text.toLocaleLowerCase().includes(q)));
}

export interface LinkTargetSpace {
  machineId: string;
  space: string;
}

export function getLinkDialogTargetKey(space?: LinkTargetSpace, session?: Session): string {
  if (space) return `sp:${space.machineId}/${space.space}`;
  if (session) return `s:${session.machine_id}/${session.session_id}`;
  return "";
}

export function LinkTodoDialog({
  open,
  session,
  space,
  onClose,
}: {
  open: boolean;
  session?: Session;
  space?: LinkTargetSpace;
  onClose: () => void;
}) {
  const todos = useStore((s) => s.todos);
  const uncompleted = useMemo(() => todos.filter((todo) => !todo.done_at), [todos]);
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newTitle, setNewTitle] = useState("");
  // Cancelling a new to-do puts the keyboard back on "New to-do…", not on the page under the dialog.
  const refocusNew = useRef(false);
  const cancelCreating = () => {
    refocusNew.current = true;
    setCreating(false);
  };

  // Reset dialog state when opened or when target changes (#874 item 3)
  const targetKey = getLinkDialogTargetKey(space, session);
  useEffect(() => {
    if (open) {
      setCreating(false);
      setNewTitle("");
      setBusy(false);
    }
  }, [open, targetKey]);

  if (!session && !space) return null;

  // Keep session.todo?.id as source for sessions so controls don't disappear if /todos hasn't loaded (#874 item 4)
  const currentTodoId = resolveLinkCurrentTodoId(space, session, todos);

  const handleSelect = async (todo: Todo) => {
    setBusy(true);
    try {
      if (space) {
        await linkSpaceToTodo(todo.id, space.machineId, space.space);
        await refreshTodos();
        await refresh();
        toast(t("linktododialog.space_linked", { title: todo.title }), "ok");
      } else if (session) {
        await linkAgentToTodo(todo.id, session.machine_id, session.session_id);
        await refreshTodos();
        await refresh();
        toast(t("linktododialog.session_linked", { title: todo.title }), "ok");
      }
      onClose();
    } catch (e) {
      if (space && isLegacySpacesRouteError(e)) {
        setSpacesSupported(false);
        toast(t("linktododialog.space_unsupported"), "error");
      } else {
        toast(space ? t("linktododialog.space_link_failed", { error: errorText(e) }) : t("linktododialog.session_link_failed", { error: errorText(e) }), "error");
      }
      void refreshTodos();
    } finally {
      setBusy(false);
    }
  };

  const handleUnlink = async () => {
    if (!currentTodoId) return;
    setBusy(true);
    try {
      if (space) {
        await unlinkSpaceFromTodo(currentTodoId, space.machineId, space.space);
        await refreshTodos();
        await refresh();
        toast(t("linktododialog.space_unlinked"), "ok");
      } else if (session) {
        await unlinkAgentFromTodo(currentTodoId, session.machine_id, session.session_id);
        await refreshTodos();
        await refresh();
        toast(t("linktododialog.session_unlinked"), "ok");
      }
      onClose();
    } catch (e) {
      if (space && isLegacySpacesRouteError(e)) {
        setSpacesSupported(false);
      }
      toast(space ? t("linktododialog.space_unlink_failed", { error: errorText(e) }) : t("linktododialog.session_unlink_failed", { error: errorText(e) }), "error");
      void refreshTodos();
    } finally {
      setBusy(false);
    }
  };

  const handleCreateAndLink = async () => {
    const trimmed = newTitle.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      const created = await createTodo(trimmed);
      try {
        if (space) {
          await linkSpaceToTodo(created.id, space.machineId, space.space);
          toast(t("linktododialog.space_created_linked", { title: created.title }), "ok");
        } else if (session) {
          await linkAgentToTodo(created.id, session.machine_id, session.session_id);
          toast(t("linktododialog.session_created_linked", { title: created.title }), "ok");
        }
      } catch (linkErr) {
        if (space && isLegacySpacesRouteError(linkErr)) {
          setSpacesSupported(false);
          toast(t("linktododialog.created_space_unsupported", { error: errorText(linkErr) }), "error");
        } else {
          toast(space ? t("linktododialog.created_space_link_failed", { error: errorText(linkErr) }) : t("linktododialog.created_session_link_failed", { error: errorText(linkErr) }), "error");
        }
      }
      await refreshTodos();
      await refresh();
      onClose();
    } catch (e) {
      toast(t("linktododialog.create_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  const dialogTitle = space
    ? currentTodoId
      ? t("linktododialog.title_move")
      : t("linktododialog.title_space_link")
    : currentTodoId
      ? t("linktododialog.title_move")
      : t("linktododialog.title_session_link");

  const description = space
    ? t("linktododialog.space_description", { space: space.space })
    : t("linktododialog.session_description", { name: session?.title || session?.session_id || "" });

  return (
    <Dialog open={open} onClose={onClose} title={dialogTitle} width={420}>
      <div className="flex flex-col gap-3">
        <div className="text-[12.5px] text-ink-3">{description}</div>

        {uncompleted.length === 0 ? (
          <div className="py-4 text-center text-meta text-ink-3">{t("linktododialog.empty")}</div>
        ) : (
          <TodoChoices todos={uncompleted} currentTodoId={currentTodoId} busy={busy} onPick={(todo) => void handleSelect(todo)} />
        )}

        {creating ? (
          <div className="flex flex-col gap-2 rounded-control border border-line bg-inset p-2.5">
            <input
              type="text"
              autoFocus
              value={newTitle}
              disabled={busy}
              onChange={(e) => setNewTitle(e.target.value)}
              onKeyDown={(e) => {
                if (composing(e.nativeEvent)) return;
                if (e.key === "Enter") {
                  e.preventDefault();
                  void handleCreateAndLink();
                } else if (e.key === "Escape") {
                  // only the new to-do is cancelled: the dialog, and the list under it, stay for the next Escape
                  e.stopPropagation();
                  cancelCreating();
                }
              }}
              placeholder={t("linktododialog.new_placeholder")}
              className="w-full bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
            />
            <div className="flex items-center justify-end gap-2">
              <Button tone="quiet" className="px-2 py-1 text-[12px] whitespace-nowrap" onClick={cancelCreating} disabled={busy}>
                {t("common.cancel")}
              </Button>
              <Button tone="primary" className="px-2 py-1 text-[12px] whitespace-nowrap" onClick={() => void handleCreateAndLink()} disabled={busy || !newTitle.trim()}>
                {t("linktododialog.create_and_link")}
              </Button>
            </div>
          </div>
        ) : (
          <button
            type="button"
            disabled={busy}
            ref={(el) => {
              if (el && refocusNew.current) {
                refocusNew.current = false;
                el.focus();
              }
            }}
            onClick={() => {
              setNewTitle("");
              setCreating(true);
            }}
            className="flex items-center gap-1.5 rounded-control px-2 py-1.5 text-left text-[12.5px] font-medium text-accent hover:bg-hover whitespace-nowrap"
          >
            <Icon name="plus" size={12} stroke={2.5} />
            <span>{t("linktododialog.new_todo")}</span>
          </button>
        )}

        <div className="mt-2 flex justify-between gap-2">
          {currentTodoId ? (
            <Button tone="quiet" onClick={handleUnlink} disabled={busy} className="text-red hover:bg-red-tint/50 whitespace-nowrap">
              {space ? t("linktododialog.space_unlink") : t("linktododialog.session_unlink")}
            </Button>
          ) : (
            <div />
          )}
          <Button tone="secondary" onClick={onClose} className="whitespace-nowrap">
            {t("common.cancel")}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}

/** The open to-dos to pick from. Each row says, under its title, where the to-do stands (the To-dos page's word), its
 *  project and when it last changed, so two with one title can be told apart; a long list gets a search field. */
function TodoChoices({ todos, currentTodoId, busy, onPick }: { todos: Todo[]; currentTodoId?: string; busy: boolean; onPick: (todo: Todo) => void }) {
  const sessions = useAgentSessions();
  const lookup = useMemo(() => makeSessionLookup(sessions), [sessions]);
  const project = projectNames(useTodoProjects());
  const now = useNow(60_000);
  const [query, setQuery] = useState("");
  const searchable = todos.length >= LINK_SEARCH_FROM;
  const shown = searchable ? matchTodos(todos, query, project) : todos;
  return (
    <>
      {searchable && (
        <SearchField
          tone="inset"
          autoFocus={hasFinePointer()}
          value={query}
          onChange={setQuery}
          onKeyDown={(e) => {
            // Escape empties the search first (the dialog stays); with nothing typed it reaches the dialog and closes it
            if (e.key !== "Escape" || !query || composing(e.nativeEvent)) return;
            e.stopPropagation();
            setQuery("");
          }}
          placeholder={t("linktododialog.search")}
        />
      )}
      {shown.length === 0 ? (
        <div className="py-4 text-center text-meta text-ink-3">{t("linktododialog.no_match")}</div>
      ) : (
        <div className="flex max-h-72 flex-col gap-1 overflow-y-auto rounded-control border border-line p-1" data-link-todos>
          {shown.map((todo) => {
            const isCurrent = todo.id === currentTodoId;
            const status = todoStatus(todo, lookup);
            const tone = TODO_STATUS_INFO[shownStatus(todo, status)].tone;
            const where = [todo.project ? project(todo.project) : "", ago(todo.updated_at, now, "narrow")].filter(Boolean);
            return (
              <button
                key={todo.id}
                type="button"
                disabled={busy}
                onClick={() => onPick(todo)}
                className={cx(
                  "flex w-full items-center gap-2 rounded px-2.5 py-1.5 text-left transition-colors whitespace-nowrap",
                  isCurrent ? "bg-accent-tint text-accent" : "hover:bg-hover text-ink",
                )}
              >
                <span className="min-w-0 flex-1">
                  <span className={cx("block truncate text-meta", isCurrent && "font-medium")}>{todo.title}</span>
                  <span className="block truncate text-xs text-ink-3">
                    <span className={TODO_TONE_CLASS[tone]}>{todoStatusLabel(todo, status)}</span>
                    {where.map((part) => ` · ${part}`).join("")}
                  </span>
                </span>
                {isCurrent && <Icon name="check" size={13} stroke={2.5} />}
              </button>
            );
          })}
        </div>
      )}
    </>
  );
}
