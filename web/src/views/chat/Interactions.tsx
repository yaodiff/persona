// What the agent is waiting on (questions, plans, permissions, terminal dialogs, MCP forms and links), as Beautiful UI's Approval Card.
import { Component, createContext, Fragment, Suspense, useCallback, useContext, useEffect, useId, useLayoutEffect, useRef, useState, type ErrorInfo, type ReactNode, type RefObject } from "react";
import { api, ApiError, enc } from "../../lib/api";
import { answerCard, usePendingLine, type AnswerFrom } from "../../lib/card-answer";
import { isCommandPrompt } from "../../lib/command-ask";
import { cardKeyAction, focusComposer } from "../../lib/card-keys";
import { composing } from "../../lib/send-history";
import { EDITABLE } from "../../lib/shortcuts";
import { lazyPage } from "../../lib/lazy-page";
import { mergeRefs } from "../../lib/tap-guard";
import { delegateLines, isDelegating, recommendedLabels, type DelegatedAnswer } from "../../lib/delegate";
import { supervisorHeld } from "../../lib/supervisor-held";
import { Choice, DigitHint } from "./Choice";
import { BUSY_MOTION, spinStyle } from "../../lib/busy-motion";
import { appArchiveDeps, archiveAgent, archiveSession } from "../../lib/agent-archives";
import { isSessionReview } from "../../lib/types";
import type { Interaction } from "../../lib/types";
import type { TurnAsk } from "../../lib/turn-asks";
import { defaults, elicitationDraft, elicitationResolution, elicitationSource, fields, numberInputMode, saveElicitationDraft, urlHost, urlTarget, validate, type ElicitationAction, type Field, type Values } from "../../lib/elicitation";
import { answerLines } from "../../lib/answered";
import { answerBlock, interactionCanAnswer, sessionTitle } from "../../lib/format";
import { modeTitle, PERMISSION_MODES } from "../../lib/session-actions";
import { interactionAsk } from "../../lib/inbox";
import { plainMarkdown } from "../../lib/plain-markdown";
import { cardToolName, mcpServerLabel } from "../../lib/turns";
import { isCommandInput, isSecretHandoff, isSecretRun, optionDisplay, secretHandoffInput, secretRunInput } from "../../lib/tool-detail";
import { RecommendedTag } from "../../ui/RecommendedTag";
import { dropAnswered, getSession, useStore, type AnsweredCard as Answered } from "../../lib/store";
import { useChildRow } from "../../lib/use-child-sessions";
import { isSide } from "../../lib/side-fold";
import { Button, Collapse, cx, EASE, Kbd, Loading, Menu, SECRET_INPUT, toast, useMenu } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { ANSWERED_LIST_MAX, cardFold, cardLine, nextWaiting, PLAN_MAX, showsAnswered } from "./dock";
import { useCardCut } from "./card-cut";
import { answeredShown, answeredTally, keptAnswers, latestAnswer, nextRun, runPosition, tallyDetail, answeredHideAt } from "./card-stack";
import { ScrollFold } from "./fold-scroll";
import { cardState } from "../../lib/card-focus";
import { href, onLinkClick, type Route } from "../../lib/router";
import { t } from "../../i18n";
import { StatusMark } from "../../ui/StatusMark";
import { InteractionMarks, ModuleCard, useCardRenderer } from "../../core/ChatSlots";
import { useComposition } from "../../lib/ime";
import { ErrorNotice } from "../../ui/ErrorNotice";
import { reportBoundary } from "../../lib/error-reports";
import { isChunkError } from "../PageBoundary";
import { useChatCardTapGuard, useChatTapGuardView } from "./card-tap-guard";
import { decisionKey } from "../../lib/decision-pager";
import { DecisionCardContext } from "./decision-card";

/** Answers a card: the chat's cards and the overview's quick answers (Home, #4090) send through this. */
export async function resolve(ix: Interaction, body: Record<string, unknown>, from: AnswerFrom = {}) {
  const r = await answerCard(ix, body, from);
  if (!r.ok) toast(r.error, "error");
  else if (r.already) toast(t("chat-interactions.already_answered"), "info");
  return r.ok;
}

/**
 * A card's own keys while it holds keyboard focus (lib/card-keys.ts decides whose a key is): digits pick options,
 * Enter runs `enter` (the card's main action; an Enter on a focused control stays that control's), Esc hands focus back
 * to the composer and never answers. `focused` is when — and only when — the card's key hints (option digits, the Enter
 * badge) are drawn: a card at rest looks exactly as before. Spread `root` on the card's root element (Card does).
 */
export function useCardKeys({ enter, digit }: { enter?: () => boolean | void; digit?: (n: number) => boolean } = {}) {
  const ref = useRef<HTMLDivElement | null>(null);
  const [focused, setFocused] = useState(false);
  const at = useRef({ enter, digit });
  at.current = { enter, digit };
  // a card this page cannot answer (a disabled fieldset, which stops its buttons but not a plain div's focus) shows no
  // hints: cardKeyAction already leaves its keys alone
  const answerable = () => !ref.current?.closest("fieldset[disabled]");
  const onKeyDown = (e: React.KeyboardEvent) => {
    const act = cardKeyAction(e.nativeEvent, ref.current);
    if (!act) return;
    if (act.kind === "esc") {
      // always this card's: the window's own Esc (ChatView's interrupt of a working agent) must not take it. The
      // composer is where focus goes when there is one to type into; without one (read-only) focus stays put
      e.preventDefault();
      e.stopPropagation();
      focusComposer();
    } else if (act.kind === "digit") {
      if (at.current.digit?.(act.digit)) e.preventDefault();
    } else if (at.current.enter?.() !== false) e.preventDefault();
  };
  return {
    focused,
    root: {
      ref,
      tabIndex: -1,
      "data-card-keys": true,
      onFocus: (e: React.FocusEvent<HTMLDivElement>) => setFocused(e.target === ref.current && answerable()),
      onBlur: (e: React.FocusEvent<HTMLDivElement>) => {
        if (!ref.current?.contains(e.relatedTarget)) setFocused(false);
      },
      onKeyDown,
    },
  };
}

type CardKeys = ReturnType<typeof useCardKeys>;

/**
 * A card waiting on you. No `onDismiss`: it has no "Hand back" (a card no computer's terminal can take back).
 * `looking`: a question the assistant is still deciding on (#544), marked as at work rather than waiting on the user.
 * `busy`: an answer to this card is on its way; hand back is off with the rest of its answers (#1120).
 * `cardKeys`: the card's own keyboard (useCardKeys); without it the card is only reachable by pointer, as before.
 * `shrink-0`: the box above the input scrolls (ChatView's dock), and a card with `overflow-hidden` would otherwise let
 * flex squeeze it below its content — its footer (the very buttons that answer it) out of sight below the fold.
 * `overflow-clip` rather than `overflow-hidden`: it clips to the rounded corners the same, but is not a scroll container,
 * so the footer's `sticky` follows the dock's scrolling and the buttons stay at its bottom edge while the card is taller
 * than the dock. `-bottom-1` cancels the dock's `p-1` (`bottom-0` would leave 4px of the body under the buttons).
 */
/** What modules mark the card with (core/module-host.ts `interaction.asked`): Card draws it in its kicker row. */
const CardMarks = createContext<React.ReactNode>(null);
/** The answer on its way for the card, as a line for its footer ("Sending: Allowed"); empty when none, or for its first
 *  moment online. InteractionCard says so (lib/card-answer.ts usePendingLine, past the card's own state) and disables
 *  every answer meanwhile. */
const SendingAnswer = createContext("");
/** Which child session asks (#5120): "From subagent · <title>" at the top of a card on its parent's page. InteractionCard
 *  says so for every kind but a tool approval, whose body already has that line (PermissionBody). */
const AskedFrom = createContext("");
/** What the card asks, in one line (its command, its question; InteractionCard): what its answer row says it answers
 *  while the rest of the card is out of view above it (card-cut.ts). */
const CardAbout = createContext("");
/** The pending stack's ear for which of its cards are cut off below its box (PendingCards' "N pending"). */
const CutReport = createContext<((key: string, cut: boolean) => void) | null>(null);
/** On the stack's first card while cards are cut off below: how many the stack holds ("N pending", 0 for none). */
const StackCount = createContext(0);
/** On the stack's first card while others wait behind it (PendingCards): folded, where it stands in the run ("2 / 6"),
 *  which opens them all; opened, "Show first only" in the same place, which folds them back. */
const QueueSlot = createContext<{ position: number; total: number; open: boolean; onToggle: () => void } | null>(null);

export function Card({ title, titleContent, kicker, onDismiss, skip, busy, children, footer, looking = false, cardKeys, tapGuard }: { title: string; titleContent?: React.ReactNode; kicker: string; onDismiss?: () => void; skip?: { run: () => void; error?: string }; busy?: boolean; children: React.ReactNode; footer: React.ReactNode; looking?: boolean; cardKeys?: CardKeys; tapGuard?: { ref: RefObject<HTMLDivElement | null> } }) {
  const marks = useContext(CardMarks);
  const sending = useContext(SendingAnswer);
  const from = useContext(AskedFrom);
  // the answer row stuck at the bottom of the box while the card's body is below it: the row says what it answers
  const end = useRef<HTMLDivElement>(null);
  const head = useRef<HTMLDivElement>(null);
  const row = useRef<HTMLDivElement>(null);
  const { cut, titleShown } = useCardCut(end, head, row);
  const about = useContext(CardAbout);
  const stack = useContext(StackCount);
  const report = useContext(CutReport);
  const cutKey = useId();
  useEffect(() => {
    if (!report) return;
    report(cutKey, cut);
    return () => report(cutKey, false);
  }, [report, cutKey, cut]);
  const answers = about && about !== title ? `${title} · ${about}` : title;
  // said while what it answers is out of view: always for a card that asks more than its title (a command, a plan), for
  // one that asks only its title (a question) while the title is out of view too, never a repeat of what is in view
  const says = cut && (answers !== title || !titleShown);
  // brings the whole card into view (its title and command over its answers) and the focus onto it, as the line goes
  const reveal = (e: React.MouseEvent<HTMLButtonElement>) => {
    const card = e.currentTarget.closest<HTMLElement>("[data-pending-card]");
    card?.scrollIntoView({ block: "nearest" });
    card?.focus({ preventScroll: true });
  };
  const queue = useContext(QueueSlot);
  const decision = useContext(DecisionCardContext);
  const noSkip = decision?.skipHint;
  // Hand back leaves the request to the computer's terminal.
  const handBack = onDismiss && (
    <button
      type="button"
      aria-label={t("chat-interactions.hand_back")}
      onClick={onDismiss}
      disabled={busy}
      data-card-answer
      className="touch-hit -ml-1.5 rounded-[6px] px-1.5 py-0.5 text-[12px] font-medium whitespace-nowrap text-ink-3 transition-colors enabled:hover:bg-hover enabled:hover:text-ink disabled:opacity-40"
    >
      {t("chat-interactions.hand_back")}
    </button>
  );
  const bottomHandBack = !decision || (decision.task && decision.interaction.kind === "question");
  const decisionHandBack = Boolean(decision) && !skip && !bottomHandBack;
  return (
    <div {...(cardKeys?.root ?? { tabIndex: -1 })} ref={tapGuard ? mergeRefs(tapGuard.ref, cardKeys?.root?.ref) : cardKeys?.root?.ref} data-pending-card aria-label={title} className="shrink-0 overflow-clip rounded-card bg-surface shadow-card outline-none focus-visible:ring-2 focus-visible:ring-accent" style={{ animation: `fade-up 350ms ${EASE} both` }}>
      <div className="px-3.5 pt-3 pb-2.5">
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <div className={cx("mb-0.5 flex items-center gap-1.5 text-[11px] font-medium", looking ? "text-ink-3" : "text-attention")}>
              <StatusMark state={looking ? "working" : "needs_you"} size="sm" label={false} />
              {kicker}
              {marks}
            </div>
            <div ref={head} className="text-[13.5px] font-medium text-ink">{titleContent ?? title}</div>
          </div>
          {stack > 1 && (
            <span data-chat-cards-count className="flex shrink-0 items-center gap-1 text-xs whitespace-nowrap text-ink-2">
              {t("chat-interactions.pending", { count: stack })}
              <Icon name="arrowDown" size={12} stroke={2.2} className="text-ink-3" />
            </span>
          )}
          {/* folded, where this card stands among those waiting, which opens them all; opened, "Show first only" in the same
              place (never an answer: the tap guard leaves it alone, and the card's keys do not take it) */}
          {queue && (
            <button
              type="button"
              data-chat-cards-position
              onClick={queue.onToggle}
              aria-expanded={queue.open}
              aria-label={queue.open ? t("chat-interactions.first_only") : t("chat-interactions.queue_position_label", { position: queue.position, total: queue.total })}
              title={queue.open ? undefined : t("chat-interactions.queue_show_all")}
              className="touch-hit shrink-0 rounded-full bg-inset px-2 py-0.5 text-submeta font-medium whitespace-nowrap text-ink-2 tabular-nums shadow-hairline transition-colors hover:text-ink"
            >
              {queue.open ? t("chat-interactions.first_only") : t("chat-interactions.queue_position", { position: queue.position, total: queue.total })}
            </button>
          )}
        </div>
        {decision && (
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-label text-ink-2" data-decision-actions>
            {decision.navigation}
            {decisionHandBack && handBack}
            {(skip || noSkip) && <button type="button" data-card-answer={noSkip ? undefined : ""} disabled={busy || Boolean(noSkip)}
              onClick={skip?.run} className="touch-hit enabled:hover:text-ink disabled:opacity-45">{t(skip?.error ? "assistant-tasks.retry" : "chat-interactions.decision_skip_label")}</button>}
            {noSkip && <span className="basis-full text-ink-3">{noSkip}</span>}
            {skip?.error && <span role="alert" className="basis-full text-attention">{skip.error}</span>}
          </div>
        )}
        <div className="mt-2">
          {from && <p data-permission-from title={from} className="mb-2 truncate text-[12px] leading-snug text-ink-3">{from}</p>}
          {children}
        </div>
        {handBack && bottomHandBack && (
          <div className="mt-2 flex">{handBack}</div>
        )}
        <div ref={end} aria-hidden className="h-0" />
      </div>
      {/* data-card-actions: what answers the card (with hand back above), for the sync scenarios (spec/fixtures/sync) */}
      {/* while cut, the line saying what it answers sits in the row before its main answers (the row's last group), so
          the row keeps its height where it fits: a row as tall as before is in view as before */}
      {/* its tooltip is the whole of what it says (the line truncates a long command); its icon is not the send arrow */}
      <div ref={row} data-card-actions className={cx("sticky -bottom-1 flex items-center justify-between gap-2 border-t border-line bg-inset px-3 py-2", (sending || says) && "flex-wrap", says && "[&>:last-child]:order-2")}>
        {says && (
          <button type="button" data-card-answers onClick={reveal} title={answers} aria-label={`${t("chat-interactions.show_card")}: ${answers}`} className="touch-hit order-1 flex min-w-0 flex-1 basis-40 items-center gap-1.5 text-left text-xs text-ink-2 transition-colors hover:text-ink max-sm:order-first max-sm:basis-full">
            <Icon name="unfold" size={12} stroke={2.2} className="shrink-0 text-ink-3" />
            <span className="min-w-0 truncate">{answers}</span>
          </button>
        )}
        {sending && <p data-card-pending role="status" className="basis-full truncate text-[12px] text-ink-2">{sending}</p>}
        {footer}
      </div>
    </div>
  );
}

/** A terminal dialog's option: its words without the "(Recommended)" suffix, the "Recommended" tag instead. */
export function OptionLabel({ label }: { label: string }) {
  const shown = optionDisplay(label);
  return (
    <span className="block text-[13px] text-ink">
      {shown.text}
      {shown.recommended && <RecommendedTag />}
    </span>
  );
}

function SendArrow({ ready, onClick, label, busy, kbd }: { ready: boolean; onClick: () => void; label: string; busy?: boolean; /** While the card has keyboard focus: the Enter that runs this. */ kbd?: boolean }) {
  return (
    <span className="flex items-center gap-1.5">
      {kbd && <Kbd>Enter</Kbd>}
      <button
        type="button"
        aria-label={label}
        title={label}
        disabled={!ready || busy}
        onClick={onClick}
        className="touch-hit flex size-7 items-center justify-center rounded-[8px] transition-[background-color,color,transform] duration-200 enabled:active:scale-[0.96]"
        style={{ background: ready ? "var(--ink)" : "var(--field)", color: ready ? "var(--surface)" : "var(--ink-3)", boxShadow: ready ? "inset 0 1px 0 rgba(255,255,255,0.14)" : "var(--shadow-btn)" }}
      >
        <Icon name="up" size={14} stroke={2.5} />
      </button>
    </span>
  );
}

function ToolLoopCard({ ix }: { ix: Interaction }) {
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  const keys = useCardKeys();
  const tapGuard = useChatCardTapGuard(ix);
  const q = ix.questions?.[0];
  const options = q?.options;
  if (!q || !options || options.length !== 2) return null;
  const send = async (label: string) => {
    if (sending.current) return;
    sending.current = true;
    setBusy(true);
    try {
      await resolve(ix, { answers: { [q.question]: label } });
    } finally {
      sending.current = false;
      setBusy(false);
    }
  };
  return (
    <Card
      kicker={t("chat-interactions.tool_loop_kicker")}
      title={q.question}
      cardKeys={keys}
      tapGuard={tapGuard}
      footer={options.map((option, i) => (
        <Button key={i} tone={i === 0 ? "quiet" : "primary"} className="min-w-0 shrink" title={option.label} disabled={busy} onClick={() => void send(option.label)}>
          <span className="truncate">{option.label}</span>
        </Button>
      ))}
    >
      {null}
    </Card>
  );
}

const SessionReviewCard = lazyPage(() => import("./SessionReviewCard").then((m) => m.SessionReviewCard));

export function QuestionCard({ ix, canApprove, onAnswer }: { ix: Interaction; canApprove: boolean; onAnswer?: (answers: Record<string, string>) => Promise<boolean> }) {
  const ime = useComposition();
  const qs = ix.questions ?? [];
  const localOnly = ix.tool_name === "persona_triage" && !canApprove;
  const decision = useContext(DecisionCardContext);
  const draftKey = decision && decisionKey(ix);
  const version = draftKey ? JSON.stringify(qs) : "";
  const stored = draftKey ? decision?.drafts.get(draftKey) : undefined;
  const draft = stored?.version === version ? stored : undefined;
  const [qi, setQi] = useState(() => draft?.page ?? 0);
  const [picked, setPicked] = useState<Record<number, string[]>>(() => draft?.picks ?? {});
  const [custom, setCustom] = useState<Record<number, string>>(() => draft?.custom ?? {});
  const [busy, setBusy] = useState(false);
  const [skipError, setSkipError] = useState<string>();
  const submitting = useRef(false);
  const q = qs[qi];
  // picking on a single-select slides to the next question by itself, a beat later; going on before that (Enter, the
  // arrow) cancels the slide, or the two would advance a question each and skip one
  const slide = useRef<number | undefined>(undefined);
  const drafts = decision?.drafts;
  useLayoutEffect(() => {
    if (draftKey) drafts?.set(draftKey, { version, page: qi, picks: picked, custom });
  }, [drafts, draftKey, version, qi, picked, custom]);
  const clearSlide = () => clearTimeout(slide.current);
  useEffect(() => () => clearTimeout(slide.current), [ix.id]);
  const goTo = (next: number | ((i: number) => number)) => {
    clearSlide();
    setQi(next);
  };
  const advance = () => goTo((i) => Math.min(qs.length - 1, i + 1));
  // the card's own keys (a hook, so before the early return): digits pick this question's options, Enter is what the
  // send arrow does, Esc is the card's wherever in it focus sits
  const keys = useCardKeys({
    digit: (n) => {
      const o = q?.options?.[n - 1];
      if (!o || busy || (localOnly && o.action !== "defer")) return false;
      toggle(o.label);
      return true;
    },
    enter: () => {
      if (busy || (last ? !all : !(answered(qi) && allowed(qi)))) return false;
      if (last) void send();
      else advance();
    },
  });
  // paging to another question shows a new option set: its answers restart the guard window
  const tapGuard = useChatCardTapGuard(ix, `:q${qi}`);
  const toggleRef = useRef<(label: string) => void>(() => {});

  useEffect(() => {
    if (!q || busy || (!canApprove && !localOnly)) return;
    const hidden = (el: Element): boolean => {
      for (let c: HTMLElement | null = el as HTMLElement; c; c = c.parentElement) {
        if (c.matches("[inert],[hidden],[aria-hidden=true],[data-chat-cards].hidden") || c.style.gridTemplateRows === "0fr") return true;
        const cs = getComputedStyle(c);
        if (cs.display === "none" || cs.visibility === "hidden") return true;
      }
      return false;
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.defaultPrevented || composing(e) || e.ctrlKey || e.metaKey || e.altKey || !/^[1-9]$/.test(e.key)) return;
      const root = keys.root.ref.current;
      if (!root?.isConnected || root.closest("fieldset[disabled]") || hidden(root)) return;
      for (const o of document.querySelectorAll("[role=dialog],[role=alertdialog],[role=menu],[aria-modal=true],dialog[open],aside[data-side-panel]")) {
        if (o.contains(root) || o.closest("[data-closing]") || hidden(o)) continue;
        if (o.tagName === "ASIDE" && (root.closest("[role=dialog],[aria-modal=true]") || getComputedStyle(o).position !== "fixed")) continue;
        return;
      }
      const el = e.target instanceof Element ? e.target : null;
      if (el && (el.closest(EDITABLE) || (el as HTMLElement).isContentEditable || (el.closest("[data-card-keys]") ?? root) !== root)) return;
      const opt = q.options?.[Number(e.key) - 1];
      if (!opt || (localOnly && opt.action !== "defer")) return;
      e.preventDefault();
      toggleRef.current(opt.label);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [q, busy, canApprove, localOnly, keys.root.ref]);

  if (!q) return null;
  const looking = isDelegating(ix);
  const picks = recommendedLabels(qs, ix.delegate?.recommend);
  // a recommendation that is the options' own words shows on them, not again in the context
  const onOptions = picks.every((p) => p.length > 0);
  const context = delegateLines(ix.delegate).filter((l) => !(l.key === "recommend" && onOptions));
  const last = qi === qs.length - 1;
  const answered = (i: number) => (picked[i]?.length ?? 0) > 0 || Boolean(custom[i]?.trim());
  // A card that became read-only after a pick must not submit that now-disabled answer. Only explicit defer choices
  // remain valid; the custom field and every message-sending option are disabled below.
  const allowed = (i: number) => !localOnly || ((picked[i]?.length ?? 0) > 0 && !custom[i]?.trim() &&
    (picked[i] ?? []).every((label) => qs[i].options?.some((o) => o.label === label && o.action === "defer")));
  const all = qs.every((_, i) => answered(i) && allowed(i));

  const send = async (directLabel?: string) => {
    if (submitting.current || directLabel === undefined && !all) return;
    submitting.current = true;
    clearSlide();
    setBusy(true);
    setSkipError(undefined);
    const answers: Record<string, string> = {};
    if (directLabel === undefined) {
      qs.forEach((x, i) => {
        answers[x.question] = custom[i]?.trim() || (picked[i] ?? []).join(", ");
      });
    } else {
      // The pick's state update has not rendered yet, so carry its label into the immediate submission explicitly.
      answers[q.question] = directLabel;
    }
    try {
      // The caller owns completion: false keeps the picks for retry, and finally only unlocks the form.
      if (onAnswer) await onAnswer(answers);
      else await resolve(ix, { answers });
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };

  const canSkip = Boolean(decision && ix.can_cancel && canApprove && !onAnswer);
  const skip = async () => {
    if (!canSkip || submitting.current) return;
    submitting.current = true;
    clearSlide();
    setBusy(true);
    try {
      const result = await answerCard(ix, { cancel: true });
      setSkipError(result.ok ? undefined : result.error);
    } finally {
      submitting.current = false;
      setBusy(false);
    }
  };

  const toggle = (label: string) => {
    if (submitting.current) return;
    if (localOnly && !q.options?.some((o) => o.label === label && o.action === "defer")) return;
    clearSlide();
    const singleChoice = qs.length === 1 && !q.multi_select;
    const hasCustomAnswer = singleChoice && Boolean(custom[qi]?.trim());
    setCustom((c) => ({ ...c, [qi]: "" }));
    setPicked((cur) => {
      const have = cur[qi] ?? [];
      const next = q.multi_select ? (have.includes(label) ? have.filter((x) => x !== label) : [...have, label]) : [label];
      return { ...cur, [qi]: next };
    });
    if (singleChoice && !hasCustomAnswer) void send(label);
    else if (!q.multi_select && !last) slide.current = window.setTimeout(() => setQi((i) => Math.min(qs.length - 1, i + 1)), 380);
  };
  toggleRef.current = toggle;
  return (
    <Card
      kicker={q.header || t("chat-interactions.question")}
      title={q.question}
      titleContent={ix.format === "markdown" ? <Markdown text={q.question} small compact /> : undefined}
      looking={looking}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={onAnswer || ix.no_hand_back ? undefined : () => {
        clearSlide();
        void resolve(ix, { dismiss: true });
      }}
      skip={canSkip ? { run: skip, error: skipError } : undefined}
      busy={busy}
      footer={
        <>
          <span className="flex items-center gap-2">
            {qs.length > 1 && (
              <>
                <button type="button" aria-label={t("chat-interactions.previous_question")} disabled={qi === 0} onClick={() => goTo(qi - 1)} className="touch-hit flex size-6 items-center justify-center rounded-[5px] text-ink-3 enabled:hover:bg-hover disabled:opacity-35">
                  <Icon name="chevronLeft" size={14} stroke={2.2} />
                </button>
                <span className="flex items-center gap-1">
                  {qs.map((_, i) => (
                    <button
                      key={i}
                      type="button"
                      aria-label={t("chat-interactions.question_n", { n: i + 1 })}
                      onClick={() => goTo(i)}
                      className="rounded-full transition-all duration-300"
                      style={i === qi ? { width: 9, height: 9, border: "2.5px solid var(--ink)" } : answered(i) ? { width: 7, height: 7, background: "var(--ink-3)" } : { width: 7, height: 7, border: "1.5px solid var(--ink-3)" }}
                    />
                  ))}
                </span>
                <button type="button" aria-label={t("chat-interactions.next_question")} disabled={last} onClick={() => goTo(qi + 1)} className="touch-hit flex size-6 items-center justify-center rounded-[5px] text-ink-3 enabled:hover:bg-hover disabled:opacity-35">
                  <Icon name="chevronRight" size={14} stroke={2.2} />
                </button>
              </>
            )}
            {q.multi_select && <span className="text-submeta text-ink-3">{t("chat-interactions.multi_select")}</span>}
          </span>
          <SendArrow ready={last ? all : answered(qi) && allowed(qi)} busy={busy} kbd={keys.focused} label={last ? t("chat-interactions.send_answer") : t("chat-interactions.next_question")} onClick={() => (last ? void send() : advance())} />
        </>
      }
    >
      {looking && (
        <div data-delegating className="mb-2 flex items-start gap-2 rounded-control bg-inset px-2.5 py-2 text-[12px] leading-snug shadow-hairline">
          <Icon name="eye" size={13} stroke={2.2} className="mt-px shrink-0 text-ink-3" />
          <span className="min-w-0">
            <span className="block font-medium text-ink">{t("chat-delegate.looking")}</span>
            <span className="block text-ink-3">{t("chat-delegate.looking_hint")}</span>
          </span>
        </div>
      )}
      {context.length > 0 && (
        <div data-delegate-context className="mb-2 rounded-control bg-inset px-2.5 py-2 shadow-hairline">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium text-ink-3">
            <Icon name="sparkles" size={12} stroke={2.2} className="shrink-0" />
            {t("chat-delegate.context_title")}
          </div>
          <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-[12px] leading-snug">
            {context.map((l) => (
              <Fragment key={l.key}>
                <dt className="whitespace-nowrap text-ink-3">{l.label}</dt>
                <dd className="break-words whitespace-pre-wrap text-ink-2">{l.text}</dd>
              </Fragment>
            ))}
          </dl>
        </div>
      )}
      <div key={qi} className="flex flex-col gap-0.5" style={{ animation: `fade-up 300ms ${EASE} both` }}>
        {(q.options ?? []).map((o, i) => (
          <Choice key={o.label} on={(picked[qi] ?? []).includes(o.label)} multi={q.multi_select} label={o.label} desc={o.description} pick={picks[qi]?.length ? picks[qi].includes(o.label) : q.recommend === o.label} hint={keys.focused && i < 9 ? i + 1 : undefined} disabled={localOnly && o.action !== "defer"} onClick={() => toggle(o.label)} marker />
        ))}
        <label className="-mx-1.5 flex items-center gap-2 rounded-control px-1.5 py-1.5 transition-colors focus-within:bg-hover hover:bg-hover">
          <span aria-hidden className="size-4 shrink-0" />
          <input
            value={custom[qi] ?? ""}
            disabled={localOnly}
            onFocus={clearSlide}
            onChange={(e) => {
              clearSlide();
              setCustom((c) => ({ ...c, [qi]: e.target.value }));
              if (e.target.value) setPicked((p) => ({ ...p, [qi]: [] }));
            }}
            {...ime.handlers}
            onKeyDown={(e) => e.key === "Enter" && !ime.busy(e.nativeEvent) && (last ? all && void send() : answered(qi) && allowed(qi) && advance())}
            placeholder={t("chat-interactions.custom_placeholder")}
            aria-label={t("chat-interactions.custom_answer")}
            // focused in a box that scrolls, it lands clear of the card's sticky answer row below and of a sticky title
            // above (the assistant's Pending tray), rather than under either
            className="min-w-0 flex-1 scroll-my-14 bg-transparent text-[13px] text-ink outline-none placeholder:text-ink-3"
          />
        </label>
      </div>
    </Card>
  );
}

const MODES = [
  { key: "acceptEdits", label: t("chat-interactions.mode_accept_edits") },
  { key: "bypassPermissions", label: t("chat-interactions.mode_bypass") },
  { key: "default", label: t("chat-interactions.mode_default") },
];

function PlanCard({ ix }: { ix: Interaction }) {
  const ime = useComposition();
  const [feedback, setFeedback] = useState("");
  const [rejecting, setRejecting] = useState(false);
  const [busy, setBusy] = useState(false);
  const menu = useMenu();
  const planThen = ix.plan_then && ix.plan_then !== "plan" && PERMISSION_MODES.some((m) => m.id === ix.plan_then) ? ix.plan_then : undefined;
  const go = async (body: Record<string, unknown>) => {
    setBusy(true);
    await resolve(ix, body);
    setBusy(false);
  };
  // Enter approves in the recorded following mode, else with edits (the Approve button); rejecting's Enter is its field's
  const keys = useCardKeys({
    enter: () => {
      if (busy || rejecting) return false;
      void go({ approve: true, mode: planThen ?? "acceptEdits" });
    },
  });
  const tapGuard = useChatCardTapGuard(ix);
  return (
    <Card
      kicker={t("chat-interactions.plan_kicker")}
      title={t("chat-interactions.plan_title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={ix.no_hand_back ? undefined : () => go({ dismiss: true })}
      busy={busy}
      footer={
        rejecting ? (
          <>
            <input
              autoFocus
              value={feedback}
              onChange={(e) => setFeedback(e.target.value)}
              {...ime.handlers}
              onKeyDown={(e) => e.key === "Enter" && !ime.busy(e.nativeEvent) && feedback.trim() && go({ approve: false, feedback: feedback.trim() })}
              placeholder={t("chat-interactions.feedback_placeholder")}
              className="h-8 min-w-0 flex-1 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3"
            />
            <Button tone="quiet" disabled={busy} onClick={() => setRejecting(false)}>
              {t("common.cancel")}
            </Button>
            <Button tone="primary" busy={busy} disabled={!feedback.trim()} onClick={() => go({ approve: false, feedback: feedback.trim() })}>
              {t("chat-interactions.reject")}
            </Button>
          </>
        ) : (
          <>
            <Button tone="quiet" disabled={busy} onClick={() => setRejecting(true)}>
              {t("chat-interactions.request_changes")}
            </Button>
            <span className="flex items-center">
              {planThen && <span className="mr-2 text-meta font-medium whitespace-nowrap text-ink-2">{modeTitle(planThen)}</span>}
              <Button tone="primary" busy={busy} className={planThen ? undefined : "rounded-r-none"} onClick={() => go({ approve: true, mode: planThen ?? "acceptEdits" })}>
                {t("chat-interactions.approve")}
                {keys.focused && <Kbd>Enter</Kbd>}
              </Button>
              {!planThen && <button type="button" aria-label={t("chat-interactions.approve_mode")} disabled={busy} onClick={menu.open} className="touch-hit flex h-8 w-7 items-center justify-center rounded-r-control border-l border-white/15 bg-ink text-surface enabled:hover:opacity-90 disabled:opacity-40">
                <Icon name="chevron" size={12} stroke={2.4} />
              </button>}
              {!planThen && menu.anchor && <Menu anchor={menu.anchor} onClose={menu.close} align="right" side="top" width={196} items={MODES.map((m) => ({ key: m.key, label: m.label, onSelect: () => go({ approve: true, mode: m.key }) }))} />}
            </span>
          </>
        )
      }
    >
      <div className="overflow-y-auto rounded-[8px] bg-inset px-3 py-2.5 shadow-hairline" style={{ maxHeight: PLAN_MAX }} data-scroll-box>
        <Markdown text={ix.plan ?? ""} small />
      </div>
    </Card>
  );
}

// The complete tool-approval card loads with the first permission interaction, keeping its optional clarification UI
// and permission editor out of the first screen. Its slot stays identifiable while the chunk loads or cannot load.
const PermissionCard = lazyPage(() => import("./PermissionCard").then((m) => m.PermissionCard));
// A card the assistant answered for the user (its "Change answer"): drawn only after such an answer, so its chunk is off
// the first screen and loads with the first one (the idle tier has no room for it).
const DelegatedCard = lazyPage(() => import("./Delegated").then((m) => m.DelegatedCard));

export function PermissionCardLoading({ ix }: { ix: Interaction }) {
  return (
    <Card kicker={t("chat-interactions.permission_kicker")} title={t("chat-interactions.permission_title", { tool: cardToolName(ix) })} footer={null}>
      <Loading />
    </Card>
  );
}

/** A repeated cached import failure gets one full reload per card and tab session. The marker survives that reload;
 *  if the chunk is still unavailable, the card keeps its local Retry instead of entering a reload loop. */
function reloadCardChunk(key: string) {
  try {
    if (sessionStorage.getItem(key)) return;
    sessionStorage.setItem(key, "1");
  } catch {
    return;
  }
  location.reload();
}

/** One failed permission chunk must not blank its card or take down the chat. Retry first remounts lazyPage; browsers
 *  that cached the failed native import reload once on its repeated failure. Another card starts without this failure. */
export class PermissionCardFrame extends Component<{ ix: Interaction; children?: ReactNode }, { error: unknown }> {
  state = { error: null as unknown };
  failures = 0;

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    const chunk = isChunkError(error);
    reportBoundary(error, "permission_card", chunk, info.componentStack);
    if (chunk && this.failures++) reloadCardChunk(`permission-chunk-reload:${this.props.ix.id}`);
  }

  render() {
    const { ix, children } = this.props;
    if (this.state.error) {
      return (
        <Card kicker={t("chat-interactions.permission_kicker")} title={t("chat-interactions.permission_title", { tool: cardToolName(ix) })}
          footer={<Button size="sm" onClick={() => this.setState({ error: null })}>{t("app.retry")}</Button>}>
          <ErrorNotice role="alert" className="border-0 bg-transparent p-0" message={t("chat-interactions.permission_load_failed")}
            description={t("chat-interactions.permission_load_hint")} />
        </Card>
      );
    }
    return <Suspense fallback={<PermissionCardLoading ix={ix} />}>{children ?? <PermissionCard ix={ix} />}</Suspense>;
  }
}

/** The same for a card the assistant answered for the user: its chunk loads with the first such answer, and a failed
 *  load keeps a Retry in its slot instead of taking the chat (and the cards beside it) down to the page boundary. */
export class DelegatedCardFrame extends Component<{ d: DelegatedAnswer; children?: ReactNode }, { error: unknown }> {
  state = { error: null as unknown };
  failures = 0;

  static getDerivedStateFromError(error: unknown) {
    return { error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    const chunk = isChunkError(error);
    reportBoundary(error, "delegated_card", chunk, info.componentStack);
    if (chunk && this.failures++) reloadCardChunk(`delegated-chunk-reload:${this.props.d.ix.id}`);
  }

  render() {
    const { d, children } = this.props;
    if (this.state.error) {
      return (
        <ErrorNotice role="alert" message={t("chat-fulltext.failed")}
          actions={<Button size="sm" onClick={() => this.setState({ error: null })}>{t("app.retry")}</Button>} />
      );
    }
    return <Suspense fallback={null}>{children ?? <DelegatedCard d={d} />}</Suspense>;
  }
}

/** A terminal dialog's words under its question (#6638): at most six lines in view, the rest a scroll inside them. */
export const DETAIL_FOLD = "mb-2 max-h-[6lh] text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2";

export const CODE_BOX = "max-h-48 overflow-auto rounded-[8px] bg-inset px-2.5 py-2 font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap text-ink-2 shadow-hairline";

const FROM_MAX = 48;

/** "From subagent · <type>" for a subagent's card, its type on one line and cut to FROM_MAX; for a child session's card
 *  on its parent's page (#5120, `folded_under`), the child's title the same way, "From worker · <title>" for a lead's
 *  worker's (#5180, `folded_as`) and "From side session · <title>" for a side session's (#7944); empty otherwise. */
export function permissionFrom(ix: Pick<Interaction, "agent_id" | "agent_type" | "folded_under" | "folded_as" | "session_title">): string {
  const oneLine = (s: string | undefined) => (s ?? "").replace(/\s+/g, " ").trim();
  const name = ix.folded_under ? oneLine(ix.session_title) || t("chat-subagentcard.untitled_task") : ix.agent_id?.trim() ? oneLine(ix.agent_type) : "";
  if (!name || (!ix.folded_under && name.toLowerCase() === "main")) return "";
  const agent = name.length > FROM_MAX ? `${name.slice(0, FROM_MAX - 1)}…` : name;
  if (ix.folded_under && isSide(ix)) return t("lib-elicitation.from_side", { agent });
  return ix.folded_under && ix.folded_as === "worker" ? t("lib-elicitation.from_worker", { agent }) : t("lib-elicitation.from_subagent", { agent });
}

/**
 * The assistant's password card (#1137, #1581): what runs where, every command verbatim and read-only, the password
 * field, Decline and Run. The password lives only in this card's state and its input: emptied as Run sends it, on
 * Decline, and when the card goes (a card with another id is a new SecretRunCard, see InteractionCard). No form and
 * nothing Enter submits: the commands run on a click on Run, never on a key pressed while typing. The card's own keys
 * are only Esc back to the composer — no digits (they are the password), no Enter (never a key runs these commands).
 */
const ownerCardsSeenSent = new Set<string>();

/** Report once when an unseen owner-only timed card is displayed in a visible tab so the server can start its post-view countdown. */
function useOwnerCardSeen(ix: Interaction, active: boolean) {
  const conn = useStore((s) => s.conn);
  useEffect(() => {
    if (!active || !ix.seen_ttl_seconds || ix.seen_at || conn === "offline" || ownerCardsSeenSent.has(ix.id)) return;
    const send = () => {
      if (ownerCardsSeenSent.has(ix.id)) return;
      ownerCardsSeenSent.add(ix.id);
      void api(`/interactions/${enc(ix.id)}/seen`, { method: "POST" }).catch((err) => {
        if (!(err instanceof ApiError) || err.status === 401 || err.status === 408 || err.status === 429 || err.status >= 500) {
          ownerCardsSeenSent.delete(ix.id);
        }
      });
    };
    if (typeof document === "undefined" || document.visibilityState !== "hidden") {
      send();
      return;
    }
    const onVisible = () => {
      if (document.visibilityState === "hidden") return;
      document.removeEventListener("visibilitychange", onVisible);
      send();
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, [active, ix.id, ix.seen_at, ix.seen_ttl_seconds, conn]);
}

function SecretRunCard({ ix }: { ix: Interaction }) {
  const [busy, setBusy] = useState<"run" | "decline" | null>(null);
  const [password, setPassword] = useState("");
  const input = useRef<HTMLInputElement>(null);
  const keys = useCardKeys();
  const tapGuard = useChatCardTapGuard(ix);
  // a field taken off the page keeps its value until collected, and Firefox offers to save a formless password field
  // that is removed while it holds one (its form-removal capture): it is emptied before React removes it
  useLayoutEffect(() => {
    const el = input.current;
    return () => {
      if (el) el.value = "";
    };
  }, []);
  const { machine, purpose, delivery, commands } = secretRunInput(ix.tool_input);
  // without commands in tool_input the hub's summary (the computer, the purpose and every command) stands in for them;
  // with neither, nothing says what would run and Run stays off
  const summary = commands.length ? "" : (ix.summary?.trim() ?? "");
  // the hub refuses a password of spaces alone
  const ready = password.trim() !== "" && (commands.length > 0 || summary !== "");
  const label = ix.fields?.find((f) => f.key === "password")?.label?.trim() || t("chat-interactions.password");
  const send = async (body: Record<string, unknown>, key: "run" | "decline") => {
    setPassword("");
    setBusy(key);
    await resolve(ix, body, { handoffCard: true });
    setBusy(null);
  };
  return (
    <Card
      kicker={t("chat-interactions.secret_run_kicker")}
      title={t("chat-interactions.secret_run_title", { machine })}
      cardKeys={keys}
      tapGuard={tapGuard}
      footer={
        <>
          <Button tone="quiet" busy={busy === "decline"} disabled={busy !== null} onClick={() => void send({ allow: false }, "decline")}>
            {t("chat-interactions.secret_run_decline")}
          </Button>
          <Button tone="primary" busy={busy === "run"} disabled={busy !== null || !ready} onClick={() => void send({ allow: true, values: { password } }, "run")}>
            {t("chat-interactions.secret_run_run")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        {purpose && <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-ink">{purpose}</p>}
        {commands.length > 0 ? (
          <div>
            <div className="mb-1 text-[11px] font-medium text-ink-3">{t("chat-interactions.secret_run_commands")}</div>
            {/* one block per command: a long one that wraps never reads as two */}
            <div className="flex flex-col gap-1.5">
              {commands.map((c, i) => <pre key={i} data-secret-run-command className={CODE_BOX}>{c}</pre>)}
            </div>
          </div>
        ) : (
          summary && <pre data-secret-run-summary className={CODE_BOX}>{summary}</pre>
        )}
        {delivery === "env" && (
          <p role="alert" className="flex items-start gap-1.5 text-xs leading-snug font-medium text-orange">
            <Icon name="warning" size={13} className="mt-px shrink-0" />
            {t("chat-interactions.secret_run_env_warning")}
          </p>
        )}
        <div className="flex flex-col gap-1">
          {/* The user's computer password, not this site's: "one-time-code" keeps Chrome from offering to save it (its
              password parser skips such fields) and from suggesting a generated one ("new-password" would); no name, no
              form. */}
          <input
            ref={input}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            // Enter only puts the keyboard away, as Return does on the iPhone card
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              e.currentTarget.blur();
            }}
            placeholder={label}
            aria-label={label}
            disabled={busy !== null}
            {...SECRET_INPUT}
            autoComplete="one-time-code"
            autoCapitalize="off"
            autoCorrect="off"
            enterKeyHint="done"
            className="h-8 w-full min-w-0 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3"
          />
          <span className="text-[12px] leading-snug text-ink-3">{t("chat-interactions.secret_run_note")}</span>
        </div>
      </div>
    </Card>
  );
}

/**
 * The hand-off card (#3962): secret values a provider holds, handed to one Agent run once the user approves here. Only
 * this card approves it (lib/card-answer.ts sends its approval with the card's feature; Overview's rows and the
 * notifications open the chat instead), and no key does: Allow is a press of its button. It says what goes where:
 * each variable with the item it comes from, the Agent and its computer, where it runs and what it is asked.
 */
function SecretHandoffCard({ ix }: { ix: Interaction }) {
  const [busy, setBusy] = useState<"allow" | "always" | "deny" | null>(null);
  const keys = useCardKeys();
  const tapGuard = useChatCardTapGuard(ix);
  const { variables, store, agent, machine, cwd, prompt } = secretHandoffInput(ix.tool_input);
  // nothing says what would be handed over: only Deny
  const ready = variables.length > 0;
  const go = async (body: Record<string, unknown>, key: "allow" | "always" | "deny") => {
    setBusy(key);
    await resolve(ix, body, { handoffCard: true });
    setBusy(null);
  };
  const lead = agent
    ? t("chat-interactions.secret_handoff_lead", { agent, machine })
    : t("chat-interactions.secret_handoff_lead_unnamed", { machine });
  return (
    <Card
      kicker={t("chat-interactions.permission_kicker")}
      title={store ? ix.tool_title || ix.summary || t("chat-interactions.secret_handoff_title") : t("chat-interactions.secret_handoff_title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      footer={
        <>
          <Button tone="quiet" busy={busy === "deny"} disabled={busy !== null} onClick={() => void go({ allow: false }, "deny")}>
            {t("chat-interactions.deny")}
          </Button>
          <span className="flex gap-1.5">
            {ix.always_label && (
              <Button tone="secondary" busy={busy === "always"} disabled={busy !== null || !ready} onClick={() => void go({ allow: true, always: true }, "always")}>
                {t("chat-interactions.always_allow")}
              </Button>
            )}
            <Button tone="primary" busy={busy === "allow"} disabled={busy !== null || !ready} onClick={() => void go({ allow: true }, "allow")}>
              {t("chat-interactions.allow")}
            </Button>
          </span>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        <p className="text-[13px] leading-relaxed text-ink">{store ? ix.summary : machine ? lead : t("chat-interactions.secret_handoff_lead_bare")}</p>
        <ul className="flex flex-col gap-1">
          {variables.map((v) => (
            <li key={v.name} data-handoff-variable className="flex min-w-0 items-baseline gap-2">
              <code className="shrink-0 font-mono text-[12px] text-ink">{v.name}</code>
              <span className="min-w-0 break-words text-[13px] text-ink-2">{v.title ? `${v.provider} › ${v.title}` : v.provider}</span>
            </li>
          ))}
        </ul>
        {(cwd || prompt) && (
          <div className="flex flex-col gap-0.5 text-[12px] leading-snug text-ink-3">
            {cwd && <span data-handoff-cwd className="truncate font-mono">{cwd}</span>}
            {prompt && <span data-handoff-prompt className="line-clamp-2">{prompt}</span>}
          </div>
        )}
        <span className="text-[12px] leading-snug text-ink-3">
          {t("chat-interactions.secret_handoff_note")}
          {ix.always_label ? ` ${t("chat-interactions.secret_handoff_always_scope", { scope: ix.always_label })}` : ""}
        </span>
      </div>
    </Card>
  );
}

export function CloseBlockedAgent({ ix, disabled }: { ix: Interaction; disabled: boolean }) {
  const session = useStore(() => getSession(ix.machine_id, ix.session_id));
  const [confirming, setConfirming] = useState(false);
  const [closing, setClosing] = useState(false);
  if (!session || session.assistant || session.status !== "blocked") return null;
  const close = async () => {
    if (closing) return;
    setClosing(true);
    await archiveAgent(session, true, { ...appArchiveDeps, archive: s => archiveSession(s, true, true) });
    setClosing(false);
    setConfirming(false);
  };
  if (!confirming) return <Button className="min-h-11" disabled={disabled} onClick={() => setConfirming(true)}>{t("chat-interactions.close_agent")}</Button>;
  return <div className="flex min-w-0 flex-col gap-2">
    <p className="text-ink-2">{t("chat-interactions.close_agent_hint")}</p>
    <div className="flex flex-wrap gap-2">
      <Button className="min-h-11" disabled={disabled || closing} onClick={() => setConfirming(false)}>{t("chat-interactions.close_agent_cancel")}</Button>
      <Button className="min-h-11" tone="danger" busy={closing} disabled={disabled || closing} onClick={() => void close()}>{t("chat-interactions.close_agent_confirm")}</Button>
    </div>
  </div>;
}

/** Claude Code asking to run a command (#5266), said in plain words: its own chunk, loaded only when a command card
 *  appears, since most chats never show one. Until it is in, the card is the terminal's question as before. */
const CommandScreenCard = lazyPage(() => import("./CommandScreenCard").then((m) => m.CommandScreenCard));

/** A card a task's supervisor holds (lib/supervisor-held.ts): what it asks, read-only, and that the supervisor decides
 *  it and passes it to the user when it needs them. No answer, no hand back: it is not the user's card yet. */
function SupervisorHeldCard({ ix }: { ix: Interaction }) {
  const about = useContext(CardAbout);
  const title = ix.kind === "screen" ? ix.dialog?.question || t("chat-interactions.screen_title")
    : ix.kind === "plan" ? t("chat-interactions.plan_title")
    : ix.kind === "permission" ? t("chat-interactions.permission_title", { tool: cardToolName(ix) || ix.tool_name || "" })
    : ix.kind === "elicitation" ? t("chat-interactions.form_title")
    : about || t("chat-interactions.screen_title");
  const detail = ix.kind === "screen" ? [ix.dialog?.detail, ix.dialog?.command].filter(Boolean).join(" · ") : about;
  return (
    <Card
      looking
      kicker={t("chat-delegate.supervisor_held")}
      title={title}
      footer={
        <p data-supervisor-held className="flex min-w-0 items-start gap-1.5 text-label leading-snug text-ink-2">
          <Icon name="eye" size={13} stroke={2.2} className="mt-px shrink-0 text-ink-3" />
          <span className="min-w-0">{t("chat-delegate.supervisor_held_hint")}</span>
        </p>
      }
    >
      {detail && detail !== title && <p className="text-meta leading-snug break-words whitespace-pre-wrap text-ink-2">{detail}</p>}
    </Card>
  );
}

function ScreenCard({ ix }: { ix: Interaction }) {
  // The hub sets `command` only on a shell command's permission prompt (one choice, no free-text field).
  const plain = <PlainScreenCard ix={ix} />;
  return isCommandPrompt(ix.dialog) ? <Suspense fallback={<fieldset disabled className="contents">{plain}</fieldset>}><CommandScreenCard ix={ix} /></Suspense> : plain;
}

function PlainScreenCard({ ix }: { ix: Interaction }) {
  const ime = useComposition();
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => {
    const el = input.current;
    return () => {
      if (el) el.value = "";
    };
  }, []);
  const d = ix.dialog;
  const [text, setText] = useState("");
  const [typing, setTyping] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const go = async (body: Record<string, unknown>, key: string) => {
    setBusy(key);
    await resolve(ix, body);
    setBusy(null);
  };
  // digits pick the options by place (their key badges say what the computer itself reads); with no options Enter sends
  // the text. A "type something" option's digit only opens its field, as a click does
  const keys = useCardKeys({
    digit: (n) => {
      const o = d?.options?.[n - 1];
      if (!o || busy !== null) return false;
      if (o.kind === "type_something") setTyping(o.key);
      else void go({ option: o.key }, o.key);
      return true;
    },
    enter: () => {
      if (busy !== null || (d?.options?.length ?? 0) > 0) return false;
      void go({ text, keys: ["enter"] }, "text");
    },
  });
  const tapGuard = useChatCardTapGuard(ix);
  if (d?.multi) return <MultiScreenCard ix={ix} />;
  return (
    <Card
      kicker={d?.header || t("chat-interactions.screen_kicker")}
      title={d?.question || t("chat-interactions.screen_title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={ix.no_hand_back ? undefined : () => go({ dismiss: true }, "dismiss")}
      busy={busy !== null}
      footer={<div className="flex w-full min-w-0 flex-col gap-2">
        <div className="flex min-w-0 items-center gap-2">
        {d?.options?.length ? (
          <span className="text-[11.5px] text-ink-3">{t("chat-interactions.screen_hint")}</span>
        ) : (
          <>
            <input
              ref={ix.secret ? input : undefined}
              type={ix.secret ? "password" : "text"}
              value={text}
              onChange={(e) => setText(e.target.value)}
              placeholder={ix.secret ? t("chat-interactions.password") : t("chat-interactions.type_and_enter")}
              autoComplete={ix.secret ? "one-time-code" : "off"}
              autoCapitalize={ix.secret ? "off" : undefined}
              autoCorrect={ix.secret ? "off" : undefined}
              {...(ix.secret ? SECRET_INPUT : {})}
              className="h-8 min-w-0 flex-1 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3"
            />
            <Button tone="primary" busy={busy === "text"} disabled={busy !== null} onClick={() => go({ text, keys: ["enter"] }, "text")}>
              {t("chat-interactions.send")}
              {keys.focused && <Kbd>Enter</Kbd>}
            </Button>
          </>
        )}
        </div>
        <CloseBlockedAgent ix={ix} disabled={busy !== null} />
      </div>}
    >
      {/* long words (a command the hub did not read as one, #6638) fold as the chat's long blocks do, so the choices
          under them are in view: six lines that scroll inside and fade while there is more */}
      {d?.detail && <ScrollFold className={DETAIL_FOLD} data-screen-detail>{d.detail}</ScrollFold>}
      {d?.options?.length ? (
        <div className="flex flex-col gap-0.5">
          {d.options.map((o, i) =>
            o.kind === "type_something" && typing === o.key ? (
              <div key={o.key} className="flex items-center gap-1.5 py-1">
                <input
                  autoFocus
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                  {...ime.handlers}
                  onKeyDown={(e) => e.key === "Enter" && !ime.busy(e.nativeEvent) && text.trim() && go({ option: o.key, text }, o.key)}
                  placeholder={o.label}
                  className="h-8 min-w-0 flex-1 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3"
                />
                <Button tone="primary" busy={busy === o.key} disabled={!text.trim() || busy !== null} onClick={() => go({ option: o.key, text }, o.key)}>
                  {t("chat-interactions.send")}
                </Button>
              </div>
            ) : (
              <button
                key={o.key}
                type="button"
                disabled={busy !== null}
                data-tap-answer
                onClick={() => (o.kind === "type_something" ? setTyping(o.key) : go({ option: o.key }, o.key))}
                className="touch-hit -mx-1.5 flex items-start gap-2 rounded-control px-1.5 py-1.5 text-left transition-colors hover:bg-hover disabled:opacity-60"
              >
                <span className="mt-[1px] flex h-4.5 min-w-4.5 shrink-0 items-center justify-center rounded-[5px] bg-field px-1 font-mono text-[10.5px] text-ink-2 shadow-hairline">{o.key}</span>
                {keys.focused && i < 9 && <DigitHint n={i + 1} />}
                <span className="min-w-0">
                  <OptionLabel label={o.label} />
                  {o.detail && <span className="block text-[12px] text-ink-3">{o.detail}</span>}
                </span>
                {busy === o.key && <span className={`ml-auto mt-1 size-3 shrink-0 rounded-full border-[1.5px] border-line-strong border-t-ink-2 ${BUSY_MOTION}`} style={spinStyle(700)} />}
              </button>
            ),
          )}
        </div>
      ) : (
        <pre className="max-h-56 overflow-auto rounded-[8px] bg-[var(--term-bg,#1b1c1f)] px-3 py-2 font-mono text-[11.5px] leading-[1.5] whitespace-pre text-[var(--term-fg,#e6e7e9)]">{ix.screen}</pre>
      )}
    </Card>
  );
}

/** A multi-select terminal question: ticks (starting from what the computer shows) and the free text, sent together;
 *  the computer's boxes end up as here, one key at a time (node/dialogmulti.go). */
function MultiScreenCard({ ix }: { ix: Interaction }) {
  const d = ix.dialog!;
  const options = d.options ?? [];
  const field = options.find((o) => o.kind === "type_something");
  const [picked, setPicked] = useState(() => new Set(options.filter((o) => !o.kind && o.checked).map((o) => o.key)));
  const [text, setText] = useState(field?.text ?? "");
  const [busy, setBusy] = useState<string | null>(null);
  const go = async (body: Record<string, unknown>, key: string) => {
    setBusy(key);
    await resolve(ix, body);
    setBusy(null);
  };
  const toggle = (key: string) => setPicked((p) => {
    const next = new Set(p);
    if (next.has(key)) next.delete(key); else next.add(key);
    return next;
  });
  const chat = options.find((o) => o.kind === "chat");
  const answer = text.trim();
  // digits tick the boxes by place (their key badges are the computer's own digits); Enter is the submit button's send
  const plain = options.filter((o) => !o.kind);
  const keys = useCardKeys({
    digit: (n) => {
      const o = plain[n - 1];
      if (!o || busy !== null) return false;
      toggle(o.key);
      return true;
    },
    enter: () => {
      if (busy !== null || (picked.size === 0 && !answer)) return false;
      void go({ options: options.map((o) => o.key).filter((k) => picked.has(k)), text: answer || undefined }, "multi");
    },
  });
  const tapGuard = useChatCardTapGuard(ix);
  return (
    <Card
      kicker={[d.header, t("chat-interactions.multi_select")].filter(Boolean).join(" · ")}
      title={d.question || t("chat-interactions.multi_title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={ix.no_hand_back ? undefined : () => go({ dismiss: true }, "dismiss")}
      busy={busy !== null}
      footer={
        <>
          {chat && (
            <Button busy={busy === chat.key} disabled={busy !== null} onClick={() => go({ option: chat.key }, chat.key)}>
              {t("chat-interactions.say_in_chat")}
            </Button>
          )}
          <span className="flex-1" />
          <Button
            tone="primary"
            busy={busy === "multi"}
            disabled={busy !== null || (picked.size === 0 && !answer)}
            onClick={() => go({ options: options.map((o) => o.key).filter((k) => picked.has(k)), text: answer || undefined }, "multi")}
          >
            {d.submit === "Next" ? t("chat-interactions.next_question") : t("chat-interactions.submit")}
            {keys.focused && <Kbd>Enter</Kbd>}
          </Button>
        </>
      }
    >
      {d.detail && <ScrollFold className={DETAIL_FOLD} data-screen-detail>{d.detail}</ScrollFold>}
      <div className="flex flex-col gap-0.5">
        {plain.map((o, i) => (
          <button
            key={o.key}
            type="button"
            role="checkbox"
            aria-checked={picked.has(o.key)}
            disabled={busy !== null}
            data-tap-answer
            onClick={() => toggle(o.key)}
            className="touch-hit -mx-1.5 flex items-start gap-2 rounded-control px-1.5 py-1.5 text-left transition-colors hover:bg-hover disabled:opacity-60"
          >
            <span className={`mt-[1px] flex h-4.5 min-w-4.5 shrink-0 items-center justify-center rounded-[5px] px-1 font-mono text-[10.5px] shadow-hairline ${picked.has(o.key) ? "bg-accent text-white" : "bg-field text-ink-2"}`}>
              {picked.has(o.key) ? "✓" : o.key}
            </span>
            {keys.focused && i < 9 && <DigitHint n={i + 1} />}
            <span className="min-w-0">
              <OptionLabel label={o.label} />
              {o.detail && <span className="block text-[12px] text-ink-3">{o.detail}</span>}
            </span>
          </button>
        ))}
        {field && (
          <input
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder={t("chat-interactions.other_answer")}
            disabled={busy !== null}
            className="mt-1 h-8 min-w-0 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3"
          />
        )}
      </div>
    </Card>
  );
}

/**
 * An MCP server's form or link (docs/protocol/protocol.md "MCP elicitation"), as Android's ElicitationFields: a form in the
 * schema subset lib/elicitation.ts checks (anything else goes to the terminal), or a link that has to be opened from
 * here before "Opened, continue". "Submit" accepts, "Deny" declines, "Cancel" cancels; × hands it back to the computer as on every card.
 */
function ElicitationCard({ ix }: { ix: Interaction }) {
  const form = ix.elicitation;
  const url = form?.mode === "url";
  // a card that moves on the page (the one above it answered) is drawn anew: it picks up what was typed and opened
  const [values, setValues] = useState<Values>(() => elicitationDraft(ix.id)?.values ?? defaults(form?.requested_schema));
  const [opened, setOpened] = useState<string | undefined>(() => elicitationDraft(ix.id)?.opened);
  const canApprove = useStore(() => interactionCanAnswer(ix, getSession(ix.machine_id, ix.session_id)));
  useEffect(() => saveElicitationDraft(ix.id, form?.requested_schema, { values, opened }), [ix.id, form?.requested_schema, values, opened]);
  const [busy, setBusy] = useState<ElicitationAction | "dismiss" | null>(null);
  const go = async (body: Record<string, unknown>, key: ElicitationAction | "dismiss") => {
    setBusy(key);
    await resolve(ix, body);
    setBusy(null);
  };
  const act = (action: ElicitationAction) => {
    const body = elicitationResolution(ix, action, values, opened);
    if (body) void go(body, action);
  };
  const set = (key: string, value: unknown) => {
    setValues((cur) => {
      const next = { ...cur };
      if (value === undefined) delete next[key];
      else next[key] = value;
      return next;
    });
  };
  const schema = form?.requested_schema;
  const check = validate(schema, values);
  const canFill = form?.mode === "form" && check.supported;
  const ready = Boolean(elicitationResolution(ix, "accept", values, opened));
  const target = urlTarget(form?.url);
  const source = elicitationSource(ix);
  const hint = "text-[12px] leading-snug text-ink-3";
  // a form answers field by field; of the card's own keys it takes only Esc back to the composer
  const keys = useCardKeys();
  const tapGuard = useChatCardTapGuard(ix);
  return (
    <Card
      kicker={url ? t("chat-interactions.url_kicker") : t("chat-interactions.form_kicker")}
      title={url ? t("chat-interactions.url_title") : t("chat-interactions.form_title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={ix.no_hand_back ? undefined : () => go({ dismiss: true }, "dismiss")}
      busy={busy !== null}
      footer={
        <>
          <Button tone="quiet" busy={busy === "cancel"} disabled={busy !== null} onClick={() => act("cancel")}>
            {t("chat-interactions.elicitation_cancel")}
          </Button>
          <span className="flex gap-1.5">
            <Button tone="quiet" busy={busy === "decline"} disabled={busy !== null} onClick={() => act("decline")}>
              {t("chat-interactions.elicitation_decline")}
            </Button>
            {(url || canFill) && (
              <Button tone="primary" busy={busy === "accept"} disabled={busy !== null || !ready} onClick={() => act("accept")}>
                {url ? t("chat-interactions.opened_continue") : t("chat-interactions.submit")}
              </Button>
            )}
          </span>
        </>
      }
    >
      {source.length > 0 && <div className={cx("mb-1.5", hint)}>{source.map((l) => <span key={l} className="block">{l}</span>)}</div>}
      {form?.message?.trim() && (
        <div className="mb-2 text-ink-2">
          <Markdown text={form.message} small />
        </div>
      )}
      {url ? (
        <div className="flex flex-col gap-1.5">
          <div className={hint}>
            <span className="block">{t("chat-interactions.url_server", { server: ix.mcp_server_name?.trim() ? mcpServerLabel(ix.mcp_server_name.trim()) : t("chat-interactions.url_server_unknown") })}</span>
            <span className="block">{t("chat-interactions.url_host", { host: urlHost(form?.url) ?? t("chat-interactions.url_invalid") })}</span>
          </div>
          <code className="rounded-[8px] bg-inset px-2.5 py-2 font-mono text-[11.5px] leading-[1.5] break-all text-ink-2 shadow-hairline">{form?.url}</code>
          <p className={hint}>{t("chat-interactions.url_hint")}</p>
          <div className="flex items-center gap-2">
            {/* a link, not window.open: a click on it is never blocked, and the MCP server's page gets neither a handle on
                this one (noopener) nor the hub's address (noreferrer) */}
            <Button icon="external" href={target} disabled={!target || !canApprove || busy !== null} onClick={() => setOpened(target)}>
              {t("chat-interactions.open_link")}
            </Button>
            {opened && <Icon name="check" size={13} stroke={2.6} className="text-green" />}
          </div>
        </div>
      ) : !canFill ? (
        <p className="text-[12.5px] leading-relaxed text-ink-2">{t("chat-interactions.unsupported_form")}</p>
      ) : (
        <div className="flex flex-col gap-3">
          {fields(schema).map((f) => (
            <ElicitationField key={f.key} field={f} value={values[f.key]} error={check.errors[f.key]} onChange={(v) => set(f.key, v)} />
          ))}
        </div>
      )}
    </Card>
  );
}

const inputType = (f: Field) =>
  f.secret ? "password" : f.type !== "string" ? "text" : ({ email: "email", uri: "url", date: "date" } as Record<string, string>)[String(f.schema.format)] ?? "text";

/** One field of an MCP form: its title ("Required" / "Optional"), what it is for, the control, and what is wrong with it. */
function ElicitationField({ field: f, value, error, onChange }: { field: Field; value: unknown; error?: string; onChange: (v: unknown) => void }) {
  const input = useRef<HTMLInputElement>(null);
  useLayoutEffect(() => {
    const el = input.current;
    return () => {
      if (el) el.value = "";
    };
  }, []);
  const id = useId();
  const text = f.type !== "boolean" && f.choices.length === 0;
  const list = Array.isArray(value) ? value : [];
  const label = f.required ? t("chat-interactions.field_required", { title: f.title }) : t("chat-interactions.field_optional", { title: f.title });
  return (
    <div role={text ? undefined : "group"} aria-labelledby={text ? undefined : `${id}-label`} className="flex flex-col gap-1">
      <label id={`${id}-label`} htmlFor={text ? id : undefined} className="text-[12.5px] font-medium text-ink">{label}</label>
      {f.description && <span className="text-[12px] leading-snug text-ink-3">{f.description}</span>}
      {f.type === "boolean" ? (
        <div className="flex gap-3">
          {([true, false] as const).map((b) => (
            <Choice key={String(b)} on={value === b} label={b ? t("chat-interactions.yes") : t("chat-interactions.no")} onClick={() => onChange(b)} marker />
          ))}
        </div>
      ) : f.choices.length > 0 ? (
        <div className="flex flex-col gap-0.5">
          {f.choices.map((c, i) => {
            const on = f.type === "array" ? list.includes(c) : value === c;
            const pick = () => onChange(f.type === "array" ? (on ? list.filter((x) => x !== c) : [...list, c]) : c);
            return <Choice key={String(c)} on={on} multi={f.type === "array"} label={f.choiceLabels[i] ?? String(c)} onClick={pick} marker />;
          })}
        </div>
      ) : (
        <input
          id={id}
          ref={f.secret ? input : undefined}
          type={inputType(f)}
          inputMode={numberInputMode(f)}
          value={value === undefined ? "" : String(value)}
          // an emptied field is not filled in: a required one says so, an optional one is left out
          onChange={(e) => onChange(e.target.value === "" ? undefined : e.target.value)}
          placeholder={f.schema.format === "date-time" ? "2026-01-01T09:00:00Z" : undefined}
          autoComplete={f.secret ? "one-time-code" : "off"}
          autoCapitalize={f.secret ? "off" : undefined}
          autoCorrect={f.secret ? "off" : undefined}
          {...(f.secret ? SECRET_INPUT : {})}
          aria-invalid={error ? true : undefined}
          className={cx(
            "h-8 w-full min-w-0 rounded-control bg-surface px-2.5 text-[13px] text-ink shadow-btn outline-none placeholder:text-ink-3",
            error && "ring-1 ring-red",
          )}
        />
      )}
      {error && <span role="alert" className="text-[12px] text-red">{error}</span>}
      {f.secret && <span className="text-[12px] leading-snug text-ink-3">{t("chat-interactions.secret_note")}</span>}
      {!f.required && value !== undefined && (
        <button type="button" onClick={() => onChange(undefined)} className="touch-hit self-start rounded-[5px] text-[12px] whitespace-nowrap text-ink-3 hover:text-ink">
          {t("chat-interactions.clear_field")}
        </button>
      )}
    </div>
  );
}

/** What an answered card was about, in one line of plain text: what it asked (interactionAsk), a plan's first line;
 *  empty when it says nothing of its own (never its kind's title, "Plan awaits your approval", which would read as still waiting). */
export function receiptAbout(ix: Interaction): string {
  const raw = ix.kind === "plan" ? ((ix.plan ?? "").split("\n").find((l) => l.trim()) ?? "").replace(/^\s*#+\s*/, "") || ix.summary || "" : interactionAsk(ix);
  return plainMarkdown(raw).replace(/\s+/g, " ").trim();
}

/** A card this page answered: what was asked and the picks, read-only, for a moment (lib/store.ts endInteraction). */
export function AnsweredCard({ card }: { card: Answered }) {
  // handed back: its head says so, and the line under it is which request went back to the computer, not those words again
  const handedBack = card.resolution.dismiss === true;
  const lines = handedBack ? [] : answerLines(card.ix, card.resolution);
  // a card with one unlabeled answer ("Allow", "Approve", a terminal option) says what it was about (#4353): which command was
  // allowed, not only that something was
  const about = handedBack || (lines.length === 1 && !lines[0].label) ? receiptAbout(card.ix) : "";
  return (
    <div role="status" data-answered={card.ix.id} className="shrink-0 overflow-hidden rounded-card bg-surface px-3.5 py-2.5 shadow-card" style={{ animation: `fade-up 300ms ${EASE} both` }}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex-1">
          <div className="mb-1 flex items-center gap-1.5 text-[11px] font-medium text-green">
            <Icon name="check" size={11} stroke={3} />
            {handedBack ? t("chat-interactions.handed_back") : t("chat-interactions.answered")}
          </div>
          <div className="flex flex-col gap-1">
            {about && <span data-answered-about className="block truncate text-[12px] text-ink-3" title={about}>{about}</span>}
            {lines.map((l, i) => (
              <div key={i} className="min-w-0 text-[13px] leading-snug">
                {l.label && <span className="block truncate text-[12px] text-ink-3">{l.label}</span>}
                <span className="block whitespace-pre-wrap text-ink">{l.answer}</span>
              </div>
            ))}
          </div>
        </div>
        <button
          type="button"
          aria-label={t("chat-interactions.collapse")}
          title={t("chat-interactions.collapse")}
          onClick={() => dropAnswered(card.ix.id)}
          className="touch-hit flex size-6 shrink-0 items-center justify-center rounded-[6px] text-ink-3 transition-colors hover:bg-hover hover:text-ink"
        >
          <Icon name="x" size={14} stroke={2.2} />
        </button>
      </div>
    </div>
  );
}

/** An answered card in one line: the answer, and what it answered (the question, the command allowed). */
function answeredLine(card: Answered): { answer: string; about: string } {
  if (card.resolution.dismiss === true) return { answer: t("chat-interactions.handed_back"), about: receiptAbout(card.ix) };
  const lines = answerLines(card.ix, card.resolution);
  const answer = lines.map((l) => l.answer.replace(/\s+/g, " ").trim()).join(" · ");
  const labels = lines.map((l) => l.label).filter(Boolean).join(" · ");
  return { answer, about: labels || receiptAbout(card.ix) };
}

/**
 * What this page just answered, as one line over the slot instead of a card each (card-stack.ts): "Answered" with the
 * answer and what it was about for one, "4 answered · All allowed" for several. It goes ANSWERED_SHOWN_MS after the
 * last answer, unless the pointer or the keyboard is on it; tapping it lists each answer (and holds it until closed).
 * Every answer keeps its own data-answered element, listed for screen readers while the line is closed.
 */
export function AnsweredSummary({ answered }: { answered: Answered[] }) {
  const [open, setOpen] = useState(false);
  const [hover, setHover] = useState(false);
  const [focus, setFocus] = useState(false);
  const [kept, setKept] = useState<Answered[]>([]);
  const [released, setReleased] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  const listId = useId();
  const held = open || hover || focus;
  const latest = latestAnswer(answered);
  const hideAt = answeredHideAt(latest, released);
  const visible = answeredShown({ now, latest, released, held });
  useEffect(() => {
    if (held || !visible) return;
    const id = setTimeout(() => setNow(Date.now()), Math.max(0, hideAt - Date.now()));
    return () => clearTimeout(id);
  }, [held, visible, hideAt]);
  const release = () => {
    const at = Date.now();
    setReleased(at);
    setNow(at);
  };
  const items = open ? keptAnswers(kept, answered) : answered;
  if (!visible || !items.length) return null;
  const tally = answeredTally(items);
  const one = items.length === 1 ? answeredLine(items[0]) : null;
  const label = one
    ? items[0].resolution.dismiss === true ? t("chat-interactions.handed_back") : t("chat-interactions.answered")
    : t("chat-interactions.answered_count", { count: items.length });
  const detail = one
    ? items[0].resolution.dismiss === true ? one.about : [one.answer, one.about].filter(Boolean).join(" · ")
    : tallyDetail(tally) || answeredLine(items[items.length - 1]).about;
  const toggle = () => {
    if (open) {
      setOpen(false);
      setKept([]);
      release();
    } else {
      setKept(answered);
      setOpen(true);
    }
  };
  return (
    <div
      data-answered-summary
      className="shrink-0 overflow-hidden rounded-card bg-surface shadow-card"
      style={{ animation: `fade-up 250ms ${EASE} both` }}
      onPointerEnter={(e) => e.pointerType === "mouse" && setHover(true)}
      onPointerLeave={(e) => {
        if (e.pointerType !== "mouse") return;
        setHover(false);
        release();
      }}
      // the keyboard on it holds it (someone reading it with a screen reader or Tab); a click's focus does not, or a line
      // opened and closed with the pointer would stay
      onFocus={(e) => e.target.matches(":focus-visible") && setFocus(true)}
      onBlur={(e) => {
        if (e.currentTarget.contains(e.relatedTarget)) return;
        setFocus(false);
        release();
      }}
    >
      <button
        type="button"
        aria-expanded={open}
        aria-controls={listId}
        onClick={toggle}
        className="flex h-11 w-full items-center gap-2 px-3.5 text-left transition-colors hover:bg-hover"
      >
        <Icon name="check" size={12} stroke={3} className="shrink-0 text-green" />
        <span aria-live="polite" className="shrink-0 text-meta font-medium whitespace-nowrap text-ink">{label}</span>
        <span className="min-w-0 flex-1 truncate text-meta text-ink-3" title={detail}>{detail}</span>
        <Icon name="chevron" size={14} stroke={2.2} className={cx("shrink-0 text-ink-3 transition-transform duration-200", open && "rotate-180")} />
      </button>
      <ul
        id={listId}
        className={open ? "flex max-h-(--answered-max) flex-col overflow-y-auto overscroll-contain border-t border-line px-3.5 py-1" : "sr-only"}
        style={{ "--answered-max": ANSWERED_LIST_MAX } as React.CSSProperties}
      >
        {items.map((card) => {
          const { answer, about } = answeredLine(card);
          return (
            <li key={card.ix.id} data-answered={card.ix.id} className="flex min-h-9 min-w-0 items-center gap-2 py-1 text-meta">
              <span className="max-w-[45%] shrink-0 truncate font-medium text-ink" title={answer}>{answer}</span>
              {about && <span data-answered-about className="min-w-0 flex-1 truncate text-label text-ink-3" title={about}>{about}</span>}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** What is drawn right under a card: views/assistant/Tasks puts the suggested rule's line here (#5802), whose code
 *  loads with the first card that carries one. A slot, not an import: Tasks imports this file. */
export const underCard: { line?: (ix: Interaction & { disabled?: boolean }) => React.ReactNode; /** The cards (by id) Always was answered on: their line stays to say what was made. */ made?: Set<string> } = {};

/** Under a card this page can't answer (#7497): why, and Live link where there is one. Its own chunk, loaded with the
 *  first blocked card, since most cards are answerable; until it is in, the card stays as it is. */
const CardBlockedLine = lazyPage(() => import("./CardBlockedLine").then((m) => m.CardBlockedLine));

export function InteractionCard({ ix }: { ix: Interaction }) {
  // a child session's card on its parent's page (#5120): its row is the one the parent's chat holds (a paging hub's
  // summary, which the store's complete rows never have)
  const child = useChildRow(ix.machine_id, ix.session_id);
  const block = useStore(() => answerBlock(ix, child ?? getSession(ix.machine_id, ix.session_id)));
  const canApprove = block === null;
  const managerWork = ix.kind === "question" && (ix.tool_name === "persona_triage" || isSessionReview(ix));
  // an answer still on its way (the hub away, apiOp retrying) holds the card, also after leaving the chat and coming back
  const { pending, line } = usePendingLine(ix);
  // a card a task's supervisor holds is not the user's yet: what it asks, read-only, never its answers
  const held = supervisorHeld(ix);
  useOwnerCardSeen(ix, !held);
  const renderer = useCardRenderer(ix.kind);
  const decision = useContext(DecisionCardContext);
  const card = held ? <SupervisorHeldCard key={ix.id} ix={ix} />
    : isSessionReview(ix) ? <SessionReviewCard key={ix.id} ix={ix} />
    : ix.kind === "question" && ix.tool_name === "persona_tool_loop" ? <ToolLoopCard key={ix.id} ix={ix} />
    : ix.kind === "question" ? <QuestionCard key={decision ? JSON.stringify(ix.questions ?? []) : undefined} ix={ix} canApprove={canApprove} />
    : ix.kind === "plan" ? <PlanCard ix={ix} />
    : isSecretRun(ix) ? <SecretRunCard key={ix.id} ix={ix} />
    : isSecretHandoff(ix) ? <SecretHandoffCard key={ix.id} ix={ix} />
    : ix.kind === "permission" ? <PermissionCardFrame key={ix.id} ix={ix} />
    : ix.kind === "elicitation" ? <ElicitationCard ix={ix} />
    // a kind the core has no card for, drawn by the module that names it (core/module-host.ts `cardRenderers`)
    : renderer ? <ModuleCard key={ix.id} module={renderer} interaction={ix} /> : <ScreenCard ix={ix} />;
  const marks = <InteractionMarks interaction={ix} />;
  // a permission card says it in its body (PermissionBody); a secret run's, hand-off's or command input's card has none,
  // so it says it here
  const from = ix.folded_under && (ix.kind !== "permission" || isSecretRun(ix) || isSecretHandoff(ix) || isCommandInput(ix)) ? permissionFrom(ix) : "";
  // what it asks in one line, as the folded cards' line says it (the command of a tool approval, a question)
  const about = plainMarkdown(cardLine(ix)).replace(/\s+/g, " ").trim();
  const disabled = pending || (!canApprove && !managerWork);
  const separateNavigation = decision && (disabled || Boolean(renderer && ix.kind === "screen"));
  // data-card: which card this is, for the sync scenarios (spec/fixtures/sync); `contents` keeps the layout as it was.
  // The fieldset is always there (switching it in would remount the card): disabled, it stops every answer and the
  // card's keys (lib/card-keys.ts) while the session can't be approved here or an answer is on its way. A manager work
  // card stays mounted for its local defer/dismiss actions; QuestionCard disables every message-sending control instead.
  return (
    <div data-card={ix.id} className="contents">
      <CardMarks.Provider value={marks}>
        <SendingAnswer.Provider value={line}>
          <AskedFrom.Provider value={from}>
            <CardAbout.Provider value={about}>
            {/* a question a supervisor passed up may carry a suggested rule: its line goes under the card (#5802) */}
            {separateNavigation && <div className="mb-1 flex items-center gap-3 px-1 text-label text-ink-2">{decision.navigation}</div>}
            <DecisionCardContext.Provider value={separateNavigation ? { ...decision, navigation: undefined } : decision}>
              <fieldset disabled={disabled} className={canApprove ? "contents" : undefined}>{card}</fieldset>
            </DecisionCardContext.Provider>
            {/* a manager work card still takes its local defer and dismiss here: no "can't answer here" line */}
            {block && !managerWork && !held && <Suspense fallback={null}><CardBlockedLine ix={ix} block={block} /></Suspense>}
            {!held && underCard.line?.(canApprove ? ix : { ...ix, disabled: true })}
            </CardAbout.Provider>
          </AskedFrom.Provider>
        </SendingAnswer.Provider>
      </CardMarks.Provider>
    </div>
  );
}

/** The cards a Persona chat's own turn waits on (lib/turn-asks.ts): each open in full, in the order they came, above the
 *  dock's capsules. Never folded behind a position chip, a "N pending" line or the Needs you tray: the chat cannot go on
 *  until one is answered. Takes only `TurnAsk`s, so no other card can be drawn here. */
export function TurnCards({ asks }: { asks: readonly TurnAsk[] }) {
  // their own box, which gives up its height first and scrolls (as the stack's does): the capsules under them stay in view
  return asks.length > 0 && <div data-turn-asks data-scroll-box className="-m-1 flex min-h-0 shrink flex-col gap-2 overflow-y-auto overscroll-contain p-1 empty:hidden">{asks.map((ix) => <InteractionCard key={ix.id} ix={ix} />)}</div>;
}

/**
 * The cards waiting on you, and the ones just answered, above the composer, as one slot: the first waiting card in full
 * with where it stands in the run ("2 / 6", which opens them all), the next taking its place once it is answered, so its
 * answers are where the last one's were; what was just answered is one line over it (AnsweredSummary). On a narrow
 * screen (#1996) they sit in a box that scrolls on its own; while the keyboard is up, one line for all of them (tapping
 * it puts the keyboard away), and on a phone on its side one line until tapped.
 */
export function PendingCards({ interactions, answered, delegated = [], offerNext, narrow, short = false, keyboard, onDismissKeyboard, reveal }: {
  interactions: Interaction[];
  answered: Answered[];
  /** The questions the assistant answered for the user (#544): shown with the answered ones, until put away. */
  delegated?: DelegatedAnswer[];
  /** This chat: offer what other chats wait on you with (#4091), one line under "Answered" once it has no card left. */
  offerNext?: { mid: string; sid: string };
  narrow: boolean;
  /** A phone on its side: all of them in one line until tapped. */
  short?: boolean;
  keyboard: boolean;
  onDismissKeyboard: () => void;
  /** Open the short-screen fold when the composer points to an existing card. */
  reveal?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const count = interactions.length;
  // stamp the chat the dock belongs to before any card paints, so a card that arrives after opening is judged against
  // the open-grace rather than mistaken for the opening itself
  useChatTapGuardView();
  useEffect(() => {
    if (!count) setOpen(false);
  }, [count]);
  useEffect(() => { if (short && reveal) setOpen(true); }, [short, reveal]);
  // the cards cut off below the box (each Card says): while any is, a stack of two or more says how many it holds
  const [cutCards, setCutCards] = useState<ReadonlySet<string>>(() => new Set());
  const report = useCallback((key: string, cut: boolean) => setCutCards((had) => {
    if (had.has(key) === cut) return had;
    const next = new Set(had);
    if (cut) next.add(key);
    else next.delete(key);
    return next;
  }), []);
  const ids = interactions.map((ix) => ix.id).join("\n");
  const run = useRun(ids);
  const slot = useSlotFocus(interactions[0]?.id);
  const { full, summary } = cardFold(count, { narrow, keyboard, open: open || (short && reveal === true), short });
  if (!count && !answered.length && !delegated.length) return null;
  const shown = showsAnswered({ narrow, keyboard, open, short });
  const done = shown
    ? [...delegated.map((d) => <DelegatedCardFrame key={d.ix.id} d={d} />), ...(answered.length ? [<AnsweredSummary key="answered" answered={answered} />] : [])]
    : [];
  // the cut-off count only while all the cards are opened: a stack folded to its first says where it stands instead (the
  // position chip). Each card has its own keyed provider, so a card moving up to first as the one above it goes keeps
  // its node, its typed answer and its focus.
  const stack = count > 1 && cutCards.size > 0 && full > 1 ? count : 0;
  // opened, the same chip folds them back: no line of its own under the cards, which would take a line of the dock's room
  // (a tall card's title then went out of view above the box) and put the control away from where it was opened
  const queue = count > 1 && (full === 1 || (open && !short && full > 1))
    ? { ...runPosition(run, count), open: full > 1, onToggle: () => setOpen(full === 1) }
    : null;
  const cards = interactions.map((ix, i) => (
    <CutReport.Provider key={ix.id} value={report}>
      <StackCount.Provider value={i === 0 ? stack : 0}>
        <QueueSlot.Provider value={i === 0 ? queue : null}><InteractionCard ix={ix} /></QueueSlot.Provider>
      </StackCount.Provider>
    </CutReport.Provider>
  ));
  // only right after answering here (the "Answered" receipt shows), never while this chat still has a card: no jump, no nag
  const onward = !count && shown && answered.length > 0 && offerNext ? <NextWaitingRow {...offerNext} /> : null;
  // a phone on its side, opened from its one line: one quiet button puts them back into it
  const fold = open && short && !keyboard ? (
    <Button tone="quiet" onClick={() => setOpen(false)} className="touch-hit -mt-1 self-center">
      {t("chat-interactions.collapse")}
    </Button>
  ) : null;
  if (!narrow) {
    return (
      <>
        {done}
        <div {...slot} className="contents">{cards.slice(0, full)}</div>
        {fold}
        {onward}
      </>
    );
  }
  return (
    <div className="flex min-h-0 shrink flex-col gap-2">
      {/* the box clips what is in it: its children keep a little room for their shadows (p-1; -m-1 gives it back) */}
      <div data-chat-cards data-scroll-box className={cx("-m-1 flex min-h-0 shrink flex-col overflow-y-auto overscroll-contain p-1", !done.length && !full && "hidden")}>
        {done.length > 0 && <div className="flex flex-col gap-2">{done}</div>}
        {/* unclipped once open (#3112): a clipping fold is the box a card's sticky footer sticks in, so a card taller than
            data-chat-cards (a long command) left its buttons below the fold, out of sight, with no way to bring them up */}
        {count > 0 && (
          <Collapse open={full >= 1} unclipped>
            <div {...slot} className={cx(done.length > 0 && "pt-2")}>{cards[0]}</div>
          </Collapse>
        )}
        {count > 1 && (
          <Collapse open={full > 1} unclipped>
            <div className="flex flex-col gap-2 pt-2">{cards.slice(1)}</div>
          </Collapse>
        )}
      </div>
      {/* the line for what is folded stays in view under the box (in it, a tall first card would scroll it away) */}
      {summary && (
        <FoldRow
          label={t("chat-interactions.pending", { count: summary.count })}
          detail={interactions.map(cardLine).join(" · ")}
          onClick={summary.kind === "typing" ? onDismissKeyboard : () => setOpen(true)}
        />
      )}
      {onward}
      {fold}
    </div>
  );
}

/** The cards seen waiting since the stack was last empty (card-stack.ts nextRun), from the waiting ids joined by "\n". */
function useRun(ids: string) {
  const [seen, setSeen] = useState<readonly string[]>(() => (ids ? ids.split("\n") : []));
  const next = nextRun(seen, ids ? ids.split("\n") : []);
  // adjusted while rendering (React's "storing information from previous renders"): the count is right on the frame
  // the card comes or goes, with no second render after it
  if (next !== seen) setSeen(next);
  return next;
}

/**
 * Answering with the keyboard goes on with the keyboard: when the slot's card is answered while the keyboard was on it
 * (its Enter, a digit, a focused button's Enter), the next card in the slot gets the focus, so Enter answers it in turn.
 * A pointer answer leaves the focus where the browser puts it: a card that took it would show the keyboard's hints.
 */
function useSlotFocus(first: string | undefined) {
  const ref = useRef<HTMLDivElement>(null);
  const keyed = useRef(false);
  const prev = useRef(first);
  useLayoutEffect(() => {
    if (prev.current === first) return;
    prev.current = first;
    const el = ref.current;
    const active = document.activeElement;
    if (!first || !el || !keyed.current || (active && active !== document.body && !el.contains(active))) return;
    el.querySelector<HTMLElement>("[data-card-keys]")?.focus({ preventScroll: true });
  }, [first]);
  return {
    ref,
    onKeyDown: () => {
      keyed.current = true;
    },
    onPointerDown: () => {
      keyed.current = false;
    },
    onBlur: (e: React.FocusEvent<HTMLDivElement>) => {
      // focus that goes somewhere else ends it; focus lost with the answered card (to nothing) does not
      if (e.relatedTarget && !ref.current?.contains(e.relatedTarget)) keyed.current = false;
    },
  };
}

/** One line in place of folded cards: how many, what they are, ›. */
function FoldRow({ label, detail, onClick }: { label: string; detail: string; onClick: () => void }) {
  return (
    <button
      type="button"
      data-chat-cards-fold
      // the composer keeps its focus until the click (Android): the row must not move out from under the finger first
      onMouseDown={(e) => e.preventDefault()}
      onClick={onClick}
      className="flex h-11 shrink-0 items-center gap-2 rounded-card bg-surface px-3.5 text-left shadow-card transition-colors hover:bg-hover"
      style={{ animation: `fade-up 250ms ${EASE} both` }}
    >
      <span className="size-1.5 shrink-0 rounded-full bg-attention" />
      <span className="shrink-0 text-[13px] font-medium whitespace-nowrap text-ink">{label}</span>
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{detail}</span>
      <Icon name="chevronRight" size={14} stroke={2.2} className="shrink-0 text-ink-3" />
    </button>
  );
}

/** "N more need you · Next: <agent> · <what it asks> ›", opening that chat with that card first (lib/card-focus.ts).
 *  Mounted only right after an answer here: the cards of other chats coming and going redraw this, not the chat. */
function NextWaitingRow({ mid, sid }: { mid: string; sid: string }) {
  const next = useStore((s) => nextWaiting(s.interactions, mid, sid));
  const session = useStore((s) => (next ? (s.sessions[next.ix.machine_id] ?? []).find((x) => x.session_id === next.ix.session_id) : undefined));
  if (!next) return null;
  const { ix, count } = next;
  // named as "Overview"'s "Needs you" row names it, worded as the folded cards' line (cardLine)
  const title = ix.session_title?.trim() || (session ? sessionTitle(session) : ix.cwd || t("home.session"));
  const ask = plainMarkdown(cardLine(ix)).replace(/\s+/g, " ").trim();
  const to: Route = { name: "session", mid: ix.machine_id, sid: ix.session_id };
  return (
    <a
      href={href(to)}
      data-chat-next-waiting
      onClick={(e) => onLinkClick(e, to, cardState(ix.id))}
      className="flex h-11 shrink-0 items-center gap-2 rounded-card bg-surface px-3.5 shadow-card transition-colors hover:bg-hover"
      style={{ animation: `fade-up 250ms ${EASE} both` }}
    >
      <span className="size-1.5 shrink-0 rounded-full bg-attention" />
      <span className="shrink-0 text-[13px] font-medium whitespace-nowrap text-ink">{t("chat-interactions.next_waiting", { count })}</span>
      <span className="min-w-0 flex-1 truncate text-[12.5px] text-ink-3">{t("chat-interactions.next_waiting_detail", { title, ask })}</span>
      <Icon name="chevronRight" size={14} stroke={2.2} className="shrink-0 text-ink-3" />
    </a>
  );
}
