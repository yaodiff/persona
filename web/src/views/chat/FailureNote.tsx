// Above the composer while the host stopped the session's turn on an error it will not retry by itself (a botmux
// notice, iOS FailureNote): the reason, "Retry" (sends "Continue" alone, not what is in the box) and "Open in Lark". The
// same error notice as the model error banner (#5313), so one card shape says what stopped a turn.
import { externalLink, failureReason } from "../../lib/format";
import type { Session } from "../../lib/types";
import { Button } from "../../ui/atoms";
import { ErrorNotice } from "../../ui/ErrorNotice";
import { t } from "../../i18n";

export { showsFailureNote } from "./failure-note-state";

export interface FailureNoteProps {
  session?: Session;
  onRetry: () => void;
  /** Explicit failure text skips reading `session.failure_reason` and `stateOf(session)`. Pass `null` or `""` to hide. */
  reason?: string | null;
  /** Explicit external URL (https only) overrides `session.external_url`. Pass `null` or `""` to hide the external action. */
  externalUrl?: string | null;
  /** Handles opening the external link instead of a direct new-tab anchor. */
  onOpenExternal?: (url: string) => void;
}

export function FailureNote({ session, onRetry, reason: controlledReason, externalUrl, onOpenExternal }: FailureNoteProps) {
  const reason = controlledReason === undefined ? failureReason(session) : controlledReason?.trim() ? controlledReason : undefined;
  if (!reason) return null;
  const link = externalLink(externalUrl === undefined ? session : { external_url: externalUrl ?? undefined });
  return (
    <ErrorNotice
      role="status"
      data-testid="chat-failure"
      message={reason}
      actions={
        <>
          <Button icon="retry" size="sm" className="touch-hit" testId="chat-failure-retry" title={t("chat-lines.retry_title")} onClick={onRetry}>
            {t("chat-chatview.retry")}
          </Button>
          {link && (
            <Button
              icon="external"
              size="sm"
              className="touch-hit"
              testId="chat-failure-lark"
              href={onOpenExternal ? undefined : link}
              onClick={onOpenExternal ? () => onOpenExternal(link) : undefined}
            >
              {t("chat-chatview.open_in_lark")}
            </Button>
          )}
        </>
      }
    />
  );
}
