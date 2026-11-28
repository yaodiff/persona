// "Restore the last working setup" on a Persona chat's model error card (views/chat/ModelPill.tsx useAssistantTurnFix):
// POST …/model/restore puts back the four dimensions the chat last finished a turn on (docs/assistant/execution-spec.md
// "last good"). Loaded on the tap, not with the first screen.
import { t } from "../i18n";
import { toast } from "../ui/atoms";
import { errorText } from "./api";
import { restoreConversationLastGood } from "./execution";
import { getSession, upsertSession } from "./store";
import type { Session } from "./types";

/** Restores `s`'s last working setup; the chat's pick is drawn at once. Returns whether it was restored. */
export async function restoreLastGood(s: Session): Promise<boolean> {
  try {
    const r = await restoreConversationLastGood(s);
    if (!r) {
      toast(t("execution.restore_none"), "info");
      return false;
    }
    if (r.model_pick) upsertSession({ ...(getSession(s.machine_id, s.session_id) ?? s), model_pick: r.model_pick });
    toast(t("execution.restored"), "ok");
    return true;
  } catch (e) {
    toast(t("execution.restore_failed", { error: errorText(e) }), "error");
    return false;
  }
}
