// The model panel's columns on a wide screen (#12315, after T3 Code's composer picker): the launchers on the left, the
// chosen launcher's models in the middle (ModelPanel draws them with Row), the thinking levels of the model under the
// pointer (else the one that runs) on the right, and a footer with what that model adds: its speed, Claude Code's
// ultracode. A launcher that can't take a pick here says why in its tooltip and once above its models, not as a line
// under every such launcher. A narrow screen keeps the single launcher-first column (LauncherRow).
import { t } from "../../i18n";
import { shownEffort, speedLabel, type PanelProvider, type PanelRow, type PanelState } from "../../lib/model-panel";
import { weeklyQuota, type LauncherQuotaAnswer } from "../../lib/quota";
import type { SpeedOption } from "../../lib/session-speed";
import { effortTitle } from "../../lib/session-actions";
import { cx } from "../atoms";
import { Icon } from "../icons";
import { ProviderMark } from "./ProviderMark";
import { SpeedMark } from "./SpeedMark";

/** The left column's entry for the saved setups (favorites, then recent) and the presets. */
export const SAVED_SIDE = "\nsaved";

const ENTRY =
  "flex min-h-8 w-full min-w-0 items-center gap-2 rounded-control px-2.5 py-1 text-left text-meta outline-none transition-colors duration-150 enabled:hover:bg-hover-2 focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent disabled:cursor-not-allowed";

export function LauncherColumn({ providers, state, quotas, side, saved, frozen, onSide }: {
  providers: PanelProvider[];
  state: PanelState;
  quotas?: Readonly<Record<string, LauncherQuotaAnswer>>;
  /** The entry chosen: a provider's id or SAVED_SIDE. */
  side: string;
  /** There are saved setups or presets to list. */
  saved: boolean;
  frozen: boolean;
  onSide: (side: string) => void;
}) {
  return (
    <div role="group" aria-label={t("ui-modelpanel.launchers")} data-panel-launchers className="flex w-[160px] shrink-0 flex-col gap-px overflow-y-auto overscroll-contain border-r border-line p-1.5">
      {saved && (
        <button type="button" data-panel-side={SAVED_SIDE} aria-pressed={side === SAVED_SIDE} disabled={frozen} onClick={() => onSide(SAVED_SIDE)} className={cx(ENTRY, side === SAVED_SIDE && "bg-hover-2 font-medium")}>
          <span className="flex size-4 shrink-0 items-center justify-center text-favorite-star">
            <Icon name="star" size={14} className="fill-current" />
          </span>
          <span className="min-w-0 flex-1 truncate">{t("ui-modelpanel.quick_picks")}</span>
        </button>
      )}
      {providers.map((p) => {
        const label = p.name;
        const named = p.idNote ? `${label} (${p.idNote})` : label;
        const why = p.unavailable || p.locked;
        const current = state.current?.provider === p.id;
        const quota = weeklyQuota(quotas?.[p.id]);
        const chosen = side === p.id;
        return (
          <button
            key={p.id}
            type="button"
            data-panel-side={p.id}
            aria-pressed={chosen}
            aria-description={why || undefined}
            title={why ? `${named}: ${why}` : p.idNote ? named : undefined}
            disabled={frozen}
            onClick={() => onSide(p.id)}
            className={cx(ENTRY, chosen && "bg-hover-2 font-medium", why && !chosen && "text-ink-3")}
          >
            <ProviderMark engine={p.engine} label={p.engineLabel} size={16} />
            <span className="min-w-0 flex-1 truncate">{label}</span>
            {current && <span aria-label={t("ui-modelpanel.current_launcher")} title={t("ui-modelpanel.current_launcher")} className="size-1.5 shrink-0 rounded-full bg-accent" />}
            {quota && <span className={cx("shrink-0 text-submeta tabular-nums", quota.tone === "red" ? "text-red" : quota.tone === "orange" ? "text-orange" : "text-ink-3")}>{quota.remaining}%</span>}
            {why && <Icon name="info" size={12} className="shrink-0 text-ink-3" />}
          </button>
        );
      })}
    </div>
  );
}

export function LevelColumn({ row, disabled, onLevel }: {
  /** The model under the pointer, else the one that runs, else the list's first; none: nothing to show. */
  row?: PanelRow;
  disabled: boolean;
  onLevel: (effort: string) => void;
}) {
  const model = row?.model;
  const levels = model?.efforts ?? [];
  const now = row && model ? shownEffort(row.setup, model) : "";
  const note = model?.effortUnavailable || model?.effortNote;
  return (
    <div role="group" aria-label={t("ui-modelpanel.effort")} data-panel-levels className="flex w-[156px] shrink-0 flex-col gap-px overflow-y-auto overscroll-contain border-l border-line p-1.5">
      <div className="flex min-w-0 items-baseline gap-2 px-2.5 pt-1 pb-1.5 text-submeta text-ink-3">
        <span className="shrink-0">{t("ui-modelpanel.effort")}</span>
        {model && <span className="ml-auto min-w-0 truncate">{model.name}</span>}
      </div>
      {model && levels.length > 0 &&
        levels.map((e) => (
          <button
            key={e}
            type="button"
            data-panel-level={e}
            aria-pressed={e === now}
            disabled={disabled || Boolean(model.effortUnavailable)}
            onClick={() => onLevel(e)}
            className={cx(ENTRY, "disabled:opacity-50")}
          >
            <span className="shrink-0">{effortTitle(e)}</span>
            {e === model.defaultEffort && !model.variants && <span title={t("ui-modelpanel.recommended")} className="ml-auto truncate text-submeta text-ink-3">{t("ui-modelpanel.recommended")}</span>}
            {e === now && <Icon name="check" size={13} stroke={2.5} className="ml-auto shrink-0 text-ink" />}
          </button>
        ))}
      {model && !levels.length && !note && <p className="px-2.5 text-label leading-[1.4] text-ink-3">{t("ui-modelpanel.no_levels")}</p>}
      {note && <p className="px-2.5 pt-2 text-label leading-[1.4] text-ink-3">{note}</p>}
    </div>
  );
}

/** The same compact control in the columns and the narrow flyout. */
export function SpeedSwitch({ options, speed, disabled, onSpeed }: {
  options: readonly SpeedOption[];
  speed?: string;
  disabled?: boolean;
  onSpeed: (speed: string) => void;
}) {
  return (
    <div role="group" aria-label={t("ui-modelpanel.speed")} className="inline-flex w-fit max-w-full shrink-0 flex-wrap items-center rounded-control bg-hover-2 p-0.5">
      {options.map((option) => {
        const { id } = option;
        const label = speedLabel(option);
        const selected = speed === id;
        const detail = option.description || (id === "fast" ? t("ui-modelpanel.fast") : id === "ultrafast" ? t("ui-modelpanel.ultrafast") : label);
        return (
          <button
            key={id}
            type="button"
            data-panel-speed={id}
            aria-pressed={selected}
            aria-label={label}
            title={detail}
            aria-disabled={disabled || undefined}
            onClick={() => !disabled && !selected && onSpeed(id)}
            className={cx(
              "flex min-h-7 max-w-full shrink-0 items-center gap-1 rounded-[7px] px-2.5 text-label whitespace-nowrap transition-colors duration-150 focus-visible:-outline-offset-2! aria-disabled:cursor-not-allowed aria-disabled:opacity-50 [@media(pointer:coarse)]:min-h-11",
              selected ? "bg-surface text-ink" : "text-ink-2 hover:text-ink",
            )}
          >
            <SpeedMark speed={id} size={12} />
            <span className="truncate">{label}</span>
          </button>
        );
      })}
    </div>
  );
}
