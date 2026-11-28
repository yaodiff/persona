// Settings › Agents › "BotMux Owner Union ID", under "Show botmux sessions" (lazy: only while that switch is on). Who
// the computers' botmux sends speak for when botmux takes actor "owner"; empty sends the usual way. A union_id found in
// the computers' bots.json is offered as a suggestion that only fills the field: nothing is saved until Save.
import { useEffect, useState } from "react";
import { t } from "../../i18n";
import { api } from "../../lib/api";
import { saveAgentsDisplay } from "../../lib/agents-display";
import { Button } from "../../ui/atoms";
import { Row, SettingsHint, settingsInputClass } from "./SettingsParts";

/** A Feishu / Lark union_id, as the hub checks it (proto.ParseBotmuxOwnerUnionID). */
const UNION_ID = /^on_[A-Za-z0-9_-]{1,125}$/;

interface Candidate { union_id: string; bots: number; sole: number; machines: number }

export function validBotmuxOwnerUnionID(value: string): boolean {
  const id = value.trim();
  return id === "" || UNION_ID.test(id);
}

export function BotmuxOwnerField({ saved, disabled }: { saved: string; disabled: boolean }) {
  const [draft, setDraft] = useState(saved);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  useEffect(() => { setDraft(saved); }, [saved]);
  useEffect(() => {
    let live = true;
    api<{ candidates?: Candidate[] }>("/agents/botmux-owner-candidates").then((res) => {
      if (live && Array.isArray(res?.candidates)) setCandidates(res.candidates.filter((c) => typeof c?.union_id === "string" && UNION_ID.test(c.union_id)));
    }, () => { /* an older hub or no computer online: no suggestion */ });
    return () => { live = false; };
  }, []);
  const id = draft.trim();
  const valid = validBotmuxOwnerUnionID(draft);
  const suggestion = candidates[0]?.union_id !== id ? candidates[0] : undefined;
  const save = (value: string) => { if (!disabled && validBotmuxOwnerUnionID(value)) void saveAgentsDisplay({ botmux_owner_union_id: value.trim() }); };
  const label = t("settings.botmux_owner");
  return <>
    <Row label={label} hint={t("settings.botmux_owner_hint")} wrapHint stackOnNarrow anchor="botmux-owner-union-id">
      <input value={draft} placeholder="on_…" aria-label={label} aria-invalid={!valid} spellCheck={false} autoComplete="off" disabled={disabled}
        data-testid="botmux-owner-input" className={`${settingsInputClass} font-mono sm:w-72`}
        onChange={(e) => setDraft(e.target.value)} onKeyDown={(e) => { if (e.key === "Enter" && valid && id !== saved) save(draft); }} />
      {suggestion && <Button disabled={disabled} title={suggestion.union_id} testId="botmux-owner-fill" onClick={() => setDraft(suggestion.union_id)}>
        {t("settings.botmux_owner_fill")}
      </Button>}
      <Button tone="primary" disabled={disabled || !valid || id === saved} testId="botmux-owner-save" onClick={() => save(draft)}>{t("settings.botmux_owner_save")}</Button>
      {saved !== "" && <Button disabled={disabled} testId="botmux-owner-clear" onClick={() => save("")}>{t("settings.botmux_owner_clear")}</Button>}
    </Row>
    {!valid && <SettingsHint alert>{t("settings.botmux_owner_invalid")}</SettingsHint>}
    {suggestion && <SettingsHint><span data-testid="botmux-owner-suggestion">{t("settings.botmux_owner_found", { id: suggestion.union_id })}</span></SettingsHint>}
    <SettingsHint>{t(saved === "" ? "settings.botmux_owner_unset" : "settings.botmux_owner_set")} {t("settings.botmux_owner_source")}</SettingsHint>
  </>;
}
