// Claude Code asking on its terminal to run a command (#5266), as a screen card in plain words: "Waiting for your
// approval", "Claude wants to run a command", the agent's words, the command, the warning plainly with Claude Code's own
// under "Show original", the countdown running, and Deny / Allow for the prompt's No / Yes (a prompt with more choices
// keeps its numbered rows). Off the first screen and the idle tier: Interactions.tsx loads it only when a command card
// appears (lazyPage) and draws the terminal's question until it is in. The iPhone's ScreenForm says the same (apple/Persona/Views/Interactions/ScreenCommandAsk.swift).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { allowDeny, commandAsk, type CommandAsk } from "../../lib/command-ask";
import { countdownLine, originalWords, riskLine } from "../../lib/command-ask-lines";
import { agentLabel } from "../../lib/mentions";
import type { TapGuardHandle } from "../../lib/tap-guard";
import type { Interaction } from "../../lib/types";
import { BUSY_MOTION, spinStyle } from "../../lib/busy-motion";
import { Button, cx, Kbd } from "../../ui/atoms";
import { Icon } from "../../ui/icons";
import { DigitHint } from "./Choice";
import { Card, CloseBlockedAgent, OptionLabel, resolve, useCardKeys } from "./Interactions";
import { useChatCardTapGuard } from "./card-tap-guard";

export interface CommandScreenCardProps {
  ix: Interaction;
  /** Controlled answer handler replacing the default `resolve(ix, body)` network and toast path. */
  onResolve?: (body: Record<string, unknown>) => Promise<unknown> | void;
  /** Controlled hand-back handler (`null` hides Hand back; omitted routes through `onResolve` or default `resolve`). */
  onDismiss?: (() => Promise<unknown> | void) | null;
  /** Explicit tap-guard handle (`null` disables the default chat tap-guard surface; omitted uses `useChatCardTapGuard`). */
  tapGuard?: TapGuardHandle | null;
  /** Controlled close-blocked-agent action (`null` hides the control without reading the store; omitted renders default `CloseBlockedAgent`). */
  onCloseBlockedAgent?: (() => Promise<unknown> | void) | null;
}

export function CommandScreenCard(props: CommandScreenCardProps) {
  if (props.tapGuard !== undefined) {
    return <CommandScreenCardView {...props} resolvedTapGuard={props.tapGuard ?? undefined} />;
  }
  return <DefaultCommandScreenCard {...props} />;
}

function DefaultCommandScreenCard(props: CommandScreenCardProps) {
  const tapGuard = useChatCardTapGuard(props.ix);
  return <CommandScreenCardView {...props} resolvedTapGuard={tapGuard} />;
}

function CommandScreenCardView({
  ix,
  onResolve,
  onDismiss,
  onCloseBlockedAgent,
  resolvedTapGuard,
}: CommandScreenCardProps & { resolvedTapGuard?: TapGuardHandle }) {
  const d = ix.dialog;
  const ask = commandAsk(d) ?? { command: d?.command ?? "", deadlineAllows: false };
  const pair = allowDeny(d);
  const options = d?.options ?? [];
  const [busy, setBusy] = useState<string | null>(null);
  const pending = useRef(false);
  const run = async (key: string, action: () => Promise<unknown> | void) => {
    if (pending.current) return;
    pending.current = true;
    setBusy(key);
    try {
      await action();
    } finally {
      pending.current = false;
      setBusy(null);
    }
  };
  const go = (body: Record<string, unknown>, key: string) =>
    run(key, () => (onResolve ? onResolve(body) : resolve(ix, body)));
  const handleDismiss =
    onDismiss === null || (onDismiss === undefined && ix.no_hand_back)
      ? undefined
      : () => void (onDismiss ? run("dismiss", onDismiss) : go({ dismiss: true }, "dismiss"));
  // digits pick the prompt's options by place, as the terminal reads them; Enter allows, as on the permission card, and
  // Deny stays an explicit press
  const keys = useCardKeys({
    digit: (n) => {
      const o = options[n - 1];
      if (!o || busy !== null) return false;
      void go({ option: o.key }, o.key);
      return true;
    },
    enter: () => {
      if (busy !== null || !pair) return false;
      void go({ option: pair.allow.key }, pair.allow.key);
    },
  });
  return (
    <Card
      kicker={t("chat-interactions.permission_kicker")}
      title={t("chat-commandask.title", { agent: agentLabel({ agent: ix.agent, agent_label: ix.agent_label }) })}
      cardKeys={keys}
      tapGuard={resolvedTapGuard}
      onDismiss={handleDismiss}
      busy={busy !== null}
      footer={<div className="flex w-full min-w-0 flex-col gap-2">
        {pair ? (
          <span className="flex w-full min-w-0 items-center justify-between gap-2">
            <Button tone="quiet" busy={busy === pair.deny.key} disabled={busy !== null} onClick={() => void go({ option: pair.deny.key }, pair.deny.key)}>
              {t("chat-interactions.deny")}
            </Button>
            <Button tone="primary" busy={busy === pair.allow.key} disabled={busy !== null} onClick={() => void go({ option: pair.allow.key }, pair.allow.key)}>
              {t("chat-interactions.allow")}
              {keys.focused && <Kbd>Enter</Kbd>}
            </Button>
          </span>
        ) : (
          <span className="text-[11.5px] text-ink-3">{t("chat-interactions.screen_hint")}</span>
        )}
        {onCloseBlockedAgent === undefined ? (
          <CloseBlockedAgent ix={ix} disabled={busy !== null} />
        ) : onCloseBlockedAgent ? (
          <ControlledCloseBlockedAgent disabled={busy !== null} onClose={onCloseBlockedAgent} />
        ) : null}
      </div>}
    >
      <CommandAskBody ask={ask} />
      {!pair && (
        <div className="flex flex-col gap-0.5">
          {options.map((o, i) => (
            <button
              key={o.key}
              type="button"
              disabled={busy !== null}
              data-tap-answer
              onClick={() => void go({ option: o.key }, o.key)}
              className="touch-hit -mx-1.5 flex items-start gap-2 rounded-control px-1.5 py-1.5 text-left transition-colors hover:bg-hover disabled:opacity-60"
            >
              <span className="mt-[1px] flex h-4.5 min-w-4.5 shrink-0 items-center justify-center rounded-[5px] bg-field px-1 font-mono text-[10.5px] text-ink-2 shadow-hairline">{o.key}</span>
              {keys.focused && i < 9 && <DigitHint n={i + 1} />}
              <span className="min-w-0">
                <OptionLabel label={o.label} />
                {o.detail && <span className="block text-[12px] text-ink-3">{o.detail}</span>}
              </span>
              {busy === o.key && <span className={`ml-auto mt-1 size-3 shrink-0 rounded-full border-[1.5px] border-line-strong border-t-ink-2 ${BUSY_MOTION}`} style={spinStyle(700)} />}
            </button>
          ))}
        </div>
      )}
    </Card>
  );
}

function ControlledCloseBlockedAgent({ disabled, onClose }: { disabled: boolean; onClose: () => Promise<unknown> | void }) {
  const [confirming, setConfirming] = useState(false);
  const [closing, setClosing] = useState(false);
  const pending = useRef(false);
  const close = async () => {
    if (pending.current) return;
    pending.current = true;
    setClosing(true);
    try {
      await onClose();
      setConfirming(false);
    } finally {
      pending.current = false;
      setClosing(false);
    }
  };
  if (!confirming) return <Button className="min-h-11" disabled={disabled} onClick={() => setConfirming(true)}>{t("chat-interactions.close_agent")}</Button>;
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <p className="text-ink-2">{t("chat-interactions.close_agent_hint")}</p>
      <div className="flex flex-wrap gap-2">
        <Button className="min-h-11" disabled={disabled || closing} onClick={() => setConfirming(false)}>{t("chat-interactions.close_agent_cancel")}</Button>
        <Button className="min-h-11" tone="danger" busy={closing} disabled={disabled || closing} onClick={() => void close()}>{t("chat-interactions.close_agent_confirm")}</Button>
      </div>
    </div>
  );
}

/** A command's permission prompt read off the terminal, in plain words (#5266): the agent's own words for it, the command
 *  (a long one folded until "Show all"), the warning in plain words with Claude Code's own under "Show original", and the
 *  countdown running. The iPhone's ScreenCommandAskBody says the same. */
function CommandAskBody({ ask }: { ask: CommandAsk }) {
  const [all, setAll] = useState(false);
  const [original, setOriginal] = useState(false);
  // "Show all" only when the four rows cut something at this width: measured, as the box's width decides it
  const box = useRef<HTMLPreElement>(null);
  const [cut, setCut] = useState(false);
  useLayoutEffect(() => {
    const el = box.current;
    if (!el || all) return;
    const measure = () => setCut(el.scrollHeight > el.clientHeight + 1);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ask.command, all]);
  const toggle = "touch-hit mt-1 self-start text-[11.5px] font-medium whitespace-nowrap text-ink-3 transition-colors hover:text-ink";
  return (
    <div className="mb-1 flex flex-col gap-2">
      {ask.description && <p className="text-[13px] leading-relaxed whitespace-pre-wrap text-ink">{ask.description}</p>}
      <div className="flex flex-col">
        <div className="mb-1 text-[11px] font-medium text-ink-3">{t("chat-interactions.field_command")}</div>
        <div className="rounded-[8px] bg-inset px-2.5 py-2 shadow-hairline">
          <pre ref={box} data-screen-command className={cx(COMMAND_TEXT, all ? "max-h-72 overflow-auto" : "line-clamp-4")}>{ask.command}</pre>
        </div>
        {(cut || all) && (
          <button type="button" aria-expanded={all} onClick={() => setAll((v) => !v)} className={toggle}>
            {t(all ? "chat-interactions.collapse" : "chat-commandask.show_all")}
          </button>
        )}
      </div>
      {riskLine(ask) && (
        <div className="flex flex-col">
          <p data-screen-risk className="flex items-start gap-1.5 text-[12.5px] leading-snug font-medium text-orange">
            <Icon name="warning" size={13} className="mt-px shrink-0" />
            {riskLine(ask)}
          </p>
          {originalWords(ask) && (
            <button type="button" aria-expanded={original} onClick={() => setOriginal((v) => !v)} className={toggle}>
              {t(original ? "chat-commandask.hide_original" : "chat-commandask.show_original")}
            </button>
          )}
          {original && <p className="mt-1 text-[12px] leading-relaxed whitespace-pre-wrap text-ink-3">{originalWords(ask)}</p>}
        </div>
      )}
      {ask.deadlineAt !== undefined && <Countdown ask={ask} />}
    </div>
  );
}

/** A command in its box (the code boxes' surface around it): code type, wrapped, a long word too. The clamp is on the
 *  text, not the padded box, so no fifth row shows in the padding. */
const COMMAND_TEXT = "font-mono text-[11.5px] leading-[1.6] whitespace-pre-wrap break-words text-ink-2";

/** The countdown Claude Code runs before it answers the prompt itself, drawn again once a second until it runs out
 *  (one timeout at a time, on the second's edge), then what happens. */
function Countdown({ ask }: { ask: CommandAsk }) {
  const [now, setNow] = useState(() => Date.now());
  const end = ask.deadlineAt;
  useEffect(() => {
    if (end === undefined || now >= end) return;
    const id = setTimeout(() => setNow(Date.now()), (end - now) % 1000 || 1000);
    return () => clearTimeout(id);
  }, [now, end]);
  return (
    <p data-screen-countdown role="timer" className="flex items-center gap-1.5 text-[12px] font-medium text-ink-3 tabular-nums">
      <Icon name="clock" size={12} stroke={2.2} className="shrink-0" />
      {countdownLine(ask, now)}
    </p>
  );
}
