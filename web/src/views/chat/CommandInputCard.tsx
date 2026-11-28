// A command an Agent runs that stopped at a prompt (`command_input`, #14527): a sign-in waiting for its code, a login
// for its password, a CLI for a pasted token. The owner types the line here and it goes straight into the command (the
// computer's node types it, never the model), so it is answered without waiting for the Agent's turn. Not part of the
// chat's first screen: PermissionCard (its lazy chunk, inside PermissionCardFrame) hands it such a card.
import { useLayoutEffect, useRef, useState } from "react";
import { t } from "../../i18n";
import { clickLink, linkHref } from "../../lib/links";
import type { Interaction } from "../../lib/types";
import { Button, IconButton, SECRET_INPUT } from "../../ui/atoms";
import { useChatCardTapGuard } from "./card-tap-guard";
import { Card, CODE_BOX, resolve, useCardKeys } from "./Interactions";

/** A command-input card's command, prompt and the output before it (untrimmed, as the command wrote it), and whether the
 *  prompt reads as secret, so the line is hidden until Show. */
export function commandInputInput(input: Record<string, unknown> | undefined): { command: string; prompt: string; output: string; masked: boolean } {
  const text = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  return { command: text(input?.command), prompt: text(input?.prompt), output: typeof input?.output === "string" ? input.output : "", masked: input?.masked === true };
}

/** A web address in the command's output: up to a space, a quote or a closing bracket. */
const URL_RE = /https?:\/\/[^\s<>"'`)\]]+/g;

/** The output's lines with each web address a link, opened where links open (lib/links.ts): the sign-in page a login
 *  prints is one tap away. Everything else is the command's text as it is. */
export function OutputLines({ text }: { text: string }) {
  const parts: React.ReactNode[] = [];
  let at = 0;
  for (const m of text.matchAll(URL_RE)) {
    const url = m[0].replace(/[.,;:!?]+$/, "");
    const start = m.index ?? 0;
    parts.push(text.slice(at, start));
    parts.push(
      <a key={start} href={linkHref(url)} target="_blank" rel="noreferrer noopener" onClick={(e) => clickLink(e, url)} className="text-accent-ink underline decoration-1 underline-offset-2 [overflow-wrap:anywhere]">
        {url}
      </a>,
    );
    at = start + url.length;
  }
  parts.push(text.slice(at));
  return <>{parts}</>;
}

/**
 * What the command waits for: its prompt over the field, the lines it printed before it, and Not now / Send. The line
 * lives only in this card's state and its input: emptied as Send sends it, on Not now, and when the card goes (a card
 * with another id is a new CommandInputCard). It is hidden while it is typed when the prompt reads as secret, with Show
 * to check it; never offered to a password manager, never sent by Enter (as on the password card): only Send types it.
 */
export function CommandInputCard({ ix }: { ix: Interaction }) {
  const { command, prompt, output, masked } = commandInputInput(ix.tool_input);
  const [line, setLine] = useState("");
  const [shown, setShown] = useState(!masked);
  const [busy, setBusy] = useState<"send" | "skip" | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const outputBox = useRef<HTMLPreElement>(null);
  const keys = useCardKeys();
  const tapGuard = useChatCardTapGuard(ix);
  // a field taken off the page keeps its value until collected, and Firefox offers to save a password field removed
  // while it holds one: it is emptied before React removes it
  useLayoutEffect(() => {
    const el = input.current;
    return () => {
      if (el) el.value = "";
    };
  }, []);
  // a long output scrolls in its box: its end, the lines just before the prompt, is what is in view
  useLayoutEffect(() => {
    const el = outputBox.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [output]);
  const field = ix.fields?.find((f) => f.key === "input");
  const label = prompt || field?.label?.trim() || t("chat-command-input.field");
  const send = async (body: Record<string, unknown>, key: "send" | "skip") => {
    setLine("");
    setBusy(key);
    await resolve(ix, body);
    setBusy(null);
  };
  return (
    <Card
      kicker={t("chat-command-input.kicker")}
      title={t("chat-command-input.title")}
      cardKeys={keys}
      tapGuard={tapGuard}
      footer={
        <>
          <Button tone="quiet" busy={busy === "skip"} disabled={busy !== null} onClick={() => void send({ allow: false }, "skip")}>
            {t("chat-command-input.not_now")}
          </Button>
          <Button tone="primary" busy={busy === "send"} disabled={busy !== null || line === ""} onClick={() => void send({ allow: true, values: { input: line } }, "send")}>
            {t("chat-command-input.send")}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-2">
        {/* the command on one line (all of it on hover): the card's room goes to what it printed and the field */}
        {command && (
          <div className="flex min-w-0 items-baseline gap-1.5 text-submeta">
            <span className="shrink-0 font-medium text-ink-3">{t("chat-command-input.command")}</span>
            <code data-command-input-command title={command} className="min-w-0 truncate font-mono text-ink-2">{command}</code>
          </div>
        )}
        {output && (
          <pre ref={outputBox} data-command-input-output className={CODE_BOX}>
            <OutputLines text={output} />
          </pre>
        )}
        <div className="flex flex-col gap-1">
          <label htmlFor={`command-input-${ix.id}`} className="text-meta font-medium text-ink">{label}</label>
          <div className="flex items-center gap-1.5">
            {/* The line a command asks for, not this site's password: "one-time-code" keeps browsers from offering to
                save it or to suggest one; no name, no form. Enter only puts the keyboard away. */}
            <input
              id={`command-input-${ix.id}`}
              ref={input}
              type={shown ? "text" : "password"}
              value={line}
              onChange={(e) => setLine(e.target.value)}
              onKeyDown={(e) => {
                if (e.key !== "Enter") return;
                e.preventDefault();
                e.currentTarget.blur();
              }}
              placeholder={t("chat-command-input.placeholder")}
              disabled={busy !== null}
              {...SECRET_INPUT}
              autoComplete="one-time-code"
              autoCapitalize="off"
              autoCorrect="off"
              enterKeyHint="done"
              className="h-8 w-full min-w-0 rounded-control bg-surface px-2.5 text-meta text-ink shadow-btn outline-none placeholder:text-ink-3"
            />
            <IconButton icon={shown ? "eyeOff" : "eye"} label={shown ? t("chat-command-input.hide") : t("chat-command-input.show")} pressed={shown}
              disabled={busy !== null} onClick={() => setShown((v) => !v)} />
          </div>
          <span className="text-label leading-snug text-ink-3">{t("chat-command-input.note")}</span>
        </div>
      </div>
    </Card>
  );
}
