import { useState } from "react";
import { t } from "../../i18n";
import { confirmationEnabled, useApprovalConfirmation } from "../../lib/approval-confirmation";
import { approvalImpact, type ApprovalImpact } from "../../lib/approval-impact";
import { cardTapKey } from "../../lib/card-tap-key";
import type { Interaction } from "../../lib/types";
import { Icon } from "../../ui/icons";

// Keep the selected answer local until confirmed. A refreshed card may reuse the same ID with new input or choices.
export function useApprovalReview(ix: Interaction) {
  const enabled = useApprovalConfirmation();
  const impact = enabled ? approvalImpact(ix) : null;
  const [pending, setPending] = useState<{ key: string; choice: string } | null>(null);
  const identity = () => JSON.stringify([ix.id, ix.created_at, cardTapKey(ix), ix.cwd, ix.always_keys, ix.summary, ix.editable_text, ix.outward_write,
    ix.approval_batch_id, ix.owner_approval_phrases, ix.dialog?.rev, ix.dialog?.detail, ix.dialog?.command, ix.dialog?.options]);
  const choice = impact && pending && pending.key === identity() ? pending.choice : null;
  const request = (answer: string) => {
    if (!confirmationEnabled() || !approvalImpact(ix)) return false;
    setPending({ key: identity(), choice: answer });
    return true;
  };
  return { impact, choice, request, cancel: () => setPending(null) };
}

export function ApprovalImpactLine({ impact }: { impact: ApprovalImpact | null }) {
  return impact && <p role="status" className="mt-2 flex items-start gap-1.5 text-label font-medium text-attention">
    <Icon name="warning" size={14} className="shrink-0" />{t(`approval-impact.${impact}`)}
  </p>;
}
