// One row of the model panel's list (#5032, after Cindy's UnifiedModelRow), two lines. The first: the Agent's mark (#5497),
// the model's name, a star and Customize that show under the pointer (or on keyboard focus, or always on a touch screen),
// and on the right what a pick applies: the effort and speed. The second: where it comes from (All,
// Recent, Favorites) or what the model is (a provider's own view). The model that runs now has a whole-row fill and no
// check mark. Enter or Space picks it, ← or a right-click opens its setup flyout. A name cut short shows the model's whole
// ID as its second line (a touch screen has no tooltip), and a touch screen adds the full ID to the provider's line.
import { useLayoutEffect, useRef, useState, type ButtonHTMLAttributes, type KeyboardEvent, type MouseEvent } from "react";
import { t } from "../../i18n";
import { priceTier, shownEffort, speedLabel, type PanelRow } from "../../lib/model-panel";
import { effortTitle } from "../../lib/session-actions";
import { agentName } from "../../lib/agent-kind";
import { cx } from "../atoms";
import { Icon, type IconName } from "../icons";
import { ProviderMark } from "./ProviderMark";
import { SpeedMark } from "./SpeedMark";

// the row's small square buttons, shown under the pointer, on keyboard focus and always without hover
const REVEAL = "opacity-0 group-hover/row:opacity-100 group-focus-visible/row:opacity-100 focus-visible:opacity-100 [@media(hover:none)]:opacity-100";

function RowButton({ icon, label, onClick, className, filled, disabled, ...rest }: { icon: IconName; label: string; onClick: (e: MouseEvent<HTMLButtonElement>) => void; className: string; filled?: boolean; disabled?: boolean } & Pick<ButtonHTMLAttributes<HTMLButtonElement>, "aria-expanded" | "aria-haspopup">) {
  return (
    <button
      type="button"
      {...rest}
      disabled={disabled}
      title={label}
      aria-label={label}
      onClick={(e) => {
        e.stopPropagation();
        onClick(e);
      }}
      className={cx("flex size-5 shrink-0 items-center justify-center rounded-[6px] transition-[opacity,color] duration-150 disabled:pointer-events-none", className)}
    >
      <Icon name={icon} size={14} className={filled ? "fill-current" : undefined} />
    </button>
  );
}

interface RowProps {
  row: PanelRow;
  /** The second line ("" for none) and whether it is the model's description (capped at about 30 characters). */
  second: string;
  describes?: boolean;
  /** Search and saved setups always name their launcher, including touch and truncated names. */
  keepSecond?: boolean;
  /** A mixed disclosure list uses buttons rather than listbox options. */
  asButton?: boolean;
  /** The panel's middle column (#12315): one tighter line, the effort and Fast left to the columns beside it, a check on
   *  the model that runs, a description kept for the tooltip. */
  dense?: boolean;
  /** Why it can't be picked (its tooltip); undefined while it can. */
  blocked?: { reason: string };
  customizable: boolean;
  flyoutOpen: boolean;
  /** The star is filled: a favorite, a recent setup saved as one, or one just saved. */
  starred: boolean;
  /** The star removes (a favorite's own row, a recent row saved as one) rather than saves a copy. */
  starRemoves: boolean;
  /** Everything waits (a pick is on its way). */
  frozen?: boolean;
  onPick: () => void;
  onStar: () => void;
  onCustomize: (row: HTMLElement, toggle: boolean) => void;
  /** ←: opens the flyout and moves the focus into it. */
  onCustomizeKey: (row: HTMLElement) => void;
  onForget?: () => void;
}

export function Row({ row, second, describes, keepSecond, asButton, dense, blocked, customizable, flyoutOpen, starred, starRemoves, frozen, onPick, onStar, onCustomize, onCustomizeKey, onForget }: RowProps) {
  const { model, provider } = row;
  const saved = row.kind === "favorite" || row.kind === "recent";
  const name = row.kind === "typed" ? t("ui-modelpanel.use_typed", { id: model.id }) : row.preset ? row.preset.name : row.kind === "favorite" || row.kind === "recent" ? `${provider.name} · ${model.name}` : model.name;
  // a typed ID's "Use “…”" and a preset (a setup of the profile's, not a copy of one's own) have no star
  const stars = row.kind !== "typed" && row.kind !== "preset";
  const effort = shownEffort(row.setup, model);
  const tier = priceTier(model.price);
  // what the right side says, in words: "Claude Code · High · Fast"
  const speed = row.setup.speed && row.setup.speed !== "standard" ? row.setup.speed : undefined;
  const speedName = speed && speedLabel(model.speeds?.find((choice) => choice.id === speed) ?? { id: speed });
  const speedMark = speedName && (
    <span data-row-speed title={speedName} aria-label={speedName} role="img" className="flex shrink-0 items-center gap-1 text-label text-ink-3">
      <SpeedMark speed={speed} size={11} />
      {speed !== "fast" && speedName}
    </span>
  );
  const setupTitle = [agentName(provider.engine, provider.engineLabel), effort && effortTitle(effort), speedName].filter(Boolean).join(" · ");
  const off = Boolean(blocked) || Boolean(frozen);
  // the running model's row stays reachable while picks are off: its Customize still opens the flyout (effort, Fast), so it
  // is not announced as disabled (its pick is a no-op anyway, and Enter does nothing while blocked)
  const ariaOff = off && !(row.current && customizable && !frozen);
  const starOff = Boolean(frozen) || (Boolean(blocked) && !starRemoves) || row.kind === "typed";
  // the name is cut short (measured again as the panel resizes): its second line becomes the whole ID
  const nameEl = useRef<HTMLSpanElement>(null);
  const [cut, setCut] = useState(false);
  useLayoutEffect(() => {
    const el = nameEl.current;
    if (!el) return;
    const measure = () => {
      const c = el.scrollWidth > el.clientWidth + 1;
      setCut((v) => (v === c ? v : c));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [name]);
  // a typed ID's row is its ID already; a preset's second line describes it
  const hasId = row.kind !== "typed" && row.kind !== "preset";
  const idLine = hasId && cut ? "always" : hasId && !describes && model.id !== name ? "touch" : null;
  const idContext = describes ? provider.name : second || provider.name;
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.target !== e.currentTarget || e.nativeEvent.isComposing) return;
    if (e.key === "ArrowLeft") {
      if (!customizable || frozen) return;
      e.preventDefault();
      onCustomizeKey(e.currentTarget);
      return;
    }
    if (e.key !== "Enter" && e.key !== " ") return;
    e.preventDefault();
    if (off && row.current && customizable && !frozen) onCustomizeKey(e.currentTarget);
    else if (!off) onPick();
  };
  return (
    <div
      role={asButton ? "button" : "option"}
      aria-selected={asButton ? undefined : row.current}
      aria-pressed={asButton ? row.current : undefined}
      aria-disabled={ariaOff || undefined}
      aria-keyshortcuts={customizable ? "ArrowLeft" : undefined}
      tabIndex={ariaOff ? -1 : 0}
      title={blocked?.reason || (dense && describes && second) || undefined}
      aria-description={blocked?.reason || undefined}
      data-panel-row={row.key}
      data-current={row.current || undefined}
      data-open={flyoutOpen || undefined}
      onClick={(e) => {
        if (off && row.current && customizable && !frozen) onCustomize(e.currentTarget, true);
        else if (!off) onPick();
      }}
      onContextMenu={(e) => {
        if (!customizable || frozen) return;
        e.preventDefault();
        onCustomize(e.currentTarget, false);
      }}
      onKeyDown={key}
      className={cx(
        "group/row relative z-[1] flex w-full flex-col rounded-control px-3 text-[14px] leading-[1.43] text-ink outline-none select-none",
        dense ? "py-1.5 [&+[data-panel-row]]:mt-px" : "py-2 [&+[data-panel-row]]:mt-1",
        "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
        row.current && "bg-hover-2 data-[glide]:bg-transparent",
        saved && "min-h-11 justify-center",
        ariaOff || frozen ? "cursor-not-allowed" : "cursor-pointer",
        ariaOff && "opacity-50",
      )}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="flex size-[18px] shrink-0 items-center justify-center text-ink-2">
          <ProviderMark engine={provider.engine} label={provider.engineLabel} size={16} />
        </span>
        <span ref={nameEl} title={row.kind === "typed" ? model.id : cut ? name : undefined} className={cx("min-w-0 truncate leading-5 font-medium", model.custom && stars && "font-mono")}>{name}</span>
        {row.latest && <span data-row-latest className="shrink-0 text-submeta leading-5 text-ink-3">{t("ui-modelpanel.latest")}</span>}
        {tier && (
          <span data-price-tier={tier} aria-label={t("ui-modelpanel.price_tier", { tier })} title={t("ui-modelpanel.price_tier", { tier })} className="shrink-0 text-[11px] leading-none font-semibold tracking-[0.5px] text-ink-2">
            {"$".repeat(tier)}
          </span>
        )}
        {stars && (
          <RowButton
            icon="star"
            filled={starred}
            disabled={starOff}
            label={starRemoves ? t("ui-modelpanel.remove_favorite") : t("ui-modelpanel.save_favorite")}
            onClick={onStar}
            className={starred ? "text-favorite-star" : cx("text-ink-3 hover:text-favorite-star", REVEAL)}
          />
        )}
        {customizable && (
          <RowButton
            icon="sliders"
            data-row-customize=""
            aria-expanded={flyoutOpen}
            aria-haspopup="dialog"
            disabled={frozen}
            label={t("ui-modelpanel.customize")}
            onClick={(e) => {
              const el = (e.currentTarget as HTMLElement).closest<HTMLElement>("[data-panel-row]");
              if (el) onCustomize(el, true);
            }}
            className={flyoutOpen ? "text-ink-2" : cx("text-ink-3 hover:text-ink-2", REVEAL)}
          />
        )}
        {model.custom && row.kind === "model" && onForget && (
          <RowButton icon="x" disabled={frozen} label={t("ui-modelpanel.forget", { id: model.id })} onClick={onForget} className={cx("text-ink-3 hover:text-ink-2", REVEAL)} />
        )}
        {dense ? (
          <span className="ml-auto flex shrink-0 items-center gap-1">
            {speedMark}
            {row.current && <Icon name="check" size={14} stroke={2.5} className="shrink-0 text-ink" />}
          </span>
        ) : (
          <span className="ml-auto flex shrink-0 items-center">
            <span title={setupTitle} data-row-setup className="flex max-w-[118px] shrink-0 items-center gap-1 truncate text-[12px] text-ink-3">
              {effort && <span className="truncate">{effortTitle(effort)}</span>}
              {speedMark}
            </span>
          </span>
        )}
      </div>
      {!saved && second && !(dense && describes) && (keepSecond || idLine !== "always") && (
        <div title={second} className={cx("min-w-0 truncate pt-px pl-[26px] text-[12px] leading-[1.4] text-ink-2", describes && "max-w-[30ch]", !keepSecond && idLine === "touch" && "[@media(hover:none)]:hidden")}>
          {second}
        </div>
      )}
      {!saved && idLine && (
        <div data-row-id={idLine} className={cx("min-w-0 pt-px pl-[26px]", idLine === "touch" && "hidden [@media(hover:none)]:block")}>
          <div title={`${idContext} · ${model.id}`} className="line-clamp-2 font-mono text-xs leading-[1.4] break-all text-ink-2">
            {idContext} · {model.id}
          </div>
        </div>
      )}
      {row.current && model.effortUnavailable && (
        <div className="min-w-0 pt-1 pl-[26px] text-label leading-[1.4] text-ink-3">{model.effortUnavailable}</div>
      )}
    </div>
  );
}
