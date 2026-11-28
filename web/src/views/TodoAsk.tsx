// What a to-do waits on from the user (#2056, docs/assistant/todos.md "Waiting for the owner"), where the to-do is shown:
// the verdict line its Lark card says, the option a decision recommends, and one button that answers it through
// POST /todos/{id}/accept, the route the briefing's Accept takes (Brief.tsx) — OK takes a decision's recommendation, Got it
// acknowledges a conclusion, Accept verifies a deployed to-do. A decision that recommends nothing has no button: it is
// answered in the conversation the task came from.
import { VerificationFollowUp } from "./assistant/VerificationFollowUp";
import { EvidenceLink } from "./assistant/EvidenceLink";
import { useRef, useState } from "react";
import { t } from "../i18n";
import { errorText } from "../lib/api";
import { acceptBrief, setRoute } from "../lib/feed";
import { conversationPage } from "../lib/conversation-page";
import { navigate } from "../lib/router";
import { stateName } from "../lib/state-labels";
import { refreshTodos } from "../lib/store";
import { askTake, askTone, openAsk, type AskTake, type Todo, type TodoAsk } from "../lib/todos";
import { Button, cx, toast, type ToastAction } from "../ui/atoms";
import { Icon, type IconName } from "../ui/icons";

/** Where answering one round stands: not yet, sent, the hub took it, or that round was already answered (409). */
export type AskTaking = "idle" | "busy" | "taken" | "recorded" | "handled";

/** Optional integrations for a panel that owns the ask's actions and feedback instead of using the default singletons. */
export interface TodoAskControls {
  onAccept?: (todoId: string, askedAt: string) => ReturnType<typeof acceptBrief>;
  onOpenConversation?: (todo: Todo, ask: TodoAsk) => void;
  onRefresh?: () => void | Promise<void>;
  /** `resend`: on a failure, the action that answers the same round again (#17431). */
  onNotice?: (message: string, tone: "ok" | "error", resend?: ToastAction) => void;
}

export interface TodoAskLineProps extends TodoAskControls {
  todo: Todo;
  className?: string;
}

export interface TodoAskPanelProps extends TodoAskControls {
  todo: Todo;
}

const TONE: Record<ReturnType<typeof askTone>, string> = { attention: "text-attention", red: "text-red", green: "text-green" };
const ICON: Record<ReturnType<typeof askTone>, IconName> = { attention: "hand", red: "info", green: "check" };

/** The button's word: short on a row or a card, the whole action on the to-do's page. */
function takeLabel(what: AskTake, short: boolean): string {
  switch (what) {
    case "take":
      return short ? t("todos.ask_ok") : t("assistant-brief.take");
    case "ack":
      return t("assistant-brief.got_it");
    case "accept":
      return t("assistant-brief.accept");
  }
}

/** What the button says it does, for a screen reader and a hover. */
function takeTitle(what: AskTake, title: string): string {
  return what === "take" ? t("assistant-brief.take_row", { title }) : t("assistant-brief.accept_row", { title });
}

/** What stands in for the button once the round is answered. */
function takenText(what: AskTake, taking: AskTaking): string {
  if (taking === "handled") return t("assistant-brief.handled");
  if (taking === "recorded") return t("assistant-brief.recorded_not_delivered");
  return what === "take" ? t("assistant-brief.taken") : what === "ack" ? t("assistant-brief.noted") : t("assistant-brief.accepted");
}

/**
 * Answers one round of a to-do's ask, once. A round already answered (here, on another device, on its Lark card) is
 * "handled", and the to-dos are read again so its stale ask goes; a failure (the recommendation did not reach the agent,
 * the hub could not save) says why and leaves the button to try again. A newer round starts over.
 */
export function useTakeAsk(
  todo: Pick<Todo, "id">,
  ask: TodoAsk,
  { onAccept = acceptBrief, onRefresh = refreshTodos, onNotice = toast }: TodoAskControls = {},
): { taking: AskTaking; take: () => void } {
  const [state, setState] = useState<{ round: string; taking: AskTaking }>({ round: "", taking: "idle" });
  const taking = state.round === ask.asked_at ? state.taking : "idle";
  const what = askTake(ask);
  // One answer on its way at a time, from the button or the failure's Resend alike: never two for one round.
  const running = useRef(false);
  const current = useRef(ask.asked_at);
  current.current = ask.asked_at;
  const failed = (e: unknown, resend?: ToastAction) => {
    const text = t("todos.ask_failed", { error: errorText(e) });
    if (resend) onNotice(text, "error", resend);
    else onNotice(text, "error");
  };
  const refresh = async () => {
    try {
      await onRefresh();
    } catch (e) {
      failed(e); // the answer already succeeded: keep it settled and report only the stale list
    }
  };
  const take = async () => {
    if (running.current || taking !== "idle" || !what) return;
    running.current = true;
    const round = ask.asked_at;
    setState({ round, taking: "busy" });
    try {
      const res = await onAccept(todo.id, round);
      setState({ round, taking: res === "accepted" ? "taken" : res });
      onNotice(takenText(what, res === "accepted" ? "taken" : res), "ok");
      // Controlled callbacks own the reread; the all-default path needs one only for an earlier round.
      if (onRefresh !== refreshTodos || onAccept !== acceptBrief || res === "handled") void refresh();
    } catch (e) {
      setState({ round, taking: "idle" });
      // the choice stays: the button works again, and the failure offers to send this round once more
      failed(e, { label: t("todos.ask_resend"), run: () => void resend(round) });
    } finally {
      running.current = false;
    }
  };
  // Resend from the failure: the same round only, while nothing else for it is on its way.
  const resend = (round: string) => {
    if (round === current.current) void take();
  };
  return { taking, take };
}

/** The ask on a to-do's row, a task's card or the task panel's banner: its verdict, the recommendation, the mark of a
 *  decision held for the briefing (#5803), and the button as a link. Its clicks never open the row. Nothing when the
 *  to-do waits on nothing. */
export function TodoAskLine({ todo, className, ...controls }: TodoAskLineProps) {
  const ask = openAsk(todo);
  return ask ? <AskLine todo={todo} ask={ask} className={className} {...controls} /> : null;
}

function AskLine({ todo, ask, className, ...controls }: TodoAskLineProps & { ask: TodoAsk }) {
  const { taking, take } = useTakeAsk(todo, ask, controls);
  const what = askTake(ask);
  const tone = askTone(ask);
  const recommend = ask.recommend ? t("assistant-brief.recommend", { option: ask.recommend }) : "";
  return (
    <div data-testid="todo-ask" data-ask={ask.outcome || "deployed"} className={cx("flex min-w-0 items-start gap-2 text-[12px]", className)}>
      {/* the verdict starts with its own mark (✅ ⛔ 🤔, as its Lark card says it): no icon beside it */}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className={cx("truncate font-medium", TONE[tone])} title={ask.verdict}>
          {ask.verdict}
        </span>
        {recommend && (
          <span className="truncate text-ink-2" title={recommend}>
            {recommend}
          </span>
        )}
        {/* held for the briefing (#5803): why it did not ring; what that means on hover, written out on the to-do's page */}
        {ask.for_briefing === true && (
          <span data-for-briefing="mark" title={t("todos.ask_held_hint")} className="flex min-w-0 items-center gap-1 text-ink-2">
            <Icon name="clock" size={11} className="shrink-0" />
            <span className="truncate">{t("todos.ask_held")}</span>
          </span>
        )}
      </span>
      {what &&
        (taking === "taken" || taking === "recorded" || taking === "handled" ? (
          <span data-ask-done={taking} className="shrink-0 whitespace-nowrap text-ink-2">
            {takenText(what, taking)}
          </span>
        ) : (
          <button
            type="button"
            disabled={taking === "busy"}
            aria-busy={taking === "busy" || undefined}
            aria-label={takeTitle(what, todo.title)}
            title={takeTitle(what, todo.title)}
            onClick={(e) => {
              e.stopPropagation();
              take();
            }}
            className="touch-hit inline-flex shrink-0 items-center gap-1 whitespace-nowrap font-semibold text-accent hover:underline disabled:opacity-50 disabled:hover:no-underline"
          >
            <Icon name="check" size={11} stroke={2.4} />
            {takeLabel(what, true)}
          </button>
        ))}
    </div>
  );
}

/** The ask on a to-do's page: what it waits on in full and how to answer it — the button, and for a decision the
 *  conversation it came from, where it can be answered another way. */
export function TodoAskPanel({ todo, ...controls }: TodoAskPanelProps) {
  const ask = openAsk(todo);
  return ask ? <AskPanel todo={todo} ask={ask} {...controls} /> : null;
}

function openConversation(todo: Todo, ask: TodoAsk) {
  setRoute(todo.origin!, todo.id, ask.asked_at);
  navigate(conversationPage(todo.origin!, undefined, todo.manager));
}

function AskPanel({ todo, ask, onOpenConversation = openConversation, ...controls }: TodoAskPanelProps & { ask: TodoAsk }) {
  const { taking, take } = useTakeAsk(todo, ask, controls);
  const what = askTake(ask);
  const tone = askTone(ask);
  const decide = ask.outcome === "decide";
  return (
    <section
      aria-label={stateName("needs_you")}
      data-testid="todo-ask-panel"
      data-ask={ask.outcome || "deployed"}
      className={cx("mt-4 min-w-0 rounded-control bg-inset p-3.5 shadow-hairline", decide && "ring-1 ring-attention/40")}
    >
      <div className="flex items-center gap-2">
        <Icon name={ICON[tone]} size={13} stroke={2.2} className={TONE[tone]} />
        <span className="text-[12.5px] font-semibold text-ink-2">{stateName("needs_you")}</span>
      </div>
      <p className={cx("mt-2 text-[13.5px] font-medium leading-[1.55] select-text [overflow-wrap:anywhere]", TONE[tone])}>{ask.verdict}</p>
      {!ask.outcome && ask.summary && <p className="mt-1 whitespace-pre-wrap text-sm leading-relaxed text-ink-2 select-text [overflow-wrap:anywhere]" title={ask.summary}>{ask.summary}</p>}
      {!ask.outcome && <EvidenceLink verification link={ask.link} todoId={todo.id} />}
      {ask.recommend && <p className="mt-1 text-[13px] leading-[1.55] text-ink select-text [overflow-wrap:anywhere]">{t("assistant-brief.recommend", { option: ask.recommend })}</p>}
      {!what && <p className="mt-1 text-[12.5px] leading-[1.6] text-ink-3">{t("todos.ask_answer_hint")}</p>}
      {(what || (decide && todo.origin)) && (
        <div className="mt-3 flex flex-wrap items-center gap-2">
          {what &&
            (taking === "taken" || taking === "recorded" || taking === "handled" ? (
              <span data-ask-done={taking} className="text-[12.5px] text-ink-2">
                {takenText(what, taking)}
              </span>
            ) : (
              <Button tone="primary" icon="check" busy={taking === "busy"} title={takeTitle(what, todo.title)} onClick={take}>
                {takeLabel(what, false)}
              </Button>
            ))}
          {taking === "idle" && <VerificationFollowUp todo={todo} />}
          {decide && todo.origin && (
            // the conversation the task came from: the to-do's manager's, whichever assistant is current
            <Button icon="chat" onClick={() => onOpenConversation(todo, ask)}>
              {t("todos.ask_answer")}
            </Button>
          )}
        </div>
      )}
    </section>
  );
}
