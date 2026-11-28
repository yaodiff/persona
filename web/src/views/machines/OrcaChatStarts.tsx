// "Start new Agents as Orca chats" under a computer's Engine (#17650, docs/integrations/engines.md "Orca chats as New
// Agent's default"): off by default; the hub sends the switch on the machine (`orca_chat_starts`) only while Orca is in its
// engine set, so it shows exactly there. It flips at once and settles on the hub's answer (PUT …/orca-chat-starts {"on"});
// a failed save goes back and says why. Where a start goes is the hub's to decide; New Agent says it (lib/orca-chat.ts).
import { useId, useState } from "react";
import { api, enc, errorText } from "../../lib/api";
import { orcaChatSwitch } from "../../lib/orca-chat";
import { applyMachine } from "../../lib/store";
import type { Machine } from "../../lib/types";
import { EASE, Spinner, Switch, toast } from "../../ui/atoms";
import { t } from "../../i18n";

export function OrcaChatStarts({ machine }: { machine: Machine }) {
  const saved = orcaChatSwitch(machine);
  const [on, setOn] = useState(saved ?? false);
  const [seen, setSeen] = useState(saved);
  const [saving, setSaving] = useState(false);
  const hintID = useId();
  // A push of the machine (another device, this save settling) moves the switch while nothing is being saved here.
  if (!saving && seen !== saved) {
    setSeen(saved);
    setOn(saved ?? false);
  }
  if (saved === null) return null;

  const change = async (next: boolean) => {
    if (saving) return;
    setSaving(true);
    setOn(next);
    try {
      const r = await api<{ on: boolean }>(`/machines/${enc(machine.id)}/orca-chat-starts`, { method: "PUT", json: { on: next } });
      setOn(r.on); // until the machine's push says so too
      // only this field: the rest of the machine stays as the latest push has it
      applyMachine({ id: machine.id, orca_chat_starts: r.on ? "on" : "off" } as Machine);
    } catch (e) {
      setOn(!next);
      toast(t("machines-orcachatstarts.save_failed", { error: errorText(e) }), "error");
    } finally {
      setSaving(false);
    }
  };

  return (
    <div data-testid="orca-chat-starts" className="mt-3 overflow-hidden rounded-card bg-surface shadow-card" style={{ animation: `fade-up 420ms ${EASE} both` }}>
      <div className="flex items-center gap-3 px-4 py-3">
        <div className="min-w-0 flex-1">
          <div className="text-meta text-ink">{t("machines-orcachatstarts.title")}</div>
          <p id={hintID} className="mt-0.5 text-submeta leading-relaxed text-ink-3">{t("machines-orcachatstarts.detail")}</p>
        </div>
        {saving && <Spinner />}
        <Switch on={on} onChange={(v) => void change(v)} disabled={saving} label={t("machines-orcachatstarts.title")} describedBy={hintID} />
      </div>
    </div>
  );
}
