// Opt-in toggle for deterministic PR/Issue Inbox auto-forwarding (#12492) surfaced on Settings › Inbox Sources.
import { useEffect, useState, type ReactNode } from "react";
import { t } from "../../../i18n";
import { errorText, isAbort } from "../../../lib/api";
import { currentAssistant, ownedAssistant, type ScopedAssistantClient } from "../../../lib/scoped-assistant-api";
import { toast } from "../../../ui/atoms";
import { useScopedAssistant } from "./useScopedAssistant";

type Settings = { inbox_todo_forward?: boolean };
export type RenderForwarding = (enabled: boolean, busy: boolean, toggle: (next: boolean) => void) => ReactNode;

export function InboxForwardController({ manager, render }: { manager?: string; render: RenderForwarding }) {
  const { client, routing } = useScopedAssistant(manager === undefined ? currentAssistant : ownedAssistant(manager), "assistant-settings", "inbox-forward");
  return <ForwardingState key={client.key} client={client} routing={routing} render={render} />;
}

function ForwardingState({ client, routing, render }: { client: ScopedAssistantClient; routing: string; render: RenderForwarding }) {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let active = true;
    const load = () => void client.request<Settings>("/assistant").then((r) => {
      if (active) setEnabled(typeof r.inbox_todo_forward === "boolean" ? r.inbox_todo_forward : null);
    }, (e: unknown) => {
      if (active && !isAbort(e)) setEnabled(null);
    });
    load();
    const stop = client.subscribe(load);
    return () => { active = false; stop(); };
  }, [client, routing]);

  if (enabled === null) return null;

  const toggle = async (next: boolean) => {
    const before = enabled;
    setEnabled(next);
    setBusy(true);
    try {
      const r = await client.request<Settings>("/assistant", { method: "PUT", json: { inbox_todo_forward: next } });
      setEnabled(typeof r.inbox_todo_forward === "boolean" ? r.inbox_todo_forward : next);
    } catch (e) {
      setEnabled(before);
      if (!isAbort(e)) toast(t("settings-assistantsettings.save_failed", { error: errorText(e) }), "error");
    } finally {
      setBusy(false);
    }
  };

  return render(enabled, busy, (on) => void toggle(on));
}
