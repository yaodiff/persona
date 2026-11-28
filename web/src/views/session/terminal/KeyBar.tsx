// The terminal's key bar (iOS TerminalKeyBar): keys a phone's keyboard lacks — and a browser on a phone cannot send —
// in one row that scrolls sideways when it does not fit: ctrl / alt / shift (a tap arms one for the next key, a quick
// second tap locks it, holding keeps it down), esc, tab, a ⌃C that goes out at once, the saved commands, a hidden line
// (LineBox: a password or a token for a prompt in the pane, #14527), copy and paste, then the arrows and return. The keys
// keep the focus in the terminal, so a phone's keyboard stays up.
import { useEffect, useRef, useState } from "react";
import type { ModifierKey, ModifierState, Modifiers } from "../../../lib/terminal";
import { cx } from "../../../ui/atoms";
import { Icon, type IconName } from "../../../ui/icons";
import { LineBox } from "./LineBox";
import { ShortcutsPopover } from "./Shortcuts";
import { t } from "../../../i18n";

/** What the bar drives: the pane's feed, or the full surface's (both answer the same calls). */
export interface KeyFeed {
  snapshot(): { mods: Modifiers };
  pressKey(name: string): void;
  pressBare(name: string): void;
  tapModifier(k: ModifierKey, quick: boolean): void;
  holdModifier(k: ModifierKey): void;
  releaseModifier(k: ModifierKey): void;
  typeText(text: string, run: boolean): void;
  copySelection(): void;
  pasteClipboard(): Promise<void>;
}

/** What the bar shows of it: keys go in only while `control` is "active". */
export interface KeyView {
  control: string;
  mods: Modifiers;
  selection: boolean;
}

const HOLD_MS = 350;
const DOUBLE_TAP_MS = 350;

/** Keeps the focus where it is (the terminal's input): a key on the bar must not take it. */
const keepFocus = (e: React.MouseEvent) => e.preventDefault();

function capClass(state: ModifierState = "off") {
  return cx(
    "relative flex h-8 min-w-8 shrink-0 items-center justify-center whitespace-nowrap rounded-chip px-2 font-mono text-[12.5px] transition-[background-color,color,box-shadow,transform] duration-150 enabled:active:scale-[0.94] disabled:opacity-40",
    state === "locked"
      ? "bg-accent text-white"
      : state === "off"
        ? "text-ink enabled:hover:bg-hover"
        : "bg-accent-tint text-accent-ink shadow-[inset_0_0_0_1.5px_var(--accent)]",
    state === "held" && "scale-[0.94]",
  );
}

function Key({
  label,
  icon,
  title,
  disabled,
  active,
  onPress,
}: {
  label?: string;
  icon?: IconName;
  title: string;
  disabled?: boolean;
  active?: boolean;
  onPress: () => void;
}) {
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      aria-pressed={active}
      disabled={disabled}
      onMouseDown={keepFocus}
      onClick={onPress}
      className={capClass(active ? "once" : "off")}
    >
      {icon ? <Icon name={icon} size={15} /> : label}
    </button>
  );
}

/** ctrl / alt / shift: down and up are tracked by hand, a tap, a quick second tap and a hold all mean something else. */
function ModifierKeyCap({ k, label, name, feed, state, disabled }: { k: ModifierKey; label: string; name: string; feed: KeyFeed; state: ModifierState; disabled: boolean }) {
  const down = useRef<number | null>(null);
  const timer = useRef(0);
  const lastTap = useRef(0);
  useEffect(() => () => clearTimeout(timer.current), []);

  const press = (e: React.PointerEvent) => {
    if (disabled || e.button !== 0) return;
    e.preventDefault();
    (e.currentTarget as HTMLElement).setPointerCapture?.(e.pointerId);
    const at = performance.now();
    down.current = at;
    clearTimeout(timer.current);
    timer.current = window.setTimeout(() => {
      if (down.current === at) feed.holdModifier(k);
    }, HOLD_MS);
  };
  const lift = () => {
    if (down.current === null) return;
    down.current = null;
    clearTimeout(timer.current);
    if (feed.snapshot().mods[k] === "held") return feed.releaseModifier(k);
    const now = performance.now();
    const quick = now - lastTap.current < DOUBLE_TAP_MS;
    lastTap.current = now;
    feed.tapModifier(k, quick);
  };
  const cancel = () => {
    down.current = null;
    clearTimeout(timer.current);
    feed.releaseModifier(k);
  };

  return (
    <button
      type="button"
      title={t("session-terminal-keybar.modifier_hint", { name })}
      aria-label={name}
      aria-pressed={state !== "off"}
      disabled={disabled}
      onMouseDown={keepFocus}
      onPointerDown={press}
      onPointerUp={lift}
      onPointerCancel={cancel}
      onClick={(e) => e.detail === 0 && feed.tapModifier(k, false)} // the keyboard's space / return
      className={capClass(state)}
    >
      {label}
      {state === "locked" && <span aria-hidden className="absolute bottom-1 left-1/2 h-0.5 w-2.5 -translate-x-1/2 rounded-full bg-white" />}
    </button>
  );
}

export function KeyBar({ feed, v, onEditShortcuts }: { feed: KeyFeed; v: KeyView; onEditShortcuts: () => void }) {
  const [shortcuts, setShortcuts] = useState(false);
  const [lineBox, setLineBox] = useState(false);
  const off = v.control !== "active";
  const k = (name: string) => () => feed.pressKey(name);
  useEffect(() => {
    if (off) {
      setShortcuts(false);
      setLineBox(false);
    }
  }, [off]);

  return (
    <div className="relative shrink-0 border-t border-line bg-canvas px-1.5 py-1.5" title={off ? t("session-terminal-keybar.take_control_first") : undefined}>
      {shortcuts && (
        <ShortcutsPopover
          onPick={(text, run) => {
            feed.typeText(text, run);
            if (run) setShortcuts(false);
          }}
          onEdit={() => {
            setShortcuts(false);
            onEditShortcuts();
          }}
          onClose={() => setShortcuts(false)}
        />
      )}
      {lineBox && <LineBox onSend={(line) => feed.typeText(line, true)} onClose={() => setLineBox(false)} />}
      <div role="toolbar" aria-label={t("session-terminal-keybar.title")} className="flex items-center gap-1">
        <div className="flex min-w-0 flex-1 items-center gap-1 overflow-x-auto scrollbar-none">
          <ModifierKeyCap k="ctrl" label="Ctrl" name="Control" feed={feed} state={v.mods.ctrl} disabled={off} />
          <Key label="Esc" title="Esc" disabled={off} onPress={k("esc")} />
          <Key label="Tab" title="Tab" disabled={off} onPress={k("tab")} />
          <Key label="⌃C" title={t("session-terminal-keybar.send_ctrl_c")} disabled={off} onPress={() => feed.pressBare("ctrl+c")} />
          <Key icon="bolt" title={t("session-terminal-keybar.shortcuts")} disabled={off} active={shortcuts} onPress={() => { setLineBox(false); setShortcuts((s) => !s); }} />
          <Key icon="key" title={t("session-terminal-keybar.line")} disabled={off} active={lineBox} onPress={() => { setShortcuts(false); setLineBox((o) => !o); }} />
          <Key icon="copy" title={t("session-terminal-keybar.copy_selection")} disabled={off || !v.selection} onPress={() => feed.copySelection()} />
          <Key icon="paste" title={t("session-terminal-keybar.paste")} disabled={off} onPress={() => void feed.pasteClipboard()} />
          <ModifierKeyCap k="alt" label="Alt" name="Option" feed={feed} state={v.mods.alt} disabled={off} />
          <ModifierKeyCap k="shift" label="⇧" name="Shift" feed={feed} state={v.mods.shift} disabled={off} />
        </div>
        <div className="flex shrink-0 items-center gap-0.5 border-l border-line pl-1">
          <Key icon="arrowLeft" title="←" disabled={off} onPress={k("left")} />
          <Key icon="up" title="↑" disabled={off} onPress={k("up")} />
          <Key icon="arrowDown" title="↓" disabled={off} onPress={k("down")} />
          <Key icon="arrowRight" title="→" disabled={off} onPress={k("right")} />
          <Key icon="enter" title={t("session-terminal-keybar.enter")} disabled={off} onPress={k("enter")} />
        </div>
      </div>
    </div>
  );
}
