// Settings › Advanced › Appearance's "Theme color", "Font" and "Sidebar icons" (#13398), and General's "Text size"
// (ui/look.ts; the app's ThemeColorPicker and StyleSettingsPage's "Text size"), plus General's "Recent chats in the
// sidebar" (lib/sidebar-recent.ts). Moving the rows changed no component and no browser storage key.
import { useState, useSyncExternalStore } from "react";
import { t } from "../../i18n";
import { Button, cx } from "../../ui/atoms";
import { pageToken } from "../../ui/extension-theme";
import { useActiveExtensionTheme } from "../../ui/ExtensionThemePicker";
import { Icon } from "../../ui/icons";
import {
  ACCENT_PRESETS, accentTokens, PRESET_COLORS, setAccentChoice, setGlassTranslucency, setSurfaceStyle, setTextSize,
  SURFACE_STYLES, TEXT_SIZES, useAccentChoice, useGlassTranslucency, usePersonaAccent, useSurfaceStyle, useTextSize,
  type AccentChoice, type SurfaceStyle, type TextSize,
} from "../../ui/look";
import { RECENT_COUNTS, setRecentCount, useRecentCount } from "../../lib/sidebar-recent";
import { setSidebarUsageShown, useSidebarUsageShown } from "../../lib/sidebar-usage";
import { setSidebarIconMode, SIDEBAR_ICON_MODES, useSidebarIconMode, type SidebarIconMode } from "../../lib/sidebar-icons";
import { PHONE_NAV_MODES, setPhoneNavMode, usePhoneNavMode, type PhoneNavMode } from "../../lib/phone-nav";
import { usePersona } from "../assistant/Persona";
import { Segmented } from "../assistant/parts";
import { Slider } from "../../ui/Slider";
import { Row, Switch } from "./SettingsParts";
import { FONT_FALLBACKS, FONT_PRESETS, FONT_SERIF, fontFamily, quoteFont, setBrowserFonts, useFontSettings, validFontFamily, type FontChoice, type FontKind, type FontPreference } from "../../ui/font-preference";
import { useFontAvailability } from "../../ui/font-availability";
import { refreshSharedFonts, saveFontChoice, useFontSync } from "../../ui/font-sync";

function subscribeScheme(l: () => void) {
  const o = new MutationObserver(l);
  o.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });
  return () => o.disconnect();
}
const darkNow = () => document.documentElement.classList.contains("dark");

/**
 * "Theme color": "Default" (the page's own blue), "Match avatar" when the persona brings colors (or it is the choice already, so it stays in
 * sight), then the five fixed colors, each a disc of its accent with where it comes from under its name.
 */
export function ThemeColorPicker() {
  const choice = useAccentChoice();
  const personaAccent = usePersonaAccent();
  const persona = usePersona();
  const extension = useActiveExtensionTheme();
  const scheme = useSyncExternalStore(subscribeScheme, darkNow) ? "dark" : "light";
  const options: AccentChoice[] = ["default", ...(personaAccent || choice === "persona" ? (["persona"] as const) : []), ...ACCENT_PRESETS];
  // The disc is the color the page gets: the same contrast floor as accentTokens; "Default"'s is styles.css's own.
  const disc = (c: AccentChoice): string => {
    if (c === "default") return pageToken("accent", scheme);
    const pair = c === "persona" ? personaAccent : PRESET_COLORS[c].accent;
    return pair ? accentTokens(pair)[scheme]["--accent"] : pageToken("accent", scheme);
  };
  const sub = (c: AccentChoice) => (c === "persona" ? (personaAccent ? persona.name : t("ui-look.persona_none")) : t(`ui-look.${c}_sub`));
  return (
    <div className="px-4 py-3" data-settings-row="theme-color">
      <div className="text-[13px] text-ink">{t("ui-look.accent")}</div>
      <div className="mt-0.5 text-[11.5px] text-ink-3">{t("ui-look.accent_hint")}</div>
      <div role="radiogroup" aria-label={t("ui-look.accent")} className="mt-3 grid grid-cols-4 gap-x-1 gap-y-3 sm:grid-cols-7">
        {options.map((c) => {
          const selected = c === choice;
          const color = disc(c);
          return (
            <button
              key={c}
              type="button"
              role="radio"
              aria-checked={selected}
              aria-label={t("ui-look.swatch_label", { name: t(`ui-look.${c}`), origin: sub(c) })}
              onClick={() => !selected && setAccentChoice(c)}
              className="group flex min-w-0 flex-col items-center gap-1.5 rounded-control px-0.5 py-1 transition-colors hover:bg-hover"
            >
              <span
                className="flex size-10 items-center justify-center rounded-full p-[3px] transition-shadow duration-150"
                style={selected ? { boxShadow: `inset 0 0 0 2px ${color}` } : undefined}
              >
                {/* the disc is data (a color to choose), like the persona picker's swatches */}
                <span className="flex size-full items-center justify-center rounded-full text-white shadow-hairline" style={{ background: color }}>
                  {selected && <Icon name="check" size={14} stroke={2.6} />}
                </span>
              </span>
              {/* a color's name wraps in a phone's four columns rather than being cut ("システムブルー") */}
              <span className={cx("max-w-full text-center text-[12px] break-words", selected ? "font-medium text-ink" : "text-ink-2")}>{t(`ui-look.${c}`)}</span>
              <span className="-mt-1 max-w-full truncate text-[11px] whitespace-nowrap text-ink-3">{sub(c)}</span>
            </button>
          );
        })}
      </div>
      {extension && <p className="mt-2.5 text-[11.5px] leading-relaxed text-ink-3">{t("ui-look.extension_over", { name: extension.name })}</p>}
    </div>
  );
}

/** "Text size": "Small" / "Standard" / "Large", the whole page's text (src/lib/text-scale.ts). */
export function TextSizeRow() {
  const size = useTextSize();
  return (
    <Row label={t("ui-look.text_size")} hint={t("ui-look.text_size_hint")} wrapHint anchor="text-size">
      <Segmented<TextSize> label={t("ui-look.text_size")} value={size} options={TEXT_SIZES.map((v) => ({ v, label: t(`ui-look.${v}`) }))} onChange={setTextSize} />
    </Row>
  );
}

/** "Recent chats in the sidebar": how many Persona chats › Recent lists before Show more (lib/sidebar-recent.ts, #5146).
 *  On a phone its four choices go under the sentence, so the name is not cut beside them (audit item 17). */
export function RecentChatsRow() {
  const count = useRecentCount();
  return (
    <Row label={t("ui-look.recent_chats")} hint={t("ui-look.recent_chats_hint")} wrapHint stackOnNarrow anchor="recent-chats">
      <Segmented label={t("ui-look.recent_chats")} value={String(count)} options={RECENT_COUNTS.map((n) => ({ v: String(n), label: String(n) }))} onChange={(v) => setRecentCount(Number(v))} />
    </Row>
  );
}

const fontKinds = ["sans", "mono"] as const;
const fontField = "h-11 min-w-0 max-w-full rounded-control bg-field px-2.5 text-label text-ink shadow-inset-field";
type LocalFontWindow = Window & { queryLocalFonts?: () => Promise<{ family: string }[]> };

export function FontRows() {
  const { fonts, browser } = useFontSettings();
  const sync = useFontSync();
  const [drafts, setDrafts] = useState<Partial<FontPreference>>({});
  const [failed, setFailed] = useState<{ kind: FontKind; choice: FontChoice }>();
  const [localError, setLocalError] = useState(false);
  const draft = (kind: FontKind, choice?: FontChoice) => setDrafts((current) => ({ ...current, [kind]: choice }));
  const commit = async (kind: FontKind, choice: FontChoice) => {
    draft(kind, choice);
    setFailed(undefined);
    setLocalError(false);
    if (await saveFontChoice(kind, choice)) draft(kind);
    else setFailed({ kind, choice });
  };
  const preview = { ...fonts, ...Object.fromEntries(fontKinds.flatMap((kind) => {
    const choice = drafts[kind];
    return choice && (choice.preset !== "custom" || validFontFamily(choice.family)) ? [[kind, choice]] : [];
  })) };
  return <div data-settings-row="font">
    <div className="px-4 pt-3 text-meta font-medium text-ink">{t("ui-look.font")}</div>
    {sync !== "local" && <Row label={t("ui-look.font_browser")} hint={t("ui-look.font_browser_hint")} wrapHint stackOnNarrow>
      <Switch label={t("ui-look.font_browser")} on={browser} disabled={sync === "saving"} onChange={(on) => {
        if (setBrowserFonts(on)) {
          setDrafts({});
          setFailed(undefined);
          setLocalError(false);
          if (!on) void refreshSharedFonts();
        } else setLocalError(true);
      }} />
    </Row>}
    {fontKinds.map((kind) => <FontRow key={kind} kind={kind} choice={drafts[kind] ?? fonts[kind]} saved={fonts[kind]}
      disabled={sync === "saving"} onDraft={(choice) => { if (failed?.kind === kind) setFailed(undefined); draft(kind, choice); }} onSave={(choice) => void commit(kind, choice)} />)}
    {(failed || localError || sync === "saving") && <div className="flex flex-wrap items-center gap-2 px-4 pb-2">
      <p className="text-submeta text-ink-2" role="status">
        {t(sync === "saving" ? "ui-look.font_saving" : "ui-look.font_save_failed")}
      </p>
      {failed && <Button className="touch-hit whitespace-nowrap" onClick={() => void commit(failed.kind, failed.choice)}>{t("settings.retry")}</Button>}
    </div>}
    <FontPreview fonts={preview} />
  </div>;
}

function FontRow({ kind, choice, saved, disabled, onDraft, onSave }: {
  kind: FontKind;
  choice: FontChoice;
  saved: FontChoice;
  disabled: boolean;
  onDraft: (choice: FontChoice) => void;
  onSave: (choice: FontChoice) => void;
}) {
  const label = t(`ui-look.font_${kind}`);
  const family = choice.family ?? "";
  const valid = validFontFamily(family);
  return <div>
    <Row label={label} hint={kind === "mono" ? t("ui-look.font_mono_hint") : undefined} wrapHint stackOnNarrow>
      <select aria-label={label} className={fontField} value={choice.preset} disabled={disabled} onChange={(event) => {
        const preset = event.target.value;
        if (preset === "custom") onDraft({ preset, family: saved.family ?? "" });
        else onSave({ preset });
      }}>
        <option value="default">{t("ui-look.default")}</option>
        {Object.entries(FONT_PRESETS[kind]).map(([preset, name]) => <option key={preset} value={preset}>
          {preset === "system" ? t(`ui-look.font_system_${kind}`) : preset === "serif" ? t("ui-look.font_serif") : name}
        </option>)}
        <option value="custom">{t("ui-look.font_custom")}</option>
      </select>
    </Row>
    {choice.preset === "custom" && <div className="space-y-2 px-4 pb-3">
      <form className="flex flex-wrap items-end gap-2" onSubmit={(event) => { event.preventDefault(); if (valid && !disabled) onSave(choice); }}>
        <label className="min-w-0 flex-1 text-label text-ink-2">{t("ui-look.font_family")}
          <input aria-label={t("ui-look.font_custom_label", { kind: label })} aria-invalid={family.length > 0 && !valid}
            value={family} maxLength={100} spellCheck={false} autoComplete="off" disabled={disabled}
            className={cx("mt-1 w-full", fontField)} onChange={(event) => onDraft({ preset: "custom", family: event.target.value })} />
        </label>
        <Button type="submit" className="touch-hit whitespace-nowrap" disabled={disabled || !valid || family.trim() === saved.family}>{t("ui-look.font_apply")}</Button>
      </form>
      {family.length > 0 && !valid && <p className="text-submeta text-ink-3" role="alert">{t("ui-look.font_invalid")}</p>}
      {typeof (window as LocalFontWindow).queryLocalFonts === "function" &&
        <LocalFontPicker label={label} disabled={disabled} onSelect={(family) => onDraft({ preset: "custom", family })} />}
    </div>}
  </div>;
}

function LocalFontPicker({ label, disabled, onSelect }: { label: string; disabled: boolean; onSelect: (family: string) => void }) {
  const [families, setFamilies] = useState<string[]>();
  const [pending, setPending] = useState(false);
  const browse = async () => {
    setPending(true);
    try {
      const fonts = await (window as LocalFontWindow).queryLocalFonts!();
      setFamilies([...new Set(fonts.map((font) => font.family).filter(validFontFamily))].sort((a, b) => a.localeCompare(b)));
    } catch {
      setFamilies([]);
    } finally {
      setPending(false);
    }
  };
  return <div className="flex flex-wrap items-center gap-2">
    <Button className="touch-hit whitespace-nowrap" disabled={disabled || pending} onClick={() => void browse()}>{t("ui-look.font_browse")}</Button>
    {families?.length ? <select aria-label={t("ui-look.font_local_label", { kind: label })} value="" disabled={disabled} className={fontField}
      onChange={(event) => onSelect(event.target.value)}>
      <option value="" disabled>{t("ui-look.font_choose_local")}</option>
      {families.map((family) => <option key={family} value={family}>{family}</option>)}
    </select> : families && <p role="status" className="text-submeta text-ink-3">{t("ui-look.font_browse_unavailable")}</p>}
  </div>;
}

function FontPreview({ fonts }: { fonts: FontPreference }) {
  const sans = useFontAvailability("sans", fonts.sans);
  const mono = useFontAvailability("mono", fonts.mono);
  const stack = (kind: FontKind) => {
    const family = fontFamily(kind, fonts[kind]);
    const fallback = document.documentElement.style.getPropertyValue(`--font-${kind}`) || FONT_FALLBACKS[kind];
    if (!family) return fallback;
    return `${fonts[kind].preset === "system" ? family : quoteFont(family)}, ${fallback}`;
  };
  const codeStyle = { fontFamily: stack("mono") };
  return <div className="mx-4 mb-3 rounded-control bg-field px-3 py-2">
    <div className="text-label font-medium text-ink">{t("ui-look.font_preview")}</div>
    <div className="prose-j" style={{ fontFamily: fonts.sans.preset === "serif" ? FONT_SERIF : stack("sans") }}>
      <p>{t("ui-look.font_sample")} <code style={codeStyle}>hello()</code></p>
      <pre><code style={codeStyle}>const hello = () =&gt; "Hello!";</code></pre>
    </div>
    {(sans !== null || mono !== null) && <div className="flex flex-wrap gap-x-3 text-submeta text-ink-3" role="status">
      {([sans, mono] as const).map((available, i) => available === null ? null :
        <span key={fontKinds[i]}>{t(available ? "ui-look.font_available" : "ui-look.font_unavailable", { kind: t(`ui-look.font_${fontKinds[i]}`) })}</span>)}
    </div>}
  </div>;
}

/** "Sidebar icons": Monochrome (the default, the neutral ink) or Colored, a color for each page's icon (#10822).
 *  Stacked on a phone like Recent chats, so the name is not cut beside the choices. */
export function SidebarIconsRow() {
  const mode = useSidebarIconMode();
  return (
    <Row label={t("ui-look.sidebar_icons")} hint={t("ui-look.sidebar_icons_hint")} wrapHint stackOnNarrow anchor="sidebar-icons">
      <Segmented<SidebarIconMode> label={t("ui-look.sidebar_icons")} value={mode} options={SIDEBAR_ICON_MODES.map((v) => ({ v, label: t(`ui-look.sidebar_icons_${v}`) }))} onChange={setSidebarIconMode} />
    </Row>
  );
}

/** "Phone navigation" (lib/phone-nav.ts): the top bar's switch, the default, or a tab bar along the bottom. */
export function PhoneNavRow() {
  const mode = usePhoneNavMode();
  return (
    <Row label={t("ui-look.phone_nav")} hint={t("ui-look.phone_nav_hint")} wrapHint stackOnNarrow anchor="phone-nav">
      <Segmented<PhoneNavMode> label={t("ui-look.phone_nav")} value={mode} options={PHONE_NAV_MODES.map((v) => ({ v, label: t(`ui-look.phone_nav_${v}`) }))} onChange={setPhoneNavMode} />
    </Row>
  );
}

/** "Window style" (#16607): Default (opaque surfaces) or Liquid Glass (translucent frosted material + macOS vibrancy). */
export function SurfaceStyleRow() {
  const style = useSurfaceStyle();
  const translucency = useGlassTranslucency();
  const label = (v: SurfaceStyle) => (v === "liquid-glass" ? t("ui-look.surface_liquid_glass") : t("ui-look.surface_default"));
  return (
    <>
      <Row label={t("ui-look.surface_style")} hint={t("ui-look.surface_style_hint")} wrapHint stackOnNarrow anchor="surface-style">
        <Segmented<SurfaceStyle> label={t("ui-look.surface_style")} value={style} options={SURFACE_STYLES.map((v) => ({ v, label: label(v) }))} onChange={setSurfaceStyle} />
      </Row>
      {style === "liquid-glass" && (
        <Row label={t("ui-look.glass_translucency")} hint={t("ui-look.glass_translucency_hint")} wrapHint stackOnNarrow anchor="glass-translucency">
          <div className="flex items-center gap-3">
            <Slider
              value={translucency}
              min={0}
              max={100}
              step={5}
              label={t("ui-look.glass_translucency")}
              valueText={`${translucency}%`}
              onInput={setGlassTranslucency}
              onCommit={setGlassTranslucency}
              className="w-36 sm:w-44"
            />
            <span className="w-9 text-right text-label font-medium tabular-nums text-ink-2">{translucency}%</span>
          </div>
        </Row>
      )}
    </>
  );
}

/** "Launcher quota in the sidebar": the usage card under the sidebar, on by default. */
export function SidebarUsageRow() {
  const on = useSidebarUsageShown();
  return (
    <Row label={t("ui-look.sidebar_usage")} hint={t("ui-look.sidebar_usage_hint")} wrapHint anchor="sidebar-usage">
      <Switch on={on} onChange={setSidebarUsageShown} label={t("ui-look.sidebar_usage")} />
    </Row>
  );
}
