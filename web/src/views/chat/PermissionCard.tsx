// Tool approval is not part of the chat's first screen: load the complete card with the first permission interaction,
// as CommandScreenCard does. Its optional question editor is a further chunk loaded only when that action opens.
import { Suspense, useRef, useState } from "react";
import { t } from "../../i18n";
import { ApprovalImpactLine, useApprovalReview } from "./ApprovalReview";
import { alwaysScope, offersAlways } from "../../lib/always-scope";
import { lazyPage } from "../../lib/lazy-page";
import { ownerApprovalAttempt, replyOwnerApproval } from "../../lib/owner-approval";
import { cardToolName } from "../../lib/turns";
import type { Interaction } from "../../lib/types";
import { Button, Kbd, toast } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { Markdown } from "../../ui/Markdown";
import { isCommandInput, permissionDetail, permissionReason, summaryExtra } from "../../lib/tool-detail";
import { DIFF_TOOLS, DiffBlocks, editDiffs } from "./ActionRow";
import { useChatCardTapGuard } from "./card-tap-guard";
import { CommandInputCard } from "./CommandInputCard";
import { RowConfirm } from "../settings/SettingsParts";
import { Card, CODE_BOX, permissionFrom, resolve, useCardKeys } from "./Interactions";
import { usePermissionText } from "./permission-text";

const ApprovalQuestion = lazyPage(() => import("./ApprovalQuestion").then((m) => m.ApprovalQuestion));

/** A permission card: a command waiting for a line (#14527) has a card of its own, in this chunk so the chat's first
 *  screen carries none of it; any other is a tool approval. */
export function PermissionCard({ ix }: { ix: Interaction }) {
  return isCommandInput(ix) ? <CommandInputCard ix={ix} /> : <ToolPermissionCard ix={ix} />;
}

function ToolPermissionCard({ ix }: { ix: Interaction }) {
  const [busy, setBusy] = useState(false);
  const [replyBusy, setReplyBusy] = useState(false);
  const [questionOpen, setQuestionOpen] = useState(false);
  const review = useApprovalReview(ix);
  const confirming = review.choice !== null;
  const replyAttempt = useRef<{ target: string; id: string } | null>(null);
  const editable = usePermissionText(ix, confirming);
  const go = async (body: Record<string, unknown>) => {
    setBusy(true);
    await resolve(ix, body);
    setBusy(false);
  };
  const replyPhrase = ix.owner_proposal ? ix.owner_approval_phrases?.[0] : undefined;
  const reply = async () => {
    if (!replyPhrase) return;
    setReplyBusy(true);
    const attempt = ownerApprovalAttempt(ix, replyAttempt.current);
    replyAttempt.current = attempt;
    const error = await replyOwnerApproval(ix, replyPhrase, attempt.id);
    if (error) toast(error, "error");
    else replyAttempt.current = null;
    setReplyBusy(false);
  };
  const approve = (asReply = false) => {
    if (review.request(asReply ? "reply" : "allow")) return;
    if (asReply) void reply();
    else void go(editable.allow());
  };
  const confirm = () => {
    if (!confirming || busy || replyBusy || !editable.canAllow) return;
    review.cancel();
    if (review.choice === "reply") void reply();
    else void go(editable.allow());
  };
  // Enter allows (the Allow button), except while the independent question
  // editor is open. Deny is never a key away.
  const keys = useCardKeys({
    enter: () => {
      if (confirming || busy || replyBusy || questionOpen || editable.composing || !editable.canAllow) return false;
      approve();
    },
  });
  // Opening confirmation is deliberate. Keep the guard for the incoming request, so its first confirmation tap lands.
  const tapGuard = useChatCardTapGuard(ix);
  return (
    <Card
      kicker={t("chat-interactions.permission_kicker")}
      title={t("chat-interactions.permission_title", { tool: cardToolName(ix) })}
      cardKeys={keys}
      tapGuard={tapGuard}
      onDismiss={ix.no_hand_back ? undefined : () => go({ dismiss: true })}
      busy={busy || replyBusy}
      footer={
        <>
          <span className="flex min-w-0 gap-1.5">
            {!confirming && ix.can_clarify && (
              <Button tone="quiet" disabled={busy || replyBusy} aria-expanded={questionOpen} onClick={() => setQuestionOpen((v) => !v)}>
                {t("chat-interactions.have_question")}
              </Button>
            )}
            <Button tone="quiet" disabled={busy || replyBusy} onClick={() => go(editable.deny())}>
              {t("chat-interactions.deny")}
            </Button>
          </span>
          <RowConfirm open={confirming} onCancel={review.cancel} onConfirm={confirm}
            confirm={t("approval-impact.confirm")} busy={busy || replyBusy} trigger={<span className="flex flex-wrap gap-1.5">
            {/* Only where an Always is kept (offersAlways): never on the manager's own cards. */}
            {!review.impact && offersAlways(ix) && !editable.changed && (
              <Button tone="secondary" disabled={busy || replyBusy} onClick={() => { if (!review.request("allow")) void go({ allow: true, always: true }); }}>
                {t("chat-interactions.always_allow")}
              </Button>
            )}
            {replyPhrase && (
              <Button tone="secondary" busy={replyBusy} disabled={busy || replyBusy} onClick={() => approve(true)}>
                {t("chat-interactions.owner_reply", { phrase: replyPhrase })}
              </Button>
            )}
            <Button tone="primary" busy={busy} disabled={replyBusy || !editable.canAllow} onClick={() => approve()}>
              {t("chat-interactions.allow")}
              {keys.focused && !questionOpen && <Kbd>Enter</Kbd>}
            </Button>
          </span>} />
        </>
      }
    >
      <PermissionBody ix={ix} />
      {editable.editor}
      {questionOpen && <Suspense fallback={null}><ApprovalQuestion ix={ix} busy={busy} onClose={() => setQuestionOpen(false)} /></Suspense>}
      <ApprovalImpactLine impact={review.impact} />
      {!review.impact && alwaysScope(ix) && <p className="mt-2 text-label leading-relaxed text-ink-3">{alwaysScope(ix)}</p>}
    </Card>
  );
}

/** What the tool will do, field by field (Android PermissionRequestSummary): the agent's sentence, then the command,
 *  file or address under a small label, an edit's changes; a tool it does not know shows the hub's summary. The raw
 *  input stays one click away, open from the start only when nothing else says what the tool does. */
export function PermissionBody({ ix }: { ix: Interaction }) {
  const input = ix.tool_input ?? {};
  const hasInput = Object.keys(input).length > 0;
  const detail = permissionDetail(ix.tool_name, input);
  const diffs = detail && DIFF_TOOLS.has(ix.tool_name ?? "") ? editDiffs({ id: ix.id, seq: 0, role: "assistant", kind: "tool_use", tool_name: ix.tool_name, tool_input: input }) : null;
  const reason = permissionReason(ix.agent, ix.reason);
  const extra = detail ? summaryExtra(ix.summary, detail.fields) : "";
  const extraShown = extra && extra !== detail?.note && extra !== reason && !detail?.fields.some((f) => f.value === extra);
  const lead = detail ? undefined : ix.summary?.trim();
  const [showInput, setShowInput] = useState(!detail && !lead);
  // a subagent's call (Claude Code sends its parent's session with agent_id and agent_type): say which one asks
  // (#4376); the main session's own cards (agent_type alone, as `claude --agent x` sets it, or "main") say nothing
  const from = permissionFrom(ix);
  return (
    <div className="flex flex-col gap-2">
      {from && <p data-permission-from title={from} className="truncate text-[12px] leading-snug text-ink-3">{from}</p>}
      {reason && <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-ink-2">{reason}</p>}
      {detail?.note && <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-ink">{detail.note}</p>}
      {detail?.fields.map((f) => (
        <div key={f.label}>
          <div className="mb-1 text-[11px] font-medium text-ink-3">{t(`chat-interactions.field_${f.label}`)}</div>
          <pre className={CODE_BOX}>{f.value}</pre>
        </div>
      ))}
      {diffs && diffs.length > 0 && (
        <div>
          <div className="mb-1 text-[11px] font-medium text-ink-3">{t(ix.tool_name === "Write" ? "chat-interactions.field_content" : "chat-interactions.field_changes")}</div>
          <div className="flex max-h-48 flex-col gap-2 overflow-auto">
            <DiffBlocks diffs={diffs} />
          </div>
        </div>
      )}
      {extraShown && <p className="text-[12.5px] leading-relaxed whitespace-pre-wrap text-ink-2">{extra}</p>}
      {lead && (ix.format === "markdown" ? <Markdown text={lead} small compact /> : <pre className={CODE_BOX}>{lead}</pre>)}
      {hasInput && (detail || lead) && (
        <button type="button" aria-expanded={showInput} onClick={() => setShowInput((v) => !v)} className="touch-hit flex items-center gap-1 self-start text-[11.5px] whitespace-nowrap text-ink-3 transition-colors hover:text-ink">
          <Icon name="chevron" size={11} stroke={2.4} style={{ transform: showInput ? "none" : "rotate(-90deg)", transition: "transform 150ms" }} />
          {t(showInput ? "chat-interactions.hide_input" : "chat-interactions.show_input")}
        </button>
      )}
      {showInput && <pre className={CODE_BOX}>{JSON.stringify(input, null, 2)}</pre>}
    </div>
  );
}
