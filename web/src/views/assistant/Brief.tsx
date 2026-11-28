// The `brief` item of a morning or evening routine's conversation: the fixed part of the run (what waits for the user,
// what happened in the last hours, what is to be verified), as the hub worked it out when the run started; the same
// rows as the Lark card, with an Accept button for each task to verify and a Handled button for each question that can wait
// (docs/extensions/bots.md "Morning and evening briefings").
import { VerificationFollowUp } from "./VerificationFollowUp";
import { EvidenceLink } from "./EvidenceLink";
import { leaveChat, openAgent, openTask } from "../../lib/handoff";
import { conversationRoute } from "../../lib/conversation-page";
import { type ReactNode, Suspense, useRef, useState } from "react";
import { lazyPage } from "../../lib/lazy-page";
import { errorText } from "../../lib/api";
import { getState, putTodo, refreshTodos } from "../../lib/store";
import { updateTodo, type Todo } from "../../lib/todos";
import { briefBody } from "../../lib/brief-rows";
import { acceptBrief, failedKey, handledLater, type BriefBody, type BriefRow, type FeedItem } from "../../lib/feed";
import { safeWebURL, shortDuration } from "../../lib/format";
import { formatDate, t } from "../../i18n";
import { Button, cx, toast } from "../../ui/atoms";
import { Icon, type IconName } from "../../ui/icons";
import { TodoStageBar } from "../TodoStageBar";

const BriefSetupRows = lazyPage(() => import("./BriefSetup").then((m) => m.BriefSetupRows));

/** A section's rows shown at once (the hub cuts the lists as its card does; the totals say how many there were). */
export function Section({ icon, tint, title, children }: { icon: IconName; tint: string; title: string; children: React.ReactNode }) {
  return (
    <section className="border-t border-line py-2 first:border-t-0" data-brief-section>
      <div role="heading" aria-level={3} className="mb-1 flex items-center gap-2 text-[12.5px] font-medium text-ink-2">
        <span className={cx("flex size-6 items-center justify-center rounded-control", tint)}>
          <Icon name={icon} size={13} />
        </span>
        {title}
      </div>
      <div className="flex flex-col">{children}</div>
    </section>
  );
}

function More({ n, text = "assistant-brief.more" }: { n: number; text?: string }) {
  return n > 0 ? <div className="py-1 text-label text-ink-2">{t(text, { n })}</div> : null;
}

/** A link the hub gave, if it is a safe web address: anything else is not drawn as one. */
function webLink(link?: string): string | undefined {
  return safeWebURL(link);
}

/** Opens the chat a waiting or later row came from in the app (#12055), as iOS and Android do (#2320): an Agent's as the
 *  task rows open theirs (How Agents open: the side panel, or its page with the way back to this conversation), a
 *  Persona conversation as its page. */
function openChat(mid: string, sid: string) {
  const s = getState();
  const to = conversationRoute(mid, sid, s.sessions, s.assistant);
  if (to.name === "session") openAgent(mid, sid);
  else leaveChat(to, false);
}

/** Open on a waiting or later row: the chat it came from, in the app; from a hub that does not name it, the row's link
 *  in a new tab, as before. */
function RowOpen({ row }: { row: BriefRow }) {
  const title = t("assistant-brief.open_row", { title: row.title });
  const { machine_id: mid, session_id: sid } = row;
  if (mid && sid) return <Button title={title} onClick={() => openChat(mid, sid)}>{t("assistant-brief.open")}</Button>;
  const link = webLink(row.link);
  return link ? <Button href={link} title={title}>{t("assistant-brief.open")}</Button> : null;
}

/** What a row's meta line is made of: numbers and text only. A row typed as BriefRow cannot carry an object here: a
 *  to-do's `ask.from` is one, and spreading the ask into a row is a type error (#11794); rows off the wire go through
 *  lib/feed `briefRow`, which drops a field that is not text. */
export function meta(row: Pick<BriefRow, "waited_s" | "nudged" | "from">): string {
  const parts: string[] = [t("assistant-brief.waited", { wait: shortDuration(row.waited_s * 1000) })];
  if (row.nudged) parts.push(t("assistant-brief.nudged"));
  if (row.from) parts.push(row.from);
  return parts.join(" · ");
}

function WaitingRow({ row }: { row: BriefRow }) {
  return (
    <div className="@container" data-brief-row="waiting">
      {/* Narrow: the words take the whole width and the link drops under them, as in the Accept row (#5569) */}
      <div className="flex items-center gap-3 py-1.5 @max-md:flex-wrap @max-md:gap-y-2">
        <div className="min-w-0 flex-1 @max-md:w-full @max-md:flex-none">
          <div className="text-[13.5px] font-medium [overflow-wrap:anywhere]">{row.title}</div>
          <div className="text-label text-ink-2">{meta(row)}</div>
        </div>
        <RowOpen row={row} />
      </div>
    </div>
  );
}

/** What the Accept button of a task says: it asked, the hub took it, or the round is already over. */
type Accepting = "idle" | "busy" | "finishing" | "finished" | "accepted" | "recorded" | "handled";

/** What the button of a row to verify says: Accept a deployed task, Got it a conclusion, Take recommendation a decision. */
function acceptLabel(row: BriefRow): { label: string; title: string } {
  if (row.outcome === "decide") return { label: t("assistant-brief.take"), title: t("assistant-brief.take_row", { title: row.title }) };
  if (row.outcome) return { label: t("assistant-brief.got_it"), title: t("assistant-brief.accept_row", { title: row.title }) };
  return { label: t("assistant-brief.accept"), title: t("assistant-brief.accept_row", { title: row.title }) };
}

export function AcceptRow({ row, verification, stacked = false, admission, onBusyChange, blocked = false, secondary, hideTitle = false }: { row: BriefRow; verification?: Todo; stacked?: boolean; admission?: { current: boolean }; onBusyChange?: (busy: boolean) => void; blocked?: boolean; secondary?: ReactNode; hideTitle?: boolean }) {
  const [state, setState] = useState<Accepting>("idle");
  // Admit either action synchronously, before React disables the other button.
  const ownAdmission = useRef(false);
  const admitted = admission ?? ownAdmission;
  const button = acceptLabel(row);
  // the primary button (and the settled word) on its own full-width line while the row is narrow
  const full = stacked ? "w-full" : "@max-md:w-full";
  const deciding = row.outcome === "decide";
  const settled = state === "accepted" || state === "recorded" || state === "handled" || state === "finished";
  // A decision with nothing recommended can still be finished, but has nothing to take.
  const takes = !deciding || !!row.recommend;
  const accept = () => {
    if (!row.asked_at || admitted.current || state !== "idle") return;
    admitted.current = true;
    onBusyChange?.(true);
    setState("busy");
    acceptBrief(row.id, row.asked_at).then(
      (res) => setState(res),
      (e) => {
        admitted.current = false;
        onBusyChange?.(false);
        setState("idle");
        toast(t("assistant-brief.accept_failed", { error: errorText(e) }), "error");
      },
    );
  };
  const finish = () => {
    if (!stacked || !row.asked_at || admitted.current || state !== "idle") return;
    admitted.current = true;
    onBusyChange?.(true);
    setState("finishing");
    // User completion only: unlike /accept, PATCH never routes the recommendation to the Agent.
    updateTodo(row.id, { done: true }).then(
      (todo) => {
        setState("finished");
        // putTodo merges optional fields; explicitly clear the ask omitted by a completed todo.
        putTodo({ ...todo, ask: todo.ask });
        void refreshTodos();
        toast(t("assistant-brief.finished"), "ok");
      },
      (e) => {
        admitted.current = false;
        onBusyChange?.(false);
        setState("idle");
        toast(t("assistant-brief.finish_failed", { error: errorText(e) }), "error");
      },
    );
  };
  return (
    <div className="@container" data-brief-row="accept">
    {/* Narrow (the tray, a phone's chat column; `stacked` always): the words take the whole width, the secondary buttons
        one row under them and the primary its own full-width line, so the words are never a four-letter column (#5569). */}
    <div className={cx("flex items-center", stacked ? "flex-wrap gap-x-2 gap-y-3 py-0.5" : "gap-3 py-1.5 @max-md:flex-wrap @max-md:gap-y-2")}>
      {/* stacked, top to bottom: the task, what is asked, what is recommended, how long it has waited; the buttons
          follow after a larger gap, so the words and the actions read as two groups */}
      <div className={cx("min-w-0", stacked ? "flex w-full flex-col gap-1.5" : "flex-1 @max-md:w-full @max-md:flex-none")}>
        {!hideTitle && <div className={cx("font-medium [overflow-wrap:anywhere]", stacked ? "text-sm font-semibold" : "text-[13.5px]")}>{row.title}</div>}
        {row.verdict && (
          <div className={cx("[overflow-wrap:anywhere]", stacked ? "text-sm leading-relaxed" : "text-[12.5px]")} data-brief-verdict={row.outcome ?? "deployed"}>
            {row.verdict}
          </div>
        )}
        {row.detail && <div className={cx("text-label text-ink-2 [overflow-wrap:anywhere]", !row.outcome && "line-clamp-4 leading-relaxed")} title={row.detail}>{row.detail}</div>}
        {!row.outcome && <EvidenceLink verification link={row.evidence_link} todoId={row.id} />}
        {row.recommend && <div className="text-label text-ink-2 [overflow-wrap:anywhere]" data-brief-recommend>{t("assistant-brief.recommend", { option: row.recommend })}</div>}
        <div className={cx("text-label", stacked ? "text-ink-3" : "text-ink-2")} data-brief-meta>{meta(row)}</div>
      </div>
      <Button title={t("assistant-brief.open_row", { title: row.title })} onClick={() => openTask(row.id)}>
        {t("assistant-brief.open")}
      </Button>
      {/* Only the live Pending row can finish a task; historical brief snapshots must not end a newer run. */}
      {stacked && deciding && !settled && (
        <Button busy={state === "finishing"} disabled={blocked || !row.asked_at || state === "busy"} title={t("assistant-brief.finish_row", { title: row.title })} onClick={finish}>
          {t("assistant-brief.finish")}
        </Button>
      )}
      {!settled && verification && <VerificationFollowUp todo={verification} disabled={blocked || state !== "idle"} />}
      {!settled && secondary}
      {settled ? (
        <span className={cx("shrink-0 text-[12.5px] text-ink-2", full)} data-brief-accepted={state}>
          {t(state === "finished" ? "assistant-brief.finished" : state === "handled" ? "assistant-brief.handled" : state === "recorded" ? "assistant-brief.recorded_not_delivered" : deciding ? "assistant-brief.taken" : row.outcome ? "assistant-brief.noted" : "assistant-brief.accepted")}
        </span>
      ) : takes ? (
        <Button tone="primary" className={full} busy={state === "busy"} disabled={blocked || !row.asked_at || state === "finishing"} title={button.title} onClick={accept}>
          {button.label}
        </Button>
      ) : null}
    </div>
    </div>
  );
}

/** What the Handled button of a question that can wait says: idle, the hub took it, or it was not in the queue any more. */
type Marking = "idle" | "busy" | "marked" | "handled";

function LaterRow({ row, conversation }: { row: BriefRow; conversation: string }) {
  const [state, setState] = useState<Marking>("idle");
  const mark = () => {
    if (state !== "idle") return;
    setState("busy");
    handledLater(row.id, conversation).then(
      (res) => setState(res),
      (e) => {
        setState("idle");
        toast(t("assistant-brief.later_failed", { error: String(e instanceof Error ? e.message : e) }), "error");
      },
    );
  };
  return (
    <div className="flex items-center gap-3 py-1.5" data-brief-row="later">
      <div className="min-w-0 flex-1">
        <div className="text-[13.5px] font-medium [overflow-wrap:anywhere]">{row.title}</div>
        <div className="text-label text-ink-2">{meta(row)}</div>
      </div>
      <RowOpen row={row} />
      {state === "marked" || state === "handled" ? (
        <span className="shrink-0 text-[12.5px] text-ink-2" data-brief-marked={state}>
          {t(state === "marked" ? "assistant-brief.later_marked" : "assistant-brief.handled")}
        </span>
      ) : (
        <Button busy={state === "busy"} title={t("assistant-brief.later_done_row", { title: row.title })} onClick={mark}>
          {t("assistant-brief.later_done")}
        </Button>
      )}
    </div>
  );
}

function clock(iso: string): string {
  return formatDate(iso, { hour: "2-digit", minute: "2-digit" });
}

export function BriefCard({ item }: { item: FeedItem }) {
  const body: BriefBody | undefined = briefBody(item);
  if (!body) return null;
  const past = body.failed.length + body.done.length;
  return (
    <div className="flex flex-col rounded-card bg-surface p-4 shadow-card" data-testid="brief-card">
      <div className="mb-2 flex items-center gap-1.5 text-label font-medium text-ink-2">
        <Icon name="sun" size={13} />
        {body.title || t("assistant-brief.title")}
      </div>

      {body.waiting_unknown ? (
        <Section icon="hand" tint="bg-field text-attention" title={t("assistant-brief.waiting_head", { n: "?" })}>
          <div className="py-1 text-meta text-ink-2">{t("assistant-brief.waiting_unknown")}</div>
        </Section>
      ) : (
        <Section icon="hand" tint="bg-field text-attention" title={t("assistant-brief.waiting_head", { n: body.waiting.length })}>
          {body.waiting.length === 0 && <div className="py-1 text-meta text-ink-2">{t("assistant-brief.none")}</div>}
          {body.waiting.map((r) => (
            <WaitingRow key={r.id} row={r} />
          ))}
        </Section>
      )}

      <Section icon="clock" tint="bg-accent-tint text-accent" title={t("assistant-brief.past_head", { hours: body.hours, done: body.done_total, failed: body.failed_total })}>
        {past === 0 && <div className="py-1 text-meta text-ink-2">{t("assistant-brief.none")}</div>}
        {body.failed.map((x) => (
          <div key={`${x.kind}-${x.id}-${x.at}`} className="flex items-start gap-2 py-1 text-meta" data-brief-row="failed">
            <Icon name="stop" size={11} className="mt-1.5 shrink-0 text-attention" />
            <div className="min-w-0 flex-1 [overflow-wrap:anywhere]">
              <span className="font-medium">{x.title}</span> · {t(failedKey(x), { time: clock(x.at) })}
              {x.why && <span className="text-ink-2"> — {x.why}</span>}
            </div>
          </div>
        ))}
        {body.done.map((x) => (
          <div key={x.id} className="flex items-start gap-2 py-1 text-meta" data-brief-row="done">
            <Icon name="check" size={12} className="mt-1 shrink-0 text-green" />
            <div className="min-w-0 flex-1 [overflow-wrap:anywhere]">
              <span className="font-medium">{x.title}</span> · {clock(x.at)}
            </div>
          </div>
        ))}
        <More n={body.done_total + body.failed_total - past} />
      </Section>

      <Section icon="check" tint="bg-green-tint text-green" title={t("assistant-brief.accept_head", { n: body.accept.length })}>
        {body.accept.length === 0 && <div className="py-1 text-meta text-ink-2">{t("assistant-brief.none")}</div>}
        {body.accept.map((r) => (
          <AcceptRow key={r.id + (r.asked_at ?? "")} row={r} />
        ))}
      </Section>
      {body.later.length > 0 && (
        <Section icon="chat" tint="bg-field text-ink-2" title={t("assistant-brief.later_head", { n: body.later_total })}>
          {body.later.map((r) => (
            <LaterRow key={r.id} row={r} conversation={item.conversation} />
          ))}
          <More n={body.later_total - body.later.length} text="assistant-brief.later_more" />
        </Section>
      )}
      {body.deferred_supervision.length > 0 && (
        <Section icon="list" tint="bg-field text-ink-2" title={t("assistant-brief.supervision_deferred")}>
          {body.deferred_supervision.map((row) => (
            <div key={row.id} className="flex items-start gap-2 py-1 text-meta">
              <div className="min-w-0 flex-1 [overflow-wrap:anywhere]">
                <div className="font-medium">{row.title}</div>
                <div className="text-ink-2">{row.reason}</div>
              </div>
              <Button onClick={() => openTask(row.todo_id)}>{t("assistant-brief.open")}</Button>
            </div>
          ))}
        </Section>
      )}
      {body.contributions.map((entry) => (
        <Section key={entry.extension_id + ":" + entry.contribution_id} icon="list" tint="bg-field text-ink-2" title={entry.title}>
          <div className="space-y-1 py-1 text-meta [overflow-wrap:anywhere]">
            {entry.facts.map((fact, i) => <p key={i}>{fact}</p>)}
            {entry.sections.map((section, i) => (
              <div key={i}>
                <h4 className="font-medium">{section.title}</h4>
                <p className="whitespace-pre-line text-ink-2">{section.body}</p>
              </div>
            ))}
          </div>
        </Section>
      ))}
      {body.progress_total > 0 && (
        <Section icon="gear" tint="bg-orange-tint text-orange" title={t("assistant-brief.progress_head", { n: body.progress_total })}>
          {body.progress.map((p) => (
            <div key={p.id} className="flex flex-col gap-1 py-1.5" data-brief-row="progress">
              <div className="flex items-center gap-3">
                <div className="min-w-0 flex-1 text-[13.5px] font-medium [overflow-wrap:anywhere]">{p.title}</div>
                <Button onClick={() => openTask(p.id)}>{t("assistant-brief.open")}</Button>
              </div>
              <TodoStageBar stage={p.stage} stageKind={p.stage_kind} />
            </div>
          ))}
          <More n={body.progress_total - body.progress.length} />
        </Section>
      )}
      {/* The week's recommended setup (#5085): the hub's own counts and words, drawn as they came. Opening the settings
          page is all a row does: nothing is turned on from here. At most once a week, so its own chunk (#11794). */}
      {body.setup.length > 0 && <Suspense fallback={null}><BriefSetupRows setup={body.setup} /></Suspense>}
    </div>
  );
}
