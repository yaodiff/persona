import { useState } from "react";
import { t } from "../i18n";
import { appArchiveDeps, restoreAgent } from "../lib/agent-archives";
import type { Session } from "../lib/types";
import { Button } from "./atoms";
import { Icon } from "./icons";

/** An archived agent's chat (lib/agent-archives.ts): it is out of the lists until restored or used again. */
export default function ArchivedNotice({ session }: { session: Session }) {
  const [busy, setBusy] = useState(false);
  return <div role="status" data-archived-notice className="flex shrink-0 items-center gap-2 border-b border-line bg-inset px-3 py-2 text-xs text-ink-2">
    <Icon name="archive" size={13} className="shrink-0 text-ink-3" />
    <span className="min-w-0 flex-1 break-words">{session.read_only_reason || t("chat-closed.archived")}</span>
    <Button tone="quiet" icon="undo" busy={busy} onClick={() => { setBusy(true); void restoreAgent(session, appArchiveDeps).finally(() => setBusy(false)); }}>{t("agents-archives.restore")}</Button>
  </div>;
}
