// What an opened ExitPlanMode / AskUserQuestion / ask_user step shows instead of its raw JSON (iOS ToolDetail,
// AskUserDetail; Android PlanDetail, QuestionsDetail): the plan as Markdown; the questions with their options; for the
// assistant's ask_user also what the user picked, ticked (its card is gone once answered, so the conversation keeps
// what was asked and what came back here).
import { askUserAnswers, optionDisplay, pickedOption, toolQuestions, typedAnswer, type ToolQuestion } from "../../lib/tool-detail";
import { stripToolResultTimestampHeaders } from "../../lib/turns";
import { stepToolName } from "./step-detail-kind";
import type { Item } from "../../lib/types";
import { cx } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { t } from "../../i18n";
import { RecommendedTag } from "../../ui/RecommendedTag";

export { hasStepDetail, showsResult } from "./step-detail-kind";

export function StepDetail({ use, result }: { use: Item; result?: Item }) {
  const name = stepToolName(use.tool_name);
  const input = (use.tool_input ?? {}) as Record<string, unknown>;
  if (name === "ExitPlanMode") {
    const plan = typeof input.plan === "string" ? input.plan : "";
    return plan ? (
      <div className="min-w-0" data-step-detail="plan">
        <Markdown text={plan} small />
      </div>
    ) : null;
  }
  const questions = toolQuestions(input);
  const ask = name === "ask_user";
  const answers = ask && result?.text ? askUserAnswers(stripToolResultTimestampHeaders(result.text), questions) : {};
  if (!questions.length) return null;
  return (
    <div className="flex min-w-0 flex-col gap-3" data-step-detail={ask ? "ask-user" : "questions"}>
      {questions.map((q, i) => (
        <Question key={i} q={q} n={questions.length > 1 ? i + 1 : undefined} answer={ask ? (answers[q.question] ?? null) : undefined} />
      ))}
      {ask && result && !result.is_error && !Object.keys(answers).length && <span className="text-[12px] text-ink-3">{t("chat-stepdetail.no_answer")}</span>}
    </div>
  );
}

/** One question with its options. `answer`: undefined for AskUserQuestion (the result says what was picked), else
 *  what the user answered (null: nothing), so each option is ticked or not. */
function Question({ q, n, answer }: { q: ToolQuestion; n?: number; answer?: string | null }) {
  const ticks = answer !== undefined;
  const typed = ticks ? typedAnswer(q, answer ?? undefined) : undefined;
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {(q.header || q.multiSelect) && (
        <span className="flex flex-wrap gap-1">
          {q.header && <Tag>{q.header}</Tag>}
          {q.multiSelect && <Tag>{t("chat-stepdetail.multi_select")}</Tag>}
        </span>
      )}
      <span className="text-[13px] font-medium text-ink [overflow-wrap:anywhere]">
        {n ? `${n}. ` : ""}
        {q.question.trim()}
      </span>
      {q.options.map((o) => (
        <Choice key={o.label} label={o.label} option description={o.description} picked={ticks ? pickedOption(o.label, answer ?? undefined, q.multiSelect) : undefined} />
      ))}
      {typed && <Choice label={typed} picked />}
    </div>
  );
}

function Tag({ children }: { children: string }) {
  return <span className="rounded-[4px] bg-field px-1 text-[11px] font-medium whitespace-nowrap text-ink-2">{children}</span>;
}

/** An option: a dot, or (ask_user) a ring ticked when picked. */
function Choice({ label, option, description, picked }: { label: string; option?: boolean; description?: string; picked?: boolean }) {
  // only an option carries Claude's suffix; a typed answer is shown as written
  const shown = option ? optionDisplay(label.trim()) : { text: label.trim(), recommended: false };
  return (
    <span className="flex min-w-0 items-baseline gap-2 text-[12.5px] leading-[1.5]" data-picked={picked || undefined}>
      {picked === undefined ? (
        <span aria-hidden className="shrink-0 text-ink-3">·</span>
      ) : (
        <span aria-hidden className={cx("inline-flex size-[13px] shrink-0 translate-y-[2px] items-center justify-center self-start rounded-full border", picked ? "border-accent bg-accent text-white" : "border-line-strong")}>
          {picked && <Icon name="check" size={9} stroke={3} />}
        </span>
      )}
      <span className={cx("min-w-0 [overflow-wrap:anywhere]", picked === false ? "text-ink-3" : "text-ink-2", picked && "text-ink")}>
        {picked && <span className="sr-only">{t("chat-stepdetail.picked")}</span>}
        {shown.text}
        {shown.recommended && <RecommendedTag />}
        {description && <span className="text-ink-3">{t("chat-stepdetail.description", { text: description })}</span>}
      </span>
    </span>
  );
}
