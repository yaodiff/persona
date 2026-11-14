// Settings › General › Appearance › Language (docs/protocol/i18n.md): Match system, or one of the three languages, each by its own name.
import { useState } from "react";
import { LANG_NAMES, LANGS, languagePreference, setLanguagePreference, systemLanguage, t, type LangPreference } from "../../i18n";
import { cx } from "../../ui/atoms";
import { Row } from "./SettingsParts";

const controlClass = "h-8 rounded-control bg-field px-2.5 text-[12.5px] text-ink shadow-inset-field outline-none focus:shadow-[inset_0_0_0_1px_var(--accent)]";

export function LanguageRow() {
  const [pref, setPref] = useState<LangPreference>(languagePreference);
  const choose = (p: LangPreference) => {
    setPref(p);
    setLanguagePreference(p);
  };
  return (
    <Row label={t("language.label")} hint={t("language.hint")} wrapHint stackOnNarrow anchor="language">
      {/* on a phone under its sentence, the width of the row */}
      <select value={pref} onChange={(e) => choose(e.target.value as LangPreference)} className={cx(controlClass, "min-w-[140px] max-sm:w-full")} aria-label={t("language.label")}>
        <option value="system">{t("language.system_detail", { language: LANG_NAMES[systemLanguage()] })}</option>
        {LANGS.map((l) => (
          <option key={l} value={l} lang={l}>
            {LANG_NAMES[l]}
          </option>
        ))}
      </select>
    </Row>
  );
}
