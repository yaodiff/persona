// What a card that was just answered shows in place of its choices: each question and the pick, or how a plan,
// permission or terminal dialog was answered. The resolution is what was sent, or — when the hub says the card was
// answered already (POST …/resolve → {already, resolution}) — the answer that took effect, as its history keeps it.
import { t } from "../i18n";
import { fields, type Field } from "./elicitation";
import { optionDisplay, pickedOption } from "./tool-detail";
import type { Interaction, Question } from "./types";

export type Resolution = Record<string, unknown>;

/** The hub's answer to POST /interactions/{id}/resolve: {ok}, or {ok, already, status, resolution} for a card that
 *  was over already (answered elsewhere, or by this page when its event was missed). */
export interface ResolveResult {
  ok?: boolean;
  already?: boolean;
  status?: string;
  resolution?: Resolution;
}

export interface AnswerLine {
  /** What was asked (a question's wording); empty for a card with one answer and nothing to label it. */
  label: string;
  answer: string;
}

const MODE_LABEL: Record<string, string> = { acceptEdits: t("lib-answered.mode_accept_edits"), bypassPermissions: t("lib-answered.mode_bypass"), default: t("lib-answered.mode_default") };

const str = (v: unknown) => (typeof v === "string" ? v : "");

/** A question's answer as the card shows it: the options picked without Claude's "(Recommended)" suffix (the card said
 *  it with its tag; the answer sent back keeps the whole label), words typed instead as they were typed. */
function shownAnswer(q: Question, answer: string): string {
  // the option an answer names, compared as pickedOption does (the label kept raw, the answer trimmed)
  const option = (a: string) => (q.options ?? []).find((o) => pickedOption(o.label, a, false))?.label;
  const whole = option(answer);
  // a multi-select answer is its labels joined by ", ", as the cards send it
  const picks = whole !== undefined ? [whole] : q.multi_select ? answer.split(", ").map(option) : [];
  if (!picks.length || picks.some((p) => p === undefined)) return answer;
  return picks.map((p) => optionDisplay(p!.trim()).text).join(", ");
}

/** The lines an answered card shows. */
export function answerLines(ix: Interaction, r: Resolution): AnswerLine[] {
  if (r.dismiss === true) return [{ label: "", answer: t("lib-answered.dismissed") }];
  if (r.answered_by === "assistant" && ix.kind !== "question") return [{ label: "", answer: t("lib-answered.by_assistant") }];
  switch (ix.kind) {
    case "question": {
      const answers = (r.answers ?? {}) as Record<string, unknown>;
      return (ix.questions ?? []).map((q) => ({ label: q.question, answer: shownAnswer(q, str(answers[q.question])) || t("lib-answered.unanswered") }));
    }
    case "plan":
      if (r.approve === true) return [{ label: "", answer: MODE_LABEL[str(r.mode)] ? t("lib-answered.approved_mode", { mode: MODE_LABEL[str(r.mode)] }) : t("lib-answered.approved") }];
      return [{ label: "", answer: str(r.feedback) ? t("lib-answered.rejected_feedback", { feedback: str(r.feedback) }) : t("lib-answered.rejected") }];
    case "permission":
      if (r.allow === true) return [{ label: "", answer: r.always === true ? t("lib-answered.always_allowed") : t("lib-answered.allowed") }];
      if (r.allow === false) return [{ label: "", answer: t("lib-answered.denied") }];
      return [{ label: "", answer: t("lib-answered.handled") }];
    case "elicitation":
      return elicitationLines(ix, r);
    default: {
      const options = ix.dialog?.options ?? [];
      const labelOf = (key: string) => {
        const label = options.find((o) => o.key === key)?.label;
        return label === undefined ? key : optionDisplay(label).text;
      };
      const typed = str(r.text) && (ix.secret || r.text === "<redacted>" ? t("lib-answered.entered") : str(r.text));
      const picked = Array.isArray(r.options) ? (r.options as unknown[]).map((k) => labelOf(str(k))) : str(r.option) ? [labelOf(str(r.option))] : [];
      const answer = [...picked, typed].filter(Boolean).join(t("lib-answered.separator"));
      return [{ label: "", answer: answer || t("lib-answered.handled") }];
    }
  }
}

/** An MCP form or link: how it was answered, and for a form what was filled in (a secret only says it was). */
function elicitationLines(ix: Interaction, r: Resolution): AnswerLine[] {
  if (r.action === "decline") return [{ label: "", answer: t("lib-answered.elicitation_declined") }];
  if (r.action === "cancel") return [{ label: "", answer: t("lib-answered.elicitation_cancelled") }];
  if (r.action !== "accept") return [{ label: "", answer: t("lib-answered.handled") }];
  if (ix.elicitation?.mode === "url") return [{ label: "", answer: t("lib-answered.elicitation_opened") }];
  const content = (r.content ?? {}) as Record<string, unknown>;
  const shown = (f: Field, v: unknown): string => {
    if (f.secret) return t("lib-answered.elicitation_hidden");
    if (typeof v === "boolean") return v ? t("lib-answered.elicitation_yes") : t("lib-answered.elicitation_no");
    const label = (x: unknown) => f.choiceLabels[f.choices.indexOf(x as string)] ?? String(x);
    return Array.isArray(v) ? v.map(label).join(t("lib-answered.separator")) : label(v);
  };
  const lines = fields(ix.elicitation?.requested_schema).filter((f) => content[f.key] !== undefined).map((f) => ({ label: f.title, answer: shown(f, content[f.key]) }));
  return lines.length ? lines : [{ label: "", answer: t("lib-answered.elicitation_submitted") }];
}

/** Why a card could not be answered any more (410, its `status`). */
export function endedText(status: unknown): string {
  switch (status) {
    case "expired":
      return t("lib-answered.ended_expired");
    case "cancelled":
      return t("lib-answered.ended_cancelled");
    case "dismissed":
      return t("lib-answered.ended_dismissed");
    default:
      return t("lib-answered.ended_generic");
  }
}
