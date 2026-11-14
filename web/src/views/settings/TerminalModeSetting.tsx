// Settings › Terminal view (docs/integrations/terminal.md "Full herdr UI"): which surface a terminal opens in. Shown only where the hub can run
// herdr's own client; "Full view" is the default, and nothing is written until another is chosen.
import { useState } from "react";
import { t } from "../../i18n";
import { useStore } from "../../lib/store";
import { MODE_LABEL, PREFERENCE_KEY, parsePreference, type ModePreference } from "../../lib/terminal-mode";
import { Segmented } from "../assistant/parts";
import { Row } from "./SettingsParts";

const OPTIONS: { v: ModePreference; label: string }[] = [
  { v: "full", label: MODE_LABEL.full },
  { v: "pane", label: MODE_LABEL.pane },
  { v: "last", label: t("terminal-full.pref_last") },
];

export function TerminalModeRow() {
  const supported = useStore((s) => s.features.includes("herdr_client"));
  const [pref, setPref] = useState(() => parsePreference(localStorage.getItem(PREFERENCE_KEY)));
  if (!supported) return null;
  const choose = (next: ModePreference) => {
    setPref(next);
    try {
      if (next === "full") localStorage.removeItem(PREFERENCE_KEY);
      else localStorage.setItem(PREFERENCE_KEY, next);
    } catch {
      // not kept: the next page starts with "Single pane"
    }
  };
  return (
    <Row label={t("terminal-full.mode_group")} hint={t("terminal-full.pref_hint")} wrapHint>
      <Segmented<ModePreference> label={t("terminal-full.mode_group")} value={pref} options={OPTIONS} onChange={choose} />
    </Row>
  );
}
